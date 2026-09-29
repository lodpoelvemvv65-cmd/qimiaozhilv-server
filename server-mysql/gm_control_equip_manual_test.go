package main

import (
	"strings"
	"testing"
)

// gm_control_equip_manual_test.go：player.equip_manual 的规则回归。
//
// 全部用例走纯内存的 applyGMEquipManualAdjust，自己搭一套最小配置表：
//   - 品质 3 的随机属性池 301/302（ManulEquipAttribute 用 id/100 分档）
//   - 普通词缀 1000001（家族 10，非六维）、六维词缀 2000001（家族 20，含 Key=3 力量）
//   - 装备 120590：MaxHole=2，EquipBase.Type=4（称号），允许 GemKey 3/4
//   - 宝石 20101(GemKey=3) / 20102(GemKey=4) / 20103(GemKey=7 部位不允许)

func manualTestAffixArr(keys ...int32) []interface{} {
	arr := make([]interface{}, 0, len(keys))
	for _, key := range keys {
		arr = append(arr, map[string]interface{}{"Key": key, "Value": int32(18)})
	}
	return arr
}

func withManualTables(t *testing.T) {
	t.Helper()
	previous := tables
	tables = &datatables{
		manulEquipAttribute: map[int64]map[string]interface{}{
			101: {"Key": int32(1)}, 102: {"Key": int32(3)},
			301: {"Key": int32(1)}, 302: {"Key": int32(3)},
			// Key 缺失的行不是可用属性，用来验证过滤
			303: {"Key": int32(0)},
		},
		// EquipAffixConfig 按 id/100000 分「词条」，同一词条的 id 升序即档位。
		// 原版每个词条有 5 档，这里给家族 10/11 各造 3 档，品质 3 取第 3 档。
		equipAffix: map[int64]map[string]interface{}{
			1000001: {"AffixArr": manualTestAffixArr(1)},
			1000002: {"AffixArr": manualTestAffixArr(1)},
			1000003: {"AffixArr": manualTestAffixArr(1)},
			1100001: {"AffixArr": manualTestAffixArr(2)},
			1100002: {"AffixArr": manualTestAffixArr(2)},
			1100003: {"AffixArr": manualTestAffixArr(2)},
			// 六维词缀：含 AttributeType 3（力量），不参与普通池
			2000001: {"AffixArr": manualTestAffixArr(3)},
		},
		equipBase: map[int64]map[string]interface{}{
			120590: {"Type": int32(4), "MaxHole": int32(2), "CanInlayGemTypeArr": []interface{}{int32(5)}},
		},
		gemInlay: map[int64]map[string]interface{}{
			4: {"CanInlayArr": []interface{}{int32(3), int32(4)}},
		},
		materialBase: map[int64]map[string]interface{}{
			20101: {"MaterialType": int32(2), "GemKey": int32(3), "GemType": int32(5), "GemLevel": int32(1)},
			20102: {"MaterialType": int32(2), "GemKey": int32(4), "GemType": int32(5), "GemLevel": int32(1)},
			20103: {"MaterialType": int32(2), "GemKey": int32(7), "GemType": int32(5), "GemLevel": int32(1)},
			// 不是宝石
			110205: {"MaterialType": int32(1)},
		},
	}
	t.Cleanup(func() { tables = previous })
}

func manualTestSession() (*session, *bagItem) {
	ss := &session{bag: map[int32]*bagItem{}, worn: map[int32]*bagItem{}}
	item := &bagItem{ItemId: 120590, ItemType: 1, ServerId: 88, Quality: 3, Star: 0, Level: 0}
	ss.worn[2] = item
	return ss, item
}

func manualTestPayload() gmEquipManualPayload {
	serverID := "88"
	return gmEquipManualPayload{ServerID: &serverID}
}

func manualStr(value string) *string { return &value }

func manualInts(values ...int32) *[]int32 {
	copied := append([]int32(nil), values...)
	return &copied
}

func TestGMEquipManualAppliesQualityStarStrengthGemsTogether(t *testing.T) {
	withManualTables(t)
	ss, item := manualTestSession()
	payload := manualTestPayload()
	payload.Star = manualStr("2")
	payload.Quality = manualStr("3")
	payload.Strength = manualStr("15")
	payload.RandomAttrs = manualInts(301, 302)
	payload.Gems = manualInts(20101, 20102)

	before, after, err := applyGMEquipManualAdjust(ss, payload)
	if err != nil {
		t.Fatal(err)
	}
	if item.Quality != 3 || item.Star != 2 || item.Level != 15 {
		t.Fatalf("item=%+v", item)
	}
	if len(item.RandomAttrs) != 2 || item.RandomAttrs[0] != 301 {
		t.Fatalf("randomAttrs=%v", item.RandomAttrs)
	}
	if len(item.GemList) != 2 || item.GemList[0] != 20101 || item.GemList[1] != 20102 {
		t.Fatalf("gems=%v", item.GemList)
	}
	if before["quality"] != int32(3) || before["star"] != int32(0) {
		t.Fatalf("before=%v", before)
	}
	if after["star"] != int32(2) || after["strength"] != int32(15) {
		t.Fatalf("after=%v", after)
	}
}

// 星级与随机属性条数强制联动，且失败时**任何字段都不能变**。
func TestGMEquipManualRejectsStarCountMismatchAndWritesNothing(t *testing.T) {
	withManualTables(t)
	ss, item := manualTestSession()
	payload := manualTestPayload()
	payload.Star = manualStr("2")
	payload.Quality = manualStr("3")
	payload.Strength = manualStr("15")
	payload.RandomAttrs = manualInts(301)

	_, _, err := applyGMEquipManualAdjust(ss, payload)
	if err == nil || !strings.Contains(err.Error(), "星级 2 需要 2 条随机属性，当前提交了 1 条") {
		t.Fatalf("err=%v", err)
	}
	if item.Star != 0 || item.Level != 0 || len(item.RandomAttrs) != 0 {
		t.Fatalf("partial write: %+v", item)
	}
}

func TestGMEquipManualRejectsRandomAttrFromAnotherQualityTier(t *testing.T) {
	withManualTables(t)
	ss, _ := manualTestSession()
	payload := manualTestPayload()
	payload.Star = manualStr("2")
	payload.Quality = manualStr("3")
	payload.RandomAttrs = manualInts(101, 102) // 品质 1 的档位
	_, _, err := applyGMEquipManualAdjust(ss, payload)
	if err == nil || !strings.Contains(err.Error(), "不属于 3 档属性池") {
		t.Fatalf("err=%v", err)
	}
}

func TestGMEquipManualRejectsUnknownAndDuplicateRandomAttrs(t *testing.T) {
	withManualTables(t)
	ss, _ := manualTestSession()
	payload := manualTestPayload()
	payload.Star = manualStr("2")
	payload.Quality = manualStr("3")
	payload.RandomAttrs = manualInts(301, 301)
	if _, _, err := applyGMEquipManualAdjust(ss, payload); err == nil || !strings.Contains(err.Error(), "随机属性重复") {
		t.Fatalf("dup err=%v", err)
	}
	// 303 在表里但 Key=0，不是可用属性
	payload.RandomAttrs = manualInts(303, 302)
	if _, _, err := applyGMEquipManualAdjust(ss, payload); err == nil || !strings.Contains(err.Error(), "不在属性表里") {
		t.Fatalf("unknown err=%v", err)
	}
}

// 星级只改一半时必须提示需要一并提交随机属性，而不是静默让两者脱钩。
func TestGMEquipManualRejectsStarChangeWithoutMatchingEntries(t *testing.T) {
	withManualTables(t)
	ss, item := manualTestSession()
	item.RandomAttrs = []int32{301, 302, 301} // 现有 3 条
	payload := manualTestPayload()
	payload.Star = manualStr("2")
	_, _, err := applyGMEquipManualAdjust(ss, payload)
	if err == nil || !strings.Contains(err.Error(), "星级 2 需要 2 条随机属性，装备当前有 3 条") {
		t.Fatalf("err=%v", err)
	}
}

// 只改强化/宝石不该受「星级 = 随机属性条数」约束：玩法侧新发放的装备天生就是
// EquipBase.Star 星级 + 0 条随机属性（repairBagItem 只补 Star，不补 RandomAttrs），
// 那是正常状态。条数核对只对“星级确实变了”的提交生效，否则这类装备连强化都改不了。
func TestGMEquipManualAllowsNonStarEditOnFreshEquip(t *testing.T) {
	withManualTables(t)
	ss, item := manualTestSession()
	item.Star = 5 // 新发放装备的基础星级，随机属性为空
	payload := manualTestPayload()
	payload.Strength = manualStr("7")
	payload.Gems = manualInts(20101, 20102)
	if _, _, err := applyGMEquipManualAdjust(ss, payload); err != nil {
		t.Fatalf("err=%v", err)
	}
	if item.Level != 7 || item.Star != 5 || len(item.RandomAttrs) != 0 {
		t.Fatalf("item=%+v", item)
	}
}

// 星级 0 不是能存住的状态：equip.go repairBagItem 会把 Star==0 的装备补回
// EquipBase.Star，所以直接拒绝，并说明下限，避免留下「星级 N 却 0 条随机属性」的存档。
func TestGMEquipManualRejectsStarZero(t *testing.T) {
	withManualTables(t)
	ss, item := manualTestSession()
	item.Star = 5
	item.RandomAttrs = []int32{301, 302}
	payload := manualTestPayload()
	payload.Star = manualStr("0")
	if _, _, err := applyGMEquipManualAdjust(ss, payload); err == nil || !strings.Contains(err.Error(), "星级不能小于 1") {
		t.Fatalf("err=%v", err)
	}
	if item.Star != 5 || len(item.RandomAttrs) != 2 {
		t.Fatalf("rejected star=0 must not change anything: %+v", item)
	}
}

// 品质 3（蓝）的槽位是 2：1 条普通词缀重登会被补齐，必须直接拒绝。
func TestGMEquipManualRejectsAffixCountBelowSlots(t *testing.T) {
	withManualTables(t)
	ss, item := manualTestSession()
	payload := manualTestPayload()
	payload.AddAttrs = manualInts(1000003)
	_, _, err := applyGMEquipManualAdjust(ss, payload)
	if err == nil || !strings.Contains(err.Error(), "只能为空或正好 2 条") {
		t.Fatalf("err=%v", err)
	}
	if len(item.AddAttrs) != 0 {
		t.Fatalf("partial write: %v", item.AddAttrs)
	}
}

func TestGMEquipManualAcceptsExactlySlotCountAffixes(t *testing.T) {
	withManualTables(t)
	ss, item := manualTestSession()
	payload := manualTestPayload()
	payload.AddAttrs = manualInts(1000003, 1100003)
	if _, _, err := applyGMEquipManualAdjust(ss, payload); err != nil {
		t.Fatal(err)
	}
	if len(item.AddAttrs) != 2 {
		t.Fatalf("addAttrs=%v", item.AddAttrs)
	}
}

// 六维词缀整件跳过登录修复，只允许 1 条。
func TestGMEquipManualAcceptsSingleSixDimensionAffixOnly(t *testing.T) {
	withManualTables(t)
	ss, item := manualTestSession()
	payload := manualTestPayload()
	payload.AddAttrs = manualInts(2000001)
	if _, _, err := applyGMEquipManualAdjust(ss, payload); err != nil {
		t.Fatal(err)
	}
	if len(item.AddAttrs) != 1 || item.AddAttrs[0] != 2000001 {
		t.Fatalf("addAttrs=%v", item.AddAttrs)
	}

	payload.AddAttrs = manualInts(2000001, 1000003)
	if _, _, err := applyGMEquipManualAdjust(ss, payload); err == nil || !strings.Contains(err.Error(), "六维词缀只能 1 条") {
		t.Fatalf("err=%v", err)
	}
}

func TestGMEquipManualRejectsAffixOutsideQualityPool(t *testing.T) {
	withManualTables(t)
	ss, _ := manualTestSession()
	payload := manualTestPayload()
	// 1000001 在词缀表里，但它是家族 10 的第 1 档，品质 3 抽取的是第 3 档。
	payload.AddAttrs = manualInts(1000001, 1100003)
	if _, _, err := applyGMEquipManualAdjust(ss, payload); err == nil || !strings.Contains(err.Error(), "不在品质 3 的词缀池内") {
		t.Fatalf("err=%v", err)
	}
	// 完全不在表里的 id 走另一条分支
	payload.AddAttrs = manualInts(3000001, 1000003)
	if _, _, err := applyGMEquipManualAdjust(ss, payload); err == nil || !strings.Contains(err.Error(), "不在词缀表里") {
		t.Fatalf("err=%v", err)
	}
}

func TestGMEquipManualClearsAffixesWithEmptyArray(t *testing.T) {
	withManualTables(t)
	ss, item := manualTestSession()
	item.AddAttrs = []int32{1000003, 1100003}
	payload := manualTestPayload()
	payload.AddAttrs = manualInts()
	if _, _, err := applyGMEquipManualAdjust(ss, payload); err != nil {
		t.Fatal(err)
	}
	if len(item.AddAttrs) != 0 {
		t.Fatalf("addAttrs=%v", item.AddAttrs)
	}
}

// GemList 必须按 MaxHole 定长；0 表示空槽。
func TestGMEquipManualRejectsWrongGemSlotCount(t *testing.T) {
	withManualTables(t)
	ss, item := manualTestSession()
	payload := manualTestPayload()
	payload.Gems = manualInts(20101)
	_, _, err := applyGMEquipManualAdjust(ss, payload)
	if err == nil || !strings.Contains(err.Error(), "必须正好提交 2 项") {
		t.Fatalf("err=%v", err)
	}
	if len(item.GemList) != 0 {
		t.Fatalf("partial write: %v", item.GemList)
	}
	// 定长但有空槽是合法的
	payload.Gems = manualInts(20101, 0)
	if _, _, err := applyGMEquipManualAdjust(ss, payload); err != nil {
		t.Fatal(err)
	}
	if len(item.GemList) != 2 || item.GemList[1] != 0 {
		t.Fatalf("gems=%v", item.GemList)
	}
}

func TestGMEquipManualRejectsGemNotAllowedOnSlot(t *testing.T) {
	withManualTables(t)
	ss, _ := manualTestSession()
	payload := manualTestPayload()
	// 20103 的 GemKey=7 不在 GemInlayConfig[4].CanInlayArr 里
	payload.Gems = manualInts(20103, 0)
	if _, _, err := applyGMEquipManualAdjust(ss, payload); err == nil || !strings.Contains(err.Error(), "该部位不能镶嵌") {
		t.Fatalf("err=%v", err)
	}
	// 非宝石
	payload.Gems = manualInts(110205, 0)
	if _, _, err := applyGMEquipManualAdjust(ss, payload); err == nil || !strings.Contains(err.Error(), "不是宝石") {
		t.Fatalf("err=%v", err)
	}
}

func TestGMEquipManualRejectsDuplicateGemKey(t *testing.T) {
	withManualTables(t)
	ss, _ := manualTestSession()
	// 20101(GemKey=3) 与 20102(GemKey=4) 不同属性可以共存；
	// 这里造一个 GemKey 同为 3 的宝石验证重复判定。
	tables.materialBase[20104] = map[string]interface{}{
		"MaterialType": int32(2), "GemKey": int32(3), "GemType": int32(5), "GemLevel": int32(2),
	}
	payload := manualTestPayload()
	payload.Gems = manualInts(20101, 20104)
	_, _, err := applyGMEquipManualAdjust(ss, payload)
	if err == nil || !strings.Contains(err.Error(), "已镶嵌相同属性宝石") {
		t.Fatalf("err=%v", err)
	}
}

func TestGMEquipManualRejectsGemsOnLockedEquip(t *testing.T) {
	withManualTables(t)
	ss, item := manualTestSession()
	item.IsLock = true
	payload := manualTestPayload()
	payload.Gems = manualInts(20101, 20102)
	if _, _, err := applyGMEquipManualAdjust(ss, payload); err == nil || !strings.Contains(err.Error(), "装备已锁定，无法镶嵌") {
		t.Fatalf("err=%v", err)
	}
}

// 星级与词缀解耦：equip_adjust 改星级时会顺手清空 AddAttrs（gm_control_equip.go），
// 本 action 不继承——只改星级的提交必须原样保留词缀。
func TestGMEquipManualStarKeepsAffixes(t *testing.T) {
	withManualTables(t)
	ss, item := manualTestSession()
	item.AddAttrs = []int32{1000003, 1100003}
	payload := manualTestPayload()
	payload.Star = manualStr("2")
	payload.RandomAttrs = manualInts(301, 302)
	if _, _, err := applyGMEquipManualAdjust(ss, payload); err != nil {
		t.Fatal(err)
	}
	if len(item.AddAttrs) != 2 {
		t.Fatalf("changing star cleared affixes: %v", item.AddAttrs)
	}
}

func TestGMEquipManualRejectsEmptyPayloadAndNonEquip(t *testing.T) {
	withManualTables(t)
	ss, _ := manualTestSession()
	if _, _, err := applyGMEquipManualAdjust(ss, manualTestPayload()); err == nil {
		t.Fatal("empty payload accepted")
	}
	nonEquipSS := &session{bag: map[int32]*bagItem{}, worn: map[int32]*bagItem{}}
	nonEquipSS.bag[0] = &bagItem{ItemId: 110205, ItemType: 2, ServerId: 7, Count: 1}
	star := "3"
	payload := gmEquipManualPayload{ServerID: manualStr("7"), Star: &star}
	if _, _, err := applyGMEquipManualAdjust(nonEquipSS, payload); err == nil || !strings.Contains(err.Error(), "只能调整装备") {
		t.Fatalf("err=%v", err)
	}
}

func TestGMEquipManualRejectsOutOfRangeFields(t *testing.T) {
	withManualTables(t)
	ss, _ := manualTestSession()
	for _, tc := range []struct {
		name string
		want string
		star *string
		qual *string
		str  *string
	}{
		{"星级超上限", "星级不能超过 20", manualStr("21"), nil, nil},
		{"品质超上限", "装备品质不能超过 6", nil, manualStr("7"), nil},
		{"品质低于下限", "装备品质不能小于 1", nil, manualStr("0"), nil},
		{"强化超上限", "强化等级不能超过 20", nil, nil, manualStr("25")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := manualTestPayload()
			payload.Star, payload.Quality, payload.Strength = tc.star, tc.qual, tc.str
			_, _, err := applyGMEquipManualAdjust(ss, payload)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err=%v, want %q", err, tc.want)
			}
		})
	}
}

// 快照必须带齐五个字段，且空列表序列化成 []，不能是 null。
func TestGMEquipManualSnapshotCoversEveryField(t *testing.T) {
	item := &bagItem{ItemId: 120590, ServerId: 5, Quality: 3, Star: 4, Level: 9,
		RandomAttrs: []int32{301}, AddAttrs: []int32{1000003, 1100003}, GemList: []int32{20101, 0}}
	snapshot := gmEquipManualSnapshot(item, itemLocationWorn, 2)
	for _, key := range []string{"quality", "star", "strength", "randomAttrs", "addAttrs", "gems"} {
		if _, ok := snapshot[key]; !ok {
			t.Fatalf("snapshot missing %q: %v", key, snapshot)
		}
	}
	if gems, ok := snapshot["gems"].([]int32); !ok || len(gems) != 2 || gems[1] != 0 {
		t.Fatalf("gems=%v", snapshot["gems"])
	}
	empty := gmEquipManualSnapshot(&bagItem{ItemId: 1}, itemLocationBag, 0)
	for _, key := range []string{"randomAttrs", "addAttrs", "gems"} {
		list, ok := empty[key].([]int32)
		if !ok || list == nil {
			t.Fatalf("%s must serialize as [], got %#v", key, empty[key])
		}
	}
}
