package main

import (
	"testing"

	"mhqserver/protocol"
)

func TestApplyGMJobAdjustSetsTrans(t *testing.T) {
	ss := &session{jobID: int32(protocol.JobType_Officer), trans: 0, skills: map[int32]int32{100001: 1}, skillOrder: []int32{100001}}
	trans := "2"
	if err := applyGMJobAdjust(ss, gmJobPayload{Trans: &trans}); err != nil {
		t.Fatal(err)
	}
	if ss.trans != 2 {
		t.Fatalf("trans=%d", ss.trans)
	}
}

func TestApplyGMJobAdjustTransfersFamilyAndRejectsSame(t *testing.T) {
	ss := &session{jobID: 1, skills: map[int32]int32{100001: 1, 110101: 2}, skillOrder: []int32{100001, 110101}, bag: map[int32]*bagItem{}, worn: map[int32]*bagItem{}}
	same := "1"
	if err := applyGMJobAdjust(ss, gmJobPayload{JobType: &same}); err == nil {
		t.Fatal("same job accepted")
	}
	next := "2"
	if err := applyGMJobAdjust(ss, gmJobPayload{JobType: &next}); err != nil {
		t.Fatal(err)
	}
	if ss.jobID != 3 || ss.skills[100001] != 0 && ss.skills[baseSkillOfJob(3)] < 1 {
		t.Fatalf("job=%d skills=%v", ss.jobID, ss.skills)
	}
	if _, ok := ss.skills[110101]; ok {
		t.Fatalf("old job skill kept %v", ss.skills)
	}
}
