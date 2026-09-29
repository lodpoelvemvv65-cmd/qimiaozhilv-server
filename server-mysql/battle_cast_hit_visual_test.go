package main

import "testing"

func TestConfiguredPlayerSkillsKeepBothCastAndHitEffects(t *testing.T) {
	loadOnlineTablesForTest(t)
	catalog := loadTestSkillLogic(t)
	for id, skill := range catalog.Skills {
		if id >= 500000 {
			continue
		}
		plan, err := catalog.Plan(id, skill.MaxLevel)
		if err != nil {
			t.Fatal(err)
		}
		row := tables.skillConfig[int64(id)*100]
		// Explicit SkillLogic projectile pairs take precedence over conflicting
		// old table IDs. This check covers separate non-projectile A/B assets.
		if !skillPlanCanCast(plan) || skillPlanContainsProjectile(plan.Effects) || configuredEffectType(int32(num(row["EffectId"]))) != 2 {
			continue
		}
		plan = withConfiguredSkillPresentation(plan)
		ids := map[int32]bool{}
		var collect func([]SkillEffect)
		collect = func(effects []SkillEffect) {
			for _, effect := range effects {
				ids[effect.EffectID], ids[effect.ImpactEffectID] = true, true
				collect(effect.Children)
				collect(effect.Success)
				collect(effect.Failure)
			}
		}
		collect(plan.Effects)
		for _, field := range []string{"EffectId", "HurtEffectId"} {
			effectID := int32(num(row[field]))
			if effectID > 0 && tables.effectConfig[int64(effectID)] != nil && !ids[effectID] {
				t.Errorf("skill %d %s missing %s=%d", id, skill.SkillName, field, effectID)
			}
		}
	}
}
