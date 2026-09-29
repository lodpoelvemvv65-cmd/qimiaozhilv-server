package main

import (
	"testing"
	"time"

	"mhqserver/internal/operationsconfig"
)

func newOverlayBattleForTest(clock *fakeCombatClock) (*battleState, CombatUnitRef, CombatUnitRef) {
	monster := &monsterUnit{id: 200, hp: 1_000, maxHP: 1_000, phyAtk: 40, phyDef: 10, alive: true}
	battle := &battleState{
		playerHP: 1_000, playerMaxHP: 1_000, playerMP: 1_000, playerMaxMP: 1_000,
		phyAtk: 30, phyDef: 5, monsters: []*monsterUnit{monster},
	}
	battle.runtime = NewCombatRuntime(battle, 1_000, clock)
	return battle, battle.runtime.Player(), MonsterCombatUnit(monster.id)
}

func periodicDamage(events []CombatEvent) (count int, total int32) {
	for _, event := range events {
		if event.Type == CombatEventDamage {
			count++
			total += event.Amount
		}
	}
	return count, total
}

// 210403 level 3 and 110503 level 4 are both 流血 (overlayType 2, stateK 105) and
// both stack (attribute 12), so the second cast has to join the 流血 already
// running instead of opening a second one that no icon and no state packet ever
// mentions. The joined bucket owes one tick of the sum of its layers.
func TestStackingBleedsOfOneOverlayFamilyShareASlot(t *testing.T) {
	clock := &fakeCombatClock{now: time.Unix(4_000, 0)}
	battle, player, target := newOverlayBattleForTest(clock)

	first := statusWithModifier(t, 210403, 3, 21040312)
	second := statusWithModifier(t, 110503, 4, 11050319)
	for _, status := range []*SkillStatusPlan{first, second} {
		if status.OverlayType != 2 || status.StateKey != 105 {
			t.Fatalf("fixture drifted: overlayType=%d stateK=%d, want the 流血 family 2/105",
				status.OverlayType, status.StateKey)
		}
		if stackMode(status) != CombatEffectStack {
			t.Fatalf("fixture drifted: modifier %d stack mode = %v, want stack", status.ModifierID, stackMode(status))
		}
	}

	if _, err := battle.applyPeriodic(SkillEffect{}, first, player, target, 12, true); err != nil {
		t.Fatalf("first 流血: %v", err)
	}
	if _, err := battle.applyPeriodic(SkillEffect{}, second, player, target, 20, true); err != nil {
		t.Fatalf("second 流血: %v", err)
	}

	effects := battle.runtime.state().effects
	if len(effects) != 1 {
		t.Fatalf("流血 effects = %d, want one shared slot: %+v", len(effects), effects)
	}
	if effects[0].key != "overlay:2:dot" || effects[0].stacks != 2 || effects[0].overlayValue != 32 {
		t.Fatalf("shared 流血 bucket = key %q stacks %d value %d, want overlay:2:dot / 2 / 32",
			effects[0].key, effects[0].stacks, effects[0].overlayValue)
	}
	if meta := battle.effectMeta["overlay:2:dot"]; meta.stateKey != 105 || meta.isBuff || meta.iconID != "bufficon_bleed" {
		t.Fatalf("shared 流血 meta = %+v, want the 流血 state 105 / bufficon_bleed / debuff", meta)
	}

	clock.Add(gameplayEffectTickInterval())
	count, total := periodicDamage(battle.runtime.Tick())
	if count != 1 || total != 32 {
		t.Fatalf("shared 流血 ticked %d times for %d, want one 32-point tick", count, total)
	}
	if got, _ := battle.runtime.Unit(target); got.HP != 968 {
		t.Fatalf("shared 流血 target hp = %d, want 968", got.HP)
	}
}

// 210502 level 4 only refreshes (attribute 8). Landing on a 流血 already running
// from 210403 it must rewrite the layer amount, or the bucket keeps ticking the
// older number for the rest of its duration.
func TestRefreshingAnOverlayBucketAdoptsTheNewAmount(t *testing.T) {
	clock := &fakeCombatClock{now: time.Unix(5_000, 0)}
	battle, player, target := newOverlayBattleForTest(clock)

	stacking := statusWithModifier(t, 210403, 3, 21040312)
	refreshing := statusWithModifier(t, 210502, 4, 21050214)
	if stackMode(refreshing) != CombatEffectRefresh {
		t.Fatalf("fixture drifted: modifier %d stack mode = %v, want refresh",
			refreshing.ModifierID, stackMode(refreshing))
	}

	if _, err := battle.applyPeriodic(SkillEffect{}, stacking, player, target, 50, true); err != nil {
		t.Fatalf("stacking 流血: %v", err)
	}
	if _, err := battle.applyPeriodic(SkillEffect{}, refreshing, player, target, 30, true); err != nil {
		t.Fatalf("refreshing 流血: %v", err)
	}

	effects := battle.runtime.state().effects
	if len(effects) != 1 {
		t.Fatalf("流血 effects = %d, want one shared slot: %+v", len(effects), effects)
	}
	if effects[0].overlayValue != 30 {
		t.Fatalf("refreshed 流血 value = %d, want the refreshed 30", effects[0].overlayValue)
	}
	// The refresh also re-anchors the duration, so the tick comes due one
	// interval after the refresh, not one interval after the original cast.
	clock.Add(gameplayEffectTickInterval())
	count, total := periodicDamage(battle.runtime.Tick())
	if count != 1 || total != 30 {
		t.Fatalf("refreshed 流血 ticked %d times for %d, want one 30-point tick", count, total)
	}
}

// The layer cap is the one number about stacking the client tables do not state,
// so it is an operator value: Gameplay.combat.effect_max_stacks. A configured 2
// must stop a third 流血 from adding a layer, and the shipped default stays 10.
func TestConfiguredEffectMaxStacksCapsOverlayLayers(t *testing.T) {
	if got := gameplayEffectMaxStacks(); got != operationsconfig.DefaultEffectMaxStacks {
		t.Fatalf("default layer cap = %d, want %d", got, operationsconfig.DefaultEffectMaxStacks)
	}
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.Combat.EffectMaxStacks = 2
	})
	if got := gameplayEffectMaxStacks(); got != 2 {
		t.Fatalf("configured layer cap = %d, want 2", got)
	}

	clock := &fakeCombatClock{now: time.Unix(6_000, 0)}
	battle, player, target := newOverlayBattleForTest(clock)
	status := statusWithModifier(t, 110503, 4, 11050319)
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := battle.applyPeriodic(SkillEffect{}, status, player, target, 10, true); err != nil {
			t.Fatalf("流血 attempt %d: %v", attempt+1, err)
		}
	}

	effects := battle.runtime.state().effects
	if len(effects) != 1 || effects[0].stacks != 2 || effects[0].overlayValue != 20 {
		t.Fatalf("capped 流血 bucket = %+v, want one effect of 2 layers worth 20", effects)
	}
	clock.Add(gameplayEffectTickInterval())
	count, total := periodicDamage(battle.runtime.Tick())
	if count != 1 || total != 20 {
		t.Fatalf("capped 流血 ticked %d times for %d, want one 20-point tick", count, total)
	}
}

// The bucket only covers the two periodic kinds, and only modifiers the client
// has a slot for. 普通攻击's 中毒 modifier ships overlayType 1 with stateK 0, and
// it must keep its own key rather than share the 中毒 slot it cannot appear in.
func TestOverlayBucketLeavesNonSlotsAlone(t *testing.T) {
	dot := statusWithModifier(t, 110503, 4, 11050319)
	for _, suffix := range []string{"control", "shield", "counter", "marker", "damage-multiplier", "attribute-1-0"} {
		if got := overlayBucket(dot, suffix); got != "" {
			t.Fatalf("overlayBucket(流血, %q) = %q, want no bucket for a non-periodic kind", suffix, got)
		}
	}
	if got := overlayBucket(nil, "dot"); got != "" {
		t.Fatalf("overlayBucket(nil) = %q, want no bucket", got)
	}

	noState := statusWithModifier(t, 300001, 1, 30000114)
	if noState.OverlayType != 1 || noState.StateKey != 0 {
		t.Fatalf("fixture drifted: overlayType=%d stateK=%d, want 1/0", noState.OverlayType, noState.StateKey)
	}
	if got := overlayBucket(noState, "dot"); got != "" {
		t.Fatalf("overlayBucket(no-state 中毒) = %q, want its own per-modifier key", got)
	}
	battle := &battleState{}
	if got := battle.registerEffectMeta(noState.ModifierID, "dot", noState); got != "modifier:30000114:dot" {
		t.Fatalf("no-state 中毒 key = %q, want modifier:30000114:dot", got)
	}
}
