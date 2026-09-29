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
// 修复：清状态 + 推当前血蓝 + 【重进当前场景】(M2C_ChangeMap)——客户端 ChangeMapEvent
// 重建场景单位（清战斗单位）→ 战斗 UI 必然关闭；同时回带 RpcId 的帧让可能挂起的
// Session.Call 完成（ET 按 RpcId 匹配，不校验 opcode）。
func (s *Server) onQuitBattle(ch *channel, req *protocol.C2M_QuitBattle) {
	ss := ch.session
	if ss == nil || ss.playerID == 0 {
		return
	}
	ss.battleMu.Lock()
	exitedBattle := ss.battle
	if exitedBattle != nil && exitedBattle.pvp != nil {
		duel := exitedBattle.pvp
		duel.mu.Lock()
		winnerID := int64(0)
		for _, id := range duel.memberIDs {
			if id != ss.playerID {
				winnerID = id
				break
			}
		}
		s.finishPVPBattleLocked(duel, winnerID, "quit")
		duel.mu.Unlock()
		ss.battleMu.Unlock()
		if req.RpcId != 0 {
			s.sendRawPush(ch, protocol.OpC2M_QuitBattle, pbAppendVarint(nil, 90, uint64(req.RpcId)))
		}
		return
	}
	wasTrial := exitedBattle != nil && exitedBattle.battleType == trialBattleType
	wasFamilyBoss := exitedBattle != nil && exitedBattle.mapID < 0
	var activity *activityBattle
	if exitedBattle != nil {
		activity = exitedBattle.activity
	}
	syncBattleHealthToSession(ss) // 残血写回会话 → 非战斗吃药可恢复（主界面血条刷新）
	persistFamilyBossProgress(ss.familyID, exitedBattle)
	s.detachCurrentBattleLocked(ch, "quit")
	ss.autoBattle = false
	ss.idleBattle = false
	ss.autoBattleEpoch++
	ss.battleMu.Unlock()
	// 魔法球（战斗结束自动回满）：退出战斗也触发。
	s.applyMagicBallRecover(ch)

	// 回响应帧（带 RpcId，客户端 Call 按 RpcId 完成，避免挂起）。
	if req.RpcId != 0 {
		s.sendRawPush(ch, protocol.OpC2M_QuitBattle, pbAppendVarint(nil, 90, uint64(req.RpcId)))
	}
	// 家族 BOSS 事件会在客户端将 MyUnit.IsFight 设为 true。同地图 ChangeMap
	// 会复用该 Unit，不会重置战斗标记。BattleDefeat 的线上处理器会发布
	// BattleEndEvent -> PosHelper.BackPos，其中明确执行 MyUnit.IsFight=false。
	if exitedBattle != nil {
		s.sendBattleDefeat(ch, exitedBattle)
	}
	// 推当前血蓝（退出战斗后主界面血条显示真实值）。
	s.pushHealth(ch)
	// 试炼/家族 BOSS 退出都从主城左侧入口返回；普通战斗重进当前场景。
	if s.returnFromActivity(ch, activity) {
		// Activity scenes return to the exact map and position saved on entry.
	} else if wasTrial || wasFamilyBoss {
		x, y := mainCityReturnSpawn()
		s.changeMap(ch, 10004, x, y)
	} else {
		s.changeMap(ch, ss.mapID, ss.x, ss.y)
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
	if ch.session.energy < mainStoryEnergyCost(region) {
		resp.Message = "体力不足"
		return resp
	}
	ok, n := s.startMainStoryFightByRegion(ch, region)
	if !ok {
		resp.Message = "开始战斗失败"
		return resp
	}
	ch.session.idleBattle = false
	// 进入战斗后自动开自动战斗
	epoch := ch.session.setAutoBattle(true)
	go s.runAutoBattle(ch, epoch)
	log.Printf("[S=%d] start main story AI region=%d monsters=%d", ch.id, region, n)
	return resp
}

// 20367 → 20368：主线 AI 托管结束。
func (s *Server) onEndMainStoryAI(ch *channel, req *protocol.C2M_EndMainStoryAI) proto.Message {
	resp := &protocol.M2C_EndMainStoryAI{RpcId: req.RpcId}
	ch.session.idleBattle = false
	ch.session.setAutoBattle(false)
	ch.session.battleMu.Lock()
	syncBattleHealthToSession(ch.session)
	s.detachCurrentBattleLocked(ch, "main-story-ai-ended")
	ch.session.battleMu.Unlock()
	// 与退出战斗一致：推血蓝 + 重进场景关闭战斗 UI。
	s.pushHealth(ch)
	s.changeMap(ch, ch.session.mapID, ch.session.x, ch.session.y)
	log.Printf("[S=%d] end main story AI", ch.id)
	return resp
}

// 20087 → 20088：挂机战斗开始（region 由当前地图推演，同主线战斗）。
// 注：C2M_StartBattleIdleFight 字段名是 SceneId（tag1），语义为场景 id。
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
	ch.session.idleBattle = false
	ch.session.setAutoBattle(false)
	ch.session.battleMu.Lock()
	syncBattleHealthToSession(ch.session)
	s.detachCurrentBattleLocked(ch, "idle-battle-ended")
	ch.session.battleMu.Unlock()
	// 与退出战斗一致：推血蓝 + 重进场景关闭战斗 UI。
	s.pushHealth(ch)
	s.changeMap(ch, ch.session.mapID, ch.session.x, ch.session.y)
	log.Printf("[S=%d] end battle idle fight", ch.id)
	return resp
}
