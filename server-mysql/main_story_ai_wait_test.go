package main

import (
	"sync/atomic"
	"testing"
	"time"

	"mhqserver/protocol"
)

func TestMainStoryAIWaitsForLatePartyMember(t *testing.T) {
	loadOnlineTablesForTest(t)
	teamMu.Lock()
	previousTeams := teams
	teams = make(map[int64]*teamState)
	teamMu.Unlock()
	t.Cleanup(func() { teamMu.Lock(); teams = previousTeams; teamMu.Unlock() })
	server := &Server{conns: make(map[int64]*channel)}
	leader, _ := consistencyChannel(88101, 10004)
	member, _ := consistencyChannel(88102, 10004)
	server.conns[leader.id], server.conns[member.id] = leader, member
	teamMu.Lock()
	teams[1] = &teamState{LeaderId: leader.session.playerID, Members: []int64{leader.session.playerID, member.session.playerID}}
	teamMu.Unlock()
	leader.session.teamID, member.session.teamID = 1, 1
	leader.session.mainStoryAIRunning = true
	seq := leader.session.markMapChange()
	leader.session.markMapReady(seq)
	leader.session.markMapStartupComplete(seq)
	lateSeq := member.session.markMapChange()
	var resumed atomic.Int32
	server.waitMainStoryAIReady(leader, 0, 10004, 1001110, seq, 3*time.Second, func() { resumed.Add(1) })
	time.Sleep(1200 * time.Millisecond)
	if resumed.Load() != 0 {
		t.Fatal("resumed before member scene was ready")
	}
	member.session.markMapReady(lateSeq)
	member.session.markMapStartupComplete(lateSeq)
	deadline := time.Now().Add(time.Second)
	for resumed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	server.closed.Store(true)
	if resumed.Load() != 1 {
		t.Fatalf("resumed=%d, want once", resumed.Load())
	}
}

func TestMainStoryAIWaitCancellationAndTimeout(t *testing.T) {
	loadOnlineTablesForTest(t)
	for _, cancel := range []bool{true, false} {
		server := &Server{conns: make(map[int64]*channel)}
		ch, conn := consistencyChannel(88103, 10004)
		server.conns[ch.id] = ch
		ch.session.markMapChange()
		ch.session.mainStoryAIRunning = true
		var resumed atomic.Bool
		server.waitMainStoryAIReady(ch, 0, 10004, 1001110, 0, 150*time.Millisecond, func() { resumed.Store(true) })
		if cancel {
			ch.session.stopMainStoryAI()
		}
		time.Sleep(400 * time.Millisecond)
		server.closed.Store(true)
		if running, _ := ch.session.mainStoryAISnapshot(); running || resumed.Load() {
			t.Fatal("cancelled/timed out continuation remained active")
		}
		frames := decodeRecordedFrames(t, conn.Bytes())
		if !cancel && (len(frames) != 1 || frames[0].opcode != protocol.OpM2C_SendTip) {
			t.Fatal("timeout did not report stop reason")
		}
	}
}

func TestMainStoryAIConsumesLeaderAndFollowerPortalRequests(t *testing.T) {
	loadOnlineTablesForTest(t)
	teamMu.Lock()
	previousTeams := teams
	teams = make(map[int64]*teamState)
	teamMu.Unlock()
	t.Cleanup(func() { teamMu.Lock(); teams = previousTeams; teamMu.Unlock() })

	server := &Server{conns: make(map[int64]*channel)}
	leader, _ := consistencyChannel(88201, 1001110)
	follower, _ := consistencyChannel(88202, 1001110)
	server.conns[leader.id], server.conns[follower.id] = leader, follower
	leader.session.teamID, follower.session.teamID = 1, 1
	leader.session.mainStoryAIRunning = true
	teamMu.Lock()
	teams[1] = &teamState{LeaderId: leader.session.playerID, Members: []int64{leader.session.playerID, follower.session.playerID}}
	teamMu.Unlock()

	for _, member := range []*channel{leader, follower} {
		response, transition := server.onRequestEnterMap(member, &protocol.C2M_RequestEnterMap{
			RpcId: 7, MapId: 1001111,
		})
		if response.Message != "" || transition != nil || member.session.mapID != 1001110 {
			t.Fatalf("player %d portal request response=%+v transition=%v map=%d",
				member.session.playerID, response, transition != nil, member.session.mapID)
		}
	}
}
