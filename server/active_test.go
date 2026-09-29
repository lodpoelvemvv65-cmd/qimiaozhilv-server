package main

import (
	"testing"
	"time"

	"mhqserver/protocol"
)

func activityTestChannel(t *testing.T, id int64) (*Server, *session, *channel) {
	t.Helper()
	ss := newSession()
	ss.playerID, ss.name = id, "activity"
	ss.jobID, ss.level, ss.mapID = 1, 15000, 10004
	ss.energy = 100
	ss.hp, ss.mp = ss.playerMaxHp(), ss.playerMaxMp()
	ss.bag = make(map[int32]*bagItem)
	ss.worn = make(map[int32]*bagItem)
	ss.skills = map[int32]int32{100001: 1}
	ss.skillOrder = []int32{100001}
	ss.tasks = make(map[int32]int32)
	ss.killCount = make(map[int32]int32)
	ss.signin = &signinState{}
	ch := &channel{id: id, conn: &recordingConn{}, session: ss}
	return &Server{conns: map[int64]*channel{id: ch}}, ss, ch
}

func finishActivityTestStage(t *testing.T, server *Server, ch *channel) {
	t.Helper()
	ch.session.battleMu.Lock()
	battle := ch.session.battle
	if battle == nil || battle.activity == nil {
		ch.session.battleMu.Unlock()
		t.Fatal("activity battle missing")
	}
	for _, monster := range battle.monsters {
		monster.hp, monster.alive = 0, false
	}
	server.emitVictory(ch, battle)
	ch.session.battleMu.Unlock()
}

func TestDailyStarSoulActivityRunsAllOnlineStagesAndChargesOnce(t *testing.T) {
	loadOnlineTablesForTest(t)
	server, ss, ch := activityTestChannel(t, 7001)
	started, message := server.startActivityStage(ch, &activityBattle{
		ActiveID: 10001, Method: activeStarSoul, CopyType: 1, Difficulty: 1, Stage: 1,
	}, false)
	if !started || message != "" {
		t.Fatalf("star soul stage 1 start=%v message=%q", started, message)
	}
	if ss.battle == nil || ss.battle.battleType != 23 || ss.battle.activity.Stage != 1 || len(ss.battle.monsters) != 10 {
		t.Fatalf("stage 1 battle=%+v", ss.battle)
	}
	if ss.mapID != 1004501 || ss.battle.mapID != 1004501 {
		t.Fatalf("stage 1 map session=%d battle=%d, want online star-soul map 1004501", ss.mapID, ss.battle.mapID)
	}
	if ss.energy != 90 {
		t.Fatalf("stage 1 energy=%d, want one 10-point charge", ss.energy)
	}

	finishActivityTestStage(t, server, ch)
	deadline := time.Now().Add(time.Second)
	for (ss.battle == nil || ss.battle.activity == nil || ss.battle.activity.Stage != 2) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if ss.battle == nil || ss.battle.activity.Stage != 2 || len(ss.battle.monsters) != 10 {
		t.Fatalf("stage 2 did not start: battle=%+v", ss.battle)
	}
	if ss.mapID != 1004502 {
		t.Fatalf("stage 2 map=%d, want 1004502", ss.mapID)
	}
	if ss.energy != 90 {
		t.Fatalf("stage 2 charged energy again: %d", ss.energy)
	}
	finishActivityTestStage(t, server, ch)
	deadline = time.Now().Add(time.Second)
	for (ss.battle == nil || ss.battle.activity == nil || ss.battle.activity.Stage != 3) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if ss.battle == nil || ss.battle.activity.Stage != 3 || ss.battle.monsters[0].monsterID != 73001 {
		t.Fatalf("stage 3 did not use online roster: battle=%+v", ss.battle)
	}
	if ss.mapID != 1004503 {
		t.Fatalf("stage 3 map=%d, want 1004503", ss.mapID)
	}
	finishActivityTestStage(t, server, ch)
	deadline = time.Now().Add(time.Second)
	for ss.battle != nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if ss.battle != nil {
		t.Fatal("star soul activity did not finish after stage 3")
	}
	deadline = time.Now().Add(time.Second)
	for ss.mapID != 10004 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if ss.mapID != 10004 {
		t.Fatalf("star soul activity returned to map %d, want original map 10004", ss.mapID)
	}
	if count := len(ss.ensureStarSoulBag().Items); count < 6 || count > 15 {
		t.Fatalf("star soul rewards=%d, want online stage ranges totaling 6..15", count)
	}
	if len(decodeRecordedFrames(t, ch.conn.(*recordingConn).Bytes())) == 0 {
		t.Fatal("star soul activity emitted no protocol frames")
	}
}

func TestFiveMemberStarSoulActivityAdvancesTogetherAndChargesOnce(t *testing.T) {
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
	memberIDs := make([]int64, 0, maxTeamMembers)
	members := make([]*channel, 0, maxTeamMembers)
	for index := 0; index < maxTeamMembers; index++ {
		_, ss, ch := activityTestChannel(t, int64(7100+index))
		ss.teamID = 1
		server.conns[ch.id] = ch
		memberIDs = append(memberIDs, ss.playerID)
		members = append(members, ch)
	}
	teamMu.Lock()
	teams[1] = &teamState{LeaderId: memberIDs[0], Members: append([]int64(nil), memberIDs...)}
	teamMu.Unlock()

	started, message := server.startActivityStage(members[0], &activityBattle{
		ActiveID: 10001, Method: activeStarSoul, CopyType: 1, Difficulty: 1, Stage: 1,
	}, false)
	if !started || message != "" {
		t.Fatalf("five-member activity start=%v message=%q", started, message)
	}

	for stage := int32(1); stage <= 3; stage++ {
		party := members[0].session.battle.party
		if party == nil || len(party.memberIDs) != maxTeamMembers {
			t.Fatalf("stage %d party=%+v", stage, party)
		}
		for _, member := range members {
			battle := member.session.battle
			if battle == nil || battle.activity == nil || battle.activity.Stage != stage || battle.party != party {
				t.Fatalf("stage %d player %d battle=%+v", stage, member.session.playerID, battle)
			}
			if member.session.energy != 90 {
				t.Fatalf("stage %d player %d energy=%d, want one 10-point charge",
					stage, member.session.playerID, member.session.energy)
			}
		}

		party.mu.Lock()
		for _, monster := range members[0].session.battle.monsters {
			monster.hp, monster.alive = 0, false
		}
		party.settled = true
		for _, battle := range party.members {
			battle.ended = true
		}
		party.mu.Unlock()
		server.finishPartyVictory(party)
	}

	for _, member := range members {
		if member.session.battle != nil {
			t.Fatalf("player %d activity did not finish", member.session.playerID)
		}
		if count := len(member.session.ensureStarSoulBag().Items); count < 6 || count > 15 {
			t.Fatalf("player %d star soul rewards=%d, want 6..15", member.session.playerID, count)
		}
	}
}

func TestDailyActivityEntryUsesOnlineDeathRoadAndWorldBossRows(t *testing.T) {
	loadOnlineTablesForTest(t)
	server, ss, ch := activityTestChannel(t, 7002)
	death := server.onStartActive(ch, &protocol.C2M_StartActive{RpcId: 1, ItemId: 10023}).(*protocol.M2C_StartActive)
	if death.Message != "" || ss.battle == nil || ss.battle.activity.Method != activeJourneyOfDeath ||
		ss.battle.activity.Difficulty != 1 || ss.battle.monsters[0].monsterID != 90001 {
		t.Fatalf("death road start=%+v battle=%+v", death, ss.battle)
	}
	finishActivityTestStage(t, server, ch)
	deadline := time.Now().Add(time.Second)
	for (ss.battle == nil || ss.battle.activity == nil || ss.battle.activity.Stage != 2) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if ss.battle == nil || ss.battle.monsters[0].monsterID != 90002 {
		t.Fatalf("death road stage 2 roster=%d battle=%+v", ss.battle.monsters[0].monsterID, ss.battle)
	}
	finishActivityTestStage(t, server, ch)
	deadline = time.Now().Add(time.Second)
	for ss.battle != nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	server, ss, ch = activityTestChannel(t, 7003)
	world := server.onStartActive(ch, &protocol.C2M_StartActive{RpcId: 2, ItemId: 10022}).(*protocol.M2C_StartActive)
	if world.Message != "" || ss.battle == nil || ss.battle.battleType != 24 || len(ss.battle.monsters) != 3 {
		t.Fatalf("world boss start=%+v battle=%+v", world, ss.battle)
	}
	for _, monster := range ss.battle.monsters {
		if monster.monsterID != 81001 {
			t.Fatalf("world boss monster=%d, want 81001", monster.monsterID)
		}
	}
}

func TestDailyActivityRejectsDefeatedPlayerBeforeMapChangeOrEnergyCharge(t *testing.T) {
	loadOnlineTablesForTest(t)
	server, ss, ch := activityTestChannel(t, 7004)
	ss.hp = 0
	startMap, startX, startY, startEnergy := ss.mapID, ss.x, ss.y, ss.energy

	result := server.onStartActive(ch, &protocol.C2M_StartActive{RpcId: 3, ItemId: 10022}).(*protocol.M2C_StartActive)
	if result.Message != battleEntryHealthMessage {
		t.Fatalf("defeated activity message=%q, want %q", result.Message, battleEntryHealthMessage)
	}
	if ss.battle != nil {
		t.Fatalf("defeated player entered battle: %+v", ss.battle)
	}
	if ss.mapID != startMap || ss.x != startX || ss.y != startY {
		t.Fatalf("defeated activity changed map/position: got %d %.2f %.2f, want %d %.2f %.2f",
			ss.mapID, ss.x, ss.y, startMap, startX, startY)
	}
	if ss.energy != startEnergy {
		t.Fatalf("defeated activity charged energy: got %d, want %d", ss.energy, startEnergy)
	}
}
