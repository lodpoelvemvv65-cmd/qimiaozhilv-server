package main

import (
	"math"
	"time"

	"mhqserver/protocol"
)

// 组队头像的当前血量/蓝量只在「数值发生变化」时才会被客户端刷新：
// ET.UpdateTeamHeadInfoEvent 重建整个头像列表时只写 MaxHP/MaxMP/MP/等级，
// 当前 HP 完全依赖 1001 变化事件；而那条链路最终落在 DOTween 补间上，时长是
// 4.0 / max(条上限, max(起点, 终点)) * |终点-起点|，满血成员接近 4 秒，0 血成员
// 是 0 秒瞬间完成。
//
// 偏偏重建时 FGUI 会把控件还池再取出来复用，上一任占用者没跑完的补间会把旧值继续
// 写回控件。结果就是队友格子里出现别人的血量：0 血那两格反而正常（补间瞬间结束），
// 满血那几格停在补间起点、或者被迟到的旧补间写成了另一个人的值。
//
// 服务端对策：每次重建之后，按固定时间点再补几轮「抖动值 → 精确值」。抖动值保证
// 客户端一定收到一次数值变化、重新起一条补间，紧随其后的精确值保证这条补间停在
// 真值上；一直补到 5 秒，是为了盖过最长的那条约 4 秒的补间，以及控件复用带来的
// 迟到写入。0 血照常补：抖动值取 1、精确值取 0，所以「没血」最终显示的就是 0。
//
// 这里只影响客户端显示，会话与战斗里的权威数值一个都不改。

// teamHeadVitalReloadDelays 是重建之后补发的时间点。最后一个必须晚于客户端最长
// 的补间（约 4 秒），否则迟到的旧补间还有机会盖掉真值。
var teamHeadVitalReloadDelays = []time.Duration{
	150 * time.Millisecond,
	800 * time.Millisecond,
	2500 * time.Millisecond,
	5000 * time.Millisecond,
}

// scheduleTeamHeadVitalReload 在推送完 M2C_TeamMember 之后安排若干轮补发。
// 每次都递增代数，旧一轮的定时器发现代数变了就直接放弃，避免多次重建互相打架。
func (s *Server) scheduleTeamHeadVitalReload(ch *channel, unitIDs []int64) {
	if s == nil || ch == nil || ch.session == nil || len(unitIDs) == 0 {
		return
	}
	gen := ch.teamHeadReloadGen.Add(1)
	snapshot := append([]int64(nil), unitIDs...)
	for _, delay := range teamHeadVitalReloadDelays {
		time.AfterFunc(delay, func() { s.reloadTeamHeadVitals(ch, snapshot, gen) })
	}
}

// reloadTeamHeadVitals 补发一轮：重新解析当前队伍成员并推 1001/1003。
// 数值都在触发时重新读取，所以中途掉线、换图、进战斗都不会推出过期值。
func (s *Server) reloadTeamHeadVitals(ch *channel, unitIDs []int64, gen uint64) {
	if ch == nil || ch.session == nil || ch.session.superseded.Load() {
		return
	}
	if ch.teamHeadReloadGen.Load() != gen {
		return
	}
	actor := ch.session.playerID
	for _, unitID := range unitIDs {
		if unitID == actor {
			continue
		}
		member := s.findChannelByPlayerID(unitID)
		if member == nil || member.session == nil {
			continue
		}
		if !s.scenePlayerVisibleTo(ch, member) {
			continue
		}
		maxHP := member.session.playerMaxHp()
		if maxHP <= 0 {
			continue
		}
		s.sendTeamHeadNudgedVital(ch, unitID, 1001, member.session.battleHP(), maxHP, actor)
		s.sendTeamHeadNudgedVital(ch, unitID, 1003, member.session.battleMP(), member.session.playerMaxMp(), actor)
	}
}

// sendTeamHeadNudgedVital 先推一个与真值差一档的抖动值，再推真值。
// 客户端只在数值变化时才刷新头像条，两次推送保证它重起一条补间并停在真值上。
func (s *Server) sendTeamHeadNudgedVital(ch *channel, unitID int64, numericType int32, exact, maxValue int32, actor int64) {
	s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
		UnitId: unitID, NumericType: numericType, Value: teamHeadNudge(exact, maxValue), ActorId: actor,
	})
	s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
		UnitId: unitID, NumericType: numericType, Value: float32(exact), ActorId: actor,
	})
}

// teamHeadNudge 取一个与真值不同的 float32 抖动值。0 值固定用 1，其余优先往上偏
// 一格（满血时不越上限，改为往下偏），float32 在该量级已经分不出 ±1 时退到相邻
// 可表示值。真值本身必须始终由紧随其后的精确推送给出。
func teamHeadNudge(exact, maxValue int32) float32 {
	value := float32(exact)
	if value <= 0 {
		return 1
	}
	if exact < maxValue && float32(exact+1) != value {
		return float32(exact + 1)
	}
	if float32(exact-1) != value {
		return float32(exact - 1)
	}
	return math.Nextafter32(value, 0)
}
