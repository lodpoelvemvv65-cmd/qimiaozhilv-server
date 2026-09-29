package main

import (
	"encoding/json"
	"testing"

	"mhqserver/protocol"
)

func useBagCapacityTables(t *testing.T) {
	t.Helper()
	previous := tables
	tables = &datatables{
		goodsBase: map[int64]map[string]interface{}{
			110305: {
				"_id":       json.Number("110305"),
				"MaxAmount": json.Number("99"),
			},
		},
		equipBase: map[int64]map[string]interface{}{
			120001: {"_id": json.Number("120001")},
		},
	}
	t.Cleanup(func() { tables = previous })
}

func fillBagExceptFirst(ss *session) {
	for index := int32(1); index < bagSlotCount; index++ {
		ss.bag[index] = &bagItem{ItemId: 120001, ItemType: int32(protocol.ItemType_EquipItem), Count: 1}
	}
}

func TestAddItemToBagRollsBackPartialStackWhenFull(t *testing.T) {
	useBagCapacityTables(t)
	original := &bagItem{ItemId: 110305, ItemType: int32(protocol.ItemType_GoodsItem), Count: 98}
	ss := &session{playerID: 7, bag: map[int32]*bagItem{0: original}}
	fillBagExceptFirst(ss)

	if index := ss.addItemToBag(110305, 2); index != invalidBagSlot {
		t.Fatalf("full bag returned slot %d, want %d", index, invalidBagSlot)
	}
	if ss.bag[0] != original || original.Count != 98 || len(ss.bag) != int(bagSlotCount) {
		t.Fatalf("failed grant mutated bag: first=%+v slots=%d", ss.bag[0], len(ss.bag))
	}
}

func TestAddItemToBagSplitsAtOnlineStackLimit(t *testing.T) {
	useBagCapacityTables(t)
	ss := &session{bag: make(map[int32]*bagItem)}
	if index := ss.addItemToBag(110305, 200); index != 0 {
		t.Fatalf("first stack index = %d, want 0", index)
	}
	if len(ss.bag) != 3 || ss.bag[0].Count != 99 || ss.bag[1].Count != 99 || ss.bag[2].Count != 2 {
		t.Fatalf("split stacks = %#v, want 99/99/2", ss.bag)
	}
}

func TestReceiveMailKeepsEverythingUnclaimedWhenBagIsFull(t *testing.T) {
	useBagCapacityTables(t)
	ss := &session{playerID: 7, coin: 10, bag: make(map[int32]*bagItem)}
	for index := int32(0); index < bagSlotCount; index++ {
		ss.bag[index] = &bagItem{ItemId: 120001, ItemType: int32(protocol.ItemType_EquipItem), Count: 1}
	}
	mail := &mailMsg{Id: 11, Items: []mailItemMsg{
		{ItemId: 110305, Count: 1, IsHasItem: true},
		{ItemId: 110203, Count: 500, IsHasItem: true},
	}}
	ss.mails = []*mailMsg{mail}
	ch := &channel{id: 1, session: ss}

	message := (&Server{}).onReceiveMail(ch, &protocol.C2M_ReceiveMail{RpcId: 9, MailId: 11})
	resp, ok := message.(*protocol.M2C_ReceiveMail)
	if !ok || resp.Message != "背包已满" {
		t.Fatalf("receive response = %#v, want 背包已满", message)
	}
	if mail.State != 0 || ss.coin != 10 || len(ss.bag) != int(bagSlotCount) {
		t.Fatalf("failed receive changed state=%d coin=%d slots=%d", mail.State, ss.coin, len(ss.bag))
	}
}
