package main

import "testing"

func TestApplyGMPetAdjustChangesFormAndResetsBusyState(t *testing.T) {
	orig := tables
	tables = &datatables{
		petConfig: map[int64]map[string]interface{}{
			2101: {"Name": "宠物01"},
			2102: {"Name": "宠物02"},
		},
		petLevelConfig: map[int64]map[string]interface{}{
			2101: {"MaxLevel": int64(100)},
			2102: {"MaxLevel": int64(150)},
		},
	}
	t.Cleanup(func() { tables = orig })

	petID := "2102"
	level := "12"
	exp := "30"
	intimacy := "40"
	show := false
	p := &petState{PetId: 2101, Level: 3, Exp: 8, Intimacy: 9, Name: "旧名字", IsShow: true, PetState: petStateExplore, ActionEnd: 99, Rewarded: true}
	if err := applyGMPetAdjust(p, gmPetPayload{PetID: &petID, Level: &level, Exp: &exp, Intimacy: &intimacy, IsShow: &show}); err != nil {
		t.Fatal(err)
	}
	if p.PetId != 2102 || p.Name != "宠物02" || p.Level != 12 || p.Exp != 30 || p.Intimacy != 40 {
		t.Fatalf("pet fields = %+v", p)
	}
	if p.IsShow || p.PetState != petStateIdle || p.ActionEnd != 0 || p.Rewarded {
		t.Fatalf("busy pet was not reset: %+v", p)
	}
}

func TestApplyGMPetAdjustRejectsUnknownAndOverMax(t *testing.T) {
	orig := tables
	tables = &datatables{
		petConfig:      map[int64]map[string]interface{}{2101: {"Name": "宠物01"}},
		petLevelConfig: map[int64]map[string]interface{}{2101: {"MaxLevel": int64(10)}},
	}
	t.Cleanup(func() { tables = orig })

	p := &petState{PetId: 2101, Level: 1, Name: "宠物01"}
	unknown := "2199"
	if err := applyGMPetAdjust(p, gmPetPayload{PetID: &unknown}); err == nil {
		t.Fatal("unknown pet id accepted")
	}
	if p.PetId != 2101 {
		t.Fatalf("failed adjust mutated pet id to %d", p.PetId)
	}
	tooHigh := "11"
	if err := applyGMPetAdjust(p, gmPetPayload{Level: &tooHigh}); err == nil {
		t.Fatal("over-max pet level accepted")
	}
	if p.Level != 1 {
		t.Fatalf("failed adjust mutated level to %d", p.Level)
	}
}

func TestApplyGMPetAdjustKeepsCustomName(t *testing.T) {
	orig := tables
	tables = &datatables{
		petConfig:      map[int64]map[string]interface{}{2101: {"Name": "宠物01"}, 2105: {"Name": "宠物05"}},
		petLevelConfig: map[int64]map[string]interface{}{2101: {"MaxLevel": int64(100)}, 2105: {"MaxLevel": int64(200)}},
	}
	t.Cleanup(func() { tables = orig })

	petID := "2105"
	name := "自定义"
	p := &petState{PetId: 2101, Level: 1, Name: "宠物01"}
	if err := applyGMPetAdjust(p, gmPetPayload{PetID: &petID, Name: &name}); err != nil {
		t.Fatal(err)
	}
	if p.PetId != 2105 || p.Name != "自定义" {
		t.Fatalf("custom name not kept: %+v", p)
	}
}
