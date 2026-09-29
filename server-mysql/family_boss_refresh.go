package main

import (
	"sync"
	"time"
)

var familyBossClock = struct {
	mu  sync.Mutex
	now func() time.Time
}{now: time.Now}

func familyBossNow() time.Time {
	familyBossClock.mu.Lock()
	defer familyBossClock.mu.Unlock()
	if familyBossClock.now == nil {
		return time.Now()
	}
	return familyBossClock.now()
}

func setFamilyBossNowForTest(now time.Time) {
	familyBossClock.mu.Lock()
	defer familyBossClock.mu.Unlock()
	captured := now
	familyBossClock.now = func() time.Time { return captured }
}

func resetFamilyBossClockForTest() {
	familyBossClock.mu.Lock()
	defer familyBossClock.mu.Unlock()
	familyBossClock.now = time.Now
}

func familyBossOperatingDay(now time.Time) string {
	return gameplayDungeonDay(now)
}

func rememberFamilyBossDeath(st *familyBossHP) {
	if st == nil {
		return
	}
	if st.Hp > 0 {
		st.DeadAt = 0
		return
	}
	if st.DeadAt <= 0 {
		st.DeadAt = familyBossNow().UnixMilli()
	}
}

func applyFamilyBossDailyRefreshLocked(arr *[5]familyBossHP, now time.Time) (changed bool, revived []int32) {
	if arr == nil {
		return false, nil
	}
	currentDay := familyBossOperatingDay(now)
	for i := 0; i < 5; i++ {
		st := &arr[i]
		if st.Hp > 0 {
			if st.DeadAt != 0 {
				st.DeadAt = 0
				changed = true
			}
			continue
		}
		if st.MaxHp <= 0 && st.DeadAt <= 0 && !st.HasReward {
			st.MaxHp = familyBossMaxHP(int32(i + 1))
			if st.MaxHp <= 0 {
				st.MaxHp = 1
			}
			st.Hp = st.MaxHp
			changed = true
			continue
		}
		if st.DeadAt <= 0 {
			st.DeadAt = now.UnixMilli()
			changed = true
			continue
		}
		if currentDay <= familyBossOperatingDay(time.UnixMilli(st.DeadAt)) {
			continue
		}
		st.MaxHp = familyBossMaxHP(int32(i + 1))
		if st.MaxHp <= 0 {
			st.MaxHp = 1
		}
		st.Hp = st.MaxHp
		st.HasReward = false
		st.DeadAt = 0
		changed = true
		revived = append(revived, int32(i+1))
	}
	return changed, revived
}

func refreshFamilyBossStatesLocked(familyID int64, arr *[5]familyBossHP, now time.Time) {
	changed, revived := applyFamilyBossDailyRefreshLocked(arr, now)
	if !changed {
		return
	}
	saveFamilyBossStateDB(familyID, arr)
	for _, bossID := range revived {
		clearFamilyBossDamage(familyID, bossID)
		clearFamilyBossRewardClaimsForBoss(familyID, bossID)
	}
}

func refreshCachedFamilyBossStates(now time.Time) {
	familyBossStates.mu.Lock()
	defer familyBossStates.mu.Unlock()
	for familyID, arr := range familyBossStates.m {
		refreshFamilyBossStatesLocked(familyID, arr, now)
	}
}
