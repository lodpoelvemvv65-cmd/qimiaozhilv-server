package main

import (
	"encoding/binary"
	"testing"

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
