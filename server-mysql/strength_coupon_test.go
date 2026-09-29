package main

import (
	"testing"

	"mhqserver/internal/operationsconfig"
	"mhqserver/protocol"
)

func strengthCouponTables() *datatables {
	return &datatables{
		equipBase: map[int64]map[string]interface{}{
			120001: {"_id": int64(120001), "Quality": int64(1), "Star": int64(1)},
		},
		goodsBase: map[int64]map[string]interface{}{
			int64(strengthCouponItemID): {"_id": int64(strengthCouponItemID), "EffectType": int64(23)},
		},
	}
}

func TestStrengthCouponRaisesFirstBagEquipmentToTwenty(t *testing.T) {
	withFeatureTables(t, strengthCouponTables())
	ss := featureTestSession(8101)
	ss.bag[0] = &bagItem{ItemId: 120001, ItemType: int32(protocol.ItemType_EquipItem), Level: 7}
	ss.bag[3] = &bagItem{ItemId: strengthCouponItemID, ItemType: int32(protocol.ItemType_GoodsItem), Count: 2}
	ch := &channel{id: 1, session: ss, conn: &recordingConn{}}
	if response := (&Server{}).onUseGoods(ch, &protocol.C2M_UseGoods{Index: 3, RpcId: 12}); response != nil {
		t.Fatalf("onUseGoods returned direct response: %+v", response)
	}
	if got := ss.bag[0].Level; got != 20 {
		t.Fatalf("equipment level = %d, want 20", got)
	}
	if got := ss.bag[3].Count; got != 1 {
		t.Fatalf("coupon count = %d, want 1", got)
	}
	if !hasRecordedOpcode(t, ch.conn.(*recordingConn), protocol.OpM2C_UseGoods) {
		t.Fatal("successful coupon use did not send M2C_UseGoods")
	}
}

func TestStrengthCouponRejectsInvalidTargetAndMaxLevelWithoutConsuming(t *testing.T) {
	withFeatureTables(t, strengthCouponTables())
	server := &Server{}
	for _, tc := range []struct {
		name   string
		target *bagItem
	}{
		{name: "missing", target: nil},
		{name: "already max", target: &bagItem{ItemId: 120001, ItemType: int32(protocol.ItemType_EquipItem), Level: 20}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ss := featureTestSession(8102)
			ss.bag[0] = tc.target
			ss.bag[2] = &bagItem{ItemId: strengthCouponItemID, ItemType: int32(protocol.ItemType_GoodsItem), Count: 1}
			ch := &channel{id: 2, session: ss, conn: &recordingConn{}}
			response := server.onUseGoods(ch, &protocol.C2M_UseGoods{Index: 2}).(*protocol.M2C_UseGoods)
			if response.Message == "" {
				t.Fatal("invalid target was accepted")
			}
			if got := ss.bag[2].Count; got != 1 {
				t.Fatalf("coupon count = %d after rejection, want 1", got)
			}
		})
	}
}

func TestStrengthCouponWorksFromMainUISlot(t *testing.T) {
	withFeatureTables(t, strengthCouponTables())
	ss := featureTestSession(8103)
	ss.bag[0] = &bagItem{ItemId: 120001, ItemType: int32(protocol.ItemType_EquipItem), Level: 1}
	ss.bag[4] = &bagItem{ItemId: strengthCouponItemID, ItemType: int32(protocol.ItemType_GoodsItem), Count: 1}
	ss.mainUISlots[6] = mainUISlot{Type: 2, Id: strengthCouponItemID}
	ch := &channel{id: 3, session: ss, conn: &recordingConn{}}
	if response := (&Server{}).onUseMainUIGoods(ch, &protocol.C2M_UseMainUIGoods{SlotId: 6}); response != nil {
		t.Fatalf("onUseMainUIGoods returned direct response: %+v", response)
	}
	if got := ss.bag[0].Level; got != 20 {
		t.Fatalf("equipment level = %d, want 20", got)
	}
	if _, exists := ss.bag[4]; exists {
		t.Fatal("main UI coupon was not consumed")
	}
	if !hasRecordedOpcode(t, ch.conn.(*recordingConn), protocol.OpM2C_UseMainUIGoods) {
		t.Fatal("successful main UI coupon use did not send response")
	}
}

func TestStrengthCouponMarketUsesFixedPrices(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.Market.MinimumPercent = 85
		config.Market.MaximumPercent = 125
	})
	row := map[string]interface{}{
		"_id": 10033, "Page": int64(0), "ItemId": int64(strengthCouponItemID),
		"Price_YuanBao": int64(999999), "Price_Voucher": int64(999999), "NoDiscount": true,
	}
	if got := marketYuanBaoPrice(row, 0.85); got != 999999 {
		t.Fatalf("fixed yuanbao price = %d, want 999999", got)
	}
	if got, ok := marketVoucherPrice(row); !ok || got != 999999 {
		t.Fatalf("fixed voucher price = %d, ok=%v; want 999999", got, ok)
	}
	withFeatureTables(t, &datatables{
		marketBase: map[int64]map[string]interface{}{10033: row},
		goodsBase:  map[int64]map[string]interface{}{int64(strengthCouponItemID): {"_id": int64(strengthCouponItemID), "MaxAmount": int64(99)}},
	})
	ss := featureTestSession(8104)
	ss.accountID, ss.yuanBao, ss.voucher = 8104, 2_000_000, 2_000_000
	ch := &channel{id: 4, session: ss, conn: &recordingConn{}}
	server := &Server{}
	server.onBuyInMarket(ch, &protocol.C2M_BuyInMarket{PageIndex: 0, SlotIndex: 0, Count: 1, Type: protocol.MarketType_YuanBaoMarket})
	if ss.yuanBao != 1_000_001 || bagItemCount(ss, strengthCouponItemID) != 1 {
		t.Fatalf("yuanbao purchase balance=%d item=%d, want 1000001/1", ss.yuanBao, bagItemCount(ss, strengthCouponItemID))
	}
	ss = featureTestSession(8105)
	ss.accountID, ss.yuanBao, ss.voucher = 8105, 2_000_000, 2_000_000
	ch = &channel{id: 5, session: ss, conn: &recordingConn{}}
	server.onBuyInMarket(ch, &protocol.C2M_BuyInMarket{PageIndex: 0, SlotIndex: 0, Count: 1, Type: protocol.MarketType_VoucherMarket})
	if ss.voucher != 1_000_001 || bagItemCount(ss, strengthCouponItemID) != 1 {
		t.Fatalf("voucher purchase balance=%d item=%d, want 1000001/1", ss.voucher, bagItemCount(ss, strengthCouponItemID))
	}
}
