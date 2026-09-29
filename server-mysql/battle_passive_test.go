package main

import (
	"testing"
	"time"
)

func TestRealFirePassiveAddsAndExpiresDefenseAfterKill(t *testing.T) {
	catalog := loadTestSkillLogic(t)
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = catalog
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })

	clock := &fakeCombatClock{now: time.Unix(4000, 0)}
	owner := newSession()
	owner.playerID = 100
	owner.skills = map[int32]int32{420201: 1}
	monster := &monsterUnit{id: 200, hp: 1, maxHP: 1, alive: true}
	battle := &battleState{
		owner: owner, playerHP: 1000, playerMaxHP: 1000,
		phyAtk: 100, phyDef: 100, spiDef: 100, monsters: []*monsterUnit{monster},
	}
	battle.runtime = NewCombatRuntime(battle, owner.playerID, clock)
	if _, err := battle.initializePassiveSkills(owner, func() float64 { return 0 }); err != nil {
		t.Fatal(err)
	}
	player := battle.runtime.Player()
	if got := battle.runtime.EffectiveAttribute(player, CombatAttributePhysicalDefense); got != 100 {
		t.Fatalf("fire passive granted defense before a kill: %d", got)
	}

	attack := SkillPlan{Effects: []SkillEffect{{
		Kind:    SkillEffectDamage,
		Trigger: SkillTrigger{Scope: "skill", Event: 1},
		Target:  SkillTargetPlan{Kind: SkillTargetSingle, Side: SkillTargetCastTarget},
		Damage:  &SkillDamagePlan{Type: SkillDamageTrue, Self: SkillFormula{Stat: SkillStatPhysicalAttack, Percent: 100}},
	}}}
	if _, err := battle.executeSkillPlan(attack, player, MonsterCombatUnit(monster.id), func() float64 { return 0 }); err != nil {
		t.Fatal(err)
	}
	if monster.alive {
		t.Fatal("test attack did not kill the monster")
	}
	if got := battle.runtime.EffectiveAttribute(player, CombatAttributePhysicalDefense); got != 104 {
		t.Fatalf("fire passive defense after kill = %d, want 104", got)
	}

	clock.Add(7 * time.Second)
	expired := battle.runtime.Tick()
	if got := battle.runtime.EffectiveAttribute(player, CombatAttributePhysicalDefense); got != 100 {
		t.Fatalf("fire passive defense after 7 seconds = %d, want 100", got)
	}
	if !containsCombatEvent(expired, CombatEventEffectExpired) {
		t.Fatalf("fire passive did not expire at 7 seconds: %+v", expired)
	}
}
