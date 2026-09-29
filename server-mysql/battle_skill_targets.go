package main

import "math/rand"

func (battle *battleState) resolveSkillTargets(plan SkillTargetPlan, source, primary, holder CombatUnitRef) []CombatUnitRef {
	return battle.resolveSkillTargetsWithIntn(plan, source, primary, holder, CombatUnitRef{}, rand.Intn)
}

func (battle *battleState) resolveSkillTargetsWithIntn(plan SkillTargetPlan, source, primary, holder, castTarget CombatUnitRef, intn func(int) int) []CombatUnitRef {
	if battle == nil || battle.runtime == nil {
		return nil
	}
	if plan.Side == "" {
		if !primary.IsZero() {
			return []CombatUnitRef{primary}
		}
		return nil
	}
	if plan.Kind == SkillTargetSingle {
		return battle.resolveSingleSkillTarget(plan.Side, source, primary, holder, castTarget)
	}

	candidates := battle.skillTargetCandidates(plan.Side, source, primary)
	count := resolvedSkillTargetCount(plan, len(candidates), intn)
	if count == 0 {
		return nil
	}
	if !plan.Random {
		return candidates[:count]
	}
	selected := append([]CombatUnitRef(nil), candidates...)
	for index := 0; index < count; index++ {
		other := index + boundedIntn(intn, len(selected)-index)
		selected[index], selected[other] = selected[other], selected[index]
	}
	return selected[:count]
}

func (battle *battleState) resolveSingleSkillTarget(side SkillTargetSide, source, primary, holder, castTarget CombatUnitRef) []CombatUnitRef {
	var target CombatUnitRef
	switch side {
	case SkillTargetSelf:
		target = source
	case SkillTargetCastTarget:
		// The skill's own target when the caller still knows it. Modifier
		// callbacks pass the event source as `primary`, so they hand the
		// original cast target separately (310601's critical-heal shield must
		// land on the healed ally rather than on the healer).
		target = primary
		if !castTarget.IsZero() {
			target = castTarget
		}
	case SkillTargetEventSource:
		target = primary
	case SkillTargetHolder:
		target = holder
	default:
		target = primary
	}
	if target.IsZero() {
		return nil
	}
	if target == source || target == holder {
		unit, ok := battle.runtime.Unit(target)
		if !ok || !unit.Alive {
			return nil
		}
	} else if !battle.runtime.CanBeTargeted(target) {
		return nil
	}
	return []CombatUnitRef{target}
}

func (battle *battleState) skillTargetCandidates(side SkillTargetSide, source, primary CombatUnitRef) []CombatUnitRef {
	wantPlayers := side == SkillTargetAny ||
		(source.Side == CombatSidePlayer && side == SkillTargetAlly) ||
		(source.Side == CombatSideMonster && side == SkillTargetEnemy)
	wantMonsters := side == SkillTargetAny ||
		(source.Side == CombatSidePlayer && side == SkillTargetEnemy) ||
		(source.Side == CombatSideMonster && side == SkillTargetAlly)
	candidates := make([]CombatUnitRef, 0, len(battle.monsters)+len(battle.alliedPlayerRefs()))
	seen := make(map[CombatUnitRef]struct{}, cap(candidates))
	appendTarget := func(ref CombatUnitRef) {
		if ref.IsZero() || !battle.runtime.CanBeTargeted(ref) {
			return
		}
		if _, duplicate := seen[ref]; duplicate {
			return
		}
		seen[ref] = struct{}{}
		candidates = append(candidates, ref)
	}
	if wantPlayers {
		if primary.Side == CombatSidePlayer {
			appendTarget(primary)
		}
		for _, ref := range battle.alliedPlayerRefs() {
			appendTarget(ref)
		}
	}
	if wantMonsters {
		if primary.Side == CombatSideMonster {
			appendTarget(primary)
		}
		for _, monster := range battle.monsters {
			if monster != nil {
				appendTarget(MonsterCombatUnit(monster.id))
			}
		}
	}
	return candidates
}

func resolvedSkillTargetCount(plan SkillTargetPlan, available int, intn func(int) int) int {
	if available <= 0 {
		return 0
	}
	maximum := int(plan.MaxCount)
	if !plan.Random {
		if maximum <= 0 || maximum > available {
			return available
		}
		return maximum
	}
	if maximum < 0 {
		maximum = 0
	}
	if maximum > available {
		maximum = available
	}
	minimum := int(plan.MinCount)
	if minimum < 0 {
		minimum = 0
	}
	if minimum > maximum {
		minimum = maximum
	}
	return minimum + boundedIntn(intn, maximum-minimum+1)
}

func boundedIntn(intn func(int) int, limit int) int {
	if limit <= 1 || intn == nil {
		return 0
	}
	value := intn(limit) % limit
	if value < 0 {
		value += limit
	}
	return value
}
