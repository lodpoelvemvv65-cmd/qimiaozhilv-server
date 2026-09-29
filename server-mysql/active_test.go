package main

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func activityTestChannel(t *testing.T, id int64) (*Server, *session, *channel) {
	t.Helper()
	ss := newSession()
	ss.state = sessInGame
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

func finishPendingActivityScene(t *testing.T, server *Server, members ...*channel) {
	t.Helper()
	if len(members) == 0 {
		t.Fatal("activity scene startup requires at least one member")
	}
	deadline := time.Now().Add(time.Second)
	for server.pendingActivityForPlayer(members[0].session.playerID) == nil &&
		members[0].session.currentActivityScene() == nil && members[0].session.battle == nil &&
		time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if server.pendingActivityForPlayer(members[0].session.playerID) == nil {
		if members[0].session.currentActivityScene() == nil && members[0].session.battle == nil {
			t.Fatal("activity continuation did not start")
		}
		for _, member := range members {
			if _, complete := member.session.currentMapStartupVersion(); !complete {
				t.Fatalf("player %d activity scene did not complete synchronously", member.session.playerID)
			}
		}
		return
	}
	for server.pendingActivityForPlayer(members[0].session.playerID) == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	pending := server.pendingActivityForPlayer(members[0].session.playerID)
	if pending == nil {
		t.Fatal("pending activity scene was not registered")
	}
	for index, member := range members {
		seq := pending.mapSeqs[member.session.playerID]
		if !server.finishMapStartup(member, pending.targetMap, seq, false) {
			t.Fatalf("player %d activity scene startup failed", member.session.playerID)
		}
		if index < len(members)-1 {
			for _, waiting := range members {
				if waiting.session.battle != nil {
					t.Fatalf("player %d entered battle before every scene was ready", waiting.session.playerID)
				}
			}
		}
	}
	if server.pendingActivityForPlayer(members[0].session.playerID) != nil {
		t.Fatal("activity remained pending after every scene was ready")
	}
}

func clickActivityFieldMonster(t *testing.T, server *Server, members ...*channel) {
	t.Helper()
	if len(members) == 0 {
		t.Fatal("activity click requires at least one member")
	}
	state := members[0].session.currentActivityScene()
	if state == nil {
		t.Fatal("interactive activity scene is missing")
	}
	var origin *channel
	for _, member := range members {
		if member.session.playerID == state.originID {
			origin = member
		}
		if member.session.battle != nil {
			t.Fatalf("player %d entered activity battle before the display monster was clicked", member.session.playerID)
		}
	}
	if origin == nil {
		t.Fatalf("activity origin %d is not in test members", state.originID)
	}
	configID, mapType, _, _, ok := activityFieldMonster(state.activity)
	if !ok {
		t.Fatalf("activity stage has no display monster: %+v", state.activity)
	}
	unitID := int64(0)
	for id, gotConfigID := range origin.session.fieldMonsterConfigIDs {
		if gotConfigID == configID {
			unitID = id
			break
		}
	}
	if unitID == 0 {
		t.Fatalf("origin has no activity display monster config=%d", configID)
	}
	response := server.onClickMapUnit(origin, &protocol.C2M_ClickMapUnit{
		RpcId: 1, Id: unitID, Type: mapType,
	}).(*protocol.M2C_ClickMapUnit)
	if response.Message != "" {
		t.Fatalf("activity display monster click failed: %+v", response)
	}
	for _, member := range members {
		if member.session.battle == nil || member.session.battle.activity == nil {
			t.Fatalf("player %d did not enter activity battle", member.session.playerID)
		}
		if member.session.currentActivityScene() != nil || len(member.session.fieldMonsterIDs) != 0 {
			t.Fatalf("player %d retained activity scene monster after battle start", member.session.playerID)
		}
	}
}

func waitForActivityMapReady(t *testing.T, member *channel, afterSeq uint64, wantMap int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		seq, ready := member.session.currentMapStartupVersion()
		if seq > afterSeq && ready {
			if member.session.mapID != wantMap {
				t.Fatalf("player %d returned to map %d, want %d", member.session.playerID, member.session.mapID, wantMap)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	seq, ready := member.session.currentMapStartupVersion()
	t.Fatalf("player %d map return timed out: map=%d seq=%d ready=%v after=%d",
		member.session.playerID, member.session.mapID, seq, ready, afterSeq)
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
	if ss.battle != nil {
		t.Fatalf("stage 1 battle started before scene startup: %+v", ss.battle)
	}
	finishPendingActivityScene(t, server, ch)
	clickActivityFieldMonster(t, server, ch)
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
	finishPendingActivityScene(t, server, ch)
	clickActivityFieldMonster(t, server, ch)
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
	finishPendingActivityScene(t, server, ch)
	clickActivityFieldMonster(t, server, ch)
	if ss.battle == nil || ss.battle.activity.Stage != 3 || ss.battle.monsters[0].monsterID != 73001 {
		t.Fatalf("stage 3 did not use online roster: battle=%+v", ss.battle)
	}
	if ss.mapID != 1004503 {
		t.Fatalf("stage 3 map=%d, want 1004503", ss.mapID)
	}
	returnAfterSeq, _ := ss.currentMapSceneVersion()
	finishActivityTestStage(t, server, ch)
	deadline := time.Now().Add(time.Second)
	for ss.battle != nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if ss.battle != nil {
		t.Fatal("star soul activity did not finish after stage 3")
	}
	waitForActivityMapReady(t, ch, returnAfterSeq, 10004)
	if count := len(ss.ensureStarSoulBag().Items); count < 6 || count > 15 {
		t.Fatalf("star soul rewards=%d, want online stage ranges totaling 6..15", count)
	}
	if len(decodeRecordedFrames(t, ch.conn.(*recordingConn).Bytes())) == 0 {
		t.Fatal("star soul activity emitted no protocol frames")
	}
}

func TestStarSoulClosedMessageNamesScheduleAndTodaysLayer(t *testing.T) {
	loadOnlineTablesForTest(t)
	row := tables.activePerDay[10001]
	if row == nil {
		t.Fatal("online A star-soul activity row is missing")
	}
	monday := time.Date(2026, 8, 24, 12, 0, 0, 0, time.Local)
	if got := activityUnavailableMessage(row, monday); got != "" {
		t.Fatalf("open A star-soul activity was rejected: %q", got)
	}
	thursday := time.Date(2026, 8, 27, 12, 0, 0, 0, time.Local)
	want := "今天是星期四，当前开放D星魂副本；A星魂副本开放时间为星期一、星期六、星期日，请点击D层"
	if got := activityUnavailableMessage(row, thursday); got != want {
		t.Fatalf("closed A star-soul message=%q, want %q", got, want)
	}
}

func TestClosedStarSoulUsesNativeTipWithoutEnteringScene(t *testing.T) {
	loadOnlineTablesForTest(t)
	server, ss, ch := activityTestChannel(t, 7010)
	thursday := time.Date(2026, 8, 27, 12, 0, 0, 0, time.Local)
	response := server.onStartActiveAt(ch, &protocol.C2M_StartActive{
		RpcId: 9, ItemId: 10001,
	}, thursday).(*protocol.M2C_StartActive)
	if response.Message != "" || ss.mapID != 10004 || ss.battle != nil || server.pendingActivityForPlayer(ss.playerID) != nil {
		t.Fatalf("closed star soul changed state: response=%+v map=%d battle=%+v", response, ss.mapID, ss.battle)
	}
	frames := decodeRecordedFrames(t, ch.conn.(*recordingConn).Bytes())
	if len(frames) != 1 || frames[0].opcode != protocol.OpM2C_SendTip {
		t.Fatalf("closed star soul opcodes=%v, want SendTip", recordedOpcodes(t, ch.conn.(*recordingConn).Bytes()))
	}
	var tip protocol.M2C_SendTip
	if err := proto.Unmarshal(frames[0].body, &tip); err != nil {
		t.Fatal(err)
	}
	want := "今天是星期四，当前开放D星魂副本；A星魂副本开放时间为星期一、星期六、星期日，请点击D层"
	if tip.Message != want || tip.ActorId != ss.playerID {
		t.Fatalf("closed star soul tip=%+v, want message=%q actor=%d", &tip, want, ss.playerID)
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
	entryX, entryY := members[0].session.x, members[0].session.y
	for _, member := range members {
		if member.session.battle != nil {
			t.Fatalf("player %d entered activity battle before scene startup", member.session.playerID)
		}
		if member.session.x != entryX || member.session.y != entryY {
			t.Fatalf("player %d activity entry position=(%f,%f), want shared (%f,%f)",
				member.session.playerID, member.session.x, member.session.y, entryX, entryY)
		}
	}
	finishPendingActivityScene(t, server, members...)
	for _, member := range members {
		teamIndex, monsterIndex, startupIndex, battleIndex := -1, -1, -1, -1
		for index, frame := range decodeRecordedFrames(t, member.conn.(*recordingConn).Bytes()) {
			switch frame.opcode {
			case protocol.OpM2C_TeamMember:
				teamIndex = index
			case protocol.OpM2C_CreateMapMonster:
				monsterIndex = index
			case protocol.OpM2C_StartupTransPoint:
				startupIndex = index
			case protocol.OpM2C_MainStoryMonsterInfo:
				battleIndex = index
			}
		}
		if teamIndex < 0 || monsterIndex <= teamIndex || startupIndex >= 0 || battleIndex >= 0 {
			t.Fatalf("player %d before click team/displayMonster/startup/battle indexes=%d/%d/%d/%d",
				member.session.playerID, teamIndex, monsterIndex, startupIndex, battleIndex)
		}
	}
	clickActivityFieldMonster(t, server, members...)
	for _, member := range members {
		enterCount, battleIndex := 0, -1
		for index, frame := range decodeRecordedFrames(t, member.conn.(*recordingConn).Bytes()) {
			switch frame.opcode {
			case protocol.OpM2C_EnterMap:
				if battleIndex < 0 {
					enterCount++
				}
			case protocol.OpM2C_MainStoryMonsterInfo:
				if battleIndex < 0 {
					battleIndex = index
				}
			}
		}
		if battleIndex < 0 || enterCount != maxTeamMembers-1 {
			t.Fatalf("player %d startup order EnterMap=%d battleIndex=%d", member.session.playerID, enterCount, battleIndex)
		}
	}

	for stage := int32(1); stage <= 3; stage++ {
		party := members[0].session.battle.party
		if party == nil || len(party.memberIDs) != maxTeamMembers {
			t.Fatalf("stage %d party=%+v", stage, party)
		}
		if party.monsterReadyAt.IsZero() {
			t.Fatalf("stage %d party has no opening deadline", stage)
		}
		for _, member := range members {
			battle := member.session.battle
			if battle == nil || battle.activity == nil || battle.activity.Stage != stage || battle.party != party {
				t.Fatalf("stage %d player %d battle=%+v", stage, member.session.playerID, battle)
			}
			if !battle.monsterReadyAt.Equal(party.monsterReadyAt) ||
				!member.session.battleActionReadyAt.Equal(party.monsterReadyAt) ||
				!member.session.autoBattleNextCastAt.Equal(party.monsterReadyAt) {
				t.Fatalf("stage %d player %d opening deadlines monster=%s party=%s player=%s auto=%s",
					stage, member.session.playerID, battle.monsterReadyAt, party.monsterReadyAt,
					member.session.battleActionReadyAt, member.session.autoBattleNextCastAt)
			}
			if member.session.energy != 90 {
				t.Fatalf("stage %d player %d energy=%d, want one 10-point charge",
					stage, member.session.playerID, member.session.energy)
			}
		}

		returnSeqs := make(map[int64]uint64, len(members))
		if stage == 3 {
			for _, member := range members {
				returnSeqs[member.session.playerID], _ = member.session.currentMapSceneVersion()
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
		if stage < 3 {
			finishPendingActivityScene(t, server, members...)
			clickActivityFieldMonster(t, server, members...)
		} else {
			for _, member := range members {
				waitForActivityMapReady(t, member, returnSeqs[member.session.playerID], 10004)
			}
		}
	}

	for _, member := range members {
		if member.session.battle != nil {
			t.Fatalf("player %d activity did not finish", member.session.playerID)
		}
		if member.session.x != 0 || member.session.y != 0 {
			t.Fatalf("player %d activity return position=(%f,%f), want shared (0,0)",
				member.session.playerID, member.session.x, member.session.y)
		}
		if count := len(member.session.ensureStarSoulBag().Items); count < 6 || count > 15 {
			t.Fatalf("player %d star soul rewards=%d, want 6..15", member.session.playerID, count)
		}
	}
}

func TestPartyActivitySceneGateMakesDeathRoadAndWorldBossInteractive(t *testing.T) {
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

	tests := []struct {
		name             string
		activity         *activityBattle
		wantMap          int32
		wantParticipants int
		wantConfigID     int32
		wantMapType      int32
	}{
		{
			name: "death road",
			activity: &activityBattle{
				ActiveID: 10023, Method: activeJourneyOfDeath, Difficulty: 1, Stage: 1,
			},
			wantMap:          1005101,
			wantParticipants: maxTeamMembers,
			wantConfigID:     1015,
			wantMapType:      mapMonsterTypeDeathCopy,
		},
		{
			name:             "world boss",
			activity:         &activityBattle{ActiveID: 10022, Method: activeWorldBoss, Stage: 1},
			wantMap:          1005001,
			wantParticipants: maxTeamMembers,
			wantConfigID:     defaultFieldMonsterConfigID,
			wantMapType:      mapMonsterTypeSpaceTravel,
		},
	}
	for caseIndex, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := &Server{conns: make(map[int64]*channel)}
			members := make([]*channel, 0, maxTeamMembers)
			ids := make([]int64, 0, maxTeamMembers)
			teamID := int64(20 + caseIndex)
			for index := 0; index < maxTeamMembers; index++ {
				_, ss, ch := activityTestChannel(t, int64(7200+caseIndex*10+index))
				ss.teamID = teamID
				server.conns[ch.id] = ch
				members = append(members, ch)
				ids = append(ids, ss.playerID)
			}
			teamMu.Lock()
			teams[teamID] = &teamState{LeaderId: ids[0], Members: append([]int64(nil), ids...)}
			teamMu.Unlock()

			started, message := server.startActivityStage(members[0], test.activity.clone(), false)
			if !started || message != "" {
				t.Fatalf("start=%v message=%q", started, message)
			}
			for index, member := range members {
				wantMap := int32(10004)
				if index < test.wantParticipants {
					wantMap = test.wantMap
				}
				if member.session.mapID != wantMap || member.session.battle != nil {
					t.Fatalf("player %d map=%d want=%d battle=%+v", member.session.playerID, member.session.mapID, wantMap, member.session.battle)
				}
			}
			battleMembers := members[:test.wantParticipants]
			finishPendingActivityScene(t, server, battleMembers...)
			for _, member := range battleMembers {
				if member.session.currentActivityScene() == nil || member.session.battle != nil {
					t.Fatalf("player %d did not remain in the interactive activity scene", member.session.playerID)
				}
				if len(member.session.fieldMonsterIDs) != 1 {
					t.Fatalf("player %d display monster ids=%v, want one", member.session.playerID, member.session.fieldMonsterIDs)
				}
				if got := member.session.fieldMonsterConfigIDs[member.session.fieldMonsterIDs[0]]; got != test.wantConfigID {
					t.Fatalf("player %d display monster config=%d, want %d", member.session.playerID, got, test.wantConfigID)
				}
			}
			if configID, mapType, _, _, ok := activityFieldMonster(test.activity); !ok ||
				configID != test.wantConfigID || mapType != test.wantMapType {
				t.Fatalf("display monster=(%d,%d,%v), want (%d,%d,true)",
					configID, mapType, ok, test.wantConfigID, test.wantMapType)
			}
			clickActivityFieldMonster(t, server, battleMembers...)
			party := members[0].session.battle.party
			if test.wantParticipants > 1 && (party == nil || len(party.memberIDs) != test.wantParticipants) {
				t.Fatalf("party battle=%+v", party)
			}
			if test.wantParticipants == 1 && party != nil {
				t.Fatalf("client CanTeam=false copy created party battle=%+v", party)
			}
			for _, member := range battleMembers {
				enterCount, battleSeen, displayMonsterSeen, startupPointSeen := 0, false, false, false
				for _, frame := range decodeRecordedFrames(t, member.conn.(*recordingConn).Bytes()) {
					switch frame.opcode {
					case protocol.OpM2C_EnterMap:
						if battleSeen {
							t.Fatalf("player %d received EnterMap after battle presentation", member.session.playerID)
						}
						enterCount++
					case protocol.OpM2C_MainStoryMonsterInfo:
						battleSeen = true
					case protocol.OpM2C_CreateMapMonster:
						displayMonsterSeen = true
					case protocol.OpM2C_StartupTransPoint:
						startupPointSeen = true
					}
				}
				if !battleSeen || !displayMonsterSeen || startupPointSeen || enterCount != test.wantParticipants-1 {
					t.Fatalf("player %d EnterMap=%d battleSeen=%v displayMonsterSeen=%v startupPointSeen=%v",
						member.session.playerID, enterCount, battleSeen, displayMonsterSeen, startupPointSeen)
				}
			}
		})
	}
}

func TestActivitySceneEntryHasNoPendingTimerOrStartupTransPoint(t *testing.T) {
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
	members := make([]*channel, 0, 2)
	ids := make([]int64, 0, 2)
	for index := 0; index < 2; index++ {
		_, ss, ch := activityTestChannel(t, int64(7300+index))
		ss.teamID = 30
		server.conns[ch.id] = ch
		members = append(members, ch)
		ids = append(ids, ss.playerID)
	}
	teamMu.Lock()
	teams[30] = &teamState{LeaderId: ids[0], Members: append([]int64(nil), ids...)}
	teamMu.Unlock()

	started, message := server.startActivityStage(members[0], &activityBattle{
		ActiveID: 10022, Method: activeWorldBoss, Stage: 1,
	}, false)
	if !started || message != "" {
		t.Fatalf("start=%v message=%q", started, message)
	}
	if server.pendingActivityForPlayer(ids[0]) != nil {
		t.Fatal("synchronous activity left a pending startup timer")
	}
	for _, member := range members {
		if member.session.battle != nil || member.session.currentActivityScene() == nil {
			t.Fatalf("player %d did not remain in the interactive scene", member.session.playerID)
		}
		if len(member.session.fieldMonsterIDs) != 1 {
			t.Fatalf("player %d display monsters=%v, want one", member.session.playerID, member.session.fieldMonsterIDs)
		}
		if _, complete := member.session.currentMapStartupVersion(); !complete {
			t.Fatalf("player %d map startup is incomplete", member.session.playerID)
		}
		for _, frame := range decodeRecordedFrames(t, member.conn.(*recordingConn).Bytes()) {
			if frame.opcode == protocol.OpM2C_StartupTransPoint {
				t.Fatalf("player %d received invalid activity StartupTransPoint", member.session.playerID)
			}
		}
	}
}

func TestDedicatedActivityMapCannotBecomeLoginScene(t *testing.T) {
	for _, mapID := range []int32{1004501, 1004903, 1005001, 1005101, 1005102} {
		ss := newSession()
		ss.mapID = mapID
		ss.resetMovement(8, 9)
		if !normalizeActivityLoginMap(ss) || ss.mapID != 10004 {
			t.Fatalf("activity map %d normalized to %d", mapID, ss.mapID)
		}
	}
	ss := newSession()
	ss.mapID = 1001001
	if normalizeActivityLoginMap(ss) || ss.mapID != 1001001 {
		t.Fatalf("ordinary map was normalized: %d", ss.mapID)
	}
}

func TestDailyActivityEntryUsesOnlineDeathRoadAndWorldBossRows(t *testing.T) {
	loadOnlineTablesForTest(t)
	server, ss, ch := activityTestChannel(t, 7002)
	death := server.onStartActive(ch, &protocol.C2M_StartActive{RpcId: 1, ItemId: 10023}).(*protocol.M2C_StartActive)
	finishPendingActivityScene(t, server, ch)
	clickActivityFieldMonster(t, server, ch)
	if death.Message != "" || ss.battle == nil || ss.battle.activity.Method != activeJourneyOfDeath ||
		ss.battle.activity.Difficulty != 1 || ss.battle.monsters[0].monsterID != 90001 {
		t.Fatalf("death road start=%+v battle=%+v", death, ss.battle)
	}
	if got := ss.signin.DeathTowerRemaining; got != 9 {
		t.Fatalf("death tower stage 1 remaining=%d, want 9", got)
	}
	finishActivityTestStage(t, server, ch)
	finishPendingActivityScene(t, server, ch)
	clickActivityFieldMonster(t, server, ch)
	if ss.battle == nil || ss.battle.monsters[0].monsterID != 90002 {
		t.Fatalf("death road stage 2 roster=%d battle=%+v", ss.battle.monsters[0].monsterID, ss.battle)
	}
	if got := ss.signin.DeathTowerRemaining; got != 9 {
		t.Fatalf("death tower stage 2 charged again: remaining=%d", got)
	}
	returnAfterSeq, _ := ss.currentMapSceneVersion()
	finishActivityTestStage(t, server, ch)
	deadline := time.Now().Add(time.Second)
	for ss.battle != nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	waitForActivityMapReady(t, ch, returnAfterSeq, 10004)

	server, ss, ch = activityTestChannel(t, 7003)
	world := server.onStartActive(ch, &protocol.C2M_StartActive{RpcId: 2, ItemId: 10022}).(*protocol.M2C_StartActive)
	finishPendingActivityScene(t, server, ch)
	if ss.battle != nil || ss.currentActivityScene() == nil {
		t.Fatalf("world boss started before its display monster was clicked: battle=%+v", ss.battle)
	}
	configID, mapType, _, _, ok := activityFieldMonster(ss.currentActivityScene().activity)
	if !ok || configID != defaultFieldMonsterConfigID || mapType != mapMonsterTypeSpaceTravel {
		t.Fatalf("world boss display monster=(config=%d type=%d ok=%v), want (1011,%d,true)",
			configID, mapType, ok, mapMonsterTypeSpaceTravel)
	}
	clickActivityFieldMonster(t, server, ch)
	if world.Message != "" || ss.battle == nil || ss.battle.battleType != 24 || len(ss.battle.monsters) != 3 {
		t.Fatalf("world boss start=%+v battle=%+v", world, ss.battle)
	}
	for _, monster := range ss.battle.monsters {
		if monster.monsterID != 81001 {
			t.Fatalf("world boss monster=%d, want 81001", monster.monsterID)
		}
	}
}

func TestWorldBossActivityUsesConfiguredOnlineRewardChain(t *testing.T) {
	loadOnlineTablesForTest(t)
	server, ss, ch := activityTestChannel(t, 7008)
	result := server.onStartActive(ch, &protocol.C2M_StartActive{
		RpcId: 1, ItemId: 10022,
	}).(*protocol.M2C_StartActive)
	finishPendingActivityScene(t, server, ch)
	if ss.battle != nil || ss.currentActivityScene() == nil {
		t.Fatalf("world boss started before its display monster was clicked: battle=%+v", ss.battle)
	}
	clickActivityFieldMonster(t, server, ch)
	if result.Message != "" || ss.battle == nil {
		t.Fatalf("world boss did not start: result=%+v battle=%+v", result, ss.battle)
	}
	returnAfterSeq, _ := ss.currentMapSceneVersion()
	finishActivityTestStage(t, server, ch)
	waitForActivityMapReady(t, ch, returnAfterSeq, 10004)
	count := bagItemCount(ss, 110205)
	if count < 88000 || count > 180000 {
		t.Fatalf("world boss guaranteed online star coin reward=%d, want 88000..180000", count)
	}
	foundReward := false
	foundStarCoinAsItem := false
	foundStarCoinSync := false
	for _, frame := range decodeRecordedFrames(t, ch.conn.(*recordingConn).Bytes()) {
		switch frame.opcode {
		case protocol.OpM2C_SendReward:
			var reward protocol.M2C_SendReward
			if err := proto.Unmarshal(frame.body, &reward); err != nil {
				t.Fatal(err)
			}
			foundReward = true
			for _, item := range reward.ItemList {
				if item != nil && item.Id == 110205 {
					foundStarCoinAsItem = true
				}
			}
		case protocol.OpM2C_SyncUnitAttribute:
			var attr protocol.M2C_SyncUnitAttribute
			if err := proto.Unmarshal(frame.body, &attr); err != nil {
				t.Fatal(err)
			}
			if attr.NumericType == ntStarCoin && int64(attr.Value) == int64(count) {
				foundStarCoinSync = true
			}
		}
	}
	if !foundReward {
		t.Fatal("world boss reward was stored but not sent through M2C_SendReward")
	}
	if foundStarCoinAsItem {
		t.Fatal("world boss star coins were rendered as a consumable reward item")
	}
	if !foundStarCoinSync {
		t.Fatalf("world boss star coin balance was not synchronized as NumericType=%d", ntStarCoin)
	}
}

func TestDeathTowerRejectsEntryWithoutAttemptBeforeMapChange(t *testing.T) {
	loadOnlineTablesForTest(t)
	server, ss, ch := activityTestChannel(t, 7005)
	setDungeonQuotaForTest(ss, 50, 0, 2)
	startMap, startX, startY := ss.mapID, ss.x, ss.y

	started, message := server.startActivityStage(ch, &activityBattle{
		ActiveID: 10023, Method: activeJourneyOfDeath, Difficulty: 1, Stage: 1,
	}, false)
	if started || message != "死亡之塔战斗次数不足" {
		t.Fatalf("death tower no-attempt start=%v message=%q", started, message)
	}
	if ss.battle != nil || ss.mapID != startMap || ss.x != startX || ss.y != startY {
		t.Fatalf("rejected death tower changed state: battle=%+v map=%d position=(%v,%v)",
			ss.battle, ss.mapID, ss.x, ss.y)
	}
	if ss.signin.DeathTowerRemaining != 0 {
		t.Fatalf("rejected death tower remaining=%d, want 0", ss.signin.DeathTowerRemaining)
	}
}

func TestDeathTowerBattleCreationFailureDoesNotChargeAttempt(t *testing.T) {
	loadOnlineTablesForTest(t)
	server, ss, ch := activityTestChannel(t, 7006)
	setDungeonQuotaForTest(ss, 50, 10, 2)
	presentation := battlePresentation{
		kind: presentationMainStory, copyID: 10016,
		activity: &activityBattle{ActiveID: 10023, Method: activeJourneyOfDeath, Difficulty: 1, Stage: 1},
	}
	started, _ := server.finishStartBattleWithPresentation(ch, 1001, nil, 1005101, presentation)
	if started {
		t.Fatal("death tower unexpectedly started without monsters")
	}
	if got := ss.signin.DeathTowerRemaining; got != 10 {
		t.Fatalf("death tower creation failure charged attempt: %d", got)
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
