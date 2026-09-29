package main

import (
	"testing"
	"time"
)

func TestModifierImmuneTagBlocksDifferentModifier(t *testing.T) {
	battle, source, target := newModifierTestBattle()
	active := testAttributeStatus(1001, 21, 21, 10)
	if _, err := battle.applyStatusEffect(SkillEffect{Kind: SkillEffectStatus, Status: active}, source, target, target, func() float64 { return 0 }); err != nil {
		t.Fatal(err)
	}

	incoming := testAttributeStatus(1002, 19, 21, 50)
	events, err := battle.applyStatusEffect(SkillEffect{Kind: SkillEffectStatus, Status: incoming}, source, target, target, func() float64 { return 0 })
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != CombatEventImmune {
		t.Fatalf("conflicting modifier events = %+v, want one immune event", events)
	}
	if got := battle.runtime.EffectivePercentAttribute(target, CombatAttributePhysicalAttack); got != 10 {
		t.Fatalf("physical attack modifier = %.0f, want existing 10", got)
	}
}

func TestModifierImmuneTagAllowsSameModifierReapplication(t *testing.T) {
	battle, source, target := newModifierTestBattle()
	status := testAttributeStatus(1001, 21, 21, 10)
	if _, err := battle.applyStatusEffect(SkillEffect{Kind: SkillEffectStatus, Status: status}, source, target, target, func() float64 { return 0 }); err != nil {
		t.Fatal(err)
	}
	status.Value = 25
	events, err := battle.applyStatusEffect(SkillEffect{Kind: SkillEffectStatus, Status: status}, source, target, target, func() float64 { return 0 })
	if err != nil {
		t.Fatal(err)
	}
	if containsCombatEvent(events, CombatEventImmune) || !containsCombatEvent(events, CombatEventEffectRefreshed) {
		t.Fatalf("same modifier reapplication events = %+v", events)
	}
	if got := battle.runtime.EffectivePercentAttribute(target, CombatAttributePhysicalAttack); got != 25 {
		t.Fatalf("refreshed physical attack modifier = %.0f, want 25", got)
	}
}

func TestModifierImmuneTagArrayIsEnforced(t *testing.T) {
	battle, source, target := newModifierTestBattle()
	active := testAttributeStatus(1001, 22, 22, 10)
	if _, err := battle.applyStatusEffect(SkillEffect{Kind: SkillEffectStatus, Status: active}, source, target, target, func() float64 { return 0 }); err != nil {
		t.Fatal(err)
	}
	incoming := testAttributeStatus(1002, 19, 0, 50)
	incoming.ImmuneTags = []int32{7, 22, 22}
	events, err := battle.applyStatusEffect(SkillEffect{Kind: SkillEffectStatus, Status: incoming}, source, target, target, func() float64 { return 0 })
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != CombatEventImmune {
		t.Fatalf("immuneTagArr events = %+v, want one immune event", events)
	}
}

func TestStackModeUsesModifierAttributeFlags(t *testing.T) {
	tests := []struct {
		name   string
		status *SkillStatusPlan
		want   CombatEffectStackMode
	}{
		{name: "nil", want: CombatEffectReplace},
		{name: "no flags", status: &SkillStatusPlan{OverlayType: 4}, want: CombatEffectReplace},
		{name: "ignore invincible only", status: &SkillStatusPlan{Attribute: 2, OverlayType: 2}, want: CombatEffectReplace},
		{name: "stackable", status: &SkillStatusPlan{Attribute: 4}, want: CombatEffectStack},
		{name: "refreshable", status: &SkillStatusPlan{Attribute: 8}, want: CombatEffectRefresh},
		{name: "stack and refresh", status: &SkillStatusPlan{Attribute: 12}, want: CombatEffectStack},
		{name: "all", status: &SkillStatusPlan{Attribute: 14}, want: CombatEffectStack},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := stackMode(test.status); got != test.want {
				t.Fatalf("stackMode(%+v) = %d, want %d", test.status, got, test.want)
			}
		})
	}
}

func TestStackableRefreshableModifierStacksAndExtendsDuration(t *testing.T) {
	runtime, clock, source, target := newEffectTestRuntime()
	status := &SkillStatusPlan{ModifierID: 1001, Attribute: 12}
	spec := modifierEffectSpec(status, EffectSpec{
		Key: "stack-and-refresh", Kind: CombatEffectAttribute,
		Attribute: CombatAttributePhysicalAttack, Percent: 10,
		Duration: 2 * time.Second, StackMode: stackMode(status), MaxStacks: 10,
	})
	applyTestEffect(t, runtime, source, target, spec)
	clock.Add(time.Second)
	events := applyTestEffect(t, runtime, source, target, spec)
	if !containsCombatEvent(events, CombatEventEffectStacked) {
		t.Fatalf("second application events = %+v, want stacked", events)
	}
	if got := runtime.EffectiveAttribute(target, CombatAttributePhysicalAttack); got != 48 {
		t.Fatalf("stacked physical attack = %d, want 48", got)
	}

	clock.Add(1500 * time.Millisecond)
	if events := runtime.Tick(); containsCombatEvent(events, CombatEventEffectExpired) {
		t.Fatalf("modifier expired before refreshed deadline: %+v", events)
	}
	if got := runtime.EffectiveAttribute(target, CombatAttributePhysicalAttack); got != 48 {
		t.Fatalf("physical attack before refreshed deadline = %d, want 48", got)
	}
}

func newModifierTestBattle() (*battleState, CombatUnitRef, CombatUnitRef) {
	monster := &monsterUnit{id: 200, hp: 1000, maxHP: 1000, alive: true}
	battle := &battleState{playerHP: 1000, playerMaxHP: 1000, monsters: []*monsterUnit{monster}}
	battle.runtime = NewCombatRuntime(battle, 100, nil)
	return battle, battle.runtime.Player(), MonsterCombatUnit(monster.id)
}

func testAttributeStatus(id int64, tag, immuneTag int32, value float64) *SkillStatusPlan {
	return &SkillStatusPlan{
		ModifierID: id, Tag: tag, ImmuneTag: immuneTag, Attribute: 8,
		DurationSeconds: float64((10 * time.Second) / time.Second), ValueKey: 4, Value: value,
		BuffType: 0, CanBeCleared: true, IsBuff: true,
	}
}
