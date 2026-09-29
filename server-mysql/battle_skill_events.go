package main

import (
	"log"
	"math"
	"time"

	"mhqserver/protocol"
)

func (s *Server) emitCombatEvents(ch *channel, battle *battleState, events []CombatEvent) {
	s.emitCombatEventsWithHP(ch, battle, events, true)
}

func (s *Server) emitCombatEventsDeferredHP(ch *channel, battle *battleState, events []CombatEvent) {
	s.emitCombatEventsWithHP(ch, battle, events, false)
}

func (s *Server) emitCombatEventsWithHP(ch *channel, battle *battleState, events []CombatEvent, syncHP bool) {
	assignCombatStateIDs(events)
	if battle != nil && battle.mapID < 0 && ch != nil && ch.session != nil {
		bossID := -battle.mapID
		for _, event := range events {
			if event.Type == CombatEventDamage && event.Target.Side == CombatSideMonster && event.Source.Side == CombatSidePlayer {
				recordFamilyBossDamage(ch.session.familyID, bossID, event.Source.ID, event.Amount)
			}
			if credit := familyBossTreatmentCredit(event); credit > 0 {
				recordFamilyBossTreatment(ch.session.familyID, bossID, event.Source.ID, credit)
			}
		}
	}
	for _, recipient := range s.battleRecipients(ch, battle) {
		s.emitCombatEventsToChannel(recipient, battle, events, recipient == ch, syncHP)
	}
	// A counter is an extra basic attack. Queue it once after broadcasting the
	// triggering hit, never once per recipient and never as reflected damage.
	s.scheduleCounterAttacks(ch, battle, events)
}

// familyBossTreatmentCredit 返回一个战斗事件应该记入家族BOSS「治疗量」的数值，0 = 不计。
//
// 只认「玩家治疗玩家」，并且**排除自己治自己**：
//
//   - `ApplyHealthRecovery`（数值属性 1044 生命回复，`battle_effects.go:1211`）走的是
//     `applyHealInternal(ref, ref, …)`，即 **自己→自己** 的 `CombatEventHeal`，
//     每次自己出手结算一次。它是数值属性的被动回血，不是「治疗队友」，
//     记进去会让只打了 303 点伤害的角色显示 7208 万治疗量（见 文档/34 §4/§15.8）。
//   - 吸血（`CombatEventLifesteal`）同理恒为自己→自己，也不是治疗队友。
//
// 2026-09-18 定稿：家族BOSS 面板的治疗量是「个人 / 全队」占比条，分母由客户端对
// 名单求和（文档/34 §15.1），所以口径错会直接扭曲那条进度条。
func familyBossTreatmentCredit(event CombatEvent) int32 {
	if event.Type != CombatEventHeal && event.Type != CombatEventLifesteal {
		return 0
	}
	if event.Source.Side != CombatSidePlayer || event.Target.Side != CombatSidePlayer {
		return 0
	}
	if event.Source == event.Target {
		return 0
	}
	if event.Amount <= 0 {
		return 0
	}
	return event.Amount
}

// emitCombatEventsToChannel 把战斗事件翻译成客户端帧。
//
// 这里刻意不发 M2C_BattleTouchState(20081)：抓包归档里 12 份含游戏流量的样本
// 合计 963 个 20080、604 个 20077、296 个 20078、303 个 20075，而 20081 一个
// 都没有；客户端 Hotfix.M2C_BattleTouchStateHandler.Run 直接 await
// ETTask.CompletedTask，全程不读消息体，是空实现。周期伤害的数值、飘字和血条
// 已经由 20078(M2C_BattleSkillRet) 与随后的 HP 同步包承担，状态的图标和续期由
// 20080(M2C_BattleChangeState) 承担，再多发一条没人读的包只会污染协议实测。
func (s *Server) emitCombatEventsToChannel(ch *channel, battle *battleState, events []CombatEvent, includeLocalCooldown, syncHP bool) {
	for index := 0; index < len(events); index++ {
		event := events[index]
		if event.Type == CombatEventAttributeChanged {
			end := index + 1
			for end < len(events) && events[end].Type == CombatEventAttributeChanged &&
				events[end].Target == event.Target && events[end].At.Equal(event.At) {
				end++
			}
			s.pushFamilyBossAttributeChanges(ch, events[index:end])
			index = end - 1
			continue
		}
		targetID := combatUnitID(event.Target)
		switch event.Type {
		case CombatEventVisual:
			if event.EffectID <= 0 {
				continue
			}
			s.sendPush(ch, protocol.OpM2C_PlaySkillEffect, &protocol.M2C_PlaySkillEffect{
				UnitId:           combatUnitID(event.Source),
				TargetId:         targetID,
				Time:             event.DelayMS,
				EffectPos:        event.EffectPos,
				EffectId:         event.EffectID,
				EffectTargetType: event.EffectTargetType,
				ActorId:          ch.session.playerID,
			})
		case CombatEventDamage:
			if event.Amount > 0 {
				s.sendPush(ch, protocol.OpM2C_BattleSkillRet, &protocol.M2C_BattleSkillRet{UnitId: targetID, ChangeHpValue: -event.Amount, IsCrit: event.IsCrit})
				// BattleSkillRet only drives hurt animation and floating text. Numeric
				// HP is authoritative and must be sent separately for the health bar.
				if syncHP {
					if event.Target.Side == CombatSideMonster {
						s.pushMonsterHPByID(ch, battle, targetID)
					} else {
						s.pushCombatHP(ch, targetID, event.HPAfter)
					}
				}
			}
		case CombatEventHeal, CombatEventLifesteal:
			if event.Amount > 0 {
				s.sendPush(ch, protocol.OpM2C_BattleSkillRet, &protocol.M2C_BattleSkillRet{UnitId: targetID, ChangeHpValue: event.Amount, IsCrit: event.IsCrit})
				if syncHP {
					if event.Target.Side == CombatSideMonster {
						s.pushMonsterHPByID(ch, battle, targetID)
					} else {
						s.pushCombatHP(ch, targetID, event.HPAfter)
					}
				}
			}
		case CombatEventEffectApplied, CombatEventEffectRefreshed, CombatEventEffectStacked:
			if event.Hidden {
				continue
			}
			s.emitCombatState(ch, battle, event, protocol.ChangeType_Add)
		case CombatEventEffectRemoved, CombatEventEffectDispelled:
			if event.Hidden {
				continue
			}
			// Cancel() 在战斗结束时移除全部状态。线上不发 Time=0，客户端按
			// Add 包里的 Time 自行销毁图标与特效（盾牌 EffectId=2109 为 10 秒），
			// 因此这里不为 "battle-ended" 强制把技能 Buff 时间改成 0。
			if event.Reason == "battle-ended" {
				continue
			}
			s.emitCombatState(ch, battle, event, protocol.ChangeType_Reduce)
		case CombatEventEffectExpired:
			// The client starts its own timer from the Add packet. Sending a
			// Reduce packet at natural expiry would insert the same buff again.
			s.forgetCombatEffectIcons(ch, targetID, event.Key)
		case CombatEventResourceClamped:
			if syncHP {
				s.pushCombatHP(ch, targetID, event.HPAfter)
			}
		case CombatEventDeath:
			if event.Target.Side == CombatSideMonster && battle.pvp == nil {
				if battle.selectedID == event.Target.ID {
					battle.selectedID = 0
				}
				s.sendPush(ch, protocol.OpM2C_MainstoryMonsterDead, &protocol.M2C_MainstoryMonsterDead{
					UnitId: targetID, ActorId: ch.session.playerID,
				})
			} else {
				s.sendPush(ch, protocol.OpM2C_UnitDead, &protocol.M2C_UnitDead{
					UnitId: targetID, ActorId: ch.session.playerID,
				})
			}
		case CombatEventGlobalCooldown:
			if !includeLocalCooldown {
				continue
			}
			now := time.Now()
			seen := make(map[int32]struct{}, len(ch.session.skillOrder))
			for _, skillID := range ch.session.skillOrder {
				if _, duplicate := seen[skillID]; duplicate {
					continue
				}
				seen[skillID] = struct{}{}
				remaining := int32(0)
				if readyAt, ok := ch.session.skillCooldowns[skillID]; ok && readyAt.After(now) {
					remaining = int32(minInt64(math.MaxInt32, readyAt.Sub(now).Milliseconds()))
				}
				s.sendPush(ch, protocol.OpM2C_StartCD, &protocol.M2C_StartCD{Id: skillID, SkillCD: remaining, Type: protocol.MainUIType_SkillSlot})
			}
		}
	}
}

func (s *Server) syncCombatEventHP(ch *channel, battle *battleState, events []CombatEvent) {
	order := make([]CombatUnitRef, 0)
	values := make(map[CombatUnitRef]int32)
	for _, event := range events {
		changesHP := (event.Type == CombatEventDamage && event.Amount > 0) ||
			((event.Type == CombatEventHeal || event.Type == CombatEventLifesteal) && event.Amount > 0) ||
			event.Type == CombatEventResourceClamped
		if !changesHP || event.Target.IsZero() {
			continue
		}
		if _, seen := values[event.Target]; !seen {
			order = append(order, event.Target)
		}
		values[event.Target] = event.HPAfter
	}
	for _, recipient := range s.battleRecipients(ch, battle) {
		for _, target := range order {
			if target.Side == CombatSideMonster {
				s.pushMonsterHPByID(recipient, battle, combatUnitID(target))
			} else {
				s.pushCombatHP(recipient, combatUnitID(target), values[target])
			}
		}
	}
	// 队伍战斗的收件人是全队，本来就同步；单人打的怪收件人只有自己，队友的头像
	// 看不到战斗中的掉血。额外排一次补推（合并 50ms，见 team_vital_sync.go）。
	if battle.party == nil {
		for _, target := range order {
			if target.Side != CombatSideMonster {
				s.scheduleTeamVitalSync(ch)
				break
			}
		}
	}
}

func (s *Server) pushCombatHP(ch *channel, unitID int64, hp int32) {
	if ch == nil || ch.session == nil || unitID == 0 {
		return
	}
	if hp < 0 {
		hp = 0
	}
	maxHP := int32(0)
	if target := s.findChannelByPlayerID(unitID); target != nil && target.session != nil {
		maxHP = target.session.playerMaxHp()
		if target.session.battle != nil && target.session.battle.playerMaxHP > 0 {
			maxHP = target.session.battle.playerMaxHP
		}
		s.syncTeamHeadHP(unitID, hp, maxHP)
	}
	s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
		UnitId: unitID, NumericType: 1001, Value: float32(hp), ActorId: ch.session.playerID,
	})
}

func (s *Server) emitCombatState(ch *channel, battle *battleState, event CombatEvent, change protocol.ChangeType) {
	// The client's monster UnitFactory overloads do not attach BuffComponent,
	// while the 20080 handler dereferences it without a nil check.
	if event.Target.Side != CombatSidePlayer {
		return
	}
	meta := battle.effectMeta[event.Key]
	iconID, ok := normalizeClientBuffIcon(meta.iconID)
	if !ok {
		// Damage-only/internal modifiers commonly use IconID "0". Sending them
		// creates blank entries because the client always looks in Skill_fui.
		return
	}
	durationMS := meta.durationMS
	targetID := combatUnitID(event.Target)
	if change == protocol.ChangeType_Add {
		stateID := event.StateID
		if stateID == 0 {
			stateID = nextCombatStateID()
		}
		s.sendPush(ch, protocol.OpM2C_BattleChangeState, &protocol.M2C_BattleChangeState{
			Id: stateID, TargetUnitId: targetID, IconId: iconID,
			IconDesc: meta.iconDesc, Type: change, Time: durationMS, IsBuff: meta.isBuff,
			ActorId: ch.session.playerID,
		})
		// The forced clear has to mirror this exact frame, so remember every
		// field the client stores under (unit, Id).
		s.trackCombatIcon(ch, targetID, stateID, combatIconState{
			effectKey: event.Key, iconID: iconID, iconDesc: meta.iconDesc, isBuff: meta.isBuff,
			expiresAt: time.Now().Add(time.Duration(durationMS) * time.Millisecond),
		})
		if battle.combatStatesSettled() {
			// A delayed impact can apply a state after the fight was settled. The
			// client keeps icons until its own 500ms timer removes them, so drop
			// every state this session still holds instead of leaving a combat icon
			// in the field until its configured duration elapses.
			if cleared := s.clearPlayerBattleStates(ch); cleared > 0 {
				log.Printf("[S=%d] cleared %d combat states applied after settlement", ch.id, cleared)
			}
		}
		return
	}
	// Each application has its own dictionary key in the original client.
	// A dispel/replacement must therefore expire every still-live instance of
	// this effect rather than inventing one modifier-level key.
	for _, instance := range s.takeCombatEffectIcons(ch, targetID, event.Key) {
		s.sendPush(ch, protocol.OpM2C_BattleChangeState, &protocol.M2C_BattleChangeState{
			Id: instance.key.id, TargetUnitId: targetID, IconId: instance.state.iconID,
			IconDesc: instance.state.iconDesc, Type: change, Time: 0, IsBuff: instance.state.isBuff,
			ActorId: ch.session.playerID,
		})
	}
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func combatUnitID(ref CombatUnitRef) int64 { return ref.ID }

// recoverPlayerAfterDefeat establishes the non-zero post-defeat baseline before
// persistent magic balls refill either resource. The battle remains attached so
// setBattleHP/setBattleMP update both the combat state and persisted session.
func (s *Server) recoverPlayerAfterDefeat(ch *channel, battle *battleState) {
	if ch == nil || ch.session == nil || battle == nil {
		return
	}
	logCombatHP(ch.session, "defeat", "before-recover")
	battle.playerHP = 1
	syncBattleHealthToSession(ch.session)
	s.applyMagicBallRecover(ch)
	logCombatHP(ch.session, "defeat-recover", "after-recover")
}

func (s *Server) settleCombatLocked(ch *channel, battle *battleState) bool {
	if battle == nil || battle.ended {
		return true
	}
	if battle.pvp != nil {
		return s.settlePVPCombatLocked(ch, battle)
	}
	if battle.runtime != nil && battle.runtime.state().inFlight > 0 {
		return false
	}
	if battle.party != nil {
		return s.settlePartyCombatLocked(ch, battle)
	}
	if battle.playerHP <= 0 {
		battle.playerHP = 0
		if battle.runtime != nil {
			s.emitCombatEvents(ch, battle, battle.runtime.Cancel())
		}
		battle.ended = true
		persistFamilyBossProgress(ch.session.familyID, battle)
		s.settleFamilyBossBattle(ch, battle, false)
		ch.session.invalidateMainStoryAILocked()
		s.recoverPlayerAfterDefeat(ch, battle)
		s.sendBattleDefeat(ch, battle)
		ch.session.battle = nil
		ch.session.idleBattle = false
		s.pushHealth(ch)
		// 复活后的血量同样要让队友那格跟上（单人战斗里 pushHealth 只推自己）。
		s.scheduleTeamVitalSync(ch)
		x, y := mainCityReturnSpawn()
		s.changeMap(ch, 10004, x, y)
		return true
	}
	if len(battle.aliveMonsters()) == 0 {
		if battle.runtime != nil {
			s.emitCombatEvents(ch, battle, battle.runtime.Cancel())
		}
		battle.ended = true
		// Callers already hold battleMu. Invalidate the automatic worker without
		// recursively taking the same mutex. 胜利【保留 autoBattle 开关】：
		// 自动战斗持续——打完当前怪，玩家点下一个怪时 finishStartBattle
		// 重新拉起 worker（用户实测"打怪结束后打下一个不自动了"）。
		ch.session.autoBattleEpoch++
		if battleVictoryDelay <= 0 {
			s.emitVictory(ch, battle)
			return true
		}
		log.Printf("[S=%d] all monsters dead; victory in %s", ch.id, battleVictoryDelay)
		time.AfterFunc(battleVictoryDelay, func() {
			configStateMu.RLock()
			defer configStateMu.RUnlock()
			ss := ch.session
			if ss == nil {
				return
			}
			ss.battleMu.Lock()
			defer ss.battleMu.Unlock()
			if ss.battle != battle || !battle.ended {
				return
			}
			if s.closed.Load() {
				ss.battle = nil
				return
			}
			s.emitVictory(ch, battle)
		})
		return true
	}
	return false
}

func (s *Server) combatTickerLoop() {
	ticker := time.NewTicker(combatTickerInterval)
	defer ticker.Stop()
	for range ticker.C {
		configStateMu.RLock()
		if s.closed.Load() {
			configStateMu.RUnlock()
			return
		}
		s.mu.RLock()
		channels := make([]*channel, 0, len(s.conns))
		for _, ch := range s.conns {
			channels = append(channels, ch)
		}
		s.mu.RUnlock()
		for _, ch := range channels {
			s.combatTickChannelAt(ch, time.Now())
		}
		configStateMu.RUnlock()
	}
}

// combatTickChannelAt advances timed effects and the monsters' independent
// action clock. Player input is not a prerequisite: disabling automatic battle
// stops only player casts, while living monsters continue attacking on cadence.
//
// 玩家手动出手不会被这里排队 —— castSkillLocked 成功即立刻生成自己的 wave
// （schedulePlayerAttack 的 "manual" 分支），失败就直接拒绝。
func (s *Server) combatTickChannelAt(ch *channel, now time.Time) {
	if ch == nil || ch.session == nil {
		return
	}
	unlock := s.lockCombatParticipants(ch)
	defer unlock()
	ss := ch.session
	battle := ss.battle
	if battle == nil || battle.ended {
		return
	}
	if battle.runtime == nil {
		battle.owner = ss
		battle.runtime = NewCombatRuntime(battle, ss.playerID, nil)
	}
	if battle.pvp != nil {
		battle.pvp.mu.Lock()
		defer battle.pvp.mu.Unlock()
	}
	if battle.party != nil {
		battle.party.mu.Lock()
		defer battle.party.mu.Unlock()
	}
	events := battle.runtime.Tick()
	hooks, hookErr := battle.triggerTimedEventHooks(events)
	if hookErr != nil {
		log.Printf("[S=%d] timed modifier hook: %v", ch.id, hookErr)
	}
	events = append(events, hooks...)
	if battle.pvp != nil {
		syncPVPStateLocked(battle)
	}
	s.emitCombatEvents(ch, battle, events)
	if s.settleCombatLocked(ch, battle) || battle.pvp != nil {
		return
	}
	s.monstersAttackAtWithoutTick(ch, battle, now)
	s.playersAutoAttackAt(ch, battle, now)
}
