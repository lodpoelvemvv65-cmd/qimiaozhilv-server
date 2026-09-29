package main

import (
	"math"
	"testing"

	"google.golang.org/protobuf/proto"
	"mhqserver/internal/operationsconfig"
	"mhqserver/protocol"
)

func TestMultiShopUsesDedicatedCurrencyForEachType(t *testing.T) {
	old := tables
	tables = &datatables{
		multiShop: map[int64]map[string]interface{}{
			1: {"_id": int64(1), "Type": int64(1), "ItemId": int64(9001), "Price": int64(7)},
			2: {"_id": int64(2), "Type": int64(2), "ItemId": int64(9002), "Price": int64(11)},
			3: {"_id": int64(3), "Type": int64(3), "ItemId": int64(9003), "Price": int64(13)},
			4: {"_id": int64(4), "Type": int64(4), "ItemId": int64(9004), "Price": int64(17)},
		},
		goodsBase: map[int64]map[string]interface{}{
			9001: {"_id": int64(9001), "MaxAmount": int64(999)}, 9002: {"_id": int64(9002), "MaxAmount": int64(999)},
			9003: {"_id": int64(9003), "MaxAmount": int64(999)}, 9004: {"_id": int64(9004), "MaxAmount": int64(999)},
			110205: {"_id": int64(110205), "MaxAmount": int64(999999999)},
		},
	}
	t.Cleanup(func() { tables = old })

	for _, tc := range []struct {
		shopType                                                         int32
		wantItem                                                         int32
		coin, yuanBao, honor, pvp, family, stars                         int64
		wantCoin, wantYuanBao, wantHonor, wantPVP, wantFamily, wantStars int64
	}{
		{1, 9001, 100, 999, 100, 100, 100, 50, 100, 999, 100, 100, 100, 43},
		{2, 9002, 100, 999, 100, 100, 100, 50, 100, 999, 89, 100, 100, 50},
		{3, 9003, 100, 999, 100, 100, 100, 50, 100, 999, 100, 87, 100, 50},
		{4, 9004, 100, 999, 100, 100, 100, 50, 100, 999, 100, 100, 83, 50},
	} {
		ss := newSession()
		ss.playerID, ss.coin, ss.yuanBao = 1, tc.coin, tc.yuanBao
		ss.honor, ss.pvpCurrency, ss.familyContribute = tc.honor, tc.pvp, int32(tc.family)
		ss.bag = map[int32]*bagItem{0: {ItemId: 110205, ItemType: int32(protocol.ItemType_GoodsItem), Count: int32(tc.stars)}}
		ch := &channel{id: 1, session: ss, conn: &recordingConn{}}
		if got := (&Server{}).onBuyInMultiShop(ch, &protocol.C2M_BuyInMultiShop{ShopType: tc.shopType, Index: 0, Count: 1}); got != nil {
			t.Fatalf("type %d returned %+v", tc.shopType, got)
		}
		if ss.coin != tc.wantCoin || ss.yuanBao != tc.wantYuanBao || ss.honor != tc.wantHonor || ss.pvpCurrency != tc.wantPVP || int64(ss.familyContribute) != tc.wantFamily || starCoinBalance(ss) != tc.wantStars {
			t.Fatalf("type %d balances coin=%d yuanbao=%d honor=%d pvp=%d family=%d stars=%d", tc.shopType, ss.coin, ss.yuanBao, ss.honor, ss.pvpCurrency, ss.familyContribute, starCoinBalance(ss))
		}
		if bagItemCount(ss, tc.wantItem) != 1 {
			t.Fatalf("type %d purchased item count=%d", tc.shopType, bagItemCount(ss, tc.wantItem))
		}
		var purchased *bagItem
		for _, item := range ss.bag {
			if item != nil && item.ItemId == tc.wantItem {
				purchased = item
			}
		}
		if purchased == nil || purchased.PurchaseSource != purchaseSourceMultiShop || purchased.PurchaseUnitPrice == 0 {
			t.Fatalf("type %d purchase origin=%+v", tc.shopType, purchased)
		}
	}
}

func TestPushMoneyPublishesAllCurrencyNumericTypes(t *testing.T) {
	ss := newSession()
	ss.playerID, ss.coin, ss.yuanBao, ss.voucher = 9, 101, 202, 303
	ss.honor, ss.pvpCurrency, ss.familyContribute = 404, 505, 606
	ss.bag = map[int32]*bagItem{0: {ItemId: 110205, ItemType: int32(protocol.ItemType_GoodsItem), Count: 707}}
	conn := &recordingConn{}
	(&Server{}).pushMoney(&channel{id: 1, session: ss, conn: conn})
	got := make(map[int32]float32)
	for _, frame := range decodeRecordedFrames(t, conn.Bytes()) {
		if frame.opcode != protocol.OpM2C_SyncUnitAttribute {
			continue
		}
		msg := &protocol.M2C_SyncUnitAttribute{}
		if err := proto.Unmarshal(frame.body, msg); err != nil {
			t.Fatal(err)
		}
		got[msg.NumericType] = msg.Value
	}
	for typ, want := range map[int32]float32{ntCoin: 101, ntYuanBao: 202, ntVoucher: 303, ntStarCoin: 707, ntHonor: 404, ntPVPCurrency: 505, ntPVPMoney: 505, ntFamilyContribution: 606} {
		if got[typ] != want {
			t.Errorf("NumericType %d=%v, want %v", typ, got[typ], want)
		}
	}
}

func TestMultiShopFullBagRefundsExactCurrency(t *testing.T) {
	old := tables
	tables = &datatables{multiShop: map[int64]map[string]interface{}{1: {"_id": int64(1), "Type": int64(2), "ItemId": int64(9001), "Price": int64(25)}}, goodsBase: map[int64]map[string]interface{}{9001: {"_id": int64(9001)}}}
	t.Cleanup(func() { tables = old })
	ss := newSession()
	ss.playerID, ss.honor, ss.coin = 1, 100, 777
	ss.bag = make(map[int32]*bagItem)
	for i := int32(0); i < bagSlotCount; i++ {
		ss.bag[i] = &bagItem{ItemId: 10000 + i, ItemType: int32(protocol.ItemType_GoodsItem), Count: 1}
	}
	response := (&Server{}).onBuyInMultiShop(&channel{id: 1, session: ss, conn: &recordingConn{}}, &protocol.C2M_BuyInMultiShop{ShopType: 2, Count: 1})
	if response == nil || ss.honor != 100 || ss.coin != 777 || len(ss.bag) != int(bagSlotCount) {
		t.Fatalf("full bag changed state response=%+v honor=%d coin=%d slots=%d", response, ss.honor, ss.coin, len(ss.bag))
	}
}

func TestSellItemUsesOnlinePriceAndRejectsCurrencyGoods(t *testing.T) {
	old := tables
	tables = &datatables{goodsBase: map[int64]map[string]interface{}{
		9001:   {"_id": int64(9001), "Price": int64(125), "MaxAmount": int64(999)},
		9002:   {"_id": int64(9002), "MaxAmount": int64(999)},
		110203: {"_id": int64(110203)},
	}, materialBase: map[int64]map[string]interface{}{
		9003: {"_id": int64(9003), "Price": int64(9), "MaxAmount": int64(999)},
	}, equipBase: map[int64]map[string]interface{}{
		120877: {"_id": int64(120877), "Price": int64(500)},
	}, manulEquip: map[int64]map[string]interface{}{
		7: {"_id": int64(7), "EquipId": int64(120877)},
	}}
	t.Cleanup(func() { tables = old })
	ss := newSession()
	ss.playerID, ss.coin = 1, 10
	ss.bag = map[int32]*bagItem{0: {ItemId: 9001, ItemType: int32(protocol.ItemType_GoodsItem), Count: 3}}
	ch := &channel{id: 1, session: ss, conn: &recordingConn{}}
	if got := (&Server{}).onSellItem(ch, &protocol.C2M_SellItem{SlotIndex: 0, Count: 2}); got != nil || ss.coin != 260 || bagItemCount(ss, 9001) != 1 {
		t.Fatalf("priced sale result=%+v coin=%d count=%d", got, ss.coin, bagItemCount(ss, 9001))
	}
	ss.bag[1] = &bagItem{ItemId: 110203, ItemType: int32(protocol.ItemType_GoodsItem), Count: 5}
	before := ss.coin
	if got := (&Server{}).onSellItem(ch, &protocol.C2M_SellItem{SlotIndex: 1, Count: 1}); got == nil || ss.coin != before || bagItemCount(ss, 110203) != 5 {
		t.Fatalf("currency sale was accepted: result=%+v coin=%d count=%d", got, ss.coin, bagItemCount(ss, 110203))
	}
	ss.bag[2] = &bagItem{ItemId: 9002, ItemType: int32(protocol.ItemType_GoodsItem), Count: 1}
	if got := (&Server{}).onSellItem(ch, &protocol.C2M_SellItem{SlotIndex: 2, Count: 1}); got != nil || ss.coin != before+500 {
		t.Fatalf("missing-price fallback sale result=%+v coin=%d, want %d", got, ss.coin, before+500)
	}
	before = ss.coin
	ss.bag[3] = &bagItem{ItemId: 9003, ItemType: int32(protocol.ItemType_MaterialsItem), Count: 2}
	if got := (&Server{}).onSellItem(ch, &protocol.C2M_SellItem{SlotIndex: 3, Count: 2}); got != nil || ss.coin != before+18 {
		t.Fatalf("material sale result=%+v coin=%d, want %d", got, ss.coin, before+18)
	}
	ss.bag[4] = &bagItem{
		ItemId:      120877,
		ItemType:    int32(protocol.ItemType_EquipItem),
		Count:       1,
		GetSource:   "manual crafter",
		RandomAttrs: []int32{608, 615, 626},
	}
	before = ss.coin
	if got := (&Server{}).onSellItem(ch, &protocol.C2M_SellItem{SlotIndex: 4, Count: 1}); got != nil || ss.coin != before+500 || ss.bag[4] != nil {
		t.Fatalf("manual equipment sale result=%+v coin=%d item=%+v, want coin=%d and empty slot", got, ss.coin, ss.bag[4], before+500)
	}
}

func TestSellItemUsesHotReloadableShopSaleRuleForAnyItemType(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.ShopSale.Items = []operationsconfig.ShopSaleRule{{ItemID: 9001, Enabled: true, PriceCoin: 700}}
	})
	old := tables
	tables = &datatables{goodsBase: map[int64]map[string]interface{}{
		9001: {"_id": int64(9001), "Price": int64(125), "MaxAmount": int64(999)},
	}}
	t.Cleanup(func() { tables = old })
	ss := newSession()
	ss.playerID, ss.coin = 1, 0
	ss.bag = map[int32]*bagItem{0: {ItemId: 9001, ItemType: int32(protocol.ItemType_GoodsItem), Count: 2}}
	ch := &channel{id: 1, session: ss, conn: &recordingConn{}}
	if got := (&Server{}).onSellItem(ch, &protocol.C2M_SellItem{SlotIndex: 0, Count: 1}); got != nil || ss.coin != 700 {
		t.Fatalf("configured goods sale result=%+v coin=%d, want 700", got, ss.coin)
	}
	// Change the in-memory snapshot as the Redis notification would do; the
	// next request must use the new value without reconnecting the player.
	config := gameplayConfigSnapshot()
	config.ShopSale.Items = append([]operationsconfig.ShopSaleRule(nil), config.ShopSale.Items...)
	config.ShopSale.Items[0].PriceCoin = 900
	storeGameplayConfig(config)
	if got := (&Server{}).onSellItem(ch, &protocol.C2M_SellItem{SlotIndex: 0, Count: 1}); got != nil || ss.coin != 1600 {
		t.Fatalf("hot-reloaded goods sale result=%+v coin=%d, want 1600", got, ss.coin)
	}
	ss.bag[0] = &bagItem{ItemId: 9001, ItemType: int32(protocol.ItemType_GoodsItem), Count: 1}
	config = gameplayConfigSnapshot()
	config.ShopSale.Items = append([]operationsconfig.ShopSaleRule(nil), config.ShopSale.Items...)
	config.ShopSale.Items[0].Enabled = false
	storeGameplayConfig(config)
	before := ss.coin
	if got := (&Server{}).onSellItem(ch, &protocol.C2M_SellItem{SlotIndex: 0, Count: 1}); got == nil || ss.coin != before || bagItemCount(ss, 9001) != 1 {
		t.Fatalf("disabled hot-reloaded sale was accepted: result=%+v coin=%d count=%d", got, ss.coin, bagItemCount(ss, 9001))
	}
}

func TestSellItemRecognizesAllManualEquipmentProductsFromTable(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.ShopSale.ManualEquipment = operationsconfig.ShopSaleCategory{Enabled: true, PriceCoin: 500}
	})
	old := tables
	tables = &datatables{
		equipBase: map[int64]map[string]interface{}{
			120877: {"_id": int64(120877), "Price": int64(1)},
			120855: {"_id": int64(120855), "Price": int64(2)},
			120854: {"_id": int64(120854), "Price": int64(3)},
			120856: {"_id": int64(120856), "Price": int64(4)},
		},
		manulEquip: map[int64]map[string]interface{}{
			7: {"EquipId": int64(120877)}, 3: {"EquipId": int64(120855)},
			4: {"EquipId": int64(120854)}, 8: {"EquipId": int64(120856)},
		},
	}
	t.Cleanup(func() { tables = old })
	ss := newSession()
	ss.playerID = 1
	ss.bag = make(map[int32]*bagItem)
	for slot, itemID := range []int32{120877, 120855, 120854, 120856} {
		ss.bag[int32(slot)] = &bagItem{ItemId: itemID, ItemType: int32(protocol.ItemType_EquipItem), Count: 1}
	}
	ch := &channel{id: 1, session: ss, conn: &recordingConn{}}
	for slot := int32(0); slot < 4; slot++ {
		if got := (&Server{}).onSellItem(ch, &protocol.C2M_SellItem{SlotIndex: slot, Count: 1}); got != nil {
			t.Fatalf("manual product slot %d sale result=%+v", slot, got)
		}
	}
	if ss.coin != 4*500 {
		t.Fatalf("manual products credited %d, want 2000", ss.coin)
	}
}

func TestMailCurrencyAttachmentsCreditMatchingBalances(t *testing.T) {
	ss := newSession()
	ss.playerID, ss.coin, ss.yuanBao, ss.voucher = 1, 10, 20, 30
	ss.bag = make(map[int32]*bagItem)
	ss.mails = []*mailMsg{{Id: 1, Items: []mailItemMsg{
		{ItemId: 110202, Count: 4, IsHasItem: true},
		{ItemId: 110203, Count: 5, IsHasItem: true},
		{ItemId: 110204, Count: 6, IsHasItem: true},
		{ItemId: 110205, Count: 7, IsHasItem: true},
	}}}
	ch := &channel{id: 1, session: ss, conn: &recordingConn{}}
	if got := (&Server{}).onReceiveMail(ch, &protocol.C2M_ReceiveMail{MailId: 1, Page: 1}); got != nil {
		t.Fatalf("receive mail returned %+v", got)
	}
	if ss.coin != 15 || ss.yuanBao != 24 || ss.voucher != 36 || starCoinBalance(ss) != 7 || ss.mails[0].State != 1 {
		t.Fatalf("mail currencies coin=%d yuanbao=%d voucher=%d star=%d state=%d", ss.coin, ss.yuanBao, ss.voucher, starCoinBalance(ss), ss.mails[0].State)
	}
}

func TestCurrencyHelpersRejectNegativeValues(t *testing.T) {
	ss := newSession()
	ss.playerID = 1
	ss.coin, ss.yuanBao = 100, 200
	ch := &channel{id: 1, session: ss, conn: &recordingConn{}}
	server := &Server{}
	server.addCoin(ch, -10)
	server.addYuanBao(ch, -10)
	if ss.coin != 100 || ss.yuanBao != 200 {
		t.Fatalf("negative currency grant changed balances: coin=%d yuanbao=%d", ss.coin, ss.yuanBao)
	}
	if server.spendCoin(ch, -1) || server.spendYuanBao(ch, -1) || server.spendVoucher(ch, -1) {
		t.Fatal("negative currency spend unexpectedly succeeded")
	}
}

func TestMarketRejectsUnknownCurrencyTypeWithoutDebit(t *testing.T) {
	old := tables
	tables = &datatables{
		marketBase: map[int64]map[string]interface{}{
			1: {"_id": int64(1), "Page": int64(0), "ItemId": int64(9001), "Price_YuanBao": int64(25)},
		},
		goodsBase: map[int64]map[string]interface{}{
			9001: {"_id": int64(9001), "MaxAmount": int64(999)},
		},
	}
	t.Cleanup(func() { tables = old })
	ss := newSession()
	ss.playerID, ss.yuanBao, ss.voucher = 1, 100, 200
	ss.bag = make(map[int32]*bagItem)
	resp := (&Server{}).onBuyInMarket(&channel{id: 1, session: ss, conn: &recordingConn{}}, &protocol.C2M_BuyInMarket{
		PageIndex: 0, SlotIndex: 0, Count: 1, Type: protocol.MarketType_NoneMarket,
	})
	if resp == nil || resp.(*protocol.M2C_BuyInMarket).Message == "" {
		t.Fatalf("unknown market type was accepted: %+v", resp)
	}
	if ss.yuanBao != 100 || ss.voucher != 200 || len(ss.bag) != 0 {
		t.Fatalf("unknown market type changed state: yuanbao=%d voucher=%d bag=%+v", ss.yuanBao, ss.voucher, ss.bag)
	}
}

func TestSellAtCurrencyLimitKeepsItem(t *testing.T) {
	old := tables
	tables = &datatables{goodsBase: map[int64]map[string]interface{}{
		9001: {"_id": int64(9001), "Price": int64(1), "MaxAmount": int64(999)},
	}}
	t.Cleanup(func() { tables = old })
	ss := newSession()
	ss.playerID, ss.coin = 1, math.MaxInt64
	ss.bag = map[int32]*bagItem{0: {ItemId: 9001, ItemType: int32(protocol.ItemType_GoodsItem), Count: 2}}
	resp := (&Server{}).onSellItem(&channel{id: 1, session: ss, conn: &recordingConn{}}, &protocol.C2M_SellItem{SlotIndex: 0, Count: 1})
	if resp == nil || bagItemCount(ss, 9001) != 2 || ss.coin != math.MaxInt64 {
		t.Fatalf("overflow sale changed state: response=%+v coin=%d count=%d", resp, ss.coin, bagItemCount(ss, 9001))
	}
}

func TestStoreCoinBoundsAreAtomic(t *testing.T) {
	server := &Server{}
	ss := newSession()
	ss.playerID, ss.coin, ss.storeCoin = 1, 10, math.MaxInt64
	ch := &channel{id: 1, session: ss, conn: &recordingConn{}}
	if resp := server.onPutCoinInStore(ch, &protocol.C2M_PutCoinInStore{Coin: 1}); resp.(*protocol.M2C_PutCoinInStore).Message == "" {
		t.Fatal("store overflow was accepted")
	}
	if ss.coin != 10 || ss.storeCoin != math.MaxInt64 {
		t.Fatalf("put overflow changed balances coin=%d store=%d", ss.coin, ss.storeCoin)
	}
	ss.coin, ss.storeCoin = math.MaxInt64, 1
	if resp := server.onTakeCoinOutStore(ch, &protocol.C2M_TakeCoinOutStore{Coin: 1}); resp.(*protocol.M2C_TakeCoinOutStore).Message == "" {
		t.Fatal("bag overflow was accepted")
	}
	if ss.coin != math.MaxInt64 || ss.storeCoin != 1 {
		t.Fatalf("take overflow changed balances coin=%d store=%d", ss.coin, ss.storeCoin)
	}
}

func TestMailCurrencyOverflowLeavesAttachmentUnclaimed(t *testing.T) {
	ss := newSession()
	ss.playerID, ss.coin = 1, math.MaxInt64
	ss.bag = make(map[int32]*bagItem)
	ss.mails = []*mailMsg{{Id: 1, Items: []mailItemMsg{{ItemId: 110203, Count: 1, IsHasItem: true}}}}
	resp := (&Server{}).onReceiveMail(&channel{id: 1, session: ss, conn: &recordingConn{}}, &protocol.C2M_ReceiveMail{MailId: 1, Page: 1})
	if resp == nil || ss.coin != math.MaxInt64 || ss.mails[0].State != 0 {
		t.Fatalf("overflow mail was claimed: response=%+v coin=%d state=%d", resp, ss.coin, ss.mails[0].State)
	}
}

func TestShopAndMarketFullBagDoNotDebit(t *testing.T) {
	old := tables
	tables = &datatables{
		shopBase:   map[int64]map[string]interface{}{1: {"_id": int64(1), "Page": int64(0), "ItemId": int64(9001), "Price": int64(10)}},
		marketBase: map[int64]map[string]interface{}{1: {"_id": int64(1), "Page": int64(0), "ItemId": int64(9002), "Price_YuanBao": int64(10)}},
		goodsBase: map[int64]map[string]interface{}{
			9001: {"_id": int64(9001), "MaxAmount": int64(999)},
			9002: {"_id": int64(9002), "MaxAmount": int64(999)},
		},
	}
	t.Cleanup(func() { tables = old })
	ss := newSession()
	ss.playerID, ss.coin, ss.yuanBao, ss.voucher = 1, 100, 100, 100
	ss.bag = make(map[int32]*bagItem)
	for i := int32(0); i < bagSlotCount; i++ {
		ss.bag[i] = &bagItem{ItemId: 10000 + i, ItemType: int32(protocol.ItemType_GoodsItem), Count: 1}
	}
	ch := &channel{id: 1, session: ss, conn: &recordingConn{}}
	if resp := (&Server{}).onBuyInShop(ch, &protocol.C2M_BuyInShop{PageIndex: 0, SlotIndex: 0, Count: 1}); resp == nil {
		t.Fatal("full normal shop purchase was accepted")
	}
	if resp := (&Server{}).onBuyInMarket(ch, &protocol.C2M_BuyInMarket{PageIndex: 0, SlotIndex: 0, Count: 1, Type: protocol.MarketType_YuanBaoMarket}); resp == nil {
		t.Fatal("full market purchase was accepted")
	}
	if ss.coin != 100 || ss.yuanBao != 100 || ss.voucher != 100 {
		t.Fatalf("full-bag purchases debited currency coin=%d yuanbao=%d voucher=%d", ss.coin, ss.yuanBao, ss.voucher)
	}
}

func TestCurrencyConversionOverflowDoesNotConsumeSource(t *testing.T) {
	server := &Server{}
	ss := newSession()
	ss.playerID, ss.yuanBao, ss.voucher = 1, 25, math.MaxInt64
	ss.bag = make(map[int32]*bagItem)
	ch := &channel{id: 1, session: ss, conn: &recordingConn{}}
	resp := server.onChargeVoucher(ch, &protocol.C2M_ChargeVoucher{YuanBao: 5}).(*protocol.M2C_ChargeVoucher)
	if resp.Message == "" || ss.yuanBao != 25 || ss.voucher != math.MaxInt64 {
		t.Fatalf("voucher conversion overflow changed state response=%+v yuanbao=%d voucher=%d", resp, ss.yuanBao, ss.voucher)
	}

	old := tables
	tables = &datatables{goodsBase: map[int64]map[string]interface{}{
		110204: {"_id": int64(110204), "EffectType": int64(0)},
	}}
	t.Cleanup(func() { tables = old })
	ss.bag[0] = &bagItem{ItemId: 110204, ItemType: int32(protocol.ItemType_GoodsItem), Count: 1}
	useResp := server.onUseGoods(ch, &protocol.C2M_UseGoods{Index: 0}).(*protocol.M2C_UseGoods)
	if useResp.Message == "" || ss.voucher != math.MaxInt64 || bagItemCount(ss, 110204) != 1 {
		t.Fatalf("currency item overflow changed state response=%+v voucher=%d item=%d", useResp, ss.voucher, bagItemCount(ss, 110204))
	}
}

func TestMarketPriceOverflowIsRejected(t *testing.T) {
	if got := marketDiscountedPrice(math.MaxInt64, 2); got >= 0 {
		t.Fatalf("market overflow produced price %d", got)
	}
}
