package main

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"mhqserver/internal/operationsconfig"
	"mhqserver/protocol"
)

func withCraftingTables(t *testing.T, value *datatables) {
	t.Helper()
	old := tables
	t.Cleanup(func() { tables = old })
	tables = value
}

func TestEquipCultivationFieldsCloneAndEncode(t *testing.T) {
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

	roundTrip := cloneBag(map[int32]*bagItem{3: it})[3]
	if roundTrip == nil || !reflect.DeepEqual(roundTrip.MainAttr, it.MainAttr) ||
		!reflect.DeepEqual(roundTrip.RandomAttrs, it.RandomAttrs) ||
		!reflect.DeepEqual(roundTrip.AddAttrs, it.AddAttrs) || roundTrip.GetSource != it.GetSource {
		t.Fatalf("cultivation clone = %#v, want %#v", roundTrip, it)
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
	if ss.bag[1].Level != 1 || ss.bag[2] != nil || ss.coin != 400 {
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
	if ss.bag[1].Level != 0 || ss.coin != 500 {
		t.Fatalf("rejected strength mutated state: level=%d coin=%d", ss.bag[1].Level, ss.coin)
	}
}

func TestStrengthEquipFailureRespectsConfiguredSafetyLevels(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.Equipment.StrengthSafetyLevels = []int32{8, 13, 17}
	})
	const (
		equipID    int32 = 120001
		materialID int32 = 20215
	)
	withCraftingTables(t, &datatables{
		equipBase: map[int64]map[string]interface{}{
			int64(equipID): {"SpecialKey": json.Number("7"), "Quality": json.Number("4"), "Star": json.Number("8")},
		},
		strengthen: map[int64]map[string]interface{}{
			8:  {"Probability": json.Number("0"), "FailLevel": json.Number("1"), "NeedCoin": json.Number("0")},
			9:  {"Probability": json.Number("0"), "FailLevel": json.Number("1"), "NeedCoin": json.Number("0")},
			11: {"Probability": json.Number("0"), "FailLevel": json.Number("8"), "NeedCoin": json.Number("0")},
			13: {"Probability": json.Number("0"), "FailLevel": json.Number("8"), "NeedCoin": json.Number("0")},
			14: {"Probability": json.Number("0"), "FailLevel": json.Number("8"), "NeedCoin": json.Number("0")},
			17: {"Probability": json.Number("0"), "FailLevel": json.Number("13"), "NeedCoin": json.Number("0")},
			18: {"Probability": json.Number("0"), "FailLevel": json.Number("13"), "NeedCoin": json.Number("0")},
		},
		strengthPlus: map[int64]map[string]interface{}{},
		materialBase: map[int64]map[string]interface{}{int64(materialID): {"Name": "强化材料"}},
	})
	for _, test := range []struct {
		level int32
		want  int32
	}{
		{8, 8},
		{9, 8},
		{11, 10},
		{13, 13},
		{14, 13},
		{17, 17},
		{18, 17},
	} {
		ss := newSession()
		ss.playerID, ss.coin = int64(test.level), 1
		ss.bag = map[int32]*bagItem{
			1: func() *bagItem { it := newBagItem(equipID); it.Level = test.level; return it }(),
		}
		resp := (&Server{}).onStrengthEquip(
			&channel{id: int64(test.level), session: ss},
			&protocol.C2M_StrengthEquip{BagIndex: 1, PlusItemIndex: -1},
		).(*protocol.M2C_StrengthEquip)
		if resp.Error != 0 || resp.IsSuccess || ss.bag[1].Level != test.want {
			t.Fatalf("level %d failure result = response=%+v itemLevel=%d, want %d", test.level, resp, ss.bag[1].Level, test.want)
		}
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
		manulEquipAttribute: manualAttributePoolFixture(12),
		equipAffix: map[int64]map[string]interface{}{
			110001: {"AffixArr": []interface{}{map[string]interface{}{"Key": json.Number("1"), "Value": json.Number("100")}}},
			210001: {"AffixArr": []interface{}{map[string]interface{}{"Key": json.Number("2"), "Value": json.Number("100")}}},
			310001: {"AffixArr": []interface{}{map[string]interface{}{"Key": json.Number("3"), "Value": json.Number("100")}}},
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
	got := ss.bag[0]
	if got == nil || got.ItemId != equipID || !got.IsLock || got.GetSource != ss.name {
		t.Fatalf("manual output bag = %#v", ss.bag)
	}
	if got.Star < 3 || got.Star > 10 || got.Quality < 1 || got.Quality > 4 {
		t.Fatalf("manual quality/star = %d/%d", got.Quality, got.Star)
	}
	// 随机词条数 = 星级本身（几星几条），模板自带的固定行（120877 的 SpecialKey）
	// 不占名额；出厂不带洗练词缀，只有玩家主动洗练才会有。
	if wantCount := int(got.Star); len(got.RandomAttrs) != wantCount {
		t.Fatalf("manual random attrs = %v star=%d, want %d", got.RandomAttrs, got.Star, wantCount)
	}
	if len(got.AddAttrs) != 0 {
		t.Fatalf("manual affix attrs = %v, want empty until the player washes", got.AddAttrs)
	}
	for _, id := range got.RandomAttrs {
		if id/100 != got.Quality {
			t.Fatalf("manual random attrs = %v quality=%d", got.RandomAttrs, got.Quality)
		}
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

// withAffixWashConfigForTest 替换洗词缀专用配置；每个用例都显式关掉用不到的概率，
// 让断言只依赖自己关心的那条分支。
func withAffixWashConfigForTest(t *testing.T, mutate func(*operationsconfig.AffixWashConfig)) {
	t.Helper()
	previous := affixWashConfigSnapshot()
	config := operationsconfig.DefaultAffixWash()
	mutate(&config)
	if err := operationsconfig.ValidateAffixWash(config); err != nil {
		t.Fatalf("invalid test AffixWash config: %v", err)
	}
	storeAffixWashConfig(config)
	t.Cleanup(func() { storeAffixWashConfig(previous) })
}

// affixRow 构造一行 EquipAffixConfig 测试数据（Key=AttributeType，Value=该档数值）。
func affixRow(attributes ...[2]int32) map[string]interface{} {
	arr := make([]interface{}, 0, len(attributes))
	for _, attribute := range attributes {
		arr = append(arr, map[string]interface{}{
			"Key":   json.Number(fmt.Sprint(attribute[0])),
			"Value": json.Number(fmt.Sprint(attribute[1])),
		})
	}
	return map[string]interface{}{"AffixArr": arr}
}

// affixTiers 为同一个词条生成 5 个档位，id 规则与线上一致：词条编号*100000 + 档位*1000。
func affixTiers(family int32, tierValues ...[2]int32) map[int64]map[string]interface{} {
	rows := make(map[int64]map[string]interface{}, len(tierValues))
	for index, attribute := range tierValues {
		id := int64(family)*100000 + int64(index+1)*1000
		rows[id] = affixRow(attribute)
	}
	return rows
}

// affixTestTables 构造一份可用的 EquipAffixConfig 测试表，外加指定装备的 EquipBase 行。
// 覆盖三类词条：
//
//	1 最大生命 / 2 最大精力 —— 普通单属性，留在普通池里
//	3 力量                  —— 六维单属性
//	34 力量+敏捷+智慧        —— 三属性六维，最高档就是线上的 510/255/255
func affixTestTables(t *testing.T, equipID int32, quality int32) *datatables {
	t.Helper()
	equipAffix := make(map[int64]map[string]interface{})
	add := func(rows map[int64]map[string]interface{}) {
		for id, row := range rows {
			equipAffix[id] = row
		}
	}
	add(affixTiers(1, [2]int32{1, 5400}, [2]int32{1, 7000}, [2]int32{1, 14400}, [2]int32{1, 21600}, [2]int32{1, 30600}))
	add(affixTiers(2, [2]int32{2, 3000}, [2]int32{2, 3600}, [2]int32{2, 4800}, [2]int32{2, 6000}, [2]int32{2, 7200}))
	add(affixTiers(3, [2]int32{3, 9}, [2]int32{3, 15}, [2]int32{3, 24}, [2]int32{3, 36}, [2]int32{3, 51}))
	add(affixTiers(34, [2]int32{3, 90}, [2]int32{3, 150}, [2]int32{3, 240}, [2]int32{3, 360}, [2]int32{3, 510}))
	return &datatables{
		equipBase: map[int64]map[string]interface{}{
			int64(equipID): {"Quality": json.Number(fmt.Sprint(quality)), "Star": json.Number("8")},
		},
		equipAffix: equipAffix,
	}
}

// affixTestSession 构造一个已登录、带足够金币的会话，背包 1 号位放一件装备。
func affixTestSession(t *testing.T, equipID int32, coin int64) (*session, *Server) {
	t.Helper()
	ss := newSession()
	ss.playerID, ss.jobID, ss.level = 7, 1, 1
	ss.coin = coin
	ss.bag = map[int32]*bagItem{1: newBagItem(equipID)}
	ss.worn = make(map[int32]*bagItem)
	return ss, &Server{}
}

func TestRefreshEquipUpdatesMainAndAffixFields(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.Equipment.MinimumMainAttributePercent = 12
		config.Equipment.MaximumMainAttributePercent = 12
	})
	// 本用例断言洗练必然产出普通词缀，三条概率全部归零，否则结果随机。
	withAffixWashConfigForTest(t, func(config *operationsconfig.AffixWashConfig) {
		config.Affix.EmptyPercent = 0
		config.Affix.SixDimensionPercent = 0
		config.Affix.SixDimensionFullPercent = 0
	})
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
	ss, server := affixTestSession(t, equipID, 100000000)
	mainResp := server.onRefreshEquipMainAttribute(
		&channel{id: 1, session: ss}, &protocol.C2M_RefreshEquipMainAttribute{RpcId: 12, BagIndex: 1},
	).(*protocol.M2C_RefreshEquipMainAttribute)
	delta, ok := ss.bag[1].MainAttr[7]
	if mainResp.Error != 0 || mainResp.Message != "" || !ok || math.Abs(float64(delta-0.12)) > 0.0001 {
		t.Fatalf("main refresh response/item = %+v/%#v", mainResp, ss.bag[1])
	}
	affixResp := server.onRefreshEquipAffix(
		&channel{id: 1, session: ss}, &protocol.C2M_RefreshEquipAffix{RpcId: 13, BagIndex: 1},
	).(*protocol.M2C_RefreshEquipAffix)
	if affixResp.Error != 0 || affixResp.Message != "" || len(ss.bag[1].AddAttrs) != 2 {
		t.Fatalf("affix refresh response/item = %+v/%#v", affixResp, ss.bag[1])
	}
}

// 洗词缀落空（affix.empty_percent 命中）是合法结果，不是配置故障：必须照常返回成功、
// 下发空词缀让客户端 RefreshEquipUI 显示“无词缀”，照常扣费，而且登录修复不能把这份
// “干净”的装备再补回词缀。
func TestRefreshEquipAffixEmptyKeepsNoAffix(t *testing.T) {
	withAffixWashConfigForTest(t, func(config *operationsconfig.AffixWashConfig) {
		config.Affix.EmptyPercent = 100
		config.Affix.SixDimensionPercent = 0
		config.Affix.SixDimensionFullPercent = 0
	})
	const equipID int32 = 120001
	withCraftingTables(t, &datatables{
		equipBase: map[int64]map[string]interface{}{
			int64(equipID): {"Quality": json.Number("6"), "Star": json.Number("8")},
		},
		equipAffix: map[int64]map[string]interface{}{
			110001: {"AffixArr": []interface{}{map[string]interface{}{"Key": json.Number("1"), "Value": json.Number("5400")}}},
			210001: {"AffixArr": []interface{}{map[string]interface{}{"Key": json.Number("2"), "Value": json.Number("3000")}}},
		},
	})
	ss, server := affixTestSession(t, equipID, affixWashCostCoin*4)
	it := ss.bag[1]
	it.AddAttrs = []int32{110001}
	resp := server.onRefreshEquipAffix(
		&channel{id: 1, session: ss}, &protocol.C2M_RefreshEquipAffix{RpcId: 13, BagIndex: 1},
	).(*protocol.M2C_RefreshEquipAffix)
	if resp.Error != 0 || resp.Message != "" {
		t.Fatalf("empty affix wash response = %+v, want a silent success", resp)
	}
	if len(it.AddAttrs) != 0 {
		t.Fatalf("empty affix wash attrs = %v, want none", it.AddAttrs)
	}
	if want := affixWashCostCoin * 3; ss.coin != want {
		t.Fatalf("coin after empty wash = %d, want %d (落空也要照常扣费)", ss.coin, want)
	}
	ensureEquipmentVariations(ss.bag)
	if len(it.AddAttrs) != 0 {
		t.Fatalf("login repair refilled an intentionally empty affix list: %v", it.AddAttrs)
	}
}

// 洗词缀固定 1000 金币（= 10,000,000 个 coin 单位）；金币不足必须拒绝且不扣费，
// 洗练本身不返还材料，落空与六维结果都照常收费。
func TestRefreshEquipAffixChargesThousandGold(t *testing.T) {
	withAffixWashConfigForTest(t, func(config *operationsconfig.AffixWashConfig) {
		config.Affix.EmptyPercent = 0
		config.Affix.SixDimensionPercent = 0
		config.Affix.SixDimensionFullPercent = 0
	})
	const equipID int32 = 120001
	withCraftingTables(t, affixTestTables(t, equipID, 6))
	ss, server := affixTestSession(t, equipID, affixWashCostCoin-1)
	resp := server.onRefreshEquipAffix(
		&channel{id: 1, session: ss}, &protocol.C2M_RefreshEquipAffix{RpcId: 13, BagIndex: 1},
	).(*protocol.M2C_RefreshEquipAffix)
	if resp.Error != 0 || resp.Message != "金币不足" {
		t.Fatalf("insufficient coin response = %+v, want 金币不足", resp)
	}
	if ss.coin != affixWashCostCoin-1 {
		t.Fatalf("coin changed on a rejected wash: %d", ss.coin)
	}
	if len(ss.bag[1].AddAttrs) != 0 {
		t.Fatalf("rejected wash still rolled affixes: %v", ss.bag[1].AddAttrs)
	}
	ss.coin = 2 * affixWashCostCoin
	ok := server.onRefreshEquipAffix(
		&channel{id: 1, session: ss}, &protocol.C2M_RefreshEquipAffix{RpcId: 14, BagIndex: 1},
	).(*protocol.M2C_RefreshEquipAffix)
	if ok.Error != 0 || ok.Message != "" {
		t.Fatalf("paid wash response = %+v", ok)
	}
	if want := affixWashCostCoin; ss.coin != want {
		t.Fatalf("coin after paid wash = %d, want %d", ss.coin, want)
	}
	if len(ss.bag[1].AddAttrs) == 0 {
		t.Fatal("paid wash produced no affixes with every probability disabled")
	}
}

// 六维是独立分支：命中 six_dimension_percent 时本次只给 1 条六维词条，
// 命中 six_dimension_full_percent 时给三属性六维的最高档 510/255/255。
func TestRefreshEquipAffixSixDimensionRules(t *testing.T) {
	const equipID int32 = 120003
	tables := affixTestTables(t, equipID, 6)
	// 三属性六维的最高档（词条 34 的档5），即 510/255/255。
	tables.equipAffix[3405000] = affixRow([2]int32{3, 510}, [2]int32{4, 255}, [2]int32{6, 255})
	withCraftingTables(t, tables)
	ss, server := affixTestSession(t, equipID, affixWashCostCoin*2)

	// six_dimension_full_percent=100：必定满六维。
	withAffixWashConfigForTest(t, func(config *operationsconfig.AffixWashConfig) {
		config.Affix.EmptyPercent = 0
		config.Affix.SixDimensionPercent = 0
		config.Affix.SixDimensionFullPercent = 100
	})
	resp := server.onRefreshEquipAffix(
		&channel{id: 1, session: ss}, &protocol.C2M_RefreshEquipAffix{RpcId: 20, BagIndex: 1},
	).(*protocol.M2C_RefreshEquipAffix)
	if resp.Error != 0 || resp.Message != "" {
		t.Fatalf("full six-dimension wash response = %+v", resp)
	}
	if got := ss.bag[1].AddAttrs; len(got) != 1 || got[0] != 3405000 {
		t.Fatalf("full six-dimension attrs = %v, want [3405000] (510/255/255)", got)
	}

	// six_dimension_percent=100：必定六维，档位取配置的 6红→档4。
	withAffixWashConfigForTest(t, func(config *operationsconfig.AffixWashConfig) {
		config.Affix.EmptyPercent = 0
		config.Affix.SixDimensionPercent = 100
		config.Affix.SixDimensionFullPercent = 0
	})
	resp = server.onRefreshEquipAffix(
		&channel{id: 1, session: ss}, &protocol.C2M_RefreshEquipAffix{RpcId: 21, BagIndex: 1},
	).(*protocol.M2C_RefreshEquipAffix)
	if resp.Error != 0 || resp.Message != "" {
		t.Fatalf("six-dimension wash response = %+v", resp)
	}
	// 六维池里同时有单属性（词条 3）和三属性（词条 34）两种，同一档随机取一条，
	// 所以只断言“必定是六维、只给 1 条、档位等于配置里的 6红→档4”。
	washed := ss.bag[1].AddAttrs
	if len(washed) != 1 || !affixIsSixDimension(washed[0]) {
		t.Fatalf("six-dimension attrs = %v, want exactly one six-dimension affix", washed)
	}
	if tier := washed[0] / 1000 % 100; tier != 4 {
		t.Fatalf("six-dimension tier = %d (id %d), want 4 (红装配置档)", tier, washed[0])
	}

	// 六维词条不得出现在普通池里，也不能被登录修复补成 1/2/3 条。
	ensureEquipmentVariations(ss.bag)
	if got := ss.bag[1].AddAttrs; len(got) != 1 || got[0] != washed[0] {
		t.Fatalf("login repair changed a six-dimension wash result: %v -> %v", washed, got)
	}
}

func TestAffixPercentRolledHonoursConfiguredPercent(t *testing.T) {
	if affixPercentRolled(0, func() float64 { return 0 }) {
		t.Fatal("percent=0 must never roll")
	}
	if !affixPercentRolled(100, func() float64 { return 0.999999 }) {
		t.Fatal("percent=100 must always roll")
	}
	if !affixPercentRolled(20, func() float64 { return 0.1999 }) {
		t.Fatal("percent=20 must catch a roll under the threshold")
	}
	if affixPercentRolled(20, func() float64 { return 0.2 }) {
		t.Fatal("percent=20 must not catch a roll at the threshold")
	}
	if !affixPercentRolled(0.1, func() float64 { return 0.000999 }) {
		t.Fatal("percent=0.1 must still catch a roll under one thousandth")
	}
	if affixPercentRolled(0.1, func() float64 { return 0.001 }) {
		t.Fatal("percent=0.1 must not catch a roll at the threshold")
	}
}

// 手工产物的 EquipBase 只有 SpecialKey/SpecialValue、没有属性列，客户端洗练面板
// 拿属性列当基础值显示（GetValueFromConfig 无 SpecialKey 回退），列是空的就恒显示 0。
// 所以手工产物不给可洗练主属性；非手工的 SpecialKey-only 装备（称号等）保持原行为。
func TestRefreshEquipMainAttributeSkipsManualProducts(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.Equipment.MinimumMainAttributePercent = 10
		config.Equipment.MaximumMainAttributePercent = 10
	})
	withCraftingTables(t, &datatables{
		manulEquip: map[int64]map[string]interface{}{
			7: {"EquipId": json.Number("120877")},
		},
		equipBase: map[int64]map[string]interface{}{
			120877: {"SpecialKey": json.Number("1"), "SpecialValue": json.Number("20000")},
			120889: {"SpecialKey": json.Number("1"), "SpecialValue": json.Number("5000")},
		},
	})
	ss := newSession()
	ss.playerID, ss.jobID, ss.level = 7, 1, 1
	ss.bag = map[int32]*bagItem{1: newBagItem(120877), 2: newBagItem(120889)}
	ss.worn = make(map[int32]*bagItem)
	server := &Server{}
	manualResp := server.onRefreshEquipMainAttribute(
		&channel{id: 1, session: ss}, &protocol.C2M_RefreshEquipMainAttribute{RpcId: 12, BagIndex: 1},
	).(*protocol.M2C_RefreshEquipMainAttribute)
	if manualResp.Message != "该装备没有可洗练的主属性" || len(ss.bag[1].MainAttr) != 0 {
		t.Fatalf("manual main refresh = %+v / %#v", manualResp, ss.bag[1].MainAttr)
	}
	titleResp := server.onRefreshEquipMainAttribute(
		&channel{id: 1, session: ss}, &protocol.C2M_RefreshEquipMainAttribute{RpcId: 13, BagIndex: 2},
	).(*protocol.M2C_RefreshEquipMainAttribute)
	if titleResp.Message != "" || math.Abs(float64(ss.bag[2].MainAttr[1]-0.1)) > 0.0001 {
		t.Fatalf("title main refresh = %+v / %#v", titleResp, ss.bag[2].MainAttr)
	}
}

func TestEquipmentBonusCountFollowsQualityColor(t *testing.T) {
	tests := []struct {
		quality int32
		want    int
	}{
		{1, 1}, {2, 1}, {3, 2}, {4, 2}, {5, 3}, {6, 3},
	}
	for _, tt := range tests {
		if got := equipmentBonusCount(tt.quality); got != tt.want {
			t.Fatalf("equipmentBonusCount(%d) = %d, want %d", tt.quality, got, tt.want)
		}
	}
}

func TestEquipmentAffixCountFollowsQualityColor(t *testing.T) {
	const equipID int32 = 120009
	affixes := make(map[int64]map[string]interface{}, 10)
	for family := int32(1); family <= 10; family++ {
		affixID := int64(family)*100000 + 4000
		affixes[affixID] = map[string]interface{}{
			"AffixArr": []interface{}{map[string]interface{}{
				"Key": json.Number("1"), "Value": json.Number("100"),
			}},
		}
	}
	withCraftingTables(t, &datatables{
		equipBase: map[int64]map[string]interface{}{int64(equipID): {
			"Quality": json.Number("4"), "Star": json.Number("8"),
		}},
		equipAffix: affixes,
	})

	item := newBagItem(equipID)
	if item.Star != 8 || item.Level != 0 || len(item.AddAttrs) != 0 {
		t.Fatalf("new purple equipment defaults star=%d level=%d affixes=%v, want star 8 level 0 and no affixes", item.Star, item.Level, item.AddAttrs)
	}
	item.AddAttrs = []int32{104000}
	if !ensureEquipmentAffixCount(item) || len(item.AddAttrs) != 2 {
		t.Fatalf("legacy purple equipment affixes=%v, want repaired 2", item.AddAttrs)
	}
	starBased := append([]int32(nil), item.AddAttrs...)
	for family := int32(3); family <= 10; family++ {
		starBased = append(starBased, family*100000+4000)
	}
	item.AddAttrs = starBased
	if !ensureEquipmentAffixCount(item) || len(item.AddAttrs) != 2 {
		t.Fatalf("star-padded purple equipment affixes=%v, want trimmed 2", item.AddAttrs)
	}
	item.AddAttrs = append(item.AddAttrs, 9999999)
	if !ensureEquipmentAffixCount(item) || len(item.AddAttrs) != 2 {
		t.Fatalf("invalid/excess purple equipment affixes=%v, want normalized 2", item.AddAttrs)
	}
}

// 线上 EquipAffixConfig 一共 37 个词条：其中 16 个含六维属性，21 个不含。
// 普通池与六维池必须互斥——六维只能由洗词缀的独立概率产出，否则“六维占 2%”
// 会被普通抽取的槽位数放大。
func TestOnlineCombinationAffixesCanBeRolledAtTheirQualityTier(t *testing.T) {
	loadOnlineTablesForTest(t)
	ids := rollAffixIDsWithShuffle(6, func(length int, swap func(int, int)) {
		for left, right := 0, length-1; left < right; left, right = left+1, right-1 {
			swap(left, right)
		}
	})
	// 品质 6 取最后一档，倒序洗牌后普通池里词条编号最大的三个（33/32/31）。
	want := []int32{3336000, 3236000, 3136000}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("quality-6 reversed affix roll = %v, want %v", ids, want)
	}
	for _, id := range ids {
		if got := len(arrOf(tables.equipAffix[int64(id)]["AffixArr"])); got != 2 {
			t.Fatalf("rolled affix %d has %d attributes, want 2", id, got)
		}
		if affixIsSixDimension(id) {
			t.Fatalf("normal pool rolled a six-dimension affix %d", id)
		}
	}
	pool := affixPoolForQuality(6)
	if len(pool) != 21 {
		t.Fatalf("quality-6 normal affix families = %d, want 21", len(pool))
	}
	for _, candidate := range pool {
		if candidate.sixDimension {
			t.Fatalf("normal pool contains six-dimension family %d", candidate.family)
		}
	}
	if pool[17].id != 2426000 || len(arrOf(tables.equipAffix[int64(pool[17].id)]["AffixArr"])) != 2 {
		t.Fatalf("first combination affix = %+v, want two-attribute family 24 tier 6", pool[17])
	}

	sixPool := affixSixDimensionPool(6, operationsconfig.DefaultAffixWash())
	if len(sixPool) != 16 {
		t.Fatalf("six-dimension families = %d, want 16", len(sixPool))
	}
	for _, candidate := range sixPool {
		if !candidate.sixDimension || !affixIsSixDimension(candidate.id) {
			t.Fatalf("six-dimension pool contains a non-six-dimension candidate %+v", candidate)
		}
	}

	// 满六维只取三属性六维的最高档，也就是线上的 510/255/255。
	fullPool := affixFullSixDimensionPool()
	if len(fullPool) != 4 {
		t.Fatalf("full six-dimension families = %d, want 4", len(fullPool))
	}
	for _, candidate := range fullPool {
		if candidate.attributeCount != affixFullSixDimensionAttributeCount {
			t.Fatalf("full six-dimension candidate %+v does not carry 3 attributes", candidate)
		}
		attributes := arrOf(tables.equipAffix[int64(candidate.id)]["AffixArr"])
		values := make([]int32, 0, len(attributes))
		for _, raw := range attributes {
			affix, _ := raw.(map[string]interface{})
			values = append(values, int32(num(affix["Value"])))
		}
		if !reflect.DeepEqual(values, []int32{510, 255, 255}) {
			t.Fatalf("full six-dimension affix %d values = %v, want [510 255 255]", candidate.id, values)
		}
	}
}

func TestCombinationAffixAppliesEveryConfiguredAttribute(t *testing.T) {
	const equipID int32 = 120001
	withCraftingTables(t, &datatables{
		equipBase: map[int64]map[string]interface{}{
			int64(equipID): {"Quality": json.Number("6")},
		},
		equipAffix: map[int64]map[string]interface{}{
			2426000: {"AffixArr": []interface{}{
				map[string]interface{}{"Key": json.Number("1"), "Value": json.Number("61200")},
				map[string]interface{}{"Key": json.Number("2"), "Value": json.Number("34000")},
			}},
			3436000: {"AffixArr": []interface{}{
				map[string]interface{}{"Key": json.Number("3"), "Value": json.Number("510")},
				map[string]interface{}{"Key": json.Number("4"), "Value": json.Number("255")},
				map[string]interface{}{"Key": json.Number("6"), "Value": json.Number("255")},
			}},
		},
	})
	it := newBagItem(equipID)
	it.AddAttrs = []int32{2426000, 3436000}
	ss := newSession()
	ss.worn = map[int32]*bagItem{0: it}
	bonus := equipBonus(ss)
	for numericType, want := range map[int32]float32{
		1002: 61200, 1004: 34000, 1005: 510, 1006: 255, 1008: 255,
	} {
		if bonus[numericType] != want {
			t.Errorf("numeric %d combination bonus = %v, want %v", numericType, bonus[numericType], want)
		}
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

func TestEquipmentVariationIsCreatedWithinRangeAndNotRerolled(t *testing.T) {
	const equipID int32 = 120001
	withCraftingTables(t, &datatables{equipBase: map[int64]map[string]interface{}{
		int64(equipID): {
			"PhyAtk": json.Number("200"), "Quality": json.Number("4"), "Star": json.Number("8"),
		},
	}})
	it := &bagItem{ItemId: equipID, ItemType: 1, Count: 1, Level: 1}
	if !ensureEquipmentVariation(it) {
		t.Fatal("legacy equipment did not receive an instance variation")
	}
	first := it.MainAttr[7]
	if first < -0.25 || first > 0.25 {
		t.Fatalf("equipment variation = %v, want -0.25..0.25", first)
	}
	if ensureEquipmentVariation(it) {
		t.Fatal("existing equipment variation was rerolled")
	}
	if it.MainAttr[7] != first {
		t.Fatalf("equipment variation changed from %v to %v", first, it.MainAttr[7])
	}
}
