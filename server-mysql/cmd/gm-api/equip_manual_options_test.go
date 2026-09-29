package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"mhqserver/internal/mysqlschema"
)

// readConfig 对 datatable 的返回形状：[]any{ []any{int64(下标), map[string]any{字段}} }。
// 这里手工造同样形状的配置，直接喂给纯函数 buildEquipManualOptions。
func optionsRow(index int64, row map[string]any) []any {
	return []any{index, row}
}

func optionsConfigRaw() map[string]any {
	return map[string]any{
		"ManulEquipAttribute": []any{
			optionsRow(101, map[string]any{"Key": int64(1), "Value": 75800.0}),
			optionsRow(103, map[string]any{"Key": int64(3), "Value": 126.0}),
			optionsRow(301, map[string]any{"Key": int64(1), "Value": 400000.0}),
			// Key<=0 不是可用属性，必须被跳过
			optionsRow(302, map[string]any{"Key": int64(0), "Value": 1.0}),
			// 档位越界（品质 9）
			optionsRow(901, map[string]any{"Key": int64(1), "Value": 1.0}),
		},
		"EquipAffixConfig": []any{
			// 家族 10：三档，品质 3 应取第三档 1000003
			optionsRow(1000001, map[string]any{"AffixArr": []any{map[string]any{"Key": int64(1), "Value": 5400.0}}}),
			optionsRow(1000002, map[string]any{"AffixArr": []any{map[string]any{"Key": int64(1), "Value": 7000.0}}}),
			optionsRow(1000003, map[string]any{"AffixArr": []any{map[string]any{"Key": int64(1), "Value": 14400.0}}}),
			// 家族 11：双属性
			optionsRow(1100001, map[string]any{"AffixArr": []any{
				map[string]any{"Key": int64(7), "Value": 30.0},
				map[string]any{"Key": int64(8), "Value": 30.0},
			}}),
			// 家族 20：六维（含 Key=3 力量），不参与普通池
			optionsRow(2000001, map[string]any{"AffixArr": []any{map[string]any{"Key": int64(3), "Value": 18.0}}}),
		},
		"MaterialBase": []any{
			optionsRow(20046, map[string]any{"Name": "生命之晶·一级", "MaterialType": int64(2), "GemKey": int64(3), "GemType": int64(5), "GemLevel": int64(1)}),
			optionsRow(20101, map[string]any{"Name": "力量石·一级", "MaterialType": int64(2), "GemKey": int64(4), "GemType": int64(5), "GemLevel": int64(1)}),
			optionsRow(20103, map[string]any{"Name": "速度石·一级", "MaterialType": int64(2), "GemKey": int64(7), "GemType": int64(5), "GemLevel": int64(1)}),
			optionsRow(20104, map[string]any{"Name": "魅力石·一级", "MaterialType": int64(2), "GemKey": int64(3), "GemType": int64(6), "GemLevel": int64(1)}),
			// 普通材料，不是宝石
			optionsRow(20215, map[string]any{"Name": "上古陨石", "MaterialType": int64(1)}),
		},
		"GemInlayConfig": []any{
			optionsRow(0, map[string]any{"CanInlayArr": []any{int64(3), int64(4)}}),
			optionsRow(4, map[string]any{"CanInlayArr": []any{int64(1), int64(2)}}),
		},
		"EquipBase": []any{
			// 110001 没有 Type 字段：num(nil)==0，等价于武器部位
			optionsRow(110001, map[string]any{"MaxHole": int64(3), "CanInlayGemTypeArr": []any{int64(5)},
				"Name": "测试武器"}),
			optionsRow(120590, map[string]any{"Type": int64(4), "MaxHole": int64(2), "CanInlayGemTypeArr": []any{int64(5)},
				"Name": "测试称号"}),
		},
	}
}

func optionsData(t *testing.T, raw map[string]any, itemID int32) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(buildEquipManualOptions(raw, itemID))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestEquipManualOptionsRandomAttrsByQuality(t *testing.T) {
	data := optionsData(t, optionsConfigRaw(), 0)
	byQuality, ok := data["randomAttrsByQuality"].(map[string]any)
	if !ok {
		t.Fatalf("randomAttrsByQuality=%T", data["randomAttrsByQuality"])
	}
	if _, ok := byQuality["9"]; ok {
		t.Fatal("out-of-range quality tier must be dropped")
	}
	quality1, ok := byQuality["1"].([]any)
	if !ok || len(quality1) != 2 {
		t.Fatalf("quality 1 entries = %v (Key<=0 and 品质9 must be skipped)", byQuality["1"])
	}
	first := quality1[0].(map[string]any)
	if first["id"] != 101.0 || first["keyName"] != "最大生命" || first["value"] != 75800.0 {
		t.Fatalf("first entry = %v", first)
	}
	if quality1[1].(map[string]any)["keyName"] != "力量" {
		t.Fatalf("second entry = %v", quality1[1])
	}
}

func TestEquipManualOptionsAffixPoolsFollowQualityTier(t *testing.T) {
	data := optionsData(t, optionsConfigRaw(), 0)
	pools := data["affixPools"].(map[string]any)
	byQuality := pools["byQuality"].(map[string]any)
	// 家族 10 三档、家族 11 一档；品质 3 取家族 10 的第三档、家族 11 收敛到第一档。
	quality3 := byQuality["3"].([]any)
	if len(quality3) != 2 || quality3[0] != 1000003.0 || quality3[1] != 1100001.0 {
		t.Fatalf("quality 3 pool = %v", quality3)
	}
	quality1 := byQuality["1"].([]any)
	if len(quality1) != 2 || quality1[0] != 1000001.0 {
		t.Fatalf("quality 1 pool = %v", quality1)
	}
	// 六维单独成池，且不能出现在普通池里
	six := pools["sixDimension"].([]any)
	if len(six) != 1 || six[0] != 2000001.0 {
		t.Fatalf("sixDimension = %v", six)
	}
	for _, quality := range byQuality {
		for _, id := range quality.([]any) {
			if id == 2000001.0 {
				t.Fatal("six-dimension affix leaked into the normal pool")
			}
		}
	}
	// 六维标记与中文 label
	affixes := data["affixes"].([]any)
	labels := map[float64]string{}
	sixFlags := map[float64]bool{}
	for _, raw := range affixes {
		entry := raw.(map[string]any)
		labels[entry["id"].(float64)] = entry["label"].(string)
		sixFlags[entry["id"].(float64)] = entry["sixDimension"].(bool)
	}
	if labels[2000001] != "力量+18" {
		t.Fatalf("six-dimension label = %q", labels[2000001])
	}
	if labels[1100001] != "物理攻击+30 精神攻击+30" {
		t.Fatalf("two-attribute label = %q", labels[1100001])
	}
	if !sixFlags[2000001] || sixFlags[1000001] {
		t.Fatalf("sixDimension flags = %v", sixFlags)
	}
}

func TestEquipManualOptionsGemsAndNames(t *testing.T) {
	data := optionsData(t, optionsConfigRaw(), 0)
	gems := data["gems"].([]any)
	if len(gems) != 4 {
		t.Fatalf("gems = %v (MaterialType!=2 must be excluded)", gems)
	}
	first := gems[0].(map[string]any)
	if first["id"] != 20046.0 || first["name"] != "生命之晶·一级" || first["gemKeyName"] != "力量" || first["gemTypeName"] != "生命" {
		t.Fatalf("gem = %v", first)
	}
	if got := data["attributeNames"].(map[string]any)["3"]; got != "力量" {
		t.Fatalf("attributeNames[3] = %v", got)
	}
	equipTypes := data["equipTypes"].([]any)
	if len(equipTypes) != 2 {
		t.Fatalf("equipTypes = %v", equipTypes)
	}
	if name := equipTypes[1].(map[string]any)["name"]; name != "称号" {
		t.Fatalf("equipTypes[1].name = %v", name)
	}
	limits := data["limits"].(map[string]any)
	bonus := limits["bonusCountByQuality"].(map[string]any)
	if bonus["1"] != 1.0 || bonus["3"] != 2.0 || bonus["5"] != 3.0 {
		t.Fatalf("bonusCountByQuality = %v", bonus)
	}
}

// EquipBase.Type 缺失 → 0（武器部位）。这个默认值必须原样复刻玩法侧 num(nil)==0，
// 否则面板给出的可镶嵌宝石会跟服务端实际判定不一致。
func TestEquipManualOptionsEquipRuleUsesTypeDefault(t *testing.T) {
	data := optionsData(t, optionsConfigRaw(), 110001)
	rules := data["equipRules"].(map[string]any)
	rule := rules["110001"].(map[string]any)
	if rule["type"] != 0.0 || rule["typeName"] != "武器" {
		t.Fatalf("rule type = %v/%v", rule["type"], rule["typeName"])
	}
	if rule["maxHole"] != 3.0 {
		t.Fatalf("maxHole = %v", rule["maxHole"])
	}
	// GemInlayConfig[0].CanInlayArr = [3,4]，CanInlayGemTypeArr=[5]
	// → 20046(GemKey 3, GemType 5) 与 20101(GemKey 4, GemType 5) 可用；
	//    20103(GemKey 7) 部位不允许，20104(GemType 6) 大类不允许。
	allowed := rule["allowedGemIds"].([]any)
	if len(allowed) != 2 || allowed[0] != 20046.0 || allowed[1] != 20101.0 {
		t.Fatalf("allowedGemIds = %v", allowed)
	}
}

func TestEquipManualOptionsEquipRuleHonoursExplicitType(t *testing.T) {
	data := optionsData(t, optionsConfigRaw(), 120590)
	rule := data["equipRules"].(map[string]any)["120590"].(map[string]any)
	if rule["type"] != 4.0 || rule["typeName"] != "称号" {
		t.Fatalf("rule = %v", rule)
	}
	// GemInlayConfig[4].CanInlayArr = [1,2]，没有任何宝石的 GemKey 落在这两个属性上。
	if allowed := rule["allowedGemIds"].([]any); len(allowed) != 0 {
		t.Fatalf("allowedGemIds = %v, want empty for 称号", allowed)
	}
	if rule["name"] != "测试称号" {
		t.Fatalf("name = %v", rule["name"])
	}
}

func TestEquipManualOptionsOmitsEquipRulesWithoutItemID(t *testing.T) {
	for _, itemID := range []int32{0, 999999} {
		data := optionsData(t, optionsConfigRaw(), itemID)
		if _, ok := data["equipRules"]; ok {
			t.Fatalf("itemId=%d must not produce equipRules", itemID)
		}
	}
}

// 只读集成：走真实已发布配置，确认五张表都能解析、候选池非空。
// 与 items_test.go 一样用环境变量门控，默认跳过。
func TestEquipManualOptionsPublishedMySQL(t *testing.T) {
	if os.Getenv("MHQ_TEST_EQUIP_MANUAL_MYSQL") != "1" {
		t.Skip("set MHQ_TEST_EQUIP_MANUAL_MYSQL=1 to check published local config")
	}
	db, err := sql.Open("mysql", mysqlschema.DefaultDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := &App{db: db, sessions: newMemorySessions()}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/equip-manual/options?itemId=120590", nil)
	session := &sessionData{ID: "equip-manual-live", Roles: []string{"operator"}, ExpiresAt: 1 << 40}
	if err := app.sessions.Put(req.Context(), session); err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
	rr := httptest.NewRecorder()
	app.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Data struct {
			Gems                 []map[string]any  `json:"gems"`
			Affixes              []map[string]any  `json:"affixes"`
			RandomAttrsByQuality map[string]any    `json:"randomAttrsByQuality"`
			AttributeNames       map[string]string `json:"attributeNames"`
			EquipRules           map[string]any    `json:"equipRules"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data.Gems) == 0 || len(body.Data.Affixes) == 0 || len(body.Data.RandomAttrsByQuality) == 0 {
		t.Fatalf("published config produced empty pools: gems=%d affixes=%d random=%d",
			len(body.Data.Gems), len(body.Data.Affixes), len(body.Data.RandomAttrsByQuality))
	}
	if body.Data.AttributeNames["3"] != "力量" {
		t.Fatalf("attributeNames[3] = %q", body.Data.AttributeNames["3"])
	}
	if _, ok := body.Data.EquipRules["120590"]; !ok {
		t.Fatalf("equipRules missing for 120590: %v", body.Data.EquipRules)
	}
}
