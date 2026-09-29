package main

import (
	"time"

	"mhqserver/protocol"
)

// 组队头像的血/蓝只在数值**变化**时才刷新（原因见 team_head_vitals.go），而吃药、
// 魔法球自动回复这两条非战斗路径原本只把 1001/1003 推给自己：玩家自己看到回满血，
// 队友那边的格子却还停在半血。战斗内的变化已经通过 battleRecipients 广播给全队，
// 这里补齐非战斗路径，让「我这边血蓝变了」对所有队友可见。
//
// 单人打的怪（battle.party == nil）走的是另一条路：battleRecipients 只返回自己，
// 于是战斗中的掉血和收尾写回的残血都只推给自己一个人，队友的头像要等下一次重建
// （换图、进出战斗、重新组队）才对齐。战斗路径全都在 battleMu 里，不能直接调
// pushTeamVitals（见下），因此用 scheduleTeamVitalSync 排到锁外执行。

// teamMemberChannels 返回 pid 所在队伍的其它成员连接（不含自己，也不含离线成员）。
func (s *Server) teamMemberChannels(pid int64) []*channel {
	if s == nil || pid == 0 {
		return nil
	}
	teamMu.Lock()
	var ids []int64
	if _, team := s.teamEntryOf(pid); team != nil {
		for _, member := range team.Members {
			if member != pid {
				ids = append(ids, member)
			}
		}
	}
	teamMu.Unlock()

	channels := make([]*channel, 0, len(ids))
	for _, id := range ids {
		if member := s.findChannelByPlayerID(id); member != nil && member.session != nil {
			channels = append(channels, member)
		}
	}
	return channels
}

// pushTeamVitals 把某个队员自己身上发生的血/蓝变化同步给同队其它人。
//
// 调用方**不得持有任何 session 的 battleMu**：本函数会取 teamMu，而队伍换图/跟随
// 里已经有 teamMu → battleMu 的加锁顺序（team.go changeMapForTeamLeader），
// 反向加锁会和它互相等待。
func (s *Server) pushTeamVitals(ch *channel, hp, mp int32) {
	if s == nil || ch == nil || ch.session == nil {
		return
	}
	unitID := ch.session.playerID
	if unitID == 0 {
		return
	}
	s.syncTeamHeadHP(unitID, hp, ch.session.playerMaxHp())
	for _, member := range s.teamMemberChannels(unitID) {
		if member == ch {
			continue
		}
		s.sendPush(member, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: unitID, NumericType: 1001, Value: float32(hp), ActorId: member.session.playerID,
		})
		s.sendPush(member, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: unitID, NumericType: 1003, Value: float32(mp), ActorId: member.session.playerID,
		})
	}
}

// teamVitalSyncDelay 是战斗路径补推血蓝的合并间隔。
const teamVitalSyncDelay = 50 * time.Millisecond

// scheduleTeamVitalSync 安排在锁外把 target 当前的血蓝补推给队友。
//
// 战斗里的血蓝变化（挨打、技能结算、胜利/失败写回残血）全都发生在 battleMu 里，
// 而 pushTeamVitals 要取 teamMu，两者与队伍换图的 teamMu → battleMu 顺序反向，
// 直接在锁内调用会 ABBA 死锁。这里只置一个标记并排一个定时器，定时器在没有任何
// 锁的 goroutine 里重新读一次权威血蓝再补推：
//   - 同一间隔内的多次调用合并成一次（单人战斗每挨一下都会请求，一次战斗几十下）；
//   - 触发时重新读 ss.battleHP()/battleMP()，拿到的永远是最新值，不会补推中间态；
//   - 会话已被顶替、连接已不是该角色的当前连接、服务器正在关闭，都直接放弃。
func (s *Server) scheduleTeamVitalSync(target *channel) {
	if s == nil || target == nil || target.session == nil {
		return
	}
	if target.session.playerID == 0 {
		return
	}
	if !target.teamVitalSyncPending.CompareAndSwap(false, true) {
		return
	}
	time.AfterFunc(teamVitalSyncDelay, func() {
		target.teamVitalSyncPending.Store(false)
		if s.closed.Load() {
			return
		}
		ss := target.session
		if ss == nil || ss.playerID == 0 || ss.superseded.Load() {
			return
		}
		// 连接已经换人（同一会话被新的 channel 接管）时，权威血蓝由新连接推。
		if s.findChannelByPlayerID(ss.playerID) != target {
			return
		}
		s.pushTeamVitals(target, ss.battleHP(), ss.battleMP())
	})
}
