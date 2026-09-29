package main

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func TestLauncherTeamRequestUsesStrictBoundedProtobuf(t *testing.T) {
	credential := func(account string) []byte {
		var body []byte
		body = protowire.AppendTag(body, 1, protowire.BytesType)
		body = protowire.AppendString(body, account)
		body = protowire.AppendTag(body, 2, protowire.BytesType)
		return protowire.AppendString(body, "123456")
	}
	var valid []byte
	valid = protowire.AppendTag(valid, 1, protowire.VarintType)
	valid = protowire.AppendVarint(valid, 1)
	valid = protowire.AppendTag(valid, 2, protowire.BytesType)
	valid = protowire.AppendString(valid, "leader")
	for _, account := range []string{"leader", "member"} {
		valid = protowire.AppendTag(valid, 3, protowire.BytesType)
		valid = protowire.AppendBytes(valid, credential(account))
	}
	request, err := decodeLauncherTeamPlanRequest(valid)
	if err != nil || request.LeaderAccount != "leader" || len(request.Accounts) != 2 {
		t.Fatalf("valid launcher request = %+v, err=%v", request, err)
	}
	for _, body := range [][]byte{
		nil,
		protowire.AppendVarint(protowire.AppendTag(nil, 4, protowire.VarintType), 1),
		append(append([]byte(nil), valid...), 0),
		[]byte(strings.Repeat("x", launcherTeamMaxRequestBytes+1)),
	} {
		if _, err := decodeLauncherTeamPlanRequest(body); err == nil {
			t.Fatalf("invalid launcher request accepted: len=%d", len(body))
		}
	}
}

func TestLauncherTeamPlanRejectsWrongPassword(t *testing.T) {
	store, err := OpenStore(mysqlTestDSN(t, "launcher-team-auth"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for index, account := range []string{"launcherleader", "launchermember"} {
		accountID, createErr := store.CreateAccount(account, "Pass123")
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, createErr = store.CreatePlayer(accountID, account, int32(index+1), 0); createErr != nil {
			t.Fatal(createErr)
		}
	}
	server := &Server{store: store, conns: make(map[int64]*channel)}
	response := server.submitLauncherTeamPlan(&launcherTeamPlanRequest{
		Version:       launcherTeamRequestVersion,
		LeaderAccount: "launcherleader",
		Accounts: []launcherTeamCredential{
			{Account: "launcherleader", Password: "Pass123"},
			{Account: "launchermember", Password: "wrong-password"},
		},
	}, time.Now())
	if response.OK || response.Message != "账号或密码错误" || len(server.launcherTeamPlans) != 0 {
		t.Fatalf("wrong-password response=%+v plans=%v", response, server.launcherTeamPlans)
	}
}

func TestLauncherTeamPlanReplacesSelectedPlayersOldTeams(t *testing.T) {
	teamMu.Lock()
	oldTeams, oldSeq := teams, teamSeq
	teams, teamSeq = make(map[int64]*teamState), 10
	teamMu.Unlock()
	t.Cleanup(func() {
		teamMu.Lock()
		teams, teamSeq = oldTeams, oldSeq
		teamMu.Unlock()
	})

	makePlayer := func(id int64) *channel {
		ss := newSession()
		ss.state, ss.playerID, ss.mapID = sessInGame, id, 10004
		ss.x, ss.y = -12, -1
		return &channel{id: id, conn: &recordingConn{}, session: ss}
	}
	leader := makePlayer(101)
	member := makePlayer(202)
	oldTeammate := makePlayer(303)
	server := &Server{conns: map[int64]*channel{
		leader.id: leader, member.id: member, oldTeammate.id: oldTeammate,
	}}

	teamMu.Lock()
	teams[5] = &teamState{LeaderId: member.session.playerID, Members: []int64{202, 303}}
	member.session.teamID, oldTeammate.session.teamID = 5, 5
	teamMu.Unlock()

	plan := &launcherTeamPlan{ID: 7, LeaderID: 101, Members: []int64{101, 202}}
	if !server.applyLauncherTeamPlan(plan) {
		t.Fatal("ready launcher team plan was not applied")
	}
	if leader.session.teamID == 0 || leader.session.teamID != member.session.teamID {
		t.Fatalf("new team ids = %d/%d", leader.session.teamID, member.session.teamID)
	}

	teamMu.Lock()
	_, newTeam := server.teamEntryOf(101)
	_, remainingTeam := server.teamEntryOf(303)
	teamMu.Unlock()
	if newTeam == nil || newTeam.LeaderId != 101 || !equalPlayerIDs(newTeam.Members, []int64{101, 202}) {
		t.Fatalf("new launcher team = %+v", newTeam)
	}
	if remainingTeam == nil || remainingTeam.LeaderId != 303 || !equalPlayerIDs(remainingTeam.Members, []int64{303}) {
		t.Fatalf("remaining old team = %+v", remainingTeam)
	}

	for _, recipient := range []*channel{leader, member} {
		frames := decodeRecordedFrames(t, recipient.conn.(*recordingConn).Bytes())
		foundTeam, foundTip := false, false
		for _, frame := range frames {
			switch frame.opcode {
			case protocol.OpM2C_TeamMember:
				var snapshot protocol.M2C_TeamMember
				if err := proto.Unmarshal(frame.body, &snapshot); err != nil {
					t.Fatal(err)
				}
				foundTeam = snapshot.LeaderId == 101 && equalPlayerIDs(snapshot.UnitIds, []int64{101, 202})
			case protocol.OpM2C_SendTip:
				foundTip = true
			}
		}
		if !foundTeam || !foundTip {
			t.Fatalf("player %d frames team=%t tip=%t", recipient.session.playerID, foundTeam, foundTip)
		}
	}
}

func TestLauncherTeamPlanWaitsForSafeGameplayState(t *testing.T) {
	teamMu.Lock()
	oldTeams, oldSeq := teams, teamSeq
	teams, teamSeq = make(map[int64]*teamState), 20
	teamMu.Unlock()
	t.Cleanup(func() {
		teamMu.Lock()
		teams, teamSeq = oldTeams, oldSeq
		teamMu.Unlock()
	})

	makePlayer := func(id int64) *channel {
		ss := newSession()
		ss.state, ss.playerID, ss.mapID = sessInGame, id, 10004
		return &channel{id: id, conn: &recordingConn{}, session: ss}
	}
	leader, member := makePlayer(401), makePlayer(402)
	server := &Server{conns: map[int64]*channel{401: leader, 402: member}}
	plan := &launcherTeamPlan{ID: 8, LeaderID: 401, Members: []int64{401, 402}}

	member.session.battle = &battleState{}
	if server.applyLauncherTeamPlan(plan) {
		t.Fatal("launcher team applied while a selected member was fighting")
	}
	member.session.battle = nil
	member.session.mapID = 1005001
	if server.applyLauncherTeamPlan(plan) {
		t.Fatal("launcher team applied while a selected member was in a private activity")
	}
	teamMu.Lock()
	_, existing := server.teamEntryOf(401)
	teamMu.Unlock()
	if existing != nil {
		t.Fatalf("unsafe launcher plan changed team state: %+v", existing)
	}
}

func TestLauncherTeamPlanExpiresWithoutChangingTeams(t *testing.T) {
	server := &Server{
		conns: make(map[int64]*channel),
		launcherTeamPlans: map[uint64]*launcherTeamPlan{
			1: {ID: 1, LeaderID: 501, Members: []int64{501, 502}, ExpiresAt: time.Now().Add(-time.Second)},
		},
	}
	server.tryApplyLauncherTeamPlans(time.Now())
	if len(server.launcherTeamPlans) != 0 {
		t.Fatalf("expired launcher plans = %+v", server.launcherTeamPlans)
	}
}

func equalPlayerIDs(left, right []int64) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
