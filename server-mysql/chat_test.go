package main

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func TestChatDoesNotEchoSenderTwice(t *testing.T) {
	firstSession := newSession()
	firstSession.state = sessInGame
	firstSession.playerID, firstSession.name = 101, "first"
	secondSession := newSession()
	secondSession.state = sessInGame
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

func TestChatRoutesNormalTeamFamilyAndWorldChannels(t *testing.T) {
	teamMu.Lock()
	oldTeams, oldSeq := teams, teamSeq
	teams = make(map[int64]*teamState)
	teamSeq = 1
	teamMu.Unlock()
	t.Cleanup(func() {
		teamMu.Lock()
		teams, teamSeq = oldTeams, oldSeq
		teamMu.Unlock()
	})

	makePlayer := func(id int64, mapID int32, familyID int64) *channel {
		ss := newSession()
		ss.state, ss.playerID, ss.name = sessInGame, id, "player"
		ss.mapID, ss.familyID = mapID, familyID
		return &channel{id: id, conn: &recordingConn{}, session: ss}
	}
	sender := makePlayer(101, 10004, 10)
	teammate := makePlayer(202, 10005, 20)
	familyMate := makePlayer(303, 10006, 10)
	bystander := makePlayer(404, 10004, 30)
	server := &Server{conns: map[int64]*channel{
		sender.id: sender, teammate.id: teammate, familyMate.id: familyMate, bystander.id: bystander,
	}}
	teamMu.Lock()
	teams[1] = &teamState{LeaderId: sender.session.playerID, Members: []int64{sender.session.playerID, teammate.session.playerID}}
	teamMu.Unlock()

	clear := func() {
		for _, ch := range []*channel{sender, teammate, familyMate, bystander} {
			ch.conn = &recordingConn{}
		}
	}
	assertOnly := func(t *testing.T, expected *channel) {
		t.Helper()
		for _, ch := range []*channel{sender, teammate, familyMate, bystander} {
			got := len(decodeRecordedFrames(t, ch.conn.(*recordingConn).Bytes()))
			want := 0
			if ch == expected {
				want = 1
			}
			if got != want {
				t.Fatalf("recipient %d frames=%d, want %d", ch.session.playerID, got, want)
			}
		}
	}

	server.onRequestChat(sender, &protocol.C2M_RequestChat{Content: "normal", Type: protocol.ChatType_Normal})
	assertOnly(t, bystander)
	clear()
	server.onRequestChat(sender, &protocol.C2M_RequestChat{Content: "team", Type: protocol.ChatType_Team})
	assertOnly(t, teammate)
	clear()
	server.onRequestChat(sender, &protocol.C2M_RequestChat{Content: "family", Type: protocol.ChatType_Family})
	assertOnly(t, familyMate)
	clear()
	server.onRequestChat(sender, &protocol.C2M_RequestChat{Content: "world", Type: protocol.ChatType_World})
	for _, ch := range []*channel{teammate, familyMate, bystander} {
		if got := len(decodeRecordedFrames(t, ch.conn.(*recordingConn).Bytes())); got != 1 {
			t.Fatalf("world recipient %d frames=%d, want 1", ch.session.playerID, got)
		}
	}
	if got := len(decodeRecordedFrames(t, sender.conn.(*recordingConn).Bytes())); got != 0 {
		t.Fatalf("world sender echoed %d frames", got)
	}
}

func TestChatRejectsPlayerSystemMessages(t *testing.T) {
	ss := newSession()
	ss.state, ss.playerID = sessInGame, 101
	ch := &channel{id: 1, conn: &recordingConn{}, session: ss}
	server := &Server{conns: map[int64]*channel{ch.id: ch}}
	response := server.onRequestChat(ch, &protocol.C2M_RequestChat{Content: "fake", Type: protocol.ChatType_System}).(*protocol.M2C_RequestChat)
	if response.Message == "" {
		t.Fatal("player-authored system chat was accepted")
	}
	if frames := decodeRecordedFrames(t, ch.conn.(*recordingConn).Bytes()); len(frames) != 0 {
		t.Fatalf("rejected system chat emitted frames=%+v", frames)
	}
}
