package main

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"mhqserver/protocol"
)

func TestBagPairsHideStarCoinCurrency(t *testing.T) {
	items := map[int32]*bagItem{
		0: {ItemId: consignmentCurrencyItemID, ItemType: int32(protocol.ItemType_GoodsItem), Count: 123},
		1: {ItemId: 9001, ItemType: int32(protocol.ItemType_GoodsItem), Count: 2},
	}
	pairs := bagPairs(items)
	if len(pairs) != 1 {
		t.Fatalf("bag snapshot contains %d entries, want only visible item", len(pairs))
	}
	if pairs[0][0].(int32) != 1 || pairs[0][1].(*bagItem).ItemId != 9001 {
		t.Fatalf("visible bag snapshot = %+v", pairs)
	}
}

func TestStarCoinGrantDoesNotConsumeVisibleBagSlot(t *testing.T) {
	oldTables := tables
	tables = &datatables{goodsBase: map[int64]map[string]interface{}{
		int64(consignmentCurrencyItemID): {"_id": int64(consignmentCurrencyItemID)},
	}}
	t.Cleanup(func() { tables = oldTables })

	ss := newSession()
	ss.playerID = 1
	ss.bag = make(map[int32]*bagItem)
	for index := int32(0); index < bagSlotCount; index++ {
		ss.bag[index] = &bagItem{ItemId: 9000 + index, ItemType: int32(protocol.ItemType_GoodsItem), Count: 1}
	}
	staged, _, ok := stageBagGrants(ss, []bagGrant{{itemID: consignmentCurrencyItemID, count: 50}})
	if !ok {
		t.Fatal("star-coin grant was rejected with a full visible bag")
	}
	if got := bagItemCount64(&session{bag: staged}, consignmentCurrencyItemID); got != 50 {
		t.Fatalf("staged star-coin balance = %d, want 50", got)
	}
	if got := len(bagPairs(staged)); got != int(bagSlotCount) {
		t.Fatalf("visible bag entries = %d, want %d", got, bagSlotCount)
	}
}

func TestSellItemSendsResponseBeforeCurrencySync(t *testing.T) {
	oldTables := tables
	tables = &datatables{goodsBase: map[int64]map[string]interface{}{
		9001: {"_id": int64(9001), "Price": int64(500), "MaxAmount": int64(999)},
	}}
	t.Cleanup(func() { tables = oldTables })

	ss := newSession()
	ss.playerID, ss.coin = 1, 0
	ss.bag = map[int32]*bagItem{0: {ItemId: 9001, ItemType: int32(protocol.ItemType_GoodsItem), Count: 1}}
	conn := &recordingConn{}
	ch := &channel{id: 1, conn: conn, session: ss}
	if got := (&Server{}).onSellItem(ch, &protocol.C2M_SellItem{RpcId: 7, SlotIndex: 0, Count: 1}); got != nil {
		t.Fatalf("sell returned %v, want raw response", got)
	}
	frames := decodeRecordedFrames(t, conn.Bytes())
	if len(frames) < 3 || frames[0].opcode != protocol.OpM2C_SellItem || frames[1].opcode != protocol.OpM2C_SendBag || frames[2].opcode != protocol.OpM2C_SyncUnitAttribute {
		t.Fatalf("sell frame order = %v, want 20179, 20260, then 20169", recordedOpcodes(t, conn.Bytes()))
	}
	var response protocol.M2C_SellItem
	if err := proto.Unmarshal(frames[0].body, &response); err != nil {
		t.Fatal(err)
	}
	if response.RpcId != 7 {
		t.Fatalf("sell response RpcId = %d, want 7", response.RpcId)
	}
	var coin protocol.M2C_SyncUnitAttribute
	if err := proto.Unmarshal(frames[2].body, &coin); err != nil {
		t.Fatal(err)
	}
	if coin.NumericType != ntCoin || coin.Value != 500 {
		t.Fatalf("coin sync = %+v, want NumericType=%d Value=500", &coin, ntCoin)
	}
}

func TestSellItemSequentialSalesSyncEachBalance(t *testing.T) {
	oldTables := tables
	tables = &datatables{goodsBase: map[int64]map[string]interface{}{
		9001: {"_id": int64(9001), "Price": int64(500), "MaxAmount": int64(999)},
	}}
	t.Cleanup(func() { tables = oldTables })

	ss := newSession()
	ss.playerID = 1
	ss.bag = map[int32]*bagItem{
		0: {ItemId: 9001, ItemType: int32(protocol.ItemType_GoodsItem), Count: 1},
		1: {ItemId: 9001, ItemType: int32(protocol.ItemType_GoodsItem), Count: 1},
	}
	conn := &recordingConn{}
	ch := &channel{id: 1, conn: conn, session: ss}
	for slot := int32(0); slot < 2; slot++ {
		if got := (&Server{}).onSellItem(ch, &protocol.C2M_SellItem{RpcId: slot + 1, SlotIndex: slot, Count: 1}); got != nil {
			t.Fatalf("sale %d returned %v", slot+1, got)
		}
	}
	frames := decodeRecordedFrames(t, conn.Bytes())
	var sellResponses, bagPushes, coinSyncs int
	var coinValues []float32
	for _, frame := range frames {
		switch frame.opcode {
		case protocol.OpM2C_SellItem:
			sellResponses++
		case protocol.OpM2C_SendBag:
			bagPushes++
		case protocol.OpM2C_SyncUnitAttribute:
			var msg protocol.M2C_SyncUnitAttribute
			if err := proto.Unmarshal(frame.body, &msg); err != nil {
				t.Fatal(err)
			}
			if msg.NumericType == ntCoin {
				coinSyncs++
				coinValues = append(coinValues, msg.Value)
			}
		}
	}
	if sellResponses != 2 || bagPushes != 2 || coinSyncs != 2 {
		t.Fatalf("sequential sale frames sell=%d bag=%d coin=%d opcodes=%v", sellResponses, bagPushes, coinSyncs, recordedOpcodes(t, conn.Bytes()))
	}
	if len(coinValues) != 2 || coinValues[0] != 500 || coinValues[1] != 1000 {
		t.Fatalf("sequential coin sync values=%v, want [500 1000]", coinValues)
	}
	if ss.coin != 1000 || len(ss.bag) != 0 {
		t.Fatalf("sequential sale state coin=%d bag=%+v", ss.coin, ss.bag)
	}
}
