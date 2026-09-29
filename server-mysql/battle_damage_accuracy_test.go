package main

import (
	"testing"
	"time"
)

func TestDirectDamageMissSkipsDamageStatusAndReactiveEffects(t *testing.T) {
	owner := &session{transBonus: map[int32]float32{1032: 0}}
	monster := &monsterUnit{
		id: 200, hp: 1000, maxHP: 1000, alive: true,
		// Hit/Res 是点数（不再 ×100），要造出 100 点抗性就直接写 100。
		extraNumeric: map[int32]float64{1033: 100},
	}
	battle := &battleState{
		owner: owner, playerHP: 500, playerMaxHP: 1000, phyAtk: 100,
		monsters: []*monsterUnit{monster},
	}
	battle.runtime = NewCombatRuntime(battle, 100, nil)
	player, target := battle.runtime.Player(), MonsterCombatUnit(monster.id)
	if _, err := battle.runtime.ApplyEffects(CombatEffectContext{Source: target, Target: target}, []CombatEffect{
		EffectSpec{Key: "reflect", Kind: CombatEffectReflect, Percent: 100, Duration: time.Minute},
		EffectSpec{Key: "counter", Kind: CombatEffectCounter, Duration: time.Minute},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := battle.runtime.ApplyEffects(CombatEffectContext{Source: player, Target: player}, []CombatEffect{
		EffectSpec{Key: "lifesteal", Kind: CombatEffectLifesteal, Percent: 100, Duration: time.Minute},
	}); err != nil {
		t.Fatal(err)
	}
	status := &SkillStatusPlan{ModifierID: 9001, DurationSeconds: 10, StateKey: 9, IsDebuff: true, IsControl: true}
	plan := SkillPlan{Effects: []SkillEffect{
		directAccuracyDamage(SkillDamagePhysical),
		{
			Kind: SkillEffectStatus, Trigger: SkillTrigger{Scope: "skill", Event: 1},
			Target: SkillTargetPlan{Kind: SkillTargetSingle, Side: SkillTargetCastTarget},
			Status: status, ModifierID: status.ModifierID,
		},
	}}
	rolls := 0
	events, err := battle.executeSkillPlan(plan, player, target, func() float64 {
		rolls++
		return 0.75
	})
	if err != nil {
		t.Fatal(err)
	}
	if monster.hp != 1000 || battle.playerHP != 500 {
		t.Fatalf("miss changed HP: player=%d monster=%d", battle.playerHP, monster.hp)
	}
	if containsAnyCombatEvent(events, CombatEventDamage, CombatEventLifesteal, CombatEventReflect, CombatEventCounter) {
		t.Fatalf("miss triggered damage or reactive events: %+v", events)
	}
	if battle.runtime.HasStatus(target, CombatStatusStunned) {
		t.Fatal("unwrapped secondary status applied after its direct hit missed")
	}
	if rolls != 1 {
		t.Fatalf("miss rolls = %d, want one accuracy roll", rolls)
	}
}

func TestDirectDamageAccuracyKeepsMonsterAttacksIndependentOfPlayerResistance(t *testing.T) {
	tests := []struct {
		name       string
		sourceSide CombatUnitSide
		hit        float64
		resistance float64
		roll       float64
		wantHit    bool
	}{
		// Hit/Res 是点数：100 点抗性把配置 100% 压到 100×100/200 = 50%。
		{name: "player misses resistant monster", sourceSide: CombatSidePlayer, resistance: 100, roll: 0.75},
		{name: "player hit offsets resistance", sourceSide: CombatSidePlayer, hit: 100, resistance: 100, roll: 0.99, wantHit: true},
		{name: "monster damages resistant player", sourceSide: CombatSideMonster, resistance: 6906.25, roll: 0.999999, wantHit: true},
		{name: "monster hit offsets resistance", sourceSide: CombatSideMonster, hit: 1, resistance: 1, roll: 0.99, wantHit: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			owner := &session{transBonus: map[int32]float32{}}
			monster := &monsterUnit{id: 200, hp: 1000, maxHP: 1000, phyAtk: 100, alive: true, extraNumeric: map[int32]float64{}}
			battle := &battleState{owner: owner, playerHP: 1000, playerMaxHP: 1000, phyAtk: 100, monsters: []*monsterUnit{monster}}
			battle.runtime = NewCombatRuntime(battle, 100, nil)
			source, target := battle.runtime.Player(), MonsterCombatUnit(monster.id)
			if test.sourceSide == CombatSidePlayer {
				owner.transBonus[1032] = float32(test.hit)
				monster.extraNumeric[1033] = test.resistance
			} else {
				source, target = target, source
				monster.extraNumeric[1032] = test.hit
				owner.transBonus[1033] = float32(test.resistance)
			}
			events, err := battle.executeSkillPlan(SkillPlan{Effects: []SkillEffect{directAccuracyDamage(SkillDamagePhysical)}}, source, target, func() float64 { return test.roll })
			if err != nil {
				t.Fatal(err)
			}
			if got := containsCombatEvent(events, CombatEventDamage); got != test.wantHit {
				t.Fatalf("damage event=%t, want %t: %+v", got, test.wantHit, events)
			}
		})
	}
}

func TestHardMainStoryLayerFourMonsterDamagesHighResistancePlayer(t *testing.T) {
	loadOnlineTablesForTest(t)
	catalog := loadTestSkillLogic(t)
	row := tables.monsterBase[10164]
	if row == nil {
		t.Fatal("MonsterBase 10164 is missing")
	}
	owner := &session{playerID: 1, transBonus: map[int32]float32{1033: 6906.2544}}
	monster := &monsterUnit{
		id: 10164, monsterID: 10164, hp: int32(num(row["Hp"])), maxHP: int32(num(row["Hp"])),
		phyAtk: int32(num(row["PhyAtk"])), spiAtk: int32(num(row["SpiAtk"])), alive: true,
	}
	battle := &battleState{
		owner: owner, playerHP: 1000000, playerMaxHP: 1000000,
		phyDef: 117394, spiDef: 117394, monsters: []*monsterUnit{monster},
	}
	battle.runtime = NewCombatRuntime(battle, owner.playerID, nil)
	plan, err := catalog.Plan(500017, 1)
	if err != nil {
		t.Fatal(err)
	}
	source, target := MonsterCombatUnit(monster.id), battle.runtime.Player()
	events, err := battle.executeSkillPlan(plan, source, target, func() float64 { return 0.999999 })
	if err != nil {
		t.Fatal(err)
	}
	if got := combatDamageAmount(events, source, target); got != 142988 {
		t.Fatalf("达人4怪物伤害 = %d, want 142988: %+v", got, events)
	}
	if battle.playerHP != 857012 {
		t.Fatalf("达人4怪物攻击后 HP = %d, want 857012", battle.playerHP)
	}
}

func TestTrueAndPeriodicDamageBypassDirectAccuracy(t *testing.T) {
	owner := &session{transBonus: map[int32]float32{1032: -0.99}}
	monster := &monsterUnit{id: 200, hp: 1000, maxHP: 1000, alive: true, extraNumeric: map[int32]float64{1033: 10}}
	battle := &battleState{owner: owner, playerHP: 1000, playerMaxHP: 1000, phyAtk: 100, monsters: []*monsterUnit{monster}}
	battle.runtime = NewCombatRuntime(battle, 100, nil)
	player, target := battle.runtime.Player(), MonsterCombatUnit(monster.id)
	rolls := 0
	roll := func() float64 { rolls++; return 0.999999 }

	events, err := battle.executeSkillPlan(SkillPlan{Effects: []SkillEffect{directAccuracyDamage(SkillDamageTrue)}}, player, target, roll)
	if err != nil || !containsCombatEvent(events, CombatEventDamage) {
		t.Fatalf("true damage was accuracy-gated: err=%v events=%+v", err, events)
	}
	status := &SkillStatusPlan{ModifierID: 9002, DurationSeconds: 5}
	periodic := directAccuracyDamage(SkillDamagePhysical)
	periodic.Damage.Periodic = true
	if _, err = battle.executeSkillEffect(periodic, player, target, target, status, roll); err != nil {
		t.Fatal(err)
	}
	if rolls != 0 {
		t.Fatalf("true/periodic damage used %d accuracy rolls, want zero", rolls)
	}
}

func TestIndependentStatusChanceCanSucceedAfterDamageMiss(t *testing.T) {
	monster := &monsterUnit{id: 200, hp: 1000, maxHP: 1000, alive: true, extraNumeric: map[int32]float64{1033: 100}}
	battle := &battleState{owner: &session{}, playerHP: 1000, playerMaxHP: 1000, phyAtk: 100, monsters: []*monsterUnit{monster}}
	battle.runtime = NewCombatRuntime(battle, 100, nil)
	status := &SkillStatusPlan{ModifierID: 9003, DurationSeconds: 10, StateKey: 9, IsDebuff: true, IsControl: true}
	plan := SkillPlan{Effects: []SkillEffect{directAccuracyDamage(SkillDamagePhysical), triggeredStatusChance(status)}}
	rolls := []float64{0.75, 0}
	index := 0
	if _, err := battle.executeSkillPlan(plan, battle.runtime.Player(), MonsterCombatUnit(monster.id), func() float64 {
		value := rolls[index]
		index++
		return value
	}); err != nil {
		t.Fatal(err)
	}
	if monster.hp != 1000 || !battle.runtime.HasStatus(MonsterCombatUnit(monster.id), CombatStatusStunned) {
		t.Fatalf("independent status after miss: hp=%d stunned=%t", monster.hp, battle.runtime.HasStatus(MonsterCombatUnit(monster.id), CombatStatusStunned))
	}
	if index != len(rolls) {
		t.Fatalf("accuracy/chance rolls = %d, want %d", index, len(rolls))
	}
}

func directAccuracyDamage(damageType SkillDamageType) SkillEffect {
	return SkillEffect{
		Kind: SkillEffectDamage, Trigger: SkillTrigger{Scope: "skill", Event: 1},
		Target: SkillTargetPlan{Kind: SkillTargetSingle, Side: SkillTargetCastTarget},
		Damage: &SkillDamagePlan{Type: damageType, Self: SkillFormula{Stat: SkillStatPhysicalAttack, Percent: 100}},
	}
}

func containsAnyCombatEvent(events []CombatEvent, types ...CombatEventType) bool {
	for _, eventType := range types {
		if containsCombatEvent(events, eventType) {
			return true
		}
	}
	return false
}
