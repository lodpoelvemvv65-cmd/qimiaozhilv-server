package main

import (
	"log"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// battleexit.go：战斗退出/AI 托管/挂机战斗 处理器（20171/20365-20368/20087-20090）。
// 均以"结束当前战斗状态"为主，避免客户端等待挂起。

// 20171：退出战斗（逃跑）。C2M_QuitBattle 是 IActorLocationMessage（protocol_dump
// 无配对 M2C 响应），但客户端"退出战斗"按钮在战斗界面内反复点击都无效果（实测
// Player.log + 服务器日志：每 5 秒重发 20171 战斗 UI 不关）——因为服务器只清状态
// 不推任何消息，客户端战斗 UI 没有关闭信号。
// 修复：清状态 + 推当前血蓝 + 【回主城】(M2C_ChangeMap)——客户端 ChangeMapEvent
// 重建场景单位（清战斗单位）→ 战斗 UI 必然关闭；同时回带 RpcId 的帧让可能挂起的
// Session.Call 完成（ET 按 RpcId 匹配，不校验 opcode）。显式退出路径自己拥有这次
// 场景切换，因此不能再叠加持续特效的延迟重载。
// 组队战斗中全队共用一个战斗，"退出战斗"因此是整队行为：队长点击时每个队员
// 一起结束战斗并回到同一个目的地，队员点击只回提示（见 quitBattleForParty）。
type battleExitResult struct {
	battle        *battleState
	activity      *activityBattle
	hadFightState bool
	wasTrial      bool
	wasManual     bool
	wasFamilyBoss bool
	wasPVP        bool
}

// clearBattleForTransition stops every server-side combat source without
// choosing a destination map. Callers can then rebuild either the current
// scene or the main city exactly once.
func (s *Server) clearBattleForTransition(ch *channel, reason string) battleExitResult {
	var result battleExitResult
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		return result
	}

	ss := ch.session
	ss.battleMu.Lock()
	ss.invalidateMainStoryAILocked()
	result.battle = ss.battle
	result.hadFightState = ss.battle != nil || ss.autoBattle || ss.idleBattle
	if result.battle != nil {
		result.activity = result.battle.activity
		result.wasTrial = result.battle.battleType == trialBattleType
		result.wasManual = isManualEquipBattle(result.battle)
		result.wasFamilyBoss = result.battle.mapID < 0
		result.wasPVP = result.battle.pvp != nil
		s.detachCurrentBattleLocked(ch, reason)
	}
	ss.autoBattle = false
	ss.idleBattle = false
	ss.autoBattleEpoch++
	ss.battleMu.Unlock()

	if result.battle != nil && !result.wasPVP {
		s.applyMagicBallRecover(ch)
		s.sendBattleDefeat(ch, result.battle)
		s.pushHealth(ch)
		// 退出战斗也是「战斗写回残血」的一种，队友那格要跟着变（见 team_vital_sync.go）。
		s.scheduleTeamVitalSync(ch)
	}
	return result
}

// 组队战斗里"退出战斗"是整队行为：全队共用同一个战斗，只有队长能决定全队
// 撤离。队员单独点击既不能让自己脱战（战斗仍在继续），也不能替队长把队伍带
// 回城，因此只回一条提示，不改动任何状态。
const teamMemberQuitBattleBlockedMessage = "组队中只有队长可以退出战斗，请先退出队伍"

// partyBattleChannels 返回调用者当前共享战斗里的在线成员；单人战斗、1v1 决斗
// 或战斗已结束都返回 nil。判定只看战斗自身的成员表，不看队伍快照：队伍在战斗
// 中解散时快照可能已经没了，但战斗仍然是全队共享的。
func (s *Server) partyBattleChannels(ch *channel) []*channel {
	if ch == nil || ch.session == nil {
		return nil
	}
	ss := ch.session
	ss.battleMu.Lock()
	battle := ss.battle
	ss.battleMu.Unlock()
	if battle == nil || battle.pvp != nil || battle.party == nil {
		return nil
	}
	members := battle.party.channels(s)
	if len(members) < 2 {
		return nil
	}
	return members
}

// partyBattleExitOwner 判断调用者是否有权决定整队退出战斗。队伍条目已经不存在
// （队伍在战斗中解散）时交给点击者收尾，否则剩下的共享战斗没人能结束。
func (s *Server) partyBattleExitOwner(ch *channel) bool {
	if ch == nil || ch.session == nil {
		return false
	}
	teamMu.Lock()
	defer teamMu.Unlock()
	_, team := s.teamEntryOf(ch.session.playerID)
	return team == nil || team.LeaderId == ch.session.playerID
}

// quitBattleForParty 让队长的一次"退出战斗"对全队生效：每个成员各自结束战斗
// 状态并收到战斗结束与血蓝同步，然后一起回到同一个目的地。
// 共享战斗的结算必须先标记 settled：成员逐个脱战时最后一次 detach 会判定
// "剩下的成员都已倒下" 并异步触发失败结算，那个结算会再发一次 ChangeMap。
func (s *Server) quitBattleForParty(leader *channel, members []*channel) {
	for _, member := range members {
		if member == nil || member.session == nil {
			continue
		}
		member.session.battleMu.Lock()
		if battle := member.session.battle; battle != nil && battle.party != nil {
			battle.party.mu.Lock()
			battle.party.settled = true
			battle.party.mu.Unlock()
		}
		member.session.battleMu.Unlock()
	}

	exits := make(map[int64]battleExitResult, len(members))
	for _, member := range members {
		if member == nil || member.session == nil {
			continue
		}
		exits[member.session.playerID] = s.clearBattleForTransition(member, "quit-party")
	}
	// 目的地取队长这次退出战斗的判定结果：活动战斗回活动记录的返回点，其余
	// （含海滩新手流程）回主城左侧入口。全队必须落在同一个点——按各自战斗状态
	// 分别判定的话，出口处会把队伍拆开。
	leaderExit := exits[leader.session.playerID]
	destMap := int32(10004)
	destX, destY := mainCityReturnSpawn()
	if leaderExit.activity != nil && leaderExit.activity.ReturnMap != 0 {
		destMap = leaderExit.activity.ReturnMap
		destX, destY = leaderExit.activity.ReturnX, leaderExit.activity.ReturnY
	} else {
		for _, member := range members {
			if member != nil && member.session != nil {
				s.grantBeachReturnProgress(member)
			}
		}
	}
	s.changeMapForParty(members, destMap, destX, destY)
	log.Printf("[S=%d] quit battle for party leader=%d members=%d -> map=%d",
		leader.id, leader.session.playerID, len(members), destMap)
}

func (s *Server) onQuitBattle(ch *channel, req *protocol.C2M_QuitBattle) {
	ss := ch.session
	if ss == nil || ss.playerID == 0 {
		return
	}

	// 组队战斗：整队一起退出，且只有队长可以触发；队员点击只回提示。
	if members := s.partyBattleChannels(ch); len(members) > 1 {
		// 回响应帧（带 RpcId，客户端 Call 按 RpcId 完成，避免挂起）。
		if req.RpcId != 0 {
			s.sendRawPush(ch, protocol.OpC2M_QuitBattle, pbAppendVarint(nil, 90, uint64(req.RpcId)))
		}
		if !s.partyBattleExitOwner(ch) {
			s.sendTeamStatusTip(ch, teamMemberQuitBattleBlockedMessage)
			log.Printf("[S=%d] reject quit battle player=%d: party member cannot exit for the whole team",
				ch.id, ss.playerID)
			return
		}
		s.quitBattleForParty(ch, members)
		return
	}

	exit := s.clearBattleForTransition(ch, "quit")

	// 回响应帧（带 RpcId，客户端 Call 按 RpcId 完成，避免挂起）。
	if req.RpcId != 0 {
		s.sendRawPush(ch, protocol.OpC2M_QuitBattle, pbAppendVarint(nil, 90, uint64(req.RpcId)))
	}
	// The native button can resend 20171 while the first scene transition is
	// still loading. Once all server-side combat flags are gone, acknowledge the
	// duplicate but never issue another ChangeMap/loading sequence.
	if !exit.hadFightState {
		return
	}
	if exit.wasPVP {
		return
	}
	// 线上新手流程允许移动端在第四层进入战斗后退出，以替代 Ctrl+G 回城。
	beachTutorialReturn := ss.mapID == 1000604 && ss.tasks[10016] == taskStateRunning
	// 活动战斗保留活动定义的返回点；其他 PVE 战斗（包括普通主线、世界
	// BOSS、试炼、手工副本和家族 BOSS）从战斗界面退出都回主城左侧入口。
	if s.returnFromActivity(ch, exit.activity) {
		// Activity scenes return to the exact map and position saved on entry.
	} else if beachTutorialReturn {
		s.grantBeachReturnProgress(ch)
		x, y := mainCityReturnSpawn()
		s.changeMap(ch, 10004, x, y)
	} else if exit.wasTrial || exit.wasManual || exit.wasFamilyBoss {
		x, y := mainCityReturnSpawn()
		s.changeMap(ch, 10004, x, y)
	} else {
		x, y := mainCityReturnSpawn()
		s.changeMap(ch, 10004, x, y)
	}
	s.saveData(ch)
	log.Printf("[S=%d] quit battle hp=%d mp=%d -> relog map=%d", ch.id, ss.hp, ss.mp, ss.mapID)
}

// pushHealth：推送当前血蓝（20169，NumericType 1001=Hp / 1003=Mp）。
func (s *Server) pushHealth(ch *channel) {
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		return
	}
	ss := ch.session
	push := func(t int32, v float32) {
		s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: ss.playerID, NumericType: t, Value: v, ActorId: ss.playerID,
		})
	}
	push(1001, float32(ss.hp))
	push(1003, float32(ss.mp))
}

// 20365 → 20366：主线 AI 托管开始（等价点击字段怪进主线战斗）。
func (s *Server) onStartMainStoryAI(ch *channel, req *protocol.C2M_StartMainStoryAI) proto.Message {
	resp := &protocol.M2C_StartMainStoryAI{RpcId: req.RpcId}
	if ch.session.battleHP() <= 0 {
		resp.Message = battleEntryHealthMessage
		return resp
	}
	region := mainStoryRegionForSession(ch.session, req.Index)
	if _, message := s.mainStoryAIEnergyFailure(ch, region); message != "" {
		resp.Message = message
		return resp
	}
	ok, n, failureMessage := s.startMainStoryFightByRegionWithReason(ch, region)
	if !ok {
		s.sendMainStoryAIStopTip(ch, failureMessage)
		if failureMessage != "" {
			resp.Message = failureMessage
			return resp
		}
		resp.Message = "开始战斗失败"
		return resp
	}
	ch.session.idleBattle = false
	ch.session.battleMu.Lock()
	ch.session.mainStoryAIRunning = true
	ch.session.mainStoryAIEpoch++
	ch.session.battleMu.Unlock()
	// 进入战斗后自动开自动战斗
	s.armMainStoryAIAutoBattle(ch)
	log.Printf("[S=%d] start main story AI region=%d monsters=%d", ch.id, region, n)
	return resp
}

// 20367 → 20368：主线 AI 托管结束。
func (s *Server) onEndMainStoryAI(ch *channel, req *protocol.C2M_EndMainStoryAI) proto.Message {
	resp := &protocol.M2C_EndMainStoryAI{RpcId: req.RpcId}
	s.stopCombatRunnerStayOnMap(ch, "main-story-ai-ended")
	log.Printf("[S=%d] end main story AI", ch.id)
	return resp
}

// 20087 -> 20088: start idle fight. C2M_StartBattleIdleFight.SceneId (tag1)
// is a scene id; region is derived from the current map, same as main-story.
func (s *Server) onStartBattleIdleFight(ch *channel, req *protocol.C2M_StartBattleIdleFight) proto.Message {
	resp := &protocol.M2C_StartBattleIdleFight{RpcId: req.RpcId}
	if ch.session.battleHP() <= 0 {
		resp.Message = battleEntryHealthMessage
		return resp
	}
	region := mainStoryRegionForSession(ch.session, req.SceneId)
	if ch.session.energy < battleEnergyCost(10010) {
		resp.Message = "体力不足"
		return resp
	}
	ok, n := s.startMainStoryFightByRegionWithPresentation(ch, region, battlePresentation{
		kind: presentationMainStory, copyID: 10010,
	})
	if !ok {
		resp.Message = "开始战斗失败"
		return resp
	}
	ch.session.idleBattle = true
	epoch := ch.session.setAutoBattle(true)
	go s.runAutoBattle(ch, epoch)
	log.Printf("[S=%d] start battle idle fight region=%d monsters=%d", ch.id, region, n)
	return resp
}

// 20089 → 20090：挂机战斗结束。
func (s *Server) onEndBattleIdleFight(ch *channel, req *protocol.C2M_EndBattleIdleFight) proto.Message {
	resp := &protocol.M2C_EndBattleIdleFight{RpcId: req.RpcId}
	s.stopCombatRunnerStayOnMap(ch, "idle-battle-ended")
	log.Printf("[S=%d] end battle idle fight", ch.id)
	return resp
}

func battleHasPendingVictory(battle *battleState) bool {
	if battle == nil || battle.pvp != nil || battle.playerDefeated {
		return false
	}
	if battle.party != nil {
		battle.party.mu.Lock()
		aborted := battle.party.aborted
		battle.party.mu.Unlock()
		if aborted {
			return false
		}
	}
	return battle.ended && len(battle.aliveMonsters()) == 0
}

func (s *Server) combatStayOnMapMembers(ch *channel) []*channel {
	if ch == nil || ch.session == nil {
		return nil
	}
	ch.session.battleMu.Lock()
	battle := ch.session.battle
	ch.session.battleMu.Unlock()
	if battle != nil && battle.party != nil {
		if members := battle.party.channels(s); len(members) > 0 {
			return members
		}
	}
	return []*channel{ch}
}

// stopCombatRunnerStayOnMap stops 20365/20087 runners without leaving the
// current map. A pending victory stays enrolled so every member still receives
// BattleVictory (and HUD Reduce) before ChangeMap. An in-progress fight is
// aborted for the whole party so MyUnit.IsFight can clear.
func (s *Server) stopCombatRunnerStayOnMap(ch *channel, reason string) {
	if ch == nil || ch.session == nil {
		return
	}
	members := s.combatStayOnMapMembers(ch)
	ch.session.battleMu.Lock()
	pendingVictory := battleHasPendingVictory(ch.session.battle)
	ch.session.battleMu.Unlock()
	for _, member := range members {
		if member == nil || member.session == nil {
			continue
		}
		member.session.battleMu.Lock()
		member.session.invalidateMainStoryAILocked()
		member.session.idleBattle = false
		syncBattleHealthToSession(member.session)
		member.session.battleMu.Unlock()
	}
	if pendingVictory {
		log.Printf("[S=%d] stop combat runner reason=%s pending victory members=%d", ch.id, reason, len(members))
		return
	}
	s.abortCombatStayOnMap(ch, reason)
}

func (s *Server) abortCombatStayOnMap(ch *channel, reason string) {
	if ch == nil || ch.session == nil {
		return
	}
	ch.session.battleMu.Lock()
	battle := ch.session.battle
	ch.session.battleMu.Unlock()
	if battle == nil || battle.pvp != nil {
		return
	}
	members := s.combatStayOnMapMembers(ch)
	if battle.party != nil {
		battle.party.mu.Lock()
		battle.party.aborted = true
		battle.party.settled = true
		battle.party.mu.Unlock()
	}
	if battle.runtime != nil {
		s.emitCombatEvents(ch, battle, battle.runtime.Cancel())
	}
	for _, member := range members {
		if member == nil || member.session == nil {
			continue
		}
		member.session.battleMu.Lock()
		member.session.invalidateMainStoryAILocked()
		member.session.idleBattle = false
		if member.session.battle != nil {
			member.session.battle.ended = true
		}
		syncBattleHealthToSession(member.session)
		member.session.battle = nil
		mapID, x, y := member.session.mapID, member.session.x, member.session.y
		member.session.battleMu.Unlock()
		s.sendBattleDefeat(member, battle)
		s.pushHealth(member)
		// 中断挂机战斗同样写回残血，队友那格要跟着变（见 team_vital_sync.go）。
		s.scheduleTeamVitalSync(member)
		s.changeMap(member, mapID, x, y)
	}
	log.Printf("[S=%d] abort combat stay on map reason=%s members=%d", ch.id, reason, len(members))
}
