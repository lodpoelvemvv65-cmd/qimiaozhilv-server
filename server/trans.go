package main

import (
	"log"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// trans.go：转生系统（20342 → 20343）。
//
// 客户端链路（19 文档 §3，HotfixView 反汇编）：
//   - Quest1UI.TransLevel（rid 1171）：发 C2M_TransLevel{Level=1}（ldc.i4.1）
//   - ButlerUI.AddTransLevel2（rid 1052）：发 C2M_TransLevel{Level=2}（ldc.i4.2，一次加两转）
//   - 响应 Message 为空 = 成功（无本地处理，全靠服务器推送刷新）；
//     Message 非空 → OpenUI 弹窗提示。
//
// 服务器刷新通道（客户端无本地状态，全部依赖推送）：
//   - M2C_SyncUnitAttribute(20169){NumericType=1029, Value=新转生等级} —— 转生等级走
//     NumericComponent 实时更新；客户端 CharacterHelper.GetLevelString 同时读 1026/1029，
//     转生等级参与等级文案。
//   - M2C_SendCharacter(20257){UnitCharacter.Trans=新值} —— 角色面板全量刷新。
//
// 转生加成（TransmigrationAddConfig.json，12 条）：
//   _id = 职业族×10 + 转生等级（11/21/31/41=一转、12/22/32/42=二转、13/23/33/43=三转）→
//   上限 3 阶。AddAttributeArr[{Key: Cal.AttributeType, Value}]，本次实现应用整数六维：
//   Key 3 力量→Str、4 敏捷→Quk、5 精神→Spi、6 智慧→Wim、20 体质→MaxHp、21 耐力→MaxMp
//   （百分比字段 19 辅助值/11-18 暴击/22-23 免伤 战斗系统暂无通道，忽略）。

// 转生上限（TransmigrationAddConfig 仅 1-3 阶反推）。
const transMaxLevel = 3

// CharacterHelper.GetLevelString uses this exact client-side array to turn a
// total level into the displayed "trans-level" value. The server stores the
// same total level; old saves created by the previous implementation stored a
// local level and are migrated when loaded.
var transLevelStart = [...]int32{0, 6000, 13000, 21500, 31500}

func transStart(level int32) int32 {
	if level < 0 {
		return 0
	}
	if level >= int32(len(transLevelStart)) {
		return transLevelStart[len(transLevelStart)-1]
	}
	return transLevelStart[level]
}

func transLevelCap(trans int32) int32 {
	if trans < 0 {
		return transLevelStart[0]
	}
	if trans+1 < int32(len(transLevelStart)) {
		return transLevelStart[trans+1]
	}
	return transLevelStart[len(transLevelStart)-1]
}

// jobTypeOf 角色职业族（JobId 首位数字 1/2/3/4，兼容 1-4 与 100001 等进阶 id）。
func jobTypeOf(jobID int32) int32 {
	if jobID <= 0 {
		return 1
	}
	// Client JobId 1..8 are concrete male/female skins; adjacent ids share
	// one profession family: (jobId + 1) / 2.
	if jobID <= 8 {
		return (jobID + 1) / 2
	}
	if jobID < 10000 {
		return jobID / 1000
	}
	if jobID < 100000 {
		return jobID / 10000
	}
	return jobID / 100000
}

// recalcTransBonus 按 (职业族, 转生等级) 查 TransmigrationAddConfig 重算转生属性加成。
// 结果存 session.transBonus（NumericType → 加值），playerXxx() 计算时叠加。
func (ss *session) recalcTransBonus() {
	if ss == nil {
		return
	}
	ss.transBonus = make(map[int32]float32)
	if ss.trans <= 0 || tables == nil || tables.transmigrationAdd == nil {
		return
	}
	cfgID := jobTypeOf(ss.jobID)*10 + ss.trans
	row, ok := tables.transmigrationAdd[int64(cfgID)]
	if !ok {
		log.Printf("[S] trans bonus config %d missing (jobType=%d trans=%d)", cfgID, jobTypeOf(ss.jobID), ss.trans)
		return
	}
	for _, e := range arrOf(row["AddAttributeArr"]) {
		eo, _ := e.(map[string]interface{})
		if eo == nil {
			continue
		}
		key := int32(num(eo["Key"]))
		val := float32(numf(eo["Value"]))
		// TransmigrationAddConfig uses the same Cal.AttributeType key space as
		// equipment and gems. Preserve every configured attribute and rate.
		if numeric, ok := gemKeyToNumeric[key]; ok {
			ss.transBonus[numeric] += val
		}
	}
	log.Printf("[S] trans bonus applied trans=%d cfg=%d bonus=%v", ss.trans, cfgID, ss.transBonus)
}

// onTransLevel：转生（20342 → 20343）。
// req.Level ∈ {1,2}（一次加一转/两转）；当前转生等级 ≥3 拒绝；超出上限拒绝。
// 成功后：推 20169(1029) + 20257(SendCharacter) + 20169 全属性（转生加成生效）。
func (s *Server) onTransLevel(ch *channel, req *protocol.C2M_TransLevel) proto.Message {
	resp := &protocol.M2C_TransLevel{RpcId: req.RpcId}
	ss := ch.session
	if ss == nil || ss.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	if req.Level != 1 && req.Level != 2 {
		// Session.Call treats a non-zero Error as an RPC failure and disconnects.
		// Keep business validation failures in Message so the client can show them.
		resp.Message = "转生参数错误"
		return resp
	}
	newTrans := ss.trans + req.Level
	if newTrans <= transMaxLevel && ss.level < transStart(newTrans) {
		resp.Message = "等级不足，无法转生"
		return resp
	}
	if ss.trans >= transMaxLevel {
		resp.Message = "已达转生上限"
		return resp
	}
	if newTrans > transMaxLevel {
		resp.Message = "超出转生上限"
		return resp
	}
	ss.trans = newTrans
	ss.recalcTransBonus()

	// 转生等级（NumericType 1029）→ 客户端 NumericComponent → 等级文案/状态栏
	s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
		UnitId: ss.playerID, NumericType: 1029, Value: float32(ss.trans), ActorId: ss.playerID,
	})
	// 角色全量刷新（UnitCharacter.Trans）→ UpdateCharacterUI
	s.sendPush(ch, protocol.OpM2C_SendCharacter, &protocol.M2C_SendCharacter{
		UnitCharacter: buildUnitCharacter(ss),
		Id:            ss.playerID,
		ActorId:       ss.playerID,
	})
	// 全属性重推（转生加成后的六维/血蓝）
	s.pushPlayerAttrs(ch)
	s.saveData(ch)
	log.Printf("[S=%d] trans level +%d -> %d (jobType=%d bonus=%v)",
		ch.id, req.Level, ss.trans, jobTypeOf(ss.jobID), ss.transBonus)
	return resp
}
