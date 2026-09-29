package main

import (
	"reflect"
	"testing"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func TestFamilyWireMatchesClientFieldLevelTypes(t *testing.T) {
	row := &familyRow{
		ID: 7, Name: "family", Leader: 101, Level: 3, Hornor: 88, Notice: "notice",
		Members: []*familyMember{{ID: 101, Name: "leader", Job: 1, Level: 20}},
	}
	info := familyInfoWire(row)
	positions := bytesFields(t, info, 4)
	if len(positions) != 1 {
		t.Fatalf("position count = %d, want 1", len(positions))
	}
	var position protocol.FamilyPositionMap
	if err := proto.Unmarshal(positions[0], &position); err != nil {
		t.Fatal(err)
	}
	if position.Name != "leader" || position.Position != protocol.FamilyPosition_FamilyLeader {
		t.Fatalf("leader position = %+v", &position)
	}

	member := &protocol.FamilyMemberInfo{Id: 101, Name: "leader", Level: 20}
	raw, err := proto.Marshal(member)
	if err != nil {
		t.Fatal(err)
	}
	raw = appendFamilyContributionList(raw, 6, member, 101, 345)
	contributions := bytesFields(t, raw, 6)
	if len(contributions) != 1 {
		t.Fatalf("contribution count = %d, want 1", len(contributions))
	}
	var contribution protocol.FamilyContributionMap
	if err := proto.Unmarshal(contributions[0], &contribution); err != nil {
		t.Fatal(err)
	}
	if contribution.Id != member.Id || contribution.Value != 345 {
		t.Fatalf("member contribution = %+v", &contribution)
	}

	if got := clientLastLogin(1_700_000_000, false); got != 1_700_000_000_000 {
		t.Fatalf("seconds were not converted to milliseconds: %d", got)
	}
	if got := clientLastLogin(1_700_000_000, true); got != 0 {
		t.Fatalf("online last-login = %d, want 0", got)
	}
}

func TestGetFriendIncludesRequestsAndClientTimeUnits(t *testing.T) {
	ownerSession := newSession()
	ownerSession.playerID, ownerSession.name = 101, "owner"
	ownerSession.friends[202] = &friendInfo{Id: 202, Name: "online", Job: 1, Level: 9, LastLogin: 1_700_000_000}
	ownerSession.friends[303] = &friendInfo{Id: 303, Name: "offline", Job: 2, Level: 8, LastLogin: 1_700_000_000}
	ownerSession.friendReqFrom[404] = true
	owner := &channel{id: 1, conn: &recordingConn{}, session: ownerSession}

	onlineSession := newSession()
	onlineSession.playerID, onlineSession.name, onlineSession.jobID, onlineSession.level = 202, "online", 1, 9
	online := &channel{id: 2, conn: &recordingConn{}, session: onlineSession}
	requestSession := newSession()
	requestSession.playerID, requestSession.name, requestSession.jobID, requestSession.level = 404, "requester", 5, 12
	requester := &channel{id: 3, conn: &recordingConn{}, session: requestSession}
	server := &Server{conns: map[int64]*channel{1: owner, 2: online, 3: requester}}

	if response := server.onGetFriend(owner, &protocol.C2M_GetFriend{RpcId: 77}); response != nil {
		t.Fatalf("raw friend response unexpectedly returned %+v", response)
	}
	frames := decodeRecordedFrames(t, owner.conn.(*recordingConn).Bytes())
	if len(frames) != 1 || frames[0].opcode != protocol.OpM2C_GetFriend {
		t.Fatalf("friend frames = %+v", frames)
	}
	friends := bytesFields(t, frames[0].body, 1)
	if len(friends) != 2 {
		t.Fatalf("friend count = %d, want 2", len(friends))
	}
	lastLogin := make(map[int64]int64)
	for _, raw := range friends {
		var friend protocol.FriendInfo
		if err := proto.Unmarshal(raw, &friend); err != nil {
			t.Fatal(err)
		}
		lastLogin[friend.Id] = friend.LastLginTime
	}
	if lastLogin[202] != 0 || lastLogin[303] != 1_700_000_000_000 {
		t.Fatalf("friend last-login values = %v", lastLogin)
	}
	requests := bytesFields(t, frames[0].body, 2)
	if len(requests) != 1 {
		t.Fatalf("request count = %d, want 1", len(requests))
	}
	var request protocol.RequestAddFriendInfo
	if err := proto.Unmarshal(requests[0], &request); err != nil {
		t.Fatal(err)
	}
	if request.Id != 404 || request.Name != "requester" || request.Level != 12 ||
		request.Job != protocol.JobType_Nurse || request.Sex != protocol.SexType_Famale {
		t.Fatalf("friend request = %+v", &request)
	}
}

func TestTeamInviteRequiresSharedSceneAndSyncsBothClients(t *testing.T) {
	teamMu.Lock()
	oldTeams, oldSeq := teams, teamSeq
	teams, teamSeq = make(map[int64]*teamState), 0
	teamMu.Unlock()
	t.Cleanup(func() {
		teamMu.Lock()
		teams, teamSeq = oldTeams, oldSeq
		teamMu.Unlock()
	})

	makePlayer := func(id int64, mapID int32) *channel {
		ss := newSession()
		ss.playerID, ss.mapID = id, mapID
		return &channel{id: id, conn: &recordingConn{}, session: ss}
	}
	leader := makePlayer(101, 10004)
	member := makePlayer(202, 10005)
	server := &Server{conns: map[int64]*channel{101: leader, 202: member}}

	rejected := server.onInviteTeam(leader, &protocol.C2M_InviteTeam{RpcId: 1, TargetId: 202}).(*protocol.M2C_InviteTeam)
	if rejected.Message == "" || len(member.conn.(*recordingConn).Bytes()) != 0 {
		t.Fatalf("cross-map invite was not rejected: response=%+v", rejected)
	}

	member.session.mapID = leader.session.mapID
	accepted := server.onInviteTeam(leader, &protocol.C2M_InviteTeam{RpcId: 2, TargetId: 202}).(*protocol.M2C_InviteTeam)
	if accepted.Message != "" {
		t.Fatalf("same-map invite rejected: %+v", accepted)
	}
	inviteFrames := decodeRecordedFrames(t, member.conn.(*recordingConn).Bytes())
	if len(inviteFrames) != 1 || inviteFrames[0].opcode != protocol.OpM2C_InviteList {
		t.Fatalf("invite frames = %+v", inviteFrames)
	}
	var invite protocol.M2C_InviteList
	if err := proto.Unmarshal(inviteFrames[0].body, &invite); err != nil {
		t.Fatal(err)
	}
	if invite.TimeOut != 30000 {
		t.Fatalf("invite timeout = %d, want client milliseconds 30000", invite.TimeOut)
	}
	member.conn = &recordingConn{}

	handled := server.onHandleTeam(member, &protocol.C2M_HandleTeam{
		RpcId: 3, IsRequest: false, HandleInfo: &protocol.HandleInfo{Id: 101, Bool: true},
	}).(*protocol.M2C_HandleTeam)
	if handled.Message != "" || leader.session.teamID == 0 || leader.session.teamID != member.session.teamID {
		t.Fatalf("team accept failed: response=%+v ids=%d/%d", handled, leader.session.teamID, member.session.teamID)
	}
	for _, recipient := range []*channel{leader, member} {
		frames := decodeRecordedFrames(t, recipient.conn.(*recordingConn).Bytes())
		var snapshots []*protocol.M2C_TeamMember
		for _, frame := range frames {
			if frame.opcode != protocol.OpM2C_TeamMember {
				continue
			}
			message := &protocol.M2C_TeamMember{}
			if err := proto.Unmarshal(frame.body, message); err != nil {
				t.Fatal(err)
			}
			snapshots = append(snapshots, message)
		}
		if len(snapshots) != 1 || snapshots[0].LeaderId != 101 ||
			!reflect.DeepEqual(snapshots[0].UnitIds, []int64{101, 202}) {
			t.Fatalf("recipient %d team snapshots = %+v", recipient.session.playerID, snapshots)
		}
	}
}

func TestLeaderMapChangeCarriesOnlineTeamMembers(t *testing.T) {
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

	leaderSession := newSession()
	leaderSession.playerID, leaderSession.mapID, leaderSession.teamID = 101, 10004, 1
	memberSession := newSession()
	memberSession.playerID, memberSession.mapID, memberSession.teamID = 202, 10004, 1
	leader := &channel{id: 1, conn: &recordingConn{}, session: leaderSession}
	member := &channel{id: 2, conn: &recordingConn{}, session: memberSession}
	server := &Server{conns: map[int64]*channel{leader.id: leader, member.id: member}}
	teamMu.Lock()
	teams[1] = &teamState{LeaderId: leaderSession.playerID, Members: []int64{leaderSession.playerID, memberSession.playerID}}
	teamMu.Unlock()

	server.changeMapForTeamLeader(leader, 1000601, -1.8, -0.84)
	if leaderSession.mapID != 1000601 || memberSession.mapID != 1000601 {
		t.Fatalf("party map ids = %d/%d, want both 1000601", leaderSession.mapID, memberSession.mapID)
	}
	for _, recipient := range []*channel{leader, member} {
		frames := decodeRecordedFrames(t, recipient.conn.(*recordingConn).Bytes())
		seen := false
		for _, frame := range frames {
			if frame.opcode != protocol.OpM2C_ChangeMap {
				continue
			}
			var message protocol.M2C_ChangeMap
			if err := proto.Unmarshal(frame.body, &message); err != nil {
				t.Fatal(err)
			}
			seen = message.MapId == 1000601
		}
		if !seen {
			t.Fatalf("recipient %d missing team map change", recipient.session.playerID)
		}
	}
}

func TestStateRebackRestoresOnlyReadySameSceneTeamUnits(t *testing.T) {
	teamMu.Lock()
	oldTeams, oldSeq := teams, teamSeq
	teams, teamSeq = make(map[int64]*teamState), 1
	teamMu.Unlock()
	t.Cleanup(func() {
		teamMu.Lock()
		teams, teamSeq = oldTeams, oldSeq
		teamMu.Unlock()
	})

	makePlayer := func(id int64, mapID int32) *channel {
		ss := newSession()
		ss.playerID, ss.mapID = id, mapID
		return &channel{id: id, conn: &recordingConn{}, session: ss}
	}
	leader := makePlayer(101, 10004)
	member := makePlayer(202, 1000401) // legacy id for the same visible city scene
	server := &Server{conns: map[int64]*channel{101: leader, 202: member}}
	teamMu.Lock()
	teams[1] = &teamState{LeaderId: 101, Members: []int64{101, 202}}
	teamMu.Unlock()
	leader.session.teamID, member.session.teamID = 1, 1

	server.onGetStateReback(leader, &protocol.C2M_GetStateReback{ActorId: 101})
	for _, recipient := range []*channel{leader, member} {
		frames := decodeRecordedFrames(t, recipient.conn.(*recordingConn).Bytes())
		if len(frames) != 1 || frames[0].opcode != protocol.OpM2C_TeamMember {
			t.Fatalf("recipient %d state-reback frames = %+v", recipient.session.playerID, frames)
		}
		var snapshot protocol.M2C_TeamMember
		if err := proto.Unmarshal(frames[0].body, &snapshot); err != nil {
			t.Fatal(err)
		}
		if snapshot.LeaderId != 101 || !reflect.DeepEqual(snapshot.UnitIds, []int64{101, 202}) {
			t.Fatalf("recipient %d same-scene snapshot = %+v", recipient.session.playerID, &snapshot)
		}
	}

	// A party member in another scene must not be advertised until its Unit is
	// present locally; this is the client-side null-reference guard.
	leader.conn = &recordingConn{}
	member.conn = &recordingConn{}
	member.session.mapID = 10005
	server.onGetStateReback(leader, &protocol.C2M_GetStateReback{ActorId: 101})
	var snapshot protocol.M2C_TeamMember
	frames := decodeRecordedFrames(t, leader.conn.(*recordingConn).Bytes())
	if len(frames) != 1 || proto.Unmarshal(frames[0].body, &snapshot) != nil {
		t.Fatalf("cross-scene state-reback frames = %+v", frames)
	}
	if snapshot.LeaderId != 101 || !reflect.DeepEqual(snapshot.UnitIds, []int64{101}) {
		t.Fatalf("cross-scene snapshot = %+v", &snapshot)
	}
}
