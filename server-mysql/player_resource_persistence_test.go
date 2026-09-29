package main

import (
	"math"
	"path/filepath"
	"testing"
)

func TestLoadDataRestoresPersistedCurrentResources(t *testing.T) {
	loadOnlineTablesForTest(t)
	player := &Player{
		ID: 1, JobID: 1, SkinID: 1, Level: 1, Energy: 1000,
		MapID: 10004, CurrentHP: 31, CurrentMP: 17,
	}
	ss := newSession()
	ss.playerID, ss.jobID, ss.skinID = player.ID, player.JobID, player.SkinID
	ss.loadData(player)

	if ss.hp != 31 || ss.mp != 17 || ss.battleHP() != 31 || ss.battleMP() != 17 {
		t.Fatalf("restored resources session=%d/%d current=%d/%d, want 31/17",
			ss.hp, ss.mp, ss.battleHP(), ss.battleMP())
	}
}

func TestLoadDataPreservesZeroAndOnlyClampsValuesAboveMaximum(t *testing.T) {
	loadOnlineTablesForTest(t)
	zero := &Player{
		ID: 1, JobID: 1, SkinID: 1, Level: 1, Energy: 1000,
		MapID: 10004, CurrentHP: 0, CurrentMP: 0,
	}
	zeroSession := newSession()
	zeroSession.playerID, zeroSession.jobID, zeroSession.skinID = zero.ID, zero.JobID, zero.SkinID
	zeroSession.loadData(zero)
	if zeroSession.battleHP() != 0 || zeroSession.battleMP() != 0 {
		t.Fatalf("zero resources were refilled to %d/%d", zeroSession.battleHP(), zeroSession.battleMP())
	}

	over := *zero
	over.CurrentHP, over.CurrentMP = math.MaxInt32, math.MaxInt32
	overSession := newSession()
	overSession.playerID, overSession.jobID, overSession.skinID = over.ID, over.JobID, over.SkinID
	if !overSession.loadData(&over) {
		t.Fatal("maximum clamp was not reported as a persistence migration")
	}
	if overSession.hp != overSession.playerMaxHp() || overSession.mp != overSession.playerMaxMp() {
		t.Fatalf("clamped resources=%d/%d, want maxima=%d/%d",
			overSession.hp, overSession.mp, overSession.playerMaxHp(), overSession.playerMaxMp())
	}
}

func TestPlayerCurrentResourcesPersistAcrossDatabaseReopen(t *testing.T) {
	dsn := mysqlTestDSN(t, filepath.Join(t.TempDir(), "current-resources"))
	store, err := OpenStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	accountID, err := store.CreateAccount("resource-persist", "password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePlayer(accountID, "resource-player", 1, 1); err != nil {
		t.Fatal(err)
	}
	player, err := store.FirstPlayer(accountID)
	if err != nil {
		t.Fatal(err)
	}
	if player.CurrentHP != -1 || player.CurrentMP != -1 {
		t.Fatalf("new role resources=%d/%d, want uninitialized -1/-1", player.CurrentHP, player.CurrentMP)
	}
	if err := store.SavePlayerResources(player.ID, 321, 123); err != nil {
		t.Fatal(err)
	}
	store.Close()

	store, err = OpenStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	player, err = store.FirstPlayer(accountID)
	if err != nil {
		t.Fatal(err)
	}
	if player.CurrentHP != 321 || player.CurrentMP != 123 {
		t.Fatalf("reopened resources=%d/%d, want 321/123", player.CurrentHP, player.CurrentMP)
	}
}
