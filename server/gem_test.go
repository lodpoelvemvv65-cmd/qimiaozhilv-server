package main

import (
	"testing"

	"mhqserver/protocol"
)

func loadGemTestTables(t *testing.T) {
	t.Helper()
	oldTables := tables
	t.Cleanup(func() { tables = oldTables })
	if err := loadDatatables("../datatable_json"); err != nil {
		t.Fatalf("load datatables: %v", err)
	}
}

func TestMeltEquipConsumesSelectedGemSlotAndRespondsFirst(t *testing.T) {
	loadGemTestTables(t)
	const (
		equipID int32 = 120660 // Type=2, MaxHole=4
		gemID   int32 = 20050  // GemKey=3, allowed on Type=2
	)
	ss := newSession()
	ss.playerID, ss.coin = 7, 1000
	ss.bag = map[int32]*bagItem{
		0: newBagItem(equipID),
		1: {ItemId: gemID, ItemType: 3, Count: 1},
		2: {ItemId: gemID, ItemType: 3, Count: 5},
	}
	conn := &recordingConn{}
	ch := &channel{id: 1, conn: conn, session: ss}
	response := (&Server{}).onMeltEquip(ch, &protocol.C2M_MeltEquip{
		RpcId: 11, EquipIndex: 0, GemIndex: 2, AttributeIndex: 0,
	})
	if response != nil {
		t.Fatalf("melt returned fallback response: %+v", response)
	}
	if ss.bag[1].Count != 1 || ss.bag[2].Count != 4 {
		t.Fatalf("gem stacks after melt: slot1=%d slot2=%d", ss.bag[1].Count, ss.bag[2].Count)
	}
	if got := ss.bag[0].GemList; len(got) != 4 || got[0] != gemID {
		t.Fatalf("equipment gem list = %v", got)
	}
	if ss.coin != 950 {
		t.Fatalf("coin after melt = %d, want 950", ss.coin)
	}
	opcodes := recordedOpcodes(t, conn.Bytes())
	if len(opcodes) == 0 || opcodes[0] != protocol.OpM2C_MeltEquip {
		t.Fatalf("first melt frame = %v, want %d", opcodes, protocol.OpM2C_MeltEquip)
	}
}

func TestMeltEquipRejectsGemNotAllowedByEquipmentPosition(t *testing.T) {
	loadGemTestTables(t)
	ss := newSession()
	ss.playerID, ss.coin = 7, 1000
	ss.bag = map[int32]*bagItem{
		0: newBagItem(120660), // Type=2 does not accept GemKey=1
		1: {ItemId: 20046, ItemType: 3, Count: 2},
	}
	response := (&Server{}).onMeltEquip(
		&channel{id: 1, conn: &recordingConn{}, session: ss},
		&protocol.C2M_MeltEquip{RpcId: 12, EquipIndex: 0, GemIndex: 1},
	).(*protocol.M2C_MeltEquip)
	if response.Message == "" {
		t.Fatal("position-incompatible gem was accepted")
	}
	if ss.bag[1].Count != 2 || len(ss.bag[0].GemList) != 0 || ss.coin != 1000 {
		t.Fatalf("rejected melt changed state: gem=%d list=%v coin=%d", ss.bag[1].Count, ss.bag[0].GemList, ss.coin)
	}
}

func TestDismountGemCommitsReturnedGemAndClearedSlotTogether(t *testing.T) {
	loadGemTestTables(t)
	const gemID int32 = 20050
	ss := newSession()
	ss.playerID, ss.coin = 7, 1000
	equip := newBagItem(120660)
	equip.GemList = []int32{gemID, 0, 0, 0}
	ss.bag = map[int32]*bagItem{
		0: equip,
		1: {ItemId: gemID, ItemType: 3, Count: 4},
	}
	ch := &channel{id: 1, conn: &recordingConn{}, session: ss}
	response := (&Server{}).onDismountGem(ch, &protocol.C2M_DismountGem{
		RpcId: 13, BagIndex: 0, GemIndex: 0,
	})
	if response != nil {
		t.Fatalf("dismount returned fallback response: %+v", response)
	}
	if ss.bag[0].GemList[0] != 0 || ss.bag[1].Count != 5 || ss.coin != 800 {
		t.Fatalf("dismount state gemList=%v stack=%d coin=%d", ss.bag[0].GemList, ss.bag[1].Count, ss.coin)
	}
	second := (&Server{}).onDismountGem(ch, &protocol.C2M_DismountGem{
		RpcId: 14, BagIndex: 0, GemIndex: 0,
	}).(*protocol.M2C_DismountGem)
	if second.Message == "" || ss.bag[1].Count != 5 || ss.coin != 800 {
		t.Fatalf("second dismount response=%+v stack=%d coin=%d", second, ss.bag[1].Count, ss.coin)
	}
}
