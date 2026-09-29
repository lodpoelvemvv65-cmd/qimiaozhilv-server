package main

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func TestLaterPlayerUsesNativeEnterMapForExistingPlayers(t *testing.T) {
	firstSession := newSession()
	firstSession.playerID = 2
	firstSession.name = "first"
	firstSession.jobID = 1
	firstSession.skinID = 1
	firstSession.level = 10
	firstSession.mapID = 10004
	firstSession.x, firstSession.y = -12, -1
	firstConn := &recordingConn{}
	first := &channel{id: 1, conn: firstConn, session: firstSession}

	laterSession := newSession()
	laterSession.playerID = 408
	laterSession.name = "later"
	laterSession.jobID = 2
	laterSession.skinID = 2
	laterSession.level = 20
	laterSession.mapID = 10004
	laterSession.x, laterSession.y = -8, -3
	later := &channel{id: 2, conn: &recordingConn{}, session: laterSession}

	server := &Server{conns: map[int64]*channel{first.id: first, later.id: later}}
	server.broadcastUnitsInMapExcept(later)

	frames := decodeRecordedFrames(t, firstConn.Bytes())
	if len(frames) != 1 || frames[0].opcode != protocol.OpM2C_EnterMap {
		t.Fatalf("existing player frames = %+v, want one EnterMap", frames)
	}
	message := &protocol.M2C_EnterMap{}
	if err := proto.Unmarshal(frames[0].body, message); err != nil {
		t.Fatal(err)
	}
	if message.PvpUnitCharacter == nil || message.PvpUnitCharacter.Id != laterSession.playerID ||
		message.UnitInfo == nil || message.UnitInfo.X != laterSession.x || message.UnitInfo.Y != laterSession.y ||
		message.ActorId != firstSession.playerID {
		t.Fatalf("EnterMap = %+v", message)
	}
}

func TestExistingPlayerUpdateDoesNotCreateDuplicateSceneUnit(t *testing.T) {
	firstSession := newSession()
	firstSession.playerID, firstSession.mapID = 2, 10004
	first := &channel{id: 1, conn: &recordingConn{}, session: firstSession}
	updatedSession := newSession()
	updatedSession.playerID, updatedSession.mapID = 408, 10004
	updatedSession.name, updatedSession.jobID, updatedSession.skinID, updatedSession.level = "updated", 1, 1, 10
	updated := &channel{id: 2, conn: &recordingConn{}, session: updatedSession}
	server := &Server{conns: map[int64]*channel{first.id: first, updated.id: updated}}

	server.broadcastPlayerUpdate(updated)
	frames := decodeRecordedFrames(t, first.conn.(*recordingConn).Bytes())
	if len(frames) == 0 || frames[0].opcode != protocol.OpM2C_SendUnitInfo {
		t.Fatalf("first update frame = %+v, want SendUnitInfo", frames)
	}
	for _, frame := range frames {
		if frame.opcode == protocol.OpM2C_EnterMap || frame.opcode == protocol.OpM2C_UnitsInMap {
			t.Fatalf("existing player update recreated scene unit with opcode %d", frame.opcode)
		}
	}
}
