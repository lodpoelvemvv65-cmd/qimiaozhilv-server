package main

import (
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func TestClientCombatCooldownUsesConfiguredDuration(t *testing.T) {
	tests := []struct {
		name   string
		baseMS int32
		wantMS int32
	}{
		{name: "configured duration", baseMS: 5000, wantMS: 5000},
		{name: "zero duration", baseMS: 0, wantMS: 0},
		{name: "negative duration", baseMS: -1, wantMS: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := clientCombatCooldownMS(test.baseMS); got != test.wantMS {
				t.Fatalf("client cooldown = %dms, want %dms", got, test.wantMS)
			}
		})
	}
}

func TestClientCombatIntervalUsesConfiguredDuration(t *testing.T) {
	if got := clientCombatInterval(5 * time.Second); got != 5*time.Second {
		t.Fatalf("combat interval = %s, want 5s", got)
	}
}

func TestPublicActionIntervalUsesSpeedAndOnlineBounds(t *testing.T) {
	tests := []struct {
		name  string
		speed float64
		want  time.Duration
	}{
		{name: "no speed", speed: 0, want: 6000 * time.Millisecond},
		{name: "historical 4746ms sample", speed: 1254, want: 4746 * time.Millisecond},
		{name: "family boss 4554ms sample", speed: 1446, want: 4554 * time.Millisecond},
		{name: "captured lower bound", speed: 2000, want: 4000 * time.Millisecond},
		{name: "speed above lower bound", speed: 9000, want: 4000 * time.Millisecond},
		{name: "invalid negative speed", speed: -1, want: 6000 * time.Millisecond},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := publicActionIntervalFromSpeed(test.speed); got != test.want {
				t.Fatalf("public action interval at speed %.3f = %s, want %s", test.speed, got, test.want)
			}
		})
	}
}

func TestMonsterPublicActionIntervalMatchesOnlineCaptures(t *testing.T) {
	oldTables := tables
	tables = &datatables{monsterBase: map[int64]map[string]interface{}{
		10001: {"Level": float64(10)},
		10161: {"Level": float64(7600)},
		50005: {"Level": float64(31500)},
	}}
	t.Cleanup(func() { tables = oldTables })

	tests := []struct {
		name      string
		monsterID int32
		mapID     int32
		want      time.Duration
	}{
		{name: "opening monster 10001", monsterID: 10001, want: 6000 * time.Millisecond},
		{name: "main-story monster 10161", monsterID: 10161, want: 5658 * time.Millisecond},
		{name: "tier-five family boss", monsterID: 50005, mapID: -5, want: 4710 * time.Millisecond},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			battle := &battleState{mapID: test.mapID, monsters: []*monsterUnit{{monsterID: test.monsterID}}}
			if got := monsterPublicActionInterval(battle); got != test.want {
				t.Fatalf("monster action interval = %s, want %s", got, test.want)
			}
		})
	}
}

func TestMonsterActionWindowKeepsGridAfterSkippedControlledTicks(t *testing.T) {
	oldTables := tables
	tables = &datatables{monsterBase: map[int64]map[string]interface{}{
		50005: {"Level": float64(31500)},
	}}
	t.Cleanup(func() { tables = oldTables })

	start := time.Unix(1_800_000_000, 0)
	battle := &battleState{
		mapID: -5, monsterReadyAt: start,
		monsters: []*monsterUnit{{monsterID: 50005}},
	}
	interval := monsterPublicActionInterval(battle)
	if !battle.claimMonsterActionWindow(start) {
		t.Fatal("opening monster action window was not claimed")
	}
	// No claim at start+interval models a control effect suppressing that turn.
	if !battle.claimMonsterActionWindow(start.Add(2 * interval)) {
		t.Fatal("monster did not resume on the original grid after a skipped turn")
	}
	if want := start.Add(3 * interval); !battle.monsterReadyAt.Equal(want) {
		t.Fatalf("monster grid drifted after control: ready=%s want=%s", battle.monsterReadyAt, want)
	}
}

func TestAutomaticBattleWaitsForCapturedFiveSecondOpening(t *testing.T) {
	installPublicActionWindowTestConfig(t)
	monster := &monsterUnit{id: 200, hp: 1_000_000_000, maxHP: 1_000_000_000, alive: true}
	ss := newSession()
	ss.playerID, ss.jobID = 7, 1
	ss.skills = map[int32]int32{100001: 1}
	battle := &battleState{
		owner: ss, monsters: []*monsterUnit{monster}, phyAtk: 100,
		playerHP: 1_000_000, playerMaxHP: 1_000_000,
		playerMP: 1_000_000, playerMaxMP: 1_000_000,
	}
	battle.runtime = NewCombatRuntime(battle, ss.playerID, nil)
	ss.battle = battle
	startedAt := time.Unix(1_800_000_000, 0)
	readyAt := initializeBattleOpeningCadence(ss, battle, startedAt)
	if want := startedAt.Add(5 * time.Second); !readyAt.Equal(want) ||
		!ss.battleActionReadyAt.Equal(want) || !ss.autoBattleNextCastAt.Equal(want) ||
		!battle.monsterReadyAt.Equal(want) {
		t.Fatalf("opening deadlines ready=%s player=%s auto=%s monster=%s want=%s",
			readyAt, ss.battleActionReadyAt, ss.autoBattleNextCastAt, battle.monsterReadyAt, want)
	}

	conn := &recordingConn{}
	ch := &channel{id: 1, conn: conn, session: ss}
	server, phases := newQueuedCombatServer()
	epoch := ss.setAutoBattle(true)
	server.monstersAttackAt(ch, battle, startedAt.Add(4999*time.Millisecond))
	if !server.autoBattleStepAt(ch, epoch, startedAt.Add(4999*time.Millisecond)) {
		t.Fatal("automatic battle stopped while waiting for the opening deadline")
	}
	if monster.hp != monster.maxHP || len(conn.Bytes()) != 0 || phases.len() != 0 {
		t.Fatalf("battle acted before opening deadline: monsterHP=%d/%d opcodes=%v phases=%d",
			monster.hp, monster.maxHP, recordedOpcodes(t, conn.Bytes()), phases.len())
	}

	if !server.autoBattleStepAt(ch, epoch, readyAt) {
		t.Fatal("automatic battle stopped at the opening deadline")
	}
	if monster.hp != monster.maxHP || phases.len() != 1 {
		t.Fatalf("first action did not run at opening deadline: monsterHP=%d/%d phases=%d",
			monster.hp, monster.maxHP, phases.len())
	}
	if want := readyAt.Add(playerPublicActionInterval(ss)); !ss.autoBattleNextCastAt.Equal(want) {
		t.Fatalf("second automatic action = %s, want %s", ss.autoBattleNextCastAt, want)
	}
	drainCombatPhases(t, phases)
	if monster.hp >= monster.maxHP {
		t.Fatal("automatic opening did not damage monster at impact")
	}
}

func TestClientPublicActionIntervalPresentationUsesSpeed(t *testing.T) {
	ss := newSession()
	ss.jobID = 1
	if got := clientPublicActionIntervalMS(ss); got != 6000 {
		t.Fatalf("baseline public action interval = %d, want 6000", got)
	}
	ss.transBonus[1031] = 1446
	if got := clientPublicActionIntervalMS(ss); got != 4554 {
		t.Fatalf("speed-adjusted public action interval = %d, want 4554", got)
	}
}

func TestClientCombatCadenceAndCooldownPacketsUseConfiguredDuration(t *testing.T) {
	oldTables := tables
	oldCatalog := skillLogicCatalog
	t.Cleanup(func() {
		tables = oldTables
		skillLogicCatalog = oldCatalog
	})
	skillLogicCatalog = loadTestSkillLogic(t)

	const basic int32 = 100001
	tables = &datatables{skillConfig: map[int64]map[string]interface{}{
		int64(basic) * 100:   testRow(map[string]int64{"CD": 5000, "CastType": 0}),
		int64(basic)*100 + 1: testRow(map[string]int64{"Args1": 100}),
	}}
	monster := &monsterUnit{id: 200, hp: 1_000_000, maxHP: 1_000_000, alive: true}
	ss := newSession()
	ss.playerID, ss.jobID = 7, 1
	ss.skills = map[int32]int32{basic: 1}
	ss.battle = &battleState{
		owner: ss, monsters: []*monsterUnit{monster}, phyAtk: 100,
		playerHP: 10_000, playerMaxHP: 10_000, playerMP: 100, playerMaxMP: 100,
	}
	conn := &recordingConn{}
	ch := &channel{id: 1, conn: conn, session: ss}
	epoch := ss.setAutoBattle(true)
	now := time.Now()
	server, phases := newQueuedCombatServer()

	if !server.autoBattleStepAt(ch, epoch, now) {
		t.Fatal("automatic battle stopped after a valid client-baseline cast")
	}
	if want := now.Add(6000 * time.Millisecond); !ss.autoBattleNextCastAt.Equal(want) {
		t.Fatalf("next automatic cast = %s, want %s", ss.autoBattleNextCastAt, want)
	}
	readyAt, ok := ss.skillCooldowns[basic]
	if !ok {
		t.Fatal("client-baseline skill cooldown was not recorded")
	}
	remaining := time.Until(readyAt)
	if remaining <= 4*time.Second || remaining > 5*time.Second {
		t.Fatalf("authoritative cooldown remaining = %s, want approximately 5s", remaining)
	}

	seenPlay, seenStart := false, false
	for _, frame := range decodeRecordedFrames(t, conn.Bytes()) {
		switch frame.opcode {
		case protocol.OpM2C_PlaySkill:
			message := &protocol.M2C_PlaySkill{}
			if err := proto.Unmarshal(frame.body, message); err != nil {
				t.Fatal(err)
			}
			if message.SkillId == basic {
				seenPlay = true
				if message.CoolTime != 0 || message.TargetId != 0 || message.MpCost != 0 {
					t.Fatalf("PlaySkill compatibility fields must be omitted: %+v", message)
				}
				assertPlaySkillWireFields(t, frame.body)
			}
		case protocol.OpM2C_StartCD:
			message := &protocol.M2C_StartCD{}
			if err := proto.Unmarshal(frame.body, message); err != nil {
				t.Fatal(err)
			}
			if message.Id == basic {
				seenStart = true
				if message.Time != 6000 {
					t.Fatalf("StartCD action time = %dms, want 6000ms", message.Time)
				}
				if message.SkillCD != 5000 {
					t.Fatalf("StartCD cooldown = %dms, want 5000ms", message.SkillCD)
				}
			}
		}
	}
	if !seenPlay || !seenStart {
		t.Fatalf("speed-adjusted cooldown packets: PlaySkill=%t StartCD=%t", seenPlay, seenStart)
	}
	drainCombatPhases(t, phases)
}

func assertPlaySkillWireFields(t *testing.T, raw []byte) {
	t.Helper()
	var fields []protowire.Number
	for len(raw) > 0 {
		number, wireType, tagLen := protowire.ConsumeTag(raw)
		if tagLen < 0 {
			t.Fatal(protowire.ParseError(tagLen))
		}
		raw = raw[tagLen:]
		valueLen := protowire.ConsumeFieldValue(number, wireType, raw)
		if valueLen < 0 {
			t.Fatal(protowire.ParseError(valueLen))
		}
		fields = append(fields, number)
		raw = raw[valueLen:]
	}
	if len(fields) != 2 || fields[0] != 1 || fields[1] != 2 {
		t.Fatalf("PlaySkill wire fields = %v, want exactly [1 2]", fields)
	}
}

func TestAutomaticBattleUsesPlayerSpeedWithoutWaitingForMonsterWave(t *testing.T) {
	installPublicActionWindowTestConfig(t)
	tables.skillConfig[10000100]["CD"] = int64(4000)
	monster := &monsterUnit{id: 200, hp: 1_000_000_000, maxHP: 1_000_000_000, alive: true}
	ss := newSession()
	ss.playerID, ss.jobID = 7, 1
	ss.transBonus[1031] = 1446
	ss.skills = map[int32]int32{100001: 1}
	ss.battle = &battleState{
		owner: ss, monsters: []*monsterUnit{monster}, phyAtk: 100,
		playerHP: 1_000_000, playerMaxHP: 1_000_000,
		playerMP: 1_000_000, playerMaxMP: 1_000_000,
	}
	ss.battle.runtime = NewCombatRuntime(ss.battle, ss.playerID, nil)
	now := time.Unix(1_800_000_000, 0)
	ss.battle.monsterReadyAt = now.Add(time.Hour)
	ss.battleActionReadyAt = now
	server, phases := newQueuedCombatServer()
	epoch := ss.setAutoBattleAt(true, now)
	ch := &channel{id: 1, conn: &recordingConn{}, session: ss}

	if !server.autoBattleStepAt(ch, epoch, now) || phases.len() != 1 {
		t.Fatalf("opening automatic cast phases=%d", phases.len())
	}
	drainCombatPhases(t, phases)
	hpAfterFirst := monster.hp
	delete(ss.skillCooldowns, 100001)
	if want := now.Add(4554 * time.Millisecond); !ss.battleActionReadyAt.Equal(want) || !ss.autoBattleNextCastAt.Equal(want) {
		t.Fatalf("speed deadline action=%s auto=%s want=%s", ss.battleActionReadyAt, ss.autoBattleNextCastAt, want)
	}
	if !server.autoBattleStepAt(ch, epoch, now.Add(4553*time.Millisecond)) || phases.len() != 0 || monster.hp != hpAfterFirst {
		t.Fatal("automatic battle acted before the 4554ms player interval")
	}
	if running := server.autoBattleStepAt(ch, epoch, now.Add(4554*time.Millisecond)); !running || phases.len() != 1 {
		t.Fatalf("automatic battle waited for the independent monster wave: running=%t phases=%d action=%s auto=%s cooldown=%s",
			running, phases.len(), ss.battleActionReadyAt, ss.autoBattleNextCastAt, ss.skillCooldowns[100001])
	}
}

func installPublicActionWindowTestConfig(t *testing.T) {
	t.Helper()
	oldTables := tables
	oldCatalog := skillLogicCatalog
	t.Cleanup(func() {
		tables = oldTables
		skillLogicCatalog = oldCatalog
	})
	skillLogicCatalog = loadTestSkillLogic(t)
	tables = &datatables{skillConfig: map[int64]map[string]interface{}{
		10000100: testRow(map[string]int64{"CD": 5000, "CastType": 0}),
		10000101: testRow(map[string]int64{"Args1": 100}),
		11010100: testRow(map[string]int64{"CD": 12000, "CastType": 0}),
		11020100: testRow(map[string]int64{"CD": 12000, "CastType": 0}),
	}}
}

func TestManualAndAutomaticSkillsShareOnePlayerActionWindow(t *testing.T) {
	installPublicActionWindowTestConfig(t)
	monster := &monsterUnit{id: 200, hp: 1_000_000_000, maxHP: 1_000_000_000, alive: true}
	ss := newSession()
	ss.playerID, ss.jobID = 7, 1
	ss.skills = map[int32]int32{100001: 1, 110101: 1, 110201: 1}
	ss.autoSkills = []int32{110101}
	ss.battle = &battleState{
		owner: ss, monsters: []*monsterUnit{monster}, phyAtk: 100,
		playerHP: 1_000_000, playerMaxHP: 1_000_000,
		playerMP: 1_000_000, playerMaxMP: 1_000_000,
	}
	conn := &recordingConn{}
	ch := &channel{id: 1, conn: conn, session: ss}
	epoch := ss.setAutoBattle(true)
	now := time.Unix(1_800_000_000, 0)
	server, phases := newQueuedCombatServer()

	if !server.autoBattleStepAt(ch, epoch, now) {
		t.Fatal("automatic battle stopped after its first action")
	}
	if want := now.Add(playerPublicActionInterval(ss)); !ss.battleActionReadyAt.Equal(want) {
		t.Fatalf("player action ready at %s, want %s", ss.battleActionReadyAt, want)
	}
	drainCombatPhases(t, phases)
	ss.setAutoBattle(false)
	hpAfterAuto := monster.hp
	if _, cast := server.castSkillLockedWithLogAt(ch, 110201, &protocol.M2C_UseMainUISkill{}, false, now.Add(5999*time.Millisecond)); cast {
		t.Fatal("manual shortcut bypassed the automatic action window")
	}
	if monster.hp != hpAfterAuto {
		t.Fatalf("rejected manual shortcut changed monster HP to %d, want %d", monster.hp, hpAfterAuto)
	}
	if _, started := ss.skillCooldowns[110201]; started {
		t.Fatal("rejected manual shortcut started its skill cooldown")
	}
	if _, cast := server.castSkillLockedWithLogAt(ch, 110201, &protocol.M2C_UseMainUISkill{}, false, now.Add(6*time.Second)); !cast {
		t.Fatal("manual shortcut was not accepted when the player's action window expired")
	}
	drainCombatPhases(t, phases)
	hpAfterManual := monster.hp
	ss.setAutoBattle(false)
	epoch = ss.setAutoBattle(true)
	if !server.autoBattleStepAt(ch, epoch, now.Add(11999*time.Millisecond)) {
		t.Fatal("automatic battle stopped while waiting for the manual action window")
	}
	if monster.hp != hpAfterManual {
		t.Fatal("toggling automatic battle bypassed the manual action window")
	}
	if !server.autoBattleStepAt(ch, epoch, now.Add(12*time.Second)) {
		t.Fatal("automatic battle did not resume when the manual action window expired")
	}
	drainCombatPhases(t, phases)
	if monster.hp >= hpAfterManual {
		t.Fatal("automatic hit did not resolve")
	}

	seenPublicCD := false
	for _, frame := range decodeRecordedFrames(t, conn.Bytes()) {
		if frame.opcode != protocol.OpM2C_StartCD {
			continue
		}
		message := &protocol.M2C_StartCD{}
		if err := proto.Unmarshal(frame.body, message); err != nil {
			t.Fatal(err)
		}
		if message.Id == 110201 && message.Time == 6000 && message.SkillCD > 0 {
			seenPublicCD = true
		}
	}
	if !seenPublicCD {
		t.Fatal("active shortcut did not receive the six-second public cooldown")
	}
}

func TestPartyManualMembersHaveIndependentActionWindows(t *testing.T) {
	installPublicActionWindowTestConfig(t)
	server, _, first, second := newSharedPartyBattleForTest(t)
	phases := installQueuedCombatScheduler(server)
	for _, member := range []*channel{first, second} {
		member.session.jobID = 1
		member.session.skills = map[int32]int32{110101: 1}
		member.session.battle.phyAtk = 100
	}
	now := time.Unix(1_800_000_000, 0)
	if _, cast := server.castSkillLockedWithLogAt(first, 110101, &protocol.M2C_UseMainUISkill{}, false, now); !cast {
		t.Fatal("first party member could not act")
	}
	if _, cast := server.castSkillLockedWithLogAt(second, 110101, &protocol.M2C_UseMainUISkill{}, false, now); !cast {
		t.Fatal("first party member prevented second member from acting")
	}
	want := now.Add(maxPublicActionInterval)
	if !first.session.battleActionReadyAt.Equal(want) || !second.session.battleActionReadyAt.Equal(want) {
		t.Fatalf("party action windows = %s/%s, want shared deadline at %s",
			first.session.battleActionReadyAt, second.session.battleActionReadyAt, want)
	}
	drainCombatPhases(t, phases)
}
