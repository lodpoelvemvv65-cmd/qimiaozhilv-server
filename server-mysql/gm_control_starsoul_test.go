package main

import "testing"

func TestApplyGMStarSoulAdjustUpdatesLevelLockAndExp(t *testing.T) {
	bag := newStarSoulBag()
	item := &starSoulItem{ID: 91001, TypeID: 1001, PosType: 0, Quality: 6, Level: 1, Exp: 0, Main: 1103}
	bag.Items[item.ID] = item
	id := "91001"
	level := "8"
	exp := "12"
	locked := true
	applied, err := applyGMStarSoulAdjust(bag, gmStarSoulPayload{
		StarSoulID: &id, Level: &level, Exp: &exp, IsLocked: &locked,
	})
	if err != nil {
		t.Fatal(err)
	}
	if item.Level != 8 || item.Exp != 12 || !item.IsLocked || len(applied.IDs) != 1 || applied.IDs[0] != 91001 {
		t.Fatalf("item=%+v applied=%+v", item, applied)
	}
}

func TestApplyGMStarSoulAdjustRejectsUnknownTypeAndMixedPayload(t *testing.T) {
	orig := tables
	tables = &datatables{starSoulType: map[int64]map[string]interface{}{}}
	t.Cleanup(func() { tables = orig })
	bag := newStarSoulBag()
	typeID := "1001"
	pos := "0"
	quality := "6"
	if _, err := applyGMStarSoulAdjust(bag, gmStarSoulPayload{TypeID: &typeID, PosType: &pos, Quality: &quality}); err == nil {
		t.Fatal("unknown type accepted")
	}
	id := "91001"
	bag.Items[91001] = &starSoulItem{ID: 91001, TypeID: 1001, PosType: 0, Quality: 6, Main: 1103}
	if _, err := applyGMStarSoulAdjust(bag, gmStarSoulPayload{StarSoulID: &id, TypeID: &typeID}); err == nil {
		t.Fatal("mixed payload accepted")
	}
}

func TestApplyGMStarSoulAdjustRemovesEquippedItem(t *testing.T) {
	bag := newStarSoulBag()
	item := &starSoulItem{ID: 91002, TypeID: 1001, PosType: 3, Quality: 4, Level: 2, Main: 1103, IsUsed: true}
	bag.Items[item.ID] = item
	bag.Used[3] = item.ID
	id := "91002"
	remove := true
	applied, err := applyGMStarSoulAdjust(bag, gmStarSoulPayload{StarSoulID: &id, Remove: &remove})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := bag.Items[91002]; ok || bag.Used[3] != 0 || !applied.Attrs || len(applied.Slots) != 1 || applied.Slots[0] != 3 {
		t.Fatalf("bag=%+v used=%v applied=%+v", bag.Items, bag.Used, applied)
	}
}

func TestApplyGMStarSoulAdjustGrantsOnlineType(t *testing.T) {
	loadOnlineTablesForTest(t)
	bag := newStarSoulBag()
	typeID := "1001"
	pos := "0"
	quality := "6"
	level := "4"
	applied, err := applyGMStarSoulAdjust(bag, gmStarSoulPayload{TypeID: &typeID, PosType: &pos, Quality: &quality, Level: &level})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied.IDs) != 1 {
		t.Fatalf("granted=%v", applied.IDs)
	}
	item := bag.Items[applied.IDs[0]]
	if item == nil || item.TypeID != 1001 || item.PosType != 0 || item.Quality != 6 || item.Level != 4 || item.Main == 0 {
		t.Fatalf("granted item=%+v", item)
	}
}

func TestApplyGMStarSoulAdjustUnlocksThenEquips(t *testing.T) {
	bag := newStarSoulBag()
	item := &starSoulItem{ID: 91003, TypeID: 1001, PosType: 1, Quality: 6, Main: 1103, IsLocked: true}
	bag.Items[item.ID] = item
	id := "91003"
	unlock := false
	equip := true
	if _, err := applyGMStarSoulAdjust(bag, gmStarSoulPayload{StarSoulID: &id, Equip: &equip}); err == nil {
		t.Fatal("locked equip accepted")
	}
	applied, err := applyGMStarSoulAdjust(bag, gmStarSoulPayload{StarSoulID: &id, IsLocked: &unlock, Equip: &equip})
	if err != nil {
		t.Fatal(err)
	}
	if item.IsLocked || !item.IsUsed || bag.Used[1] != 91003 || !applied.Attrs {
		t.Fatalf("item=%+v used=%v applied=%+v", item, bag.Used, applied)
	}
}
