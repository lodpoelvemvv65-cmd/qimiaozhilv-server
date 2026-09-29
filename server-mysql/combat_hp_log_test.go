package main

import "testing"

func TestShouldLogBattleHPWriteCatchesZeroAndDrops(t *testing.T) {
	tests := []struct {
		name                            string
		requested, before, after, maxHP int32
		want                            bool
	}{
		{name: "unchanged", requested: 100, before: 100, after: 100, maxHP: 800_000_000},
		{name: "small heal", requested: 120, before: 100, after: 120, maxHP: 800_000_000},
		{name: "drop to twenty", requested: 20, before: 800_000_000, after: 20, maxHP: 800_000_000, want: true},
		{name: "drop to zero", requested: 0, before: 800_000_000, after: 0, maxHP: 800_000_000, want: true},
		{name: "negative clamp", requested: -2, before: 800_000_000, after: 0, maxHP: 800_000_000, want: true},
		{name: "recover from zero", requested: 1, before: 0, after: 1, maxHP: 800_000_000, want: true},
		{name: "large heal", requested: 800_000_000, before: 1, after: 800_000_000, maxHP: 800_000_000, want: true},
	}
	for _, test := range tests {
		if got := shouldLogBattleHPWrite(test.requested, test.before, test.after, test.maxHP); got != test.want {
			t.Fatalf("%s = %t, want %t", test.name, got, test.want)
		}
	}
}

func TestCombatMonsterSummaryGroupsRoster(t *testing.T) {
	got := combatMonsterSummary([]*monsterUnit{
		{monsterID: 10023, phyAtk: 200},
		{monsterID: 10023, phyAtk: 200},
		{monsterID: 10024, phyAtk: 50},
	})
	if got != "monsters=10023x2 atk=200,10024x1 atk=50" {
		t.Fatalf("summary = %q", got)
	}
}

func TestCombatDamageLogIDsUsePlayerAndMonster(t *testing.T) {
	monster := &monsterUnit{id: 11, monsterID: 10023}
	battle := &battleState{monsters: []*monsterUnit{monster}}
	runtime := &CombatRuntime{battle: battle, playerID: 3894}
	uid, monsterID := combatDamageLogIDs(runtime, MonsterCombatUnit(monster.id), PlayerCombatUnit(3894))
	if uid != 3894 || monsterID != 10023 {
		t.Fatalf("uid=%d monster=%d, want 3894/10023", uid, monsterID)
	}
}

func TestSetBattleHPLogsDropToZero(t *testing.T) {
	ss := newSession()
	ss.playerID = 3894
	ss.hp = 800_000_000
	ss.setBattleHP(0)
	if ss.hp != 0 {
		t.Fatalf("hp=%d, want 0", ss.hp)
	}
}
