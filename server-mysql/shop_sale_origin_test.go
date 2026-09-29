package main

import (
	"math"
	"testing"

	"mhqserver/internal/operationsconfig"
	"mhqserver/protocol"
)

func saleOriginTables() *datatables {
	return &datatables{goodsBase: map[int64]map[string]interface{}{
		9001: {"_id": int64(9001), "Price": int64(500), "MaxAmount": int64(999)},
	}}
}

func TestPremiumMarketPurchasesRecoverVoucherAtConfiguredPercent(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.ShopSale.MarketRecoveryPercent = 85
	})
	oldTables := tables
	tables = saleOriginTables()
	t.Cleanup(func() { tables = oldTables })

	for _, currency := range []int32{purchaseCurrencyYuanBao, purchaseCurrencyVoucher} {
		ss := newSession()
		ss.playerID = 1
		ss.saleDestination = saleDestinationMarket
		ss.bag = map[int32]*bagItem{0: {
			ItemId: 9001, ItemType: int32(protocol.ItemType_GoodsItem), Count: 3,
			PurchaseSource: purchaseSourceMarket, PurchaseCurrency: currency, PurchaseUnitPrice: 101,
		}}
		ch := &channel{id: 1, session: ss, conn: &recordingConn{}}
		if got := (&Server{}).onSellItem(ch, &protocol.C2M_SellItem{SlotIndex: 0, Count: 2}); got != nil {
			t.Fatalf("currency %d sale returned %+v", currency, got)
		}
		if ss.voucher != 171 || ss.coin != 0 || ss.bag[0].Count != 1 {
			t.Fatalf("currency %d balances voucher=%d coin=%d count=%d", currency, ss.voucher, ss.coin, ss.bag[0].Count)
		}
	}
}

func TestPremiumItemOnlyRecoversVoucherWhenSoldBackToMarket(t *testing.T) {
	oldTables := tables
	tables = saleOriginTables()
	t.Cleanup(func() { tables = oldTables })

	for _, test := range []struct {
		name        string
		destination int32
		source      int32
		currency    int32
	}{
		{"premium to shop", saleDestinationShop, purchaseSourceMarket, purchaseCurrencyYuanBao},
		{"coin purchase to market", saleDestinationMarket, purchaseSourceShop, purchaseCurrencyCoin},
		{"reward to market", saleDestinationMarket, purchaseSourceNone, purchaseCurrencyNone},
	} {
		t.Run(test.name, func(t *testing.T) {
			ss := newSession()
			ss.playerID = 1
			ss.saleDestination = test.destination
			ss.bag = map[int32]*bagItem{0: {
				ItemId: 9001, ItemType: int32(protocol.ItemType_GoodsItem), Count: 1,
				PurchaseSource: test.source, PurchaseCurrency: test.currency, PurchaseUnitPrice: 1000,
			}}
			ch := &channel{id: 1, session: ss, conn: &recordingConn{}}
			if got := (&Server{}).onSellItem(ch, &protocol.C2M_SellItem{SlotIndex: 0, Count: 1}); got != nil {
				t.Fatalf("sale returned %+v", got)
			}
			if ss.coin != 500 || ss.voucher != 0 {
				t.Fatalf("balances coin=%d voucher=%d", ss.coin, ss.voucher)
			}
		})
	}
}

func TestPurchaseOriginsKeepSameItemInSeparateStacks(t *testing.T) {
	oldTables := tables
	tables = saleOriginTables()
	t.Cleanup(func() { tables = oldTables })

	ss := newSession()
	ss.playerID = 1
	ss.bag = make(map[int32]*bagItem)
	grants := []bagGrant{
		{itemID: 9001, count: 2},
		{itemID: 9001, count: 3, purchaseSource: purchaseSourceMarket, purchaseCurrency: purchaseCurrencyYuanBao, purchaseUnitPrice: 100},
		{itemID: 9001, count: 4, purchaseSource: purchaseSourceMarket, purchaseCurrency: purchaseCurrencyYuanBao, purchaseUnitPrice: 120},
	}
	staged, _, ok := stageBagGrants(ss, grants)
	if !ok || len(staged) != 3 {
		t.Fatalf("staged=%+v ok=%v, want three source batches", staged, ok)
	}
	ss.bag = staged
	ch := &channel{id: 1, session: ss, conn: &recordingConn{}}
	if got := (&Server{}).onSortBag(ch, &protocol.C2M_SortBag{}); got != nil || len(ss.bag) != 3 {
		t.Fatalf("sort result=%+v bag=%+v", got, ss.bag)
	}
}

func TestMarketSnapshotPushDoesNotChangeSaleDestination(t *testing.T) {
	oldTables := tables
	tables = &datatables{marketBase: map[int64]map[string]interface{}{
		1: {"_id": int64(1), "Page": int64(0), "ItemId": int64(9001), "Price_YuanBao": int64(100)},
	}}
	t.Cleanup(func() { tables = oldTables })

	ss := newSession()
	ss.playerID = 1
	ss.saleDestination = saleDestinationShop
	ch := &channel{id: 1, session: ss, conn: &recordingConn{}}
	(&Server{}).pushMarketSnapshot(ch)
	if ss.saleDestination != saleDestinationShop {
		t.Fatalf("config push changed sale destination to %d", ss.saleDestination)
	}
	if got := (&Server{}).onGetMarket(ch, &protocol.C2M_GetMarket{}); got != nil || ss.saleDestination != saleDestinationMarket {
		t.Fatalf("explicit market open result=%+v destination=%d", got, ss.saleDestination)
	}
}

func TestStoreRoundTripPreservesPurchaseOrigin(t *testing.T) {
	oldTables := tables
	tables = saleOriginTables()
	t.Cleanup(func() { tables = oldTables })

	ss := newSession()
	ss.playerID = 1
	ss.storePages = 2
	ss.bag = map[int32]*bagItem{0: {
		ItemId: 9001, ItemType: int32(protocol.ItemType_GoodsItem), Count: 2,
		PurchaseSource: purchaseSourceMarket, PurchaseCurrency: purchaseCurrencyVoucher, PurchaseUnitPrice: 123,
	}}
	ch := &channel{id: 1, session: ss, conn: &recordingConn{}}
	server := &Server{}
	if got := server.onPutInStore(ch, &protocol.C2M_PutInStore{BagIndex: 0, Count: 2, Page: 0}); got != nil {
		t.Fatalf("put in store returned %+v", got)
	}
	if got := server.onTakeOffStore(ch, &protocol.C2M_TakeOffStore{StoreIndex: 0, Count: 2, Page: 0}); got != nil {
		t.Fatalf("take off store returned %+v", got)
	}
	item := ss.bag[0]
	if item == nil || item.PurchaseSource != purchaseSourceMarket || item.PurchaseCurrency != purchaseCurrencyVoucher || item.PurchaseUnitPrice != 123 {
		t.Fatalf("store round-trip origin=%+v", item)
	}
}

func TestSplitTradeAndMailKeepRecoveryEligibility(t *testing.T) {
	oldTables := tables
	tables = saleOriginTables()
	t.Cleanup(func() { tables = oldTables })
	original := &bagItem{ItemId: 9001, ItemType: 2, Count: 4,
		PurchaseSource: purchaseSourceMarket, PurchaseCurrency: purchaseCurrencyVoucher, PurchaseUnitPrice: 100}
	sender := newSession()
	sender.playerID, sender.bag = 1, map[int32]*bagItem{0: cloneBagItem(original)}
	channel := &channel{id: 1, session: sender, conn: &recordingConn{}}
	if response := (&Server{}).onSplitItem(channel, &protocol.C2M_SplitItem{Index: 0, Count: 2}); response != nil {
		t.Fatalf("split: %+v", response)
	}
	if !samePurchaseOrigin(sender.bag[0], original) || !samePurchaseOrigin(sender.bag[1], original) {
		t.Fatal("split lost purchase origin")
	}
	receiver := newSession()
	receiver.saleDestination = saleDestinationMarket
	receiver.bag = map[int32]*bagItem{0: {ItemId: 9001, ItemType: 2, Count: 1}}
	if err := addTransferredItem(receiver.bag, sender.bag[1]); err != nil {
		t.Fatal(err)
	}
	if len(receiver.bag) != 2 || !samePurchaseOrigin(receiver.bag[1], original) {
		t.Fatal("trade merged premium items with rewards")
	}
	delta, _, _, ok := saleCurrencyDelta(receiver, receiver.bag[1], 2)
	if !ok || delta.voucher != 170 || delta.coin != 0 {
		t.Fatalf("traded item recovery = %+v, valid=%v", delta, ok)
	}
	mail := newConsignmentRefundMail(&consignmentItem{ItemId: 9001, Count: 2,
		PurchaseSource: original.PurchaseSource, PurchaseCurrency: original.PurchaseCurrency,
		PurchaseUnitPrice: original.PurchaseUnitPrice})
	staged, _, ok := stageMailRewards(receiver, []*mailMsg{mail})
	if !ok || len(staged) != 2 || staged[1].Count != 4 || !samePurchaseOrigin(staged[1], original) {
		t.Fatalf("mail recovery lost origin: %+v", staged)
	}
	staged, ok = stageTrialItems(receiver, map[int32]int64{9001: 2})
	if !ok || staged[0].Count != 3 || staged[1].Count != 2 {
		t.Fatalf("dungeon rewards merged into purchased batch: %+v", staged)
	}
}

func TestVoucherSaleRejectsOverflowAndKeepsWireOrder(t *testing.T) {
	oldTables := tables
	tables = saleOriginTables()
	t.Cleanup(func() { tables = oldTables })
	ss := newSession()
	ss.playerID, ss.saleDestination, ss.voucher = 1, saleDestinationMarket, math.MaxInt64
	ss.bag = map[int32]*bagItem{0: {ItemId: 9001, ItemType: 2, Count: 1,
		PurchaseSource: purchaseSourceMarket, PurchaseCurrency: purchaseCurrencyYuanBao, PurchaseUnitPrice: 100}}
	conn := &recordingConn{}
	ch := &channel{id: 1, session: ss, conn: conn}
	server := &Server{}
	if response := server.onSellItem(ch, &protocol.C2M_SellItem{Count: 1}); response == nil || ss.bag[0] == nil || ss.voucher != math.MaxInt64 {
		t.Fatal("overflow sale changed inventory or balance")
	}
	ss.voucher = 0
	if response := server.onSellItem(ch, &protocol.C2M_SellItem{Count: 1}); response != nil {
		t.Fatalf("sale: %+v", response)
	}
	frames := decodeRecordedFrames(t, conn.Bytes())
	if len(frames) < 3 || frames[0].opcode != protocol.OpM2C_SellItem || frames[1].opcode != protocol.OpM2C_SendBag || frames[2].opcode != protocol.OpM2C_SyncUnitAttribute {
		t.Fatal("voucher sale response/bag/balance order changed")
	}
	if ss.voucher != 85 || ss.coin != 0 || len(ss.bag) != 0 {
		t.Fatal("voucher recovery did not directly credit currency balance")
	}
}
