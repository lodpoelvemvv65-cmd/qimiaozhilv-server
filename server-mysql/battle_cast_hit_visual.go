package main

// SkillLogic owns selectors, branches and explicit projectile pairs. Complete
// the older SkillConfig presentation only when a phase is absent. In particular,
// the original nurse projectile IDs differ between the two tables: adding a
// second projectile from SkillConfig would fire both weapons at once.
func withMissingCastAndHitVisuals(plan SkillPlan) SkillPlan {
	if tables == nil || skillPlanContainsProjectile(plan.Effects) {
		return plan
	}
	row := tables.skillConfig[int64(plan.SkillID)*100]
	castID, hitID := int32(num(row["EffectId"])), int32(num(row["HurtEffectId"]))
	castType := configuredEffectType(castID)
	// Non-projectile A/B assets have distinct attack and hit animations. Keep
	// them bound to the same resolved status holder, including random targets.
	if castID > 0 && castType != 1 && castType != 0 && hitID != castID {
		for _, id := range []int32{castID, hitID} {
			if id <= 0 || tables.effectConfig[int64(id)] == nil || skillEffectsContainVisualID(plan.Effects, id) {
				continue
			}
			if effects, ok := withSupportModifierVisual(plan.Effects, id); ok {
				plan.Effects = effects
			} else {
				plan.Effects = append(plan.Effects, SkillEffect{
					Kind: SkillEffectVisual, EffectID: id,
					Trigger: SkillTrigger{Scope: "skill", Event: 1},
					Target:  skillPlanPresentationTarget(plan.Effects),
				})
			}
		}
	}
	// A persistent C effect does not replace the skill's one-shot B effect.
	if castID == 0 && hitID > 0 && tables.effectConfig[int64(hitID)] != nil &&
		!skillEffectsContainVisualID(plan.Effects, hitID) && !skillPlanContainsOneShotVisual(plan.Effects) {
		if effects, ok := withSupportModifierVisual(plan.Effects, hitID); ok {
			plan.Effects = effects
		}
	}
	return plan
}

func skillPlanContainsOneShotVisual(effects []SkillEffect) bool {
	for _, effect := range effects {
		if effect.Kind == SkillEffectVisual && effect.EffectID > 0 && configuredEffectType(effect.EffectID) != 4 {
			return true
		}
		for _, children := range [][]SkillEffect{effect.Children, effect.Success, effect.Failure} {
			if skillPlanContainsOneShotVisual(children) {
				return true
			}
		}
	}
	return false
}
