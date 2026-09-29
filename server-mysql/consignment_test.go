package main

import (
	"testing"
	"time"

	"mhqserver/protocol"
)

func TestConsignmentFirstPageShowsNewestListings(t *testing.T) {
	consignMu.Lock()
	previous := consignItems
	now := time.Now()
	consignItems = make([]*consignmentItem, 0, 15)
	for id := int64(1); id <= 15; id++ {
		consignItems = append(consignItems, &consignmentItem{
			ID: id, ItemId: 110305, ItemType: int32(protocol.ItemType_GoodsItem),
			Count: 1, Price: 100, putAt: now.Add(time.Duration(id) * time.Second),
		})
	}
	consignMu.Unlock()
	t.Cleanup(func() {
		consignMu.Lock()
		consignItems = previous
		consignMu.Unlock()
	})

	items := (&Server{}).consignmentSnapshot(protocol.JobType_UnKnown, protocol.ItemType_NoneItem)
	if len(items) != 15 {
		t.Fatalf("snapshot count = %d, want 15", len(items))
	}
	for index, item := range items {
		want := int64(15 - index)
		if item.ID != want {
			t.Fatalf("snapshot item %d ID = %d, want newest-first %d", index, item.ID, want)
		}
	}
	if items[consignPageSize-1].ID != 4 {
		t.Fatalf("page zero ended with ID %d, want 4", items[consignPageSize-1].ID)
	}
}

func TestConsignmentPurchaseUsesStarCoinsAndPaysSellerByMail(t *testing.T) {
	oldTables := tables
	tables = &datatables{goodsBase: map[int64]map[string]interface{}{
		110205: {"_id": int64(110205), "MaxAmount": int64(9999)},
		110305: {"_id": int64(110305), "MaxAmount": int64(999)},
	}}
	t.Cleanup(func() { tables = oldTables })

	consignMu.Lock()
	previous := consignItems
	listing := &consignmentItem{
		ID: 51, SellerID: 22, SellerName: "seller", ItemId: 110305,
		ItemType: int32(protocol.ItemType_GoodsItem), Count: 1, Price: 1000, putAt: time.Now(),
		PurchaseSource: purchaseSourceMarket, PurchaseCurrency: purchaseCurrencyYuanBao, PurchaseUnitPrice: 200,
	}
	consignItems = []*consignmentItem{listing}
	consignMu.Unlock()
	t.Cleanup(func() {
		consignMu.Lock()
		consignItems = previous
		consignMu.Unlock()
	})

	buyer := newSession()
	buyer.playerID = 11
	buyer.coin = 96000 // Copper must not be accepted as consignment currency.
	buyer.bag = make(map[int32]*bagItem)
	seller := newSession()
	seller.state = sessInGame
	seller.playerID = listing.SellerID
	seller.bag = make(map[int32]*bagItem)
	buyerConn := &recordingConn{}
	server := &Server{conns: map[int64]*channel{
		1: {id: 1, conn: buyerConn, session: buyer},
		2: {id: 2, session: seller},
	}}
	buyerChannel := server.conns[1]

	response := server.onBuyInConsignment(buyerChannel, &protocol.C2M_BuyInConsignment{
		RpcId: 1, ConsignmentId: listing.ID,
	}).(*protocol.M2C_BuyInConsignment)
	if response.Message != "星币不足" {
		t.Fatalf("purchase without star coins message = %q", response.Message)
	}
	if buyer.coin != 96000 || len(buyer.bag) != 0 {
		t.Fatalf("failed purchase mutated buyer: coin=%d bag=%+v", buyer.coin, buyer.bag)
	}
	consignMu.Lock()
	remaining := len(consignItems)
	consignMu.Unlock()
	if remaining != 1 {
		t.Fatalf("failed purchase removed listing: remaining=%d", remaining)
	}

	buyer.bag[0] = &bagItem{
		ItemId: consignmentCurrencyItemID, ItemType: int32(protocol.ItemType_GoodsItem), Count: 1000,
	}
	if response := server.onBuyInConsignment(buyerChannel, &protocol.C2M_BuyInConsignment{
		RpcId: 2, ConsignmentId: listing.ID,
	}); response != nil {
		t.Fatalf("successful purchase returned response %+v", response)
	}
	if got := bagItemCount64(buyer, consignmentCurrencyItemID); got != 0 {
		t.Fatalf("buyer star coins = %d, want 0", got)
	}
	if got := bagItemCount64(buyer, listing.ItemId); got != 1 {
		t.Fatalf("buyer purchased item count = %d, want 1", got)
	}
	var purchased *bagItem
	for _, item := range buyer.bag {
		if item != nil && item.ItemId == listing.ItemId {
			purchased = item
		}
	}
	if purchased == nil || purchased.PurchaseSource != purchaseSourceConsignment ||
		purchased.PurchaseCurrency != purchaseCurrencyStarCoin || purchased.PurchaseUnitPrice != listing.Price {
		t.Fatalf("buyer purchase origin = %+v", purchased)
	}
	if buyer.coin != 96000 {
		t.Fatalf("buyer copper = %d, want unchanged 96000", buyer.coin)
	}
	consignMu.Lock()
	remaining = len(consignItems)
	consignMu.Unlock()
	if remaining != 0 {
		t.Fatalf("successful purchase left %d listings", remaining)
	}
	if len(seller.mails) != 1 || len(seller.mails[0].Items) != 1 {
		t.Fatalf("seller proceeds mail = %+v", seller.mails)
	}
	attachment := seller.mails[0].Items[0]
	if attachment.ItemId != consignmentCurrencyItemID || attachment.Count != 1000 || !attachment.IsHasItem {
		t.Fatalf("seller proceeds attachment = %+v", attachment)
	}
}

func TestConsignmentRefundReturnsOriginalItemWhenBagFull(t *testing.T) {
	seller := newSession()
	seller.state = sessInGame
	seller.playerID = 99
	seller.bag = make(map[int32]*bagItem)
	for index := int32(0); index < bagSlotCount; index++ {
		seller.bag[index] = &bagItem{ItemId: 9000 + index, ItemType: int32(protocol.ItemType_GoodsItem), Count: 1}
	}
	server := &Server{conns: map[int64]*channel{
		1: {id: 1, session: seller, conn: &recordingConn{}},
	}}
	listing := &consignmentItem{
		ID: 101, SellerID: seller.playerID, ItemId: 110305,
		ItemType: int32(protocol.ItemType_GoodsItem), Count: 3, Price: 999999,
		putAt:          time.Now().Add(-consignTTL - time.Second),
		PurchaseSource: purchaseSourceMarket, PurchaseCurrency: purchaseCurrencyVoucher, PurchaseUnitPrice: 321,
	}
	server.consignmentRefund(listing)
	if seller.coin != 0 {
		t.Fatalf("expired item value was converted to copper: coin=%d", seller.coin)
	}
	if len(seller.mails) != 1 || len(seller.mails[0].Items) != 1 {
		t.Fatalf("expired item refund mail=%+v", seller.mails)
	}
	attachment := seller.mails[0].Items[0]
	if attachment.ItemId != listing.ItemId || attachment.Count != listing.Count || !attachment.IsHasItem {
		t.Fatalf("expired item refund attachment=%+v, want item %d x%d", attachment, listing.ItemId, listing.Count)
	}
	if attachment.PurchaseSource != listing.PurchaseSource || attachment.PurchaseCurrency != listing.PurchaseCurrency ||
		attachment.PurchaseUnitPrice != listing.PurchaseUnitPrice {
		t.Fatalf("expired item refund origin=%+v", attachment)
	}
}
