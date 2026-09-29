package main

import (
	"testing"

	"mhqserver/protocol"
)

func TestApplyGMSkillAdjustLearnsAndSetsPoints(t *testing.T) {
	orig := tables
	tables = &datatables{skillConfig: map[int64]map[string]interface{}{
		11010100: {"MaxLevel": int64(7)},
	}}
	t.Cleanup(func() { tables = orig })

	ss := &session{
		jobID: int32(protocol.JobType_Officer), skillPoint: 1,
		skills: map[int32]int32{100001: 1}, skillOrder: []int32{100001},
	}
	skillID := "110101"
	level := "3"
	delta := "4"
	if err := applyGMSkillAdjust(ss, gmSkillPayload{
		SkillPointDelta: &delta,
		Skills:          []gmSkillEntry{{SkillID: &skillID, Level: &level}},
	}); err != nil {
		t.Fatal(err)
	}
	if ss.skillPoint != 5 || ss.skills[110101] != 3 || len(ss.skillOrder) != 2 || ss.skillOrder[1] != 110101 {
		t.Fatalf("skills=%v order=%v points=%d", ss.skills, ss.skillOrder, ss.skillPoint)
	}
}

func TestApplyGMSkillAdjustRejectsOtherJobAndBaseUnlearn(t *testing.T) {
	ss := &session{
		jobID: int32(protocol.JobType_Officer), skillPoint: 2,
		skills: map[int32]int32{100001: 1, 110101: 2}, skillOrder: []int32{100001, 110101}, autoSkills: []int32{110101},
	}
	other := "210101"
	level := "1"
	if err := applyGMSkillAdjust(ss, gmSkillPayload{Skills: []gmSkillEntry{{SkillID: &other, Level: &level}}}); err == nil {
		t.Fatal("other-job skill accepted")
	}
	base := "100001"
	zero := "0"
	if err := applyGMSkillAdjust(ss, gmSkillPayload{Skills: []gmSkillEntry{{SkillID: &base, Level: &zero}}}); err == nil {
		t.Fatal("base skill unlearn accepted")
	}
	forget := "110101"
	if err := applyGMSkillAdjust(ss, gmSkillPayload{Skills: []gmSkillEntry{{SkillID: &forget, Level: &zero}}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := ss.skills[110101]; ok || len(ss.autoSkills) != 0 || len(ss.skillOrder) != 1 {
		t.Fatalf("unlearn leftover skills=%v order=%v auto=%v", ss.skills, ss.skillOrder, ss.autoSkills)
	}
}
