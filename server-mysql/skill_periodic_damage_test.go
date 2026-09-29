package main

import (
	"testing"
	"time"
)

// findStatusEffectByModifier walks a plan for the effect that installs one
// modifier, so a test can look at the modifier's duration and at the event-8
// tick it carries together.
func findStatusEffectByModifier(effects []SkillEffect, modifierID int64) *SkillEffect {
	for index := range effects {
		if effects[index].ModifierID == modifierID && effects[index].Status != nil {
			return &effects[index]
		}
		if found := findStatusEffectByModifier(effects[index].Success, modifierID); found != nil {
			return found
		}
		if found := findStatusEffectByModifier(effects[index].Failure, modifierID); found != nil {
			return found
		}
		if found := findStatusEffectByModifier(effects[index].Children, modifierID); found != nil {
			return found
		}
	}
	return nil
}

func periodicDamageChild(status *SkillStatusPlan, children []SkillEffect) *SkillDamagePlan {
	for index := range children {
		if children[index].Damage != nil && children[index].Damage.Periodic {
			return children[index].Damage
		}
	}
	return nil
}

// 开大脚 level 3 is the only event-8 伤害 node in the shipped table that declares
// a per-level param table and then leaves it empty, so its tick resolved to no
// formula at all. Its own description states the missing number:
// "20%的几率给敌方添加4%敌方最大生命值的【流血】，持续14秒效果".
func TestOpenVolleyLevelThreeBleedTickCarriesTheDescribedPercent(t *testing.T) {
	catalog := loadTestSkillLogic(t)
	plan, err := catalog.Plan(210603, 3)
	if err != nil {
		t.Fatal(err)
	}
	effect := findStatusEffectByModifier(plan.Effects, 21060317)
	if effect == nil {
		t.Fatal("level 3 does not install the 流血 modifier 21060317")
	}
	if effect.Status.DurationSeconds != 14 || !effect.Status.HasDOT {
		t.Fatalf("流血 status = duration %v hasDOT %t, want 14 / true",
			effect.Status.DurationSeconds, effect.Status.HasDOT)
	}
	tick := periodicDamageChild(effect.Status, effect.Children)
	if tick == nil {
		t.Fatal("流血 has no periodic damage child")
	}
	if tick.Target.Stat != SkillStatMaxHP || tick.Target.Percent != 4 {
		t.Fatalf("流血 tick target formula = %s/%v%%, want max_hp/4%%", tick.Target.Stat, tick.Target.Percent)
	}
	// The repair fills the target formula only; the node's own self and limit
	// entries keep the numbers they shipped with.
	if tick.Self.Stat != SkillStatPhysicalAttack || tick.Self.Percent != 0 {
		t.Fatalf("流血 self formula = %s/%v%%, want the shipped physical_attack/0%%", tick.Self.Stat, tick.Self.Percent)
	}
	if tick.Limited || tick.Limit.Stat != SkillStatPhysicalAttack || tick.Limit.Percent != 0 {
		t.Fatalf("流血 limit = limited %t %s/%v%%, want the shipped unlimited physical_attack/0%%",
			tick.Limited, tick.Limit.Stat, tick.Limit.Percent)
	}
	// The modifier is level-gated: the lower levels do not install it at all.
	for _, level := range []int32{1, 2} {
		plan, err := catalog.Plan(210603, level)
		if err != nil {
			t.Fatal(err)
		}
		if other := findStatusEffectByModifier(plan.Effects, 21060317); other != nil {
			t.Fatalf("level %d unexpectedly installs 21060317: %+v", level, other.Status)
		}
	}
}

// The repair must stay a repair: every other periodic skill writes its own
// percentage, and those numbers have to come out unchanged.
func TestTickDamageRepairLeavesShippedNumbersAlone(t *testing.T) {
	catalog := loadTestSkillLogic(t)
	tests := []struct {
		skillID    int32
		level      int32
		modifierID int64
		percent    float64
	}{
		{210403, 3, 21040312, 3}, // 流血 "3%敌方最大生命值"
		{210403, 4, 21040312, 3},
		{210403, 5, 21040312, 3},
		{210502, 4, 21050214, 4}, // 流血 "4%敌方最大生命值"
		{110503, 4, 11050319, 2}, // 流血 "2%敌方最大生命值"
		{310202, 1, 31020211, 1}, // 中毒 "1%最大生命值"
		{410601, 3, 41060112, 3}, // 流血 "3%敌方最大生命值"
	}
	for _, test := range tests {
		plan, err := catalog.Plan(test.skillID, test.level)
		if err != nil {
			t.Fatalf("Plan(%d,%d): %v", test.skillID, test.level, err)
		}
		effect := findStatusEffectByModifier(plan.Effects, test.modifierID)
		if effect == nil {
			t.Fatalf("skill %d level %d does not install %d", test.skillID, test.level, test.modifierID)
		}
		tick := periodicDamageChild(effect.Status, effect.Children)
		if tick == nil {
			t.Fatalf("modifier %d has no periodic damage child", test.modifierID)
		}
		if tick.Target.Stat != SkillStatMaxHP || tick.Target.Percent != test.percent {
			t.Fatalf("modifier %d tick = %s/%v%%, want the shipped max_hp/%v%%",
				test.modifierID, tick.Target.Stat, tick.Target.Percent, test.percent)
		}
	}
}

// 210603's 流血 shares the 流血 overlay slot with 210403's (overlayType 2,
// stateK 105), so the two land in one bucket and only the layer amount tells
// them apart. Both keep their own percentage.
func TestOpenVolleyBleedAndHookBleedKeepTheirOwnPercent(t *testing.T) {
	catalog := loadTestSkillLogic(t)
	for _, test := range []struct {
		skillID    int32
		level      int32
		modifierID int64
		percent    float64
	}{
		{210403, 3, 21040312, 3},
		{210603, 3, 21060317, 4},
	} {
		plan, err := catalog.Plan(test.skillID, test.level)
		if err != nil {
			t.Fatal(err)
		}
		effect := findStatusEffectByModifier(plan.Effects, test.modifierID)
		if effect == nil {
			t.Fatalf("skill %d level %d installs no 流血", test.skillID, test.level)
		}
		if effect.Status.OverlayType != 2 || effect.Status.StateKey != 105 {
			t.Fatalf("skill %d 流血 slot = overlayType %d stateK %d, want 2/105",
				test.skillID, effect.Status.OverlayType, effect.Status.StateKey)
		}
		tick := periodicDamageChild(effect.Status, effect.Children)
		if tick == nil {
			t.Fatalf("skill %d 流血 has no tick", test.skillID)
		}
		if tick.Target.Stat != SkillStatMaxHP || tick.Target.Percent != test.percent {
			t.Fatalf("skill %d 流血 tick = %s/%v%%, want max_hp/%v%%",
				test.skillID, tick.Target.Stat, tick.Target.Percent, test.percent)
		}
	}
}

// Casting the skill used to install nothing at all: the tick amount came out 0
// and the runtime only installs a periodic effect with a positive amount, so
// the 流血 never landed, no icon appeared and no tick ever fired.
func TestOpenVolleyBleedLandsAndTicksFourPercentOfMaxHP(t *testing.T) {
	catalog := loadTestSkillLogic(t)
	plan, err := catalog.Plan(210603, 3)
	if err != nil {
		t.Fatal(err)
	}
	effect := findStatusEffectByModifier(plan.Effects, 21060317)
	if effect == nil {
		t.Fatal("level 3 does not install the 流血 modifier 21060317")
	}

	clock := &fakeCombatClock{now: time.Unix(9_000, 0)}
	monster := &monsterUnit{id: 200, hp: 1_000, maxHP: 1_000, phyAtk: 10, phyDef: 0, alive: true}
	battle := &battleState{
		owner: &session{}, playerHP: 1_000, playerMaxHP: 1_000,
		phyAtk: 100, phyDef: 0, monsters: []*monsterUnit{monster},
	}
	battle.runtime = NewCombatRuntime(battle, 1_000, clock)
	source, target := battle.runtime.Player(), MonsterCombatUnit(monster.id)

	if _, err := battle.executeSkillEffect(*effect, source, target, target, nil, func() float64 { return 0 }); err != nil {
		t.Fatal(err)
	}
	effects := battle.runtime.state().effects
	if len(effects) != 1 || effects[0].key != "overlay:2:dot" || effects[0].overlayValue != 40 {
		t.Fatalf("installed 流血 = %+v, want one overlay:2:dot layer worth 4%% of 1000 HP", effects)
	}
	if monster.hp != 1_000 {
		t.Fatalf("流血 damaged on landing: hp = %d, want the untouched 1000", monster.hp)
	}

	clock.Add(gameplayEffectTickInterval())
	count, total := periodicDamage(battle.runtime.Tick())
	if count != 1 || total != 40 {
		t.Fatalf("流血 ticked %d times for %d, want one 40-point tick", count, total)
	}
	if monster.hp != 960 {
		t.Fatalf("流血 target hp = %d, want 960", monster.hp)
	}
}

// 每跳和别的伤害走同一个 positiveCombatAmount，所以是向上取整：720 血的怪每跳
// 4% = 28.8 → 29（沙滩第 9 层 10009 实测每跳就是 29）。1000 血是整数倍，看不出取整。
func TestOpenVolleyBleedTickCeilsLikeEveryOtherDamage(t *testing.T) {
	catalog := loadTestSkillLogic(t)
	plan, err := catalog.Plan(210603, 3)
	if err != nil {
		t.Fatal(err)
	}
	effect := findStatusEffectByModifier(plan.Effects, 21060317)
	if effect == nil {
		t.Fatal("level 3 does not install the 流血 modifier 21060317")
	}

	clock := &fakeCombatClock{now: time.Unix(9_100, 0)}
	monster := &monsterUnit{id: 201, hp: 720, maxHP: 720, phyAtk: 10, phyDef: 0, alive: true}
	battle := &battleState{
		owner: &session{}, playerHP: 1_000, playerMaxHP: 1_000,
		phyAtk: 100, phyDef: 0, monsters: []*monsterUnit{monster},
	}
	battle.runtime = NewCombatRuntime(battle, 1_000, clock)
	source, target := battle.runtime.Player(), MonsterCombatUnit(monster.id)

	if _, err := battle.executeSkillEffect(*effect, source, target, target, nil, func() float64 { return 0 }); err != nil {
		t.Fatal(err)
	}
	clock.Add(gameplayEffectTickInterval())
	count, total := periodicDamage(battle.runtime.Tick())
	if count != 1 || total != 29 {
		t.Fatalf("流血 ticked %d times for %d, want one 29-point tick (4%% of 720 = 28.8, ceiled)",
			count, total)
	}
	if monster.hp != 691 {
		t.Fatalf("流血 target hp = %d, want 691", monster.hp)
	}
}
