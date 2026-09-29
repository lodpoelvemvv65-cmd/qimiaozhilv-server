package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gproto "google.golang.org/protobuf/proto"
	"mhqserver/protocol"
)

func TestSkillPlanRuntimeAppliesDamageAndSecondaryStatusInOrder(t *testing.T) {
	catalog := loadTestSkillLogic(t)
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = catalog
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })

	monster := &monsterUnit{id: 200, hp: 1000, maxHP: 1000, phyDef: 10, alive: true}
	battle := &battleState{
		playerHP: 1000, playerMaxHP: 1000, phyAtk: 100,
		monsters: []*monsterUnit{monster},
	}
	battle.runtime = NewCombatRuntime(battle, 100, nil)
	plan, err := catalog.Plan(100001, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx := newSkillExecutionContext(battle, func() float64 { return 0 })
	ctx.targetIntn = func(limit int) int { return limit - 1 }
	events, err := battle.executeSkillPlanWithContext(plan, battle.runtime.Player(), MonsterCombatUnit(monster.id), ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 || events[0].Type != CombatEventDamage || events[1].Type != CombatEventEffectApplied {
		t.Fatalf("damage/status declaration order = %+v", events)
	}
	if monster.hp != 872 {
		t.Fatalf("configured 142%% physical damage left hp=%d, want 872", monster.hp)
	}
	if player, _ := battle.runtime.Unit(battle.runtime.Player()); player.Shield != 50 {
		t.Fatalf("secondary configured shield = %d, want 50", player.Shield)
	}
}

func TestMonsterSkillPlanAppliesDOTAndControl(t *testing.T) {
	catalog := loadTestSkillLogic(t)
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = catalog
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })

	clock := &fakeCombatClock{now: time.Unix(1000, 0)}
	monster := &monsterUnit{id: 200, hp: 1000, maxHP: 1000, phyAtk: 100, spiAtk: 100, alive: true}
	battle := &battleState{
		playerHP: 1000, playerMaxHP: 1000, monsters: []*monsterUnit{monster},
	}
	battle.runtime = NewCombatRuntime(battle, 100, clock)

	dotPlan, err := catalog.Plan(500009, 1)
	if err != nil {
		t.Fatal(err)
	}
	events, err := battle.executeSkillPlan(dotPlan, MonsterCombatUnit(monster.id), battle.runtime.Player(), func() float64 { return 0 })
	if err != nil {
		t.Fatal(err)
	}
	if !containsCombatEvent(events, CombatEventEffectApplied) {
		t.Fatalf("monster DOT was not installed: %+v", events)
	}
	// 500009 点燃 ships an empty thinkInterval, so it follows the same captured
	// cadence as every other periodic effect — first tick one interval after it
	// lands, not on a one-second timer.
	clock.Add(gameplayEffectTickInterval() - time.Millisecond)
	if early := battle.runtime.Tick(); containsCombatEvent(early, CombatEventDamage) {
		t.Fatalf("monster DOT ticked before its first interval: %+v", early)
	}
	clock.Add(time.Millisecond)
	ticks := battle.runtime.Tick()
	if !containsCombatEvent(ticks, CombatEventDamage) || battle.playerHP >= 1000 {
		t.Fatalf("monster DOT did not tick: hp=%d events=%+v", battle.playerHP, ticks)
	}

	controlPlan, err := catalog.Plan(500006, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := battle.executeSkillPlan(controlPlan, MonsterCombatUnit(monster.id), battle.runtime.Player(), func() float64 { return 0 }); err != nil {
		t.Fatal(err)
	}
	if battle.runtime.CanAct(battle.runtime.Player()) {
		t.Fatal("configured monster stun did not close the action gate")
	}
}

func TestConfiguredReactiveModifierEventsInstallAndTrigger(t *testing.T) {
	tests := []struct {
		name      string
		kind      SkillEffectKind
		eventCode int32
		wantEvent CombatEventType
	}{
		{name: "counter-event-2", kind: SkillEffectCounter, eventCode: 2, wantEvent: CombatEventCounter},
		{name: "lifesteal-event-5", kind: SkillEffectLifeSteal, eventCode: 5, wantEvent: CombatEventLifesteal},
		{name: "reflect-event-6", kind: SkillEffectReflect, eventCode: 6, wantEvent: CombatEventReflect},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			monster := &monsterUnit{id: 200, hp: 1000, maxHP: 1000, alive: true}
			battle := &battleState{
				playerHP: 500, playerMaxHP: 1000,
				monsters: []*monsterUnit{monster},
			}
			battle.runtime = NewCombatRuntime(battle, 100, nil)
			player := battle.runtime.Player()
			enemy := MonsterCombatUnit(monster.id)
			status := &SkillStatusPlan{ModifierID: int64(9000 + tc.eventCode), DurationSeconds: 10, CanBeCleared: true}
			effect := SkillEffect{
				Kind: SkillEffectStatus, ModifierID: status.ModifierID, Status: status,
				Children: []SkillEffect{{
					Kind: tc.kind, Trigger: SkillTrigger{Scope: "modifier", Event: tc.eventCode},
					Target: SkillTargetPlan{Kind: SkillTargetSingle, Side: SkillTargetHolder}, ParamPercent: 50,
				}},
			}
			installed, err := battle.applyStatusEffect(effect, player, player, enemy, func() float64 { return 0 })
			if err != nil {
				t.Fatal(err)
			}
			if !containsCombatEvent(installed, CombatEventEffectApplied) {
				t.Fatalf("reactive modifier was not installed: %+v", installed)
			}
			var triggered []CombatEvent
			if tc.kind == SkillEffectLifeSteal {
				triggered, err = battle.runtime.ApplyDamage(CombatDamageRequest{Source: player, Target: enemy, Amount: 100})
				if battle.playerHP != 550 {
					t.Fatalf("lifesteal hp=%d, want 550", battle.playerHP)
				}
			} else {
				triggered, err = battle.runtime.ApplyDamage(CombatDamageRequest{Source: enemy, Target: player, Amount: 100})
				wantHP := int32(950)
				if tc.kind == SkillEffectCounter {
					wantHP = 1000 // Basic attack damage is deferred until impact.
				}
				if monster.hp != wantHP {
					t.Fatalf("reactive damage left monster hp=%d, want %d", monster.hp, wantHP)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if !containsCombatEvent(triggered, tc.wantEvent) {
				t.Fatalf("event %v did not trigger: %+v", tc.wantEvent, triggered)
			}
		})
	}
}

func TestInvisibleUnitCanReceiveSelfAndHolderEffects(t *testing.T) {
	monster := &monsterUnit{id: 200, hp: 1000, maxHP: 1000, alive: true}
	battle := &battleState{playerHP: 100, playerMaxHP: 1000, monsters: []*monsterUnit{monster}}
	battle.runtime = NewCombatRuntime(battle, 100, nil)
	player := battle.runtime.Player()
	if _, err := battle.runtime.ApplyEffects(
		CombatEffectContext{Source: player, Target: player},
		[]CombatEffect{EffectSpec{Key: "hidden", Kind: CombatEffectStatus, Status: CombatStatusInvisible, Duration: time.Minute}},
	); err != nil {
		t.Fatal(err)
	}
	if battle.runtime.CanBeTargeted(player) {
		t.Fatal("invisible player unexpectedly remained externally targetable")
	}
	for _, side := range []SkillTargetSide{SkillTargetSelf, SkillTargetHolder} {
		targets := battle.resolveSkillTargets(
			SkillTargetPlan{Kind: SkillTargetSingle, Side: side}, player, MonsterCombatUnit(monster.id), player,
		)
		if len(targets) != 1 || targets[0] != player {
			t.Fatalf("hidden %s targets = %+v, want player", side, targets)
		}
	}
	events, err := battle.executeSkillEffect(SkillEffect{
		Kind:      SkillEffectHeal,
		Target:    SkillTargetPlan{Kind: SkillTargetSingle, Side: SkillTargetSelf},
		Treatment: &SkillTreatmentPlan{Self: SkillFormula{Stat: SkillStatMaxHP, Percent: 10}},
	}, player, MonsterCombatUnit(monster.id), player, nil, func() float64 { return 0 })
	if err != nil {
		t.Fatal(err)
	}
	if battle.playerHP != 200 || !containsCombatEvent(events, CombatEventHeal) {
		t.Fatalf("hidden self heal: hp=%d events=%+v", battle.playerHP, events)
	}
}

func TestSkillVisualAndProjectileEventsCarryConfiguredTiming(t *testing.T) {
	monster := &monsterUnit{id: 200, hp: 1000, maxHP: 1000, alive: true}
	battle := &battleState{playerHP: 1000, playerMaxHP: 1000, monsters: []*monsterUnit{monster}}
	battle.runtime = NewCombatRuntime(battle, 100, nil)
	plan := SkillPlan{Effects: []SkillEffect{{
		Kind: SkillEffectDelay, Trigger: SkillTrigger{Scope: "skill", Event: 1}, DelayMS: 250,
		Children: []SkillEffect{{
			Kind: SkillEffectProjectile, DelayMS: 750, EffectID: 2103, ImpactEffectID: 2104,
			Target: SkillTargetPlan{Kind: SkillTargetSingle, Side: SkillTargetCastTarget},
			Children: []SkillEffect{{
				Kind: SkillEffectVisual, EffectID: 2226, EffectType: 2, EffectPosition: 1,
				Target: SkillTargetPlan{Kind: SkillTargetSingle, Side: SkillTargetCastTarget},
			}},
		}},
	}}}
	events, err := battle.executeSkillPlan(plan, battle.runtime.Player(), MonsterCombatUnit(monster.id), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("visual event count=%d, want 3: %+v", len(events), events)
	}
	if events[0].Type != CombatEventVisual || events[0].EffectID != 2103 || events[0].DelayMS != 1000 {
		t.Fatalf("projectile event = %+v", events[0])
	}
	if events[1].Type != CombatEventVisual || events[1].EffectID != 2104 || events[1].DelayMS != 0 ||
		events[1].Source != events[0].Source || events[1].Target != events[0].Target {
		t.Fatalf("impact event = %+v", events[1])
	}
	if events[2].Type != CombatEventVisual || events[2].EffectID != 2226 || events[2].DelayMS != 1000 ||
		events[2].EffectPos != 1 || events[2].EffectTargetType != 0 {
		t.Fatalf("nested visual event = %+v", events[2])
	}

	conn := &recordingConn{}
	oldTables := tables
	t.Cleanup(func() { tables = oldTables })
	tables = &datatables{effectConfig: map[int64]map[string]interface{}{
		2103: testRow(map[string]int64{"EffectType": 1}),
		2104: testRow(map[string]int64{"EffectType": 2}),
		2226: testRow(map[string]int64{"EffectType": 2}),
	}}
	(&Server{}).emitCombatEvents(&channel{conn: conn, session: newSession()}, battle, events)
	opcodes := recordedOpcodes(t, conn.Bytes())
	if len(opcodes) != 3 || opcodes[0] != 20077 || opcodes[1] != 20077 || opcodes[2] != 20077 {
		t.Fatalf("visual push opcodes = %v, want three 20077 pushes", opcodes)
	}
	visuals := recordedSkillVisuals(t, conn.Bytes())
	if len(visuals) != 3 {
		t.Fatalf("visual messages = %d, want 3", len(visuals))
	}
	for index, want := range []struct {
		effectID int32
		timeMS   int32
	}{{2103, 1000}, {2104, 0}, {2226, 1000}} {
		got := visuals[index]
		if got.EffectId != want.effectID || got.Time != want.timeMS || got.UnitId != 100 || got.TargetId != 200 {
			t.Errorf("visual[%d] = %+v, want effect=%d time=%d source=100 target=200", index, got, want.effectID, want.timeMS)
		}
	}
}

func TestBasicAttackPlansRestoreConfiguredProjectiles(t *testing.T) {
	skillTable, err := loadKVTable(filepath.Join("..", "datatable_json", "SkillConfig.json"))
	if err != nil {
		t.Fatal(err)
	}
	effectTable, err := loadKVTable(filepath.Join("..", "datatable_json", "EffectConfig.json"))
	if err != nil {
		t.Fatal(err)
	}
	oldTables := tables
	t.Cleanup(func() { tables = oldTables })
	tables = &datatables{skillConfig: skillTable, effectConfig: effectTable}

	catalog := loadTestSkillLogic(t)
	for skillID, wantEffectID := range map[int32]int32{
		100001: 2101,
		200001: 2201,
		300001: 2301,
		400001: 2401,
	} {
		plan, planErr := catalog.Plan(skillID, 1)
		if planErr != nil {
			t.Fatalf("Plan(%d): %v", skillID, planErr)
		}
		originalEffectCount := len(plan.Effects)
		hadProjectile := skillPlanContainsProjectile(plan.Effects)
		plan = withConfiguredBasicAttackProjectile(plan)
		var projectiles []SkillEffect
		var collect func([]SkillEffect)
		collect = func(effects []SkillEffect) {
			for _, effect := range effects {
				if effect.Kind == SkillEffectProjectile {
					projectiles = append(projectiles, effect)
				}
				collect(effect.Success)
				collect(effect.Failure)
				collect(effect.Children)
			}
		}
		collect(plan.Effects)
		if len(projectiles) != 1 {
			t.Fatalf("skill %d projectiles = %+v, want one", skillID, projectiles)
		}
		projectile := projectiles[0]
		if projectile.EffectID != wantEffectID || projectile.DelayMS != 1000 {
			t.Errorf("skill %d projectile = %+v", skillID, projectile)
		}
		if !hadProjectile && (projectile.ImpactEffectID != wantEffectID+1 ||
			projectile.Target.Side != SkillTargetCastTarget) {
			t.Errorf("skill %d fallback projectile = %+v", skillID, projectile)
		}
		monster := &monsterUnit{id: 200, hp: 1000, maxHP: 1000, alive: true}
		battle := &battleState{playerHP: 1000, playerMaxHP: 1000, monsters: []*monsterUnit{monster}}
		battle.runtime = NewCombatRuntime(battle, 100, nil)
		events, executeErr := battle.executeSkillEffect(
			projectile, battle.runtime.Player(), MonsterCombatUnit(monster.id), battle.runtime.Player(), nil,
			func() float64 { return 0 },
		)
		if executeErr != nil {
			t.Fatalf("execute projectile for skill %d: %v", skillID, executeErr)
		}
		if len(events) < 2 || events[0].EffectID != wantEffectID || events[0].DelayMS != 1000 ||
			events[1].EffectID != wantEffectID+1 || events[1].DelayMS != 0 ||
			events[1].Source != events[0].Source || events[1].Target != events[0].Target {
			t.Errorf("skill %d projectile runtime events = %+v", skillID, events)
		}
		if hadProjectile && len(plan.Effects) != originalEffectCount {
			t.Errorf("skill %d duplicated its structured projectile", skillID)
		}
		if duplicate := withConfiguredBasicAttackProjectile(plan); len(duplicate.Effects) != len(plan.Effects) {
			t.Errorf("skill %d projectile fallback is not idempotent", skillID)
		}
	}
}

func TestProjectileVisualIsSent(t *testing.T) {
	oldTables := tables
	t.Cleanup(func() { tables = oldTables })
	tables = &datatables{effectConfig: map[int64]map[string]interface{}{
		2103: testRow(map[string]int64{"EffectType": 1}),
	}}
	conn := &recordingConn{}
	battle := &battleState{}
	(&Server{}).emitCombatEvents(&channel{conn: conn, session: newSession()}, battle, []CombatEvent{{
		Type: CombatEventVisual, EffectID: 2103,
	}})
	if opcodes := recordedOpcodes(t, conn.Bytes()); len(opcodes) != 1 || opcodes[0] != 20077 {
		t.Fatalf("projectile opcodes = %v, want [20077]", opcodes)
	}
}

func TestTrueDamageDoesNotSubtractDefense(t *testing.T) {
	monster := &monsterUnit{id: 200, hp: 1000, maxHP: 1000, phyDef: 10_000, spiDef: 10_000, alive: true}
	battle := &battleState{playerHP: 1000, playerMaxHP: 1000, phyAtk: 100, monsters: []*monsterUnit{monster}}
	battle.runtime = NewCombatRuntime(battle, 100, nil)
	plan := &SkillDamagePlan{
		Type: SkillDamageTrue,
		Self: SkillFormula{Stat: SkillStatPhysicalAttack, Percent: 100},
	}
	if got := battle.damageAmount(plan, battle.runtime.Player(), MonsterCombatUnit(monster.id)); got != 100 {
		t.Fatalf("true damage=%d, want 100", got)
	}
}

func TestBattleInitializesEventEightPassivesAndRejectsActiveCast(t *testing.T) {
	catalog := loadTestSkillLogic(t)
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = catalog
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })
	plan, err := catalog.Plan(320201, 1)
	if err != nil {
		t.Fatal(err)
	}
	if skillPlanCanCast(plan) {
		t.Fatal("event-8-only passive was accepted as an active cast")
	}
	battle := &battleState{playerHP: 1000, playerMaxHP: 1000}
	battle.runtime = NewCombatRuntime(battle, 100, nil)
	events, err := battle.initializePassiveSkills(&session{skills: map[int32]int32{320201: 1}}, func() float64 { return 0 })
	if err != nil {
		t.Fatal(err)
	}
	if !containsCombatEvent(events, CombatEventEffectApplied) {
		t.Fatalf("event-8 passive was not initialized: %+v", events)
	}
	found := false
	for _, effect := range battle.runtime.effects {
		if strings.HasPrefix(effect.key, "modifier:32020111:") && effect.expiresAt.IsZero() {
			found = true
		}
	}
	if !found {
		t.Fatalf("permanent passive effect missing: %+v", battle.runtime.effects)
	}
}

func TestSkillExecutionFailureRollsBackResourcesAndCooldown(t *testing.T) {
	catalog := loadTestSkillLogic(t)
	skill := catalog.Skills[110301]
	skill.Modifiers[11030111].ValueKey = 1
	skill.Modifiers[11030111].Value = SkillValue{Value: math.NaN()}
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = catalog
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })

	monster := &monsterUnit{id: 200, hp: 1000, maxHP: 1000, alive: true}
	battle := &battleState{
		playerHP: 1000, playerMaxHP: 1000, playerMP: 100, playerMaxMP: 100,
		monsters: []*monsterUnit{monster},
	}
	battle.runtime = NewCombatRuntime(battle, 100, nil)
	ss := newSession()
	ss.playerID = 100
	ss.skills = map[int32]int32{110301: 1}
	ss.skillOrder = []int32{110301}
	ss.battle = battle
	conn := &recordingConn{}
	resp := &protocol.M2C_UseMainUISkill{}
	_, cast := (&Server{}).useSkillPlanLockedEx(
		&channel{conn: conn, session: ss},
		&protocol.C2M_UseMainUISkill{SlotId: 0},
		resp,
	)
	if cast {
		t.Fatal("invalid runtime effect unexpectedly succeeded")
	}
	if resp.Error != 0 {
		t.Fatalf("business rejection must keep Error=0 for client: %d", resp.Error)
	}
	if battle.playerHP != 1000 || battle.playerMP != 100 {
		t.Fatalf("failed cast consumed resources: hp=%d mp=%d", battle.playerHP, battle.playerMP)
	}
	if _, started := ss.skillCooldowns[110301]; started {
		t.Fatalf("failed cast retained cooldown: %+v", ss.skillCooldowns)
	}
	if len(conn.Bytes()) != 0 {
		t.Fatalf("failed cast emitted pushes: %v", recordedOpcodes(t, conn.Bytes()))
	}
}

func TestConditionalModifierEventsAreNotExecutedOnStatusApplication(t *testing.T) {
	for _, eventCode := range []int32{4, 7, 22, 23} {
		t.Run(strconv.Itoa(int(eventCode)), func(t *testing.T) {
			battle := &battleState{playerHP: 100, playerMaxHP: 1000}
			battle.runtime = NewCombatRuntime(battle, 100, nil)
			player := battle.runtime.Player()
			status := &SkillStatusPlan{ModifierID: int64(8000 + eventCode), DurationSeconds: 10}
			_, err := battle.applyStatusEffect(SkillEffect{
				Kind: SkillEffectStatus, Status: status,
				Children: []SkillEffect{{
					Kind: SkillEffectHeal, Trigger: SkillTrigger{Scope: "modifier", Event: eventCode},
					Target:    SkillTargetPlan{Kind: SkillTargetSingle, Side: SkillTargetHolder},
					Treatment: &SkillTreatmentPlan{Self: SkillFormula{Stat: SkillStatMaxHP, Percent: 50}},
				}},
			}, player, player, player, func() float64 { return 0 })
			if err != nil {
				t.Fatal(err)
			}
			if battle.playerHP != 100 {
				t.Fatalf("conditional event %d ran at status application: hp=%d", eventCode, battle.playerHP)
			}
		})
	}
}

func TestModifierEvent24RunsBeforeStatusApplication(t *testing.T) {
	battle := &battleState{playerHP: 100, playerMaxHP: 1000}
	battle.runtime = NewCombatRuntime(battle, 100, nil)
	player := battle.runtime.Player()
	status := &SkillStatusPlan{ModifierID: 8024, DurationSeconds: 10}
	events, err := battle.applyStatusEffect(SkillEffect{
		Kind: SkillEffectStatus, Status: status,
		Children: []SkillEffect{{
			Kind: SkillEffectHeal, Trigger: SkillTrigger{Scope: "modifier", Event: 24},
			Target:    SkillTargetPlan{Kind: SkillTargetSingle, Side: SkillTargetHolder},
			Treatment: &SkillTreatmentPlan{Self: SkillFormula{Stat: SkillStatMaxHP, Percent: 50}},
		}},
	}, player, player, player, func() float64 { return 0 })
	if err != nil {
		t.Fatal(err)
	}
	if battle.playerHP != 600 || len(events) < 2 || events[0].Type != CombatEventHeal {
		t.Fatalf("event 24 did not run before marker: hp=%d events=%+v", battle.playerHP, events)
	}
}

func TestRealDamageAndCastCountModifiers(t *testing.T) {
	catalog := loadTestSkillLogic(t)
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = catalog
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })

	t.Run("400001-current-projectile-gets-1.5x", func(t *testing.T) {
		monster := &monsterUnit{id: 200, hp: 1000, maxHP: 1000, alive: true}
		battle := &battleState{playerHP: 1000, playerMaxHP: 1000, spiAtk: 100, monsters: []*monsterUnit{monster}}
		battle.runtime = NewCombatRuntime(battle, 100, nil)
		plan, err := catalog.Plan(400001, 1)
		if err != nil {
			t.Fatal(err)
		}
		events, err := battle.executeSkillPlan(plan, battle.runtime.Player(), MonsterCombatUnit(monster.id), func() float64 { return 0 })
		if err != nil {
			t.Fatal(err)
		}
		if monster.hp != 710 || combatEventAmount(events, CombatEventDamage) != 290 {
			t.Fatalf("400001 damage multiplier: hp=%d events=%+v", monster.hp, events)
		}
	})

	t.Run("210402-precast-repeats-main-effect", func(t *testing.T) {
		monster := &monsterUnit{id: 200, hp: 2000, maxHP: 2000, alive: true}
		battle := &battleState{playerHP: 1000, playerMaxHP: 1000, phyAtk: 100, monsters: []*monsterUnit{monster}}
		battle.runtime = NewCombatRuntime(battle, 100, nil)
		plan, err := catalog.Plan(210402, 5)
		if err != nil {
			t.Fatal(err)
		}
		events, err := battle.executeSkillPlan(plan, battle.runtime.Player(), MonsterCombatUnit(monster.id), func() float64 { return 0 })
		if err != nil {
			t.Fatal(err)
		}
		damageEvents := 0
		for _, event := range events {
			if event.Type == CombatEventDamage {
				damageEvents++
			}
		}
		if damageEvents != 3 || monster.hp != 1328 {
			t.Fatalf("210402 repeated casts=%d hp=%d events=%+v", damageEvents, monster.hp, events)
		}
	})
}

func TestRealCounterDamageModifierAndGlobalCooldownHooks(t *testing.T) {
	catalog := loadTestSkillLogic(t)
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = catalog
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })

	t.Run("110404-damage-received-hook", func(t *testing.T) {
		monster := &monsterUnit{id: 200, hp: 1000, maxHP: 1000, phyAtk: 100, alive: true}
		battle := &battleState{playerHP: 1000, playerMaxHP: 1000, phyAtk: 100, monsters: []*monsterUnit{monster}}
		battle.runtime = NewCombatRuntime(battle, 100, nil)
		buff, err := catalog.Plan(110404, 4)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := battle.executeSkillPlan(buff, battle.runtime.Player(), battle.runtime.Player(), func() float64 { return 0 }); err != nil {
			t.Fatal(err)
		}
		attack := SkillPlan{Effects: []SkillEffect{{
			Kind: SkillEffectDamage, Trigger: SkillTrigger{Scope: "skill", Event: 1},
			Target: SkillTargetPlan{Kind: SkillTargetSingle, Side: SkillTargetCastTarget},
			Damage: &SkillDamagePlan{Type: SkillDamagePhysical, Self: SkillFormula{Stat: SkillStatPhysicalAttack, Percent: 100}},
		}}}
		events, err := battle.executeSkillPlan(attack, MonsterCombatUnit(monster.id), battle.runtime.Player(), func() float64 { return 0 })
		if err != nil {
			t.Fatal(err)
		}
		if !containsCombatEvent(events, CombatEventCounter) || monster.hp != 1000 || battle.runtime.DamageMultiplier(battle.runtime.Player()) != 2 {
			t.Fatalf("110404 hook: monsterHP=%d multiplier=%v events=%+v", monster.hp, battle.runtime.DamageMultiplier(battle.runtime.Player()), events)
		}
	})

	t.Run("220201-kill-clears-all-cooldowns", func(t *testing.T) {
		clock := &fakeCombatClock{now: time.Unix(2000, 0)}
		monster := &monsterUnit{id: 200, hp: 150, maxHP: 150, alive: true}
		ss := newSession()
		ss.playerID = 100
		ss.skills = map[int32]int32{220201: 4}
		ss.skillCooldowns = map[int32]time.Time{210401: clock.now.Add(8 * time.Second), 210402: clock.now.Add(12 * time.Second)}
		battle := &battleState{playerHP: 1000, playerMaxHP: 1000, phyAtk: 100, monsters: []*monsterUnit{monster}, owner: ss}
		battle.runtime = NewCombatRuntime(battle, 100, clock)
		if _, err := battle.initializePassiveSkills(ss, func() float64 { return 0 }); err != nil {
			t.Fatal(err)
		}
		attack := SkillPlan{Effects: []SkillEffect{{
			Kind: SkillEffectDamage, Trigger: SkillTrigger{Scope: "skill", Event: 1},
			Target: SkillTargetPlan{Kind: SkillTargetSingle, Side: SkillTargetCastTarget},
			Damage: &SkillDamagePlan{Type: SkillDamageTrue, Self: SkillFormula{Stat: SkillStatPhysicalAttack, Percent: 100}},
		}}}
		events, err := battle.executeSkillPlan(attack, battle.runtime.Player(), MonsterCombatUnit(monster.id), func() float64 { return 0 })
		if err != nil {
			t.Fatal(err)
		}
		if monster.alive || len(ss.skillCooldowns) != 0 || !containsCombatEvent(events, CombatEventGlobalCooldown) {
			t.Fatalf("220201 kill hook: alive=%v cooldowns=%+v events=%+v", monster.alive, ss.skillCooldowns, events)
		}
	})
}

func TestRealMissingHealthPassiveRecalculatesDerivedAttributes(t *testing.T) {
	catalog := loadTestSkillLogic(t)
	oldCatalog, oldTables := skillLogicCatalog, tables
	skillLogicCatalog = catalog
	tables = &datatables{skillConfig: map[int64]map[string]interface{}{
		12020101: {"Args0": float64(10)},
	}}
	t.Cleanup(func() { skillLogicCatalog, tables = oldCatalog, oldTables })

	monster := &monsterUnit{id: 200, hp: 1000, maxHP: 1000, phyAtk: 250, alive: true}
	ss := newSession()
	ss.playerID = 100
	ss.skills = map[int32]int32{120201: 1}
	battle := &battleState{playerHP: 1000, playerMaxHP: 1000, phyDef: 100, monsters: []*monsterUnit{monster}, owner: ss}
	clock := &fakeCombatClock{now: time.Unix(3000, 0)}
	battle.runtime = NewCombatRuntime(battle, 100, clock)
	if _, err := battle.initializePassiveSkills(ss, func() float64 { return 0 }); err != nil {
		t.Fatal(err)
	}
	attack := SkillPlan{Effects: []SkillEffect{{
		Kind: SkillEffectDamage, Trigger: SkillTrigger{Scope: "skill", Event: 1},
		Target: SkillTargetPlan{Kind: SkillTargetSingle, Side: SkillTargetCastTarget},
		Damage: &SkillDamagePlan{Type: SkillDamageTrue, Self: SkillFormula{Stat: SkillStatPhysicalAttack, Percent: 100}},
	}}}
	if _, err := battle.executeSkillPlan(attack, MonsterCombatUnit(monster.id), battle.runtime.Player(), func() float64 { return 0 }); err != nil {
		t.Fatal(err)
	}
	player := battle.runtime.Player()
	if got := battle.runtime.EffectiveAttribute(player, CombatAttributePhysicalDefense); got != 140 {
		t.Fatalf("25%% missing HP physical defense=%d, want 140", got)
	}
	if got := battle.runtime.EffectivePercentAttribute(player, CombatAttributeSpiritualDamageReduction); math.Abs(got-3) > 1e-9 {
		t.Fatalf("25%% missing HP spiritual reduction=%v, want 3", got)
	}
	if _, err := battle.runtime.ApplyEffects(CombatEffectContext{Source: player, Target: player}, []CombatEffect{EffectSpec{
		Key: "test-hot", Kind: CombatEffectHealOverTime, Value: 150, Interval: time.Second, Duration: time.Second,
	}}); err != nil {
		t.Fatal(err)
	}
	clock.Add(time.Second)
	timed := battle.runtime.Tick()
	if _, err := battle.triggerTimedEventHooks(timed); err != nil {
		t.Fatal(err)
	}
	if battle.playerHP != 900 || battle.runtime.EffectiveAttribute(player, CombatAttributePhysicalDefense) != 120 {
		t.Fatalf("HOT passive did not downgrade: hp=%d defense=%d", battle.playerHP, battle.runtime.EffectiveAttribute(player, CombatAttributePhysicalDefense))
	}
	if got := battle.runtime.EffectivePercentAttribute(player, CombatAttributeSpiritualDamageReduction); math.Abs(got-1.5) > 1e-9 {
		t.Fatalf("healed spiritual reduction=%v, want 1.5", got)
	}
}

func TestCritAndTypedDamageReductionAffectAuthoritativeDamage(t *testing.T) {
	monster := &monsterUnit{id: 200, hp: 1000, maxHP: 1000, alive: true}
	battle := &battleState{playerHP: 1000, playerMaxHP: 1000, phyAtk: 100, monsters: []*monsterUnit{monster}}
	battle.runtime = NewCombatRuntime(battle, 100, nil)
	player, target := battle.runtime.Player(), MonsterCombatUnit(monster.id)
	for _, spec := range []EffectSpec{
		{Key: "crit", Kind: CombatEffectAttribute, Attribute: CombatAttributePhysicalCritRate, Percent: 100},
		{Key: "reduction", Kind: CombatEffectAttribute, Attribute: CombatAttributePhysicalDamageReduction, Percent: 25},
	} {
		if _, err := battle.runtime.ApplyEffects(CombatEffectContext{Source: player, Target: map[bool]CombatUnitRef{true: player, false: target}[spec.Key == "crit"]}, []CombatEffect{spec}); err != nil {
			t.Fatal(err)
		}
	}
	amount, critical := battle.damageOutcome(&SkillDamagePlan{
		Type: SkillDamagePhysical, CanCrit: true,
		Self: SkillFormula{Stat: SkillStatPhysicalAttack, Percent: 100},
	}, player, target, newSkillExecutionContext(battle, func() float64 { return 0 }))
	if !critical || amount != 113 {
		t.Fatalf("crit/reduction outcome amount=%d critical=%v, want 113/true", amount, critical)
	}
}

func TestEveryActiveSkillPlanExecutesInCombatRuntime(t *testing.T) {
	catalog := loadTestSkillLogic(t)
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = catalog
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })
	for id, skill := range catalog.Skills {
		if skill.SkillType == 1 {
			continue
		}
		for level := int32(1); level <= skill.MaxLevel; level++ {
			for _, rolled := range []float64{0, 0.999999} {
				monster := &monsterUnit{
					id: 200, hp: 1_000_000, maxHP: 1_000_000,
					phyAtk: 1000, spiAtk: 1000, phyDef: 100, spiDef: 100, alive: true,
				}
				battle := &battleState{
					playerHP: 1_000_000, playerMaxHP: 1_000_000,
					phyAtk: 1000, spiAtk: 1000, phyDef: 100, spiDef: 100,
					monsters: []*monsterUnit{monster},
				}
				battle.runtime = NewCombatRuntime(battle, 100, nil)
				source, primary := battle.runtime.Player(), MonsterCombatUnit(monster.id)
				if id >= 500001 && id <= 500042 {
					source, primary = primary, source
				}
				plan, err := catalog.Plan(id, level)
				if err != nil {
					t.Fatalf("Plan(%d,%d): %v", id, level, err)
				}
				if _, err := battle.executeSkillPlan(plan, source, primary, func() float64 { return rolled }); err != nil {
					t.Errorf("execute skill %d level %d roll %.3f: %v", id, level, rolled, err)
				}
			}
		}
	}
}

func TestCombatEventsPushStateHPAndDeaths(t *testing.T) {
	conn := &recordingConn{}
	ss := newSession()
	ss.playerID = 100
	ch := &channel{conn: conn, session: ss}
	battle := &battleState{effectMeta: map[string]battleEffectMetadata{
		"modifier:42:dot": {modifierID: 42, durationMS: 5000, stateKey: 104, iconID: "bufficon_bleed", isBuff: false},
	}}
	server := &Server{}
	server.emitCombatEvents(ch, battle, []CombatEvent{
		{Type: CombatEventEffectApplied, Target: PlayerCombatUnit(100), Key: "modifier:42:dot"},
		{Type: CombatEventDamage, Source: MonsterCombatUnit(200), Target: PlayerCombatUnit(100), Key: "modifier:42:dot", Amount: 5, HPAfter: 95},
		{Type: CombatEventDeath, Target: MonsterCombatUnit(200)},
		{Type: CombatEventDeath, Target: PlayerCombatUnit(100)},
	})
	want := map[uint16]bool{
		protocol.OpM2C_BattleChangeState:    false,
		protocol.OpM2C_BattleSkillRet:       false,
		protocol.OpM2C_SyncUnitAttribute:    false,
		protocol.OpM2C_MainstoryMonsterDead: false,
		protocol.OpM2C_UnitDead:             false,
	}
	for _, opcode := range recordedOpcodes(t, conn.Bytes()) {
		// 20081 M2C_BattleTouchState must never show up: the client handler is an
		// empty async body and the capture archive has no sample of it. The DOT
		// numbers ride on 20078 and the icon on 20080.
		if opcode == protocol.OpM2C_BattleTouchState {
			t.Fatal("20081 M2C_BattleTouchState was emitted for a periodic tick")
		}
		if _, expected := want[opcode]; expected {
			want[opcode] = true
		}
	}
	for opcode, seen := range want {
		if !seen {
			t.Errorf("opcode %d was not emitted", opcode)
		}
	}
}

func TestPlayerDamageSyncsAbsoluteHPBeforeDeath(t *testing.T) {
	conn := &recordingConn{}
	ss := newSession()
	ss.playerID = 100
	ch := &channel{conn: conn, session: ss}

	(&Server{}).emitCombatEvents(ch, &battleState{}, []CombatEvent{
		{Type: CombatEventDamage, Source: MonsterCombatUnit(200), Target: PlayerCombatUnit(100), Amount: 100, HPAfter: 0},
		{Type: CombatEventDeath, Target: PlayerCombatUnit(100)},
	})

	data := conn.Bytes()
	seenHPZero := false
	for len(data) > 0 {
		length := int(binary.LittleEndian.Uint16(data[:2]))
		opcode := binary.LittleEndian.Uint16(data[2:4])
		body := data[4 : length+2]
		switch opcode {
		case protocol.OpM2C_SyncUnitAttribute:
			message := &protocol.M2C_SyncUnitAttribute{}
			if err := gproto.Unmarshal(body, message); err != nil {
				t.Fatal(err)
			}
			if message.UnitId == 100 && message.NumericType == 1001 && message.Value == 0 {
				seenHPZero = true
			}
		case protocol.OpM2C_UnitDead:
			if !seenHPZero {
				t.Fatal("UnitDead was emitted before the authoritative HP=0 sync")
			}
		}
		data = data[length+2:]
	}
	if !seenHPZero {
		t.Fatal("missing authoritative player HP=0 sync")
	}
}

func TestMonsterHPPushUsesNumericSyncWithoutRebuildingBattle(t *testing.T) {
	conn := &recordingConn{}
	ss := newSession()
	ss.playerID = 100
	monster := &monsterUnit{id: 200, monsterID: 10257, hp: 750, maxHP: 1000, alive: true}
	(&Server{}).pushMonsterHP(&channel{conn: conn, session: ss}, monster)

	want := map[int32]float32{1002: 1000, 1001: 750}
	data := conn.Bytes()
	for len(data) > 0 {
		length := int(binary.LittleEndian.Uint16(data[:2]))
		opcode := binary.LittleEndian.Uint16(data[2:4])
		if opcode == protocol.OpM2C_ReMainStoryMonsterInfo {
			t.Fatal("normal HP update emitted battle reconstruction opcode 20053")
		}
		if opcode != protocol.OpM2C_SyncUnitAttribute {
			t.Fatalf("monster HP update opcode = %d, want 20169", opcode)
		}
		message := &protocol.M2C_SyncUnitAttribute{}
		if err := gproto.Unmarshal(data[4:length+2], message); err != nil {
			t.Fatalf("decode monster numeric update: %v", err)
		}
		value, ok := want[message.NumericType]
		if !ok || message.UnitId != monster.id || message.Value != value {
			t.Fatalf("unexpected monster numeric update: %+v", message)
		}
		delete(want, message.NumericType)
		data = data[length+2:]
	}
	if len(want) != 0 {
		t.Fatalf("missing monster numeric updates: %+v", want)
	}
}

func TestMonsterOpeningHPPushInitializesPrefabBar(t *testing.T) {
	conn := &recordingConn{}
	ss := newSession()
	ss.playerID = 101
	monster := &monsterUnit{id: 201, monsterID: 10257, hp: 321, maxHP: 654, alive: true}
	(&Server{}).pushMonsterOpeningHP(&channel{conn: conn, session: ss}, []*monsterUnit{monster})
	frames := decodeRecordedFrames(t, conn.Bytes())
	if len(frames) != 2 {
		t.Fatalf("opening HP frame count=%d, want 2", len(frames))
	}
	want := []struct {
		numeric int32
		value   float32
	}{{1002, 654}, {1001, 321}}
	for i, frame := range frames {
		if frame.opcode != protocol.OpM2C_SyncUnitAttribute {
			t.Fatalf("frame %d opcode=%d, want 20169", i, frame.opcode)
		}
		message := &protocol.M2C_SyncUnitAttribute{}
		if err := gproto.Unmarshal(frame.body, message); err != nil {
			t.Fatal(err)
		}
		if message.UnitId != monster.id || message.NumericType != want[i].numeric || message.Value != want[i].value {
			t.Fatalf("frame %d = %+v, want unit=%d numeric=%d value=%v", i, message, monster.id, want[i].numeric, want[i].value)
		}
	}
}

func TestCombatStateProtocolUsesClientCompatibleExpiryAndRemoval(t *testing.T) {
	battle := &battleState{effectMeta: map[string]battleEffectMetadata{
		"modifier:42:dot": {modifierID: 42, durationMS: 5000, stateKey: 104, iconID: "bufficon_bleed", isBuff: false},
	}}
	target := PlayerCombatUnit(100)
	server := &Server{}

	t.Run("add keeps configured lifetime", func(t *testing.T) {
		conn := &recordingConn{}
		server.emitCombatEvents(&channel{conn: conn, session: newSession()}, battle, []CombatEvent{{
			Type: CombatEventEffectApplied, Target: target, Key: "modifier:42:dot",
		}})
		message := recordedCombatState(t, conn.Bytes())
		if message.Type != protocol.ChangeType_Add || message.Time != 5000 {
			t.Fatalf("add state = %+v", message)
		}
	})

	t.Run("natural expiry relies on client timer", func(t *testing.T) {
		conn := &recordingConn{}
		server.emitCombatEvents(&channel{conn: conn, session: newSession()}, battle, []CombatEvent{{
			Type: CombatEventEffectExpired, Target: target, Key: "modifier:42:dot",
		}})
		if data := conn.Bytes(); len(data) != 0 {
			t.Fatalf("natural expiry reinserted client buff: %x", data)
		}
	})

	t.Run("battle end keeps the client timer instead of zero duration", func(t *testing.T) {
		conn := &recordingConn{}
		server.emitCombatEvents(&channel{conn: conn, session: newSession()}, battle, []CombatEvent{{
			Type: CombatEventEffectRemoved, Target: target, Key: "modifier:42:dot", Reason: "battle-ended",
		}})
		// 线上不发 Time=0，客户端按 Add 包里的 Time 自行销毁图标与特效。
		if data := conn.Bytes(); len(data) != 0 {
			t.Fatalf("battle-ended forced a zero-time buff state: %x", data)
		}
	})

	for _, eventType := range []CombatEventType{CombatEventEffectRemoved, CombatEventEffectDispelled} {
		t.Run(strconv.Itoa(int(eventType)), func(t *testing.T) {
			conn := &recordingConn{}
			ch := &channel{conn: conn, session: newSession()}
			server.emitCombatEvents(ch, battle, []CombatEvent{{
				Type: CombatEventEffectApplied, Target: target, Key: "modifier:42:dot",
			}})
			server.emitCombatEvents(ch, battle, []CombatEvent{{
				Type: eventType, Target: target, Key: "modifier:42:dot",
			}})
			states := decodeCombatStates(t, conn.Bytes())
			if len(states) != 2 || states[0].Id != states[1].Id {
				t.Fatalf("state lifecycle = %+v", states)
			}
			message := states[1]
			if message.Type != protocol.ChangeType_Reduce || message.Time != 0 {
				t.Fatalf("forced removal state = %+v", message)
			}
		})
	}
}

func TestSettleCombatCancelsRuntimeForVictoryAndDefeat(t *testing.T) {
	oldDelay := battleVictoryDelay
	battleVictoryDelay = 0
	t.Cleanup(func() { battleVictoryDelay = oldDelay })
	for _, defeat := range []bool{false, true} {
		t.Run(map[bool]string{false: "victory", true: "defeat"}[defeat], func(t *testing.T) {
			conn := &recordingConn{}
			ss := newSession()
			ss.tasks, ss.killCount = map[int32]int32{}, map[int32]int32{}
			monster := &monsterUnit{id: 200, hp: 0, maxHP: 100, alive: false}
			battle := &battleState{playerHP: 100, playerMaxHP: 100, monsters: []*monsterUnit{monster}}
			if defeat {
				battle.playerHP, monster.hp, monster.alive = 0, 100, true
			}
			battle.runtime = NewCombatRuntime(battle, 0, nil)
			ss.battle = battle
			ch := &channel{conn: conn, session: ss}
			if !(&Server{}).settleCombatLocked(ch, battle) {
				t.Fatal("terminal combat was not settled")
			}
			if !battle.ended || !battle.runtime.Cancelled() || ss.battle != nil {
				t.Fatalf("terminal cleanup: ended=%v cancelled=%v battle=%p", battle.ended, battle.runtime.Cancelled(), ss.battle)
			}
		})
	}
}

func containsCombatEvent(events []CombatEvent, eventType CombatEventType) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

type recordingConn struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (conn *recordingConn) Read([]byte) (int, error) { return 0, io.EOF }
func (conn *recordingConn) Write(data []byte) (int, error) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	return conn.buffer.Write(data)
}
func (conn *recordingConn) Close() error                     { return nil }
func (conn *recordingConn) LocalAddr() net.Addr              { return recordingAddr("local") }
func (conn *recordingConn) RemoteAddr() net.Addr             { return recordingAddr("remote") }
func (conn *recordingConn) SetDeadline(time.Time) error      { return nil }
func (conn *recordingConn) SetReadDeadline(time.Time) error  { return nil }
func (conn *recordingConn) SetWriteDeadline(time.Time) error { return nil }
func (conn *recordingConn) Bytes() []byte {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	return append([]byte(nil), conn.buffer.Bytes()...)
}

type recordingAddr string

func (addr recordingAddr) Network() string { return "test" }
func (addr recordingAddr) String() string  { return string(addr) }

func recordedOpcodes(t *testing.T, data []byte) []uint16 {
	t.Helper()
	var opcodes []uint16
	for len(data) > 0 {
		if len(data) < 4 {
			t.Fatalf("truncated frame: %x", data)
		}
		length := int(binary.LittleEndian.Uint16(data[:2]))
		if length < 2 || len(data) < length+2 {
			t.Fatalf("invalid frame length %d in %x", length, data)
		}
		opcodes = append(opcodes, binary.LittleEndian.Uint16(data[2:4]))
		data = data[length+2:]
	}
	return opcodes
}

func recordedSkillVisuals(t *testing.T, data []byte) []*protocol.M2C_PlaySkillEffect {
	t.Helper()
	var visuals []*protocol.M2C_PlaySkillEffect
	for len(data) > 0 {
		if len(data) < 4 {
			t.Fatalf("truncated frame: %x", data)
		}
		length := int(binary.LittleEndian.Uint16(data[:2]))
		if length < 2 || len(data) < length+2 {
			t.Fatalf("invalid frame length %d in %x", length, data)
		}
		if binary.LittleEndian.Uint16(data[2:4]) == protocol.OpM2C_PlaySkillEffect {
			message := &protocol.M2C_PlaySkillEffect{}
			if err := gproto.Unmarshal(data[4:length+2], message); err != nil {
				t.Fatalf("decode skill visual: %v", err)
			}
			visuals = append(visuals, message)
		}
		data = data[length+2:]
	}
	return visuals
}

func recordedCombatState(t *testing.T, data []byte) *protocol.M2C_BattleChangeState {
	t.Helper()
	if len(data) < 4 {
		t.Fatalf("missing combat state frame: %x", data)
	}
	length := int(binary.LittleEndian.Uint16(data[:2]))
	if length < 2 || len(data) != length+2 {
		t.Fatalf("invalid combat state frame length %d in %x", length, data)
	}
	if opcode := binary.LittleEndian.Uint16(data[2:4]); opcode != protocol.OpM2C_BattleChangeState {
		t.Fatalf("combat state opcode = %d", opcode)
	}
	message := &protocol.M2C_BattleChangeState{}
	if err := gproto.Unmarshal(data[4:], message); err != nil {
		t.Fatalf("decode combat state: %v", err)
	}
	return message
}
