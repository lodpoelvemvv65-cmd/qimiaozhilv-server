package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDecodeSourceAcceptsYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "WorldBossConfig.yaml")
	if err := os.WriteFile(path, []byte("- - 1\n  - _id: 1\n    Interval: 30\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := decodeSource(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := value.([]interface{})
	if !ok || len(rows) != 1 {
		t.Fatalf("decoded rows = %#v", value)
	}
	pair := rows[0].([]interface{})
	if id, err := numberInt(pair[0]); err != nil || id != 1 {
		t.Fatalf("decoded id = %d, err=%v", id, err)
	}
}

func TestDecodeSourceRejectsNonYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "WorldBossConfig.json")
	if err := os.WriteFile(path, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeSource(path); err == nil {
		t.Fatal("non-YAML source was accepted")
	}
}

func TestValidateGameplayConfigAndRejectUnknownFields(t *testing.T) {
	path := filepath.Join("..", "..", "config", "operations", "Gameplay.yaml")
	value, err := decodeSource(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateConfig("Gameplay", value); err != nil {
		t.Fatalf("valid Gameplay config rejected: %v", err)
	}
	root := value.(map[string]interface{})
	root["misspelled_section"] = true
	if err := validateConfig("Gameplay", root); err == nil {
		t.Fatal("unknown Gameplay field was accepted")
	}
}

func TestValidateAffixWashConfigAndRejectUnknownFields(t *testing.T) {
	path := filepath.Join("..", "..", "config", "operations", "AffixWash.yaml")
	value, err := decodeSource(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateConfig("AffixWash", value); err != nil {
		t.Fatalf("valid AffixWash config rejected: %v", err)
	}
	root := value.(map[string]interface{})
	root["misspelled_section"] = true
	if err := validateConfig("AffixWash", root); err == nil {
		t.Fatal("unknown AffixWash field was accepted")
	}
}
