package main

import "time"

func (s *Server) emitPlayerSkillEvents(ch *channel, battle *battleState, plan SkillPlan, events []CombatEvent) {
	if skillPlanDealsDamage(plan) && tables != nil {
		s.emitPlayerAttackPhases(ch, battle, plan, events)
		return
	}
	if tables == nil {
		s.emitCombatEvents(ch, battle, events)
		return
	}
	delay := int32(num(tables.skillConfig[int64(plan.SkillID)*100]["DelayTime"]))
	if delay <= 0 {
		s.emitCombatEvents(ch, battle, events)
		return
	}
	var immediate, visuals []CombatEvent
	for _, event := range events {
		effectType := int32(num(tables.effectConfig[int64(event.EffectID)]["EffectType"]))
		if event.Type == CombatEventVisual && (effectType == 2 || effectType == 3) {
			// The original common-effect handler ignores Time: wait on the
			// server, then send Time=0, as in the captured support casts.
			event.DelayMS = 0
			visuals = append(visuals, event)
		} else {
			immediate = append(immediate, event)
		}
	}
	s.emitCombatEvents(ch, battle, immediate)
	if len(visuals) == 0 {
		return
	}
	s.scheduleCombatPhase(time.Duration(delay)*time.Millisecond, func() {
		s.runBattlePresentationPhase(ch, battle, func() {
			var valid []CombatEvent
			for _, event := range visuals {
				_, sourceExists := battle.runtime.Unit(event.Source)
				target, targetExists := battle.runtime.Unit(event.Target)
				if sourceExists && targetExists && target.Alive {
					valid = append(valid, event)
				}
			}
			s.emitCombatEvents(ch, battle, valid)
		})
	})
}

func (s *Server) emitPlayerAttackPhases(ch *channel, battle *battleState, plan SkillPlan, events []CombatEvent) {
	row := tables.skillConfig[int64(plan.SkillID)*100]
	castID := int32(num(row["EffectId"]))
	delay := int32(num(row["DelayTime"]))
	var cast, impacts, results []CombatEvent
	for _, event := range events {
		if event.Type != CombatEventVisual {
			results = append(results, event)
			continue
		}
		kind := configuredEffectType(event.EffectID)
		if kind == 1 || event.EffectID == castID {
			cast = append(cast, event)
			if kind == 1 && event.DelayMS > delay {
				delay = event.DelayMS
			}
		} else {
			if kind != 4 {
				event.DelayMS = 0 // Common effects do not interpret Time as a delay.
			}
			impacts = append(impacts, event)
		}
	}
	s.emitCombatEvents(ch, battle, cast)
	// Combat state is authoritative as soon as the cast is accepted. Keep
	// damage, healing, deaths and resource changes on the original immediate
	// path; only the visual impact waits for the projectile travel time.
	s.emitCombatEvents(ch, battle, results)
	if len(impacts) == 0 {
		return
	}
	if delay <= 0 {
		s.emitCombatEvents(ch, battle, impacts)
		return
	}
	s.scheduleCombatPhase(time.Duration(delay)*time.Millisecond, func() {
		// Authoritative damage was already resolved. A fatal hit must still
		// show its impact and death before the delayed victory scene transition.
		s.runBattlePresentationPhaseWithEnding(ch, battle, true, func() {
			s.emitCombatEvents(ch, battle, impacts)
		})
	})
}

func (s *Server) runBattlePresentationPhase(ch *channel, battle *battleState, run func()) {
	s.runBattlePresentationPhaseWithEnding(ch, battle, false, run)
}

func (s *Server) runBattlePresentationPhaseWithEnding(ch *channel, battle *battleState, allowEnding bool, run func()) {
	if s == nil || ch == nil || ch.session == nil || battle == nil || run == nil {
		return
	}
	configStateMu.RLock()
	defer configStateMu.RUnlock()
	if s.closed.Load() {
		return
	}
	ss := ch.session
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	if ss.battle != battle || (!allowEnding && battle.ended) || battle.runtime == nil {
		return
	}
	if battle.pvp != nil {
		battle.pvp.mu.Lock()
		defer battle.pvp.mu.Unlock()
		if battle.pvp.settled && !allowEnding {
			return
		}
	}
	if battle.party != nil {
		battle.party.mu.Lock()
		defer battle.party.mu.Unlock()
		if battle.party.settled && !allowEnding {
			return
		}
	}
	run()
}
