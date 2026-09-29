package main

import (
	"fmt"
	"testing"
	"time"

	"mhqserver/internal/operationsconfig"
	"mhqserver/protocol"
)

func TestDailyDungeonQuotasResetFromGameplayConfig(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.Dungeon.TimezoneOffsetHours = 8
		config.Dungeon.SpaceTravelDailyAttempts = 50
		config.Dungeon.DeathTowerDailyAttempts = 10
		config.Dungeon.FamilyBossDailyKeys = 2
	})
	ss := newSession()
	ss.signin = &signinState{}
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	if !ss.refreshDailyDungeonQuotas(now) {
		t.Fatal("first refresh did not initialize daily quotas")
	}
	if got := ss.signin; got.DungeonQuotaDay != "20260820" || got.SpaceTravelRemaining != 50 ||
		got.DeathTowerRemaining != 10 || got.FamilyBossKeys != 2 {
		t.Fatalf("initialized quotas = %+v", got)
	}

	ss.signin.SpaceTravelRemaining = 7
	ss.signin.DeathTowerRemaining = 3
	ss.signin.FamilyBossKeys = 1
	if ss.refreshDailyDungeonQuotas(now.Add(time.Hour)) {
		t.Fatal("same operating day refilled spent quotas")
	}
	if got := ss.signin; got.SpaceTravelRemaining != 7 || got.DeathTowerRemaining != 3 || got.FamilyBossKeys != 1 {
		t.Fatalf("same-day quotas changed = %+v", got)
	}
}

func TestDailyDungeonQuotaHotReloadAppliesOnNextDay(t *testing.T) {
	ss := newSession()
	ss.signin = &signinState{
		DungeonQuotaDay: "20260820", SpaceTravelRemaining: 1,
		DeathTowerRemaining: 2, FamilyBossKeys: 0,
	}
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.Dungeon.SpaceTravelDailyAttempts = 60
		config.Dungeon.DeathTowerDailyAttempts = 12
		config.Dungeon.FamilyBossDailyKeys = 4
	})
	nextDay := time.Date(2026, 8, 21, 0, 1, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	if !ss.refreshDailyDungeonQuotas(nextDay) {
		t.Fatal("next operating day did not reset quotas")
	}
	if got := ss.signin; got.DungeonQuotaDay != "20260821" || got.SpaceTravelRemaining != 60 ||
		got.DeathTowerRemaining != 12 || got.FamilyBossKeys != 4 {
		t.Fatalf("next-day quotas = %+v", got)
	}
}

func setDungeonQuotaForTest(ss *session, spaceTravel, deathTower, familyBoss int32) {
	ss.signin = &signinState{
		DungeonQuotaDay:      gameplayDungeonDay(time.Now()),
		SpaceTravelRemaining: spaceTravel,
		DeathTowerRemaining:  deathTower,
		FamilyBossKeys:       familyBoss,
	}
}

func fiveMemberDungeonTeam(t *testing.T, mapID int32, familyID int64) (*Server, []*channel) {
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
	members := make([]*channel, 0, maxTeamMembers)
	ids := make([]int64, 0, maxTeamMembers)
	for index := 0; index < maxTeamMembers; index++ {
		ss := newSession()
		ss.state = sessInGame
		ss.playerID = int64(9200 + index)
		ss.name = fmt.Sprintf("member-%d", index+1)
		ss.jobID, ss.level, ss.energy = 1, 15000, 100
		ss.mapID, ss.teamID, ss.familyID = mapID, 1, familyID
		ss.hp, ss.mp = ss.playerMaxHp(), ss.playerMaxMp()
		ss.worn = make(map[int32]*bagItem)
		ss.tasks = make(map[int32]int32)
		ss.killCount = make(map[int32]int32)
		setDungeonQuotaForTest(ss, 50, 10, 2)
		ch := &channel{id: ss.playerID, conn: &recordingConn{}, session: ss}
		server.conns[ch.id] = ch
		members = append(members, ch)
		ids = append(ids, ss.playerID)
	}
	teamMu.Lock()
	teams[1] = &teamState{LeaderId: ids[0], Members: append([]int64(nil), ids...)}
	teamMu.Unlock()
	return server, members
}

func TestFiveMemberSpaceTravelChargesEveryParticipantOnce(t *testing.T) {
	loadOnlineTablesForTest(t)
	server, members := fiveMemberDungeonTeam(t, 1003901, 0)
	started, count := server.startSceneBattleByMap(members[0], 1003901)
	if !started || count == 0 {
		t.Fatalf("five-member space travel start=(%v,%d)", started, count)
	}
	party := members[0].session.battle.party
	if party == nil || len(party.memberIDs) != maxTeamMembers {
		t.Fatalf("space travel party=%+v, want %d members", party, maxTeamMembers)
	}
	for _, member := range members {
		if member.session.battle == nil || member.session.battle.party != party {
			t.Fatalf("player %d did not enter shared space travel battle", member.session.playerID)
		}
		if got := member.session.signin.SpaceTravelRemaining; got != 49 {
			t.Fatalf("player %d space travel remaining=%d, want 49", member.session.playerID, got)
		}
	}
}

func TestSoloSpaceTravelChargesOneAttempt(t *testing.T) {
	loadOnlineTablesForTest(t)
	server, members := fiveMemberDungeonTeam(t, 1003901, 0)
	member := members[0]
	member.session.teamID = 0

	started, count := server.startSceneBattleByMap(member, 1003901)
	if !started || count == 0 || member.session.battle == nil || member.session.battle.party != nil {
		t.Fatalf("solo space travel start=(%v,%d) battle=%+v", started, count, member.session.battle)
	}
	if got := member.session.signin.SpaceTravelRemaining; got != 49 {
		t.Fatalf("solo space travel remaining=%d, want 49", got)
	}
}

func TestFiveMemberSpaceTravelRejectsEveryoneWhenOneMemberHasNoAttempts(t *testing.T) {
	loadOnlineTablesForTest(t)
	server, members := fiveMemberDungeonTeam(t, 1003901, 0)
	members[4].session.signin.SpaceTravelRemaining = 0

	response := server.onClickMapUnit(members[0], &protocol.C2M_ClickMapUnit{RpcId: 1}).(*protocol.M2C_ClickMapUnit)
	if response.Message != "队员member-5时空旅行战斗次数不足" {
		t.Fatalf("space travel rejection=%q", response.Message)
	}
	for index, member := range members {
		if member.session.battle != nil {
			t.Fatalf("player %d entered rejected space travel battle", member.session.playerID)
		}
		want := int32(50)
		if index == 4 {
			want = 0
		}
		if got := member.session.signin.SpaceTravelRemaining; got != want {
			t.Fatalf("player %d remaining=%d, want %d", member.session.playerID, got, want)
		}
	}
}

func TestSpaceTravelBattleCreationFailureDoesNotChargeAttempt(t *testing.T) {
	loadOnlineTablesForTest(t)
	server, members := fiveMemberDungeonTeam(t, 1003901, 0)
	started, _ := server.finishStartBattleWithPresentation(members[0], 1001, nil, 1003901,
		battlePresentation{kind: presentationMainStory})
	if started {
		t.Fatal("space travel battle unexpectedly started without monsters")
	}
	for _, member := range members {
		if got := member.session.signin.SpaceTravelRemaining; got != 50 {
			t.Fatalf("player %d charged after creation failure: %d", member.session.playerID, got)
		}
	}
}

func resetFamilyBossForQuotaTest(t *testing.T, familyID int64) {
	t.Helper()
	familyBossStates.mu.Lock()
	delete(familyBossStates.m, familyID)
	familyBossStates.mu.Unlock()
	t.Cleanup(func() {
		familyBossStates.mu.Lock()
		delete(familyBossStates.m, familyID)
		familyBossStates.mu.Unlock()
	})
}

func TestFiveMemberFamilyBossChargesEveryParticipantKey(t *testing.T) {
	loadOnlineTablesForTest(t)
	const familyID int64 = 87654001
	resetFamilyBossForQuotaTest(t, familyID)
	server, members := fiveMemberDungeonTeam(t, 10004, familyID)

	response := server.onStartFamilyBossFight(members[0], &protocol.C2M_StartFamilyBossFight{
		RpcId: 1, BossId: 1,
	}).(*protocol.M2C_StartFamilyBossFight)
	if response.Message != "" {
		t.Fatalf("five-member family boss response=%q", response.Message)
	}
	party := members[0].session.battle.party
	if party == nil || len(party.memberIDs) != maxTeamMembers {
		t.Fatalf("family boss party=%+v, want %d members", party, maxTeamMembers)
	}
	for _, member := range members {
		if member.session.battle == nil || member.session.battle.party != party {
			t.Fatalf("player %d did not enter shared family boss battle", member.session.playerID)
		}
		if got := member.session.signin.FamilyBossKeys; got != 1 {
			t.Fatalf("player %d family boss keys=%d, want 1", member.session.playerID, got)
		}
	}
}

func TestFiveMemberFamilyBossRejectsEveryoneWhenOneMemberHasNoKey(t *testing.T) {
	loadOnlineTablesForTest(t)
	const familyID int64 = 87654002
	resetFamilyBossForQuotaTest(t, familyID)
	server, members := fiveMemberDungeonTeam(t, 10004, familyID)
	members[4].session.signin.FamilyBossKeys = 0

	response := server.onStartFamilyBossFight(members[0], &protocol.C2M_StartFamilyBossFight{
		RpcId: 1, BossId: 1,
	}).(*protocol.M2C_StartFamilyBossFight)
	if response.Message != "队员member-5家族 Boss 钥匙不足" {
		t.Fatalf("family boss rejection=%q", response.Message)
	}
	for index, member := range members {
		if member.session.battle != nil {
			t.Fatalf("player %d entered rejected family boss battle", member.session.playerID)
		}
		want := int32(2)
		if index == 4 {
			want = 0
		}
		if got := member.session.signin.FamilyBossKeys; got != want {
			t.Fatalf("player %d keys=%d, want %d", member.session.playerID, got, want)
		}
	}
}

func TestFamilyBossBattleCreationFailureDoesNotChargeKey(t *testing.T) {
	loadOnlineTablesForTest(t)
	server, members := fiveMemberDungeonTeam(t, 10004, 87654003)
	started, _ := server.finishStartBattleWithPresentation(members[0], 1001, nil, -1,
		battlePresentation{kind: presentationFamilyBoss, familyBossID: 1})
	if started {
		t.Fatal("family boss unexpectedly started without a monster")
	}
	for _, member := range members {
		if got := member.session.signin.FamilyBossKeys; got != 2 {
			t.Fatalf("player %d charged after family boss creation failure: %d", member.session.playerID, got)
		}
	}
}
