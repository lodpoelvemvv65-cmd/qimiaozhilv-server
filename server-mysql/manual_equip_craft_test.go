package main

import (
	"encoding/json"
	"testing"

	"mhqserver/internal/operationsconfig"
	"mhqserver/protocol"
)

func TestRollManualCraftUsesOnlineStarBindAndPity(t *testing.T) {
	intn := func(int) int { return 0 }
	normal := operationsconfig.Defaults().ManualEquip.Normal
	quality, star, count, pityAt := rollManualCraft(normal, 0, 0, intn)
	if star < 3 || star > 10 || quality < 1 || quality > 4 || count != 0 || pityAt != 0 {
		t.Fatalf("normal roll quality=%d star=%d count=%d pityAt=%d", quality, star, count, pityAt)
	}

	rare := operationsconfig.ManualEquipRecipe{
		MinStar: 6, MaxStar: 6, RedPercent: 0,
		PityMinCount: 2, PityMaxCount: 2, PityMinQuality: 5,
		QualityWeights: []operationsconfig.ManualQualityWeight{{Quality: 3, Weight: 1}},
	}
	quality, star, count, pityAt = rollManualCraft(rare, 0, 0, intn)
	if quality != 3 || star != 6 || count != 1 || pityAt != 2 {
		t.Fatalf("rare first roll quality=%d star=%d count=%d pityAt=%d", quality, star, count, pityAt)
	}
	quality, star, count, pityAt = rollManualCraft(rare, count, pityAt, intn)
	if quality != 5 || star != 6 || count != 0 || pityAt != 0 {
		t.Fatalf("rare pity roll quality=%d star=%d count=%d pityAt=%d", quality, star, count, pityAt)
	}

	epic := operationsconfig.ManualEquipRecipe{
		MinStar: 6, MaxStar: 6, MinQuality: 3,
		PityMinCount: 1, PityMaxCount: 1, PityMinQuality: 6,
		QualityWeights: []operationsconfig.ManualQualityWeight{{Quality: 3, Weight: 1}},
	}
	quality, star, count, pityAt = rollManualCraft(epic, 0, 0, intn)
	if quality != 6 || star != 6 || count != 0 || pityAt != 0 {
		t.Fatalf("epic pity roll quality=%d star=%d count=%d pityAt=%d", quality, star, count, pityAt)
	}
}

func TestRollManualCraftRedPercentUsesIndependentFivePercent(t *testing.T) {
	calls := 0
	intn := func(n int) int {
		calls++
		if n == 10000 {
			return 499
		}
		return 0
	}
	recipe := operationsconfig.ManualEquipRecipe{
		MinStar: 6, MaxStar: 6, RedPercent: 5,
		QualityWeights: []operationsconfig.ManualQualityWeight{{Quality: 3, Weight: 1}},
	}
	quality, _, _, _ := rollManualCraft(recipe, 0, 0, intn)
	if quality != 6 {
		t.Fatalf("red percent roll quality=%d, want 6", quality)
	}
	if calls == 0 {
		t.Fatal("red percent did not consult intn")
	}
}

func TestApplyManualCraftOutcomeSetsBoundQualityAndSharedPity(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.ManualEquip.Rare.MinStar = 7
		config.ManualEquip.Rare.MaxStar = 7
		config.ManualEquip.Rare.RedPercent = 0
		config.ManualEquip.Rare.PityMinCount = 1
		config.ManualEquip.Rare.PityMaxCount = 1
		config.ManualEquip.Rare.PityMinQuality = 5
		config.ManualEquip.Rare.QualityWeights = []operationsconfig.ManualQualityWeight{{Quality: 3, Weight: 1}}
		config.ManualEquip.UpgradeRare = config.ManualEquip.Rare
		config.ManualEquip.UpgradeRare.Bound = true
		config.ManualEquip.Normal.MinStar = 4
		config.ManualEquip.Normal.MaxStar = 4
		config.ManualEquip.Normal.QualityWeights = []operationsconfig.ManualQualityWeight{{Quality: 2, Weight: 1}}
	})
	withCraftingTables(t, &datatables{
		// 120877 月语：只有 SpecialKey/SpecialValue 一条固定行，占 1 个词条名额。
		equipBase: map[int64]map[string]interface{}{
			120877: {"SpecialKey": json.Number("1"), "SpecialValue": json.Number("20000")},
		},
		equipAffix: map[int64]map[string]interface{}{
			110001: {"AffixArr": []interface{}{map[string]interface{}{"Key": json.Number("1"), "Value": json.Number("100")}}},
			210001: {"AffixArr": []interface{}{map[string]interface{}{"Key": json.Number("2"), "Value": json.Number("100")}}},
			310001: {"AffixArr": []interface{}{map[string]interface{}{"Key": json.Number("3"), "Value": json.Number("100")}}},
		},
		manulEquipAttribute: manualAttributePoolFixture(12),
	})
	ss := newSession()
	normal := &bagItem{ItemId: 120877, ItemType: 1}
	applyManualCraftOutcomeWithIntn(ss, int32(protocol.StoneType_Nomal), normal, func(int) int { return 0 })
	// 随机词条数 = 星级：4 星 → 4 条随机（固定行不占名额），
	// 且随机取自对应品质档（2 星档 → 2xx）。出厂不带洗练词缀（AddAttrs 必须为空）。
	if !normal.IsLock || normal.Quality != 2 || normal.Star != 4 || len(normal.RandomAttrs) != 4 || len(normal.AddAttrs) != 0 {
		t.Fatalf("normal item = %#v", normal)
	}
	for _, id := range normal.RandomAttrs {
		if id/100 != 2 {
			t.Fatalf("normal random attrs = %v, want quality 2", normal.RandomAttrs)
		}
	}

	rare := &bagItem{ItemId: 120877, ItemType: 1}
	applyManualCraftOutcomeWithIntn(ss, int32(protocol.StoneType_Rare), rare, func(int) int { return 0 })
	if rare.IsLock || rare.Quality != 5 || rare.Star != 7 || ss.signin.ManualRareCraftCount != 0 || len(rare.RandomAttrs) != 7 || len(rare.AddAttrs) != 0 {
		t.Fatalf("rare item/pity = %#v / %+v", rare, ss.signin)
	}
	for _, id := range rare.RandomAttrs {
		if id/100 != 5 {
			t.Fatalf("rare random attrs = %v, want quality 5", rare.RandomAttrs)
		}
	}

	bound := &bagItem{ItemId: 120877, ItemType: 1}
	applyManualCraftOutcomeWithIntn(ss, int32(protocol.StoneType_UpgradeRare), bound, func(int) int { return 0 })
	if !bound.IsLock || bound.Quality != 5 || bound.Star != 7 || len(bound.RandomAttrs) != 7 || len(bound.AddAttrs) != 0 {
		t.Fatalf("upgrade rare item = %#v", bound)
	}
}

// manualAttributePoolFixture 构造 ManulEquipAttribute 形状的测试数据：6 个品质档，
// 每档 keyCount 条（keyCount 需覆盖星级上限，否则条数会被池子大小截断）。
func manualAttributePoolFixture(keyCount int) map[int64]map[string]interface{} {
	rows := make(map[int64]map[string]interface{}, 6*keyCount)
	for quality := 1; quality <= 6; quality++ {
		for key := 1; key <= keyCount; key++ {
			rows[int64(quality*100+key)] = map[string]interface{}{
				"Key":   float64(key),
				"Value": float64(quality * 10),
			}
		}
	}
	return rows
}
