package main

import (
	"math"
	"testing"

	"mhqserver/internal/operationsconfig"
)

func TestSkillMPCostMatchesOnlineCapture(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.Combat.SkillMPCostPercent = 50
	})
	catalog := loadTestSkillLogic(t)
	// online-skill-effects-20260906.pcapng: UnitCharacter.Level=21500,
	// MaxMp=968682. Consecutive 20169/1003 values give these exact deductions.
	battle := &battleState{owner: &session{level: 21500}, playerMP: 968682, playerMaxMP: 968682}
	for _, test := range []struct {
		skillID       int32
		before, after int32
	}{
		{310101, 910632, 908482},
		{310401, 954707, 949332},
		{310501, 963307, 954707},
		{310601, 929982, 919232},
	} {
		plan, err := catalog.Plan(test.skillID, 1)
		if err != nil {
			t.Fatal(err)
		}
		battle.playerMP = test.before
		for _, maximum := range []int32{968682, 1937364} {
			battle.playerMaxMP = maximum
			mp, hp, err := battle.skillResourceCost(plan.Cast)
			if err != nil || hp != 0 || battle.playerMP-mp != test.after {
				t.Errorf("skill %d maxMP=%d cost=%d/%d err=%v; want %d MP",
					test.skillID, maximum, mp, hp, err, test.before-test.after)
			}
		}
	}
}

func TestSkillMPCostUsesHotReloadedGameplayPercent(t *testing.T) {
	battle := &battleState{owner: &session{level: 21500}, playerMP: 100000, playerMaxMP: 100000}
	plan, err := loadTestSkillLogic(t).Plan(310501, 1)
	if err != nil {
		t.Fatal(err)
	}
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.Combat.SkillMPCostPercent = 25
	})
	for _, test := range []struct {
		percent float64
		want    int32
	}{{25, 4300}, {75, 12900}, {0, 0}} {
		config := gameplayConfigSnapshot()
		config.Combat.SkillMPCostPercent = test.percent
		storeGameplayConfig(config)
		mp, hp, err := battle.skillResourceCost(plan.Cast)
		if err != nil || mp != test.want || hp != 0 {
			t.Fatalf("hot-reloaded %g%% cost=%d/%d err=%v, want %d/0", test.percent, mp, hp, err, test.want)
		}
	}
}

func TestSkillResourceCostBasesAndBoundaries(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.Combat.SkillMPCostPercent = 50
	})
	battle := &battleState{owner: &session{level: 10}, playerMP: 100, playerMaxMP: 1000, playerHP: 500, playerMaxHP: 1000}
	for _, test := range []struct {
		name   string
		cast   SkillCast
		mp, hp int32
		fail   bool
	}{
		{"level", SkillCast{SkillCastType: 1, CastBaseType: skillCastBaseLevel, SkillCast: .2}, 1, 0, false},
		{"current", SkillCast{SkillCastType: 1, CastBaseType: skillCastBaseCurrent, SkillCast: .2}, 10, 0, false},
		{"maximum", SkillCast{SkillCastType: 1, CastBaseType: skillCastBaseMax, SkillCast: .2}, 100, 0, false},
		{"insufficient", SkillCast{SkillCastType: 1, CastBaseType: skillCastBaseMax, SkillCast: .3}, 0, 0, true},
		{"hp_current", SkillCast{SkillCastType: 2, CastBaseType: skillCastBaseCurrent, SkillCast: .08}, 0, 40, false},
		{"hp_maximum", SkillCast{SkillCastType: 2, CastBaseType: skillCastBaseMax, SkillCast: .08}, 0, 80, false},
		{"nonlethal", SkillCast{SkillCastType: 2, CastBaseType: skillCastBaseCurrent, SkillCast: 1}, 0, 499, false},
		{"invalid_base", SkillCast{SkillCastType: 1, CastBaseType: 99, SkillCast: .2}, 0, 0, true},
		{"nan", SkillCast{SkillCastType: 1, SkillCast: math.NaN()}, 0, 0, true},
		{"overflow", SkillCast{SkillCastType: 1, SkillCast: math.MaxFloat64}, 0, 0, true},
		{"negative", SkillCast{SkillCastType: 1, SkillCast: -.2}, 0, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			mp, hp, err := battle.skillResourceCost(test.cast)
			if (err != nil) != test.fail || mp != test.mp || hp != test.hp {
				t.Fatalf("cost=%d/%d err=%v, want %d/%d error=%v", mp, hp, err, test.mp, test.hp, test.fail)
			}
		})
	}
	plan, err := loadTestSkillLogic(t).Plan(310503, 1)
	if err != nil {
		t.Fatal(err)
	}
	if mp, hp, err := battle.skillResourceCost(plan.Cast); err != nil || mp != 0 || hp != 80 {
		t.Fatalf("bloodthirst cost=%d/%d err=%v, want undiscounted 80 HP from maximum HP", mp, hp, err)
	}
	battle.playerHP = 1
	if _, _, err := battle.skillResourceCost(plan.Cast); err == nil {
		t.Fatal("HP-cost skill accepted at one HP")
	}
}
