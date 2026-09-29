package main

import (
	"reflect"
	"testing"
)

func TestRandomSkillTargetCountIncludesZeroAndMaximum(t *testing.T) {
	battle, source, primary := newSkillTargetTestBattle(5)
	plan := SkillTargetPlan{
		Kind: SkillTargetMulti, Side: SkillTargetEnemy,
		Random: true, MinCount: 0, MaxCount: 5,
	}
	if targets := battle.resolveSkillTargetsWithIntn(plan, source, primary, source, CombatUnitRef{}, func(int) int { return 0 }); len(targets) != 0 {
		t.Fatalf("minimum random target count = %d, want 0: %+v", len(targets), targets)
	}
	targets := battle.resolveSkillTargetsWithIntn(plan, source, primary, source, CombatUnitRef{}, func(limit int) int { return limit - 1 })
	if len(targets) != 5 {
		t.Fatalf("maximum random target count = %d, want 5: %+v", len(targets), targets)
	}
	seen := make(map[CombatUnitRef]struct{}, len(targets))
	for _, target := range targets {
		seen[target] = struct{}{}
	}
	if len(seen) != len(targets) {
		t.Fatalf("random targets contain duplicates: %+v", targets)
	}
}

func TestRandomSkillTargetsAreSampledWithoutReplacement(t *testing.T) {
	battle, source, primary := newSkillTargetTestBattle(5)
	plan := SkillTargetPlan{
		Kind: SkillTargetMulti, Side: SkillTargetEnemy,
		Random: true, MinCount: 2, MaxCount: 2,
	}
	targets := battle.resolveSkillTargetsWithIntn(plan, source, primary, source, CombatUnitRef{}, func(limit int) int { return limit - 1 })
	want := []CombatUnitRef{MonsterCombatUnit(204), MonsterCombatUnit(200)}
	if !reflect.DeepEqual(targets, want) {
		t.Fatalf("random targets = %+v, want %+v", targets, want)
	}
}

func TestMonsterMultiTargetCanReachEveryPartyMember(t *testing.T) {
	_, _, first, _ := newSharedPartyBattleForTest(t)
	battle := first.session.battle
	source := MonsterCombatUnit(battle.monsters[0].id)
	primary := battle.runtime.Player()
	plan := SkillTargetPlan{Kind: SkillTargetMulti, Side: SkillTargetEnemy, MaxCount: 5}
	targets := battle.resolveSkillTargetsWithIntn(plan, source, primary, source, CombatUnitRef{}, func(int) int {
		t.Fatal("non-random selection called intn")
		return 0
	})
	want := []CombatUnitRef{PlayerCombatUnit(101), PlayerCombatUnit(202)}
	if !reflect.DeepEqual(targets, want) {
		t.Fatalf("monster party targets = %+v, want %+v", targets, want)
	}
}

func newSkillTargetTestBattle(monsterCount int) (*battleState, CombatUnitRef, CombatUnitRef) {
	monsters := make([]*monsterUnit, 0, monsterCount)
	for index := 0; index < monsterCount; index++ {
		monsters = append(monsters, &monsterUnit{id: int64(200 + index), hp: 100, maxHP: 100, alive: true})
	}
	battle := &battleState{playerHP: 100, playerMaxHP: 100, monsters: monsters}
	battle.runtime = NewCombatRuntime(battle, 100, nil)
	primary := CombatUnitRef{}
	if len(monsters) > 0 {
		primary = MonsterCombatUnit(monsters[0].id)
	}
	return battle, battle.runtime.Player(), primary
}
