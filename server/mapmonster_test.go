package main

import (
	"encoding/json"
	"testing"
)

func TestConfigIDForMonsterUsesOnlinePrefabRow(t *testing.T) {
	oldTables := tables
	t.Cleanup(func() { tables = oldTables })
	tables = &datatables{monsterBase: map[int64]map[string]interface{}{
		10001: {"PrefabId": json.Number("281")},
	}, mapMonsterConfig: map[int64]map[string]interface{}{
		1006: {"PrefabId": json.Number("281")},
	}}

	if got := configIDForMonsterID(10001); got != 1006 {
		t.Fatalf("configIDForMonsterID(10001) = %d, want online row 1006", got)
	}
	if got := configIDForMonsterID(0); got != 1011 {
		t.Fatalf("fallback config id = %d, want online row 1011", got)
	}
}
