package main

import (
	"testing"
	"time"
)

func TestApplyGMActivityAdjustSetsQuotas(t *testing.T) {
	ss := &session{signin: &signinState{DungeonQuotaDay: "20200101", SpaceTravelRemaining: 1, DeathTowerRemaining: 1, FamilyBossKeys: 0}}
	space := "9"
	tower := "4"
	keys := "3"
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.FixedZone("CST", 8*3600))
	if err := applyGMActivityAdjust(ss, gmActivityPayload{SpaceTravelRemaining: &space, DeathTowerRemaining: &tower, FamilyBossKeys: &keys}, now); err != nil {
		t.Fatal(err)
	}
	if ss.signin.SpaceTravelRemaining != 9 || ss.signin.DeathTowerRemaining != 4 || ss.signin.FamilyBossKeys != 3 {
		t.Fatalf("signin=%+v", ss.signin)
	}
	if ss.signin.DungeonQuotaDay != gameplayDungeonDay(now) {
		t.Fatalf("day=%s", ss.signin.DungeonQuotaDay)
	}
}
