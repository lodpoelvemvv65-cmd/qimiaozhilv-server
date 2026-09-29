package main

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func TestPushPlayerAttrsKeepsAuthoritativeCurrentResources(t *testing.T) {
	loadOnlineTablesForTest(t)
	ss := newSession()
	ss.playerID, ss.jobID, ss.skinID, ss.level = 7, 1, 1, 25
	ss.worn = make(map[int32]*bagItem)
	ss.hp, ss.mp = 31, 17
	conn := &recordingConn{}
	ch := &channel{id: 1, conn: conn, session: ss}

	(&Server{}).pushPlayerAttrs(ch)

	values := make(map[int32]float32)
	for _, frame := range decodeRecordedFrames(t, conn.Bytes()) {
		if frame.opcode != protocol.OpM2C_SyncUnitAttribute {
			continue
		}
		message := &protocol.M2C_SyncUnitAttribute{}
		if err := proto.Unmarshal(frame.body, message); err != nil {
			t.Fatal(err)
		}
		values[message.NumericType] = message.Value
	}
	if values[1001] != 31 || values[1003] != 17 {
		t.Fatalf("current resource sync = hp %v mp %v, want 31/17", values[1001], values[1003])
	}
	if values[1002] != float32(ss.playerMaxHp()) || values[1004] != float32(ss.playerMaxMp()) {
		t.Fatalf("maximum resource sync = hp %v mp %v, want %d/%d",
			values[1002], values[1004], ss.playerMaxHp(), ss.playerMaxMp())
	}
}

func TestPushPlayerAttrsClampsCurrentResourcesAfterMaximumDrops(t *testing.T) {
	loadOnlineTablesForTest(t)
	ss := newSession()
	ss.playerID, ss.jobID, ss.skinID, ss.level = 8, 1, 1, 1
	ss.worn = make(map[int32]*bagItem)
	ss.hp, ss.mp = 1_000_000, 1_000_000
	ss.battle = &battleState{
		playerHP: 1_000_000, playerMaxHP: 1_000_000,
		playerMP: 1_000_000, playerMaxMP: 1_000_000,
	}
	ch := &channel{id: 2, conn: &recordingConn{}, session: ss}

	(&Server{}).pushPlayerAttrs(ch)

	if ss.battle.playerMaxHP != ss.playerMaxHp() || ss.battle.playerMaxMP != ss.playerMaxMp() {
		t.Fatalf("battle maxima = %d/%d, want %d/%d", ss.battle.playerMaxHP, ss.battle.playerMaxMP,
			ss.playerMaxHp(), ss.playerMaxMp())
	}
	if ss.battle.playerHP != ss.playerMaxHp() || ss.battle.playerMP != ss.playerMaxMp() ||
		ss.hp != ss.playerMaxHp() || ss.mp != ss.playerMaxMp() {
		t.Fatalf("clamped current resources battle=%d/%d session=%d/%d, want %d/%d",
			ss.battle.playerHP, ss.battle.playerMP, ss.hp, ss.mp, ss.playerMaxHp(), ss.playerMaxMp())
	}
}
