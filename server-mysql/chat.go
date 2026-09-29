package main

import (
	"log"
	"strings"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// chat.go：聊天系统（20298-20300 段，见 16 文档 §2.4）。
//
// C2M_RequestChat(20298) → M2C_RequestChat(20299)：响应不带聊天内容，仅 Error/Message
// 表达结果（客户端发送成功靠本地回显）。成功时服务器广播 M2C_SendNormalChat(20300)
// （Content/Type/Id/Name/IsSystemBrocast）。客户端成功后会本地回显，服务端不能再把同一条
// 20300 发回发送者，否则聊天窗口会出现两条完全相同的消息。

// onRequestChat：20298 → 20299。校验登录 → 按频道投递 M2C_SendNormalChat → 空响应成功。
func (s *Server) onRequestChat(ch *channel, req *protocol.C2M_RequestChat) proto.Message {
	resp := &protocol.M2C_RequestChat{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	ss := ch.session
	if strings.TrimSpace(req.Content) == "" {
		resp.Message = "聊天内容不能为空"
		return resp
	}
	if req.Type == protocol.ChatType_NoneChat || req.Type == protocol.ChatType_System {
		resp.Message = "不能发送到该频道"
		return resp
	}
	msg := &protocol.M2C_SendNormalChat{
		Content:         req.Content,
		Type:            protocol.ChatType(req.Type),
		Id:              req.Id,
		Name:            ss.name,
		IsSystemBrocast: false,
		ActorId:         ss.playerID,
	}
	if req.Type == protocol.ChatType_Private { // 私聊只投递目标；发送者由客户端本地回显。
		if req.Id <= 0 || req.Id == ss.playerID {
			resp.Message = "私聊目标无效"
			return resp
		}
		target := s.findChannelByPlayerID(req.Id)
		if target == nil {
			resp.Message = "对方不在线"
			return resp
		}
		s.sendPush(target, protocol.OpM2C_SendNormalChat, msg)
	} else {
		recipients, message := s.chatRecipients(ch, req.Type)
		if message != "" {
			resp.Message = message
			return resp
		}
		for _, recipient := range recipients {
			if recipient != nil && recipient != ch {
				s.sendPush(recipient, protocol.OpM2C_SendNormalChat, msg)
			}
		}
	}
	log.Printf("[S=%d] chat type=%d id=%d name=%q content=%q", ch.id, int32(req.Type), req.Id, ss.name, req.Content)
	return resp
}

func (s *Server) chatRecipients(sender *channel, chatType protocol.ChatType) ([]*channel, string) {
	if sender == nil || sender.session == nil {
		return nil, "请先登录"
	}
	ss := sender.session
	online := s.onlineChannels()
	switch chatType {
	case protocol.ChatType_Normal:
		out := make([]*channel, 0, len(online))
		for _, recipient := range online {
			if recipient != nil && sameMapSession(ss, recipient.session) {
				out = append(out, recipient)
			}
		}
		return out, ""
	case protocol.ChatType_Team:
		teamMu.Lock()
		_, team := s.teamEntryOf(ss.playerID)
		if team == nil {
			teamMu.Unlock()
			return nil, "尚未加入队伍"
		}
		memberIDs := make(map[int64]struct{}, len(team.Members))
		for _, id := range team.Members {
			memberIDs[id] = struct{}{}
		}
		teamMu.Unlock()
		out := make([]*channel, 0, len(memberIDs))
		for _, recipient := range online {
			if recipient != nil && recipient.session != nil {
				if _, ok := memberIDs[recipient.session.playerID]; ok {
					out = append(out, recipient)
				}
			}
		}
		return out, ""
	case protocol.ChatType_Family:
		if ss.familyID == 0 {
			return nil, "尚未加入家族"
		}
		out := make([]*channel, 0, len(online))
		for _, recipient := range online {
			if recipient != nil && recipient.session != nil && recipient.session.familyID == ss.familyID {
				out = append(out, recipient)
			}
		}
		return out, ""
	case protocol.ChatType_Camp:
		// UnitCharacter currently advertises every character as Pioneer. Keep the
		// routing predicate explicit so a later persisted camp field cannot leak
		// old messages across camps.
		camp := playerCampType(ss)
		out := make([]*channel, 0, len(online))
		for _, recipient := range online {
			if recipient != nil && playerCampType(recipient.session) == camp {
				out = append(out, recipient)
			}
		}
		return out, ""
	case protocol.ChatType_World:
		return online, ""
	default:
		return nil, "不能发送到该频道"
	}
}

func playerCampType(ss *session) protocol.CampType {
	if ss == nil {
		return protocol.CampType_NoneCamp
	}
	return protocol.CampType_Pioneer
}
