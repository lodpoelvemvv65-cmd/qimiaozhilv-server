package main

import (
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func TestFifthTierFamilyBossOpeningMultiplierMatchesOnlineCapture(t *testing.T) {
	loadOnlineTablesForTest(t)

	owner := &session{playerID: 1}
	units := owner.buildMonsterUnitsFromRoster([]int32{50005}, []int{1})
	if len(units) != 1 {
		t.Fatalf("family boss units=%d, want 1", len(units))
	}
	boss := units[0]
	baseHP := boss.maxHP
	baseReductions := map[int32]float64{1022: boss.extraNumeric[1022], 1023: boss.extraNumeric[1023]}

	if !applyFamilyBossOpeningMultiplier(units, battlePresentation{kind: presentationFamilyBoss, familyBossID: 5}) {
		t.Fatal("fifth-tier family boss multiplier was not applied")
	}
	if boss.phyAtk != 3_672_000 || boss.spiAtk != 3_672_000 ||
		boss.phyDef != 3_208_532 || boss.spiDef != 2_585_411 {
		t.Fatalf("scaled boss core stats atk=%d/%d def=%d/%d",
			boss.phyAtk, boss.spiAtk, boss.phyDef, boss.spiDef)
	}
	if boss.hp != baseHP || boss.maxHP != baseHP {
		t.Fatalf("family boss HP was scaled: hp=%d/%d want %d", boss.hp, boss.maxHP, baseHP)
	}
	for numericType, want := range map[int32]float64{
		1013: 1.989, 1014: 1.989, 1015: 2.295, 1016: 2.295, 1017: 3.06,
		1018: 3.672, 1019: 3.06, 1020: 3.672, 1021: 3.06,
	} {
		if got := boss.extraNumeric[numericType]; math.Abs(got-want) > 1e-9 {
			t.Errorf("scaled numeric %d=%v, want %v", numericType, got, want)
		}
	}
	for numericType, want := range baseReductions {
		if got := boss.extraNumeric[numericType]; got != want {
			t.Errorf("damage reduction %d=%v, want unchanged %v", numericType, got, want)
		}
	}
}

func TestScaledFamilyBossIntegerUsesOnlineRounding(t *testing.T) {
	if got := scaledFamilyBossInteger(1_689_811, 1.53); got != 2_585_411 {
		t.Fatalf("scaled spiritual defense=%d, want capture-rounded 2585411", got)
	}
}

func TestFamilyBossOpeningMultiplierAndStackStepUseTierFormula(t *testing.T) {
	loadOnlineTablesForTest(t)

	tiers := []struct {
		bossID       int32
		monsterID    int32
		characterAdd float64
		multiplier   float64
	}{
		{bossID: 1, monsterID: 50001, characterAdd: 0.05, multiplier: 1.25},
		{bossID: 2, monsterID: 50002, characterAdd: 0.08, multiplier: 1.28},
		{bossID: 3, monsterID: 50003, characterAdd: 0.15, multiplier: 1.35},
		{bossID: 4, monsterID: 50004, characterAdd: 0.23, multiplier: 1.43},
		{bossID: 5, monsterID: 50005, characterAdd: 0.33, multiplier: 1.53},
	}
	for _, tier := range tiers {
		t.Run(fmt.Sprintf("tier_%d", tier.bossID), func(t *testing.T) {
			owner := &session{playerID: 1}
			units := owner.buildMonsterUnitsFromRoster([]int32{tier.monsterID}, []int{1})
			if len(units) != 1 {
				t.Fatalf("family boss units=%d, want 1", len(units))
			}
			boss := units[0]
			baseHP := boss.maxHP
			baseCore := []int32{boss.phyAtk, boss.spiAtk, boss.phyDef, boss.spiDef}
			baseExtra := make(map[int32]float64, len(familyBossOpeningScaledNumericTypes))
			for _, numericType := range familyBossOpeningScaledNumericTypes {
				baseExtra[numericType] = boss.extraNumeric[numericType]
			}

			presentation := battlePresentation{kind: presentationFamilyBoss, familyBossID: tier.bossID}
			if !applyFamilyBossOpeningMultiplier(units, presentation) {
				t.Fatal("family boss opening multiplier was not applied")
			}
			gotCore := []int32{boss.phyAtk, boss.spiAtk, boss.phyDef, boss.spiDef}
			for index, got := range gotCore {
				if want := scaledFamilyBossInteger(baseCore[index], tier.multiplier); got != want {
					t.Errorf("core attribute[%d]=%d, want %d", index, got, want)
				}
			}
			for numericType, base := range baseExtra {
				if got, want := boss.extraNumeric[numericType], base*tier.multiplier; math.Abs(got-want) > 1e-9 {
					t.Errorf("numeric %d=%v, want %v", numericType, got, want)
				}
			}
			if boss.hp != baseHP || boss.maxHP != baseHP {
				t.Errorf("HP changed to %d/%d, want %d", boss.hp, boss.maxHP, baseHP)
			}

			clock := &fakeCombatClock{now: time.Unix(2_000, 0)}
			battle := &battleState{monsters: units}
			runtime := NewCombatRuntime(battle, 1, clock)
			runtime.initializeFamilyBossCombatScaling(presentation, clock.now)
			if runtime.familyBossScaling == nil {
				t.Fatal("family boss stack scaling was not initialized")
			}
			if got, want := runtime.familyBossScaling.stepPercent, tier.characterAdd*100; math.Abs(got-want) > 1e-9 {
				t.Errorf("stack step=%v, want %v", got, want)
			}
			if runtime.familyBossScaling.interval != 4710*time.Millisecond {
				t.Errorf("stack interval=%v, want 4710ms", runtime.familyBossScaling.interval)
			}
		})
	}
}

func newFamilyBossScalingTestRuntime() (*CombatRuntime, *fakeCombatClock, CombatUnitRef) {
	clock := &fakeCombatClock{now: time.Unix(2_000, 0)}
	boss := &monsterUnit{
		id: 9001, hp: 10_000, maxHP: 10_000, alive: true,
		phyAtk: 1_000, spiAtk: 2_000, phyDef: 3_000, spiDef: 4_000,
		extraNumeric: map[int32]float64{
			1013: 0.10, 1014: 0.20, 1015: 0.30, 1016: 0.40, 1017: 0.50,
			1018: 0.60, 1019: 0.70, 1020: 0.80, 1021: 0.90,
			1022: 0.25, 1023: 0.35,
		},
	}
	battle := &battleState{
		playerHP: 100, playerMaxHP: 100, monsters: []*monsterUnit{boss},
	}
	runtime := NewCombatRuntime(battle, 100, clock)
	runtime.familyBossScaling = &familyBossCombatScaling{
		target: MonsterCombatUnit(boss.id), stepPercent: 33,
		interval: 4710 * time.Millisecond, nextTick: clock.now.Add(4710 * time.Millisecond),
		stacks: make(map[CombatAttribute]int, len(familyBossStackedAttributes)),
	}
	battle.runtime = runtime
	return runtime, clock, MonsterCombatUnit(boss.id)
}

func attributeChangeEvents(events []CombatEvent) []CombatEvent {
	changes := make([]CombatEvent, 0, len(events))
	for _, event := range events {
		if event.Type == CombatEventAttributeChanged {
			changes = append(changes, event)
		}
	}
	return changes
}

func TestFamilyBossScalingCadenceFormulaAndEffectReset(t *testing.T) {
	runtime, clock, boss := newFamilyBossScalingTestRuntime()

	clock.Add(4709 * time.Millisecond)
	if events := runtime.Tick(); len(events) != 0 {
		t.Fatalf("family boss scaling fired before 4710ms: %+v", events)
	}
	clock.Add(time.Millisecond)
	events := attributeChangeEvents(runtime.Tick())
	if len(events) != len(familyBossStackedAttributes) {
		t.Fatalf("first stack emitted %d attributes, want %d", len(events), len(familyBossStackedAttributes))
	}
	for index, event := range events {
		if event.Attribute != familyBossStackedAttributes[index] {
			t.Errorf("first stack attribute[%d]=%d, want %d", index, event.Attribute, familyBossStackedAttributes[index])
		}
	}
	if got := runtime.EffectiveAttribute(boss, CombatAttributePhysicalAttack); got != 1330 {
		t.Fatalf("first stack attack=%d, want 1330", got)
	}
	clock.Add(4710 * time.Millisecond)
	if events := attributeChangeEvents(runtime.Tick()); len(events) != len(familyBossStackedAttributes) {
		t.Fatalf("second stack emitted %d attributes, want %d", len(events), len(familyBossStackedAttributes))
	}
	if got := runtime.EffectiveAttribute(boss, CombatAttributePhysicalAttack); got != 1660 {
		t.Fatalf("second stack attack=%d, want 1660", got)
	}
	if got := runtime.EffectivePercentAttribute(boss, CombatAttributePhysicalCritRate); got != 10 {
		t.Fatalf("stack changed opening-only physical crit rate to %v, want 10", got)
	}

	spec := EffectSpec{
		Key: "capture-break-defense", Kind: CombatEffectAttribute,
		Attribute: CombatAttributePhysicalAttack, Percent: -30,
		Duration: 20 * time.Second, StackMode: CombatEffectRefresh,
	}
	events = applyTestEffect(t, runtime, runtime.Player(), boss, spec)
	if changes := attributeChangeEvents(events); len(changes) != 1 || changes[0].Attribute != CombatAttributePhysicalAttack {
		t.Fatalf("new external modifier reset emitted %d attributes", len(changes))
	}
	if runtime.familyBossScaling.stacks[CombatAttributePhysicalAttack] != 0 ||
		runtime.familyBossScaling.stacks[CombatAttributeSpiritualDefense] != 2 ||
		runtime.EffectiveAttribute(boss, CombatAttributePhysicalAttack) != 700 ||
		runtime.EffectiveAttribute(boss, CombatAttributeSpiritualDefense) != 6640 {
		t.Fatalf("modifier reset stacks=%v attack=%d spiritualDefense=%d, want physical 0/spiritual 2 and 700/6640",
			runtime.familyBossScaling.stacks, runtime.EffectiveAttribute(boss, CombatAttributePhysicalAttack),
			runtime.EffectiveAttribute(boss, CombatAttributeSpiritualDefense))
	}

	clock.Add(4710 * time.Millisecond)
	runtime.Tick()
	if runtime.familyBossScaling.stacks[CombatAttributePhysicalAttack] != 1 ||
		runtime.familyBossScaling.stacks[CombatAttributeSpiritualDefense] != 3 ||
		runtime.EffectiveAttribute(boss, CombatAttributePhysicalAttack) != 1030 ||
		runtime.EffectiveAttribute(boss, CombatAttributeSpiritualDefense) != 7960 {
		t.Fatalf("post-reset stacks=%v attack=%d spiritualDefense=%d, want physical 1/spiritual 3 and 1030/7960",
			runtime.familyBossScaling.stacks, runtime.EffectiveAttribute(boss, CombatAttributePhysicalAttack),
			runtime.EffectiveAttribute(boss, CombatAttributeSpiritualDefense))
	}
	spec.Duration = time.Second
	events = applyTestEffect(t, runtime, runtime.Player(), boss, spec)
	if len(attributeChangeEvents(events)) != 1 ||
		runtime.familyBossScaling.stacks[CombatAttributePhysicalAttack] != 1 ||
		runtime.familyBossScaling.stacks[CombatAttributeSpiritualDefense] != 3 {
		t.Fatalf("active refresh changed stack state: stacks=%v events=%+v", runtime.familyBossScaling.stacks, events)
	}
	clock.Add(time.Second)
	events = runtime.Tick()
	if changes := attributeChangeEvents(events); len(changes) != 1 || changes[0].Attribute != CombatAttributePhysicalAttack {
		t.Fatalf("modifier expiry reset emitted %d attributes", len(changes))
	}
	if runtime.familyBossScaling.stacks[CombatAttributePhysicalAttack] != 0 ||
		runtime.familyBossScaling.stacks[CombatAttributeSpiritualDefense] != 3 ||
		runtime.EffectiveAttribute(boss, CombatAttributePhysicalAttack) != 1000 ||
		runtime.EffectiveAttribute(boss, CombatAttributeSpiritualDefense) != 7960 {
		t.Fatalf("expiry reset stacks=%v attack=%d spiritualDefense=%d, want physical 0/spiritual 3 and 1000/7960",
			runtime.familyBossScaling.stacks, runtime.EffectiveAttribute(boss, CombatAttributePhysicalAttack),
			runtime.EffectiveAttribute(boss, CombatAttributeSpiritualDefense))
	}
}

func TestFamilyBossScalingSharedPartyAdvancesOnce(t *testing.T) {
	canonical, clock, _ := newFamilyBossScalingTestRuntime()
	secondBattle := &battleState{playerHP: 100, playerMaxHP: 100, monsters: canonical.battle.monsters}
	second := NewCombatRuntime(secondBattle, 200, clock)
	second.shared = canonical
	secondBattle.runtime = second

	clock.Add(4710 * time.Millisecond)
	if got := len(attributeChangeEvents(canonical.Tick())); got != len(familyBossStackedAttributes) {
		t.Fatalf("canonical tick changes=%d", got)
	}
	if events := second.Tick(); len(events) != 0 {
		t.Fatalf("second party runtime advanced shared cadence twice: %+v", events)
	}
	for _, attribute := range familyBossStackedAttributes {
		if canonical.familyBossScaling.stacks[attribute] != 1 {
			t.Fatalf("shared family boss attribute %d stacks=%d, want 1",
				attribute, canonical.familyBossScaling.stacks[attribute])
		}
	}
}

func TestFamilyBossOpeningAndIncrementalAttributePackets(t *testing.T) {
	runtime, _, bossRef := newFamilyBossScalingTestRuntime()
	boss := runtime.battle.monsters[0]
	server := &Server{}

	for _, resend := range []bool{false, true} {
		conn := &recordingConn{}
		ss := &session{playerID: 100, battle: runtime.battle}
		ch := &channel{id: 1, conn: conn, session: ss}
		server.pushFamilyBossPresentation(ch, 5, boss, resend)
		frames := decodeRecordedFrames(t, conn.Bytes())
		wantOpeningOpcode := uint16(protocol.OpM2C_SendFamilyBossInfo)
		if resend {
			wantOpeningOpcode = protocol.OpM2C_ReSendFamilyBossInfo
		}
		if len(frames) != 2 || frames[0].opcode != wantOpeningOpcode ||
			frames[1].opcode != protocol.OpM2C_SyncUnitAttributeList {
			t.Fatalf("resend=%v opening opcodes=%v", resend, recordedOpcodes(t, conn.Bytes()))
		}
		var message protocol.M2C_SyncUnitAttributeList
		if err := proto.Unmarshal(frames[1].body, &message); err != nil {
			t.Fatal(err)
		}
		attributes := decodeAttributeMapList(t, frames[1].body, 2)
		wantTypes := []int32{1001, 1002, 1009, 1010, 1011, 1012, 1013, 1014, 1015, 1016, 1017, 1018, 1019, 1020, 1021, 1022, 1023}
		gotTypes := make([]int32, 0, len(attributes))
		values := make(map[int32]float32, len(attributes))
		for index := range attributes {
			attribute := &attributes[index]
			gotTypes = append(gotTypes, attribute.Key)
			values[attribute.Key] = attribute.Value
		}
		if message.UnitId != boss.id || message.ActorId != ss.playerID || !reflect.DeepEqual(gotTypes, wantTypes) {
			t.Fatalf("resend=%v opening attributes unit=%d actor=%d types=%v", resend, message.UnitId, message.ActorId, gotTypes)
		}
		if values[1001] != float32(boss.hp) || values[1002] != float32(boss.maxHP) ||
			values[1022] != float32(boss.extraNumeric[1022]) || values[1023] != float32(boss.extraNumeric[1023]) {
			t.Fatalf("opening HP/reductions changed: values=%v", values)
		}
	}

	conn := &recordingConn{}
	ch := &channel{id: 2, conn: conn, session: &session{playerID: 100, battle: runtime.battle}}
	server.pushFamilyBossAttributeChanges(ch, []CombatEvent{
		{Type: CombatEventAttributeChanged, Target: bossRef, Attribute: CombatAttributePhysicalAttack, AttributeValue: 1330},
		{Type: CombatEventAttributeChanged, Target: bossRef, Attribute: CombatAttributePhysicalAttack, AttributeValue: 700},
		{Type: CombatEventAttributeChanged, Target: bossRef, Attribute: CombatAttributeSpiritualDefense, AttributeValue: 5320},
	})
	frames := decodeRecordedFrames(t, conn.Bytes())
	if len(frames) != 1 || frames[0].opcode != protocol.OpM2C_SyncUnitAttributeList {
		t.Fatalf("incremental packet opcodes=%v", recordedOpcodes(t, conn.Bytes()))
	}
	attributes := decodeAttributeMapList(t, frames[0].body, 2)
	wantTypes := []int32{1009, 1009, 1012}
	wantValues := []float32{1330, 700, 5320}
	if len(attributes) != len(wantTypes) {
		t.Fatalf("incremental attribute count=%d, want %d", len(attributes), len(wantTypes))
	}
	for index := range attributes {
		attribute := &attributes[index]
		if attribute.Key != wantTypes[index] || attribute.Value != wantValues[index] {
			t.Fatalf("incremental attribute[%d]=%d/%v, want %d/%v",
				index, attribute.Key, attribute.Value, wantTypes[index], wantValues[index])
		}
	}
}
