package main

import (
	"log"
	"time"
)

// Counter events request a basic attack; Amount is deliberately zero. Only
// the normal skill plan may calculate damage, at the attack's impact phase.
func (battle *battleState) applySkillCounter(effect SkillEffect, source, target CombatUnitRef, parent *SkillStatusPlan, ctx *skillExecutionContext) ([]CombatEvent, error) {
	if ctx.counterAttack && ctx.triggerEvent != 0 {
		return nil, nil // A counter's on-hit hooks must not start a counter chain.
	}
	key := battle.registerEffectMeta(effect.ModifierID, "counter", parent)
	if ctx.triggerEvent != 0 {
		if !battle.runtime.CanCast(source) || !battle.runtime.CanBeTargeted(target) || source == target {
			return nil, nil
		}
		event := battle.runtime.event(battle.runtime.clock.Now(), CombatEventCounter, source, target)
		event.Key = key
		return []CombatEvent{event}, nil
	}
	return battle.runtime.ApplyEffects(CombatEffectContext{Source: source, Target: target}, []CombatEffect{modifierEffectSpec(parent, EffectSpec{
		Key: key, Kind: CombatEffectCounter, Duration: statusDuration(parent),
		StackMode: stackMode(parent), MaxStacks: gameplayEffectMaxStacks(),
	})})
}

func (r *CombatRuntime) counterAttackReaction(holder, attacker CombatUnitRef, at time.Time) []CombatEvent {
	if holder == attacker || !r.CanCast(holder) || !r.CanBeTargeted(attacker) {
		return nil
	}
	for _, effect := range r.state().effects {
		if effect.kind == CombatEffectCounter && effect.target == holder {
			event := r.event(at, CombatEventCounter, holder, attacker)
			event.Key = effect.key
			return []CombatEvent{event}
		}
	}
	return nil
}

// counterAttackCast resolves the holder's own session and profession. In a
// party the channel that processed the incoming hit may belong to someone
// else, including the officer who originally supplied the counter buff.
func (s *Server) counterAttackCast(origin *channel, battle *battleState, event CombatEvent) (monsterSkillCast, bool) {
	if origin == nil || origin.session == nil || battle == nil || battle.runtime == nil || ensureSkillLogicCatalog() != nil {
		return monsterSkillCast{}, false
	}
	source, target := battle.runtime.normalize(event.Source), battle.runtime.normalize(event.Target)
	if source == target || source.Side == target.Side {
		return monsterSkillCast{}, false
	}
	actor, owner := origin, battle
	skillID := int32(500001) // Native monster basic attack; no damage fallback.
	player := source.Side == CombatSidePlayer || battle.pvp != nil
	if player {
		if source.Side == CombatSideMonster && battle.pvp != nil {
			owner = battle.pvp.members[source.ID]
			source, target = PlayerCombatUnit(source.ID), MonsterCombatUnit(target.ID)
		} else if unit, ok := battle.runtime.resolve(source); ok {
			owner = unit.battle
		} else {
			return monsterSkillCast{}, false
		}
		if origin.session.playerID != source.ID {
			actor = s.findChannelByPlayerID(source.ID)
		}
		if actor == nil || actor.session == nil {
			return monsterSkillCast{}, false
		}
		skillID = baseSkillOfJob(actor.session.jobID)
	}
	if owner == nil || owner.runtime == nil || actor.session.battle != owner || owner.ended || owner.scenePending ||
		!owner.runtime.CanCast(source) || !owner.runtime.CanBeTargeted(target) {
		return monsterSkillCast{}, false
	}
	plan, err := skillLogicCatalog.Plan(skillID, 1)
	if err != nil {
		log.Printf("[combat.counter] source=%d:%d target=%d:%d skill=%d rejected=%v", source.Side, source.ID, target.Side, target.ID, skillID, err)
		return monsterSkillCast{}, false
	}
	if player {
		plan = withConfiguredSkillPresentation(plan)
	} else {
		plan = withMonsterSkillPresentation(plan)
	}
	return monsterSkillCast{
		actor: actor, battle: owner, source: source, primary: target,
		skillID: skillID, plan: plan, active: true, counter: true,
	}, true
}

// scheduleCounterAttacks answers one landed hit with the holder's basic attack.
// A counter is just a normal attack that nobody cast: the wave starts with no
// 20075/20076 for the countering unit (20075 would make the client banner its
// SkillConfig name, i.e. 「普通攻击」, over the holder; 20076 would still play
// the attack animation) and no windup, so the attack is on the wire the moment
// the incoming projectile lands. Everything else is a plain basic attack: its
// own projectile is launched with the configured flight time and the impact
// effect and damage land when that flight ends.
func (s *Server) scheduleCounterAttacks(origin *channel, battle *battleState, events []CombatEvent) {
	wave := &monsterAttackWave{origin: origin, battle: battle, immediate: true}
	for _, event := range events {
		if event.Type != CombatEventCounter {
			continue
		}
		cast, ok := s.counterAttackCast(origin, battle, event)
		if !ok {
			continue
		}
		wave.casts = append(wave.casts, cast)
		logMonsterAttackPhase(wave, &cast, "start", "counter_basic_attack")
	}
	// Extra attacks do not spend MP or reset either normal-action cooldown.
	// startAttackWave holds settlement until their impact has been resolved.
	s.startAttackWave(wave)
}
