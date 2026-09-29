package main

import (
	"log"
	"math/rand"
	"time"

	"mhqserver/protocol"
)

const monsterAttackWindup = time.Second

// Monster80's prefab is present in the online client bundle with only Idle
// and Hurt clips.  PlayAnimation_AttackEvent still calls PlayAtk unconditionally
// when it receives M2C_PlaySkill, so sending that packet makes the client throw
// a KeyNotFoundException.  Keep this compatibility check keyed by prefab (and
// not by one MonsterBase row): the same client model is reused by several
// normal and hard monster rows.
const monsterPrefabWithoutAttackAnimation int32 = 280

func monsterAttackAnimationAvailable(monsterID int32) bool {
	if tables == nil || tables.monsterBase == nil {
		// Unknown/test-only monsters keep the historical presentation behavior.
		return true
	}
	row := tables.monsterBase[int64(monsterID)]
	if row == nil {
		return true
	}
	return int32(num(row["PrefabId"])) != monsterPrefabWithoutAttackAnimation
}

type combatPhaseScheduler func(time.Duration, func())

type monsterSkillCast struct {
	actor         *channel
	battle        *battleState
	source        CombatUnitRef
	primary       CombatUnitRef
	skillID       int32
	plan          SkillPlan
	active        bool
	counter       bool
	hasProjectile bool
	impactEvents  []CombatEvent
	launchEvents  []CombatEvent
}

type monsterAttackWave struct {
	origin *channel
	battle *battleState
	casts  []monsterSkillCast
	// Automatic player casts are queued while the shared round is assembled
	// and emitted as one contiguous presentation batch before launch.
	playerSkillPushes []playerSkillPush
	registered        bool
	// immediate marks a wave the server raises on its own the instant a hit
	// lands (today only a counter). It only skips the windup: the basic attack
	// itself is presented exactly like any other one - cast animation omitted,
	// but its projectile keeps its own flight time and the impact still lands
	// when that flight ends (M2C_PlaySkillEffect.Time is the bullet's tween
	// duration, so a zeroed one would be an instant, invisible bullet).
	immediate bool
}

type playerSkillPush struct {
	unitID  int64
	skillID int32
}

func monsterSkillUsesSingleTarget(plan SkillPlan) bool {
	seenDamage, seenMulti := false, false
	var walk func([]SkillEffect)
	walk = func(effects []SkillEffect) {
		for _, effect := range effects {
			if effect.Kind == SkillEffectDamage {
				if effect.Target.Kind == SkillTargetSingle {
					seenDamage = true
				} else if effect.Target.Kind != "" {
					seenMulti = true
				}
			}
			walk(effect.Success)
			walk(effect.Failure)
			walk(effect.Children)
		}
	}
	walk(plan.Effects)
	return seenDamage && !seenMulti
}

func (s *Server) monstersAttack(ch *channel, battle *battleState) {
	s.monstersAttackAt(ch, battle, time.Now())
}

func (s *Server) monstersAttackAt(ch *channel, battle *battleState, now time.Time) {
	s.monstersAttackAtWithTick(ch, battle, now, true)
}

// monstersAttackAtWithoutTick is used by the combat ticker after it has
// already advanced timed effects for this tick. Keeping the monster action
// claim separate avoids advancing the shared runtime twice at one timestamp.
func (s *Server) monstersAttackAtWithoutTick(ch *channel, battle *battleState, now time.Time) {
	s.monstersAttackAtWithTick(ch, battle, now, false)
}

func (s *Server) monstersAttackAtWithTick(ch *channel, battle *battleState, now time.Time, advanceTimedEffects bool) {
	if battle == nil || battle.ended || battle.scenePending || battle.runtime == nil || ensureSkillLogicCatalog() != nil {
		return
	}
	roundAt := battle.monsterReadyAt
	if battle.party != nil {
		roundAt = battle.party.monsterReadyAt
	}
	interval := monsterPublicActionInterval(battle)
	if roundAt.IsZero() {
		roundAt = now
	} else if !now.Before(roundAt) {
		roundAt = roundAt.Add(now.Sub(roundAt) / interval * interval)
	}
	if !battle.claimMonsterActionWindow(now) {
		return
	}
	if advanceTimedEffects {
		tickEvents := battle.runtime.Tick()
		tickHooks, _ := battle.triggerTimedEventHooks(tickEvents)
		s.emitCombatEvents(ch, battle, append(tickEvents, tickHooks...))
	}

	wave := &monsterAttackWave{origin: ch, battle: battle}
	for _, monster := range battle.monsters {
		source := MonsterCombatUnit(monster.id)
		if !battle.runtime.CanCast(source) {
			continue
		}
		skillID := monsterSkillID(monster.monsterID)
		plan, err := skillLogicCatalog.Plan(skillID, 1)
		if err != nil {
			continue
		}
		plan = withMonsterSkillPresentation(plan)
		primary := battle.runtime.Player()
		if plan.TeamType == 2 {
			// Friendly/self monster skills keep their native target semantics.
			primary = source
		} else if battle.party != nil && monsterSkillUsesSingleTarget(plan) {
			// Enemy single-target skills rotate through living party members.
			// Multi-target resolution still expands from this valid primary via
			// resolveSkillTargets, preserving the skill's configured range.
			if target := battle.party.nextMonsterTarget(battle); !target.IsZero() {
				primary = target
			}
		}
		wave.casts = append(wave.casts, monsterSkillCast{
			actor: ch, battle: battle, source: source, primary: primary, skillID: skillID, plan: plan, active: true,
		})
		logMonsterAttackPhase(wave, &wave.casts[len(wave.casts)-1], "start", "ready")
		if monsterAttackAnimationAvailable(monster.monsterID) {
			for _, recipient := range s.battleRecipients(ch, battle) {
				s.sendPush(recipient, protocol.OpM2C_PlaySkill, &protocol.M2C_PlaySkill{
					UnitId: monster.id, SkillId: skillID,
				})
			}
		}
	}
	s.appendAutomaticCasts(wave, roundAt)
	s.startAttackWave(wave)
}

func (s *Server) scheduleCombatPhase(delay time.Duration, run func()) {
	if s != nil && s.combatScheduler != nil {
		s.combatScheduler(delay, run)
		return
	}
	time.AfterFunc(delay, run)
}

func (s *Server) launchMonsterAttackWave(wave *monsterAttackWave) {
	s.runMonsterAttackPhase(wave, func() {
		maxTravel := time.Duration(0)
		for index := range wave.casts {
			cast := &wave.casts[index]
			if cast.battle == nil {
				cast.battle, cast.actor = wave.battle, wave.origin
			}
			if cast.actor.session.battle != cast.battle || !cast.battle.runtime.CanCast(cast.source) {
				cast.active = false
				logMonsterAttackPhase(wave, cast, "cancel", "source_cannot_cast_before_launch")
				continue
			}
			if cast.counter && !cast.battle.runtime.CanBeTargeted(cast.primary) {
				cast.active = false
				logMonsterAttackPhase(wave, cast, "cancel", "counter_target_unavailable")
				continue
			}
			launches, impacts, travel := cast.battle.monsterProjectilePresentation(*cast)
			cast.hasProjectile = len(launches) != 0
			cast.launchEvents = launches
			cast.impactEvents = impacts
			logMonsterAttackPhase(wave, cast, "launch", "ready")
			if travel > maxTravel {
				maxTravel = travel
			}
		}
		for index := range wave.casts {
			cast := &wave.casts[index]
			if !cast.active {
				continue
			}
			for eventIndex := range cast.launchEvents {
				cast.launchEvents[eventIndex].DelayMS = int32(maxTravel / time.Millisecond)
			}
			s.emitCombatEvents(cast.actor, cast.battle, cast.launchEvents)
		}
		s.scheduleCombatPhase(maxTravel, func() { s.resolveMonsterAttackWave(wave) })
	})
}

func (s *Server) resolveMonsterAttackWave(wave *monsterAttackWave) {
	s.runMonsterAttackPhase(wave, func() {
		var healthEvents []CombatEvent
		for index := range wave.casts {
			cast := &wave.casts[index]
			if !cast.active {
				continue
			}
			if cast.actor != nil && cast.actor.session.battle != cast.battle {
				cast.active = false
				logMonsterAttackPhase(wave, cast, "cancel", "actor_left_battle")
				continue
			}
			logMonsterAttackPhase(wave, cast, "impact", "resolving")
			if cast.battle == nil {
				cast.battle, cast.actor = wave.battle, wave.origin
			}
			if cast.counter && (!cast.battle.runtime.CanCast(cast.source) || !cast.battle.runtime.CanBeTargeted(cast.primary)) {
				cast.active = false
				logMonsterAttackPhase(wave, cast, "cancel", "counter_unit_unavailable_at_impact")
				continue
			}
			ctx := newSkillExecutionContext(cast.battle, rand.Float64)
			ctx.counterAttack = cast.counter
			events, err := cast.battle.executeSkillPlanWithContext(cast.plan, cast.source, cast.primary, ctx)
			if cast.hasProjectile {
				events = withoutPresentedCombatVisuals(events, cast.launchEvents, cast.impactEvents)
			}
			if err == nil {
				recoveryEvents, _ := cast.battle.runtime.ApplyHealthRecovery(cast.source)
				events = append(events, recoveryEvents...)
			}
			if cast.battle.pvp != nil {
				syncPVPStateLocked(cast.battle)
			}
			healthEvents = append(healthEvents, cast.impactEvents...)
			healthEvents = append(healthEvents, events...)
			cast.active = false
		}
		s.emitAttackWaveResults(wave, healthEvents)
		if wave.registered {
			wave.battle.runtime.state().inFlight--
			wave.registered = false
		}
		s.settleCombatLocked(wave.origin, wave.battle)
	})
}

func (s *Server) runMonsterAttackPhase(wave *monsterAttackWave, run func()) {
	if s == nil || wave == nil || wave.origin == nil || wave.origin.session == nil || wave.battle == nil || run == nil {
		return
	}
	configStateMu.RLock()
	defer configStateMu.RUnlock()
	if s.closed.Load() {
		return
	}
	unlock := s.lockCombatParticipants(wave.origin)
	defer unlock()
	ss := wave.origin.session
	if wave.battle.pvp != nil {
		wave.battle.pvp.mu.Lock()
		defer wave.battle.pvp.mu.Unlock()
		if wave.battle.pvp.settled {
			cancelMonsterAttackWave(wave, "pvp_settled")
			return
		}
	}
	if wave.battle.party != nil {
		wave.battle.party.mu.Lock()
		defer wave.battle.party.mu.Unlock()
		if wave.battle.party.settled {
			cancelMonsterAttackWave(wave, "party_settled")
			return
		}
	}
	if ss.battle != wave.battle || wave.battle.ended {
		cancelMonsterAttackWave(wave, "battle_ended_or_replaced")
		return
	}
	run()
}

func logMonsterAttackPhase(wave *monsterAttackWave, cast *monsterSkillCast, phase, reason string) {
	log.Printf("[combat.attack] uid=%d battle=%p wave=%p source=%d:%d target=%d:%d skill=%d phase=%s reason=%s at=%s",
		wave.origin.session.playerID, wave.battle, wave, cast.source.Side, cast.source.ID,
		cast.primary.Side, cast.primary.ID, cast.skillID, phase, reason, time.Now().Format(time.RFC3339Nano))
}

func cancelMonsterAttackWave(wave *monsterAttackWave, reason string) {
	if wave.registered {
		wave.battle.runtime.state().inFlight--
		wave.registered = false
	}
	for index := range wave.casts {
		cast := &wave.casts[index]
		if cast.active {
			cast.active = false
			logMonsterAttackPhase(wave, cast, "cancel", reason)
		}
	}
}

func (battle *battleState) monsterProjectilePresentation(cast monsterSkillCast) ([]CombatEvent, []CombatEvent, time.Duration) {
	var launches, impacts []CombatEvent
	maxDelayMS := int32(0)
	var collect func([]SkillEffect, int32)
	collect = func(effects []SkillEffect, inheritedDelayMS int32) {
		for _, effect := range effects {
			switch effect.Kind {
			case SkillEffectDelay:
				collect(effect.Children, addDelayMS(inheritedDelayMS, effect.DelayMS))
			case SkillEffectProjectile:
				delayMS := addDelayMS(inheritedDelayMS, effect.DelayMS)
				projectiles := battle.skillVisualEvents(effect, cast.source, cast.primary, cast.primary, delayMS)
				launches = append(launches, projectiles...)
				if delayMS > maxDelayMS {
					maxDelayMS = delayMS
				}
				if effect.ImpactEffectID > 0 {
					for _, projectile := range projectiles {
						impact := projectile
						impact.DelayMS = projectileImpactDurationMS(effect)
						impact.EffectID = effect.ImpactEffectID
						impacts = append(impacts, impact)
					}
				}
			}
		}
	}
	for _, effect := range cast.plan.Effects {
		if effect.Trigger.Scope == "skill" && effect.Trigger.Event == 1 {
			collect([]SkillEffect{effect}, 0)
		}
	}
	return launches, impacts, time.Duration(maxDelayMS) * time.Millisecond
}

func withoutPresentedCombatVisuals(events []CombatEvent, batches ...[]CombatEvent) []CombatEvent {
	type visualKey struct {
		source, target CombatUnitRef
		effectID       int32
	}
	shown := make(map[visualKey]int)
	for _, batch := range batches {
		for _, event := range batch {
			shown[visualKey{event.Source, event.Target, event.EffectID}]++
		}
	}
	filtered := make([]CombatEvent, 0, len(events))
	for _, event := range events {
		key := visualKey{event.Source, event.Target, event.EffectID}
		if event.Type == CombatEventVisual && shown[key] > 0 {
			shown[key]--
			continue
		}
		filtered = append(filtered, event)
	}
	return filtered
}
