package main

import (
	"testing"
	"time"

	"mhqserver/internal/operationsconfig"
)

// statusWithModifier walks a real client plan and returns the status the given
// modifier lands on, so the assertions below run against the shipped online
// numbers instead of a hand-written fixture.
func statusWithModifier(t *testing.T, skillID, level int32, modifierID int64) *SkillStatusPlan {
	t.Helper()
	catalog := loadTestSkillLogic(t)
	plan, err := catalog.Plan(skillID, level)
	if err != nil {
		t.Fatalf("Plan(%d, %d): %v", skillID, level, err)
	}
	var found *SkillStatusPlan
	var walk func(effects []SkillEffect)
	walk = func(effects []SkillEffect) {
		for _, effect := range effects {
			if effect.Status != nil && effect.Status.ModifierID == modifierID {
				found = effect.Status
			}
			walk(effect.Children)
			walk(effect.Success)
			walk(effect.Failure)
		}
	}
	walk(plan.Effects)
	if found == nil {
		t.Fatalf("modifier %d is not reachable from skill %d level %d", modifierID, skillID, level)
	}
	return found
}

// countKind counts one kind of combat event and ignores the bookkeeping events a
// tick batch also carries.
func countKind(events []CombatEvent, kind CombatEventType) int {
	count := 0
	for _, event := range events {
		if event.Type == kind {
			count++
		}
	}
	return count
}

// The cadence is an operator value. No client table states it, so the shipped
// default is the 4000ms measured from the online HOT capture. Only modifiers
// with separate capture evidence override it.
func TestPeriodicTickIntervalComesFromOperationsConfig(t *testing.T) {
	want := time.Duration(operationsconfig.DefaultEffectTickIntervalMS) * time.Millisecond
	if want != 4*time.Second {
		t.Fatalf("captured cadence default drifted to %v, want 4s", want)
	}
	for _, plan := range []struct {
		skillID    int32
		level      int32
		modifierID int64
	}{
		{110503, 4, 11050319}, // 流血, thinkInterval empty
		{310202, 1, 31020211}, // 中毒, thinkInterval 5s
		{110603, 3, 11060318}, // 点燃, thinkInterval 5s
		{310501, 1, 31050111}, // 持续治疗, thinkInterval 5s
	} {
		status := statusWithModifier(t, plan.skillID, plan.level, plan.modifierID)
		if got := periodicTickInterval(status); got != want {
			t.Fatalf("modifier %d tick interval = %v, want the configured %v",
				plan.modifierID, got, want)
		}
	}

	// A plan that carries no timing at all must not fall back to a one-second
	// timer, which is what made a ten-second bleed land ten hits in one round.
	if got := periodicTickInterval(&SkillStatusPlan{DurationSeconds: 10}); got != want {
		t.Fatalf("unset tick interval = %v, want %v", got, want)
	}
	if got := periodicTickInterval(nil); got != want {
		t.Fatalf("nil plan tick interval = %v, want %v", got, want)
	}
	poison := statusWithModifier(t, 500010, 1, 50001033)
	if got := periodicTickInterval(poison); got != 3500*time.Millisecond {
		t.Fatalf("captured boss poison interval = %v, want 3.5s", got)
	}

	// 热重载：运营侧改这个键，节拍必须立刻跟着变。
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.Combat.EffectTickIntervalMS = 2500
		config.Combat.PeriodicTickOverridesMS["50001033"] = 3200
	})
	if got := periodicTickInterval(nil); got != 2500*time.Millisecond {
		t.Fatalf("configured tick interval = %v, want 2.5s", got)
	}
	if got := periodicTickInterval(poison); got != 3200*time.Millisecond {
		t.Fatalf("configured poison override = %v, want 3.2s", got)
	}
}

func TestBossPoisonTicksAtCaptured3500MillisecondCadence(t *testing.T) {
	clock := &fakeCombatClock{now: time.Unix(1_900, 0)}
	battle, player, target := newOverlayBattleForTest(clock)
	status := statusWithModifier(t, 500010, 1, 50001033)
	if _, err := battle.applyPeriodic(SkillEffect{}, status, player, target, 10, true); err != nil {
		t.Fatal(err)
	}

	clock.Add(3500*time.Millisecond - time.Millisecond)
	if got := countKind(battle.runtime.Tick(), CombatEventDamage); got != 0 {
		t.Fatalf("boss poison ticked at 3499ms: %d hits", got)
	}
	clock.Add(time.Millisecond)
	if got := countKind(battle.runtime.Tick(), CombatEventDamage); got != 1 {
		t.Fatalf("boss poison first tick at 3500ms = %d hits, want 1", got)
	}
	clock.Add(3500 * time.Millisecond)
	if got := countKind(battle.runtime.Tick(), CombatEventDamage); got != 1 {
		t.Fatalf("boss poison second tick at 7000ms = %d hits, want 1", got)
	}
}

// 线上抓包实测：首跳延后一个间隔，跳数 = floor(持续时间 / 间隔)。
//
// 这让 10 秒的流血落两跳（+4.00s 与 +8.00s）。第三跳要到 +12s、已超出 10 秒，
// 所以在过期时刻只有过期事件、没有伤害。
func TestTenSecondBleedTicksTwiceAtTheCapturedCadence(t *testing.T) {
	clock := &fakeCombatClock{now: time.Unix(2_000, 0)}
	battle, player, target := newOverlayBattleForTest(clock)

	status := statusWithModifier(t, 110503, 4, 11050319)
	if status.DurationSeconds != 10 {
		t.Fatalf("fixture drifted: 流血 duration = %v, want the shipped 10s", status.DurationSeconds)
	}
	if _, err := battle.applyPeriodic(SkillEffect{}, status, player, target, 20, true); err != nil {
		t.Fatalf("applyPeriodic: %v", err)
	}

	// 挂上瞬间不跳伤：抓包里挂上 bufficon 的那一刻没有任何伤害/治疗帧。
	if got := countKind(battle.runtime.Tick(), CombatEventDamage); got != 0 {
		t.Fatalf("bleed ticked the moment it landed: %d hits", got)
	}

	clock.Add(4*time.Second - time.Millisecond)
	if got := countKind(battle.runtime.Tick(), CombatEventDamage); got != 0 {
		t.Fatalf("bleed ticked before one full interval: %d hits", got)
	}

	clock.Add(time.Millisecond)
	if got := countKind(battle.runtime.Tick(), CombatEventDamage); got != 1 {
		t.Fatalf("first tick at +4s = %d hits, want 1", got)
	}
	if got, _ := battle.runtime.Unit(target); got.HP != 980 {
		t.Fatalf("bleed target hp after tick one = %d, want 980", got.HP)
	}

	clock.Add(4 * time.Second)
	if got := countKind(battle.runtime.Tick(), CombatEventDamage); got != 1 {
		t.Fatalf("second tick at +8s = %d hits, want 1", got)
	}
	if got, _ := battle.runtime.Unit(target); got.HP != 960 {
		t.Fatalf("bleed target hp after tick two = %d, want 960 (two 20-point ticks)", got.HP)
	}

	// 第三跳要到 +12s，而 10 秒时长在 +10s 就结束：只过期，不再跳伤。
	clock.Add(2 * time.Second)
	events := battle.runtime.Tick()
	if got := countKind(events, CombatEventDamage); got != 0 {
		t.Fatalf("bleed ticked past its duration: %d hits", got)
	}
	if got := countKind(events, CombatEventEffectExpired); got != 1 {
		t.Fatalf("bleed expiry events = %d, want 1: %+v", got, events)
	}
	if got, _ := battle.runtime.Unit(target); got.HP != 960 {
		t.Fatalf("bleed target hp after expiry = %d, want 960", got.HP)
	}
}

// 14 秒的中毒落三跳（+4/+8/+12），不跟着它自己的 thinkInterval=5 在 5 秒和 10 秒
// 各跳一次；+16s 的第 4 跳超出时长，永远不该发生。抓包里 14 秒的持续治疗正好也是
// 三跳。
func TestFourteenSecondPoisonTicksThreeTimesIgnoringThinkInterval(t *testing.T) {
	clock := &fakeCombatClock{now: time.Unix(3_000, 0)}
	battle, player, target := newOverlayBattleForTest(clock)

	status := statusWithModifier(t, 310202, 1, 31020211)
	if status.DurationSeconds != 14 {
		t.Fatalf("fixture drifted: 中毒 duration = %v, want the shipped 14s", status.DurationSeconds)
	}
	if status.TickSeconds != 5 {
		t.Fatalf("fixture drifted: 中毒 thinkInterval = %v, want the shipped 5s", status.TickSeconds)
	}
	if _, err := battle.applyPeriodic(SkillEffect{}, status, player, target, 10, true); err != nil {
		t.Fatalf("applyPeriodic: %v", err)
	}

	// +4s：节拍第一跳。
	clock.Add(4 * time.Second)
	if got := countKind(battle.runtime.Tick(), CombatEventDamage); got != 1 {
		t.Fatalf("first tick at +4s = %d hits, want 1", got)
	}
	// +5s：thinkInterval 到点，但节拍在 +4s 已经跳过了，不该再跳。
	clock.Add(time.Second)
	if got := countKind(battle.runtime.Tick(), CombatEventDamage); got != 0 {
		t.Fatalf("thinkInterval fired an extra tick at +5s: %d hits", got)
	}
	// +8s / +12s：第二、第三跳。
	clock.Add(3 * time.Second)
	if got := countKind(battle.runtime.Tick(), CombatEventDamage); got != 1 {
		t.Fatalf("second tick at +8s = %d hits, want 1", got)
	}
	clock.Add(4 * time.Second)
	if got := countKind(battle.runtime.Tick(), CombatEventDamage); got != 1 {
		t.Fatalf("third tick at +12s = %d hits, want 1", got)
	}
	if got, _ := battle.runtime.Unit(target); got.HP != 970 {
		t.Fatalf("中毒 target hp = %d, want 970 (three 10-point ticks)", got.HP)
	}

	// +16s 超出 14 秒时长。
	clock.Add(4 * time.Second)
	events := battle.runtime.Tick()
	if got := countKind(events, CombatEventDamage); got != 0 {
		t.Fatalf("fourth tick past the 14s duration = %d hits, want 0", got)
	}
	if got, _ := battle.runtime.Unit(target); got.HP != 970 {
		t.Fatalf("中毒 target hp after expiry = %d, want 970", got.HP)
	}
}

// 持续治疗（HOT）走同一套节拍：线上抓包里护士三次施放 310501 都是挂上后
// +4.00/+8.00/+12.00 三跳，这里用同一个 14 秒 modifier 复核。
func TestFourteenSecondTreatmentTicksThreeTimes(t *testing.T) {
	clock := &fakeCombatClock{now: time.Unix(4_000, 0)}
	battle, player, _ := newOverlayBattleForTest(clock)
	battle.playerHP = 100 // 留出治疗空间，否则满血会被上限截掉

	status := statusWithModifier(t, 310501, 1, 31050111)
	if status.DurationSeconds != 14 {
		t.Fatalf("fixture drifted: 持续治疗 duration = %v, want the shipped 14s", status.DurationSeconds)
	}
	if _, err := battle.applyPeriodic(SkillEffect{}, status, player, player, 10, false); err != nil {
		t.Fatalf("applyPeriodic: %v", err)
	}

	if got := countKind(battle.runtime.Tick(), CombatEventHeal); got != 0 {
		t.Fatalf("treatment healed the moment it landed: %d ticks", got)
	}
	for tick := 1; tick <= 3; tick++ {
		clock.Add(4 * time.Second)
		if got := countKind(battle.runtime.Tick(), CombatEventHeal); got != 1 {
			t.Fatalf("treatment tick %d = %d heals, want 1", tick, got)
		}
	}
	if battle.playerHP != 130 {
		t.Fatalf("treatment healed player to %d, want 130 (three 10-point ticks)", battle.playerHP)
	}

	// +16s 的第 4 跳超出 14 秒。
	clock.Add(4 * time.Second)
	if got := countKind(battle.runtime.Tick(), CombatEventHeal); got != 0 {
		t.Fatalf("fourth treatment tick past the 14s duration = %d, want 0", got)
	}
}
