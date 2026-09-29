package main

import "math"

// Modifier effects are a separate client playback path, including monster
// auras and secondary control effects attached to a projectile's damage.
func withModifierSkillPresentation(plan SkillPlan) SkillPlan {
	if tables == nil || tables.effectConfig == nil {
		return plan
	}
	plan.Effects = withModifierVisuals(plan.Effects, 0)
	return plan
}

func withModifierVisuals(effects []SkillEffect, projectileImpactID int32) []SkillEffect {
	result := make([]SkillEffect, 0, len(effects))
	for _, effect := range effects {
		impactID := projectileImpactID
		if effect.Kind == SkillEffectProjectile {
			impactID = effect.ImpactEffectID
		}
		effect.Success = withModifierVisuals(effect.Success, impactID)
		effect.Failure = withModifierVisuals(effect.Failure, impactID)
		effect.Children = withModifierVisuals(effect.Children, impactID)
		status := effect.Status
		if effect.Kind == SkillEffectStatus && status != nil && status.EffectID > 0 &&
			status.EffectID != impactID && tables.effectConfig[int64(status.EffectID)] != nil &&
			!skillEffectsContainVisualID(effect.Children, status.EffectID) {
			effect.Children = append([]SkillEffect{{
				Kind: SkillEffectVisual, EffectID: status.EffectID,
				EffectAttachType: status.EffectAttachType,
				Trigger:          SkillTrigger{Scope: "modifier", Event: 0, ModifierID: status.ModifierID},
				Target:           SkillTargetPlan{Kind: SkillTargetSingle, Side: SkillTargetHolder},
			}}, effect.Children...)
		}
		result = append(result, effect)
	}
	return result
}

func skillEffectsContainVisualID(effects []SkillEffect, id int32) bool {
	for _, effect := range effects {
		if (effect.Kind == SkillEffectVisual || effect.Kind == SkillEffectProjectile) && effect.EffectID == id {
			return true
		}
		if skillEffectsContainVisualID(effect.Success, id) || skillEffectsContainVisualID(effect.Failure, id) ||
			skillEffectsContainVisualID(effect.Children, id) {
			return true
		}
	}
	return false
}

func configuredEffectType(id int32) int32 {
	if tables == nil {
		return 0
	}
	return int32(num(tables.effectConfig[int64(id)]["EffectType"]))
}

func modifierVisualDurationMS(status *SkillStatusPlan) int32 {
	if status == nil {
		return 0
	}
	return int32(minInt64(math.MaxInt32, maxInt64(0, secondsDuration(status.DurationSeconds).Milliseconds())))
}

func projectileImpactDurationMS(effect SkillEffect) int32 {
	if configuredEffectType(effect.ImpactEffectID) != 4 {
		return 0
	}
	var find func([]SkillEffect) *SkillStatusPlan
	find = func(effects []SkillEffect) *SkillStatusPlan {
		for _, child := range effects {
			if child.Status != nil && child.Status.EffectID == effect.ImpactEffectID {
				return child.Status
			}
			if status := find(child.Children); status != nil {
				return status
			}
		}
		return nil
	}
	return modifierVisualDurationMS(find(effect.Children))
}
