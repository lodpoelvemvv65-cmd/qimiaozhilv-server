package main

import (
	"log"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func lockSocialSessions(a, b *session) func() {
	if a == nil || b == nil || a == b {
		if a != nil {
			a.socialMu.Lock()
			return a.socialMu.Unlock
		}
		return func() {}
	}
	first, second := a, b
	if first.playerID > second.playerID {
		first, second = second, first
	}
	first.socialMu.Lock()
	second.socialMu.Lock()
	return func() {
		second.socialMu.Unlock()
		first.socialMu.Unlock()
	}
}

func cloneFriends(m map[int64]*friendInfo) map[int64]*friendInfo {
	out := make(map[int64]*friendInfo, len(m))
	for id, friend := range m {
		if friend == nil {
			continue
		}
		copy := *friend
		out[id] = &copy
	}
	return out
}

func cloneRequestIDs(m map[int64]bool) map[int64]bool {
	out := make(map[int64]bool, len(m))
	for id, value := range m {
		if value {
			out[id] = true
		}
	}
	return out
}

func sessionFriendsSnapshot(ss *session) map[int64]*friendInfo {
	if ss == nil {
		return map[int64]*friendInfo{}
	}
	ss.socialMu.Lock()
	defer ss.socialMu.Unlock()
	return cloneFriends(ss.friends)
}

func sessionFriendsJSON(ss *session) string {
	return friendsToJSON(sessionFriendsSnapshot(ss))
}

// friend.go：好友系统（20107-20118 段）。
//
// 数据模型（session，friends_json 列持久化）：
//   ss.friends       好友 Id → friendInfo（Id/Name/Job/Level/LastLogin）
//   ss.friendReqFrom 待处理的好友申请（对方 Id）
//   ss.friendReqTo   我发出的申请（对方 Id）
//
// M2C_GetFriend(20108) 的 FriendInfoList(tag1) / RequestInfoList(tag2) 与
// M2C_HandleAddFriend(20115) 的 FriendInfoList(tag1) 是客户端"字段级 ProtoMember"
// （List 类型），protoc 生成代码不含 → 手工用 pbAppendBytes 追加（RequestInfoList
// 为空列表，字段省略）。
// M2C_SendAddFriendRequest(20113) / M2C_HandleAddFriendResult(20116) 无业务字段，
// 客户端用 ActorId 标识请求方/响应方。

// encodeFriendInfo：FriendInfo{Id=1, Name=2, Job=3, Level=4, LastLginTime=5}
// （LastLginTime 为线上字段名拼写）。
func encodeFriendInfo(f *friendInfo) []byte {
	var b []byte
	b = pbAppendVarint(b, 1, uint64(f.Id))
	if f.Name != "" {
		b = pbAppendBytes(b, 2, []byte(f.Name))
	}
	b = pbAppendVarint(b, 3, uint64(f.Job))
	b = pbAppendVarint(b, 4, uint64(f.Level))
	b = pbAppendVarint(b, 5, uint64(f.LastLogin))
	return b
}

// appendFriendList：把好友映射编码为 FriendInfoList（field 为目标消息中的 tag），
// 按好友 Id 升序。
func appendFriendList(msg []byte, field int, m map[int64]*friendInfo) []byte {
	ids := make([]int64, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		f := m[id]
		if f == nil {
			continue
		}
		msg = pbAppendBytes(msg, field, encodeFriendInfo(f))
	}
	return msg
}

func friendWireSnapshot(s *Server, m map[int64]*friendInfo) map[int64]*friendInfo {
	out := make(map[int64]*friendInfo, len(m))
	for id, friend := range m {
		if friend == nil {
			continue
		}
		copy := *friend
		copy.LastLogin = clientLastLogin(copy.LastLogin, s != nil && s.findChannelByPlayerID(id) != nil)
		out[id] = &copy
	}
	return out
}

func encodeFriendRequest(s *Server, id int64) []byte {
	if s == nil || id <= 0 {
		return nil
	}
	name := ""
	var job, level int32
	if target := s.findChannelByPlayerID(id); target != nil && target.session != nil {
		name, job, level = target.session.name, target.session.jobID, target.session.level
	} else if s.store != nil {
		_ = s.store.db.QueryRow(`SELECT name, job_id, level FROM players WHERE id = ?`, id).
			Scan(&name, &job, &level)
	}
	if name == "" {
		return nil
	}
	raw, err := proto.Marshal(&protocol.RequestAddFriendInfo{
		Name: name, Level: level, Job: protocol.JobType(jobTypeOf(job)),
		Sex: sexTypeOfJob(job), Id: id,
	})
	if err != nil {
		return nil
	}
	return raw
}

// onGetFriend：20107 → 20108。查询好友列表（raw 20108 + FriendInfoList）。
func (s *Server) onGetFriend(ch *channel, req *protocol.C2M_GetFriend) proto.Message {
	ss := ch.session
	ss.socialMu.Lock()
	friends := cloneFriends(ss.friends)
	requests := cloneRequestIDs(ss.friendReqFrom)
	ss.socialMu.Unlock()
	base, err := proto.Marshal(&protocol.M2C_GetFriend{RpcId: req.RpcId})
	if err != nil {
		resp := &protocol.M2C_GetFriend{RpcId: req.RpcId}
		resp.Error, resp.Message = errBadParam, "序列化失败"
		return resp
	}
	base = appendFriendList(base, 1, friendWireSnapshot(s, friends))
	ids := make([]int64, 0, len(requests))
	for id := range requests {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		if raw := encodeFriendRequest(s, id); len(raw) > 0 {
			base = pbAppendBytes(base, 2, raw)
		}
	}
	s.sendRawPush(ch, protocol.OpM2C_GetFriend, base)
	log.Printf("[S=%d] get friend count=%d requests=%d", ch.id, len(friends), len(requests))
	return nil
}

// onFindFriend：20109 → 20110。按名字查找在线玩家，返回其 Id。
func (s *Server) onFindFriend(ch *channel, req *protocol.C2M_FindFriend) proto.Message {
	resp := &protocol.M2C_FindFriend{RpcId: req.RpcId}
	ss := ch.session
	name := strings.TrimSpace(req.Name)
	if name == "" || name == ss.name {
		resp.Message = "不能添加自己"
		return resp
	}
	for _, ch2 := range s.onlineChannels() {
		if ch2.session != nil && ch2.session.name == name {
			resp.Id = ch2.session.playerID
			log.Printf("[S=%d] find friend %q -> %d", ch.id, name, resp.Id)
			return resp
		}
	}
	resp.Message = "玩家不在线或不存在"
	return resp
}

// onAddFriend：20111 → 20112。发起好友申请：给对方推 20113（ActorId=申请者）。
func (s *Server) onAddFriend(ch *channel, req *protocol.C2M_AddFriend) proto.Message {
	resp := &protocol.M2C_AddFriend{RpcId: req.RpcId}
	ss := ch.session
	if req.Id == ss.playerID {
		resp.Message = "不能添加自己"
		return resp
	}
	target := s.findChannelByPlayerID(req.Id)
	if target == nil {
		resp.Message = "玩家不在线"
		return resp
	}
	unlock := lockSocialSessions(ss, target.session)
	if ss.friends[req.Id] != nil || target.session.friends[ss.playerID] != nil {
		unlock()
		resp.Message = "已经是好友"
		return resp
	}
	ss.friendReqTo[req.Id] = true
	target.session.friendReqFrom[ss.playerID] = true
	unlock()
	// M2C_SendAddFriendRequest 无业务字段，ActorId = 申请者 Id（客户端据此识别请求人）
	s.sendPush(target, protocol.OpM2C_SendAddFriendRequest, &protocol.M2C_SendAddFriendRequest{
		ActorId: ss.playerID,
	})
	log.Printf("[S=%d] add friend request from %d to %d", ch.id, ss.playerID, req.Id)
	return resp
}

// onHandleAddFriend：20114 → 20115。处理好友申请（同意/拒绝）。
// 同意时双方互加好友（raw 20115 回好友列表），并给对方推 20116（ActorId=响应方）。
func (s *Server) onHandleAddFriend(ch *channel, req *protocol.C2M_HandleAddFriend) proto.Message {
	resp := &protocol.M2C_HandleAddFriend{RpcId: req.RpcId}
	ss := ch.session
	id := req.Id
	target := s.findChannelByPlayerID(id)
	if target == nil && req.IsAgree {
		resp.Message = "对方不在线"
		return resp
	}
	var targetSession *session
	if target != nil {
		targetSession = target.session
	}
	unlock := lockSocialSessions(ss, targetSession)
	delete(ss.friendReqFrom, id)
	if targetSession != nil {
		delete(targetSession.friendReqTo, ss.playerID)
	}
	if !req.IsAgree {
		unlock()
		log.Printf("[S=%d] handle add friend id=%d reject", ch.id, id)
		return resp
	}
	now := time.Now().Unix()
	tss := targetSession
	ss.friends[id] = &friendInfo{
		Id:        id,
		Name:      tss.name,
		Job:       jobTypeOf(tss.jobID),
		Level:     tss.level,
		LastLogin: now,
	}
	tss.friends[ss.playerID] = &friendInfo{
		Id:        ss.playerID,
		Name:      ss.name,
		Job:       jobTypeOf(ss.jobID),
		Level:     ss.level,
		LastLogin: now,
	}
	unlock()
	s.saveData(ch)
	s.saveData(target)
	// 通知对方：M2C_HandleAddFriendResult 无业务字段，ActorId = 响应方（即我）
	s.sendPush(target, protocol.OpM2C_HandleAddFriendResult, &protocol.M2C_HandleAddFriendResult{
		ActorId: ss.playerID,
	})
	// 响应：raw 20115（基础响应 + FriendInfoList）
	base, err := proto.Marshal(resp)
	if err != nil {
		resp.Error, resp.Message = errBadParam, "序列化失败"
		return resp
	}
	base = appendFriendList(base, 1, friendWireSnapshot(s, sessionFriendsSnapshot(ss)))
	s.sendRawPush(ch, protocol.OpM2C_HandleAddFriend, base)
	log.Printf("[S=%d] handle add friend id=%d agree", ch.id, id)
	return nil
}

// onDeleteFriend：20117 → 20118。删除好友。
func (s *Server) onDeleteFriend(ch *channel, req *protocol.C2M_DeleteFriend) proto.Message {
	ss := ch.session
	target := s.findChannelByPlayerID(req.Id)
	unlock := lockSocialSessions(ss, func() *session {
		if target != nil {
			return target.session
		}
		return nil
	}())
	delete(ss.friends, req.Id)
	if target != nil && target.session != nil {
		delete(target.session.friends, ss.playerID)
	}
	unlock()
	s.saveData(ch)
	if target != nil {
		s.saveData(target)
	}
	log.Printf("[S=%d] delete friend id=%d", ch.id, req.Id)
	return &protocol.M2C_DeleteFriend{RpcId: req.RpcId}
}
