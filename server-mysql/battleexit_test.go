package main

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func TestQuitFamilyBossClearsClientFightStateAndReturnsAtLeftPortal(t *testing.T) {
	oldTables := tables
	tables = &datatables{}
	t.Cleanup(func() { tables = oldTables })

	ss := newSession()
	ss.playerID = 7
	ss.mapID = 10004
	ss.resetMovement(-6.10, -2.50)
	ss.battle = &battleState{
		mapID:       -1,
		battleType:  1,
		playerHP:    80,
		playerMaxHP: 100,
		playerMP:    40,
		playerMaxMP: 100,
	}
	conn := &recordingConn{}
	ch := &channel{id: 1, conn: conn, session: ss}

	(&Server{}).onQuitBattle(ch, &protocol.C2M_QuitBattle{})

	if ss.battle != nil {
		t.Fatal("family boss battle was not cleared")
	}
	wantX, wantY := mainCityReturnSpawn()
	if ss.mapID != 10004 || ss.x != wantX || ss.y != wantY {
		t.Fatalf("quit destination = map %d (%.5f, %.5f), want main city left portal (%.5f, %.5f)",
			ss.mapID, ss.x, ss.y, wantX, wantY)
	}

	data := conn.Bytes()
	seenBattleEnd := false
	seenChangeMap := false
	for len(data) > 0 {
		if len(data) < 4 {
			t.Fatalf("truncated frame: %x", data)
		}
		length := int(binary.LittleEndian.Uint16(data[:2]))
		if length < 2 || len(data) < length+2 {
			t.Fatalf("invalid frame length %d in %x", length, data)
		}
		opcode := binary.LittleEndian.Uint16(data[2:4])
		body := data[4 : length+2]
		switch opcode {
		case protocol.OpM2C_BattleDefeat:
			if seenChangeMap {
				t.Fatal("BattleDefeat must precede ChangeMap so BattleEnd clears MyUnit.IsFight")
			}
			seenBattleEnd = true
		case protocol.OpM2C_ChangeMap:
			if !seenBattleEnd {
				t.Fatal("ChangeMap was sent before the client battle-end event")
			}
			var change protocol.M2C_ChangeMap
			if err := proto.Unmarshal(body, &change); err != nil {
				t.Fatal(err)
			}
			if change.MapId != wireMapID(10004) || change.X != wantX || change.Y != wantY {
				t.Fatalf("ChangeMap = map %d at (%.5f, %.5f), want map %d at (%.5f, %.5f)",
					change.MapId, change.X, change.Y, wireMapID(10004), wantX, wantY)
			}
			seenChangeMap = true
		}
		data = data[length+2:]
	}
	if !seenBattleEnd || !seenChangeMap {
		t.Fatalf("missing exit frames: BattleDefeat=%v ChangeMap=%v", seenBattleEnd, seenChangeMap)
	}
}

func TestQuitOrdinaryBattleReturnsToCityWithOneChangeMap(t *testing.T) {
	oldTables := tables
	tables = &datatables{}
	t.Cleanup(func() { tables = oldTables })

	ss := newSession()
	ss.playerID = 8
	ss.mapID = 1001101
	ss.resetMovement(2.5, -1.25)
	ss.battle = &battleState{
		mapID: 1001101, playerHP: 80, playerMaxHP: 100,
		playerMP: 40, playerMaxMP: 100,
	}
	conn := &recordingConn{}
	ch := &channel{id: 8, conn: conn, session: ss}

	(&Server{}).onQuitBattle(ch, &protocol.C2M_QuitBattle{})

	changes := 0
	seenDefeat := false
	data := conn.Bytes()
	for len(data) > 0 {
		if len(data) < 4 {
			t.Fatalf("truncated frame: %x", data)
		}
		length := int(binary.LittleEndian.Uint16(data[:2]))
		if length < 2 || len(data) < length+2 {
			t.Fatalf("invalid frame length %d in %x", length, data)
		}
		opcode := binary.LittleEndian.Uint16(data[2:4])
		body := data[4 : length+2]
		switch opcode {
		case protocol.OpM2C_BattleDefeat:
			seenDefeat = true
		case protocol.OpM2C_ChangeMap:
			changes++
			var change protocol.M2C_ChangeMap
			if err := proto.Unmarshal(body, &change); err != nil {
				t.Fatal(err)
			}
			wantX, wantY := mainCityReturnSpawn()
			if change.MapId != wireMapID(10004) || change.X != wantX || change.Y != wantY {
				t.Fatalf("ChangeMap = %+v, want city map=%d at (%.5f,%.5f)",
					&change, wireMapID(10004), wantX, wantY)
			}
		}
		data = data[length+2:]
	}
	if !seenDefeat {
		t.Fatal("ordinary quit did not send BattleDefeat")
	}
	if changes != 1 {
		t.Fatalf("ordinary quit ChangeMap count=%d, want 1", changes)
	}

	// 线上不在战后补发同场景切图，退出战斗只保留这一次正常 ChangeMap。
	time.Sleep(200 * time.Millisecond)
	if got := recordedOpcodeCount(t, conn, protocol.OpM2C_ChangeMap); got != 1 {
		t.Fatalf("ordinary quit emitted a delayed second ChangeMap: %d", got)
	}
}

func TestQuitBattleDuplicateAfterStateClearDoesNotReload(t *testing.T) {
	oldTables := tables
	tables = &datatables{}
	t.Cleanup(func() { tables = oldTables })

	ss := newSession()
	ss.playerID = 9
	ss.mapID = 1001101
	ss.resetMovement(2.5, -1.25)
	ss.battle = &battleState{mapID: 1001101, playerHP: 80, playerMaxHP: 100}
	conn := &recordingConn{}
	ch := &channel{id: 9, conn: conn, session: ss}

	server := &Server{}
	server.onQuitBattle(ch, &protocol.C2M_QuitBattle{})
	server.onQuitBattle(ch, &protocol.C2M_QuitBattle{})

	if got := recordedOpcodeCount(t, conn, protocol.OpM2C_ChangeMap); got != 1 {
		t.Fatalf("duplicate quit emitted ChangeMap count=%d, want 1", got)
	}
}

func TestQuitBattleLeaderReturnsWholePartyToCity(t *testing.T) {
	server, leader, member := returnTeamTestPlayers(t)
	installSharedReturnBattle(leader, member)

	server.onQuitBattle(leader, &protocol.C2M_QuitBattle{RpcId: 71})

	wantX, wantY := mainCityReturnSpawn()
	for _, ch := range []*channel{leader, member} {
		ss := ch.session
		if ss.battle != nil || ss.autoBattle || ss.idleBattle {
			t.Fatalf("player %d kept fight state after leader quit: battle=%p auto=%v idle=%v",
				ss.playerID, ss.battle, ss.autoBattle, ss.idleBattle)
		}
		if ss.mapID != 10004 || ss.x != wantX || ss.y != wantY {
			t.Fatalf("player %d quit destination = map %d (%.5f, %.5f), want city (%.5f, %.5f)",
				ss.playerID, ss.mapID, ss.x, ss.y, wantX, wantY)
		}
		conn := ch.conn.(*recordingConn)
		if !hasRecordedOpcode(t, conn, protocol.OpM2C_BattleDefeat) {
			t.Fatalf("player %d received no BattleDefeat", ss.playerID)
		}
		if got := recordedOpcodeCount(t, conn, protocol.OpM2C_ChangeMap); got != 1 {
			t.Fatalf("player %d ChangeMap count=%d, want 1", ss.playerID, got)
		}
	}

	// 队伍共享战斗的异步结算不能在队员回城后再补一次切图。
	time.Sleep(200 * time.Millisecond)
	for _, ch := range []*channel{leader, member} {
		if got := recordedOpcodeCount(t, ch.conn.(*recordingConn), protocol.OpM2C_ChangeMap); got != 1 {
			t.Fatalf("player %d emitted a delayed second ChangeMap: %d", ch.session.playerID, got)
		}
	}
}

func TestQuitBattleTeamMemberIsRejectedWithTip(t *testing.T) {
	server, leader, member := returnTeamTestPlayers(t)
	installSharedReturnBattle(leader, member)

	server.onQuitBattle(member, &protocol.C2M_QuitBattle{RpcId: 72})

	memberConn := member.conn.(*recordingConn)
	if !hasRecordedOpcode(t, memberConn, protocol.OpM2C_SendTip) {
		t.Fatalf("member quit battle was silent: %v", recordedOpcodes(t, memberConn.Bytes()))
	}
	if member.session.battle == nil || member.session.mapID != 1000601 {
		t.Fatalf("member quit changed its own state: battle=%p map=%d",
			member.session.battle, member.session.mapID)
	}
	if hasRecordedOpcode(t, memberConn, protocol.OpM2C_ChangeMap) {
		t.Fatal("rejected member quit battle emitted ChangeMap")
	}
	// 队长的战斗与场景不受队员点击影响，两人仍共享同一个战斗。
	if leader.session.battle == nil || leader.session.mapID != 1000601 {
		t.Fatalf("leader was dragged out by a member: battle=%p map=%d",
			leader.session.battle, leader.session.mapID)
	}
	if member.session.battle.party != leader.session.battle.party {
		t.Fatal("member detach split the shared party battle")
	}

	tip := ""
	for _, frame := range decodeRecordedFrames(t, memberConn.Bytes()) {
		if frame.opcode != protocol.OpM2C_SendTip {
			continue
		}
		var message protocol.M2C_SendTip
		if err := proto.Unmarshal(frame.body, &message); err != nil {
			t.Fatal(err)
		}
		tip = message.Message
	}
	if !strings.Contains(tip, "队长") {
		t.Fatalf("member quit battle tip = %q, want it to mention the party leader", tip)
	}
}

func TestEndMainStoryAIAbortsPartyFightStayOnMap(t *testing.T) {
	server, party, first, second := newSharedPartyBattleForTest(t)
	const mapID int32 = 1001303
	for _, member := range []*channel{first, second} {
		member.session.mapID = mapID
		member.session.resetMovement(3.5, -1.25)
		member.session.mainStoryAIRunning = true
		member.session.autoBattle = true
		member.session.idleBattle = false
	}
	cancelReturnTestMapStartup(t, first, second)
	installVisibleCombatBuff(t, first.session.battle, first.session.playerID, "party-burn")

	resp := server.onEndMainStoryAI(first, &protocol.C2M_EndMainStoryAI{RpcId: 9})
	if resp.(*protocol.M2C_EndMainStoryAI).RpcId != 9 {
		t.Fatalf("end AI response = %+v", resp)
	}
	if first.session.battle != nil || second.session.battle != nil {
		t.Fatalf("party fights were not cleared: first=%p second=%p", first.session.battle, second.session.battle)
	}
	if !party.aborted || !party.settled {
		t.Fatalf("party abort state aborted=%v settled=%v", party.aborted, party.settled)
	}
	for _, member := range []*channel{first, second} {
		if member.session.mainStoryAIRunning || member.session.autoBattle || member.session.idleBattle {
			t.Fatalf("member %d runner flags running=%v auto=%v idle=%v",
				member.session.playerID, member.session.mainStoryAIRunning, member.session.autoBattle, member.session.idleBattle)
		}
		if member.session.mapID != mapID {
			t.Fatalf("member %d map=%d, want stay on %d", member.session.playerID, member.session.mapID, mapID)
		}
		assertBattleEndBeforeSameMap(t, member.conn.(*recordingConn), protocol.OpM2C_BattleDefeat, mapID, 3.5, -1.25)
		// 线上战斗结束不发 Time=0 的 20080，HUD 图标由客户端按自身 Time 销毁。
		if hasRecordedReduceState(t, member.conn.(*recordingConn), first.session.playerID) {
			t.Fatalf("member %d received a forced zero-time HUD Reduce", member.session.playerID)
		}
	}
}

func TestEndMainStoryAIKeepsPendingPartyVictoryEnrolled(t *testing.T) {
	oldTables := tables
	tables = &datatables{}
	t.Cleanup(func() { tables = oldTables })

	server, party, first, second := newSharedPartyBattleForTest(t)
	const mapID int32 = 1001303
	monster := first.session.battle.monsters[0]
	monster.hp, monster.alive = 0, false
	for _, member := range []*channel{first, second} {
		member.session.mapID = mapID
		member.session.resetMovement(2, 4)
		member.session.mainStoryAIRunning = true
		member.session.autoBattle = true
		member.session.tasks = make(map[int32]int32)
		member.session.killCount = make(map[int32]int32)
		member.session.bag = make(map[int32]*bagItem)
		member.session.battle.ended = true
	}
	party.settled = true
	cancelReturnTestMapStartup(t, first, second)

	server.onEndMainStoryAI(first, &protocol.C2M_EndMainStoryAI{RpcId: 3})
	if first.session.battle == nil || second.session.battle == nil {
		t.Fatal("pending victory detached the party before BattleVictory")
	}
	if party.aborted {
		t.Fatal("pending victory marked the party aborted")
	}
	for _, member := range []*channel{first, second} {
		if member.session.mainStoryAIRunning || member.session.autoBattle {
			t.Fatalf("member %d AI still running after stop: running=%v auto=%v",
				member.session.playerID, member.session.mainStoryAIRunning, member.session.autoBattle)
		}
		conn := member.conn.(*recordingConn)
		if hasRecordedOpcode(t, conn, protocol.OpM2C_BattleDefeat) ||
			hasRecordedOpcode(t, conn, protocol.OpM2C_BattleVictory) ||
			hasRecordedOpcode(t, conn, protocol.OpM2C_ChangeMap) {
			t.Fatalf("member %d got early settlement frames: %v",
				member.session.playerID, recordedOpcodes(t, conn.Bytes()))
		}
	}

	server.finishPartyVictory(party)
	for _, member := range []*channel{first, second} {
		if member.session.battle != nil {
			t.Fatalf("member %d battle still attached after victory", member.session.playerID)
		}
		if !hasRecordedOpcode(t, member.conn.(*recordingConn), protocol.OpM2C_BattleVictory) {
			t.Fatalf("member %d missing BattleVictory after delayed settlement", member.session.playerID)
		}
		if hasRecordedOpcode(t, member.conn.(*recordingConn), protocol.OpM2C_BattleDefeat) {
			t.Fatalf("member %d received BattleDefeat for a pending victory", member.session.playerID)
		}
	}
}

func TestEndBattleIdleFightAbortsSoloStayOnMap(t *testing.T) {
	ss := newSession()
	ss.state = sessInGame
	ss.playerID = 8
	ss.mapID = 1001303
	ss.resetMovement(1.5, -2.25)
	ss.idleBattle = true
	ss.autoBattle = true
	ss.battle = &battleState{
		mapID: 1001303, playerHP: 80, playerMaxHP: 100,
		playerMP: 40, playerMaxMP: 100, owner: ss,
	}
	ss.battle.runtime = NewCombatRuntime(ss.battle, ss.playerID, nil)
	conn := &recordingConn{}
	ch := &channel{id: 8, conn: conn, session: ss}
	server := &Server{conns: map[int64]*channel{ch.id: ch}}
	cancelReturnTestMapStartup(t, ch)

	resp := server.onEndBattleIdleFight(ch, &protocol.C2M_EndBattleIdleFight{RpcId: 4})
	if resp.(*protocol.M2C_EndBattleIdleFight).RpcId != 4 {
		t.Fatalf("end idle response = %+v", resp)
	}
	if ss.battle != nil || ss.idleBattle || ss.autoBattle {
		t.Fatalf("idle fight remained: battle=%p idle=%v auto=%v", ss.battle, ss.idleBattle, ss.autoBattle)
	}
	if ss.mapID != 1001303 {
		t.Fatalf("idle abort map=%d, want stay on 1001303", ss.mapID)
	}
	assertBattleEndBeforeSameMap(t, conn, protocol.OpM2C_BattleDefeat, 1001303, 1.5, -2.25)
}

func installVisibleCombatBuff(t *testing.T, battle *battleState, targetID int64, key string) {
	t.Helper()
	_, err := battle.runtime.ApplyEffect(CombatEffectContext{
		Source: PlayerCombatUnit(targetID), Target: PlayerCombatUnit(targetID),
	}, EffectSpec{
		Key: key, Kind: CombatEffectAttribute, Attribute: CombatAttributePhysicalAttack,
		Percent: 50, Duration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if battle.effectMeta == nil {
		battle.effectMeta = make(map[string]battleEffectMetadata)
	}
	battle.effectMeta[key] = battleEffectMetadata{
		modifierID: 42, durationMS: 60000, iconID: "bufficon_atkAdd", isBuff: true,
	}
}

func assertBattleEndBeforeSameMap(t *testing.T, conn *recordingConn, endOpcode uint16, mapID int32, x, y float32) {
	t.Helper()
	seenEnd := false
	seenChange := false
	for _, frame := range decodeRecordedFrames(t, conn.Bytes()) {
		switch frame.opcode {
		case endOpcode:
			if seenChange {
				t.Fatalf("opcode %d arrived after ChangeMap", endOpcode)
			}
			seenEnd = true
		case protocol.OpM2C_ChangeMap:
			if !seenEnd {
				t.Fatal("ChangeMap was sent before the client battle-end event")
			}
			var change protocol.M2C_ChangeMap
			if err := proto.Unmarshal(frame.body, &change); err != nil {
				t.Fatal(err)
			}
			if change.MapId != wireMapID(mapID) || change.X != x || change.Y != y {
				t.Fatalf("ChangeMap = map %d at (%.5f, %.5f), want map %d at (%.5f, %.5f)",
					change.MapId, change.X, change.Y, wireMapID(mapID), x, y)
			}
			seenChange = true
		}
	}
	if !seenEnd || !seenChange {
		t.Fatalf("missing stay-on-map exit frames: end=%v change=%v opcodes=%v",
			seenEnd, seenChange, recordedOpcodes(t, conn.Bytes()))
	}
}

func hasRecordedReduceState(t *testing.T, conn *recordingConn, targetID int64) bool {
	t.Helper()
	for _, frame := range decodeRecordedFrames(t, conn.Bytes()) {
		if frame.opcode != protocol.OpM2C_BattleChangeState {
			continue
		}
		var state protocol.M2C_BattleChangeState
		if err := proto.Unmarshal(frame.body, &state); err != nil {
			t.Fatal(err)
		}
		if state.Type == protocol.ChangeType_Reduce && state.Time == 0 && state.TargetUnitId == targetID {
			return true
		}
	}
	return false
}
