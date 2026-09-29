package main

import (
	"encoding/json"
	"math"
	"testing"
)

func TestStarSoulViceValueMatchesClientQualityFormula(t *testing.T) {
	oldTables := tables
	t.Cleanup(func() { tables = oldTables })
	tables = &datatables{
		starSoulAttribute: map[int64]map[string]interface{}{
			1: {"Type": json.Number("2"), "Key": json.Number("7"), "Value": json.Number("4800")},
			2: {"Type": json.Number("2"), "Key": json.Number("11"), "Value": json.Number("0.05")},
		},
		starSoulType: map[int64]map[string]interface{}{},
	}
	item := &starSoulItem{ID: 1, Quality: 6, Vice: []int32{1, 2}, ViceAdd: []float32{2, 0}}
	bag := newStarSoulBag()
	bag.Items[item.ID], bag.Used[0] = item, item.ID
	ss := &session{starSoul: bag}

	bonus := starSoulBonus(ss)
	if bonus[1009] != 14400 {
		t.Fatalf("quality-6 vice attack = %v, want 4800 * (1 + 2) = 14400", bonus[1009])
	}
	if math.Abs(bonus[1013]-0.05) > 0.0000001 {
		t.Fatalf("single rate vice = %v, want 0.05 without early rounding", bonus[1013])
	}

	item.Quality = 4
	if got := starSoulBonus(ss)[1009]; math.Abs(got-11520) > 0.0001 {
		t.Fatalf("quality-4 vice attack = %v, want 4800 * 3 * 0.8", got)
	}
	item.Quality = 2
	if got := starSoulBonus(ss)[1009]; math.Abs(got-6912) > 0.0001 {
		t.Fatalf("quality-2 vice attack = %v, want 4800 * 3 * 0.48", got)
	}
}

func TestOnlineHighTierStarSoulEffectsReachPanelNumerics(t *testing.T) {
	loadOnlineTablesForTest(t)
	item := &starSoulItem{
		ID: 1, PosType: 0, Quality: 6,
		Vice:    []int32{1144, 1145, 1146, 1147, 1148, 1149, 1150},
		ViceAdd: make([]float32, 7),
	}
	bag := newStarSoulBag()
	bag.Items[item.ID], bag.Used[0] = item, item.ID
	bonus := starSoulBonus(&session{starSoul: bag})
	for numericType, want := range map[int32]float64{
		1032: 0.06, 1033: 0.06,
		1042: 0.008, 1043: 0.01, 1044: 0.0125,
		1045: 0.0166666666666667, 1046: 0.0166666666666667,
	} {
		if math.Abs(bonus[numericType]-want) > 0.0000000001 {
			t.Errorf("high-tier numeric %d = %.16g, want %.16g", numericType, bonus[numericType], want)
		}
	}
}

func TestStarSoulRandomGrowthIncreasesClientViceAdd(t *testing.T) {
	item := &starSoulItem{Vice: []int32{1, 2, 3}, ViceAdd: []float32{0, 2, 2}}
	if !growStarSoulVice(item, func(int) int { return 0 }) {
		t.Fatal("random attribute did not grow")
	}
	if item.ViceAdd[0] != 1 {
		t.Fatalf("grown viceAdd = %v, want first tier increased to 1", item.ViceAdd)
	}
}

func TestStarSoulSuitBonusesApplyAtFourAndEightPieces(t *testing.T) {
	oldTables := tables
	t.Cleanup(func() { tables = oldTables })
	tables = &datatables{
		characterGrowth: map[int64]map[string]interface{}{
			71: {"Str": json.Number("100")},
		},
		starSoulAttribute: map[int64]map[string]interface{}{},
		starSoulType: map[int64]map[string]interface{}{
			1001: {"Suit4Key": json.Number("32"), "Suit4Value": json.Number("0.4")},
			1009: {
				"Suit4Key": json.Number("27"), "Suit4Value": json.Number("0.05"),
				"Suit8Key": json.Number("28"), "Suit8Value": json.Number("0.1"),
			},
		},
	}
	bag := newStarSoulBag()
	for slot := 0; slot < 12; slot++ {
		id := int64(slot + 1)
		typeID := int32(1009)
		if slot < 4 {
			typeID = 1001
		}
		bag.Items[id] = &starSoulItem{ID: id, TypeID: typeID, PosType: int32(slot), Quality: 6}
		bag.Used[slot] = id
	}
	ss := &session{jobID: 1, level: 10, starSoul: bag}

	if got := ss.playerPhyAtk(); got != 140 {
		t.Fatalf("four-piece attack = %d, want 100 * 1.4 = 140", got)
	}
	bonus := starSoulBonus(ss)
	if math.Abs(bonus[1042]-0.05) > 0.000001 || math.Abs(bonus[1043]-0.1) > 0.000001 {
		t.Fatalf("eight-piece lifesteal bonuses = %#v, want rate 0.05 and value 0.1", bonus)
	}
	if got := ss.playerExtraNumeric(1042); math.Abs(got-0.05) > 0.000001 {
		t.Fatalf("lifesteal rate = %v, want direct 0.05 without percentage rescaling", got)
	}
}
