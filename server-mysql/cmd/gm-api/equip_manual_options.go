package main

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"mhqserver/internal/equipattributes"
	"mhqserver/internal/itemcatalog"
)

// GET /api/v1/equip-manual/options[?itemId=N] —— 手工装备面板的候选池。
//
// 一次把面板需要的所有清单读出来：随机属性按品质分档、洗练词缀（普通池 + 六维）、
// 宝石、装备部位与各部位允许的宝石属性。带 ?itemId= 时再额外算这一件装备的
// 宝石规则（MaxHole 与允许镶嵌的宝石 id）。
//
// 全部条目都带中文名（统一口径），前端不显示裸 id。
//
// 配置只从 MySQL game_config_nodes 读（a.readConfig），不读解包目录里的 JSON 配置文件，
// 遵守 Server Configuration Boundary（亦有 storage_contract_test.go 兜底）。

// gemTypeNames 是 ET.GemType（文档/14 §4.9）。MaterialBase.GemType 取 2..8，
// 1 希望只有枚举没有宝石物品，仍然列出来让配置异常可见。
var gemTypeNames = map[int32]string{
	1: "希望", 2: "智慧", 3: "玄微", 4: "幻冥", 5: "生命", 6: "魅力", 7: "命运", 8: "天机",
}

// affixSixDimensionKeys 与玩法侧 equip_crafting.go 的 affixSixDimensionKeys 一致：
// 3 力量、4 敏捷、5 精神、6 智慧、20 体质、21 耐力。六维词缀在登录修复里整件跳过。
var affixSixDimensionKeys = map[int32]bool{3: true, 4: true, 5: true, 6: true, 20: true, 21: true}

const (
	equipManualQualityMin = 1
	equipManualQualityMax = 6
	// equipManualStarMin 与游戏服 gm_control_equip_manual.go 的 gmManualStarMin 一致：
	// 星级 0 会被 equip.go repairBagItem 补回 EquipBase.Star，不是能存住的状态，
	// 所以面板的下限也是 1，而不是 0。
	equipManualStarMin     = 1
	equipManualStarMax     = 20
	equipManualStrengthMax = 20
)

func (a *App) handleEquipManualOptions(w http.ResponseWriter, r *http.Request, rid string, session *sessionData) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, rid, "method_not_allowed", "请求方法不支持")
		return
	}
	if !hasPermission(session, "players.equip") {
		writeError(w, http.StatusForbidden, rid, "forbidden", "没有手工装备权限")
		return
	}
	ctx, cancel := a.requestContext(r)
	defer cancel()
	raw := map[string]any{}
	for _, name := range []string{"ManulEquipAttribute", "EquipAffixConfig", "EquipBase", "GemInlayConfig", "MaterialBase"} {
		value, err := a.readConfig(ctx, name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, rid, "config_unavailable", name+" 配置读取失败，请稍后重试")
			return
		}
		raw[name] = value
	}
	itemID := int32(parseInt64Param(r.URL.Query().Get("itemId"), 0))
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, rid, buildEquipManualOptions(raw, itemID), nil)
}

// buildEquipManualOptions 是纯函数，不碰 DB，便于用固定配置直接单测。
// raw 的每一项就是 readConfig 的返回原样。
func buildEquipManualOptions(raw map[string]any, itemID int32) map[string]any {
	attributeNames := equipattributes.NamesByKey()

	randomAttrsByQuality := map[string][]map[string]any{}
	for _, entry := range configEntries(raw["ManulEquipAttribute"]) {
		key := configFieldInt(entry.row, "Key")
		if key <= 0 {
			continue
		}
		// ManulEquipAttribute 用 id/100 分品质档：101..126 是品质 1 … 601..626 是品质 6。
		quality := entry.index / 100
		if quality < equipManualQualityMin || quality > equipManualQualityMax {
			continue
		}
		slot := strconv.FormatInt(quality, 10)
		randomAttrsByQuality[slot] = append(randomAttrsByQuality[slot], map[string]any{
			"id": entry.index, "key": key, "keyName": equipattributes.AttributeName(int32(key)),
			"value": configFieldValue(entry.row, "Value"),
		})
	}
	for _, list := range randomAttrsByQuality {
		sort.Slice(list, func(i, j int) bool { return configNumber(list[i]["id"]) < configNumber(list[j]["id"]) })
	}

	affixes := make([]map[string]any, 0)
	families := map[int64][]int64{}
	sixDimensionIDs := make([]int64, 0)
	for _, entry := range configEntries(raw["EquipAffixConfig"]) {
		attrs := make([]map[string]any, 0)
		sixDimension := false
		labels := make([]string, 0)
		for _, attr := range configFieldList(entry.row, "AffixArr") {
			row, ok := attr.(map[string]any)
			if !ok {
				continue
			}
			key := configFieldInt(row, "Key")
			if key <= 0 {
				continue
			}
			value := configFieldValue(row, "Value")
			if affixSixDimensionKeys[int32(key)] {
				sixDimension = true
			}
			name := equipattributes.AttributeName(int32(key))
			attrs = append(attrs, map[string]any{"key": key, "keyName": name, "value": value})
			labels = append(labels, name+"+"+formatConfigNumber(value))
		}
		if len(attrs) == 0 {
			continue
		}
		// EquipAffixConfig 用 id/100000 分「词条」，同一词条的 id 升序即档位。
		family := entry.index / 100000
		if family <= 0 {
			continue
		}
		if sixDimension {
			// 六维不进普通池：玩法侧 affixPoolForQuality 把六维整条过滤掉，
			// 由 affixSixDimensionPool 单独按概率产出。
			sixDimensionIDs = append(sixDimensionIDs, entry.index)
		} else {
			families[family] = append(families[family], entry.index)
		}
		affixes = append(affixes, map[string]any{
			"id": entry.index, "family": family, "sixDimension": sixDimension,
			// label 直接给前端当选项文字用（「力量+18 智慧+9」），不显示裸 id。
			"label": strings.Join(labels, " "), "attrs": attrs,
		})
	}
	affixPools := affixPoolsByQuality(families)
	sort.Slice(sixDimensionIDs, func(i, j int) bool { return sixDimensionIDs[i] < sixDimensionIDs[j] })

	// 宝石：MaterialBase.MaterialType==2，与玩法侧 gemConfig 同一判定。
	gems := make([]map[string]any, 0)
	gemsByID := map[int64]map[string]any{}
	for _, entry := range configEntries(raw["MaterialBase"]) {
		if configFieldInt(entry.row, "MaterialType") != 2 {
			continue
		}
		key := configFieldInt(entry.row, "GemKey")
		gemType := configFieldInt(entry.row, "GemType")
		name := strings.TrimSpace(configFieldString(entry.row, "Name"))
		if name == "" {
			name = "未命名宝石（" + strconv.FormatInt(entry.index, 10) + "）"
		}
		gem := map[string]any{
			"id": entry.index, "name": name, "gemKey": key,
			"gemKeyName": equipattributes.AttributeName(int32(key)),
			"gemType":    gemType, "gemTypeName": gemTypeName(gemType),
			"gemLevel": configFieldInt(entry.row, "GemLevel"),
		}
		gems = append(gems, gem)
		gemsByID[entry.index] = gem
	}
	sort.Slice(gems, func(i, j int) bool { return configNumber(gems[i]["id"]) < configNumber(gems[j]["id"]) })

	// 装备部位 → 允许的宝石属性（GemInlayConfig.CanInlayArr）。
	equipTypes := make([]map[string]any, 0)
	inlayByPosition := map[int64][]int64{}
	for _, entry := range configEntries(raw["GemInlayConfig"]) {
		allowed := configIntList(entry.row, "CanInlayArr")
		inlayByPosition[entry.index] = allowed
		equipTypes = append(equipTypes, map[string]any{
			"type": entry.index, "name": itemcatalog.EquipSlotName(int32(entry.index)),
			"allowedGemKeys": allowed,
		})
	}
	sort.Slice(equipTypes, func(i, j int) bool { return configNumber(equipTypes[i]["type"]) < configNumber(equipTypes[j]["type"]) })

	options := map[string]any{
		"limits": map[string]any{
			"minStar": equipManualStarMin, "maxStar": equipManualStarMax, "maxStrength": equipManualStrengthMax,
			"minQuality": equipManualQualityMin, "maxQuality": equipManualQualityMax,
			"bonusCountByQuality": bonusCountByQuality(),
		},
		"attributeNames":       attributeNames,
		"equipTypes":           equipTypes,
		"gemTypeNames":         stringifyGemTypeNames(),
		"gems":                 gems,
		"randomAttrsByQuality": randomAttrsByQuality,
		"affixes":              affixes,
		"affixPools": map[string]any{
			"byQuality": affixPools, "sixDimension": sixDimensionIDs,
		},
	}
	// 只有带了 itemId 才算装备规则：996 行 EquipBase 全算一遍没有意义。
	if rule, ok := equipManualRule(raw["EquipBase"], inlayByPosition, gemsByID, itemID); ok {
		options["equipRules"] = map[string]any{strconv.FormatInt(int64(itemID), 10): rule}
	}
	return options
}

// equipManualRule 复刻玩法侧 gem.go equipAllowsGem 的两段判定，只算 itemID 这一件：
//
//  1. EquipBase.CanInlayGemTypeArr 非空时必须包含宝石的 GemType（宝石大类）；
//  2. EquipBase.Type 必须能在 GemInlayConfig 里查到，且 CanInlayArr 要包含 GemKey（具体属性）。
//
// ⚠ EquipBase.Type 缺失时 num(nil)==0，等价于「武器部位」。996 行里 216 行没有 Type，
// 这里必须原样复刻这个默认值，否则面板给出的可镶嵌宝石会跟服务端实际判定不一致。
// 与其让前端复刻，不如服务端算好直接把 allowedGemIds 发下去。
func equipManualRule(equipRaw any, inlayByPosition map[int64][]int64, gemsByID map[int64]map[string]any, itemID int32) (map[string]any, bool) {
	if itemID <= 0 {
		return nil, false
	}
	var row map[string]any
	name := ""
	for _, entry := range configEntries(equipRaw) {
		if entry.index != int64(itemID) {
			continue
		}
		row = entry.row
		name = strings.TrimSpace(configFieldString(entry.row, "Name"))
		break
	}
	if row == nil {
		return nil, false
	}
	specialKeys := configIntList(row, "CanInlayGemTypeArr")
	position := configFieldInt(row, "Type")
	allowedKeys := inlayByPosition[position]

	allowedGemIDs := make([]int64, 0)
	allowedGemKeySet := map[int64]bool{}
	for _, key := range allowedKeys {
		allowedGemKeySet[key] = true
	}
	for id, gem := range gemsByID {
		if len(specialKeys) > 0 && !configContains(specialKeys, configNumber(gem["gemType"])) {
			continue
		}
		gemKey := configNumber(gem["gemKey"])
		if gemKey <= 0 || !allowedGemKeySet[gemKey] {
			continue
		}
		allowedGemIDs = append(allowedGemIDs, id)
	}
	sort.Slice(allowedGemIDs, func(i, j int) bool { return allowedGemIDs[i] < allowedGemIDs[j] })
	if name == "" {
		name = "未命名装备（" + strconv.FormatInt(int64(itemID), 10) + "）"
	}
	return map[string]any{
		"itemId": itemID, "name": name,
		"type": position, "typeName": itemcatalog.EquipSlotName(int32(position)),
		"maxHole":          configFieldInt(row, "MaxHole"),
		"allowedGemKeys":   allowedKeys,
		"allowedGemIds":    allowedGemIDs,
		"canInlayGemTypes": specialKeys,
	}, true
}

// affixPoolsByQuality 复刻玩法侧 affixPoolForQuality：
// 每个词条（家族）按 id 升序排档，品质 q 取第 q 档（下标 q-1，不足则收敛到最后一档），
// 并且只保留非六维词条——六维由六维池单独产出。
func affixPoolsByQuality(families map[int64][]int64) map[string][]int64 {
	pools := map[string][]int64{}
	ordered := make([]int64, 0, len(families))
	for family := range families {
		ordered = append(ordered, family)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	for quality := int64(equipManualQualityMin); quality <= equipManualQualityMax; quality++ {
		ids := make([]int64, 0, len(ordered))
		for _, family := range ordered {
			tiers := families[family]
			if len(tiers) == 0 {
				continue
			}
			sort.Slice(tiers, func(i, j int) bool { return tiers[i] < tiers[j] })
			pick := int(quality) - 1
			if pick >= len(tiers) {
				pick = len(tiers) - 1
			}
			ids = append(ids, tiers[pick])
		}
		pools[strconv.FormatInt(quality, 10)] = ids
	}
	return pools
}

// bonusCountByQuality 与玩法侧 equipmentBonusCount 一致：白绿 1、蓝紫 2、橙红 3。
// 洗练词缀只允许「空」或「正好这么多条」，中间态重登会被 ensureEquipmentAffixCount 补齐。
func bonusCountByQuality() map[string]int {
	out := map[string]int{}
	for quality := int64(equipManualQualityMin); quality <= equipManualQualityMax; quality++ {
		count := 1
		if quality >= 3 {
			count = 2
		}
		if quality >= 5 {
			count = 3
		}
		out[strconv.FormatInt(quality, 10)] = count
	}
	return out
}

func gemTypeName(gemType int64) string {
	if name, ok := gemTypeNames[int32(gemType)]; ok {
		return name
	}
	return "宝石类型" + strconv.FormatInt(gemType, 10)
}

func stringifyGemTypeNames() map[string]string {
	out := make(map[string]string, len(gemTypeNames))
	for key, name := range gemTypeNames {
		out[strconv.FormatInt(int64(key), 10)] = name
	}
	return out
}

// ===================== readConfig 返回值的取值helper =====================
//
// readConfig 对 datatable 返回 []any，每项是 []any{int64(下标), map[string]any{字段}}；
// 标量是 int64 / float64 / string，嵌套数组是 []any{map[string]any{...}}。

type configEntry struct {
	index int64
	row   map[string]any
}

func configEntries(raw any) []configEntry {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	entries := make([]configEntry, 0, len(list))
	for _, item := range list {
		pair, ok := item.([]any)
		if !ok || len(pair) != 2 {
			continue
		}
		row, ok := pair[1].(map[string]any)
		if !ok {
			continue
		}
		entries = append(entries, configEntry{index: configNumber(pair[0]), row: row})
	}
	return entries
}

func configNumber(value any) int64 {
	switch n := value.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		if err != nil {
			return 0
		}
		return parsed
	}
	return 0
}

func configFieldInt(row map[string]any, field string) int64 {
	return configNumber(row[field])
}

func configFieldValue(row map[string]any, field string) float64 {
	switch n := row[field].(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	case int:
		return float64(n)
	}
	return 0
}

func configFieldString(row map[string]any, field string) string {
	return stringValue(row[field])
}

func configFieldList(row map[string]any, field string) []any {
	list, _ := row[field].([]any)
	return list
}

func configIntList(row map[string]any, field string) []int64 {
	list := configFieldList(row, field)
	out := make([]int64, 0, len(list))
	for _, value := range list {
		out = append(out, configNumber(value))
	}
	return out
}

func configContains(list []int64, value int64) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// formatConfigNumber 让词缀文字里的数值不带小数点：18 而不是 18.000000。
func formatConfigNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}
