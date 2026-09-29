package main

import (
	"strings"
	"testing"
)

func loadTestSkillLogic(t *testing.T) *SkillLogicCatalog {
	t.Helper()
	path := skillLogicFixturePath()
	catalog, err := LoadSkillLogicCatalog(path)
	if err != nil {
		t.Fatalf("LoadSkillLogicCatalog: %v", err)
	}
	return catalog
}

func TestSkillLogicCatalogCoversEveryConfiguredOption(t *testing.T) {
	catalog := loadTestSkillLogic(t)
	if got := len(catalog.Skills); got != 130 {
		t.Fatalf("skill count = %d, want 130", got)
	}
	players, monsters := 0, 0
	for id := range catalog.Skills {
		if id >= 500001 && id <= 500042 {
			monsters++
		} else {
			players++
		}
	}
	if players != 88 || monsters != 42 {
		t.Fatalf("player/monster skill counts = %d/%d, want 88/42", players, monsters)
	}

	wantCounts := map[SkillOptionKind]int{
		SkillOptionPlayEffect:      42,
		SkillOptionProjectile:      63,
		SkillOptionCounter:         3,
		SkillOptionReflect:         2,
		SkillOptionChangeGlobalCD:  1,
		SkillOptionChangeDamage:    3,
		SkillOptionChangeCastCount: 1,
		SkillOptionShield:          8,
		SkillOptionChance:          92,
		SkillOptionDamage:          102,
		SkillOptionLifeSteal:       1,
		SkillOptionModifyModifier:  2,
		SkillOptionDelay:           2,
		SkillOptionApplyModifier:   217,
		SkillOptionHeal:            9,
	}
	gotCounts := catalog.OptionKindCounts()
	if len(gotCounts) != len(wantCounts) {
		t.Fatalf("option kind count = %d, want %d: %#v", len(gotCounts), len(wantCounts), gotCounts)
	}
	for kind, want := range wantCounts {
		if got := gotCounts[kind]; got != want {
			t.Errorf("option %q count = %d, want %d", kind, got, want)
		}
	}
}

func TestEverySkillLevelBuildsExecutablePlan(t *testing.T) {
	catalog := loadTestSkillLogic(t)
	coverage := struct {
		physical, spiritual, trueDamage bool
		heal, shield, buff, debuff      bool
		control, dot, hot, chance       bool
		modifierTrigger, modifyStatus   bool
	}{}

	for id, skill := range catalog.Skills {
		if skill.MaxLevel < 1 {
			t.Errorf("skill %d has invalid max level %d", id, skill.MaxLevel)
			continue
		}
		for level := int32(1); level <= skill.MaxLevel; level++ {
			plan, err := catalog.Plan(id, level)
			if err != nil {
				t.Errorf("Plan(%d, %d): %v", id, level, err)
				continue
			}
			walkSkillEffects(plan.Effects, func(effect SkillEffect) {
				coverage.chance = coverage.chance || effect.Kind == SkillEffectChance
				coverage.heal = coverage.heal || effect.Kind == SkillEffectHeal
				coverage.shield = coverage.shield || effect.Kind == SkillEffectShield
				coverage.modifyStatus = coverage.modifyStatus || effect.Kind == SkillEffectModifyStatus
				coverage.modifierTrigger = coverage.modifierTrigger || effect.Trigger.Scope == "modifier"
				if effect.Damage != nil {
					switch effect.Damage.Type {
					case SkillDamagePhysical:
						coverage.physical = true
					case SkillDamageSpiritual:
						coverage.spiritual = true
					case SkillDamageTrue:
						coverage.trueDamage = true
					}
				}
				if effect.Status != nil {
					coverage.buff = coverage.buff || effect.Status.IsBuff
					coverage.debuff = coverage.debuff || effect.Status.IsDebuff
					coverage.control = coverage.control || effect.Status.IsControl
					coverage.dot = coverage.dot || effect.Status.HasDOT
					coverage.hot = coverage.hot || effect.Status.HasHOT
				}
			})
		}
	}
	if !coverage.physical || !coverage.spiritual || !coverage.trueDamage ||
		!coverage.heal || !coverage.shield || !coverage.buff || !coverage.debuff ||
		!coverage.control || !coverage.dot || !coverage.hot || !coverage.chance ||
		!coverage.modifierTrigger || !coverage.modifyStatus {
		t.Fatalf("incomplete executable effect coverage: %+v", coverage)
	}
}

func TestEveryMultiTargetSkillResolvesToOneClientTeam(t *testing.T) {
	catalog := loadTestSkillLogic(t)
	for skillID, skill := range catalog.Skills {
		for level := int32(1); level <= skill.MaxLevel; level++ {
			plan, err := catalog.Plan(skillID, level)
			if err != nil {
				t.Fatalf("skill %d level %d: %v", skillID, level, err)
			}
			walkSkillEffects(plan.Effects, func(effect SkillEffect) {
				if effect.Target.Kind == SkillTargetMulti && effect.Target.Side == SkillTargetAny {
					t.Errorf("skill %d level %d has unresolved enemy/ally target", skillID, level)
				}
			})
		}
	}
}

func TestSkillPlanUsesStructuredDamageFormula(t *testing.T) {
	catalog := loadTestSkillLogic(t)
	tests := []struct {
		id         int32
		level      int32
		damageType SkillDamageType
		stat       SkillFormulaStat
		percent    float64
		target     bool
	}{
		{id: 100001, level: 1, damageType: SkillDamagePhysical, stat: SkillStatPhysicalAttack, percent: 142},
		{id: 500015, level: 1, damageType: SkillDamageSpiritual, stat: SkillStatSpiritualAttack, percent: 100},
		{id: 500012, level: 1, damageType: SkillDamageTrue, stat: SkillStatMaxHP, percent: 20, target: true},
		{id: 110403, level: 5, damageType: SkillDamageTrue, stat: SkillStatCurrentHP, percent: 3, target: true},
	}
	for _, test := range tests {
		plan, err := catalog.Plan(test.id, test.level)
		if err != nil {
			t.Fatalf("Plan(%d, %d): %v", test.id, test.level, err)
		}
		var found bool
		walkSkillEffects(plan.Effects, func(effect SkillEffect) {
			if effect.Damage == nil || effect.Damage.Type != test.damageType {
				return
			}
			formula := effect.Damage.Self
			if test.target {
				formula = effect.Damage.Target
			}
			if formula.Stat == test.stat && formula.Percent == test.percent {
				found = true
			}
		})
		if !found {
			t.Errorf("skill %d level %d lacks %s %s %.2f%% damage", test.id, test.level, test.damageType, test.stat, test.percent)
		}
	}
}

func TestSkillLogicRejectsUnknownOption(t *testing.T) {
	input := `{
		"_t":"SkillLogicConfigCollection",
		"skillDic":[[1,{
			"skillId":1,"skillName":"x","maxLevel":1,
			"cast":{"skillCastType":0,"castBaseType":0,"skillCast":0,"attributeType":0},
			"skillType":0,"CD":0,"duration":0,"interval":0,"teamType":1,"flagType":1,"desc":"ignored",
			"skillEventDic":[[1,[{"_t":"SkillOption_未知"}]]],"modifierDic":null
		}]]
	}`
	_, err := DecodeSkillLogicCatalog(strings.NewReader(input))
	if err == nil || !strings.Contains(err.Error(), "unsupported skill option") {
		t.Fatalf("unknown option error = %v", err)
	}
}

func walkSkillEffects(effects []SkillEffect, visit func(SkillEffect)) {
	for _, effect := range effects {
		visit(effect)
		walkSkillEffects(effect.Success, visit)
		walkSkillEffects(effect.Failure, visit)
		walkSkillEffects(effect.Children, visit)
	}
}
