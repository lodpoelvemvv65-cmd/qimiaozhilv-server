package main

import (
	"fmt"
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCustomSkinOverlayRowsAreDirectIDOnly(t *testing.T) {
	raw, err := os.ReadFile("config/operations/CustomSkins.yaml")
	if err != nil {
		t.Fatalf("read CustomSkins.yaml: %v", err)
	}
	var source struct {
		Version int `yaml:"version"`
		Tables  map[string][]struct {
			ID     int64                  `yaml:"id"`
			Fields map[string]interface{} `yaml:"fields"`
		} `yaml:"tables"`
	}
	if err := yaml.Unmarshal(raw, &source); err != nil {
		t.Fatalf("decode CustomSkins.yaml: %v", err)
	}
	if source.Version != 1 {
		t.Fatalf("CustomSkins.yaml version = %d, want 1", source.Version)
	}
	for table, want := range map[string]map[int64]map[string]string{
		"EquipBase": {
			130001: {"Name": "朽木白哉", "Type": "2", "IconName": "130001"},
			130002: {"Name": "卍解一护", "Type": "2", "IconName": "130002"},
		},
		"SkinBase": {
			130001: {"PrfabId": "13001"},
			130002: {"PrfabId": "13002"},
		},
		"Sys_Prefab": {
			13001: {"AssetPath": "Assets/Download/Role/RolePrefab/Skin/SkinByakuya.prefab"},
			13002: {"AssetPath": "Assets/Download/Role/RolePrefab/Skin/SkinBankaiIchigo.prefab"},
		},
	} {
		rows := make(map[int64]map[string]interface{})
		for _, row := range source.Tables[table] {
			rows[row.ID] = row.Fields
		}
		for id, fields := range want {
			row, ok := rows[id]
			if !ok {
				t.Fatalf("%s YAML is missing row %d", table, id)
			}
			for key, expected := range fields {
				if got := fmt.Sprint(row[key]); got != expected {
					t.Errorf("%s[%d].%s = %q, want %q", table, id, key, got, expected)
				}
			}
		}
	}
}

func TestDirectSkinIDSurvivesLoadWithoutWornSkin(t *testing.T) {
	oldTables := tables
	tables = &datatables{
		skinBase:  map[int64]map[string]interface{}{130001: {}},
		equipBase: map[int64]map[string]interface{}{},
	}
	t.Cleanup(func() { tables = oldTables })
	s := &session{jobID: 1, skinID: 130001, worn: map[int32]*bagItem{}}
	applySkinEquipOnLoad(s)
	if s.skinID != 130001 {
		t.Fatalf("direct SkinId changed to %d", s.skinID)
	}
}

func TestCustomSkinEquipmentWinsOnLoad(t *testing.T) {
	oldTables := tables
	tables = &datatables{
		skinBase:  map[int64]map[string]interface{}{130001: {}},
		equipBase: map[int64]map[string]interface{}{130001: {"Type": int64(2)}},
	}
	t.Cleanup(func() { tables = oldTables })
	s := &session{jobID: 1, skinID: 1, worn: map[int32]*bagItem{2: {ItemId: 130001, ItemType: 1}}}
	applySkinEquipOnLoad(s)
	if s.skinID != 130001 {
		t.Fatalf("worn custom skin changed to %d", s.skinID)
	}
}
