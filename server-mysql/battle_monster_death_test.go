package main

import (
	"testing"
	"time"
)

func TestMonsterDeathPreservesLaunchedAttack(t *testing.T) {
	for _, timing := range []string{"before_launch", "before_impact", "battle_ended", "alive"} {
		t.Run(timing, func(t *testing.T) {
			oldCatalog := skillLogicCatalog
			skillLogicCatalog = loadTestSkillLogic(t)
			t.Cleanup(func() { skillLogicCatalog = oldCatalog })
			session := newSession()
			session.playerID = 100
			attacker := &monsterUnit{id: 200, monsterID: 1, hp: 1000, maxHP: 1000, phyAtk: 100, alive: true}
			other := &monsterUnit{id: 201, monsterID: 1, hp: 1000, maxHP: 1000, alive: true}
			battle := &battleState{owner: session, playerHP: 1000, playerMaxHP: 1000, monsters: []*monsterUnit{attacker, other}}
			battle.runtime = NewCombatRuntime(battle, session.playerID, nil)
			session.battle = battle
			channel := &channel{id: 1, session: session, conn: &recordingConn{}}
			server, phases := newQueuedCombatServer()
			plan, err := skillLogicCatalog.Plan(500001, 1)
			if err != nil {
				t.Fatal(err)
			}
			wave := &monsterAttackWave{origin: channel, battle: battle, casts: []monsterSkillCast{{
				source: MonsterCombatUnit(attacker.id), primary: battle.runtime.Player(),
				skillID: 500001, plan: withMonsterSkillPresentation(plan), active: true,
			}}}
			kill := func() {
				if _, err := battle.runtime.ApplyDamage(CombatDamageRequest{Source: battle.runtime.Player(), Target: MonsterCombatUnit(attacker.id), Amount: 1000}); err != nil {
					t.Fatal(err)
				}
			}
			if timing == "before_launch" {
				kill()
			}
			server.launchMonsterAttackWave(wave)
			if timing == "before_impact" || timing == "battle_ended" {
				kill()
			}
			if timing == "battle_ended" {
				battle.ended = true
			}
			for phases.len() > 0 {
				phases.runNext(t, phases.items[0].delay)
			}
			if timing == "alive" || timing == "before_impact" {
				if battle.playerHP >= 1000 {
					t.Fatal("living monster did not deal damage")
				}
			} else if battle.playerHP != 1000 || wave.casts[0].active {
				t.Fatalf("dead monster attack not cancelled: hp=%d active=%v", battle.playerHP, wave.casts[0].active)
			}
		})
	}
}

func TestDeadMonsterLaunchedDamageAndExistingEffectsRemain(t *testing.T) {
	runtime, clock, player, monster := newEffectTestRuntime()
	applyTestEffect(t, runtime, monster, player, EffectSpec{
		Key: "burn", Kind: CombatEffectDamageOverTime, Value: 10,
		Interval: time.Second, Duration: 3 * time.Second,
	})
	applyTestEffect(t, runtime, monster, monster, EffectSpec{Key: "thorns", Kind: CombatEffectReflect, Percent: 25})
	if _, err := runtime.ApplyDamage(CombatDamageRequest{Source: player, Target: monster, Amount: 100}); err != nil {
		t.Fatal(err)
	}
	if unit, _ := runtime.Unit(player); unit.HP != 55 {
		t.Fatalf("lethal-hit reflect changed: hp=%d, want 55", unit.HP)
	}
	if _, err := runtime.ApplyDamage(CombatDamageRequest{Source: monster, Target: player, Amount: 20}); err != nil {
		t.Fatalf("dead monster direct damage error=%v", err)
	}
	clock.Add(time.Second)
	runtime.Tick()
	if unit, _ := runtime.Unit(player); unit.HP != 25 {
		t.Fatalf("existing burn after source death: hp=%d, want 25", unit.HP)
	}
}

func TestMonsterDiesDuringDamageClockAdvance(t *testing.T) {
	runtime, clock, player, monster := newEffectTestRuntime()
	applyTestEffect(t, runtime, player, monster, EffectSpec{
		Key: "lethal-dot", Kind: CombatEffectDamageOverTime, Value: 100,
		Interval: time.Second, Duration: 2 * time.Second,
	})
	clock.Add(time.Second)
	if _, err := runtime.ApplyDamage(CombatDamageRequest{Source: monster, Target: player, Amount: 20}); err != nil {
		t.Fatalf("monster killed by due DOT still attacked: err=%v", err)
	}
	if unit, _ := runtime.Unit(player); unit.HP != 60 {
		t.Fatalf("dead monster damaged player after clock advance: hp=%d", unit.HP)
	}
}
