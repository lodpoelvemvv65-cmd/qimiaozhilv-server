package main

import "testing"

func TestAnatomyNormalHealDoesNotApplyCriticalShield(t *testing.T) {
	catalog := loadTestSkillLogic(t)
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = catalog
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })
	owner := &session{playerID: 100, transBonus: map[int32]float32{1014: 1}}
	battle := &battleState{owner: owner, playerHP: 500, playerMaxHP: 1000, spiAtk: 100}
	battle.runtime = NewCombatRuntime(battle, owner.playerID, nil)
	plan, err := catalog.Plan(310601, 3)
	if err != nil {
		t.Fatal(err)
	}
	events, err := battle.executeSkillPlanWithContext(plan, battle.runtime.Player(), battle.runtime.Player(), newSkillExecutionContext(battle, func() float64 { return 1 }))
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Key == "modifier:31060112:shield" {
			t.Fatalf("normal Anatomy heal unexpectedly applied shield: %+v", events)
		}
	}
	if unit, ok := battle.runtime.Unit(battle.runtime.Player()); !ok || unit.Shield != 0 {
		t.Fatalf("normal Anatomy heal shield=%+v, want zero", unit)
	}
}

func TestAnatomyAndAdvancedFirstAidCriticalShieldDoesNotRecurse(t *testing.T) {
	catalog := loadTestSkillLogic(t)
	owner := &session{transBonus: map[int32]float32{1014: 1}}
	battle := &battleState{owner: owner, playerHP: 500, playerMaxHP: 1000, spiAtk: 100}
	battle.runtime = NewCombatRuntime(battle, 100, nil)
	player := battle.runtime.Player()
	rolls := 0
	ctx := newSkillExecutionContext(battle, func() float64 {
		rolls++
		if rolls > 16 {
			t.Fatal("critical shield recursively triggered its own modifier event")
		}
		return 0
	})
	anatomy, err := catalog.Plan(310601, 3)
	if err != nil {
		t.Fatal(err)
	}
	anatomyEvents, err := battle.executeSkillPlanWithContext(anatomy, player, player, ctx)
	if err != nil {
		t.Fatal(err)
	}
	anatomyShields := 0
	for _, event := range anatomyEvents {
		if event.Key == "modifier:31060112:shield" &&
			(event.Type == CombatEventEffectApplied || event.Type == CombatEventEffectRefreshed || event.Type == CombatEventEffectStacked) {
			anatomyShields++
		}
	}
	if anatomyShields != 1 {
		t.Fatalf("critical Anatomy cast must apply its shield immediately: shields=%d events=%+v", anatomyShields, anatomyEvents)
	}
	firstAid, err := catalog.Plan(310401, 5)
	if err != nil {
		t.Fatal(err)
	}
	// Reuse the context to ensure a completed callback can run again for the
	// next cast. The shield may itself crit, but cannot recursively grant itself.
	for cast := 0; cast < 2; cast++ {
		battle.playerHP = 500
		events, err := battle.executeSkillPlanWithContext(firstAid, player, player, ctx)
		if err != nil {
			t.Fatal(err)
		}
		criticalHeal, regularHeal, shields := false, false, 0
		for _, event := range events {
			if event.Type == CombatEventHeal {
				criticalHeal = criticalHeal || event.IsCrit
				regularHeal = regularHeal || !event.IsCrit
			}
			if event.Key == "modifier:31060112:shield" &&
				(event.Type == CombatEventEffectApplied || event.Type == CombatEventEffectRefreshed) {
				shields++
				if !event.IsCrit {
					t.Fatal("reactive shield lost its configured critical effect")
				}
			}
		}
		if !criticalHeal || !regularHeal || shields != 1 {
			t.Fatalf("cast %d: critical heal=%v secondary heal=%v shields=%d events=%+v",
				cast, criticalHeal, regularHeal, shields, events)
		}
		if unit, ok := battle.runtime.Unit(player); !ok || unit.Shield <= 0 || unit.HP <= 500 {
			t.Fatalf("cast %d did not retain its healing and shield: %+v", cast, unit)
		}
	}
}

func TestModifierReentryGuardAllowsOtherHoldersAndLaterEvents(t *testing.T) {
	battle, player, monster := newModifierTestBattle()
	battle.playerHP, battle.monsters[0].hp = 500, 500
	status := &SkillStatusPlan{ModifierID: 9001, DurationSeconds: 10}
	effect := SkillEffect{Kind: SkillEffectStatus, Status: status, Children: []SkillEffect{{
		Kind: SkillEffectHeal, Trigger: SkillTrigger{Event: 23},
		Target:    SkillTargetPlan{Kind: SkillTargetSingle, Side: SkillTargetEventSource},
		Treatment: &SkillTreatmentPlan{Target: SkillFormula{Stat: SkillStatMaxHP, Percent: 10}},
	}}}
	for _, holder := range []CombatUnitRef{player, monster} {
		if _, err := battle.applyStatusEffect(effect, holder, holder, holder, nil); err != nil {
			t.Fatal(err)
		}
	}
	ctx := newSkillExecutionContext(battle, nil)
	for event := 1; event <= 2; event++ {
		events, err := battle.triggerModifierEvent(23, player, monster, ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 2 || events[0].Target != monster || events[1].Target != player {
			t.Fatalf("event %d must heal each holder once: %+v", event, events)
		}
		wantHP := int32(500 + 100*event)
		if battle.playerHP != wantHP || battle.monsters[0].hp != wantHP {
			t.Fatalf("event %d: player=%d monster=%d, want %d each", event, battle.playerHP, battle.monsters[0].hp, wantHP)
		}
	}
}

func TestModifierReentryGuardClearsAfterCallbackError(t *testing.T) {
	battle, player, monster := newModifierTestBattle()
	status := &SkillStatusPlan{ModifierID: 9001, DurationSeconds: 10}
	effect := SkillEffect{Kind: SkillEffectStatus, Status: status, Children: []SkillEffect{{
		Kind: SkillEffectChangeDamage, Trigger: SkillTrigger{Event: 7}, ParamPercent: -1,
	}}}
	if _, err := battle.applyStatusEffect(effect, player, player, player, nil); err != nil {
		t.Fatal(err)
	}
	ctx := newSkillExecutionContext(battle, nil)
	ctx.triggerEvent, ctx.triggerAmount = 5, 123
	if _, err := battle.triggerModifierEvent(7, player, monster, ctx, 10); err == nil {
		t.Fatal("invalid callback did not return an error")
	}
	if ctx.triggerEvent != 5 || ctx.triggerAmount != 123 {
		t.Fatal("callback failed to restore its enclosing event context")
	}
	for _, hook := range battle.modifierHooks {
		hook.effects[7] = []SkillEffect{{
			Kind: SkillEffectVisual, EffectID: 2308,
			Target: SkillTargetPlan{Kind: SkillTargetSingle, Side: SkillTargetHolder},
		}}
	}
	events, err := battle.triggerModifierEvent(7, player, monster, ctx, 10)
	if err != nil || len(events) != 1 || events[0].Type != CombatEventVisual {
		t.Fatalf("callback stayed blocked after error: events=%+v err=%v", events, err)
	}
}
