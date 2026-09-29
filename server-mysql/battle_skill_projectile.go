package main

import "time"

func withConfiguredSkillPresentation(plan SkillPlan) SkillPlan {
	if !skillPlanCanCast(plan) {
		return plan
	}
	plan = withModifierSkillPresentation(plan)
	plan = withConfiguredBasicAttackProjectile(plan)
	plan = withMissingCastAndHitVisuals(plan)
	if skillPlanContainsVisual(plan.Effects) ||
		tables == nil || tables.skillConfig == nil || tables.effectConfig == nil {
		return plan
	}
	row := tables.skillConfig[int64(plan.SkillID)*100]
	if row == nil {
		return plan
	}
	target := skillPlanPresentationTarget(plan.Effects)
	effectID := int32(num(row["EffectId"]))
	hurtEffectID := int32(num(row["HurtEffectId"]))
	if !skillPlanDealsDamage(plan) {
		visualID := effectID
		if tables.effectConfig[int64(visualID)] == nil {
			visualID = hurtEffectID
		}
		if visualID > 0 && tables.effectConfig[int64(visualID)] != nil {
			if effects, ok := withSupportModifierVisual(plan.Effects, visualID); ok {
				plan.Effects = effects
				return plan
			}
		}
	}
	if effectID > 0 && tables.effectConfig[int64(effectID)] != nil {
		effect := SkillEffect{
			Trigger: SkillTrigger{Scope: "skill", Event: 1}, Target: target,
			EffectID: effectID,
		}
		if int32(num(tables.effectConfig[int64(effectID)]["EffectType"])) == 1 {
			effect.Kind = SkillEffectProjectile
			effect.DelayMS = int32(num(row["DelayTime"]))
			if hurtEffectID > 0 && tables.effectConfig[int64(hurtEffectID)] != nil {
				effect.ImpactEffectID = hurtEffectID
			}
		} else {
			effect.Kind = SkillEffectVisual
		}
		plan.Effects = append([]SkillEffect{effect}, plan.Effects...)
		return plan
	}
	if hurtEffectID > 0 && tables.effectConfig[int64(hurtEffectID)] != nil {
		plan.Effects = append([]SkillEffect{{
			Kind: SkillEffectVisual, Trigger: SkillTrigger{Scope: "skill", Event: 1},
			Target: target, EffectID: hurtEffectID,
		}}, plan.Effects...)
	}
	return plan
}

// Place a support visual on the applied modifier's holder. Copying a random
// selector into an independent visual node would roll a second set of allies.
func withSupportModifierVisual(effects []SkillEffect, effectID int32) ([]SkillEffect, bool) {
	for index, effect := range effects {
		if effect.Trigger.Scope == "skill" && effect.Trigger.Event != 1 {
			continue
		}
		if effect.Kind == SkillEffectStatus && effect.Status != nil {
			visual := SkillEffect{
				Kind: SkillEffectVisual, EffectID: effectID,
				Trigger: SkillTrigger{Scope: "modifier", Event: 0, ModifierID: effect.ModifierID},
				Target:  SkillTargetPlan{Kind: SkillTargetSingle, Side: SkillTargetHolder},
			}
			effect.Children = append([]SkillEffect{visual}, effect.Children...)
		} else {
			found := false
			for _, children := range []*[]SkillEffect{&effect.Success, &effect.Failure, &effect.Children} {
				if updated, ok := withSupportModifierVisual(*children, effectID); ok {
					*children, found = updated, true
					break
				}
			}
			if !found {
				continue
			}
		}
		updated := append([]SkillEffect(nil), effects...)
		updated[index] = effect
		return updated, true
	}
	return effects, false
}

func withMonsterSkillPresentation(plan SkillPlan) SkillPlan {
	return withModifierSkillPresentation(plan)
}

func skillPlanContainsVisual(effects []SkillEffect) bool {
	for _, effect := range effects {
		if ((effect.Kind == SkillEffectProjectile || effect.Kind == SkillEffectVisual) && effect.EffectID > 0) ||
			skillPlanContainsVisual(effect.Success) || skillPlanContainsVisual(effect.Failure) ||
			skillPlanContainsVisual(effect.Children) {
			return true
		}
	}
	return false
}

func skillPlanPresentationTarget(effects []SkillEffect) SkillTargetPlan {
	// Damage targets are the safest choice for offensive skills. A number of
	// online support skills have no damage node, however, so fall back to the
	// first configured target when selecting their HurtEffectId presentation.
	if target, ok := findSkillPlanTarget(effects, true); ok {
		return target
	}
	if target, ok := findSkillPlanTarget(effects, false); ok {
		return target
	}
	return SkillTargetPlan{Kind: SkillTargetSingle, Side: SkillTargetCastTarget}
}

func findSkillPlanTarget(effects []SkillEffect, damageOnly bool) (SkillTargetPlan, bool) {
	for _, effect := range effects {
		if damageOnly && !skillEffectDealsDamage(effect) {
			continue
		}
		if effect.Target.Kind != "" {
			return effect.Target, true
		}
		for _, nested := range [][]SkillEffect{effect.Success, effect.Failure, effect.Children} {
			if target, ok := findSkillPlanTarget(nested, damageOnly); ok {
				return target, true
			}
		}
	}
	return SkillTargetPlan{}, false
}

func skillEffectDealsDamage(effect SkillEffect) bool {
	if effect.Kind == SkillEffectDamage {
		return true
	}
	for _, nested := range [][]SkillEffect{effect.Success, effect.Failure, effect.Children} {
		for _, child := range nested {
			if skillEffectDealsDamage(child) {
				return true
			}
		}
	}
	return false
}

func (battle *battleState) claimMonsterActionWindow(now time.Time) bool {
	if battle == nil {
		return false
	}
	readyAt := &battle.monsterReadyAt
	if battle.party != nil {
		readyAt = &battle.party.monsterReadyAt
	}
	if now.Before(*readyAt) {
		return false
	}
	if readyAt.IsZero() {
		*readyAt = now
	}
	interval := monsterPublicActionInterval(battle)
	*readyAt = readyAt.Add((now.Sub(*readyAt)/interval + 1) * interval)
	return true
}

func withConfiguredBasicAttackProjectile(plan SkillPlan) SkillPlan {
	if !isBasicAttackSkill(plan.SkillID) || skillPlanContainsProjectile(plan.Effects) ||
		tables == nil || tables.skillConfig == nil || tables.effectConfig == nil {
		return plan
	}
	row := tables.skillConfig[int64(plan.SkillID)*100]
	if row == nil {
		return plan
	}
	effectID := int32(num(row["EffectId"]))
	effect := tables.effectConfig[int64(effectID)]
	if effectID <= 0 || int32(num(effect["EffectType"])) != 1 {
		return plan
	}
	projectile := SkillEffect{
		Kind:           SkillEffectProjectile,
		Trigger:        SkillTrigger{Scope: "skill", Event: 1},
		Target:         SkillTargetPlan{Kind: SkillTargetSingle, Side: SkillTargetCastTarget},
		DelayMS:        int32(num(row["DelayTime"])),
		EffectID:       effectID,
		ImpactEffectID: int32(num(row["HurtEffectId"])),
	}
	plan.Effects = append([]SkillEffect{projectile}, plan.Effects...)
	return plan
}

func isBasicAttackSkill(skillID int32) bool {
	switch skillID {
	case 100001, 200001, 300001, 400001:
		return true
	default:
		return false
	}
}

func skillPlanContainsProjectile(effects []SkillEffect) bool {
	for _, effect := range effects {
		if effect.Kind == SkillEffectProjectile ||
			skillPlanContainsProjectile(effect.Success) ||
			skillPlanContainsProjectile(effect.Failure) ||
			skillPlanContainsProjectile(effect.Children) {
			return true
		}
	}
	return false
}
