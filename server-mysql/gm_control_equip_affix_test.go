package main

import (
	"strings"
	"testing"
)

// gm_control_equip_affix_test.go：player.equip_affix 的规则回归。
//
// 配置表沿用 gm_control_equip_manual_test.go 的 withManualTables/manualTestSession：
// 装备 120590 品质 3（蓝紫档，词缀槽位 2 个），词缀池是家族 10/11 的第 3 档
// （1000003 / 1100003），另有六维词缀 2000001。

func affixTestPayload() gmEquipAffixPayload {
	serverID := "88"
	return gmEquipAffixPayload{ServerID: &serverID}
}

// 整组设置、替换、清空都走同一个「设置最终状态」的入口。
func TestGMEquipAffixSetsReplacesAndClears(t *testing.T) {
	withManualTables(t)
	ss, item := manualTestSession()

	payload := affixTestPayload()
	payload.AddAttrs = manualInts(1000003, 1100003)
	if _, after, err := applyGMEquipAffixAdjust(ss, payload); err != nil {
		t.Fatal(err)
	} else if rows := after["addAttrs"].([]int32); len(rows) != 2 || rows[0] != 1000003 || rows[1] != 1100003 {
		t.Fatalf("after=%v", after)
	}
	if len(item.AddAttrs) != 2 || item.AddAttrs[0] != 1000003 {
		t.Fatalf("addAttrs=%v", item.AddAttrs)
	}

	// 换成六维（整条替换，不是追加）。
	payload = affixTestPayload()
	payload.AddAttrs = manualInts(2000001)
	if _, _, err := applyGMEquipAffixAdjust(ss, payload); err != nil {
		t.Fatal(err)
	}
	if len(item.AddAttrs) != 1 || item.AddAttrs[0] != 2000001 {
		t.Fatalf("addAttrs=%v", item.AddAttrs)
	}

	// 清空：空数组是合法提交值（nil 才是没提交）。
	payload = affixTestPayload()
	payload.AddAttrs = manualInts()
	if _, _, err := applyGMEquipAffixAdjust(ss, payload); err != nil {
		t.Fatal(err)
	}
	if len(item.AddAttrs) != 0 {
		t.Fatalf("addAttrs=%v", item.AddAttrs)
	}
}

// 词缀是这个 action 的**唯一**作用面：星级、品质、强化、随机属性、宝石一律不碰。
// 这条也是它和 player.equip_manual 分开的理由——给一件「星级 5 但 0 条随机属性」的
// 新装备换词缀，不该被「星级 N 需要 N 条随机属性」连带拒绝。
func TestGMEquipAffixLeavesEveryOtherFieldAlone(t *testing.T) {
	withManualTables(t)
	ss, item := manualTestSession()
	item.Star = 5
	item.Quality = 3
	item.Level = 15
	item.RandomAttrs = nil
	item.GemList = []int32{20101, 20102}

	payload := affixTestPayload()
	payload.AddAttrs = manualInts(1000003, 1100003)
	if _, _, err := applyGMEquipAffixAdjust(ss, payload); err != nil {
		t.Fatalf("err=%v", err)
	}
	if item.Star != 5 || item.Quality != 3 || item.Level != 15 {
		t.Fatalf("星级/品质/强化被改动了：%+v", item)
	}
	if len(item.RandomAttrs) != 0 {
		t.Fatalf("随机属性被改动了：%v", item.RandomAttrs)
	}
	if len(item.GemList) != 2 || item.GemList[0] != 20101 {
		t.Fatalf("宝石被改动了：%v", item.GemList)
	}
	if len(item.AddAttrs) != 2 || item.AddAttrs[1] != 1100003 {
		t.Fatalf("addAttrs=%v", item.AddAttrs)
	}
}

// 只有三种状态能稳定持久，其余整单拒绝且不写入。
// 「品质 3 只给 1 条」是这里最关键的一条：它重登会被 ensureEquipmentAffixCount
// 从词缀池补齐到 2 条，存下去等于给运营一个下次登录就变样的存档。
func TestGMEquipAffixRejectsUnstableStates(t *testing.T) {
	cases := []struct {
		name     string
		addAttrs *[]int32
		fragment string
	}{
		{"条数不足会被补齐", manualInts(1000003), "只能为空或正好 2 条"},
		{"条数过多会被裁掉", manualInts(1000003, 1100003, 1000002), "只能为空或正好 2 条"},
		// 1000001 在词缀表里，但它是家族 10 的**第 1 档**，品质 3 取第 3 档，所以不在池内。
		// 这条和「不在词缀表里」是两种不同的错，运营要能分辨自己该改什么。
		{"档位不对不在该品质池内", manualInts(1000001, 1100003), "不在品质 3 的词缀池内"},
		{"词缀不在词缀表里", manualInts(3000001, 1100003), "不在词缀表里"},
		{"六维给两条", manualInts(2000001, 2000001), "洗练词缀重复"},
		{"没提交词缀", nil, "请提交洗练词缀"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			withManualTables(t)
			ss, item := manualTestSession()
			item.AddAttrs = []int32{1000003, 1100003}
			payload := affixTestPayload()
			payload.AddAttrs = testCase.addAttrs
			_, _, err := applyGMEquipAffixAdjust(ss, payload)
			if err == nil || !strings.Contains(err.Error(), testCase.fragment) {
				t.Fatalf("err=%v，期望包含 %q", err, testCase.fragment)
			}
			if len(item.AddAttrs) != 2 || item.AddAttrs[0] != 1000003 {
				t.Fatalf("被拒后词缀变了：%v", item.AddAttrs)
			}
		})
	}
}

// 六维只允许 1 条，且不受品质槽位数约束（含六维时玩法侧整件跳过修复）。
func TestGMEquipAffixAcceptsSingleSixDimensionAtAnyQuality(t *testing.T) {
	withManualTables(t)
	ss, item := manualTestSession()
	item.Quality = 1 // 白装槽位 1：普通词缀只能 1 条，六维也必须能设进来
	payload := affixTestPayload()
	payload.AddAttrs = manualInts(2000001)
	if _, _, err := applyGMEquipAffixAdjust(ss, payload); err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(item.AddAttrs) != 1 || item.AddAttrs[0] != 2000001 {
		t.Fatalf("addAttrs=%v", item.AddAttrs)
	}

	// 同品质下普通词缀的合法条数是 1 条（白装），给 2 条要拒。
	payload = affixTestPayload()
	payload.AddAttrs = manualInts(1000001, 1100001)
	if _, _, err := applyGMEquipAffixAdjust(ss, payload); err == nil || !strings.Contains(err.Error(), "只能为空或正好 1 条") {
		t.Fatalf("err=%v", err)
	}
	if len(item.AddAttrs) != 1 || item.AddAttrs[0] != 2000001 {
		t.Fatalf("被拒后词缀变了：%v", item.AddAttrs)
	}
}

// 对照组：同一件「星级 5 但 0 条随机属性」的装备，手工那条 action 因为星级联动规则
// 拒绝改星级，词缀这条不受影响——这就是把词缀拆成独立入口的实际收益。
func TestGMEquipAffixIsIndependentOfStarRandomAttrRule(t *testing.T) {
	withManualTables(t)
	ss, item := manualTestSession()
	item.Star = 5
	item.RandomAttrs = nil

	manual := manualTestPayload()
	manual.Star = manualStr("6")
	if _, _, err := applyGMEquipManualAdjust(ss, manual); err == nil ||
		!strings.Contains(err.Error(), "星级 6 需要 6 条随机属性") {
		t.Fatalf("手工改星级 err=%v", err)
	}

	affix := affixTestPayload()
	affix.AddAttrs = manualInts(1000003, 1100003)
	if _, _, err := applyGMEquipAffixAdjust(ss, affix); err != nil {
		t.Fatalf("词缀 err=%v", err)
	}
	if len(item.AddAttrs) != 2 {
		t.Fatalf("addAttrs=%v", item.AddAttrs)
	}
	if item.Star != 5 || len(item.RandomAttrs) != 0 {
		t.Fatalf("星级/随机属性被改动了：%+v", item)
	}
}

// 词缀池和槽位数按装备**当前**品质算，不接受外部传入的品质（payload 里根本没有这个字段）。
func TestGMEquipAffixUsesEquipCurrentQuality(t *testing.T) {
	withManualTables(t)
	ss, item := manualTestSession()
	item.Quality = 1 // 白装：槽位 1
	item.AddAttrs = []int32{1000003, 1100003}

	payload := affixTestPayload()
	payload.AddAttrs = manualInts(1000001)
	if _, _, err := applyGMEquipAffixAdjust(ss, payload); err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(item.AddAttrs) != 1 || item.AddAttrs[0] != 1000001 {
		t.Fatalf("addAttrs=%v", item.AddAttrs)
	}
}
