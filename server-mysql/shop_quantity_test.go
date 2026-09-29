package main

import (
	"testing"

	"mhqserver/internal/operationsconfig"
	"mhqserver/protocol"
)

func TestBuyInShopHonorsRequestedQuantity(t *testing.T) {
	previous := tables
	tables = &datatables{
		shopBase: map[int64]map[string]interface{}{
			1: {"_id": int64(1), "Page": int64(0), "Price": int64(7), "ItemId": int64(110305)},
		},
		goodsBase: map[int64]map[string]interface{}{
			110305: {"_id": int64(110305), "MaxAmount": int64(999)},
		},
	}
	defer func() { tables = previous }()

	ss := newSession()
	ss.playerID = 42
	ss.coin = 100
	ss.bag = make(map[int32]*bagItem)
	conn := &recordingConn{}
	ch := &channel{id: 1, session: ss, conn: conn}
	server := &Server{}
	if got := server.onBuyInShop(ch, &protocol.C2M_BuyInShop{PageIndex: 0, SlotIndex: 0, Count: 4, RpcId: 9}); got != nil {
		t.Fatalf("onBuyInShop returned unexpected RPC response: %+v", got)
	}
	if ss.coin != 72 {
		t.Fatalf("coin after purchase = %d, want 72", ss.coin)
	}
	if got := bagItemCount(ss, 110305); got != 4 {
		t.Fatalf("purchased stack count = %d, want 4", got)
	}
	if item := ss.bag[0]; item == nil || item.PurchaseSource != purchaseSourceShop ||
		item.PurchaseCurrency != purchaseCurrencyCoin || item.PurchaseUnitPrice != 7 {
		t.Fatalf("shop purchase origin = %+v", item)
	}
	_ = findRecordedFrame(t, conn.Bytes(), protocol.OpM2C_SendBag)
}

func TestBuyInShopMarksEquipmentSource(t *testing.T) {
	previous := tables
	tables = &datatables{
		shopBase: map[int64]map[string]interface{}{
			1: {"_id": int64(1), "Page": int64(0), "Price": int64(7), "ItemId": int64(120001)},
		},
		equipBase: map[int64]map[string]interface{}{
			120001: {"_id": int64(120001), "Quality": int64(1), "Star": int64(1)},
		},
	}
	defer func() { tables = previous }()

	ss := newSession()
	ss.playerID, ss.coin = 42, 100
	ss.bag = make(map[int32]*bagItem)
	conn := &recordingConn{}
	ch := &channel{id: 1, session: ss, conn: conn}
	if got := (&Server{}).onBuyInShop(ch, &protocol.C2M_BuyInShop{PageIndex: 0, SlotIndex: 0, Count: 1, RpcId: 10}); got != nil {
		t.Fatalf("onBuyInShop returned unexpected RPC response: %+v", got)
	}
	if ss.bag[0] == nil || ss.bag[0].GetSource != "普通商店" {
		t.Fatalf("purchased equipment source = %#v, want 普通商店", ss.bag[0])
	}
}

func TestBuyInMarketAllowsVoucherAndYuanBaoForEveryItem(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.Market.MinimumPercent = 100
		config.Market.MaximumPercent = 100
	})
	previous := tables
	tables = &datatables{
		marketBase: map[int64]map[string]interface{}{
			1: {"_id": int64(1), "Page": int64(0), "Price_YuanBao": int64(10), "ItemId": int64(110305), "OnlyYuanBao": true},
		},
		goodsBase: map[int64]map[string]interface{}{
			110305: {"_id": int64(110305), "MaxAmount": int64(999)},
		},
	}
	defer func() { tables = previous }()

	tests := []struct {
		name          string
		marketType    protocol.MarketType
		yuanBao       int64
		voucher       int64
		wantYB        int64
		wantVoucher   int64
		wantCurrency  int32
		wantUnitPrice int64
	}{
		{name: "voucher", marketType: protocol.MarketType_VoucherMarket, voucher: 100, wantVoucher: 60, wantCurrency: purchaseCurrencyVoucher, wantUnitPrice: 20},
		{name: "yuanbao", marketType: protocol.MarketType_YuanBaoMarket, yuanBao: 100, wantYB: 80, wantCurrency: purchaseCurrencyYuanBao, wantUnitPrice: 10},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ss := newSession()
			ss.playerID = 42
			ss.accountID = 42
			ss.yuanBao = tc.yuanBao
			ss.voucher = tc.voucher
			ss.bag = make(map[int32]*bagItem)
			conn := &recordingConn{}
			ch := &channel{id: 1, session: ss, conn: conn}
			server := &Server{}
			if got := server.onBuyInMarket(ch, &protocol.C2M_BuyInMarket{
				PageIndex: 0, SlotIndex: 0, Count: 2, Type: tc.marketType, RpcId: 9,
			}); got != nil {
				t.Fatalf("onBuyInMarket returned unexpected RPC response: %+v", got)
			}
			if ss.yuanBao != tc.wantYB || ss.voucher != tc.wantVoucher {
				t.Fatalf("currencies after purchase = yuanbao %d voucher %d, want %d/%d",
					ss.yuanBao, ss.voucher, tc.wantYB, tc.wantVoucher)
			}
			if got := bagItemCount(ss, 110305); got != 2 {
				t.Fatalf("purchased stack count = %d, want 2", got)
			}
			item := ss.bag[0]
			if item == nil || item.PurchaseSource != purchaseSourceMarket || item.PurchaseCurrency != tc.wantCurrency ||
				item.PurchaseUnitPrice != tc.wantUnitPrice {
				t.Fatalf("market purchase origin = %+v", item)
			}
			_ = findRecordedFrame(t, conn.Bytes(), protocol.OpM2C_SendBag)
		})
	}
}
