package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDecodeSourceFileYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ShopBase.yaml")
	content := "- - 10001\n  - _id: 10001\n    ItemType: 2\n    Price: 25.5\n    Enabled: true\n    Name: potion\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := decodeSourceFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := value.([]interface{})
	if !ok || len(rows) != 1 {
		t.Fatalf("YAML rows = %#v", value)
	}
	pair := rows[0].([]interface{})
	if got, err := numberInt(pair[0]); err != nil || got != 10001 {
		t.Fatalf("YAML key = %v, %v", got, err)
	}
	row := pair[1].(map[string]interface{})
	if row["Enabled"] != true || row["Name"] != "potion" {
		t.Fatalf("YAML scalar normalization = %#v", row)
	}
	if got, ok := row["Price"].(float64); !ok || got != 25.5 {
		t.Fatalf("YAML numeric normalization = %#v", row["Price"])
	}
}

func TestSourcePathPrefersYAML(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ShopBase.json"), []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ShopBase.yaml"), []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := sourcePath(dir, "ShopBase")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(path) != ".yaml" {
		t.Fatalf("sourcePath selected %s, want YAML", path)
	}
}

func TestDecodeSourceFileRejectsJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ItemUpgrade.json")
	content := `[[20059,{"_id":20059,"SucceefulRate":0.7,"UpgradeNeedMaterialArr":[{"UpgradeNeedMaterial_Id":20059,"UpgradeNeedMaterial_Count":4}]}]]`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeSourceFile(path); err == nil {
		t.Fatal("JSON source was accepted; server config inputs must be YAML/YML")
	}
}
