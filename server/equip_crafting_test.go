package main

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func withCraftingTables(t *testing.T, value *datatables) {
	t.Helper()
	old := tables
	t.Cleanup(func() { tables = old })
	tables = value
}

func TestEquipCultivationFieldsRoundTripAndEncode(t *testing.T) {
	const itemID int32 = 120001
	withCraftingTables(t, &datatables{equipBase: map[int64]map[string]interface{}{
		int64(itemID): {
			"SpecialKey": json.Number("7"), "Quality": json.Number("4"),
			"Star": json.Number("8"), "PhyAtk": json.Number("100"),
		},
	}})
	it := newBagItem(itemID)
	it.MainAttr = map[int32]float32{7: 0.15, 25: -0.08}
	it.RandomAttrs = []int32{101, 102}
	it.AddAttrs = []int32{110001, 111000}
	it.GemList = []int32{20046, 0}
	it.GetSource = "测试制作者"

	roundTrip := bagFromJSON(bagToJSON(map[int32]*bagItem{3: it}))[3]
	if roundTrip == nil || !reflect.DeepEqual(roundTrip.MainAttr, it.MainAttr) ||
		!reflect.DeepEqual(roundTrip.RandomAttrs, it.RandomAttrs) ||
		!reflect.DeepEqual(roundTrip.AddAttrs, it.AddAttrs) || roundTrip.GetSource != it.GetSource {
		t.Fatalf("cultivation JSON round trip = %#v, want %#v", roundTrip, it)
	}

	raw := encodeEquipTrans(roundTrip)
	var randomIDs, affixIDs []int32
	mainValues := make(map[int32]float32)
	for len(raw) > 0 {
		number, wireType, tagLen := protowire.ConsumeTag(raw)
		if tagLen < 0 {
			t.Fatal(protowire.ParseError(tagLen))
		}
		raw = raw[tagLen:]
		switch {
		case number == 5 && wireType == protowire.BytesType:
			payload, fieldLen := protowire.ConsumeBytes(raw)
			if fieldLen < 0 {
				t.Fatal(protowire.ParseError(fieldLen))
			}
			raw = raw[fieldLen:]
			var attribute protocol.AttributeMap
			if err := proto.Unmarshal(payload, &attribute); err != nil {
				t.Fatal(err)
			}
			mainValues[attribute.Key] = attribute.Value
		case (number == 6 || number == 11) && wireType == protowire.VarintType:
			value, fieldLen := protowire.ConsumeVarint(raw)
			if fieldLen < 0 {
				t.Fatal(protowire.ParseError(fieldLen))
			}
			raw = raw[fieldLen:]
			if number == 6 {
				randomIDs = append(randomIDs, int32(value))
			} else {
				affixIDs = append(affixIDs, int32(value))
			}
		default:
			fieldLen := protowire.ConsumeFieldValue(number, wireType, raw)
			if fieldLen < 0 {
				t.Fatal(protowire.ParseError(fieldLen))
			}
			raw = raw[fieldLen:]
		}
	}
	if math.Abs(float64(mainValues[7]-0.15)) > 0.001 || math.Abs(float64(mainValues[25]+0.08)) > 0.001 {
		t.Fatalf("wire main attributes = %v", mainValues)
	}
	if !reflect.DeepEqual(randomIDs, it.RandomAttrs) || !reflect.DeepEqual(affixIDs, it.AddAttrs) {
		t.Fatalf("wire random/affix = %v/%v, want %v/%v", randomIDs, affixIDs, it.RandomAttrs, it.AddAttrs)
	}
}

func TestStrengthEquipConsumesOnlineCostsAndLevelsUp(t *testing.T) {
	const (
		equipID    int32 = 120001
		materialID int32 = 20215
	)
	withCraftingTables(t, &datatables{
		equipBase: map[int64]map[string]interface{}{
			int64(equipID): {"SpecialKey": json.Number("7"), "SpecialValue": json.Number("100"), "Quality": json.Number("4"), "Star": json.Number("8")},
		},
		strengthen: map[int64]map[string]interface{}{
			1: {
				"Probability": json.Number("1"), "FailLevel": json.Number("0"),
				"NeedCoin":        json.Number("100"),
				"NeedMaterialArr": []interface{}{map[string]interface{}{"_Id": json.Number("20215"), "Count": json.Number("1")}},
			},
		},
		strengthPlus: map[int64]map[string]interface{}{},
		materialBase: map[int64]map[string]interface{}{int64(materialID): {"Name": "上古陨石"}},
	})
	ss := newSession()
	ss.playerID, ss.jobID, ss.level, ss.coin = 7, 1, 1, 500
	ss.bag = map[int32]*bagItem{
		1: newBagItem(equipID),
		2: {ItemId: materialID, ItemType: 3, Count: 1},
	}
	ss.worn = make(map[int32]*bagItem)
	resp := (&Server{}).onStrengthEquip(
		&channel{id: 1, session: ss},
		&protocol.C2M_StrengthEquip{RpcId: 9, BagIndex: 1, PlusItemIndex: -1},
	).(*protocol.M2C_StrengthEquip)
	if resp.Error != 0 || resp.Message != "" || !resp.IsSuccess {
		t.Fatalf("strength response = %+v", resp)
	}
	if ss.bag[1].Level != 2 || ss.bag[2] != nil || ss.coin != 400 {
		t.Fatalf("strength state: level=%d material=%#v coin=%d", ss.bag[1].Level, ss.bag[2], ss.coin)
	}
}

func TestStrengthEquipBusinessFailureKeepsConnectionContract(t *testing.T) {
	const equipID int32 = 120001
	withCraftingTables(t, &datatables{
		equipBase: map[int64]map[string]interface{}{
			int64(equipID): {"SpecialKey": json.Number("7"), "Quality": json.Number("4"), "Star": json.Number("8")},
		},
		strengthen: map[int64]map[string]interface{}{
			1: {"Probability": json.Number("1"), "NeedCoin": json.Number("100"), "NeedMaterialArr": []interface{}{map[string]interface{}{"_Id": json.Number("20215"), "Count": json.Number("1")}}},
		},
		materialBase: map[int64]map[string]interface{}{20215: {"Name": "上古陨石"}},
	})
	ss := newSession()
	ss.playerID, ss.coin = 7, 500
	ss.bag = map[int32]*bagItem{1: newBagItem(equipID)}
	resp := (&Server{}).onStrengthEquip(
		&channel{id: 1, session: ss},
		&protocol.C2M_StrengthEquip{RpcId: 10, BagIndex: 1, PlusItemIndex: -1},
	).(*protocol.M2C_StrengthEquip)
	if resp.Error != 0 || !strings.Contains(resp.Message, "上古陨石") {
		t.Fatalf("strength reject = %+v, want Error=0 material message", resp)
	}
	if ss.bag[1].Level != 1 || ss.coin != 500 {
		t.Fatalf("rejected strength mutated state: level=%d coin=%d", ss.bag[1].Level, ss.coin)
	}
}

func TestMakeManualEquipConsumesMaterialsAndCreatesRandomAttributes(t *testing.T) {
	const (
		equipID int32 = 120877
		stoneID int32 = 20242
	)
	withCraftingTables(t, &datatables{
		equipBase: map[int64]map[string]interface{}{
			int64(equipID): {"SpecialKey": json.Number("1"), "Quality": json.Number("4"), "Star": json.Number("10")},
		},
		manulEquip: map[int64]map[string]interface{}{
			7: {"EquipId": json.Number("120877"), "MaterialArr": []interface{}{map[string]interface{}{"MaterialId": json.Number("20242"), "Count": json.Number("2")}}},
		},
		manulEquipAttribute: map[int64]map[string]interface{}{
			101: {"Key": json.Number("1"), "Value": json.Number("75800")},
			102: {"Key": json.Number("2"), "Value": json.Number("6822")},
		},
		materialBase: map[int64]map[string]interface{}{int64(stoneID): {"Name": "红色彩石"}},
	})
	ss := newSession()
	ss.playerID, ss.name = 7, "手工匠人"
	ss.bag = map[int32]*bagItem{1: {ItemId: stoneID, ItemType: 3, Count: 2}}
	resp := (&Server{}).onMakeMunalEquip(
		&channel{id: 1, session: ss},
		&protocol.C2M_MakeMunalEquip{RpcId: 11, Type: 0, IsRare: protocol.StoneType_Nomal},
	).(*protocol.M2C_MakeMunalEquip)
	if resp.Error != 0 || resp.Message != "" {
		t.Fatalf("manual response = %+v", resp)
	}
	if ss.bag[0] == nil || ss.bag[0].ItemId != equipID || len(ss.bag[0].RandomAttrs) != 1 {
		t.Fatalf("manual output bag = %#v", ss.bag)
	}
	if ss.bag[0].GetSource != ss.name {
		t.Fatalf("manual equip source = %q, want maker %q", ss.bag[0].GetSource, ss.name)
	}
}

func TestNetItemEncodesClientGetSource(t *testing.T) {
	raw := encodeNetItem(&bagItem{ItemId: 120877, ItemType: 1, Count: 1, GetSource: "手工测试"})
	var item protocol.NetItem
	if err := proto.Unmarshal(raw, &item); err != nil {
		t.Fatal(err)
	}
	if item.GetSource != "手工测试" {
		t.Fatalf("GetSource = %q", item.GetSource)
	}
}

func TestManualEquipClientTypeMapping(t *testing.T) {
	tests := []struct {
		raw  int32
		want int64
		ok   bool
	}{
		{raw: 0, want: 7, ok: true},
		{raw: 1, want: 3, ok: true},
		{raw: 2, want: 6, ok: true},
		{raw: 3, want: 5, ok: true},
		{raw: 5, want: 5, ok: true},
		{raw: 6, want: 6, ok: true},
		{raw: 7, want: 7, ok: true},
		{raw: 4, ok: false},
		{raw: 8, ok: false},
	}
	for _, tt := range tests {
		got, ok := manualEquipConfigID(tt.raw)
		if got != tt.want || ok != tt.ok {
			t.Errorf("manualEquipConfigID(%d) = (%d, %v), want (%d, %v)", tt.raw, got, ok, tt.want, tt.ok)
		}
	}
}

func TestRefreshEquipUpdatesMainAndAffixFields(t *testing.T) {
	const equipID int32 = 120001
	withCraftingTables(t, &datatables{
		equipBase: map[int64]map[string]interface{}{
			int64(equipID): {
				"SpecialKey": json.Number("7"), "SpecialValue": json.Number("100"),
				"Quality": json.Number("4"), "Star": json.Number("8"), "PhyAtk": json.Number("200"),
			},
		},
		equipAffix: map[int64]map[string]interface{}{
			110001: {"AffixArr": []interface{}{map[string]interface{}{"Key": json.Number("1"), "Value": json.Number("5400")}}},
			210001: {"AffixArr": []interface{}{map[string]interface{}{"Key": json.Number("2"), "Value": json.Number("3000")}}},
		},
	})
	ss := newSession()
	ss.playerID, ss.jobID, ss.level = 7, 1, 1
	ss.bag = map[int32]*bagItem{1: newBagItem(equipID)}
	ss.worn = make(map[int32]*bagItem)
	server := &Server{}
	mainResp := server.onRefreshEquipMainAttribute(
		&channel{id: 1, session: ss}, &protocol.C2M_RefreshEquipMainAttribute{RpcId: 12, BagIndex: 1},
	).(*protocol.M2C_RefreshEquipMainAttribute)
	delta, ok := ss.bag[1].MainAttr[7]
	if mainResp.Error != 0 || mainResp.Message != "" || !ok || delta < -0.2 || delta > 0.2 {
		t.Fatalf("main refresh response/item = %+v/%#v", mainResp, ss.bag[1])
	}
	affixResp := server.onRefreshEquipAffix(
		&channel{id: 1, session: ss}, &protocol.C2M_RefreshEquipAffix{RpcId: 13, BagIndex: 1},
	).(*protocol.M2C_RefreshEquipAffix)
	if affixResp.Error != 0 || affixResp.Message != "" || len(ss.bag[1].AddAttrs) != 2 {
		t.Fatalf("affix refresh response/item = %+v/%#v", affixResp, ss.bag[1])
	}
}

func TestMainAttributeBonusMatchesClientPercentageFormula(t *testing.T) {
	const equipID int32 = 120001
	withCraftingTables(t, &datatables{equipBase: map[int64]map[string]interface{}{
		int64(equipID): {
			"PhyAtk": json.Number("200"), "SpecialKey": json.Number("1"),
			"SpecialValue": json.Number("100"), "Quality": json.Number("4"), "Star": json.Number("8"),
		},
	}})
	it := newBagItem(equipID)
	it.MainAttr = map[int32]float32{7: 0.15}
	ss := newSession()
	ss.worn = map[int32]*bagItem{0: it}
	if got := equipBonus(ss)[1009]; math.Abs(float64(got-230)) > 0.001 {
		t.Fatalf("physical attack bonus = %v, want base 200 * (1 + 0.15) = 230", got)
	}
}
