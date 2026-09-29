package main

import (
	"encoding/json"
	"testing"
)

func TestBlueMechSkinIsExposedWithPackagedPrefab(t *testing.T) {
	skins, err := loadKVTable("../datatable_json/SkinBase.json")
	if err != nil {
		t.Fatalf("load SkinBase: %v", err)
	}

	for _, skinID := range []int64{120642, 120643} {
		skin, ok := skins[skinID]
		if !ok {
			t.Errorf("SkinBase is missing blue mech skin %d", skinID)
			continue
		}
		if got := skin["PrfabId"]; got != json.Number("34") {
			t.Errorf("SkinBase[%d].PrfabId = %d, want 34", skinID, got)
		}
	}

	prefabs, err := loadKVTable("../datatable_json/Sys_Prefab.json")
	if err != nil {
		t.Fatalf("load Sys_Prefab: %v", err)
	}
	prefab, ok := prefabs[34]
	if !ok {
		t.Fatal("Sys_Prefab is missing Skin25 prefab 34")
	}
	got, err := configString(prefab["AssetPath"], "AssetPath")
	if err != nil {
		t.Fatalf("Sys_Prefab[34].AssetPath: %v", err)
	}
	if got != "Assets/Download/Role/RolePrefab/Skin/Skin25.prefab" {
		t.Errorf("Sys_Prefab[34].AssetPath = %q, want Skin25 prefab", got)
	}

	mapMonsters, err := loadKVTable("../datatable_json/MapMonsterConfig.json")
	if err != nil {
		t.Fatalf("load MapMonsterConfig: %v", err)
	}
	if _, ok := mapMonsters[1017]; ok {
		t.Error("MapMonsterConfig still contains the unwanted row 1017")
	}
}
