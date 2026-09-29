package main

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func TestPushSyncPetBroadcastsToVisiblePlayers(t *testing.T) {
	owner, ownerConn := consistencyChannel(7101, 10004)
	owner.session.pet = &petState{
		PetId: 2102, Level: 12, Exp: 345, Intimacy: 67,
		Name: "同行宠物", IsShow: true, Active: 89,
	}
	visible, visibleConn := consistencyChannel(7102, 1000401)
	hidden, hiddenConn := consistencyChannel(7103, 1000601)
	server := &Server{conns: map[int64]*channel{
		owner.id: owner, visible.id: visible, hidden.id: hidden,
	}}

	server.pushSyncPet(owner)

	assertSnapshot := func(receiver *channel, conn *recordingConn) {
		t.Helper()
		frames := decodeRecordedFrames(t, conn.Bytes())
		if len(frames) != 1 || frames[0].opcode != protocol.OpM2C_SyncPet {
			t.Fatalf("receiver %d pet opcodes=%v, want one SyncPet", receiver.id, recordedOpcodes(t, conn.Bytes()))
		}
		message := &protocol.M2C_SyncPet{}
		if err := proto.Unmarshal(frames[0].body, message); err != nil {
			t.Fatal(err)
		}
		pet := owner.session.pet
		if message.UnitId != owner.session.playerID || message.ActorId != receiver.session.playerID ||
			message.PetId != pet.PetId || message.Level != pet.Level || message.Exp != pet.Exp ||
			message.Intimacy != pet.Intimacy || message.Name != pet.Name ||
			message.IsShow != pet.IsShow || message.Active != pet.Active {
			t.Fatalf("receiver %d pet snapshot=%+v", receiver.id, message)
		}
	}

	assertSnapshot(owner, ownerConn)
	assertSnapshot(visible, visibleConn)
	if got := recordedOpcodes(t, hiddenConn.Bytes()); len(got) != 0 {
		t.Fatalf("other-map player received pet opcodes=%v", got)
	}
}
