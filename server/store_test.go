package main

import (
	"path/filepath"
	"testing"
)

func TestPlayerEnergyDefaultsAndPersists(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "energy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	accountID, err := store.CreateAccount("energy-test", "password")
	if err != nil {
		t.Fatal(err)
	}
	playerID, err := store.CreatePlayer(accountID, "energy-player", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	player, err := store.FirstPlayer(accountID)
	if err != nil {
		t.Fatal(err)
	}
	if player.Energy != 2000 {
		t.Fatalf("default energy = %d, want 2000", player.Energy)
	}
	if player.MapID != 1000601 || player.PosX != -1.8 || player.PosY != -0.84 {
		t.Fatalf("default location = (%d, %.2f, %.2f)", player.MapID, player.PosX, player.PosY)
	}

	itemBuffs := `{"1":{"item_id":110349,"effect_type":6,"expires_at":4102444800000,"multiplier":1.5}}`
	if err := store.SavePlayerData(playerID, 1, 0, 1997, 1000606, 3.25, -1.5, 0, 0, 0, 0, 0, 0,
		"", "", true, "", "", "",
		100000, 100, 100, 0, "", "", "", "", "", "", itemBuffs, 0, 0, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	player, err = store.FirstPlayer(accountID)
	if err != nil {
		t.Fatal(err)
	}
	if player.Energy != 1997 {
		t.Fatalf("persisted energy = %d, want 1997", player.Energy)
	}
	if player.MapID != 1000606 || player.PosX != 3.25 || player.PosY != -1.5 {
		t.Fatalf("persisted location = (%d, %.2f, %.2f)", player.MapID, player.PosX, player.PosY)
	}
	if player.SkillPoint != 0 {
		t.Fatalf("default skill point = %d, want 0", player.SkillPoint)
	}
	if player.ItemBuffs != itemBuffs {
		t.Fatalf("persisted item buffs = %q, want %q", player.ItemBuffs, itemBuffs)
	}
}

func TestPlayerProgressSurvivesDatabaseReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "progress.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	accountID, err := store.CreateAccount("progress-test", "password")
	if err != nil {
		t.Fatal(err)
	}
	playerID, err := store.CreatePlayer(accountID, "progress-player", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	bagJSON := `{"0":{"i":110101,"t":1,"c":1,"v":7}}`
	wornJSON := `{"1":{"i":110201,"t":1,"c":1,"v":12}}`
	if err = store.SavePlayerData(playerID, 20, 516152, 1973, 10004, -6.5, 2.25,
		19, 18, 4, 5, 6, 4, "100001:1,110101:1", "110101", true, "1001:2", bagJSON, wornJSON,
		123456, 789, 456, 321, "", "", "", "", "", "", "", 2, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	store.Close()

	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	player, err := store.FirstPlayer(accountID)
	if err != nil {
		t.Fatal(err)
	}
	if player.Level != 20 || player.Exp != 516152 || player.Energy != 1973 ||
		player.MapID != 10004 || player.PosX != -6.5 || player.PosY != 2.25 ||
		player.CharPoint != 19 || player.SkillPoint != 18 ||
		player.StrAdd != 4 || player.QukAdd != 5 || player.SpiAdd != 6 || player.WimAdd != 4 ||
		player.Skills != "100001:1,110101:1" || player.AutoSkills != "110101" || player.AutoBattle != 1 ||
		player.BagJSON != bagJSON || player.WornJSON != wornJSON || player.Trans != 2 {
		t.Fatalf("progress changed after reopen: %+v", player)
	}
}
