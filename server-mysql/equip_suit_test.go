package main

import (
	"encoding/json"
	"math"
	"testing"
)

func TestEquipSuitBonusRequiresCompleteProfessionSet(t *testing.T) {
	oldTables := tables
	t.Cleanup(func() { tables = oldTables })

	equipIDs := []int32{120001, 120002, 120003, 120004}
	equipBase := make(map[int64]map[string]interface{}, len(equipIDs))
	worn := make(map[int32]*bagItem, len(equipIDs))
	for slot, id := range equipIDs {
		equipBase[int64(id)] = map[string]interface{}{}
		if slot < 3 {
			worn[int32(slot)] = &bagItem{ItemId: id, ItemType: 1, Count: 1}
		}
	}
	tables = &datatables{
		equipBase: equipBase,
		suitConfig: map[int64]map[string]interface{}{
			400001: {
				"EquipArr": []interface{}{
					map[string]interface{}{
						"Job": json.Number("1"),
						"EquipIdArr": []interface{}{
							json.Number("120001"), json.Number("120002"),
							json.Number("120003"), json.Number("120004"),
						},
					},
				},
				"SuitArr": []interface{}{
					map[string]interface{}{"Key": json.Number("3"), "Value": json.Number("210")},
					map[string]interface{}{"Key": json.Number("27"), "Value": json.Number("0.15")},
				},
			},
		},
	}
	ss := &session{jobID: 1, worn: worn}
	if bonus := equipBonus(ss); bonus[1005] != 0 || bonus[1042] != 0 {
		t.Fatalf("incomplete suit bonus = %#v, want none", bonus)
	}

	ss.worn[3] = &bagItem{ItemId: equipIDs[3], ItemType: 1, Count: 1}
	bonus := equipBonus(ss)
	if bonus[1005] != 210 || math.Abs(float64(bonus[1042]-0.15)) > 0.00001 {
		t.Fatalf("complete suit bonus = %#v, want Str=210 and SuckR=0.15", bonus)
	}

	ss.jobID = 3
	if bonus := equipBonus(ss); bonus[1005] != 0 || bonus[1042] != 0 {
		t.Fatalf("wrong-profession suit bonus = %#v, want none", bonus)
	}
}
