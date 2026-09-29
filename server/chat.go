package main

import (
	"log"

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
	msg := &protocol.M2C_SendNormalChat{
		Content:         req.Content,
		Type:            protocol.ChatType(req.Type),
		Id:              req.Id,
		Name:            ss.name,
		IsSystemBrocast: false,
		ActorId:         ss.playerID,
	}
	if int32(req.Type) == 6 { // ChatType.Private（私聊：只投递给目标；发送者本地回显）
		target := s.findChannelByPlayerID(req.Id)
		if target == nil {
			resp.Message = "对方不在线"
			return resp
		}
		if target != ch {
			s.sendPush(target, protocol.OpM2C_SendNormalChat, msg)
		}
	} else { // 普通/队伍/家族/阵营/世界等：广播给其他在线玩家
		for _, ch2 := range s.onlineChannels() {
			if ch2 == ch {
				continue
			}
			s.sendPush(ch2, protocol.OpM2C_SendNormalChat, msg)
		}
	}
	log.Printf("[S=%d] chat type=%d id=%d name=%q content=%q", ch.id, int32(req.Type), req.Id, ss.name, req.Content)
	return resp
}
