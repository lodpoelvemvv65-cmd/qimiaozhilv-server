package main

import (
	"path/filepath"
	"testing"
	"time"
)

func familyBossCST(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, time.FixedZone("dungeon", 8*3600))
}

func resetFamilyBossCache(t *testing.T, familyID int64) {
	t.Helper()
	familyBossStates.mu.Lock()
	delete(familyBossStates.m, familyID)
	familyBossStates.mu.Unlock()
	for bossID := int32(1); bossID <= 5; bossID++ {
		clearFamilyBossDamage(familyID, bossID)
		clearFamilyBossRewardClaimsForBoss(familyID, bossID)
	}
	t.Cleanup(func() {
		resetFamilyBossClockForTest()
		familyBossStates.mu.Lock()
		delete(familyBossStates.m, familyID)
		familyBossStates.mu.Unlock()
		for bossID := int32(1); bossID <= 5; bossID++ {
			clearFamilyBossDamage(familyID, bossID)
			clearFamilyBossRewardClaimsForBoss(familyID, bossID)
		}
	})
}

func TestFamilyBossDailyRefreshWaitsUntilNextOperatingDay(t *testing.T) {
	loadOnlineTablesForTest(t)
	const familyID int64 = 91001
	resetFamilyBossCache(t, familyID)
	killAt := familyBossCST(2026, 9, 9, 15, 0)
	setFamilyBossNowForTest(killAt)
	state := familyBossState(familyID, 1)
	state.Hp = 0
	state.HasReward = true
	rememberFamilyBossDeath(state)
	if state.DeadAt != killAt.UnixMilli() {
		t.Fatalf("dead_at=%d, want %d", state.DeadAt, killAt.UnixMilli())
	}

	setFamilyBossNowForTest(familyBossCST(2026, 9, 9, 23, 59))
	sameDay := familyBossState(familyID, 1)
	if sameDay.Hp != 0 || !sameDay.HasReward || sameDay.DeadAt != killAt.UnixMilli() {
		t.Fatalf("same-day family boss = %+v", sameDay)
	}

	setFamilyBossNowForTest(familyBossCST(2026, 9, 10, 0, 0))
	nextDay := familyBossState(familyID, 1)
	if nextDay.Hp != familyBossMaxHP(1) || nextDay.HasReward || nextDay.DeadAt != 0 {
		t.Fatalf("next-day family boss = %+v", nextDay)
	}
}

func TestFamilyBossDailyRefreshDoesNotResetLivingHP(t *testing.T) {
	loadOnlineTablesForTest(t)
	const familyID int64 = 91002
	resetFamilyBossCache(t, familyID)
	setFamilyBossNowForTest(familyBossCST(2026, 9, 9, 12, 0))
	state := familyBossState(familyID, 1)
	state.Hp = 12345678
	state.HasReward = false
	state.DeadAt = 0
	setFamilyBossNowForTest(familyBossCST(2026, 9, 10, 8, 0))
	got := familyBossState(familyID, 1)
	if got.Hp != 12345678 || got.HasReward || got.DeadAt != 0 {
		t.Fatalf("living family boss reset = %+v", got)
	}
}

func TestFamilyBossDeathTimestampDoesNotMoveOnRepeatedPersist(t *testing.T) {
	loadOnlineTablesForTest(t)
	const familyID int64 = 91003
	resetFamilyBossCache(t, familyID)
	killAt := familyBossCST(2026, 9, 9, 18, 30)
	setFamilyBossNowForTest(killAt)
	state := familyBossState(familyID, 1)
	state.Hp = 0
	rememberFamilyBossDeath(state)
	first := state.DeadAt
	setFamilyBossNowForTest(killAt.Add(2 * time.Hour))
	persistFamilyBossProgress(familyID, &battleState{
		mapID:    -1,
		monsters: []*monsterUnit{{hp: 0}},
	})
	got := familyBossState(familyID, 1)
	if got.DeadAt != first {
		t.Fatalf("dead_at moved from %d to %d", first, got.DeadAt)
	}
}

func TestFamilyBossDailyRefreshClearsDamageAndClaims(t *testing.T) {
	loadOnlineTablesForTest(t)
	const familyID int64 = 91004
	const playerID int64 = 88
	resetFamilyBossCache(t, familyID)
	setFamilyBossNowForTest(familyBossCST(2026, 9, 9, 10, 0))
	state := familyBossState(familyID, 1)
	state.Hp = 0
	state.HasReward = true
	rememberFamilyBossDeath(state)
	recordFamilyBossDamage(familyID, 1, playerID, 99)
	server := &Server{}
	if !server.grantFamilyBossReward(familyID, 1, playerID) || !server.canClaimFamilyBossReward(familyID, 1, playerID) {
		t.Fatal("claim was not granted")
	}
	setFamilyBossNowForTest(familyBossCST(2026, 9, 10, 0, 1))
	got := familyBossState(familyID, 1)
	if got.Hp != familyBossMaxHP(1) || got.HasReward {
		t.Fatalf("refreshed family boss = %+v", got)
	}
	if dmg := familyBossDamageSnapshot(familyID, 1); len(dmg) != 0 {
		t.Fatalf("damage ranking survived refresh: %v", dmg)
	}
	if server.canClaimFamilyBossReward(familyID, 1, playerID) {
		t.Fatal("reward claim survived refresh")
	}
}

func TestFamilyBossDailyRefreshSurvivesRestart(t *testing.T) {
	loadOnlineTablesForTest(t)
	dsn := mysqlTestDSN(t, filepath.Join(t.TempDir(), "family-boss-daily-refresh.db"))
	store, err := OpenStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	oldServer := globalServer
	globalServer = &Server{store: store}
	t.Cleanup(func() { globalServer = oldServer })

	const familyID int64 = 91005
	resetFamilyBossCache(t, familyID)
	killAt := familyBossCST(2026, 9, 9, 21, 0)
	setFamilyBossNowForTest(killAt)
	state := familyBossState(familyID, 1)
	state.Hp = 0
	state.HasReward = true
	rememberFamilyBossDeath(state)
	saveFamilyBossStateDB(familyID, familyBossAllStates(familyID))
	store.Close()

	store, err = OpenStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	globalServer.store = store
	familyBossStates.mu.Lock()
	delete(familyBossStates.m, familyID)
	familyBossStates.mu.Unlock()
	setFamilyBossNowForTest(familyBossCST(2026, 9, 9, 23, 50))
	sameDay := familyBossState(familyID, 1)
	if sameDay.Hp != 0 || !sameDay.HasReward || sameDay.DeadAt != killAt.UnixMilli() {
		t.Fatalf("restart same day = %+v", sameDay)
	}

	familyBossStates.mu.Lock()
	delete(familyBossStates.m, familyID)
	familyBossStates.mu.Unlock()
	setFamilyBossNowForTest(familyBossCST(2026, 9, 10, 0, 5))
	nextDay := familyBossState(familyID, 1)
	if nextDay.Hp != familyBossMaxHP(1) || nextDay.HasReward || nextDay.DeadAt != 0 {
		t.Fatalf("restart next day = %+v", nextDay)
	}
}

func TestFamilyBossLegacyDeadRowStampsTimeInsteadOfRevivingImmediately(t *testing.T) {
	loadOnlineTablesForTest(t)
	dsn := mysqlTestDSN(t, filepath.Join(t.TempDir(), "family-boss-legacy-dead.db"))
	store, err := OpenStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	oldServer := globalServer
	globalServer = &Server{store: store}
	t.Cleanup(func() { globalServer = oldServer })

	const familyID int64 = 91006
	resetFamilyBossCache(t, familyID)
	firstSeen := familyBossCST(2026, 9, 9, 16, 0)
	setFamilyBossNowForTest(firstSeen)
	if _, err := store.db.Exec(`INSERT INTO family_boss_states (family_id, boss_id, hp, max_hp, has_reward)
		VALUES (?, 1, 0, ?, 1)`, familyID, familyBossMaxHP(1)); err != nil {
		t.Fatal(err)
	}
	stamped := familyBossState(familyID, 1)
	if stamped.Hp != 0 || stamped.DeadAt != firstSeen.UnixMilli() {
		t.Fatalf("legacy stamp = %+v", stamped)
	}
	familyBossStates.mu.Lock()
	delete(familyBossStates.m, familyID)
	familyBossStates.mu.Unlock()
	setFamilyBossNowForTest(familyBossCST(2026, 9, 10, 0, 0))
	revived := familyBossState(familyID, 1)
	if revived.Hp != familyBossMaxHP(1) || revived.DeadAt != 0 {
		t.Fatalf("legacy revive = %+v", revived)
	}
}
