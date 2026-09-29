package main

import (
	"reflect"
	"testing"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func dungeonTeamTestParty(t *testing.T, ids ...int64) (*Server, []*channel) {
	t.Helper()
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
	members := make([]*channel, 0, len(ids))
	for _, id := range ids {
		ss := newSession()
		ss.state = sessInGame
		ss.playerID, ss.name = id, "dungeon-team"
		ss.jobID, ss.level, ss.mapID, ss.teamID = 1, 15000, 10004, 1
		ss.signin = &signinState{}
		ch := &channel{id: id, conn: &recordingConn{}, session: ss}
		server.conns[id] = ch
		members = append(members, ch)
	}
	teamMu.Lock()
	teams[1] = &teamState{LeaderId: ids[0], Members: append([]int64(nil), ids...)}
	teamMu.Unlock()
	return server, members
}

func lastRecordedTeamSnapshot(t *testing.T, ch *channel) *protocol.M2C_TeamMember {
	t.Helper()
	var snapshot *protocol.M2C_TeamMember
	for _, frame := range decodeRecordedFrames(t, ch.conn.(*recordingConn).Bytes()) {
		if frame.opcode != protocol.OpM2C_TeamMember {
			continue
		}
		snapshot = &protocol.M2C_TeamMember{}
		if err := proto.Unmarshal(frame.body, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	return snapshot
}

func TestSoloInstanceTeamExitUsesEntrantRole(t *testing.T) {
	loadOnlineTablesForTest(t)

	t.Run("member leaves", func(t *testing.T) {
		server, members := dungeonTeamTestParty(t, 101, 202, 303)
		if !server.leaveTeamForSoloInstance(members[1], 10004) {
			t.Fatal("trial entry did not remove the regular member")
		}
		teamMu.Lock()
		team := teams[1]
		gotMembers := append([]int64(nil), team.Members...)
		teamMu.Unlock()
		if team == nil || team.LeaderId != 101 || !reflect.DeepEqual(gotMembers, []int64{101, 303}) {
			t.Fatalf("remaining team=%+v members=%v", team, gotMembers)
		}
		if members[0].session.teamID != 1 || members[1].session.teamID != 0 || members[2].session.teamID != 1 {
			t.Fatalf("team ids after member exit=%d/%d/%d",
				members[0].session.teamID, members[1].session.teamID, members[2].session.teamID)
		}
		if snapshot := lastRecordedTeamSnapshot(t, members[1]); snapshot == nil ||
			snapshot.LeaderId != 202 || !reflect.DeepEqual(snapshot.UnitIds, []int64{202}) {
			t.Fatalf("departing member snapshot=%+v", snapshot)
		}
	})

	t.Run("leader disbands", func(t *testing.T) {
		server, members := dungeonTeamTestParty(t, 401, 402, 403)
		if !server.leaveTeamForSoloInstance(members[0], 10008) {
			t.Fatal("personal PVP entry did not disband the leader's team")
		}
		teamMu.Lock()
		_, exists := teams[1]
		teamMu.Unlock()
		if exists {
			t.Fatal("leader's team still exists after solo entry")
		}
		for _, member := range members {
			if member.session.teamID != 0 {
				t.Fatalf("player %d retained team id %d", member.session.playerID, member.session.teamID)
			}
			snapshot := lastRecordedTeamSnapshot(t, member)
			if snapshot == nil || snapshot.LeaderId != member.session.playerID ||
				!reflect.DeepEqual(snapshot.UnitIds, []int64{member.session.playerID}) {
				t.Fatalf("player %d disband snapshot=%+v", member.session.playerID, snapshot)
			}
		}
	})

	t.Run("team copy stays grouped", func(t *testing.T) {
		server, members := dungeonTeamTestParty(t, 501, 502)
		if server.leaveTeamForSoloInstance(members[0], 10014) {
			t.Fatal("team-enabled star-soul copy changed the team")
		}
		teamMu.Lock()
		team := teams[1]
		teamMu.Unlock()
		if team == nil || len(team.Members) != 2 || members[0].session.teamID != 1 || members[1].session.teamID != 1 {
			t.Fatalf("team-enabled copy state=%+v ids=%d/%d", team,
				members[0].session.teamID, members[1].session.teamID)
		}
	})

	t.Run("death road stays grouped", func(t *testing.T) {
		server, members := dungeonTeamTestParty(t, 511, 512)
		if boolOf(tables.copyConfig[10016]["CanTeam"]) {
			t.Fatal("online death-road copy unexpectedly permits teams")
		}
		if server.leaveTeamForSoloInstance(members[0], 10016) {
			t.Fatal("death-road party exception changed the team")
		}
		participants := server.configuredBattleParticipants(members[0], 10016, false)
		if len(participants) != 2 {
			t.Fatalf("death-road participants=%d, want 2", len(participants))
		}
		teamMu.Lock()
		team := teams[1]
		teamMu.Unlock()
		if team == nil || len(team.Members) != 2 || members[0].session.teamID != 1 || members[1].session.teamID != 1 {
			t.Fatalf("death-road team state=%+v ids=%d/%d", team,
				members[0].session.teamID, members[1].session.teamID)
		}
	})
}

func TestPartyMemberCannotInitiateTeamDungeon(t *testing.T) {
	loadOnlineTablesForTest(t)
	server, members := dungeonTeamTestParty(t, 601, 602)
	member := members[1]

	active := server.onStartActive(member, &protocol.C2M_StartActive{
		RpcId: 1, ItemId: 10022,
	}).(*protocol.M2C_StartActive)
	if active.Message != teamLeaderDungeonEntryMessage {
		t.Fatalf("party member world-boss response=%+v", active)
	}
	if member.session.mapID != 10004 || server.hasPendingActivityStart(member.session.playerID) {
		t.Fatalf("rejected activity changed state: map=%d pending=%v",
			member.session.mapID, server.hasPendingActivityStart(member.session.playerID))
	}

	deathRoad := server.onStartActive(member, &protocol.C2M_StartActive{
		RpcId: 3, ItemId: 10023,
	}).(*protocol.M2C_StartActive)
	if deathRoad.Message != teamLeaderDungeonEntryMessage {
		t.Fatalf("party member death-road response=%+v", deathRoad)
	}
	if member.session.mapID != 10004 || server.hasPendingActivityStart(member.session.playerID) {
		t.Fatalf("rejected death-road entry changed state: map=%d pending=%v",
			member.session.mapID, server.hasPendingActivityStart(member.session.playerID))
	}

	mapResponse, transition := server.onRequestEnterMap(member, &protocol.C2M_RequestEnterMap{
		RpcId: 2, MapId: 1001001,
	})
	if mapResponse.Message != teamLeaderDungeonEntryMessage || transition != nil {
		t.Fatalf("party member dungeon map response=%+v transition=%v", mapResponse, transition != nil)
	}
	teamMu.Lock()
	team := teams[1]
	teamMu.Unlock()
	if team == nil || len(team.Members) != 2 || members[0].session.teamID != 1 || member.session.teamID != 1 {
		t.Fatalf("rejected entries changed team=%+v ids=%d/%d", team,
			members[0].session.teamID, member.session.teamID)
	}
}

func TestRejectedSoloInstanceEntryKeepsTeam(t *testing.T) {
	loadOnlineTablesForTest(t)
	server, members := dungeonTeamTestParty(t, 701, 702)
	leader := members[0]
	leader.session.hp = leader.session.playerMaxHp()
	leader.session.mp = leader.session.playerMaxMp()
	setDungeonQuotaForTest(leader.session, 50, 0, 2)

	started, message := server.startActivityStage(leader, &activityBattle{
		ActiveID: 10023, Method: activeJourneyOfDeath, Difficulty: 1, Stage: 1,
	}, false)
	if started || message != "死亡之塔战斗次数不足" {
		t.Fatalf("rejected death-road entry start=%v message=%q", started, message)
	}
	teamMu.Lock()
	team := teams[1]
	teamMu.Unlock()
	if team == nil || len(team.Members) != 2 || members[0].session.teamID != 1 || members[1].session.teamID != 1 {
		t.Fatalf("rejected solo entry changed team=%+v ids=%d/%d", team,
			members[0].session.teamID, members[1].session.teamID)
	}
}

func TestPersonalPVPLeavesTeamsOnlyAfterMatch(t *testing.T) {
	loadOnlineTablesForTest(t)
	teamMu.Lock()
	oldTeams, oldSeq := teams, teamSeq
	teams, teamSeq = make(map[int64]*teamState), 2
	teamMu.Unlock()
	t.Cleanup(func() {
		teamMu.Lock()
		teams, teamSeq = oldTeams, oldSeq
		teamMu.Unlock()
	})

	requester := newPersonalPVPTestChannel(801, 10004, -2, -1)
	waiting := newPersonalPVPTestChannel(802, 10004, 2, -1)
	requesterFollower := newPersonalPVPTestChannel(803, 10004, -3, -1)
	waitingLeader := newPersonalPVPTestChannel(804, 10004, 3, -1)
	requester.session.teamID, requesterFollower.session.teamID = 1, 1
	waiting.session.teamID, waitingLeader.session.teamID = 2, 2
	server := &Server{conns: map[int64]*channel{
		requester.id: requester, waiting.id: waiting,
		requesterFollower.id: requesterFollower, waitingLeader.id: waitingLeader,
	}}
	teamMu.Lock()
	teams[1] = &teamState{LeaderId: 801, Members: []int64{801, 803}}
	teams[2] = &teamState{LeaderId: 804, Members: []int64{804, 802}}
	teamMu.Unlock()

	if response := server.onRequestPersonalPvp(requester, &protocol.C2M_RequestPersonalPvp{RpcId: 1}); response == nil {
		t.Fatal("first personal PVP request did not enter the queue")
	}
	teamMu.Lock()
	queuedLeaderTeam := teams[1]
	teamMu.Unlock()
	if queuedLeaderTeam == nil || requester.session.teamID != 1 || requesterFollower.session.teamID != 1 {
		t.Fatal("waiting for a match changed the requester's team")
	}

	if response := server.onRequestPersonalPvp(waiting, &protocol.C2M_RequestPersonalPvp{RpcId: 2}); response != nil {
		t.Fatalf("second personal PVP request did not match: %+v", response)
	}
	teamMu.Lock()
	_, leaderTeamExists := teams[1]
	memberTeam := teams[2]
	var memberTeamIDs []int64
	if memberTeam != nil {
		memberTeamIDs = append(memberTeamIDs, memberTeam.Members...)
	}
	teamMu.Unlock()
	if leaderTeamExists || requester.session.teamID != 0 || requesterFollower.session.teamID != 0 {
		t.Fatalf("matched leader did not disband team: exists=%v ids=%d/%d",
			leaderTeamExists, requester.session.teamID, requesterFollower.session.teamID)
	}
	if memberTeam == nil || memberTeam.LeaderId != 804 || !reflect.DeepEqual(memberTeamIDs, []int64{804}) ||
		waiting.session.teamID != 0 || waitingLeader.session.teamID != 2 {
		t.Fatalf("matched member exit state team=%+v members=%v ids=%d/%d",
			memberTeam, memberTeamIDs, waiting.session.teamID, waitingLeader.session.teamID)
	}
	if requester.session.mapID != personalPVPArenaMapID || waiting.session.mapID != personalPVPArenaMapID {
		t.Fatalf("matched players did not enter arena: maps=%d/%d",
			requester.session.mapID, waiting.session.mapID)
	}

	duel := requester.session.battle.pvp
	duel.mu.Lock()
	server.finishPVPBattleLocked(duel, requester.session.playerID, "test-cleanup")
	duel.mu.Unlock()
}
