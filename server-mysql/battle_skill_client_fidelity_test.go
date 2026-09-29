package main

import (
	"strings"
	"testing"
)

// 本文件锁定 A 类修复（服务端没照客户端 SkillLogicConfig 执行）。
// 每项断言都以客户端随包配置为准，不做任何客户端改动。

// A1：110602 殊途同归的【反伤】百分比来自选项 param（20/23/28），
// 不是运行时回退的 100%。
func TestFellowReflectUsesConfiguredParam(t *testing.T) {
	catalog := loadTestSkillLogic(t)
	want := map[int32]float64{1: 20, 2: 23, 3: 28}
	for level, percent := range want {
		plan, err := catalog.Plan(110602, level)
		if err != nil {
			t.Fatalf("Plan(110602,%d): %v", level, err)
		}
		status := findSkillStatusEffect(plan.Effects, 11060211)
		if status == nil {
			t.Fatalf("level %d: modifier 11060211 missing", level)
		}
		reflect := findSkillEffect(status.Children, SkillEffectReflect, 6)
		if reflect == nil {
			t.Fatalf("level %d: reflect effect missing", level)
		}
		if reflect.ParamPercent != percent {
			t.Fatalf("level %d: reflect percent = %v, want %v", level, reflect.ParamPercent, percent)
		}
	}
}

// A2：210601 猛虎式射门的事件 24 在子弹 Modifier（挂在敌人身上）被创建前执行，
// 它的 targetType=0（自身）必须解析成施法者，而不是子弹的持有者。
func TestTigerShotEvent24BuffsCasterNotBulletHolder(t *testing.T) {
	catalog := loadTestSkillLogic(t)
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = catalog
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })

	battle, caster, enemy := newModifierTestBattle()
	plan, err := catalog.Plan(210601, 3)
	if err != nil {
		t.Fatal(err)
	}
	events, err := battle.executeSkillPlanWithContext(plan, caster, enemy, newSkillExecutionContext(battle, func() float64 { return 0 }))
	if err != nil {
		t.Fatal(err)
	}

	applied := map[CombatUnitRef]int{}
	for _, event := range events {
		if !strings.HasPrefix(event.Key, "modifier:21060111:attribute-") {
			continue
		}
		switch event.Type {
		case CombatEventEffectApplied, CombatEventEffectRefreshed, CombatEventEffectStacked:
			applied[event.Target]++
		}
	}
	if applied[caster] == 0 {
		t.Fatalf("caster never received the physical-attack buff: %+v", events)
	}
	if applied[enemy] != 0 {
		t.Fatalf("bullet holder (enemy) wrongly received the self buff: %+v", events)
	}
}

// A3：310601 人体学在治疗暴击时把护盾加给「被治疗者」，
// 而不是把施法者当成技能主目标。
func TestAnatomyCriticalShieldLandsOnHealTarget(t *testing.T) {
	catalog := loadTestSkillLogic(t)
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = catalog
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })

	_, _, first, second := newSharedPartyBattleForTest(t)
	first.session.transBonus = map[int32]float32{1014: 1}
	second.session.transBonus = map[int32]float32{1014: 1}
	battle := first.session.battle
	battle.spiAtk = 100
	caster := PlayerCombatUnit(first.session.playerID)
	target := PlayerCombatUnit(second.session.playerID)
	// 被治疗者要先掉血，治疗量与治疗暴击才会真正产生。
	if _, err := battle.runtime.ApplyDamage(CombatDamageRequest{
		Source: MonsterCombatUnit(battle.monsters[0].id), Target: target, Amount: 300,
	}); err != nil {
		t.Fatal(err)
	}

	plan, err := catalog.Plan(310601, 3)
	if err != nil {
		t.Fatal(err)
	}
	events, err := battle.executeSkillPlanWithContext(plan, caster, target, newSkillExecutionContext(battle, func() float64 { return 0 }))
	if err != nil {
		t.Fatal(err)
	}

	criticalHeal := false
	for _, event := range events {
		if event.Type == CombatEventHeal && event.Target == target && event.IsCrit {
			criticalHeal = true
		}
	}
	if !criticalHeal {
		t.Fatalf("test setup did not produce a critical heal on the ally: %+v", events)
	}

	shielded := map[CombatUnitRef]int{}
	for _, event := range events {
		if event.Key != "modifier:31060112:shield" {
			continue
		}
		switch event.Type {
		case CombatEventEffectApplied, CombatEventEffectRefreshed, CombatEventEffectStacked:
			shielded[event.Target]++
		}
	}
	if shielded[target] == 0 {
		t.Fatalf("healed ally never received the shield: %+v", events)
	}
	if shielded[caster] != 0 {
		t.Fatalf("healer wrongly received the shield meant for the ally: %+v", events)
	}
	if unit, ok := battle.runtime.Unit(target); !ok || unit.Shield <= 0 {
		t.Fatalf("healed ally shield = %+v, want > 0", unit)
	}
}

func findSkillEffect(effects []SkillEffect, kind SkillEffectKind, event int32) *SkillEffect {
	for index := range effects {
		effect := &effects[index]
		if effect.Kind == kind && effect.Trigger.Event == event {
			return effect
		}
		for _, nested := range [][]SkillEffect{effect.Children, effect.Success, effect.Failure} {
			if found := findSkillEffect(nested, kind, event); found != nil {
				return found
			}
		}
	}
	return nil
}

func findSkillStatusEffect(effects []SkillEffect, modifierID int64) *SkillEffect {
	for index := range effects {
		effect := &effects[index]
		if effect.Status != nil && effect.Status.ModifierID == modifierID {
			return effect
		}
		for _, nested := range [][]SkillEffect{effect.Children, effect.Success, effect.Failure} {
			if found := findSkillStatusEffect(nested, modifierID); found != nil {
				return found
			}
		}
	}
	return nil
}
