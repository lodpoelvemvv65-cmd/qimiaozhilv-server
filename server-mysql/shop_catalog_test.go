package main

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"mhqserver/protocol"
)

func TestBuildShopCatalogHonorsEnabledAndPrices(t *testing.T) {
	old := tables
	t.Cleanup(func() { tables = old })
	tables = &datatables{
		marketBase: map[int64]map[string]interface{}{
			1: {"_id": int64(1), "Page": int64(0), "ItemId": int64(1101), "Price_YuanBao": int64(10), "Price_Voucher": int64(25), "NoDiscount": true},
			2: {"_id": int64(2), "Page": int64(0), "ItemId": int64(1102), "Price_YuanBao": int64(20), "Enabled": false},
		},
		shopBase: map[int64]map[string]interface{}{
			3: {"_id": int64(3), "Page": int64(0), "ItemId": int64(1103), "Price": int64(500)},
		},
		multiShop: map[int64]map[string]interface{}{
			4: {"_id": int64(4), "Type": int64(1), "ItemId": int64(1104), "Price": int64(7)},
		},
	}
	catalog := buildShopCatalog(nil)
	if len(catalog) != 3 {
		t.Fatalf("catalog length=%d, want 3 (disabled row omitted)", len(catalog))
	}
	var market, shop, multi *protocol.ShopItemPrice
	for _, item := range catalog {
		switch item.StoreType {
		case shopCatalogMarket:
			market = item
		case shopCatalogShop:
			shop = item
		case shopCatalogMulti:
			multi = item
		}
	}
	if market == nil || market.Price != 10 || market.VoucherPrice != 25 || market.SlotIndex != 0 {
		t.Fatalf("market catalog item=%+v", market)
	}
	if shop == nil || shop.Price != 500 || shop.PageOrType != 0 {
		t.Fatalf("shop catalog item=%+v", shop)
	}
	if multi == nil || multi.Price != 7 || multi.PageOrType != 1 {
		t.Fatalf("multi catalog item=%+v", multi)
	}
}

func TestShopRowEnabledDefaultsToTrue(t *testing.T) {
	if !shopRowEnabled(map[string]interface{}{}) {
		t.Fatal("missing Enabled should keep legacy row on sale")
	}
	if shopRowEnabled(map[string]interface{}{"Enabled": false}) {
		t.Fatal("Enabled=false row still considered active")
	}
}

func TestBuildShopCatalogCompactsSlotsAfterUnlisting(t *testing.T) {
	old := tables
	t.Cleanup(func() { tables = old })
	tables = &datatables{
		shopBase: map[int64]map[string]interface{}{
			1: {"_id": int64(1), "Page": int64(0), "ItemId": int64(1101), "Price": int64(10)},
			2: {"_id": int64(2), "Page": int64(0), "ItemId": int64(1102), "Price": int64(20), "Enabled": false},
			3: {"_id": int64(3), "Page": int64(0), "ItemId": int64(1103), "Price": int64(30)},
		},
	}
	catalog := buildShopCatalog(nil)
	if len(catalog) != 2 {
		t.Fatalf("catalog length=%d, want 2", len(catalog))
	}
	for _, item := range catalog {
		if item.StoreType != shopCatalogShop {
			t.Fatalf("unexpected store type=%d", item.StoreType)
		}
		if item.ConfigId == 1 && item.SlotIndex != 0 {
			t.Fatalf("first item slot=%d, want 0", item.SlotIndex)
		}
		if item.ConfigId == 3 && item.SlotIndex != 1 {
			t.Fatalf("item after disabled row slot=%d, want compact slot 1", item.SlotIndex)
		}
	}
}

func TestRememberShopCatalogUsesConfigIDAfterUnlisting(t *testing.T) {
	ss := &session{}
	ss.rememberShopCatalog([]*protocol.ShopItemPrice{{
		StoreType: shopCatalogShop, ConfigId: 42, PageOrType: 0, SlotIndex: 0,
	}})
	if id, ok := ss.catalogConfigID(shopCatalogShop, 0, 0); !ok || id != 42 {
		t.Fatalf("catalog lookup=(%d,%v), want (42,true)", id, ok)
	}
	ss.mergeShopCatalog([]*protocol.ShopItemPrice{{
		StoreType: shopCatalogMarket, ConfigId: 99, PageOrType: 0, SlotIndex: 0,
	}})
	if id, ok := ss.catalogConfigID(shopCatalogShop, 0, 0); !ok || id != 42 {
		t.Fatalf("merge replaced existing shop entry=(%d,%v)", id, ok)
	}
}

func TestCompactShopPriceDataSkipsDisabledAndPreservesFields(t *testing.T) {
	data := compactShopPriceData([]*protocol.ShopItemPrice{
		{StoreType: 1, ConfigId: 11, PageOrType: 2, SlotIndex: 3, ItemId: 1101, Price: 999999, VoucherPrice: 1999998, Enabled: true},
		{StoreType: 2, ConfigId: 12, PageOrType: 0, SlotIndex: 4, ItemId: 1102, Price: 50, Enabled: false},
	})
	want := []int32{1, 2, 3, 11, 1101, 999999, 1999998}
	if len(data) != len(want) {
		t.Fatalf("compact length=%d, want %d (%v)", len(data), len(want), data)
	}
	for i := range want {
		if data[i] != want[i] {
			t.Fatalf("compact[%d]=%d, want %d", i, data[i], want[i])
		}
	}
}

func TestShopPriceCompatibilityFieldsRoundTrip(t *testing.T) {
	market := &protocol.M2C_GetMarket{PriceList: []int32{12, 24, 99, 198}}
	raw, err := proto.Marshal(market)
	if err != nil {
		t.Fatal(err)
	}
	var decodedMarket protocol.M2C_GetMarket
	if err := proto.Unmarshal(raw, &decodedMarket); err != nil {
		t.Fatal(err)
	}
	if len(decodedMarket.PriceList) != 4 || decodedMarket.PriceList[2] != 99 {
		t.Fatalf("market PriceList=%v", decodedMarket.PriceList)
	}
	active := &protocol.M2C_SendActiveInfo{ShopPriceData: []int32{1, 0, 0, 7, 1101, 88, 176}}
	raw, err = proto.Marshal(active)
	if err != nil {
		t.Fatal(err)
	}
	var decodedActive protocol.M2C_SendActiveInfo
	if err := proto.Unmarshal(raw, &decodedActive); err != nil {
		t.Fatal(err)
	}
	if len(decodedActive.ShopPriceData) != 7 || decodedActive.ShopPriceData[5] != 88 {
		t.Fatalf("active ShopPriceData=%v", decodedActive.ShopPriceData)
	}
}
