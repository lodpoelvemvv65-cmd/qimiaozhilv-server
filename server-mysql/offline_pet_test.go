package main

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func TestDisconnectDeepRemovesPlayerAndVisiblePet(t *testing.T) {
	owner, _ := consistencyChannel(7201, 10004)
	owner.session.state = sessInGame
	owner.session.pet = &petState{
		PetId: 2102, Level: 12, Name: "visible pet", IsShow: true,
	}
	observer, observerConn := consistencyChannel(7202, 1000401)
	server := &Server{conns: map[int64]*channel{
		owner.id: owner, observer.id: observer,
	}}

	server.cleanupPlayerSession(owner)

	frames := decodeRecordedFrames(t, observerConn.Bytes())
	if len(frames) != 1 || frames[0].opcode != protocol.OpM2C_OffLine {
		t.Fatalf("observer opcodes=%v, want one OffLine", recordedOpcodes(t, observerConn.Bytes()))
	}
	message := &protocol.M2C_OffLine{}
	if err := proto.Unmarshal(frames[0].body, message); err != nil {
		t.Fatal(err)
	}
	if message.UnitId != owner.session.playerID || message.ActorId != observer.session.playerID {
		t.Fatalf("offline message=%+v", message)
	}
	if !owner.session.offlineRun.Load() {
		t.Fatal("disconnect cleanup did not mark the owner offline")
	}
}

func TestMapTransitionStillUsesShallowLeaveMap(t *testing.T) {
	owner, _ := consistencyChannel(7301, 10004)
	observer, observerConn := consistencyChannel(7302, 1000401)
	server := &Server{conns: map[int64]*channel{
		owner.id: owner, observer.id: observer,
	}}

	server.broadcastLeaveMap(owner)

	frames := decodeRecordedFrames(t, observerConn.Bytes())
	if len(frames) != 1 || frames[0].opcode != protocol.OpM2C_LeaveMap {
		t.Fatalf("observer opcodes=%v, want one LeaveMap", recordedOpcodes(t, observerConn.Bytes()))
	}
}
