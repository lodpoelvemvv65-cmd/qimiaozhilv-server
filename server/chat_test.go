package main

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func TestChatDoesNotEchoSenderTwice(t *testing.T) {
	firstSession := newSession()
	firstSession.playerID, firstSession.name = 101, "first"
	secondSession := newSession()
	secondSession.playerID, secondSession.name = 202, "second"
	first := &channel{id: 1, conn: &recordingConn{}, session: firstSession}
	second := &channel{id: 2, conn: &recordingConn{}, session: secondSession}
	server := &Server{conns: map[int64]*channel{first.id: first, second.id: second}}

	response := server.onRequestChat(first, &protocol.C2M_RequestChat{
		RpcId: 1, Content: "hello", Type: protocol.ChatType_World,
	})
	if response == nil {
		t.Fatal("chat response is nil")
	}
	firstFrames := decodeRecordedFrames(t, first.conn.(*recordingConn).Bytes())
	secondFrames := decodeRecordedFrames(t, second.conn.(*recordingConn).Bytes())
	if len(firstFrames) != 0 {
		t.Fatalf("sender received server chat echo = %+v", firstFrames)
	}
	if len(secondFrames) != 1 || secondFrames[0].opcode != protocol.OpM2C_SendNormalChat {
		t.Fatalf("other player chat frames = %+v", secondFrames)
	}
	var message protocol.M2C_SendNormalChat
	if err := proto.Unmarshal(secondFrames[0].body, &message); err != nil {
		t.Fatal(err)
	}
	if message.Content != "hello" || message.ActorId != firstSession.playerID {
		t.Fatalf("chat message = %+v", &message)
	}
}
