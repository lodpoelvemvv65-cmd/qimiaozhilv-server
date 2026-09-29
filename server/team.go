package main

import (
	"log"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// team.go：组队系统（20154-20168 段，全量同步模型）。
//
// 服务器内存态（不持久化）：
//   teamMu  保护 teams 与各 session.teamID
//   teams   teamId → *teamState（teamId 由 teamSeq 自增生成）
//   session.teamID  玩家所属队伍 id（0 = 未入队）
//
// 协议要点（见 文档/20-组队系统.md）：
//   - 邀请/申请只推异步通知：C2M_RequestTeam → 目标收 M2C_RequestList(20158)；
//     C2M_InviteTeam → 目标收 M2C_InviteList(20159)；TimeOut=30 秒。
//   - 同意/拒绝统一回 C2M_HandleTeam{HandleInfo{Id=对方, Bool}, IsRequest}。
//     IsRequest 语义（HotfixView.dll SendHandleInviteTeamEvent，IL 实证）：
//       IsRequest=true  = 队长/被申请方处理"申请入队"条(type=1) → 申请者(Id)加入我的队伍；
//       IsRequest=false = 被邀请方处理"组队邀请"条(type=0) → 我加入邀请者(Id)的队伍。
//     即：队长始终是"被申请/发出邀请"的一方（同意后队伍 LeaderId 保持不变）。
//   - 队伍任何变化后向所有成员推 M2C_TeamMember(20162){LeaderId, UnitIds 全量}，
//     客户端 TeamComponent 清空重建。
//   - M2C_TeamMember 为 protoc 生成消息（LeaderId tag1 + UnitIds tag2 已含），
//     直接用 sendPush。

var (
	teamMu  sync.Mutex
	teams   = make(map[int64]*teamState) // teamId → 队伍
	teamSeq int64                        // 队伍 id 发生器
)

// The client has five formation slots and its battle HP difficulty table has
// exactly five entries (solo plus four teammates). Advertising a sixth member
// makes the formation UI enumerate a unit for which no battle slot exists.
const maxTeamMembers = 5

// teamState：一个队伍的服务器内存态。
type teamState struct {
	LeaderId int64   // 队长 playerID
	Members  []int64 // 成员 playerID 列表（首个 = 队长/转让后的新队长）
}

// teamOf 返回 pid 所属队伍（扫描 teams；无则 nil）。调用方须持有 teamMu。
func (s *Server) teamOf(pid int64) *teamState {
	_, t := s.teamEntryOf(pid)
	return t
}

// teamEntryOf 返回 (teamId, team)；无则 (0, nil)。调用方须持有 teamMu。
func (s *Server) teamEntryOf(pid int64) (int64, *teamState) {
	for id, t := range teams {
		for _, m := range t.Members {
			if m == pid {
				return id, t
			}
		}
	}
	return 0, nil
}

// leaveTeamOnClose：玩家下线时从队伍移除并通知剩余在线成员。
// 注意：由 onClose（持 s.mu 写锁）调用，推送不得同步执行（findChannelByPlayerID
// 会取 s.mu 读锁 → 死锁），故用 AfterFunc 延迟到 s.mu 释放后再推。
func (s *Server) leaveTeamOnClose(playerID int64) {
	if playerID <= 0 {
		return
	}
	teamMu.Lock()
	teamID, t := s.teamEntryOf(playerID)
	if t == nil {
		teamMu.Unlock()
		return
	}
	members := t.Members
	for i, m := range members {
		if m == playerID {
			members = append(members[:i], members[i+1:]...)
			break
		}
	}
	t.Members = members
	if len(members) == 0 {
		delete(teams, teamID)
		teamMu.Unlock()
		return
	}
	if t.LeaderId == playerID {
		t.LeaderId = members[0] // 队长下线 → 转让给首个剩余成员
	}
	leader := t.LeaderId
	ids := append([]int64(nil), members...)
	teamMu.Unlock()

	time.AfterFunc(50*time.Millisecond, func() {
		for _, id := range ids {
			if mc := s.findChannelByPlayerID(id); mc != nil {
				s.sendTeamMember(mc, leader, ids)
			}
		}
	})
	log.Printf("player %d left team on disconnect (team %d, remaining %d)", playerID, teamID, len(ids))
}

// sendTeamMember 给单个 channel 推送一条队伍全量同步（M2C_TeamMember 20162）。
func (s *Server) sendTeamMember(ch *channel, leaderID int64, unitIDs []int64) {
	s.sendPush(ch, protocol.OpM2C_TeamMember, &protocol.M2C_TeamMember{
		LeaderId: leaderID,
		UnitIds:  unitIDs,
		ActorId:  ch.session.playerID,
	})
}

// pushTeamMember copies team state under teamMu, then resolves connections
// and sends the snapshot after releasing the lock.
func (s *Server) pushTeamMember(ch *channel) {
	if ch == nil || ch.session == nil {
		return
	}
	pid := ch.session.playerID
	teamMu.Lock()
	// Resolve by player id instead of trusting the session's cached teamID. A
	// reconnect/map restore can briefly carry a stale zero or old team id while
	// the authoritative in-memory team table is already updated.
	_, t := s.teamEntryOf(pid)
	if t == nil {
		teamMu.Unlock()
		s.sendTeamMember(ch, pid, []int64{pid})
		return
	}
	leader := t.LeaderId
	ids := append([]int64(nil), t.Members...)
	teamMu.Unlock()
	s.sendTeamSnapshot(leader, ids)
}

// teamSnapshotFor returns the subset of a party whose scene Units are already
// available to recipient. OperaComponent.ClickTarget dereferences every id in
// TeamMember without a nil check, so advertising an online member from another
// scene (or a member that has not entered the game yet) makes all field clicks
// throw a client-side NullReferenceException before C2M_ClickMapUnit is sent.
func (s *Server) teamSnapshotFor(recipient *channel, leader int64, ids []int64) (int64, []int64) {
	if recipient == nil || recipient.session == nil || recipient.session.playerID <= 0 {
		return leader, nil
	}
	available := make([]int64, 0, len(ids))
	for _, id := range ids {
		member := s.findChannelByPlayerID(id)
		if member == nil || member.session == nil || member.session.playerID <= 0 {
			continue
		}
		if !sameSceneMapID(recipient.session.mapID, member.session.mapID) {
			continue
		}
		available = append(available, id)
	}
	if !teamContains(available, recipient.session.playerID) {
		available = append([]int64{recipient.session.playerID}, available...)
	}
	// The client looks up LeaderId before it looks up the member list. If the
	// real leader is in another scene, use a temporary self-only team until the
	// party is co-located again; a later snapshot restores the real leader.
	if !teamContains(available, leader) {
		return recipient.session.playerID, []int64{recipient.session.playerID}
	}
	return leader, available
}

func (s *Server) sendTeamSnapshot(leader int64, ids []int64) {
	for _, id := range ids {
		if member := s.findChannelByPlayerID(id); member != nil {
			snapshotLeader, snapshotIDs := s.teamSnapshotFor(member, leader, ids)
			if len(snapshotIDs) > 0 {
				s.sendTeamMember(member, snapshotLeader, snapshotIDs)
			}
		}
	}
}

// changeMapForTeamLeader keeps an online party together when its leader uses
// a portal. The client only sends C2M_RequestEnterMap from the leader; without
// this server-side fan-out the leader enters the dungeon while teammates stay
// in the old scene and can never satisfy the party battle preconditions.
func (s *Server) changeMapForTeamLeader(ch *channel, mapID int32, x, y float32) {
	if ch == nil || ch.session == nil {
		return
	}
	pid := ch.session.playerID
	teamMu.Lock()
	t := s.teamOf(pid)
	if t == nil || t.LeaderId != pid {
		teamMu.Unlock()
		s.changeMap(ch, mapID, x, y)
		return
	}
	ids := append([]int64(nil), t.Members...)
	leader := t.LeaderId
	teamMu.Unlock()

	for _, id := range ids {
		member := s.findChannelByPlayerID(id)
		if member == nil || member.session == nil {
			continue
		}
		// A member already in a battle must finish that battle independently;
		// moving its scene underneath the combat state corrupts both clients.
		member.session.battleMu.Lock()
		inBattle := member.session.battle != nil
		member.session.battleMu.Unlock()
		if inBattle {
			continue
		}
		s.changeMap(member, mapID, x, y)
	}
	// changeMap sends UnitsInMap/EnterMap while the party is being moved. Send
	// the team state only after all those writes have been queued, otherwise the
	// client can rebuild TeamComponent with ids whose Unit has not been created.
	s.sendTeamSnapshot(leader, ids)
}

// teamContains 判断 pid 是否在成员列表内。
func teamContains(members []int64, pid int64) bool {
	for _, m := range members {
		if m == pid {
			return true
		}
	}
	return false
}

// onRequestTeam：20154 → 20155。申请加入 TargetId 的队伍（推 20158 给目标）。
// onRequestTeam：20154 → 20155。申请加入 TargetId 的队伍。
// 目标可在线任意玩家：目标有队伍 → 申请加入其队伍（队长同意后入队）；
// 目标无队伍 → 目标收到申请条，同意后建队（LeaderId=目标）收我入队。
func (s *Server) onRequestTeam(ch *channel, req *protocol.C2M_RequestTeam) proto.Message {
	resp := &protocol.M2C_RequestTeam{RpcId: req.RpcId}
	ss := ch.session
	if req.TargetId == ss.playerID {
		resp.Message = "不能申请自己的队伍"
		return resp
	}
	target := s.findChannelByPlayerID(req.TargetId)
	if target == nil {
		resp.Message = "玩家不在线"
		return resp
	}
	if !sameMapSession(ss, target.session) {
		resp.Message = "目标不在当前场景"
		return resp
	}
	teamMu.Lock()
	_, targetTeam := s.teamEntryOf(target.session.playerID)
	targetFull := targetTeam != nil && len(targetTeam.Members) >= maxTeamMembers
	teamMu.Unlock()
	if targetFull {
		resp.Message = "对方队伍已满，不能进队"
		return resp
	}
	s.sendPush(target, protocol.OpM2C_RequestList, &protocol.M2C_RequestList{
		UnitId: ss.playerID,
		// TeamRequestUI.TweenValue interprets this field as milliseconds.
		TimeOut: 30000,
		ActorId: req.TargetId,
	})
	log.Printf("[S=%d] request team from %d to %d", ch.id, ss.playerID, req.TargetId)
	return resp
}

// onInviteTeam：20156 → 20157。邀请 TargetId 组队（推 20159 给目标）。
// 邀请方可有队伍（队长拉人）；仅拒绝：目标已有队伍 / 目标就是自己。
func (s *Server) onInviteTeam(ch *channel, req *protocol.C2M_InviteTeam) proto.Message {
	resp := &protocol.M2C_InviteTeam{RpcId: req.RpcId}
	ss := ch.session
	if req.TargetId == ss.playerID {
		resp.Message = "不能邀请自己"
		return resp
	}
	target := s.findChannelByPlayerID(req.TargetId)
	if target == nil {
		resp.Message = "玩家不在线"
		return resp
	}
	if !sameMapSession(ss, target.session) {
		resp.Message = "目标不在当前场景"
		return resp
	}
	teamMu.Lock()
	_, inviterTeam := s.teamEntryOf(ss.playerID)
	targetTeamID, targetTeam := s.teamEntryOf(target.session.playerID)
	targetHasTeam := targetTeamID != 0 && targetTeam != nil
	inviterIsLeader := inviterTeam == nil || inviterTeam.LeaderId == ss.playerID
	inviterFull := inviterTeam != nil && len(inviterTeam.Members) >= maxTeamMembers
	teamMu.Unlock()
	if !inviterIsLeader {
		resp.Message = "只有队长可以邀请"
		return resp
	}
	if inviterFull {
		resp.Message = "队伍已满，不能邀请"
		return resp
	}
	if targetHasTeam {
		resp.Message = "对方已有队伍"
		return resp
	}
	s.sendPush(target, protocol.OpM2C_InviteList, &protocol.M2C_InviteList{
		UnitId: ss.playerID,
		// TeamRequestUI.TweenValue interprets this field as milliseconds.
		TimeOut: 30000,
		ActorId: req.TargetId,
	})
	log.Printf("[S=%d] invite team from %d to %d", ch.id, ss.playerID, req.TargetId)
	return resp
}

// onHandleTeam：20160 → 20161。处理收到的申请/邀请（同意/拒绝）。
//
// 同意时的队长判定（文档 20 §2.2，IL 实证）：
//
//	IsRequest=true  = 队长/被申请方同意申请 → 申请者(Id)加入我的队伍 → LeaderId=我；
//	IsRequest=false = 被邀请方同意邀请 → 我加入邀请者(Id)的队伍 → LeaderId=Id。
//
// 入队后向所有成员推 20162 全量同步。
func (s *Server) onHandleTeam(ch *channel, req *protocol.C2M_HandleTeam) proto.Message {
	resp := &protocol.M2C_HandleTeam{RpcId: req.RpcId}
	ss := ch.session
	hi := req.HandleInfo
	if hi == nil {
		resp.Message = "参数错误"
		return resp
	}
	other := hi.Id
	if other == ss.playerID {
		resp.Message = "参数错误"
		return resp
	}
	target := s.findChannelByPlayerID(other)
	if target == nil {
		resp.Message = "对方不在线"
		return resp
	}
	if !sameMapSession(ss, target.session) {
		resp.Message = "目标不在当前场景"
		return resp
	}
	if !hi.Bool {
		// 拒绝：待处理状态在客户端本地，服务器无需要清理的状态
		log.Printf("[S=%d] handle team id=%d reject isRequest=%v", ch.id, other, req.IsRequest)
		return resp
	}

	var leader, joiner int64
	if req.IsRequest {
		// 队长（ch）同意申请：申请者（other）加入 ch 的队伍
		leader, joiner = ss.playerID, other
	} else {
		// 被邀请方（ch）同意邀请：ch 加入邀请者（other）的队伍
		leader, joiner = other, ss.playerID
	}
	leaderCh := target
	if leader == ss.playerID {
		leaderCh = ch
	}

	teamMu.Lock()
	if _, existing := s.teamEntryOf(joiner); existing != nil {
		teamMu.Unlock()
		resp.Message = "对方已有队伍"
		return resp
	}
	tid := leaderCh.session.teamID
	t := teams[tid]
	if t == nil {
		teamSeq++
		tid = teamSeq
		t = &teamState{LeaderId: leader, Members: []int64{leader}} // 新建队伍须含队长
		teams[tid] = t
	}
	if !teamContains(t.Members, joiner) && len(t.Members) >= maxTeamMembers {
		teamMu.Unlock()
		resp.Message = "队伍已满，不能进队"
		return resp
	}
	t.LeaderId = leader
	if !teamContains(t.Members, joiner) {
		t.Members = append(t.Members, joiner)
	}
	ch.session.teamID = tid
	target.session.teamID = tid
	// 收集成员快照（锁内），推送放锁内（SendToChannel 只写 socket）
	ids := append([]int64(nil), t.Members...)
	teamLeader := t.LeaderId
	teamMu.Unlock()
	s.sendTeamSnapshot(teamLeader, ids)
	log.Printf("[S=%d] handle team id=%d agree isRequest=%v team=%d members=%v",
		ch.id, other, req.IsRequest, tid, ids)
	return resp
}

// onTransferTeamLeader：20163 → 20164。转让队长（仅队长）。
func (s *Server) onTransferTeamLeader(ch *channel, req *protocol.C2M_TransferTeamLeader) proto.Message {
	resp := &protocol.M2C_TransferTeamLeader{RpcId: req.RpcId}
	ss := ch.session
	teamMu.Lock()
	t := s.teamOf(ss.playerID)
	if t == nil {
		teamMu.Unlock()
		resp.Message = "没有队伍"
		return resp
	}
	if t.LeaderId != ss.playerID {
		teamMu.Unlock()
		resp.Message = "只有队长可以转让"
		return resp
	}
	if !teamContains(t.Members, req.UnitId) {
		teamMu.Unlock()
		resp.Message = "目标不在队伍中"
		return resp
	}
	t.LeaderId = req.UnitId
	ids := append([]int64(nil), t.Members...)
	teamMu.Unlock()
	s.sendTeamSnapshot(req.UnitId, ids)
	log.Printf("[S=%d] transfer team leader %d -> %d members=%v", ch.id, ss.playerID, req.UnitId, ids)
	return resp
}

// onQuitTeam：20165 → 20166。退出队伍。队长退出时转让给剩余首个成员；
// 队伍清空则解散。剩余成员收 20162，退队者自己收单人队伍。
func (s *Server) onQuitTeam(ch *channel, req *protocol.C2M_QuitTeam) proto.Message {
	resp := &protocol.M2C_QuitTeam{RpcId: req.RpcId}
	ss := ch.session
	pid := ss.playerID
	teamMu.Lock()
	t := s.teamOf(pid)
	if t == nil {
		teamMu.Unlock()
		log.Printf("[S=%d] quit team no team", ch.id)
		// PK creates a temporary enemy Team on the client.  Even when the
		// server has no persistent team, refresh the own singleton so stale
		// team-head entries are removed after the duel result.
		s.sendTeamMember(ch, pid, []int64{pid})
		return resp
	}
	teamID := ss.teamID
	members := t.Members[:0]
	for _, m := range t.Members {
		if m != pid {
			members = append(members, m)
		}
	}
	t.Members = members
	ss.teamID = 0
	var leader int64
	var remaining []int64
	if len(t.Members) == 0 {
		delete(teams, teamID)
	} else {
		if t.LeaderId == pid {
			t.LeaderId = t.Members[0]
		}
		leader = t.LeaderId
		remaining = append([]int64(nil), t.Members...)
	}
	teamMu.Unlock()
	if len(remaining) > 0 {
		s.sendTeamSnapshot(leader, remaining)
	}
	s.sendTeamMember(ch, pid, []int64{pid})
	log.Printf("[S=%d] quit team %d members=%v", ch.id, teamID, t.Members)
	return resp
}

// onKickoutTeam：20167 → 20168。踢出队员（仅队长）。
// 剩余成员收 20162，被踢者收单人队伍。
func (s *Server) onKickoutTeam(ch *channel, req *protocol.C2M_KickoutTeam) proto.Message {
	resp := &protocol.M2C_KickoutTeam{RpcId: req.RpcId}
	ss := ch.session
	kicked := s.findChannelByPlayerID(req.TargetId)
	teamMu.Lock()
	t := s.teamOf(ss.playerID)
	if t == nil {
		teamMu.Unlock()
		resp.Message = "没有队伍"
		return resp
	}
	if t.LeaderId != ss.playerID {
		teamMu.Unlock()
		resp.Message = "只有队长可以踢人"
		return resp
	}
	if req.TargetId == ss.playerID {
		teamMu.Unlock()
		resp.Message = "不能踢自己"
		return resp
	}
	if !teamContains(t.Members, req.TargetId) {
		teamMu.Unlock()
		resp.Message = "目标不在队伍中"
		return resp
	}
	members := t.Members[:0]
	for _, m := range t.Members {
		if m != req.TargetId {
			members = append(members, m)
		}
	}
	t.Members = members
	if kicked != nil {
		kicked.session.teamID = 0
	}
	leader := t.LeaderId
	remaining := append([]int64(nil), t.Members...)
	teamMu.Unlock()
	if len(remaining) > 0 {
		s.sendTeamSnapshot(leader, remaining)
	}
	if kicked != nil {
		s.sendTeamMember(kicked, req.TargetId, []int64{req.TargetId})
	}
	log.Printf("[S=%d] kickout team member %d members=%v", ch.id, req.TargetId, t.Members)
	return resp
}
