package main

import (
	"testing"

	"google.golang.org/protobuf/proto"

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

func TestMeltEquipRejectsGemKeyNotAllowedForPositionWithNativeTip(t *testing.T) {
	loadGemTestTables(t)
	ss := newSession()
	ss.playerID, ss.coin = 7, 1000
	equip := newBagItem(120644) // Type=2; GemInlayConfig excludes GemKey=1.
	ss.bag = map[int32]*bagItem{
		0: equip,
		1: {ItemId: 20046, ItemType: 3, Count: 2},
	}
	conn := &recordingConn{}
	response := (&Server{}).onMeltEquip(
		&channel{id: 1, conn: conn, session: ss},
		&protocol.C2M_MeltEquip{RpcId: 12, EquipIndex: 0, GemIndex: 1},
	)
	if response != nil {
		t.Fatalf("position rejection returned dispatcher response: %+v", response)
	}
	if ss.bag[1].Count != 2 || len(equip.GemList) != 0 || ss.coin != 1000 {
		t.Fatalf("position rejection changed state: gem=%d list=%v coin=%d", ss.bag[1].Count, equip.GemList, ss.coin)
	}
	frames := decodeRecordedFrames(t, conn.Bytes())
	if got := recordedOpcodes(t, conn.Bytes()); len(got) != 2 || got[0] != protocol.OpM2C_MeltEquip || got[1] != protocol.OpM2C_SendTip {
		t.Fatalf("position rejection frames=%v", got)
	}
	var melt protocol.M2C_MeltEquip
	if err := proto.Unmarshal(frames[0].body, &melt); err != nil || melt.RpcId != 12 || melt.Message != "" {
		t.Fatalf("position rejection response=%+v err=%v", &melt, err)
	}
	var tip protocol.M2C_SendTip
	if err := proto.Unmarshal(frames[1].body, &tip); err != nil || tip.Message != "该部位不能镶嵌这种宝石！" {
		t.Fatalf("position rejection tip=%+v err=%v", &tip, err)
	}
}

func TestMeltEquipRejectsDuplicateGemKeyInAnotherSocketWithNativeTip(t *testing.T) {
	loadGemTestTables(t)
	ss := newSession()
	ss.playerID, ss.coin = 7, 1000
	equip := newBagItem(120660)
	equip.GemList = []int32{20050, 0, 0, 0}
	ss.bag = map[int32]*bagItem{
		0: equip,
		1: {ItemId: 20063, ItemType: 3, Count: 2}, // Same GemKey as 20050 (different level).
	}
	conn := &recordingConn{}
	response := (&Server{}).onMeltEquip(
		&channel{id: 1, conn: conn, session: ss},
		&protocol.C2M_MeltEquip{RpcId: 21, EquipIndex: 0, GemIndex: 1, AttributeIndex: 1},
	)
	if response != nil {
		t.Fatalf("duplicate rejection returned dispatcher response: %+v", response)
	}
	if got := equip.GemList; len(got) != 4 || got[0] != 20050 || got[1] != 0 {
		t.Fatalf("duplicate rejection changed sockets: %v", got)
	}
	if ss.bag[1].Count != 2 || ss.coin != 1000 {
		t.Fatalf("duplicate rejection consumed resources: count=%d coin=%d", ss.bag[1].Count, ss.coin)
	}
	frames := decodeRecordedFrames(t, conn.Bytes())
	if got := recordedOpcodes(t, conn.Bytes()); len(got) != 2 || got[0] != protocol.OpM2C_MeltEquip || got[1] != protocol.OpM2C_SendTip {
		t.Fatalf("duplicate rejection frames=%v", got)
	}
	var melt protocol.M2C_MeltEquip
	if err := proto.Unmarshal(frames[0].body, &melt); err != nil || melt.RpcId != 21 || melt.Message != "" || len(frames[0].body) < 20 {
		t.Fatalf("duplicate rejection response=%+v len=%d err=%v", &melt, len(frames[0].body), err)
	}
	var tip protocol.M2C_SendTip
	if err := proto.Unmarshal(frames[1].body, &tip); err != nil || tip.Message != "该装备已镶嵌相同属性宝石，请更换其他属性！" {
		t.Fatalf("duplicate rejection tip=%+v err=%v", &tip, err)
	}
}

func TestMeltEquipAllowsDifferentGemKeysInSameGemType(t *testing.T) {
	loadGemTestTables(t)
	ss := newSession()
	ss.playerID, ss.coin = 7, 1000
	equip := newBagItem(120660)             // Type=2 allows GemKey 20 and 21.
	equip.GemList = []int32{20264, 0, 0, 0} // GemKey=20, GemType=5.
	ss.bag = map[int32]*bagItem{
		0: equip,
		1: {ItemId: 20271, ItemType: 3, Count: 1}, // GemKey=21, also GemType=5.
	}
	inlay, _ := gemPrice(1)
	response := (&Server{}).onMeltEquip(
		&channel{id: 1, conn: &recordingConn{}, session: ss},
		&protocol.C2M_MeltEquip{RpcId: 23, EquipIndex: 0, GemIndex: 1, AttributeIndex: 1},
	)
	if response != nil {
		t.Fatalf("different GemKeys in the same GemType were rejected: %+v", response)
	}
	if got := equip.GemList; len(got) != 4 || got[0] != 20264 || got[1] != 20271 {
		t.Fatalf("different GemKeys did not occupy separate sockets: %v", got)
	}
	if ss.coin != 1000-inlay {
		t.Fatalf("different GemKeys coin=%d want=%d", ss.coin, 1000-inlay)
	}
	if _, exists := ss.bag[1]; exists {
		t.Fatal("different GemKeys did not consume the inserted gem")
	}
}

func TestMeltEquipCanOverwriteTargetSocketWithSameGemKey(t *testing.T) {
	loadGemTestTables(t)
	ss := newSession()
	ss.playerID, ss.coin = 7, 1000
	equip := newBagItem(120660)
	equip.GemList = []int32{20050, 0, 0, 0}
	ss.bag = map[int32]*bagItem{
		0: equip,
		1: {ItemId: 20063, ItemType: 3, Count: 1},
	}
	inlay, _ := gemPrice(2)
	response := (&Server{}).onMeltEquip(
		&channel{id: 1, conn: &recordingConn{}, session: ss},
		&protocol.C2M_MeltEquip{RpcId: 22, EquipIndex: 0, GemIndex: 1, AttributeIndex: 0},
	)
	if response != nil {
		t.Fatalf("same-socket overwrite was rejected: %+v", response)
	}
	if equip.GemList[0] != 20063 || ss.coin != 1000-inlay {
		t.Fatalf("same-socket overwrite: gems=%v coin=%d wantCoin=%d", equip.GemList, ss.coin, 1000-inlay)
	}
	if _, exists := ss.bag[1]; exists {
		t.Fatal("same-socket overwrite did not consume the replacement gem")
	}
}

func TestEquipAllowsGemRequiresFamilyAndPositionAttribute(t *testing.T) {
	oldTables := tables
	t.Cleanup(func() { tables = oldTables })
	tables = &datatables{
		equipBase: map[int64]map[string]interface{}{
			1: {"Type": int64(2), "CanInlayGemTypeArr": []interface{}{int64(2)}},
			2: {"Type": int64(9), "CanInlayGemTypeArr": []interface{}{int64(2)}},
		},
		gemInlay: map[int64]map[string]interface{}{
			2: {"CanInlayArr": []interface{}{int64(3), int64(4)}},
		},
	}
	if !equipAllowsGem(1, map[string]interface{}{"GemType": int64(2), "GemKey": int64(3)}) {
		t.Fatal("allowed gem family and position attribute were rejected")
	}
	if equipAllowsGem(1, map[string]interface{}{"GemType": int64(3), "GemKey": int64(3)}) {
		t.Fatal("undeclared gem family was accepted")
	}
	if equipAllowsGem(1, map[string]interface{}{"GemType": int64(2), "GemKey": int64(1)}) {
		t.Fatal("GemKey excluded by the equipment position was accepted")
	}
	if equipAllowsGem(2, map[string]interface{}{"GemType": int64(2), "GemKey": int64(3)}) {
		t.Fatal("equipment position without GemInlayConfig was accepted")
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
