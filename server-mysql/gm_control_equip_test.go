package main

import "testing"

func TestApplyGMEquipAdjustStrengthStarLock(t *testing.T) {
	ss := &session{bag: map[int32]*bagItem{}, worn: map[int32]*bagItem{}}
	item := &bagItem{ItemId: 120590, ItemType: 1, ServerId: 88, Level: 1, Star: 1, GemList: []int32{1101, 0}}
	ss.worn[2] = item
	id := "88"
	strength := "12"
	star := "4"
	locked := true
	before, after, err := applyGMEquipAdjust(ss, gmEquipPayload{ServerID: &id, Strength: &strength, Star: &star, IsLocked: &locked})
	if err != nil {
		t.Fatal(err)
	}
	if item.Level != 12 || item.Star != 4 || !item.IsLock || before["strength"] != int32(1) || after["star"] != int32(4) {
		t.Fatalf("item=%+v before=%v after=%v", item, before, after)
	}
}

func TestApplyGMEquipAdjustClearsGemsIntoBag(t *testing.T) {
	ss := &session{bag: map[int32]*bagItem{}, worn: map[int32]*bagItem{}}
	item := &bagItem{ItemId: 120590, ItemType: 1, ServerId: 91, Level: 3, GemList: []int32{20101, 20102}}
	ss.worn[1] = item
	id := "91"
	clear := true
	if _, _, err := applyGMEquipAdjust(ss, gmEquipPayload{ServerID: &id, ClearGems: &clear}); err != nil {
		t.Fatal(err)
	}
	if item.GemList[0] != 0 || item.GemList[1] != 0 {
		t.Fatalf("gems=%v", item.GemList)
	}
	found := map[int32]int32{}
	for _, bagItem := range ss.bag {
		if bagItem != nil {
			found[bagItem.ItemId] += bagItem.Count
		}
	}
	if found[20101] != 1 || found[20102] != 1 {
		t.Fatalf("bag gems=%v bag=%v", found, ss.bag)
	}
}

func TestApplyGMEquipAdjustRejectsMissingAndNonEquip(t *testing.T) {
	ss := &session{bag: map[int32]*bagItem{0: {ItemId: 110205, ItemType: 2, ServerId: 7, Count: 1}}}
	id := "7"
	level := "2"
	if _, _, err := applyGMEquipAdjust(ss, gmEquipPayload{ServerID: &id, Strength: &level}); err == nil {
		t.Fatal("non-equip accepted")
	}
	missing := "9"
	if _, _, err := applyGMEquipAdjust(ss, gmEquipPayload{ServerID: &missing, Strength: &level}); err == nil {
		t.Fatal("missing accepted")
	}
}
