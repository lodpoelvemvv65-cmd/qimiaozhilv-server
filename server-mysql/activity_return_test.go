package main

import (
	"testing"
	"time"

	"mhqserver/protocol"
)

func TestWorldBossPartyReturnWithContinuousVisualKeepsAllMembers(t *testing.T) {
	loadOnlineTablesForTest(t)
	teamMu.Lock()
	oldTeams, oldSeq := teams, teamSeq
	teams, teamSeq = make(map[int64]*teamState), 1
	teamMu.Unlock()
	t.Cleanup(func() {
		teamMu.Lock()
		teams, teamSeq = oldTeams, oldSeq
		teamMu.Unlock()
	})
	server := &Server{conns: make(map[int64]*channel)}
	members := make([]*channel, 0, maxTeamMembers)
	ids := make([]int64, 0, maxTeamMembers)
	for index := 0; index < maxTeamMembers; index++ {
		_, session, member := activityTestChannel(t, int64(7800+index))
		session.teamID = 1
		server.conns[member.id] = member
		members = append(members, member)
		ids = append(ids, session.playerID)
	}
	teamMu.Lock()
	teams[1] = &teamState{LeaderId: ids[0], Members: ids}
	teamMu.Unlock()
	activity := &activityBattle{ActiveID: 10022, Method: activeWorldBoss, Stage: 1}
	if started, message := server.startActivityStage(members[0], activity, false); !started {
		t.Fatalf("start world boss: %s", message)
	}
	finishPendingActivityScene(t, server, members...)
	clickActivityFieldMonster(t, server, members...)
	party := members[0].session.battle.party
	returnSeqs := make(map[int64]uint64, len(members))
	initialChanges := make(map[int64]int, len(members))
	for _, member := range members {
		returnSeqs[member.session.playerID], _ = member.session.currentMapSceneVersion()
		for _, opcode := range recordedOpcodes(t, member.conn.(*recordingConn).Bytes()) {
			if opcode == protocol.OpM2C_ChangeMap {
				initialChanges[member.session.playerID]++
			}
		}
	}
	party.mu.Lock()
	party.settled = true
	for _, battle := range party.members {
		battle.ended = true
		for _, monster := range battle.monsters {
			monster.hp, monster.alive = 0, false
		}
	}
	party.mu.Unlock()
	server.finishPartyVictory(party)
	for _, member := range members {
		waitForActivityMapReady(t, member, returnSeqs[member.session.playerID], 10004)
	}
	time.Sleep(mapStartupDelay + 50*time.Millisecond)
	for _, member := range members {
		if member.session.mapID != 10004 || member.session.battle != nil {
			t.Fatalf("player %d stranded: map=%d battle=%v", member.session.playerID, member.session.mapID, member.session.battle)
		}
		changes := -initialChanges[member.session.playerID]
		for _, opcode := range recordedOpcodes(t, member.conn.(*recordingConn).Bytes()) {
			if opcode == protocol.OpM2C_ChangeMap {
				changes++
			}
		}
		if changes != 1 {
			t.Fatalf("player %d map changes=%d, want one return", member.session.playerID, changes)
		}
		leader, visible := server.teamSnapshotFor(member, ids[0], ids)
		if leader != ids[0] || len(visible) != maxTeamMembers {
			t.Fatalf("player %d team split: leader=%d members=%v", member.session.playerID, leader, visible)
		}
	}
}
