package main

import "testing"

func TestItemPurchaseOriginMySQLRoundTrip(t *testing.T) {
	store, err := OpenStore(mysqlTestDSN(t, "item-purchase-origin-round-trip"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	accountID, err := store.CreateAccount("origin-account", "secret")
	if err != nil {
		t.Fatal(err)
	}
	playerID, err := store.CreatePlayer(accountID, "origin-role", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	relations := playerRelations{
		bag: map[int32]*bagItem{3: {
			ItemId: 9001, ItemType: 2, Count: 2,
			PurchaseSource: purchaseSourceMarket, PurchaseCurrency: purchaseCurrencyYuanBao, PurchaseUnitPrice: 123,
		}},
		store: map[int32]*bagItem{7: {
			ItemId: 9002, ItemType: 3, Count: 4,
			PurchaseSource: purchaseSourceShop, PurchaseCurrency: purchaseCurrencyCoin, PurchaseUnitPrice: 500,
		}},
		mails: []*mailMsg{{Id: 1, Title: "refund", Items: []mailItemMsg{{
			ItemId: 9003, Count: 1, IsHasItem: true,
			PurchaseSource: purchaseSourceMarket, PurchaseCurrency: purchaseCurrencyVoucher, PurchaseUnitPrice: 321,
		}}}},
	}
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := savePlayerRelationsTx(tx, playerID, relations); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.FirstPlayer(accountID)
	if err != nil {
		t.Fatal(err)
	}
	bagItem := loaded.Relations.bag[3]
	if bagItem == nil || bagItem.PurchaseSource != purchaseSourceMarket ||
		bagItem.PurchaseCurrency != purchaseCurrencyYuanBao || bagItem.PurchaseUnitPrice != 123 {
		t.Fatalf("loaded bag origin=%+v", bagItem)
	}
	storeItem := loaded.Relations.store[7]
	if storeItem == nil || storeItem.PurchaseSource != purchaseSourceShop ||
		storeItem.PurchaseCurrency != purchaseCurrencyCoin || storeItem.PurchaseUnitPrice != 500 {
		t.Fatalf("loaded store origin=%+v", storeItem)
	}
	if len(loaded.Relations.mails) != 1 || len(loaded.Relations.mails[0].Items) != 1 {
		t.Fatalf("loaded mails=%+v", loaded.Relations.mails)
	}
	mailItem := loaded.Relations.mails[0].Items[0]
	if mailItem.PurchaseSource != purchaseSourceMarket || mailItem.PurchaseCurrency != purchaseCurrencyVoucher ||
		mailItem.PurchaseUnitPrice != 321 {
		t.Fatalf("loaded mail origin=%+v", mailItem)
	}
	previousServer := globalServer
	server := &Server{store: store}
	globalServer = server
	t.Cleanup(func() { globalServer = previousServer })
	listing := &consignmentItem{ID: 501, SellerID: playerID, ItemId: 9004, ItemType: 2, Count: 3, Price: 50,
		PurchaseSource: purchaseSourceMarket, PurchaseCurrency: purchaseCurrencyVoucher, PurchaseUnitPrice: 200}
	if err := saveConsignmentItemDB(listing); err != nil {
		t.Fatal(err)
	}
	var unitPrice int64
	if err := store.db.QueryRow(`SELECT purchase_unit_price FROM consignment_item_purchase_origins WHERE consignment_id = ?`, listing.ID).Scan(&unitPrice); err != nil || unitPrice != 200 {
		t.Fatalf("saved listing origin price=%d err=%v", unitPrice, err)
	}
	if !server.consignmentRefund(listing) {
		t.Fatal("offline refund failed")
	}
	loaded, err = store.FirstPlayer(accountID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Relations.mails) != 2 {
		t.Fatalf("refund mails=%d, want 2", len(loaded.Relations.mails))
	}
	refund := loaded.Relations.mails[1].Items[0]
	if refund.PurchaseSource != listing.PurchaseSource || refund.PurchaseCurrency != listing.PurchaseCurrency || refund.PurchaseUnitPrice != 200 {
		t.Fatalf("offline refund origin=%+v", refund)
	}
	var remaining int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM consignment_item_purchase_origins WHERE consignment_id = ?`, listing.ID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("refunded listing origin remains=%d err=%v", remaining, err)
	}
}
