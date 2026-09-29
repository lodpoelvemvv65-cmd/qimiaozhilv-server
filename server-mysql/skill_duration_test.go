package main

import "testing"

func findSkillStatus(effects []SkillEffect, modifierID int64) *SkillStatusPlan {
	for _, effect := range effects {
		if effect.ModifierID == modifierID && effect.Status != nil {
			return effect.Status
		}
		if status := findSkillStatus(effect.Success, modifierID); status != nil {
			return status
		}
		if status := findSkillStatus(effect.Failure, modifierID); status != nil {
			return status
		}
		if status := findSkillStatus(effect.Children, modifierID); status != nil {
			return status
		}
	}
	return nil
}

func TestOnlineModifierLevelAndDurationOverrides(t *testing.T) {
	loadOnlineTablesForTest(t)
	catalog := loadTestSkillLogic(t)
	tests := []struct {
		name       string
		skillID    int32
		modifierID int64
		active     []int32
		duration   float64
	}{
		{name: "anatomy revival", skillID: 310601, modifierID: 31060113, active: []int32{3}, duration: 7},
		// Demolition Bomb: the client's continueTime table holds -7 for the
		// level-4/5 spirit-defense debuff while its description says
		// "持续7秒". The sign is a data-entry artefact, so the duration is the
		// magnitude -- reading it verbatim made the debuff permanent.
		{name: "demolition bomb spirit defense", skillID: 110302, modifierID: 11030216, active: []int32{4, 5}, duration: 7},
		{name: "hook critical resistance", skillID: 210403, modifierID: 21040313, active: []int32{4, 5}, duration: 14},
		{name: "berserk attack", skillID: 210404, modifierID: 21040412, active: []int32{1, 2, 3, 4, 5}, duration: 14},
		{name: "beacon spirit defense", skillID: 410402, modifierID: 41040213, active: []int32{5}, duration: 7},
		{name: "magic shield defense", skillID: 410604, modifierID: 41060412, active: []int32{3}, duration: 14},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			active := make(map[int32]bool, len(test.active))
			for _, level := range test.active {
				active[level] = true
			}
			skill := catalog.Skills[test.skillID]
			for level := int32(1); level <= skill.MaxLevel; level++ {
				plan, err := catalog.Plan(test.skillID, level)
				if err != nil {
					t.Fatalf("Plan(%d,%d): %v", test.skillID, level, err)
				}
				status := findSkillStatus(plan.Effects, test.modifierID)
				if !active[level] {
					if status != nil {
						t.Errorf("modifier %d unexpectedly active at level %d: %+v", test.modifierID, level, status)
					}
					continue
				}
				if status == nil {
					t.Errorf("modifier %d missing at level %d", test.modifierID, level)
					continue
				}
				if status.DurationSeconds != test.duration {
					t.Errorf("modifier %d level %d duration = %v, want %v", test.modifierID, level, status.DurationSeconds, test.duration)
				}
			}
		})
	}
}
