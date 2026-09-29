package main

import "testing"

func TestMonsterDamageUsesConfiguredMechanics(t *testing.T) {
	tests := []struct {
		name    string
		skillID int32
		monster monsterUnit
		battle  battleState
		want    int32
	}{
		{
			name:    "500001 uses authoritative 100 percent physical attack",
			skillID: 500001,
			monster: monsterUnit{phyAtk: 110, spiAtk: 999},
			battle:  battleState{phyDef: 10, spiDef: 900, playerMaxHP: 1000},
			want:    100,
		},
		{
			name:    "500003 uses authoritative 200 percent physical attack",
			skillID: 500003,
			monster: monsterUnit{phyAtk: 110},
			battle:  battleState{phyDef: 10},
			want:    200,
		},
		{
			name:    "500015 uses 100 percent spiritual attack",
			skillID: 500015,
			monster: monsterUnit{phyAtk: 999, spiAtk: 210},
			battle:  battleState{phyDef: 900, spiDef: 10, playerMaxHP: 1000},
			want:    200,
		},
		{
			name:    "500012 removes 20 percent target max hp",
			skillID: 500012,
			monster: monsterUnit{},
			battle:  battleState{playerMaxHP: 1234},
			want:    246,
		},
		{
			name:    "500026 shares target max hp mechanic",
			skillID: 500026,
			monster: monsterUnit{},
			battle:  battleState{playerMaxHP: 1000},
			want:    200,
		},
		{
			name:    "unknown skill safely falls back to basic physical attack",
			skillID: 599999,
			monster: monsterUnit{phyAtk: 110},
			battle:  battleState{phyDef: 10},
			want:    100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.battle.monsterDamage(&tt.monster, tt.skillID); got != tt.want {
				t.Fatalf("monsterDamage(%d) = %d, want %d", tt.skillID, got, tt.want)
			}
		})
	}
}

func TestAllKnownMonsterSkillsHaveAuditedSpecs(t *testing.T) {
	for skillID := int32(500001); skillID <= 500042; skillID++ {
		if _, ok := monsterSkillSpecs[skillID]; !ok {
			t.Errorf("monster skill %d fell through to the unaudited fallback", skillID)
		}
	}
}

func TestNonImmediateMonsterSkillsDealNoPlayerDamage(t *testing.T) {
	b := battleState{playerMaxHP: 1000, phyDef: 10, spiDef: 10}
	m := monsterUnit{phyAtk: 500, spiAtk: 500}
	for _, skillID := range []int32{500009, 500023, 500031, 500032, 500033, 500034, 500035, 500036, 500037, 500038, 500042} {
		if got := b.monsterDamage(&m, skillID); got != 0 {
			t.Errorf("friendly skill %d dealt %d damage", skillID, got)
		}
	}
}

func TestMonsterDamageKeepsMinimumForDamagingSkills(t *testing.T) {
	b := battleState{phyDef: 1000, spiDef: 1000, playerMaxHP: 1}
	m := monsterUnit{phyAtk: 1, spiAtk: 1}
	for _, skillID := range []int32{500001, 500015, 500012} {
		if got := b.monsterDamage(&m, skillID); got != 1 {
			t.Errorf("damaging skill %d dealt %d damage, want minimum 1", skillID, got)
		}
	}
}

func TestMonsterDamageNilInputsAreSafe(t *testing.T) {
	var b *battleState
	if got := b.monsterDamage(&monsterUnit{}, 500001); got != 0 {
		t.Fatalf("nil battle damage = %d, want 0", got)
	}
	b = &battleState{}
	if got := b.monsterDamage(nil, 500001); got != 0 {
		t.Fatalf("nil monster damage = %d, want 0", got)
	}
}
