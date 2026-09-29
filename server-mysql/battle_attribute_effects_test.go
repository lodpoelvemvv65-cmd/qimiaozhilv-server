package main

import (
	"testing"
	"time"
)

func TestTypedDamageAmplificationAffectsMatchingDamageOnly(t *testing.T) {
	for _, test := range []struct {
		name        string
		damageType  SkillDamageType
		stat        SkillFormulaStat
		numericType int32
		want        int32
	}{
		{name: "physical", damageType: SkillDamagePhysical, stat: SkillStatPhysicalAttack, numericType: 1045, want: 125},
		{name: "spiritual", damageType: SkillDamageSpiritual, stat: SkillStatSpiritualAttack, numericType: 1046, want: 125},
		{name: "true-damage", damageType: SkillDamageTrue, stat: SkillStatPhysicalAttack, numericType: 1045, want: 100},
	} {
		t.Run(test.name, func(t *testing.T) {
			owner := &session{transBonus: map[int32]float32{test.numericType: 0.25}}
			monster := &monsterUnit{id: 200, hp: 1000, maxHP: 1000, alive: true}
			battle := &battleState{
				owner: owner, playerHP: 1000, playerMaxHP: 1000,
				phyAtk: 100, spiAtk: 100, monsters: []*monsterUnit{monster},
			}
			battle.runtime = NewCombatRuntime(battle, 100, nil)
			plan := &SkillDamagePlan{
				Type: test.damageType,
				Self: SkillFormula{Stat: test.stat, Percent: 100},
			}
			got, critical := battle.damageOutcome(
				plan, battle.runtime.Player(), MonsterCombatUnit(monster.id),
				&skillExecutionContext{roll: func() float64 { return 1 }, damageMultiplier: 1},
			)
			if critical || got != test.want {
				t.Fatalf("damage = %d critical=%t, want %d non-critical", got, critical, test.want)
			}
		})
	}
}

func TestAuxiliaryAttributeAmplifiesSkillHealingAndShielding(t *testing.T) {
	owner := &session{transBonus: map[int32]float32{1017: 0.25}}
	monster := &monsterUnit{id: 200, hp: 1000, maxHP: 1000, alive: true}
	battle := &battleState{
		owner: owner, playerHP: 500, playerMaxHP: 1000,
		spiAtk: 100, monsters: []*monsterUnit{monster},
	}
	battle.runtime = NewCombatRuntime(battle, 100, nil)
	player := battle.runtime.Player()
	if _, err := battle.runtime.ApplyEffects(CombatEffectContext{Source: player, Target: player}, []CombatEffect{EffectSpec{
		Key: "auxiliary", Kind: CombatEffectAttribute, Attribute: CombatAttributeAuxiliary,
		Percent: 20, Duration: time.Minute,
	}}); err != nil {
		t.Fatal(err)
	}
	plan := &SkillTreatmentPlan{Self: SkillFormula{Stat: SkillStatSpiritualAttack, Percent: 100}}
	if got := battle.treatmentAmount(plan, player, player); got != 145 {
		t.Fatalf("auxiliary treatment amount = %d, want 145", got)
	}
	effect := SkillEffect{Kind: SkillEffectShield, Treatment: plan, Target: SkillTargetPlan{Kind: SkillTargetSingle, Side: SkillTargetSelf}}
	events, err := battle.executeSkillEffect(effect, player, player, player, nil, func() float64 { return 1 })
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || battle.runtime.totalShield(player) != 145 {
		t.Fatalf("auxiliary shield events=%+v total=%d, want 145", events, battle.runtime.totalShield(player))
	}
}
