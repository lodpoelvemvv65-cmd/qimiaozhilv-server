package main

import (
	"log"
	"sort"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// spacetravel.go：星空旅行（SpaceTravel）入口。
//
// 客户端链路（HotfixView 反汇编，rid 238/629/632/637）：
//   - 奇妙广场（场景 10005）的时空旅人 NPC（NPCBase 1018）被点击后，线上服务器推
//     M2C_OpenSpaceTravelNpcUI(20372) → 客户端打开 SpaceTravelNPCUI（面板含"传送点"按钮）
//   - 点"传送点" → SpaceTravelPointUI（星空旅行选图列表，行 = SpaceTravelConfig 顺序）
//     → 双击某行发送 C2M_RequestEnterMap{MapId = (10039+index)*100+1}（见 portal.go 的
//     场景白名单：10039xx..10044xx 均为 SceneTransConfig 中的 Scene_SpaceTravel*）
//   - 进入星空旅行场景后点战斗 NPC → C2M_StartSpaceTravel{index} → 服务器回 M2C_StartSpaceTravel
//
// 自建服实现：20372 由 onClickNPC 推（task.go），20393 在此实现（校验后回成功）。

// spaceTravelIndexes 按 _id 升序返回全部星空旅行配置行 id（选图顺序）。
func spaceTravelIndexes() []int32 {
	if tables == nil {
		return nil
	}
	ids := make([]int32, 0, len(tables.spaceTravelConfig))
	for k := range tables.spaceTravelConfig {
		ids = append(ids, int32(k))
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// pushOpenSpaceTravelNpcUI：推送 20372，客户端打开星空旅行 NPC 面板。
func (s *Server) pushOpenSpaceTravelNpcUI(ch *channel) {
	if ch == nil || ch.session == nil {
		return
	}
	s.sendPush(ch, protocol.OpM2C_OpenSpaceTravelNpcUI, &protocol.M2C_OpenSpaceTravelNpcUI{
		ActorId: ch.session.playerID,
	})
}

// 20393 → 20394：开始星空旅行（index = 选图列表序号，0 起）。
// 响应 Message 留空 = 成功；越界返回错误。
func (s *Server) onStartSpaceTravel(ch *channel, req *protocol.C2M_StartSpaceTravel) proto.Message {
	resp := &protocol.M2C_StartSpaceTravel{RpcId: req.RpcId}
	if ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	ids := spaceTravelIndexes()
	if req.Index < 0 || int(req.Index) >= len(ids) {
		resp.Error, resp.Message = errBadParam, "星空旅行不存在"
		return resp
	}
	row := tables.spaceTravelConfig[int64(ids[req.Index])]
	log.Printf("[S=%d] start space travel index=%d config=%d monsters=%d",
		ch.id, req.Index, ids[req.Index], len(arrOf(row["MonsterIdArr"])))
	// 成功：客户端进入星空旅行场景（选图后的 RequestEnterMap 已由 portal.go 处理）。
	return resp
}
