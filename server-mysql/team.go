package main

import (
	"log"
	"math"
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

const teamLeaderDungeonEntryMessage = "只有队长可以进入副本"

const (
	teamRequestPendingMessage = "正在申请..."
	teamInvitePendingMessage  = "正在邀请..."
	teamAcceptedMessage       = "对方已同意"
	teamRejectedMessage       = "对方已拒绝"
	teamKickedMessage         = "你已被踢出队伍"
	teamLeaderOfflineMessage  = "队长离线，队伍已解散"
)

// Scene entry stays folded. Once the leader moves, members follow in one line
// behind the leader. The delay is approximately one formation spacing at the
// native movement speed, which reproduces the observed catch-up cadence.
const (
	teamFollowSpacing float32       = 0.5
	teamFollowDelay   time.Duration = 140 * time.Millisecond
)

func teamFollowerTarget(leaderX, leaderY, unitX, unitY float32, slot int) (float32, float32) {
	if slot <= 0 {
		return leaderX, leaderY
	}
	distance := teamFollowSpacing * float32(slot)
	return leaderX - unitX*distance, leaderY - unitY*distance
}

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

// unitCharacterLeaderID is UnitCharacter.LeaderId. The EnterMap handler marks
// teammate units as UnitType=5 only when this value is the party leader, so
// team-head buff icons can refresh for every member rather than the leader.
func unitCharacterLeaderID(playerID int64) int64 {
	if playerID <= 0 {
		return playerID
	}
	teamMu.Lock()
	defer teamMu.Unlock()
	for _, team := range teams {
		if team != nil && team.LeaderId > 0 && teamContains(team.Members, playerID) {
			return team.LeaderId
		}
	}
	return playerID
}

func copyRequiresSoloTeamExit(copyID int64) bool {
	if tables == nil || tables.copyConfig == nil {
		return false
	}
	row, ok := tables.copyConfig[copyID]
	return ok && !copySupportsTeam(copyID, row)
}

func copySupportsTeam(copyID int64, row map[string]interface{}) bool {
	// Death Road uses the online solo-marked copy 10016, but its activity
	// scene and battle protocol support the same pre-entry party flow as the
	// other shared activity copies. This is the only configured exception.
	return copyID == 10016 || boolOf(row["CanTeam"])
}

// teamLeaderDungeonEntryFailure applies to copies that support a party. Solo
// copies use leaveTeamForSoloInstance at their final accepted entry point.
func (s *Server) teamLeaderDungeonEntryFailure(ch *channel) string {
	if ch == nil || ch.session == nil || ch.session.playerID <= 0 {
		return ""
	}
	pid := ch.session.playerID
	teamMu.Lock()
	_, team := s.teamEntryOf(pid)
	notLeader := team != nil && team.LeaderId != pid
	teamMu.Unlock()
	if notLeader {
		return teamLeaderDungeonEntryMessage
	}
	return ""
}

// leaveTeamForSoloInstance removes the entrant only when they are a regular
// member. A leader entering a solo copy disbands the whole party. Callers use
// this only after every ordinary entry check succeeds.
func (s *Server) leaveTeamForSoloInstance(ch *channel, copyID int64) bool {
	if ch == nil || ch.session == nil || ch.session.playerID <= 0 ||
		!copyRequiresSoloTeamExit(copyID) {
		return false
	}
	pid := ch.session.playerID
	online := make(map[int64]*channel)
	for _, member := range s.onlineChannels() {
		if member != nil && member.session != nil {
			online[member.session.playerID] = member
		}
	}
	online[pid] = ch

	teamMu.Lock()
	teamID, team := s.teamEntryOf(pid)
	if team == nil {
		teamMu.Unlock()
		return false
	}
	members := append([]int64(nil), team.Members...)
	disband := team.LeaderId == pid
	var leader int64
	var remaining []int64
	if disband {
		delete(teams, teamID)
		for _, memberID := range members {
			if member := online[memberID]; member != nil && member.session != nil {
				member.session.teamID = 0
			}
		}
	} else {
		remaining = make([]int64, 0, len(members)-1)
		for _, memberID := range members {
			if memberID != pid {
				remaining = append(remaining, memberID)
			}
		}
		team.Members = remaining
		leader = team.LeaderId
		ch.session.teamID = 0
	}
	teamMu.Unlock()

	if disband {
		for _, memberID := range members {
			if member := online[memberID]; member != nil {
				s.sendTeamMember(member, memberID, []int64{memberID})
			}
		}
		log.Printf("[S=%d] solo copy=%d disband team=%d leader=%d members=%v",
			ch.id, copyID, teamID, pid, members)
		return true
	}
	if len(remaining) > 0 {
		s.sendTeamSnapshot(leader, remaining)
	}
	s.sendTeamMember(ch, pid, []int64{pid})
	log.Printf("[S=%d] solo copy=%d leave team=%d member=%d remaining=%v",
		ch.id, copyID, teamID, pid, remaining)
	return true
}

// leaveTeamOnClose：普通成员下线时移除本人；队长下线时解散整队。
// removeChannel invokes onClose after releasing s.mu, so snapshots and tips can
// be sent synchronously before the old leader Unit disappears from the scene.
func (s *Server) leaveTeamOnClose(playerID int64) {
	if playerID <= 0 {
		return
	}
	online := make(map[int64]*channel)
	for _, member := range s.onlineChannels() {
		if member != nil && member.session != nil {
			online[member.session.playerID] = member
		}
	}
	teamMu.Lock()
	teamID, t := s.teamEntryOf(playerID)
	if t == nil {
		teamMu.Unlock()
		return
	}
	members := append([]int64(nil), t.Members...)
	if t.LeaderId == playerID {
		delete(teams, teamID)
		for _, memberID := range members {
			if member := online[memberID]; member != nil {
				member.session.teamID = 0
			}
		}
		teamMu.Unlock()

		for _, memberID := range members {
			if memberID == playerID {
				continue
			}
			if member := online[memberID]; member != nil {
				s.sendTeamMember(member, memberID, []int64{memberID})
				s.sendTeamStatusTip(member, teamLeaderOfflineMessage)
			}
		}
		log.Printf("leader %d disconnected and disbanded team %d members=%v", playerID, teamID, members)
		return
	}

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
	leader := t.LeaderId
	ids := append([]int64(nil), members...)
	teamMu.Unlock()

	for _, id := range ids {
		if mc := s.findChannelByPlayerID(id); mc != nil {
			s.sendTeamMember(mc, leader, ids)
		}
	}
	log.Printf("player %d left team on disconnect (team %d, remaining %d)", playerID, teamID, len(ids))
}

// sendTeamMember 给单个 channel 推送一条队伍全量同步（M2C_TeamMember 20162）。
func (s *Server) sendTeamMember(ch *channel, leaderID int64, unitIDs []int64) {
	vitals := s.prepareTeamHeadHPRefresh(ch, unitIDs)
	s.sendPush(ch, protocol.OpM2C_TeamMember, &protocol.M2C_TeamMember{
		LeaderId: leaderID,
		UnitIds:  unitIDs,
		ActorId:  ch.session.playerID,
	})
	for _, vital := range vitals {
		s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: vital.unitID, NumericType: 1002, Value: vital.maxHP,
			ActorId: ch.session.playerID,
		})
		s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: vital.unitID, NumericType: 1001, Value: vital.hp,
			ActorId: ch.session.playerID,
		})
		s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: vital.unitID, NumericType: 1004, Value: vital.maxMP,
			ActorId: ch.session.playerID,
		})
		s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: vital.unitID, NumericType: 1003, Value: vital.mp,
			ActorId: ch.session.playerID,
		})
	}
	// 上面这一份会被客户端 FGUI 还池复用和迟到补间盖掉，重建之后再补几轮，见 team_head_vitals.go。
	s.scheduleTeamHeadVitalReload(ch, unitIDs)
}

type teamHeadHPVital struct {
	unitID int64
	maxHP  float32
	hp     float32
	maxMP  float32
	mp     float32
}

func (s *Server) syncTeamHeadHP(unitID int64, hp, maxHP int32) {
	if unitID == 0 {
		return
	}
	if maxHP <= 0 {
		maxHP = 1
	}
	if hp < 0 {
		hp = 0
	} else if hp > maxHP {
		hp = maxHP
	}
	if member := s.findChannelByPlayerID(unitID); member != nil && member.session != nil {
		member.session.storeTeamHeadHP(hp, maxHP)
	}
}

func (s *Server) broadcastFinalPartyHealth(party *partyBattle) {
	if party == nil {
		return
	}
	participants := party.channels(s)
	for _, id := range party.memberIDs {
		battle := party.members[id]
		member := s.findChannelByPlayerID(id)
		if member == nil || member.session == nil || battle == nil {
			continue
		}
		hp, maxHP := battle.playerHP, battle.playerMaxHP
		s.syncTeamHeadHP(id, hp, maxHP)
		for _, recipient := range participants {
			s.sendPush(recipient, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
				UnitId: id, NumericType: 1002, Value: float32(maxHP), ActorId: recipient.session.playerID,
			})
			s.sendPush(recipient, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
				UnitId: id, NumericType: 1001, Value: float32(hp), ActorId: recipient.session.playerID,
			})
		}
	}
}

// The native team-head builder initializes MaxHP but leaves HP at the FGUI
// template's 31. Nudge each visible remote Unit before rebuilding the list,
// then restore its exact HP after M2C_TeamMember so NumericWatcher_Hp updates
// the newly-created progress bar. This changes client presentation only; the
// authoritative session and battle values are never modified.
func (s *Server) prepareTeamHeadHPRefresh(ch *channel, unitIDs []int64) []teamHeadHPVital {
	if ch == nil || ch.session == nil || ch.session.playerID <= 0 {
		return nil
	}
	vitals := make([]teamHeadHPVital, 0, len(unitIDs))
	for _, unitID := range unitIDs {
		if unitID == ch.session.playerID {
			continue
		}
		member := s.findChannelByPlayerID(unitID)
		if member == nil || member.session == nil {
			continue
		}
		if unitID != ch.session.playerID && !s.scenePlayerVisibleTo(ch, member) {
			continue
		}
		hp := member.session.battleHP()
		maxHP := member.session.playerMaxHp()
		mp := member.session.battleMP()
		maxMP := member.session.playerMaxMp()
		if maxHP <= 0 {
			continue
		}
		member.session.storeTeamHeadHP(hp, maxHP)
		// 抖动值必须在客户端重建列表之前送达，重建后紧跟的精确值才会被当成一次
		// 数值变化。血量与蓝量的刷新链路是同一套，所以两个都要抖。
		s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: unitID, NumericType: 1001, Value: teamHeadNudge(hp, maxHP),
			ActorId: ch.session.playerID,
		})
		s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: unitID, NumericType: 1003, Value: teamHeadNudge(mp, maxMP),
			ActorId: ch.session.playerID,
		})
		vitals = append(vitals, teamHeadHPVital{
			unitID: unitID, maxHP: float32(maxHP), hp: float32(hp),
			maxMP: float32(maxMP), mp: float32(mp),
		})
	}
	return vitals
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
		if !sameMapSession(recipient.session, member.session) {
			continue
		}
		if id != recipient.session.playerID && !s.scenePlayerVisibleTo(recipient, member) {
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

func (s *Server) sendCurrentTeamSnapshotTo(ch *channel) {
	if ch == nil || ch.session == nil || ch.session.playerID <= 0 {
		return
	}
	pid := ch.session.playerID
	teamMu.Lock()
	_, team := s.teamEntryOf(pid)
	if team == nil {
		teamMu.Unlock()
		s.sendTeamMember(ch, pid, []int64{pid})
		return
	}
	leader := team.LeaderId
	ids := append([]int64(nil), team.Members...)
	teamMu.Unlock()
	snapshotLeader, snapshotIDs := s.teamSnapshotFor(ch, leader, ids)
	if len(snapshotIDs) > 0 {
		s.sendTeamMember(ch, snapshotLeader, snapshotIDs)
	}
}

// restoreTeamSnapshotAfterMapStartup avoids exposing a transient self-only
// team while a leader-driven public map change is still loading. The client
// treats every 20162 as authoritative, so such an intermediate snapshot looks
// exactly like leaving the party. The last member to become ready establishes
// every directed scene Unit and restores the complete team for all members.
func (s *Server) restoreTeamSnapshotAfterMapStartup(ch *channel) {
	if ch == nil || ch.session == nil || ch.session.playerID <= 0 {
		return
	}
	pid := ch.session.playerID
	teamMu.Lock()
	_, team := s.teamEntryOf(pid)
	if team == nil {
		teamMu.Unlock()
		s.sendTeamMember(ch, pid, []int64{pid})
		return
	}
	leader := team.LeaderId
	ids := append([]int64(nil), team.Members...)
	teamMu.Unlock()

	members := make([]*channel, 0, len(ids))
	allCoLocated := true
	allReady := true
	for _, id := range ids {
		member := s.findChannelByPlayerID(id)
		if member == nil || member.session == nil {
			continue
		}
		members = append(members, member)
		if !sameMapSession(ch.session, member.session) {
			allCoLocated = false
			continue
		}
		// TeamMember references scene Units, which are valid once markMapReady
		// has completed. Later HUD/startup work does not need to hold back the
		// party snapshot and may still be in progress for a returning member.
		if _, ready := member.session.currentMapSceneVersion(); !ready {
			allReady = false
		}
	}
	if allCoLocated && !allReady {
		return
	}
	if !allCoLocated {
		s.sendCurrentTeamSnapshotTo(ch)
		return
	}
	for _, member := range members {
		s.syncReadyScenePlayers(member)
	}
	s.sendTeamSnapshot(leader, ids)
}

// refreshTeamSnapshotsForPlayer rebuilds the whole party after one member
// enters or leaves a private trial instance. The authoritative party remains
// intact, but each client may only reference members visible in its scene.
func (s *Server) refreshTeamSnapshotsForPlayer(playerID int64) {
	teamMu.Lock()
	_, team := s.teamEntryOf(playerID)
	if team == nil {
		teamMu.Unlock()
		return
	}
	leader := team.LeaderId
	ids := append([]int64(nil), team.Members...)
	teamMu.Unlock()
	s.sendTeamSnapshot(leader, ids)
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
	teamMu.Unlock()

	members := make([]*channel, 0, len(ids))
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
		members = append(members, member)
	}
	s.changeMapForParty(members, mapID, x, y)
	// Scene startup restores the complete team after every moved member is
	// ready. Sending a snapshot here would advertise a temporary self-only team
	// while the clients are between scenes, which their UI treats as a leave.
}

// teamFollowerIsControlled reports whether a member currently shares the
// leader's public scene. Private trial members remain independently movable.
func (s *Server) teamFollowerIsControlled(ch *channel) bool {
	if ch == nil || ch.session == nil || ch.session.playerID <= 0 {
		return false
	}
	pid := ch.session.playerID
	teamMu.Lock()
	_, team := s.teamEntryOf(pid)
	if team == nil || team.LeaderId == pid {
		teamMu.Unlock()
		return false
	}
	leaderID := team.LeaderId
	teamMu.Unlock()
	leader := s.findChannelByPlayerID(leaderID)
	return leader != nil && sameMapSession(ch.session, leader.session)
}

// moveTeamFollowers turns one leader path into native per-member 20024 paths.
// The leader path is sent immediately; followers are released one spacing at
// a time so a folded party naturally unfolds as A, B, C, D catch up. Timers
// survive subsequent ground clicks and are invalidated only by a scene change
// or a team membership change, allowing continuous mouse movement to stream
// follower paths instead of cancelling them on every click.
func (s *Server) moveTeamFollowers(leader *channel, path *protocol.M2C_PathfindingResult, now time.Time) {
	if leader == nil || leader.session == nil || path == nil {
		return
	}
	pid := leader.session.playerID
	sequence := leader.session.teamMoveSeq.Load()
	teamMu.Lock()
	_, team := s.teamEntryOf(pid)
	if team == nil || team.LeaderId != pid {
		teamMu.Unlock()
		return
	}
	teamRef := team
	ids := append([]int64(nil), team.Members...)
	teamMu.Unlock()

	dx := float64(path.TX - path.X)
	dy := float64(path.TY - path.Y)
	distance := math.Hypot(dx, dy)
	if distance <= 0.0001 {
		return
	}
	unitX, unitY := float32(dx/distance), float32(dy/distance)
	type follower struct {
		id int64
	}
	followers := make([]follower, 0, len(ids)-1)
	for _, id := range ids {
		if id == pid {
			continue
		}
		member := s.findChannelByPlayerID(id)
		if member == nil || member.session == nil || !sameMapSession(leader.session, member.session) {
			continue
		}
		member.session.battleMu.Lock()
		inBattle := member.session.battle != nil
		member.session.battleMu.Unlock()
		if inBattle {
			continue
		}
		followers = append(followers, follower{id: id})
	}
	if len(followers) == 0 {
		return
	}
	// Keep the complete queue visible even when the click is shorter than the
	// nominal formation length. Compress the spacing for that one path so
	// every member still receives a distinct target between start and finish.
	spacing := teamFollowSpacing
	if total := spacing * float32(len(followers)); total > float32(distance) {
		spacing = float32(distance) / float32(len(followers))
	}
	perSlotDelay := teamFollowDelay
	if teamFollowSpacing > 0 {
		perSlotDelay = time.Duration(float64(teamFollowDelay) * float64(spacing/teamFollowSpacing))
		if perSlotDelay < 20*time.Millisecond {
			perSlotDelay = 20 * time.Millisecond
		}
	}
	for slot, entry := range followers {
		followSlot := slot + 1
		gap := spacing * float32(followSlot)
		targetX := path.TX - unitX*gap
		targetY := path.TY - unitY*gap
		delay := perSlotDelay * time.Duration(followSlot)
		memberID := entry.id
		time.AfterFunc(delay, func() {
			if leader.session.teamMoveSeq.Load() != sequence {
				return
			}
			teamMu.Lock()
			_, activeTeam := s.teamEntryOf(pid)
			validMember := activeTeam == teamRef && activeTeam.LeaderId == pid &&
				teamContains(activeTeam.Members, memberID)
			teamMu.Unlock()
			if !validMember {
				return
			}
			current := s.findChannelByPlayerID(memberID)
			if current == nil || current.session == nil ||
				!sameMapSession(leader.session, current.session) {
				return
			}
			current.session.battleMu.Lock()
			inBattle := current.session.battle != nil
			current.session.battleMu.Unlock()
			if inBattle || leader.session.teamMoveSeq.Load() != sequence {
				return
			}
			sx, sy := current.session.beginMovement(time.Now(), targetX, targetY, moveSpeed)
			followerPath := &protocol.M2C_PathfindingResult{
				Id: memberID, X: sx, Y: sy, TX: targetX, TY: targetY,
				MoveSpeed: moveSpeed, ActorId: memberID,
			}
			s.sendPush(current, protocol.OpM2C_PathfindingResult, followerPath)
			s.broadcastMove(current, followerPath)
		})
	}
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

func (s *Server) sendTeamStatusTip(ch *channel, message string) {
	if ch == nil || ch.session == nil || message == "" {
		return
	}
	s.sendPush(ch, protocol.OpM2C_SendTip, &protocol.M2C_SendTip{
		Message: message, ActorId: ch.session.playerID,
	})
}

// onRequestTeam：20154 → 20155。申请加入 TargetId 的队伍（推 20158 给目标）。
// onRequestTeam：20154 → 20155。申请加入 TargetId 的队伍。
// 目标可在线任意玩家：目标有队伍 → 申请加入其队伍（队长同意后入队）；
// 目标无队伍 → 目标收到申请条，同意后建队（LeaderId=目标）收我入队。
func (s *Server) onRequestTeam(ch *channel, req *protocol.C2M_RequestTeam) proto.Message {
	resp := &protocol.M2C_RequestTeam{RpcId: req.RpcId}
	ss := ch.session
	// The patched client reuses the original team request wire message for
	// trade requests and negates TargetId. Keep this discriminator entirely on
	// the server side so ordinary positive team requests remain unchanged.
	if req.TargetId < 0 {
		tradeResp := s.onRequestTradeCompat(ch, &protocol.C2M_RequestTrade{
			TargetId: -req.TargetId,
			RpcId:    req.RpcId,
		}).(*protocol.M2C_RequestTrade)
		if tradeResp != nil {
			resp.Error, resp.Message = tradeResp.Error, tradeResp.Message
		}
		return resp
	}
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
	requestRecipientID := target.session.playerID
	if targetTeam != nil {
		requestRecipientID = targetTeam.LeaderId
	}
	teamMu.Unlock()
	if targetFull {
		resp.Message = "对方队伍已满，不能进队"
		return resp
	}
	requestRecipient := target
	if requestRecipientID != target.session.playerID {
		requestRecipient = s.findChannelByPlayerID(requestRecipientID)
		if requestRecipient == nil {
			resp.Message = "队长不在线"
			return resp
		}
		if !sameMapSession(ss, requestRecipient.session) {
			resp.Message = "队长不在当前场景"
			return resp
		}
	}
	s.sendPush(requestRecipient, protocol.OpM2C_RequestList, &protocol.M2C_RequestList{
		UnitId: ss.playerID,
		// TeamRequestUI.TweenValue interprets this field as milliseconds.
		TimeOut: 30000,
		ActorId: requestRecipientID,
	})
	resp.Message = teamRequestPendingMessage
	log.Printf("[S=%d] request team from %d via target=%d to leader=%d",
		ch.id, ss.playerID, req.TargetId, requestRecipientID)
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
	resp.Message = teamInvitePendingMessage
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
	if other < 0 {
		return s.onHandleTradeByRequester(ch, -other, hi.Bool, req.RpcId)
	}
	if other == ss.playerID {
		resp.Message = "参数错误"
		return resp
	}
	if req.IsRequest {
		teamMu.Lock()
		_, handlerTeam := s.teamEntryOf(ss.playerID)
		handlerIsLeader := handlerTeam == nil || handlerTeam.LeaderId == ss.playerID
		teamMu.Unlock()
		if !handlerIsLeader {
			resp.Message = "只有队长可以处理入队申请"
			return resp
		}
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
		// M2C_SendTip reuses the sender's existing TipUI and replaces its text.
		s.sendTeamStatusTip(target, teamRejectedMessage)
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
	teamMu.Lock()
	if _, existing := s.teamEntryOf(joiner); existing != nil {
		teamMu.Unlock()
		resp.Message = "对方已有队伍"
		return resp
	}
	tid, t := s.teamEntryOf(leader)
	if t != nil && t.LeaderId != leader {
		teamMu.Unlock()
		resp.Message = "只有当前队长可以处理组队请求"
		return resp
	}
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
	s.sendTeamStatusTip(target, teamAcceptedMessage)
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
		s.sendTeamStatusTip(kicked, teamKickedMessage)
	}
	log.Printf("[S=%d] kickout team member %d members=%v", ch.id, req.TargetId, t.Members)
	return resp
}
