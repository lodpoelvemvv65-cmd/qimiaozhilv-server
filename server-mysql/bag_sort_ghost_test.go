package main

import (
	"reflect"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

const (
	testGoodsID    int32 = 110305
	testWeaponA    int32 = 120590
	testWeaponB    int32 = 120591
	testCraftedID  int32 = 120592
	clientBagSlots int32 = 48
)

type clientBagDic map[int32]int32

type bagSnap struct {
	index  int32
	itemID int32
}

func testBagChannel(t *testing.T, bag map[int32]*bagItem) (*Server, *channel, *recordingConn) {
	t.Helper()
	ss := newSession()
	ss.playerID, ss.jobID, ss.level = 9, 1, 5000
	ss.bag = bag
	ss.worn = make(map[int32]*bagItem)
	conn := &recordingConn{}
	return &Server{}, &channel{id: 9, conn: conn, session: ss}, conn
}

func visibleBagIDs(ss *session) map[int32]int32 {
	out := make(map[int32]int32)
	for index, item := range ss.bag {
		if item == nil || isStarCoinItem(item.ItemId) {
			continue
		}
		out[index] = item.ItemId
	}
	return out
}

func storeContains(ss *session, itemID int32) bool {
	for _, item := range ss.store {
		if item != nil && item.ItemId == itemID {
			return true
		}
	}
	return false
}

func lastRecordedBody(t *testing.T, conn *recordingConn, opcode uint16) []byte {
	t.Helper()
	frames := decodeRecordedFrames(t, conn.Bytes())
	for i := len(frames) - 1; i >= 0; i-- {
		if frames[i].opcode == opcode {
			return frames[i].body
		}
	}
	t.Fatalf("missing opcode %d in %v", opcode, recordedOpcodes(t, conn.Bytes()))
	return nil
}

func decodeBagSnaps(t *testing.T, raw []byte, field protowire.Number) []bagSnap {
	t.Helper()
	var snaps []bagSnap
	for _, payload := range bytesFields(t, raw, field) {
		var entry protocol.BagMap
		if err := proto.Unmarshal(payload, &entry); err != nil {
			t.Fatalf("decode BagMap: %v", err)
		}
		itemID := int32(0)
		if entry.NetItem != nil {
			itemID = entry.NetItem.ItemId
		}
		snaps = append(snaps, bagSnap{index: entry.Index, itemID: itemID})
	}
	return snaps
}

func snapIDs(snaps []bagSnap) map[int32]int32 {
	out := make(map[int32]int32)
	for _, snap := range snaps {
		if snap.itemID != 0 {
			out[snap.index] = snap.itemID
		}
	}
	return out
}

func applyUpdateBagUI(itemDic clientBagDic, bagUIOpen bool, numItems int32, snapshot []bagSnap) {
	if !bagUIOpen {
		return
	}
	needClear := make(map[int32]struct{}, numItems)
	for index := int32(0); index < numItems; index++ {
		needClear[index] = struct{}{}
	}
	for _, snap := range snapshot {
		delete(needClear, snap.index)
		itemDic[snap.index] = snap.itemID
	}
	for index := range needClear {
		itemDic[index] = 0
	}
}

func visibleClientIDs(itemDic clientBagDic) map[int32]int32 {
	out := make(map[int32]int32)
	for index, itemID := range itemDic {
		if itemID != 0 {
			out[index] = itemID
		}
	}
	return out
}

func TestStoreThenSortBagDoesNotKeepStoredItemsOnServer(t *testing.T) {
	server, ch, conn := testBagChannel(t, map[int32]*bagItem{
		0: {ItemId: testGoodsID, ItemType: int32(protocol.ItemType_GoodsItem), Count: 8},
		2: {ItemId: testWeaponA, ItemType: int32(protocol.ItemType_EquipItem), Count: 1},
		5: {ItemId: testWeaponB, ItemType: int32(protocol.ItemType_EquipItem), Count: 1},
	})

	if got := server.onPutInStore(ch, &protocol.C2M_PutInStore{RpcId: 1, BagIndex: 0, Count: 8, Page: 0}); got != nil {
		t.Fatalf("put in store returned %+v", got)
	}
	if got := server.onPutInStore(ch, &protocol.C2M_PutInStore{RpcId: 2, BagIndex: 2, Count: 1, Page: 0}); got != nil {
		t.Fatalf("put weapon A in store returned %+v", got)
	}
	if visibleBagIDs(ch.session)[5] != testWeaponB || !storeContains(ch.session, testGoodsID) {
		t.Fatalf("after store bag=%v store=%v", visibleBagIDs(ch.session), ch.session.store)
	}

	if got := server.onSortBag(ch, &protocol.C2M_SortBag{RpcId: 3}); got != nil {
		t.Fatalf("sort bag returned %+v", got)
	}
	gotBag := visibleBagIDs(ch.session)
	wantBag := map[int32]int32{0: testWeaponB}
	if !reflect.DeepEqual(gotBag, wantBag) {
		t.Fatalf("server bag after store+sort = %v, want %v", gotBag, wantBag)
	}
	if !storeContains(ch.session, testGoodsID) || !storeContains(ch.session, testWeaponA) {
		t.Fatalf("stored items missing from warehouse: %#v", ch.session.store)
	}
	if storeContains(ch.session, testWeaponB) {
		t.Fatalf("remaining bag item was copied into warehouse: %#v", ch.session.store)
	}

	snapshot := snapIDs(decodeBagSnaps(t, lastRecordedBody(t, conn, protocol.OpM2C_SortBag), 1))
	if !reflect.DeepEqual(snapshot, wantBag) {
		t.Fatalf("sort BagMapList = %v, want %v", snapshot, wantBag)
	}
}

func TestPutOnThenSortBagDoesNotKeepWornItemOnServer(t *testing.T) {
	withEquipTable(t, testWeaponA, 7)
	server, ch, conn := testBagChannel(t, map[int32]*bagItem{
		0: {ItemId: testGoodsID, ItemType: int32(protocol.ItemType_GoodsItem), Count: 2},
		3: {ItemId: testWeaponA, ItemType: int32(protocol.ItemType_EquipItem), Count: 1},
	})

	if got := server.onPutOn(ch, &protocol.C2M_PutOn{RpcId: 4, Index: 3}); got != nil {
		t.Fatalf("put on returned %+v", got)
	}
	if ch.session.bag[3] != nil || ch.session.worn[0] == nil || ch.session.worn[0].ItemId != testWeaponA {
		t.Fatalf("after put on bag=%v worn=%v", visibleBagIDs(ch.session), ch.session.worn)
	}

	if got := server.onSortBag(ch, &protocol.C2M_SortBag{RpcId: 5}); got != nil {
		t.Fatalf("sort bag returned %+v", got)
	}
	gotBag := visibleBagIDs(ch.session)
	wantBag := map[int32]int32{0: testGoodsID}
	if !reflect.DeepEqual(gotBag, wantBag) {
		t.Fatalf("server bag after put-on+sort = %v, want %v", gotBag, wantBag)
	}
	if ch.session.worn[0] == nil || ch.session.worn[0].ItemId != testWeaponA {
		t.Fatalf("worn item missing after sort: %#v", ch.session.worn)
	}
	snapshot := snapIDs(decodeBagSnaps(t, lastRecordedBody(t, conn, protocol.OpM2C_SortBag), 1))
	if !reflect.DeepEqual(snapshot, wantBag) {
		t.Fatalf("sort BagMapList still contains worn item: %v", snapshot)
	}
}

func TestStoreSortCraftAndPutOnMatchUserFlowOnServer(t *testing.T) {
	withEquipTable(t, testCraftedID, 7)
	server, ch, conn := testBagChannel(t, map[int32]*bagItem{
		0: {ItemId: testGoodsID, ItemType: int32(protocol.ItemType_GoodsItem), Count: 3},
		4: {ItemId: testWeaponA, ItemType: int32(protocol.ItemType_EquipItem), Count: 1},
	})

	if got := server.onPutInStore(ch, &protocol.C2M_PutInStore{RpcId: 1, BagIndex: 0, Count: 3, Page: 0}); got != nil {
		t.Fatalf("store goods returned %+v", got)
	}
	if got := server.onPutInStore(ch, &protocol.C2M_PutInStore{RpcId: 2, BagIndex: 4, Count: 1, Page: 0}); got != nil {
		t.Fatalf("store weapon returned %+v", got)
	}
	if got := server.onSortBag(ch, &protocol.C2M_SortBag{RpcId: 3}); got != nil {
		t.Fatalf("first sort returned %+v", got)
	}
	if len(visibleBagIDs(ch.session)) != 0 {
		t.Fatalf("bag after storing everything = %v, want empty", visibleBagIDs(ch.session))
	}

	crafted := &bagItem{ItemId: testCraftedID, ItemType: int32(protocol.ItemType_EquipItem), Count: 1}
	slot := nextBagIndex(ch.session)
	if slot != 0 {
		t.Fatalf("crafted item slot = %d, want 0", slot)
	}
	ch.session.bag[slot] = crafted
	if visibleBagIDs(ch.session)[0] != testCraftedID {
		t.Fatalf("crafted/bought item did not appear in bag: %v", visibleBagIDs(ch.session))
	}

	if got := server.onPutOn(ch, &protocol.C2M_PutOn{RpcId: 4, Index: 0}); got != nil {
		t.Fatalf("put on crafted item returned %+v", got)
	}
	if got := server.onSortBag(ch, &protocol.C2M_SortBag{RpcId: 5}); got != nil {
		t.Fatalf("second sort returned %+v", got)
	}
	if len(visibleBagIDs(ch.session)) != 0 {
		t.Fatalf("equipped crafted item still in bag after sort: %v", visibleBagIDs(ch.session))
	}
	if ch.session.worn[0] == nil || ch.session.worn[0].ItemId != testCraftedID {
		t.Fatalf("crafted item missing from worn after sort: %#v", ch.session.worn)
	}
	snapshot := snapIDs(decodeBagSnaps(t, lastRecordedBody(t, conn, protocol.OpM2C_SortBag), 1))
	if len(snapshot) != 0 {
		t.Fatalf("second sort BagMapList = %v, want empty", snapshot)
	}
}

func TestStaleBagIndexAfterStoreCannotPutOnRemainingItem(t *testing.T) {
	withEquipTable(t, testWeaponA, 7)
	server, ch, _ := testBagChannel(t, map[int32]*bagItem{
		0: {ItemId: testGoodsID, ItemType: int32(protocol.ItemType_GoodsItem), Count: 1},
		5: {ItemId: testWeaponA, ItemType: int32(protocol.ItemType_EquipItem), Count: 1},
	})
	if got := server.onPutInStore(ch, &protocol.C2M_PutInStore{RpcId: 1, BagIndex: 0, Count: 1, Page: 0}); got != nil {
		t.Fatalf("store goods returned %+v", got)
	}
	if got := server.onSortBag(ch, &protocol.C2M_SortBag{RpcId: 2}); got != nil {
		t.Fatalf("sort returned %+v", got)
	}
	if visibleBagIDs(ch.session)[0] != testWeaponA {
		t.Fatalf("compacted bag = %v, want weapon at 0", visibleBagIDs(ch.session))
	}

	if got := server.onPutOn(ch, &protocol.C2M_PutOn{RpcId: 3, Index: 5}); got != nil {
		t.Fatalf("stale-index put on returned %+v", got)
	}
	if ch.session.worn[0] != nil {
		t.Fatalf("stale index unexpectedly equipped: %#v", ch.session.worn)
	}
	if visibleBagIDs(ch.session)[0] != testWeaponA {
		t.Fatalf("weapon left bag after failed stale put-on: %v", visibleBagIDs(ch.session))
	}

	if got := server.onPutOn(ch, &protocol.C2M_PutOn{RpcId: 4, Index: 0}); got != nil {
		t.Fatalf("compacted-index put on returned %+v", got)
	}
	if ch.session.worn[0] == nil || ch.session.worn[0].ItemId != testWeaponA || len(visibleBagIDs(ch.session)) != 0 {
		t.Fatalf("compacted put-on state bag=%v worn=%v", visibleBagIDs(ch.session), ch.session.worn)
	}
}

func TestUpdateBagUIClearsStoredItemsWhenBagUIOpen(t *testing.T) {
	itemDic := clientBagDic{0: testGoodsID, 2: testWeaponA, 5: testWeaponB}
	server, ch, conn := testBagChannel(t, map[int32]*bagItem{
		0: {ItemId: testGoodsID, ItemType: int32(protocol.ItemType_GoodsItem), Count: 1},
		2: {ItemId: testWeaponA, ItemType: int32(protocol.ItemType_EquipItem), Count: 1},
		5: {ItemId: testWeaponB, ItemType: int32(protocol.ItemType_EquipItem), Count: 1},
	})
	if got := server.onPutInStore(ch, &protocol.C2M_PutInStore{RpcId: 1, BagIndex: 0, Count: 1, Page: 0}); got != nil {
		t.Fatalf("store returned %+v", got)
	}
	applyUpdateBagUI(itemDic, true, clientBagSlots, decodeBagSnaps(t, lastRecordedBody(t, conn, protocol.OpM2C_PutInStore), 2))
	if got := server.onSortBag(ch, &protocol.C2M_SortBag{RpcId: 2}); got != nil {
		t.Fatalf("sort returned %+v", got)
	}
	applyUpdateBagUI(itemDic, true, clientBagSlots, decodeBagSnaps(t, lastRecordedBody(t, conn, protocol.OpM2C_SortBag), 1))

	got := visibleClientIDs(itemDic)
	want := map[int32]int32{0: testWeaponA, 1: testWeaponB}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("client bag after open-UI store+sort = %v, want %v", got, want)
	}
}

func TestUpdateBagUIKeepsGhostsWhenBagUIClosedThenSortWithZeroNumItems(t *testing.T) {
	itemDic := clientBagDic{0: testGoodsID, 2: testWeaponA, 5: testWeaponB}
	server, ch, conn := testBagChannel(t, map[int32]*bagItem{
		0: {ItemId: testGoodsID, ItemType: int32(protocol.ItemType_GoodsItem), Count: 1},
		2: {ItemId: testWeaponA, ItemType: int32(protocol.ItemType_EquipItem), Count: 1},
		5: {ItemId: testWeaponB, ItemType: int32(protocol.ItemType_EquipItem), Count: 1},
	})
	if got := server.onPutInStore(ch, &protocol.C2M_PutInStore{RpcId: 1, BagIndex: 0, Count: 1, Page: 0}); got != nil {
		t.Fatalf("store returned %+v", got)
	}
	applyUpdateBagUI(itemDic, false, clientBagSlots, decodeBagSnaps(t, lastRecordedBody(t, conn, protocol.OpM2C_PutInStore), 2))
	if !reflect.DeepEqual(visibleClientIDs(itemDic), map[int32]int32{0: testGoodsID, 2: testWeaponA, 5: testWeaponB}) {
		t.Fatalf("closed BagUI still changed ItemDic: %v", visibleClientIDs(itemDic))
	}

	if got := server.onSortBag(ch, &protocol.C2M_SortBag{RpcId: 2}); got != nil {
		t.Fatalf("sort returned %+v", got)
	}
	applyUpdateBagUI(itemDic, true, 0, decodeBagSnaps(t, lastRecordedBody(t, conn, protocol.OpM2C_SortBag), 1))
	got := visibleClientIDs(itemDic)
	if got[0] != testWeaponA || got[1] != testWeaponB {
		t.Fatalf("sorted snapshot was not applied: %v", got)
	}
	if got[2] != testWeaponA || got[5] != testWeaponB {
		t.Fatalf("expected old slots to remain as ghosts, got %v", got)
	}
}

func TestUpdateBagUIClearsGhostsIfSortRunsWithFullSlotList(t *testing.T) {
	itemDic := clientBagDic{0: testGoodsID, 2: testWeaponA, 5: testWeaponB}
	server, ch, conn := testBagChannel(t, map[int32]*bagItem{
		0: {ItemId: testGoodsID, ItemType: int32(protocol.ItemType_GoodsItem), Count: 1},
		2: {ItemId: testWeaponA, ItemType: int32(protocol.ItemType_EquipItem), Count: 1},
		5: {ItemId: testWeaponB, ItemType: int32(protocol.ItemType_EquipItem), Count: 1},
	})
	if got := server.onPutInStore(ch, &protocol.C2M_PutInStore{RpcId: 1, BagIndex: 0, Count: 1, Page: 0}); got != nil {
		t.Fatalf("store returned %+v", got)
	}
	applyUpdateBagUI(itemDic, false, clientBagSlots, decodeBagSnaps(t, lastRecordedBody(t, conn, protocol.OpM2C_PutInStore), 2))
	if got := server.onSortBag(ch, &protocol.C2M_SortBag{RpcId: 2}); got != nil {
		t.Fatalf("sort returned %+v", got)
	}
	applyUpdateBagUI(itemDic, true, clientBagSlots, decodeBagSnaps(t, lastRecordedBody(t, conn, protocol.OpM2C_SortBag), 1))
	got := visibleClientIDs(itemDic)
	want := map[int32]int32{0: testWeaponA, 1: testWeaponB}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("full-slot sort did not clear store ghosts: %v, want %v", got, want)
	}
}

func TestSortBagDoesNotMoveStarCoinsIntoVisibleSlots(t *testing.T) {
	server, ch, conn := testBagChannel(t, map[int32]*bagItem{
		starCoinBagSlot: {ItemId: consignmentCurrencyItemID, ItemType: int32(protocol.ItemType_GoodsItem), Count: 50},
		3:               {ItemId: testGoodsID, ItemType: int32(protocol.ItemType_GoodsItem), Count: 1},
	})
	if got := server.onSortBag(ch, &protocol.C2M_SortBag{RpcId: 1}); got != nil {
		t.Fatalf("sort returned %+v", got)
	}
	if ch.session.bag[starCoinBagSlot] == nil || ch.session.bag[starCoinBagSlot].Count != 50 {
		t.Fatalf("star coins left hidden slot after sort: %#v", ch.session.bag)
	}
	gotBag := visibleBagIDs(ch.session)
	wantBag := map[int32]int32{0: testGoodsID}
	if !reflect.DeepEqual(gotBag, wantBag) {
		t.Fatalf("visible bag after sort = %v, want %v", gotBag, wantBag)
	}
	snapshot := snapIDs(decodeBagSnaps(t, lastRecordedBody(t, conn, protocol.OpM2C_SortBag), 1))
	if !reflect.DeepEqual(snapshot, wantBag) {
		t.Fatalf("sort snapshot = %v, want %v", snapshot, wantBag)
	}
	if nextBagIndex(ch.session) != 1 {
		t.Fatalf("next bag index = %d, want 1 after one visible item", nextBagIndex(ch.session))
	}
}

func TestStoreSortCraftAndPutOnKeepsHiddenStarCoins(t *testing.T) {
	withEquipTable(t, testCraftedID, 7)
	server, ch, conn := testBagChannel(t, map[int32]*bagItem{
		starCoinBagSlot: {ItemId: consignmentCurrencyItemID, ItemType: int32(protocol.ItemType_GoodsItem), Count: 50},
		0:               {ItemId: testGoodsID, ItemType: int32(protocol.ItemType_GoodsItem), Count: 3},
		4:               {ItemId: testWeaponA, ItemType: int32(protocol.ItemType_EquipItem), Count: 1},
	})

	if got := server.onPutInStore(ch, &protocol.C2M_PutInStore{RpcId: 1, BagIndex: 0, Count: 3, Page: 0}); got != nil {
		t.Fatalf("store goods returned %+v", got)
	}
	if got := server.onPutInStore(ch, &protocol.C2M_PutInStore{RpcId: 2, BagIndex: 4, Count: 1, Page: 0}); got != nil {
		t.Fatalf("store weapon returned %+v", got)
	}
	if got := server.onSortBag(ch, &protocol.C2M_SortBag{RpcId: 3}); got != nil {
		t.Fatalf("first sort returned %+v", got)
	}
	if len(visibleBagIDs(ch.session)) != 0 {
		t.Fatalf("bag after storing everything = %v, want empty", visibleBagIDs(ch.session))
	}
	if ch.session.bag[starCoinBagSlot] == nil || ch.session.bag[starCoinBagSlot].Count != 50 {
		t.Fatalf("star coins left hidden slot after first sort: %#v", ch.session.bag)
	}
	if nextBagIndex(ch.session) != 0 {
		t.Fatalf("next bag index = %d, want 0 while only hidden star coins remain", nextBagIndex(ch.session))
	}

	crafted := &bagItem{ItemId: testCraftedID, ItemType: int32(protocol.ItemType_EquipItem), Count: 1}
	slot := nextBagIndex(ch.session)
	ch.session.bag[slot] = crafted
	if visibleBagIDs(ch.session)[0] != testCraftedID {
		t.Fatalf("crafted/bought item did not appear in bag: %v", visibleBagIDs(ch.session))
	}

	if got := server.onPutOn(ch, &protocol.C2M_PutOn{RpcId: 4, Index: 0}); got != nil {
		t.Fatalf("put on crafted item returned %+v", got)
	}
	if got := server.onSortBag(ch, &protocol.C2M_SortBag{RpcId: 5}); got != nil {
		t.Fatalf("second sort returned %+v", got)
	}
	if len(visibleBagIDs(ch.session)) != 0 {
		t.Fatalf("equipped crafted item still in bag after sort: %v", visibleBagIDs(ch.session))
	}
	if ch.session.worn[0] == nil || ch.session.worn[0].ItemId != testCraftedID {
		t.Fatalf("crafted item missing from worn after sort: %#v", ch.session.worn)
	}
	if ch.session.bag[starCoinBagSlot] == nil || ch.session.bag[starCoinBagSlot].Count != 50 {
		t.Fatalf("star coins missing after second sort: %#v", ch.session.bag)
	}
	snapshot := snapIDs(decodeBagSnaps(t, lastRecordedBody(t, conn, protocol.OpM2C_SortBag), 1))
	if len(snapshot) != 0 {
		t.Fatalf("second sort BagMapList = %v, want empty", snapshot)
	}
}

func TestSortBagMovesLegacyVisibleStarCoinsBackToHiddenSlot(t *testing.T) {
	server, ch, conn := testBagChannel(t, map[int32]*bagItem{
		0: {ItemId: consignmentCurrencyItemID, ItemType: int32(protocol.ItemType_GoodsItem), Count: 50},
		3: {ItemId: testGoodsID, ItemType: int32(protocol.ItemType_GoodsItem), Count: 1},
	})
	if got := server.onSortBag(ch, &protocol.C2M_SortBag{RpcId: 1}); got != nil {
		t.Fatalf("sort returned %+v", got)
	}
	if ch.session.bag[0] != nil && isStarCoinItem(ch.session.bag[0].ItemId) {
		t.Fatalf("legacy star coins stayed in visible slot 0: %#v", ch.session.bag)
	}
	if ch.session.bag[starCoinBagSlot] == nil || ch.session.bag[starCoinBagSlot].Count != 50 {
		t.Fatalf("legacy star coins were not moved to hidden slot: %#v", ch.session.bag)
	}
	gotBag := visibleBagIDs(ch.session)
	wantBag := map[int32]int32{0: testGoodsID}
	if !reflect.DeepEqual(gotBag, wantBag) {
		t.Fatalf("visible bag after sorting legacy star coins = %v, want %v", gotBag, wantBag)
	}
	snapshot := snapIDs(decodeBagSnaps(t, lastRecordedBody(t, conn, protocol.OpM2C_SortBag), 1))
	if !reflect.DeepEqual(snapshot, wantBag) {
		t.Fatalf("sort snapshot = %v, want %v", snapshot, wantBag)
	}
	if nextBagIndex(ch.session) != 1 {
		t.Fatalf("next bag index = %d, want 1", nextBagIndex(ch.session))
	}
}
