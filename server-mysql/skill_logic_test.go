package main

import (
	"strings"
	"testing"
)

func loadTestSkillLogic(t *testing.T) *SkillLogicCatalog {
	t.Helper()
	catalog, err := LoadSkillLogicCatalog(skillLogicFixturePath())
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

func TestProjectilePlanUsesConfiguredModifierImpactEffect(t *testing.T) {
	catalog := loadTestSkillLogic(t)
	plan, err := catalog.Plan(110101, 1)
	if err != nil {
		t.Fatal(err)
	}
	var projectile *SkillEffect
	walkSkillEffects(plan.Effects, func(effect SkillEffect) {
		if projectile == nil && effect.Kind == SkillEffectProjectile {
			copy := effect
			projectile = &copy
		}
	})
	if projectile == nil {
		t.Fatal("skill 110101 projectile not found")
	}
	if projectile.EffectID != 2103 || projectile.ImpactEffectID != 2104 {
		t.Fatalf("skill 110101 effects = projectile %d impact %d, want 2103/2104",
			projectile.EffectID, projectile.ImpactEffectID)
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

// 双面镜（310604）与柳暗花明（410404）的 teamType 与同职业其余友方技能
// （以及它们自己的文案）相反，是线上数据错误；服务端用技能级定点覆盖把它们
// 改回友方，否则玩家点队友会把效果落到敌人身上。
func TestTeamTypeOverridesPointBothSkillsAtFriendlyTargets(t *testing.T) {
	catalog := loadTestSkillLogic(t)
	for skillID, want := range onlineSkillTeamTypeOverrides {
		skill := catalog.Skills[skillID]
		if skill == nil {
			t.Fatalf("skill %d is not in the shipped catalog", skillID)
		}
		if skill.TeamType == want {
			t.Fatalf("skill %d already ships teamType=%d; the override is dead and should be removed", skillID, want)
		}
		for level := int32(1); level <= skill.MaxLevel; level++ {
			plan, err := catalog.Plan(skillID, level)
			if err != nil {
				t.Fatalf("Plan(%d, %d): %v", skillID, level, err)
			}
			if plan.TeamType != want {
				t.Fatalf("skill %d level %d teamType = %d, want the overridden %d", skillID, level, plan.TeamType, want)
			}
		}
	}
}

// 覆盖表必须只影响它点名的那几个技能：其余技能一律照配置自己的 teamType 构建，
// 否则就是一次静默的全表偏移。
func TestTeamTypeOverridesDoNotLeakIntoOtherSkills(t *testing.T) {
	catalog := loadTestSkillLogic(t)
	leaked := 0
	for skillID, skill := range catalog.Skills {
		_, overridden := onlineSkillTeamTypeOverrides[skillID]
		for level := int32(1); level <= skill.MaxLevel; level++ {
			plan, err := catalog.Plan(skillID, level)
			if err != nil {
				t.Fatalf("Plan(%d, %d): %v", skillID, level, err)
			}
			if overridden {
				continue
			}
			if plan.TeamType != skill.TeamType {
				leaked++
				t.Errorf("skill %d level %d teamType = %d, but the configuration says %d",
					skillID, level, plan.TeamType, skill.TeamType)
			}
		}
	}
	if leaked != 0 {
		t.Fatalf("%d skill levels had their teamType changed by the override table", leaked)
	}
}

// teamType 覆盖必须真的改变落点：点队友（selectedAllyID）之后，效果落在队友身上，
// 而不是像修复前那样落到敌人（怪物）身上。修复前这两条会失败。
func TestFriendlyTeamTypeOverridesLandOnTheSelectedAlly(t *testing.T) {
	catalog := loadTestSkillLogic(t)
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = catalog
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })

	tests := []struct {
		name      string
		skillID   int32
		modifiers []string
	}{
		{name: "双面镜", skillID: 310604, modifiers: []string{"31060411", "31060412"}},
		{name: "柳暗花明", skillID: 410404, modifiers: []string{"41040411"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, first, second := newSharedPartyBattleForTest(t)
			battle := first.session.battle
			caster := PlayerCombatUnit(first.session.playerID)
			ally := PlayerCombatUnit(second.session.playerID)
			// 玩家在战斗里点选队友 —— 客户端点队友发的是 C2M_SelectTeamMember。
			battle.selectedAllyID = second.session.playerID

			plan, err := catalog.Plan(test.skillID, 1)
			if err != nil {
				t.Fatal(err)
			}
			primary, ok := battle.playerSkillPrimaryTarget(plan)
			if !ok {
				t.Fatalf("skill %d: no primary target", test.skillID)
			}
			if primary != ally {
				t.Fatalf("skill %d primary target = %+v, want the selected ally %+v", test.skillID, primary, ally)
			}

			events, err := battle.executeSkillPlanWithContext(plan, caster, primary,
				newSkillExecutionContext(battle, func() float64 { return 0 }))
			if err != nil {
				t.Fatal(err)
			}

			landed := map[string]bool{}
			for _, event := range events {
				switch event.Type {
				case CombatEventEffectApplied, CombatEventEffectRefreshed, CombatEventEffectStacked:
				default:
					continue
				}
				for _, modifier := range test.modifiers {
					if !strings.Contains(event.Key, modifier) {
						continue
					}
					landed[modifier] = true
					if event.Target != ally {
						t.Fatalf("skill %d modifier %s landed on %+v, want the selected ally %+v (events=%+v)",
							test.skillID, modifier, event.Target, ally, events)
					}
				}
			}
			for _, modifier := range test.modifiers {
				if !landed[modifier] {
					t.Fatalf("skill %d modifier %s never landed (events=%+v)", test.skillID, modifier, events)
				}
			}
		})
	}
}
