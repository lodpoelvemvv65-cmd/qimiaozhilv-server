package main

import (
	"strings"
	"testing"
)

// gm_control_equip_gem_test.go：player.equip_gem 的规则回归。
//
// 配置表沿用 gm_control_equip_manual_test.go 的 withManualTables/manualTestSession：
// 装备 120590 有 2 个宝石槽、部位（Type=4）只允许 GemKey 3/4，
// 宝石 20101(GemKey=3) / 20102(GemKey=4) / 20103(GemKey=7 部位不允许)，
// 110205 不是宝石。

func gemTestPayload() gmEquipGemPayload {
	serverID := "88"
	return gmEquipGemPayload{ServerID: &serverID}
}

// 镶嵌、替换、整件卸下都走同一个「设置最终状态」的入口。
func TestGMEquipGemSetsReplacesAndClearsSlots(t *testing.T) {
	withManualTables(t)
	ss, item := manualTestSession()

	payload := gemTestPayload()
	payload.Gems = manualInts(20101, 20102)
	if _, after, err := applyGMEquipGemAdjust(ss, payload); err != nil {
		t.Fatal(err)
	} else if gems := after["gems"].([]int32); len(gems) != 2 || gems[0] != 20101 || gems[1] != 20102 {
		t.Fatalf("after=%v", after)
	}
	if len(item.GemList) != 2 || item.GemList[0] != 20101 {
		t.Fatalf("gems=%v", item.GemList)
	}

	// 替换：只换第 2 个孔，第 1 个孔留空。
	payload = gemTestPayload()
	payload.Gems = manualInts(0, 20102)
	if _, _, err := applyGMEquipGemAdjust(ss, payload); err != nil {
		t.Fatal(err)
	}
	if len(item.GemList) != 2 || item.GemList[0] != 0 || item.GemList[1] != 20102 {
		t.Fatalf("gems=%v", item.GemList)
	}

	// 整件卸下：全填 0 必须成功，且仍然是定长 2 项（长度不符客户端的宝石槽会错位）。
	payload = gemTestPayload()
	payload.Gems = manualInts(0, 0)
	if _, _, err := applyGMEquipGemAdjust(ss, payload); err != nil {
		t.Fatal(err)
	}
	if len(item.GemList) != 2 || item.GemList[0] != 0 || item.GemList[1] != 0 {
		t.Fatalf("gems=%v", item.GemList)
	}
}

// 宝石是这个 action 的**唯一**作用面：星级、品质、强化、随机属性、词缀一律不碰。
// 这条也是它和 player.equip_manual 分开的理由——给一件「星级 5 但 0 条随机属性」的
// 新装备镶宝石，不该被「星级 N 需要 N 条随机属性」连带拒绝。
func TestGMEquipGemLeavesEveryOtherFieldAlone(t *testing.T) {
	withManualTables(t)
	ss, item := manualTestSession()
	item.Star = 5
	item.Quality = 2
	item.Level = 15
	item.RandomAttrs = nil
	item.AddAttrs = []int32{1000001}

	payload := gemTestPayload()
	payload.Gems = manualInts(20101, 20102)
	if _, _, err := applyGMEquipGemAdjust(ss, payload); err != nil {
		t.Fatalf("err=%v", err)
	}
	if item.Star != 5 || item.Quality != 2 || item.Level != 15 {
		t.Fatalf("星级/品质/强化被改动了：%+v", item)
	}
	if len(item.RandomAttrs) != 0 {
		t.Fatalf("随机属性被改动了：%v", item.RandomAttrs)
	}
	if len(item.AddAttrs) != 1 || item.AddAttrs[0] != 1000001 {
		t.Fatalf("洗练词缀被改动了：%v", item.AddAttrs)
	}
	if len(item.GemList) != 2 || item.GemList[0] != 20101 {
		t.Fatalf("gems=%v", item.GemList)
	}
}

// 任一孔非法就整单拒绝，且宝石槽保持原样（不留「镶了一半」）。
func TestGMEquipGemRejectsAndWritesNothing(t *testing.T) {
	cases := []struct {
		name     string
		gems     *[]int32
		fragment string
	}{
		{"条数不足", manualInts(20101), "该装备有 2 个宝石槽"},
		{"条数过多", manualInts(20101, 20102, 0), "该装备有 2 个宝石槽"},
		{"不是宝石", manualInts(110205, 0), "只能放入宝石"},
		{"部位不允许", manualInts(20103, 0), "该部位不能镶嵌"},
		{"属性重复", manualInts(20101, 20101), "相同属性宝石"},
		{"没提交宝石槽", nil, "请至少选择宝石孔"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			withManualTables(t)
			ss, item := manualTestSession()
			item.GemList = []int32{20102, 20101}
			payload := gemTestPayload()
			payload.Gems = testCase.gems
			_, _, err := applyGMEquipGemAdjust(ss, payload)
			if err == nil || !strings.Contains(err.Error(), testCase.fragment) {
				t.Fatalf("err=%v，期望包含 %q", err, testCase.fragment)
			}
			if len(item.GemList) != 2 || item.GemList[0] != 20102 || item.GemList[1] != 20101 {
				t.Fatalf("被拒后宝石槽变了：%v", item.GemList)
			}
		})
	}
}

// 锁定装备整单拒绝，与玩法侧「装备已锁定，无法镶嵌」一致。
func TestGMEquipGemRejectsLockedEquip(t *testing.T) {
	withManualTables(t)
	ss, item := manualTestSession()
	item.IsLock = true
	item.GemList = []int32{20101, 20102}
	payload := gemTestPayload()
	payload.Gems = manualInts(0, 0)
	_, _, err := applyGMEquipGemAdjust(ss, payload)
	if err == nil || !strings.Contains(err.Error(), "装备已锁定") {
		t.Fatalf("err=%v", err)
	}
	if len(item.GemList) != 2 || item.GemList[0] != 20101 {
		t.Fatalf("被拒后宝石槽变了：%v", item.GemList)
	}
}

// 没有宝石槽的装备（EquipBase.MaxHole 为 0）直接拒绝，而不是静默写入空列表。
func TestGMEquipGemRejectsEquipWithoutHoles(t *testing.T) {
	withManualTables(t)
	tables.equipBase[120590]["MaxHole"] = int32(0)
	ss, item := manualTestSession()
	payload := gemTestPayload()
	payload.Gems = manualInts(20101)
	_, _, err := applyGMEquipGemAdjust(ss, payload)
	if err == nil || !strings.Contains(err.Error(), "没有宝石槽") {
		t.Fatalf("err=%v", err)
	}
	if len(item.GemList) != 0 {
		t.Fatalf("被拒后宝石槽变了：%v", item.GemList)
	}
}
