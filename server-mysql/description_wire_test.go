package main

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"mhqserver/protocol"
)

func TestNetItemCarriesHotReloadedDescription(t *testing.T) {
	previous := tables
	t.Cleanup(func() { tables = previous })
	tables = &datatables{
		goodsBase:    map[int64]map[string]interface{}{110344: {"Description": "使用后返回城镇1"}},
		equipBase:    map[int64]map[string]interface{}{},
		materialBase: map[int64]map[string]interface{}{},
	}
	raw := encodeNetItem(&bagItem{ItemId: 110344, ItemType: 2, Count: 1})
	var got protocol.NetItem
	if err := proto.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.GetDescription() != "使用后返回城镇1" {
		t.Fatalf("description=%q", got.GetDescription())
	}
}

func TestCompactShopDescriptionDataKeepsCatalogOrder(t *testing.T) {
	items := []*protocol.ShopItemPrice{
		{ItemId: 1, Enabled: true, Description: "a"},
		{ItemId: 2, Enabled: false, Description: "hidden"},
		{ItemId: 3, Enabled: true, Description: "c"},
	}
	got := compactShopDescriptionData(items)
	if len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Fatalf("descriptions=%v", got)
	}
}

func TestShopCatalogCarriesCurrentDescription(t *testing.T) {
	previous := tables
	t.Cleanup(func() { tables = previous })
	tables = &datatables{
		goodsBase: map[int64]map[string]interface{}{110344: {"Description": "服务端说明"}},
		marketBase: map[int64]map[string]interface{}{1: {
			"_id": int64(1), "Page": int64(1), "ItemId": int64(110344), "Price": int64(10),
		}},
		shopBase:  map[int64]map[string]interface{}{},
		multiShop: map[int64]map[string]interface{}{},
	}
	items := buildShopCatalog(nil)
	if len(items) != 1 || items[0].Description != "服务端说明" {
		t.Fatalf("catalog=%+v", items)
	}
}
