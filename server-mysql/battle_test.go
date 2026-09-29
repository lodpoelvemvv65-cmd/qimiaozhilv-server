package main

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"mhqserver/protocol"
)

func TestMainStoryRegionUsesCurrentBeachLayer(t *testing.T) {
	ss := newSession()
	ss.mapID = 1000607
	if got := mainStoryRegionForSession(ss, 0); got != 1007 {
		t.Fatalf("resolved region = %d, want 1007", got)
	}
	if got := mainStoryRegionForSession(ss, 1003); got != 1007 {
		t.Fatalf("stale explicit region = %d, want current layer 1007", got)
	}
	ss.mapID = 1000606
	if got := mainStoryRegionForSession(ss, 1); got != 1006 {
		t.Fatalf("local region on layer 6 = %d, want 1006", got)
	}
	ss.mapID = 2000101
	if got := mainStoryRegionForSession(ss, 1003); got != 1003 {
		t.Fatalf("non-beach explicit region = %d, want 1003", got)
	}
}

func TestNativeBeachRegionStartsOnlyClickedEncounterRoster(t *testing.T) {
	loadOnlineTablesForTest(t)
	server := &Server{}
	ch, _ := consistencyChannel(900001, 1000604)

	started, count := server.startMainStoryEncounter(ch, 1004, 1)
	if !started || count != 2 {
		t.Fatalf("start encounter = (%v,%d), want (true,2)", started, count)
	}
	for _, monster := range ch.session.battle.monsters {
		if monster.monsterID != 10004 {
			t.Fatalf("clicked fourth-layer encounter included monster %d, want only 10004", monster.monsterID)
		}
	}
}

func TestStartMainStoryFightUsesCompleteRosterOutsideBeach(t *testing.T) {
	loadOnlineTablesForTest(t)
	server := &Server{}
	for _, mapID := range []int32{1001105, 1005205} {
		ch, _ := consistencyChannel(900100+int64(mapID), mapID)
		response := server.onStartMainStoryFight(ch, &protocol.C2M_StartMainStoryFight{RpcId: 1})
		if response.(*protocol.M2C_StartMainStoryFight).Message != "" {
			t.Fatalf("map %d start response = %+v", mapID, response)
		}
		_, wantIDs, wantCounts, _, ok := mainStoryRosterForMap(mapID)
		if !ok {
			t.Fatalf("map %d has no main-story roster", mapID)
		}
		want := expandedRoster(wantIDs, wantCounts)
		if got := len(ch.session.battle.monsters); got != len(want) {
			t.Fatalf("map %d battle monsters = %d, want %d", mapID, got, len(want))
		}
		for index, monster := range ch.session.battle.monsters {
			if monster.monsterID != want[index] {
				t.Fatalf("map %d monster[%d] = %d, want %d", mapID, index, monster.monsterID, want[index])
			}
		}
	}
}

func TestMainStoryFinalVictoryReturnsToCity(t *testing.T) {
	loadOnlineTablesForTest(t)
	server := &Server{}
	for _, mapID := range []int32{1001110, 1005210} {
		ch, _ := consistencyChannel(900200+int64(mapID), mapID)
		region, ids, _, _, ok := mainStoryRosterForMap(mapID)
		if !ok || len(ids) == 0 || !isMainStoryFinalStageMap(mapID) {
			t.Fatalf("map %d is not a configured final main-story stage", mapID)
		}
		ch.session.killCount = make(map[int32]int32)
		ch.session.tasks = make(map[int32]int32)
		battle := &battleState{
			mapID: mapID, region: region, copyID: 10003, owner: ch.session,
			monsters: []*monsterUnit{{id: 10001, monsterID: ids[0], alive: false}},
		}
		ch.session.battle = battle
		ch.session.autoBattle = true
		server.emitVictory(ch, battle)
		if ch.session.mapID != 10004 {
			t.Fatalf("map %d final victory map = %d, want city 10004", mapID, ch.session.mapID)
		}
		if ch.session.battle != nil || ch.session.autoBattle {
			t.Fatalf("map %d final victory left battle/auto state: battle=%v auto=%v", mapID, ch.session.battle, ch.session.autoBattle)
		}
	}
}

func TestBeachLayerTransitionIsSequentialAndRequiresVictory(t *testing.T) {
	if validMapTransition(1000602, 1000604) {
		t.Fatal("beach transition skipped directly from layer 2 to layer 4")
	}
	if !validMapTransition(1000602, 1000603) {
		t.Fatal("adjacent beach transition was rejected")
	}
	ss := newSession()
	ss.mapID = 1000602
	if beachHighestCompletedLayer(ss) >= 2 {
		t.Fatal("new session unexpectedly has beach victory progress")
	}
	if !markBeachLayerVictory(ss, 1002) || beachHighestCompletedLayer(ss) != 2 {
		t.Fatalf("beach victory progress = %d, want 2", beachHighestCompletedLayer(ss))
	}
}

func TestBeachMapStartupCompletesWithoutPrematureTransferPoint(t *testing.T) {
	loadOnlineTablesForTest(t)
	server := &Server{}
	ch, conn := consistencyChannel(7001, 1000601)
	seq := ch.session.markMapChange()

	if !server.finishMapStartup(ch, ch.session.mapID, seq, false) {
		t.Fatal("uncleared beach startup did not complete")
	}
	if _, complete := ch.session.currentMapStartupVersion(); !complete {
		t.Fatal("uncleared beach startup was not marked complete")
	}
	for _, opcode := range recordedOpcodes(t, conn.Bytes()) {
		if opcode == protocol.OpM2C_StartupTransPoint {
			t.Fatal("uncleared beach startup exposed the transfer point")
		}
	}

	cleared, clearedConn := consistencyChannel(7002, 1000601)
	cleared.session.killCount = make(map[int32]int32)
	cleared.session.killCount[beachHighestLayerKey] = 1
	clearedSeq := cleared.session.markMapChange()
	if !server.finishMapStartup(cleared, cleared.session.mapID, clearedSeq, false) {
		t.Fatal("cleared beach startup did not complete")
	}
	if index := opcodeIndex(recordedOpcodes(t, clearedConn.Bytes()), protocol.OpM2C_StartupTransPoint); index < 0 {
		t.Fatal("cleared beach startup did not restore the transfer point")
	}
}

func TestMainStoryMapStartupRequiresVictoryForTransferPoint(t *testing.T) {
	loadOnlineTablesForTest(t)
	server := &Server{}
	ch, conn := consistencyChannel(7010, 1001101)
	seq := ch.session.markMapChange()
	if !server.finishMapStartup(ch, ch.session.mapID, seq, false) {
		t.Fatal("uncleared main-story startup did not complete")
	}
	for _, opcode := range recordedOpcodes(t, conn.Bytes()) {
		if opcode == protocol.OpM2C_StartupTransPoint {
			t.Fatal("uncleared main-story startup exposed transfer point")
		}
	}

	cleared, clearedConn := consistencyChannel(7011, 1001101)
	cleared.session.killCount = map[int32]int32{mainStoryCompletionKey(1011): 1}
	clearedSeq := cleared.session.markMapChange()
	if !server.finishMapStartup(cleared, cleared.session.mapID, clearedSeq, false) {
		t.Fatal("cleared main-story startup did not complete")
	}
	if index := opcodeIndex(recordedOpcodes(t, clearedConn.Bytes()), protocol.OpM2C_StartupTransPoint); index < 0 {
		t.Fatal("cleared main-story startup did not restore transfer point")
	}
}

func TestMainStoryTransitionRequiresCurrentVictoryAndNextRegion(t *testing.T) {
	loadOnlineTablesForTest(t)
	server := &Server{}
	ss := newSession()
	ss.state, ss.playerID, ss.mapID = sessInGame, 7020, 1001101
	ch := &channel{id: 7020, conn: &recordingConn{}, session: ss}

	response, transition := server.onRequestEnterMap(ch, &protocol.C2M_RequestEnterMap{MapId: 1001102})
	if response.Message == "" || transition != nil {
		t.Fatalf("uncleared main-story advance response=%+v transition=%v", response, transition != nil)
	}
	ss.killCount = map[int32]int32{mainStoryCompletionKey(1011): 1}
	response, transition = server.onRequestEnterMap(ch, &protocol.C2M_RequestEnterMap{MapId: 1001103})
	if response.Message == "" || transition != nil {
		t.Fatalf("skipped main-story advance response=%+v transition=%v", response, transition != nil)
	}
	response, transition = server.onRequestEnterMap(ch, &protocol.C2M_RequestEnterMap{MapId: 1001102})
	if response.Message != "" || transition == nil {
		t.Fatalf("next main-story advance response=%+v transition=%v", response, transition != nil)
	}
}

func TestBeachVictoryUnlocksTransferPointAfterVictoryPacket(t *testing.T) {
	loadOnlineTablesForTest(t)
	server := &Server{}
	ch, conn := consistencyChannel(7003, 1000601)
	ch.session.killCount = make(map[int32]int32)
	seq := ch.session.markMapChange()
	if !ch.session.markMapReady(seq) || !ch.session.markMapStartupComplete(seq) {
		t.Fatal("failed to prepare ready beach scene")
	}
	battle := &battleState{
		mapID: 1000601, region: 1001, owner: ch.session,
		monsters: []*monsterUnit{{id: 8001, monsterID: 10001, alive: false}},
	}
	ch.session.battle = battle

	server.emitVictory(ch, battle)
	opcodes := recordedOpcodes(t, conn.Bytes())
	victoryIndex := opcodeIndex(opcodes, protocol.OpM2C_BattleVictory)
	if victoryIndex < 0 {
		t.Fatalf("beach victory packet missing: %v", opcodes)
	}
	if opcodeIndex(opcodes, protocol.OpM2C_StartupTransPoint) >= 0 {
		t.Fatalf("beach transfer point was not delayed after victory: %v", opcodes)
	}

	time.Sleep(beachPortalDelay + 100*time.Millisecond)
	opcodes = recordedOpcodes(t, conn.Bytes())
	portalIndex := opcodeIndex(opcodes, protocol.OpM2C_StartupTransPoint)
	if portalIndex <= victoryIndex {
		t.Fatalf("beach packet order = %v, want victory before transfer point", opcodes)
	}
}

func TestBeachVictoryWithUnsetBattleMapUsesCurrentSceneForTransferPoint(t *testing.T) {
	loadOnlineTablesForTest(t)
	server := &Server{}
	ch, conn := consistencyChannel(7004, 1000601)
	ch.session.killCount = make(map[int32]int32)
	seq := ch.session.markMapChange()
	if !ch.session.markMapReady(seq) || !ch.session.markMapStartupComplete(seq) {
		t.Fatal("failed to prepare ready beach scene")
	}
	// The native main-story encounter path can leave battleState.mapID unset.
	battle := &battleState{
		mapID: 0, region: 1001, owner: ch.session,
		monsters: []*monsterUnit{{id: 8002, monsterID: 10001, alive: false}},
	}
	ch.session.battle = battle

	server.emitVictory(ch, battle)
	time.Sleep(beachPortalDelay + 100*time.Millisecond)
	if index := opcodeIndex(recordedOpcodes(t, conn.Bytes()), protocol.OpM2C_StartupTransPoint); index < 0 {
		t.Fatal("beach transfer point was not pushed when battle map id was unset")
	}
}

func TestBattleUnitIDsDoNotRepeatBetweenBattles(t *testing.T) {
	ss := newSession()
	first := ss.nextBattleUnitBase()
	second := ss.nextBattleUnitBase()
	if first != 1000000000 {
		t.Fatalf("first battle unit base = %d, want 1000000000", first)
	}
	if second <= first+10 {
		t.Fatalf("second battle unit base = %d overlaps first battle base %d", second, first)
	}
}

func TestMainStoryEnergyCostUsesCopyConfig(t *testing.T) {
	oldTables := tables
	tables = &datatables{
		copyConfig: map[int64]map[string]interface{}{
			10001: testRow(map[string]int64{"NeedEnergy": 7}),
			10003: testRow(map[string]int64{"NeedEnergy": 2}),
			10005: testRow(map[string]int64{"NeedBossEnergy": 1}),
			10006: testRow(map[string]int64{}),
			10014: testRow(map[string]int64{"NeedEnergy": 10}),
		},
		mainStory: map[int64]map[string]interface{}{
			1001: testRow(map[string]int64{"Layer": 1}),
			1010: testRow(map[string]int64{"Layer": 10}),
		},
	}
	t.Cleanup(func() { tables = oldTables })
	if got := mainStoryEnergyCost(1001); got != 7 {
		t.Fatalf("normal energy cost = %d, want 7", got)
	}
	if got := mainStoryEnergyCost(1010); got != 2 {
		t.Fatalf("boss-layer energy cost = %d, want 2", got)
	}
	if got := bossEnergyCost(); got != 1 {
		t.Fatalf("boss-copy energy cost = %d, want 1", got)
	}
	for copyID, want := range map[int64]int32{10001: 7, 10003: 2, 10005: 1, 10006: 0, 10014: 10} {
		if got := battleEnergyCost(copyID); got != want {
			t.Errorf("copy %d energy cost = %d, want %d", copyID, got, want)
		}
	}
}

func testRow(values map[string]int64) map[string]interface{} {
	row := make(map[string]interface{}, len(values))
	for k, v := range values {
		row[k] = json.Number(strconv.FormatInt(v, 10))
	}
	return row
}

func TestParseSkillCastSpecUsesLevelArgs(t *testing.T) {
	base := testRow(map[string]int64{"CD": 5500, "CastType": 1, "CastValue": 0})
	// CastValue is a fractional JSON number in production; use it directly here.
	base["CastValue"] = json.Number("0.02")
	level := testRow(map[string]int64{"Args0": 2, "Args1": 112})
	base["EffectId"] = json.Number("2103")
	spec, err := parseSkillCastSpec(110101, 1, base, level)
	if err != nil {
		t.Fatal(err)
	}
	if spec.castType != 1 || spec.castValue != 0.02 || spec.targetCount != 2 || spec.damageValue != 112 {
		t.Fatalf("unexpected spec: %+v", spec)
	}

	// Range skills use Args1 as the upper target count and Args2 as multiplier.
	rangeLevel := testRow(map[string]int64{"Args0": 2, "Args1": 3, "Args2": 97})
	rangeBase := testRow(map[string]int64{"CD": 5500, "CastType": 1})
	rangeBase["CastValue"] = json.Number("0.02")
	rangeBase["EffectId"] = json.Number("2403")
	rangeSpec, err := parseSkillCastSpec(410101, 1, rangeBase, rangeLevel)
	if err != nil {
		t.Fatal(err)
	}
	if rangeSpec.targetCount != 3 || rangeSpec.damageValue != 97 {
		t.Fatalf("range args not decoded: %+v", rangeSpec)
	}
}

func TestParseSkillCastSpecSkipsFriendlyAndPassiveDamage(t *testing.T) {
	base := testRow(map[string]int64{"CD": 7200, "CastType": 1})
	base["CastValue"] = json.Number("0.05")
	base["EffectId"] = json.Number("9999")
	level := testRow(map[string]int64{"Args0": 999, "Args1": 999})
	if spec, err := parseSkillCastSpec(110301, 1, base, level); err != nil || spec.damageMode != skillDamageNone || !spec.friendly {
		t.Fatalf("friendly skill was treated as damage: spec=%+v err=%v", spec, err)
	}
	passive := testRow(map[string]int64{"CD": 0, "CastType": 0})
	passive["SkillType"] = json.Number("1")
	if _, err := parseSkillCastSpec(120201, 1, passive, level); err == nil {
		t.Fatal("passive skill accepted as active cast")
	}
	custom, err := parseSkillCastSpec(110503, 1, base, level)
	if err != nil || custom.damageMode != skillDamagePlayerCurrentHP || custom.damageValue != 1 {
		t.Fatalf("current HP damage skill not decoded: spec=%+v err=%v", custom, err)
	}
}

func TestParseSkillCastSpecUsesAuditedSemantics(t *testing.T) {
	tests := []struct {
		name          string
		skillID       int32
		level         int32
		args          map[string]int64
		wantTargets   int
		wantDamage    float64
		wantSpiritual bool
	}{
		{
			name:    "superman basic attack uses logic constant instead of stale Args",
			skillID: 400001, level: 1,
			args:        map[string]int64{"Args0": 1, "Args1": 188},
			wantTargets: 1, wantDamage: 193, wantSpiritual: true,
		},
		{
			name:    "fire explosion ignores unrelated larger Args",
			skillID: 410502, level: 1,
			args:        map[string]int64{"Args0": 5, "Args1": 380},
			wantTargets: 10, wantDamage: 100, wantSpiritual: true,
		},
		{
			name:    "dynamic light wave uses logic multiplier",
			skillID: 420101, level: 1,
			args:        map[string]int64{"Args0": 2, "Args1": 3, "Args2": 97},
			wantTargets: 3, wantDamage: 140, wantSpiritual: true,
		},
		{
			name:    "triple cast damages without EffectId",
			skillID: 410603, level: 1,
			args:        map[string]int64{"Args0": 5, "Args1": 10, "Args2": 4, "Args3": 7},
			wantTargets: 5, wantDamage: 50, wantSpiritual: true,
		},
		{
			name:    "rotating ball uses audited three targets",
			skillID: 210501, level: 1,
			args:        map[string]int64{"Args0": 2, "Args1": 98},
			wantTargets: 3, wantDamage: 98,
		},
		{
			name:    "heat ray uses upper target bound",
			skillID: 410601, level: 1,
			args:        map[string]int64{"Args0": 5, "Args1": 115},
			wantTargets: 10, wantDamage: 115, wantSpiritual: true,
		},
		{
			name:    "death burst uses explicit Args4 damage field",
			skillID: 210604, level: 1,
			args: map[string]int64{
				"Args0": -28, "Args1": -18, "Args2": -26, "Args3": 1, "Args4": 270,
			},
			wantTargets: 1, wantDamage: 270,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := testRow(map[string]int64{"CD": 5000, "CastType": 1})
			base["CastValue"] = json.Number("0.02")
			spec, err := parseSkillCastSpec(tt.skillID, tt.level, base, testRow(tt.args))
			if err != nil {
				t.Fatal(err)
			}
			if spec.damageMode != skillDamageAttackMultiplier || spec.targetCount != tt.wantTargets ||
				spec.damageValue != tt.wantDamage || spec.spiritualHit != tt.wantSpiritual {
				t.Fatalf("unexpected semantics: %+v", spec)
			}
		})
	}
}

func TestPlayerTargetAndTrueDamageSemantics(t *testing.T) {
	base := testRow(map[string]int64{"CD": 8100, "CastType": 1})
	base["CastValue"] = json.Number("0.05")
	for _, skillID := range []int32{210404, 210504, 310304, 310503, 410404, 410602} {
		spec, err := parseSkillCastSpec(skillID, 1, base, testRow(map[string]int64{"Args0": 999}))
		if err != nil {
			t.Fatalf("skill %d: %v", skillID, err)
		}
		if !spec.friendly || spec.damageMode != skillDamageNone {
			t.Fatalf("skill %d should target player without immediate enemy damage: %+v", skillID, spec)
		}
	}

	trueDamageBase := testRow(map[string]int64{"CD": 9600, "CastType": 1})
	trueDamageBase["CastValue"] = json.Number("0.08")
	spec, err := parseSkillCastSpec(110504, 1, trueDamageBase, testRow(map[string]int64{"Args0": 102}))
	if err != nil {
		t.Fatal(err)
	}
	if !spec.ignoreDefense {
		t.Fatalf("true damage skill did not bypass defense: %+v", spec)
	}
	b := &battleState{phyAtk: 200}
	target := &monsterUnit{phyDef: 150}
	if got := b.attackMultiplierDamage(spec, target); got != 204 {
		t.Fatalf("true damage was reduced by defense: got %d want 204", got)
	}
}

func TestEffectIdDoesNotImplyDamage(t *testing.T) {
	base := testRow(map[string]int64{"CD": 5000, "CastType": 1, "EffectId": 9999})
	base["CastValue"] = json.Number("0.02")
	spec, err := parseSkillCastSpec(999999, 1, base, testRow(map[string]int64{"Args0": 9999}))
	if err != nil {
		t.Fatal(err)
	}
	if spec.damageMode != skillDamageNone || spec.damageValue != 0 {
		t.Fatalf("unknown EffectId was guessed as damage: %+v", spec)
	}
}

func TestStateSkillAppliesThroughCombatRuntime(t *testing.T) {
	oldTables := tables
	t.Cleanup(func() { tables = oldTables })

	const skillID int32 = 110301
	base := testRow(map[string]int64{
		"CD": 7200, "CastType": 1, "EffectId": 9999, "MaxLevel": 1,
	})
	base["CastValue"] = json.Number("0.05")
	tables = &datatables{skillConfig: map[int64]map[string]interface{}{
		int64(skillID) * 100:   base,
		int64(skillID)*100 + 1: testRow(map[string]int64{"Args0": 999}),
	}}

	monster := &monsterUnit{id: 1001, hp: 100, maxHP: 100, alive: true}
	battle := &battleState{
		monsters:    []*monsterUnit{monster},
		playerHP:    80,
		playerMaxHP: 100,
		playerMP:    50,
		playerMaxMP: 100,
	}
	ss := newSession()
	ss.playerID = 7
	ss.level = 12
	ss.skills = map[int32]int32{skillID: 1}
	ss.skillOrder = []int32{skillID}
	ss.mainUISlots[0] = mainUISlot{Type: 1, Id: skillID}
	ss.battle = battle
	conn := &recordingConn{}
	ch := &channel{conn: conn, session: ss}

	server, phases := newQueuedCombatServer()
	resp := server.onUseMainUISkill(ch, &protocol.C2M_UseMainUISkill{SlotId: 0})
	result, ok := resp.(*protocol.M2C_UseMainUISkill)
	if !ok || result.Error != 0 || result.Message != "" {
		t.Fatalf("state skill failed: %#v", resp)
	}
	if battle.playerHP != 80 || battle.playerMP != 47 || monster.hp != 100 || !monster.alive {
		t.Fatalf("state skill settlement mismatch: battle=%+v monster=%+v", battle, monster)
	}
	if battle.runtime == nil || battle.runtime.EffectiveAttribute(battle.runtime.Player(), CombatAttributeMaxHP) <= battle.playerMaxHP {
		t.Fatalf("max HP buff was not applied: runtime=%+v", battle.runtime)
	}
	if _, started := ss.skillCooldowns[skillID]; !started {
		t.Fatalf("successful state skill did not start cooldown: %+v", ss.skillCooldowns)
	}
	seenMP := false
	for _, opcode := range recordedOpcodes(t, conn.Bytes()) {
		switch opcode {
		case protocol.OpM2C_SyncUnitAttribute:
			seenMP = true
		case protocol.OpM2C_BattleChangeMP, protocol.OpM2C_ReMainStoryMonsterInfo:
			t.Fatalf("normal skill cast emitted client-incompatible opcode %d", opcode)
		}
	}
	if !seenMP {
		t.Fatal("normal skill cast did not emit numeric MP synchronization")
	}
	server.combatTickChannelAt(ch, time.Now())
	phases.runNext(t, time.Second)
	phases.runNext(t, time.Second)
	if battle.playerHP != 79 {
		t.Fatalf("delayed monster impact HP = %d, want 79", battle.playerHP)
	}
}

func TestBattleSkillTargetsAndDamage(t *testing.T) {
	b := &battleState{
		monsters: []*monsterUnit{
			{id: 1, hp: 100, maxHP: 100, phyDef: 10, alive: true},
			{id: 2, hp: 100, maxHP: 100, phyDef: 10, alive: true},
			{id: 3, hp: 100, maxHP: 100, phyDef: 10, alive: true},
		},
		selectedID: 2,
		phyAtk:     110,
		spiAtk:     210,
	}
	targets := b.selectSkillTargets(2)
	if len(targets) != 2 || targets[0].id != 2 || targets[1].id != 1 {
		t.Fatalf("selected target ordering wrong: %+v", targets)
	}
	if got := b.playerDamage(142, 10, false); got != 142 {
		t.Fatalf("physical multiplier not applied: got %d", got)
	}
	b.monsters[0].spiDef = 10
	if got := b.playerDamage(150, 10, true); got != 300 {
		t.Fatalf("spiritual attack not applied: got %d", got)
	}
}

func TestSkillCooldownRejectsReplay(t *testing.T) {
	s := newSession()
	now := time.Unix(100, 0)
	if rem, ok := s.tryStartSkillCooldown(100001, now, 5*time.Second); !ok || rem != 0 {
		t.Fatalf("first cast not accepted: rem=%s ok=%v", rem, ok)
	}
	if rem, ok := s.tryStartSkillCooldown(100001, now.Add(time.Second), 5*time.Second); ok || rem <= 0 {
		t.Fatalf("replay not rejected: rem=%s ok=%v", rem, ok)
	}
	if _, ok := s.tryStartSkillCooldown(100001, now.Add(5*time.Second), 5*time.Second); !ok {
		t.Fatal("cast not accepted after cooldown")
	}
}

func TestAllPlayerSkillConfigsAreSafeToParse(t *testing.T) {
	skillTable, err := loadKVTable(filepath.Join("..", "datatable_json", "SkillConfig.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, ids := range jobSkillMap {
		for _, skillID := range ids {
			base := skillTable[int64(skillID)*100]
			if base == nil {
				t.Fatalf("skill %d base row missing", skillID)
			}
			maxLevel := int32(num(base["MaxLevel"]))
			if int32(num(base["SkillType"])) == 1 {
				if _, err := parseSkillCastSpec(skillID, 1, base, skillTable[int64(skillID)*100+1]); err == nil {
					t.Fatalf("passive skill %d accepted", skillID)
				}
				continue
			}
			for level := int32(1); level <= maxLevel; level++ {
				row := skillTable[int64(skillID)*100+int64(level)]
				if _, err := parseSkillCastSpec(skillID, level, base, row); err != nil {
					t.Fatalf("skill %d level %d: %v", skillID, level, err)
				}
			}
		}
	}
}
