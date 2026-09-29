package main

import (
	"errors"
	"testing"
	"time"
)

type fakeCombatClock struct{ now time.Time }

func (c *fakeCombatClock) Now() time.Time      { return c.now }
func (c *fakeCombatClock) Add(d time.Duration) { c.now = c.now.Add(d) }

func newEffectTestRuntime() (*CombatRuntime, *fakeCombatClock, CombatUnitRef, CombatUnitRef) {
	clock := &fakeCombatClock{now: time.Unix(1_000, 0)}
	monster := &monsterUnit{id: 200, hp: 100, maxHP: 100, phyAtk: 40, phyDef: 10, alive: true}
	battle := &battleState{
		playerHP: 80, playerMaxHP: 100, phyAtk: 30, phyDef: 5,
		monsters: []*monsterUnit{monster},
	}
	runtime := NewCombatRuntime(battle, 100, clock)
	return runtime, clock, runtime.Player(), MonsterCombatUnit(200)
}

func applyTestEffect(t *testing.T, runtime *CombatRuntime, source, target CombatUnitRef, spec EffectSpec) []CombatEvent {
	t.Helper()
	events, err := spec.Apply(runtime, CombatEffectContext{Source: source, Target: target})
	if err != nil {
		t.Fatalf("apply %+v: %v", spec, err)
	}
	return events
}

func TestCombatRuntimeInstantHealAndUnitRefs(t *testing.T) {
	runtime, _, player, monster := newEffectTestRuntime()
	applyTestEffect(t, runtime, player, player, EffectSpec{Kind: CombatEffectInstantHeal, Value: 50})
	got, ok := runtime.Unit(player)
	if !ok || got.HP != 100 || got.MaxHP != 100 || !got.Alive {
		t.Fatalf("clamped player heal = %+v, ok=%v", got, ok)
	}
	if _, ok := runtime.Unit(MonsterCombatUnit(999)); ok {
		t.Fatal("unknown monster resolved")
	}
	if _, err := runtime.ApplyHeal(player, CombatUnitRef{Side: CombatSidePlayer, ID: 999}, 1); !errors.Is(err, ErrInvalidUnit) {
		t.Fatalf("wrong player identity err = %v", err)
	}
	if got, ok := runtime.Unit(monster); !ok || got.Ref != monster {
		t.Fatalf("monster ref mismatch: %+v, ok=%v", got, ok)
	}
}

func TestCombatRuntimeSkillPlanOrderDamageThenStatus(t *testing.T) {
	runtime, _, player, monster := newEffectTestRuntime()
	events, err := runtime.ApplyEffects(CombatEffectContext{Source: player, Target: monster}, []CombatEffect{
		EffectSpec{Kind: CombatEffectInstantDamage, Value: 10},
		EffectSpec{Key: "skill-silence", Kind: CombatEffectStatus, Status: CombatStatusSilenced},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Type != CombatEventDamage || events[1].Type != CombatEventEffectApplied {
		t.Fatalf("skill plan event order = %+v", events)
	}
	if got, _ := runtime.Unit(monster); got.HP != 90 || !runtime.HasStatus(monster, CombatStatusSilenced) {
		t.Fatalf("skill plan result = %+v silence=%v", got, runtime.HasStatus(monster, CombatStatusSilenced))
	}
}

func TestCombatRuntimeShieldInvincibleUndyingAndDeath(t *testing.T) {
	runtime, _, player, monster := newEffectTestRuntime()
	applyTestEffect(t, runtime, monster, player, EffectSpec{Key: "shield", Kind: CombatEffectShield, Value: 30})
	events, err := runtime.ApplyDamage(CombatDamageRequest{Source: monster, Target: player, Amount: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 3 || events[0].Type != CombatEventShieldAbsorb || events[0].Absorbed != 30 ||
		events[len(events)-1].Type != CombatEventDamage {
		t.Fatalf("shield event order = %+v", events)
	}
	if got, _ := runtime.Unit(player); got.HP != 60 || got.Shield != 0 {
		t.Fatalf("post-shield unit = %+v", got)
	}

	applyTestEffect(t, runtime, player, player, EffectSpec{Key: "invincible", Kind: CombatEffectStatus, Status: CombatStatusInvincible})
	events, err = runtime.ApplyDamage(CombatDamageRequest{Source: monster, Target: player, Amount: 999})
	if err != nil || len(events) != 1 || events[0].Type != CombatEventImmune {
		t.Fatalf("invincible damage = %+v, err=%v", events, err)
	}
	applyTestEffect(t, runtime, monster, player, EffectSpec{Key: "invincible", Kind: CombatEffectStatus, Status: CombatStatusUndying})
	events, err = runtime.ApplyDamage(CombatDamageRequest{Source: monster, Target: player, Amount: 999})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := runtime.Unit(player); got.HP != 1 || !got.Alive {
		t.Fatalf("undying did not clamp to one HP: %+v", got)
	}
	if _, err := runtime.Dispel(monster, player, CombatDispelBeneficial, 0); err != nil {
		t.Fatal(err)
	}
	events, err = runtime.ApplyDamage(CombatDamageRequest{Source: monster, Target: player, Amount: 1})
	if err != nil || len(events) < 2 || events[0].Type != CombatEventDamage || events[1].Type != CombatEventDeath {
		t.Fatalf("death events = %+v, err=%v", events, err)
	}
	if got, _ := runtime.Unit(player); got.Alive || got.HP != 0 {
		t.Fatalf("dead player = %+v", got)
	}
}

func TestCombatRuntimePeriodicTickBoundaryStackRefreshAndExpiry(t *testing.T) {
	runtime, clock, player, monster := newEffectTestRuntime()
	applyTestEffect(t, runtime, player, monster, EffectSpec{
		Key: "burn", Kind: CombatEffectDamageOverTime, Value: 5, Interval: time.Second,
		Duration: 3 * time.Second, StackMode: CombatEffectStack, MaxStacks: 2,
	})
	applyTestEffect(t, runtime, player, monster, EffectSpec{
		Key: "burn", Kind: CombatEffectDamageOverTime, Value: 5, Interval: time.Second,
		Duration: 3 * time.Second, StackMode: CombatEffectStack, MaxStacks: 2,
	})
	clock.Add(999 * time.Millisecond)
	if events := runtime.Tick(); len(events) != 0 {
		t.Fatalf("tick fired early: %+v", events)
	}
	clock.Add(time.Millisecond)
	events := runtime.Tick()
	if len(events) != 1 || events[0].Type != CombatEventDamage || events[0].Amount != 10 {
		t.Fatalf("first stacked DOT tick = %+v", events)
	}
	clock.Add(2 * time.Second)
	events = runtime.Tick()
	var damages, expiries int
	for _, event := range events {
		if event.Type == CombatEventDamage {
			damages++
		}
		if event.Type == CombatEventEffectExpired {
			expiries++
		}
	}
	if damages != 2 || expiries != 1 {
		t.Fatalf("expiry boundary must tick at t=3 before removal: %+v", events)
	}
	if got, _ := runtime.Unit(monster); got.HP != 70 {
		t.Fatalf("DOT hp = %d, want 70", got.HP)
	}

	applyTestEffect(t, runtime, player, monster, EffectSpec{
		Key: "poison", Kind: CombatEffectDamageOverTime, Value: 3, Interval: time.Second,
		Duration: 2 * time.Second, StackMode: CombatEffectRefresh,
	})
	clock.Add(time.Second)
	runtime.Tick()
	applyTestEffect(t, runtime, player, monster, EffectSpec{
		Key: "poison", Kind: CombatEffectDamageOverTime, Value: 4, Interval: time.Second,
		Duration: 2 * time.Second, StackMode: CombatEffectRefresh,
	})
	clock.Add(time.Second)
	events = runtime.Tick()
	if len(events) == 0 || events[0].Type != CombatEventDamage || events[0].Amount != 4 {
		t.Fatalf("refresh did not retain cadence/update value: %+v", events)
	}
}

func TestCombatRuntimeAttributeBuffDebuffAndMaxHPExpiryClamp(t *testing.T) {
	runtime, clock, player, _ := newEffectTestRuntime()
	applyTestEffect(t, runtime, player, player, EffectSpec{
		Key: "attack-up", Kind: CombatEffectAttribute, Attribute: CombatAttributePhysicalAttack,
		Value: 5, Percent: 50, Duration: time.Second,
	})
	if got := runtime.EffectiveAttribute(player, CombatAttributePhysicalAttack); got != 50 {
		t.Fatalf("effective attack = %d, want 50", got)
	}
	applyTestEffect(t, runtime, player, player, EffectSpec{
		Key: "defense-down", Kind: CombatEffectAttribute, Attribute: CombatAttributePhysicalDefense,
		Value: -2, Percent: -20, Duration: time.Second,
	})
	if got := runtime.EffectiveAttribute(player, CombatAttributePhysicalDefense); got != 2 {
		t.Fatalf("effective debuffed defense = %d, want 2", got)
	}
	applyTestEffect(t, runtime, player, player, EffectSpec{
		Key: "max-hp", Kind: CombatEffectAttribute, Attribute: CombatAttributeMaxHP,
		Value: 50, Duration: time.Second,
	})
	applyTestEffect(t, runtime, player, player, EffectSpec{Kind: CombatEffectInstantHeal, Value: 100})
	if got, _ := runtime.Unit(player); got.HP != 150 || got.MaxHP != 150 {
		t.Fatalf("buffed HP = %+v", got)
	}
	clock.Add(time.Second)
	events := runtime.Tick()
	if got, _ := runtime.Unit(player); got.HP != 100 || got.MaxHP != 100 {
		t.Fatalf("expired max HP was not clamped: %+v", got)
	}
	foundClamp := false
	for _, event := range events {
		foundClamp = foundClamp || event.Type == CombatEventResourceClamped
	}
	if !foundClamp {
		t.Fatalf("missing max HP clamp event: %+v", events)
	}
}

func TestCombatRuntimeHOTAndAtomicMaxHPReplacement(t *testing.T) {
	runtime, clock, player, _ := newEffectTestRuntime()
	applyTestEffect(t, runtime, player, player, EffectSpec{
		Key: "max-hp", Kind: CombatEffectAttribute, Attribute: CombatAttributeMaxHP,
		Value: 50, Duration: 5 * time.Second,
	})
	applyTestEffect(t, runtime, player, player, EffectSpec{Kind: CombatEffectInstantHeal, Value: 100})
	events := applyTestEffect(t, runtime, player, player, EffectSpec{
		Key: "max-hp", Kind: CombatEffectAttribute, Attribute: CombatAttributeMaxHP,
		Value: 100, Duration: 5 * time.Second, StackMode: CombatEffectReplace,
	})
	for _, event := range events {
		if event.Type == CombatEventResourceClamped {
			t.Fatalf("atomic max HP upgrade transiently clamped HP: %+v", events)
		}
	}
	if got, _ := runtime.Unit(player); got.HP != 150 || got.MaxHP != 200 {
		t.Fatalf("replaced max HP effect = %+v", got)
	}
	if _, err := runtime.ApplyDamage(CombatDamageRequest{Target: player, Amount: 100}); err != nil {
		t.Fatal(err)
	}
	applyTestEffect(t, runtime, player, player, EffectSpec{
		Key: "regen", Kind: CombatEffectHealOverTime, Value: 10,
		Interval: time.Second, Duration: 2 * time.Second,
	})
	clock.Add(2 * time.Second)
	events = runtime.Tick()
	var heals int
	for _, event := range events {
		if event.Type == CombatEventHeal && event.Amount == 10 {
			heals++
		}
	}
	if heals != 2 {
		t.Fatalf("HOT boundary ticks = %+v", events)
	}
}

func TestCombatRuntimeControlStatusesAndDispel(t *testing.T) {
	runtime, _, player, monster := newEffectTestRuntime()
	applyTestEffect(t, runtime, monster, player, EffectSpec{Key: "stun", Kind: CombatEffectStatus, Status: CombatStatusStunned})
	applyTestEffect(t, runtime, monster, player, EffectSpec{Key: "silence", Kind: CombatEffectStatus, Status: CombatStatusSilenced})
	if runtime.CanAct(player) || runtime.CanCast(player) {
		t.Fatal("controlled player can act/cast")
	}
	events, err := runtime.Dispel(player, player, CombatDispelHarmful, 1)
	if err != nil || len(events) != 1 || events[0].Key != "stun" || events[0].Type != CombatEventEffectDispelled {
		t.Fatalf("ordered limited dispel = %+v, err=%v", events, err)
	}
	if !runtime.CanAct(player) || runtime.CanCast(player) {
		t.Fatal("silenced player action/cast gates are wrong")
	}
	applyTestEffect(t, runtime, player, player, EffectSpec{Key: "hide", Kind: CombatEffectStatus, Status: CombatStatusInvisible})
	if runtime.CanBeTargeted(player) {
		t.Fatal("invisible player targetable")
	}
	if _, err := runtime.Dispel(player, player, CombatDispelAll, 0); err != nil {
		t.Fatal(err)
	}
	if !runtime.CanCast(player) || !runtime.CanBeTargeted(player) {
		t.Fatal("all-dispel did not restore action/targeting")
	}
}

func TestCombatRuntimeLifestealReflectCounterTriggerOrder(t *testing.T) {
	runtime, _, player, monster := newEffectTestRuntime()
	applyTestEffect(t, runtime, player, player, EffectSpec{Key: "leech", Kind: CombatEffectLifesteal, Percent: 50})
	applyTestEffect(t, runtime, monster, monster, EffectSpec{Key: "thorns", Kind: CombatEffectReflect, Percent: 25})
	applyTestEffect(t, runtime, monster, monster, EffectSpec{Key: "riposte", Kind: CombatEffectCounter})
	events, err := runtime.ApplyDamage(CombatDamageRequest{Source: player, Target: monster, Amount: 20})
	if err != nil {
		t.Fatal(err)
	}
	want := []CombatEventType{
		CombatEventDamage, CombatEventLifesteal, CombatEventReflect, CombatEventDamage,
		CombatEventCounter,
	}
	if len(events) != len(want) {
		t.Fatalf("reactive event count/order = %+v", events)
	}
	for i := range want {
		if events[i].Type != want[i] {
			t.Fatalf("event %d type=%v want=%v; all=%+v", i, events[i].Type, want[i], events)
		}
	}
	if got, _ := runtime.Unit(player); got.HP != 85 {
		// 80 + 10 lifesteal - 5 reflect. The counter queues a basic attack.
		t.Fatalf("player hp after reactions = %d, want 85", got.HP)
	}
	if events[len(events)-1].Amount != 0 {
		t.Fatal("counter request must not carry reflected damage")
	}
	if got, _ := runtime.Unit(monster); got.HP != 80 {
		t.Fatalf("monster hp after attack = %d, want 80", got.HP)
	}
}

func TestCombatRuntimeStaticLifestealUsesRateAndDamagePercent(t *testing.T) {
	runtime, _, player, monster := newEffectTestRuntime()
	runtime.battle.owner = &session{transBonus: map[int32]float32{
		1042: 1,
		1043: 0.25,
	}}
	events, err := runtime.ApplyDamage(CombatDamageRequest{Source: player, Target: monster, Amount: 20})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := runtime.Unit(player); got.HP != 85 {
		t.Fatalf("player hp after static lifesteal = %d, want 80 + 20*25%% = 85", got.HP)
	}
	if len(events) != 2 || events[0].Type != CombatEventDamage ||
		events[1].Type != CombatEventLifesteal || events[1].Amount != 5 {
		t.Fatalf("static lifesteal events = %+v", events)
	}
}

func TestCombatRuntimeHealthRecoveryUsesMaximumHPPercent(t *testing.T) {
	runtime, _, player, _ := newEffectTestRuntime()
	runtime.battle.owner = &session{transBonus: map[int32]float32{1044: 0.1}}
	events, err := runtime.ApplyHealthRecovery(player)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := runtime.Unit(player); got.HP != 90 {
		t.Fatalf("player hp after recovery = %d, want 80 + 100*10%% = 90", got.HP)
	}
	if len(events) != 1 || events[0].Type != CombatEventHeal || events[0].Amount != 10 {
		t.Fatalf("health recovery events = %+v", events)
	}
}

func TestCombatRuntimeDeathClearsEffectsAndBattleEndCancels(t *testing.T) {
	runtime, clock, player, monster := newEffectTestRuntime()
	applyTestEffect(t, runtime, player, monster, EffectSpec{
		Key: "dot", Kind: CombatEffectDamageOverTime, Value: 5, Interval: time.Second,
	})
	deathEvents, err := runtime.ApplyDamage(CombatDamageRequest{Source: player, Target: monster, Amount: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(deathEvents) != 3 || deathEvents[0].Type != CombatEventDamage ||
		deathEvents[1].Type != CombatEventEffectRemoved || deathEvents[1].Reason != "death" ||
		deathEvents[2].Type != CombatEventDeath {
		t.Fatalf("death must remove target effects before deleting the unit: %+v", deathEvents)
	}
	clock.Add(time.Second)
	if events := runtime.Tick(); len(events) != 0 {
		t.Fatalf("dead unit retained effects: %+v", events)
	}

	runtime, _, player, monster = newEffectTestRuntime()
	applyTestEffect(t, runtime, monster, player, EffectSpec{Key: "stun", Kind: CombatEffectStatus, Status: CombatStatusStunned})
	runtime.battle.ended = true
	if events := runtime.Tick(); len(events) != 0 || !runtime.Cancelled() {
		t.Fatalf("ended battle runtime not canceled: events=%+v canceled=%v", events, runtime.Cancelled())
	}
	if _, err := runtime.ApplyHeal(player, player, 1); !errors.Is(err, ErrCombatEnded) {
		t.Fatalf("post-end operation err = %v", err)
	}
}
