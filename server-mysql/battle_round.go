package main

import (
	"sort"
	"time"

	"mhqserver/protocol"
)

func (s *Server) lockCombatParticipants(ch *channel) func() {
	ch.session.battleMu.Lock()
	battle := ch.session.battle
	if battle == nil || battle.party == nil {
		return ch.session.battleMu.Unlock
	}
	party := battle.party
	ch.session.battleMu.Unlock()
	_, battles := party.settlementSnapshot()
	sessions := []*session{ch.session}
	for _, member := range battles {
		if member != nil && member.owner != nil && member.owner != ch.session {
			sessions = append(sessions, member.owner)
		}
	}
	sort.Slice(sessions, func(first, second int) bool { return sessions[first].playerID < sessions[second].playerID })
	for _, member := range sessions {
		member.battleMu.Lock()
	}
	return func() {
		for index := len(sessions) - 1; index >= 0; index-- {
			sessions[index].battleMu.Unlock()
		}
	}
}

func (s *Server) appendAutomaticCasts(wave *monsterAttackWave, now time.Time) {
	participants := []*channel{wave.origin}
	if wave.battle.party != nil {
		participants = wave.battle.party.channels(s)
	}
	for _, actor := range participants {
		ss := actor.session
		battle := ss.battle
		if battle == nil || battle.ended || battle.scenePending || battle.playerHP <= 0 || !ss.autoBattle || now.Before(ss.battleActionReadyAt) || now.Before(ss.autoBattleNextCastAt) {
			continue
		}
		battle.assemblingWave = wave
		for _, skillID := range ss.autoBattleSkillIDsLocked() {
			if _, cast := s.castSkillLockedWithLogAtHeld(actor, skillID, &protocol.M2C_UseMainUISkill{}, true, now, true); cast {
				ss.advanceAutoBattleCursorLocked(skillID)
				break
			}
		}
		battle.assemblingWave = nil
		ss.autoBattleNextCastAt = ss.battleActionReadyAt
	}
}

// playersAutoAttackAt lets every player keep the interval derived from their
// own Speed. Automatic casts used to be collected only when the independent
// six-second monster clock fired, which silently stretched 4000/4554ms player
// intervals back to six seconds.
func (s *Server) playersAutoAttackAt(ch *channel, battle *battleState, now time.Time) {
	wave := &monsterAttackWave{origin: ch, battle: battle}
	s.appendAutomaticCasts(wave, now)
	if len(wave.casts) != 0 {
		s.startAttackWave(wave)
	}
}

func (s *Server) schedulePlayerAttack(ch *channel, battle *battleState, plan SkillPlan, primary CombatUnitRef) {
	cast := monsterSkillCast{actor: ch, battle: battle, source: battle.runtime.Player(), primary: primary, skillID: plan.SkillID, plan: plan, active: true}
	if battle.assemblingWave != nil {
		battle.assemblingWave.casts = append(battle.assemblingWave.casts, cast)
		logMonsterAttackPhase(battle.assemblingWave, &cast, "start", "automatic_round")
		return
	}
	wave := &monsterAttackWave{origin: ch, battle: battle, casts: []monsterSkillCast{cast}}
	logMonsterAttackPhase(wave, &cast, "start", "manual")
	s.startAttackWave(wave)
}

func (s *Server) startAttackWave(wave *monsterAttackWave) {
	if len(wave.casts) == 0 {
		return
	}
	// Automatic party casts are collected under the shared round lock. Send
	// their PlaySkill packets only after the complete list is assembled so the
	// clients receive one contiguous action batch instead of seeing later
	// members start after an earlier cast's bookkeeping.
	for _, push := range wave.playerSkillPushes {
		for _, recipient := range s.battleRecipients(wave.origin, wave.battle) {
			s.sendPush(recipient, protocol.OpM2C_PlaySkill, &protocol.M2C_PlaySkill{
				UnitId: push.unitID, SkillId: push.skillID,
			})
		}
	}
	wave.registered = true
	wave.battle.runtime.state().inFlight++
	if wave.immediate {
		// 反击：没有施法动作也没有 windup。这里仍然只排一个 0 延迟阶段，不直接
		// 调用 launchMonsterAttackWave——发起方（命中结算）自己还握着战斗锁，
		// 而 launch/resolve 会重新加锁。
		s.scheduleCombatPhase(0, func() { s.launchMonsterAttackWave(wave) })
		return
	}
	s.scheduleCombatPhase(monsterAttackWindup, func() { s.launchMonsterAttackWave(wave) })
}

func (s *Server) emitAttackWaveResults(wave *monsterAttackWave, events []CombatEvent) {
	var regular, deaths, cooldowns []CombatEvent
	for _, event := range events {
		if event.Type == CombatEventDeath {
			deaths = append(deaths, event)
		} else if event.Type == CombatEventGlobalCooldown {
			cooldowns = append(cooldowns, event)
		} else {
			regular = append(regular, event)
		}
	}
	s.emitCombatEventsDeferredHP(wave.origin, wave.battle, regular)
	for _, event := range cooldowns {
		for _, recipient := range s.battleRecipients(wave.origin, wave.battle) {
			if event.Source == PlayerCombatUnit(recipient.session.playerID) {
				s.emitCombatEventsToChannel(recipient, wave.battle, []CombatEvent{event}, true, false)
			}
		}
	}
	s.syncCombatEventHP(wave.origin, wave.battle, events)
	s.emitCombatEventsDeferredHP(wave.origin, wave.battle, deaths)
}
