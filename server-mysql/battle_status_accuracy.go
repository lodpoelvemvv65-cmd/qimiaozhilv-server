package main

import "math"

func (battle *battleState) contestedStatusChance(configured float64, effects []SkillEffect, source, primary, holder CombatUnitRef) (float64, bool) {
	target, ok := battle.singleHarmfulStatusTarget(effects, source, primary, holder)
	if !ok {
		return configured, false
	}
	return battle.statusContestChance(configured, source, target), true
}

func (battle *battleState) singleHarmfulStatusTarget(effects []SkillEffect, source, primary, holder CombatUnitRef) (CombatUnitRef, bool) {
	var found CombatUnitRef
	for _, effect := range effects {
		if effect.Kind == SkillEffectStatus && statusPolarity(effect.Status) == CombatEffectHarmful {
			targets := battle.resolveSkillTargets(effect.Target, source, primary, holder)
			if len(targets) != 1 || (!found.IsZero() && found != targets[0]) {
				return CombatUnitRef{}, false
			}
			found = targets[0]
		}
		if effect.Kind == SkillEffectChance {
			continue
		}
		if nested, ok := battle.singleHarmfulStatusTarget(effect.Children, source, primary, holder); ok {
			if !found.IsZero() && found != nested {
				return CombatUnitRef{}, false
			}
			found = nested
		}
	}
	return found, !found.IsZero()
}

func (battle *battleState) statusContestChance(configured float64, source, target CombatUnitRef) float64 {
	if battle == nil || battle.runtime == nil || source == target {
		return clampStatusChance(configured)
	}
	// Online PvE captures use the configured probability directly in both
	// directions. Family boss skill 500041 landed 30 of 33 alive-player
	// opportunities despite every observed player carrying at least 130 direct
	// Resistance. The old contest formula predicts 41.3% and is incompatible
	// with that sample. PvP remains contested because both units are players.
	if battle.pvp == nil {
		return clampStatusChance(configured)
	}
	return battle.ratingContestChance(configured, source, target)
}

func (battle *battleState) ratingContestChance(configured float64, source, target CombatUnitRef) float64 {
	if battle == nil || battle.runtime == nil || source == target {
		return clampStatusChance(configured)
	}
	hit := battle.runtime.EffectivePercentAttribute(source, CombatAttributeHit)
	resistance := battle.runtime.EffectivePercentAttribute(target, CombatAttributeResistance)
	hit = math.Max(-99, hit)
	resistance = math.Max(-99, resistance)
	return clampStatusChance(configured * (100 + hit) / (100 + resistance))
}

func clampStatusChance(chance float64) float64 {
	if math.IsNaN(chance) || chance <= 0 {
		return 0
	}
	return math.Min(100, chance)
}
