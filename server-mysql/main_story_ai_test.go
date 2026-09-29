package main

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func TestMainStoryAIStartFailureSendsHealthReason(t *testing.T) {
	loadOnlineTablesForTest(t)
	server := &Server{conns: make(map[int64]*channel)}
	ch, conn := consistencyChannel(990000, 1001101)
	ch.session.hp = 0
	server.conns[ch.id] = ch

	started, _, message := server.startMainStoryFightByRegionWithReason(ch, 1001)
	if started {
		t.Fatal("main-story fight unexpectedly started with zero HP")
	}
	if message != battleEntryHealthMessage {
		t.Fatalf("failure message=%q, want %q", message, battleEntryHealthMessage)
	}
	server.sendMainStoryAIStopTip(ch, message)
	frames := decodeRecordedFrames(t, conn.Bytes())
	if len(frames) != 1 || frames[0].opcode != protocol.OpM2C_SendTip {
		t.Fatalf("tip frames=%v, want one SendTip", recordedOpcodes(t, conn.Bytes()))
	}
	var tip protocol.M2C_SendTip
	if err := proto.Unmarshal(frames[0].body, &tip); err != nil {
		t.Fatal(err)
	}
	if tip.Message != battleEntryHealthMessage || tip.ActorId != ch.session.playerID {
		t.Fatalf("tip=%+v, want health message for player %d", &tip, ch.session.playerID)
	}
}

func TestMainStoryAIAdvancesAfterVictoryOutsideBeach(t *testing.T) {
	loadOnlineTablesForTest(t)
	server := &Server{conns: make(map[int64]*channel)}
	ch, conn := consistencyChannel(990001, 1001101)
	ch.session.tasks = make(map[int32]int32)
	ch.session.killCount = make(map[int32]int32)
	ch.session.bag = make(map[int32]*bagItem)
	ch.session.skills = map[int32]int32{100001: 1}
	ch.session.skillOrder = []int32{100001}
	server.conns[ch.id] = ch

	response := server.onStartMainStoryAI(ch, &protocol.C2M_StartMainStoryAI{RpcId: 1, Index: 0})
	if response.(*protocol.M2C_StartMainStoryAI).Message != "" {
		t.Fatalf("start AI response = %+v", response)
	}
	ch.session.battleMu.Lock()
	battle := ch.session.battle
	if battle == nil || !ch.session.mainStoryAIRunning {
		ch.session.battleMu.Unlock()
		t.Fatalf("AI start state battle=%v running=%v", battle, ch.session.mainStoryAIRunning)
	}
	for _, monster := range battle.monsters {
		monster.hp, monster.alive = 0, false
	}
	server.emitVictory(ch, battle)
	ch.session.battleMu.Unlock()

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		ch.session.battleMu.Lock()
		mapID, nextBattle, autoRunning := ch.session.mapID, ch.session.battle != nil, ch.session.autoBattle
		ch.session.battleMu.Unlock()
		if mapID == 1001102 && nextBattle && autoRunning {
			frames := decodeRecordedFrames(t, conn.Bytes())
			walked := false
			for _, frame := range frames {
				if frame.opcode != protocol.OpM2C_PathfindingResult {
					continue
				}
				var path protocol.M2C_PathfindingResult
				if err := proto.Unmarshal(frame.body, &path); err != nil {
					t.Fatal(err)
				}
				if path.Id == ch.session.playerID && path.TX != path.X {
					walked = true
					break
				}
			}
			if !walked {
				t.Fatal("main-story AI advanced without a portal movement path")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	frames := decodeRecordedFrames(t, conn.Bytes())
	var changes []int32
	for _, frame := range frames {
		if frame.opcode != protocol.OpM2C_ChangeMap {
			continue
		}
		var change protocol.M2C_ChangeMap
		if err := proto.Unmarshal(frame.body, &change); err != nil {
			t.Fatal(err)
		}
		changes = append(changes, change.MapId)
	}
	t.Fatalf("AI did not advance to next layer: map=%d battle=%v changes=%v",
		ch.session.mapID, ch.session.battle != nil, changes)
}

func TestMainStoryAILoopsSameChapterAfterReturningToCity(t *testing.T) {
	assertMainStoryAIChapterLoop(t)
}

func TestPartyMainStoryAIFinalLayerKeepsFollowersForPortalRun(t *testing.T) {
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

	server, party, leader, follower := newSharedPartyBattleForTest(t)
	const finalMapID int32 = 1001110
	ids := []int64{leader.session.playerID, follower.session.playerID}
	teamMu.Lock()
	teams[1] = &teamState{LeaderId: ids[0], Members: append([]int64(nil), ids...)}
	teamMu.Unlock()

	portal := sceneTransPos(finalMapID / 100)[0]
	for _, member := range []*channel{leader, follower} {
		ss := member.session
		ss.mapID, ss.teamID = finalMapID, 1
		ss.resetMovement(portal[0]-8, portal[1])
		ss.tasks = make(map[int32]int32)
		ss.killCount = make(map[int32]int32)
		ss.bag = make(map[int32]*bagItem)
		battle := party.members[ss.playerID]
		battle.region, battle.copyID, battle.mapID = 1010, 10003, finalMapID
		battle.ended = true
		for _, monster := range battle.monsters {
			monster.hp, monster.alive = 0, false
		}
	}
	leader.session.mainStoryAIRunning = true
	leader.session.mainStoryAIEpoch++
	party.settled = true
	cancelReturnTestMapStartup(t, leader, follower)
	t.Cleanup(func() {
		leader.session.stopMainStoryAI()
		leader.session.teamMoveSeq.Add(1)
	})

	server.finishPartyVictory(party)
	for _, member := range []*channel{leader, follower} {
		if member.session.mapID != finalMapID || member.session.battle != nil {
			t.Fatalf("player %d settlement map=%d battle=%p, want map=%d and detached battle",
				member.session.playerID, member.session.mapID, member.session.battle, finalMapID)
		}
		if hasRecordedOpcode(t, member.conn.(*recordingConn), protocol.OpM2C_ChangeMap) {
			t.Fatalf("player %d returned to town before the portal run", member.session.playerID)
		}
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		allMoved := true
		for _, member := range []*channel{leader, follower} {
			moved := false
			for _, frame := range decodeRecordedFrames(t, member.conn.(*recordingConn).Bytes()) {
				if frame.opcode != protocol.OpM2C_PathfindingResult {
					continue
				}
				var path protocol.M2C_PathfindingResult
				if err := proto.Unmarshal(frame.body, &path); err != nil {
					t.Fatal(err)
				}
				if path.Id == member.session.playerID {
					moved = true
					break
				}
			}
			allMoved = allMoved && moved
		}
		if allMoved {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("leader and follower did not both receive the final-layer portal path")
}

func TestPartyMainStoryAIRegularLayerRunsToNextPortal(t *testing.T) {
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

	server, party, leader, follower := newSharedPartyBattleForTest(t)
	const mapID int32 = 1001101
	ids := []int64{leader.session.playerID, follower.session.playerID}
	teamMu.Lock()
	teams[1] = &teamState{LeaderId: ids[0], Members: append([]int64(nil), ids...)}
	teamMu.Unlock()

	portal := sceneTransPos(mapID / 100)[0]
	for _, member := range []*channel{leader, follower} {
		ss := member.session
		ss.mapID, ss.teamID = mapID, 1
		ss.energy = 100
		ss.resetMovement(portal[0]-8, portal[1])
		ss.tasks = make(map[int32]int32)
		ss.killCount = make(map[int32]int32)
		ss.bag = make(map[int32]*bagItem)
		battle := party.members[ss.playerID]
		battle.region, battle.copyID, battle.mapID = 1001, 10001, mapID
		battle.ended = true
		for _, monster := range battle.monsters {
			monster.hp, monster.alive = 0, false
		}
	}
	leader.session.mainStoryAIRunning = true
	leader.session.mainStoryAIEpoch++
	party.settled = true
	t.Cleanup(func() {
		leader.session.stopMainStoryAI()
		leader.session.teamMoveSeq.Add(1)
	})

	server.finishPartyVictory(party)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		allMoved := true
		for _, member := range []*channel{leader, follower} {
			moved := false
			for _, frame := range decodeRecordedFrames(t, member.conn.(*recordingConn).Bytes()) {
				if frame.opcode != protocol.OpM2C_PathfindingResult {
					continue
				}
				var path protocol.M2C_PathfindingResult
				if err := proto.Unmarshal(frame.body, &path); err != nil {
					t.Fatal(err)
				}
				if path.Id == member.session.playerID && path.TX != path.X {
					moved = true
					break
				}
			}
			allMoved = allMoved && moved
		}
		if allMoved {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("leader and follower did not receive the regular-layer portal path")
}

func TestPartyMainStoryAIStopsAndNamesMemberWithInsufficientEnergy(t *testing.T) {
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

	leader, leaderConn := consistencyChannel(990020, 1001101)
	follower, followerConn := consistencyChannel(990021, 1001101)
	leader.session.name, follower.session.name = "甲队长", "乙队员"
	leader.session.teamID, follower.session.teamID = 1, 1
	leader.session.energy, follower.session.energy = 0, 100
	leader.session.mainStoryAIRunning = true
	leader.session.mainStoryAIEpoch++
	epoch := leader.session.mainStoryAIEpoch
	server := &Server{conns: map[int64]*channel{leader.id: leader, follower.id: follower}}
	teamMu.Lock()
	teams[1] = &teamState{LeaderId: leader.session.playerID, Members: []int64{
		leader.session.playerID, follower.session.playerID,
	}}
	teamMu.Unlock()
	if _, message := server.mainStoryAIEnergyFailure(leader, 1002); message != "队长甲队长体力不足，已停止自动跑图" {
		t.Fatalf("leader energy message=%q", message)
	}
	leader.session.energy, follower.session.energy = 100, 0

	if !server.stopMainStoryAIForEnergy(leader, epoch, 1002, 1001102) {
		t.Fatal("AI did not stop for the follower's insufficient energy")
	}
	if running, _ := leader.session.mainStoryAISnapshot(); running {
		t.Fatal("main-story AI remained running after the energy failure")
	}
	const want = "队员乙队员体力不足，已停止自动跑图"
	for _, entry := range []struct {
		name string
		conn *recordingConn
	}{{"leader", leaderConn}, {"follower", followerConn}} {
		frames := decodeRecordedFrames(t, entry.conn.Bytes())
		if len(frames) != 1 || frames[0].opcode != protocol.OpM2C_SendTip {
			t.Fatalf("%s frames=%v, want one SendTip", entry.name, recordedOpcodes(t, entry.conn.Bytes()))
		}
		var tip protocol.M2C_SendTip
		if err := proto.Unmarshal(frames[0].body, &tip); err != nil {
			t.Fatal(err)
		}
		if tip.Message != want {
			t.Fatalf("%s tip=%q, want %q", entry.name, tip.Message, want)
		}
	}
}

func TestMainStoryAIPortalRunKeepsSceneWithoutReload(t *testing.T) {
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

	leader, leaderConn := consistencyChannel(990010, 1001108)
	follower, followerConn := consistencyChannel(990011, 1001108)
	leader.session.teamID, follower.session.teamID = 1, 1
	leader.session.mainStoryAIRunning = true
	leader.session.mainStoryAIEpoch++
	epoch := leader.session.mainStoryAIEpoch
	server := &Server{conns: map[int64]*channel{leader.id: leader, follower.id: follower}}
	teamMu.Lock()
	teams[1] = &teamState{LeaderId: leader.session.playerID, Members: []int64{
		leader.session.playerID, follower.session.playerID,
	}}
	teamMu.Unlock()
	cancelReturnTestMapStartup(t, leader, follower)
	t.Cleanup(func() { leader.session.stopMainStoryAI() })

	ready := make(chan struct{}, 1)
	server.prepareMainStoryAIPortalRun(leader, epoch, leader.session.mapID, func() {
		ready <- struct{}{}
	})
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("portal run did not resume after the battle")
	}
	// 线上不发同场景 ChangeMap 清洗特效，客户端按特效自身 Time 销毁；多发一次
	// 换图会让玩家多等一次读条并看到闪屏。
	for _, entry := range []struct {
		member *channel
		conn   *recordingConn
	}{{leader, leaderConn}, {follower, followerConn}} {
		if entry.member.session.mapID != 1001108 {
			t.Fatalf("player %d map changed to %d", entry.member.session.playerID, entry.member.session.mapID)
		}
		for _, frame := range decodeRecordedFrames(t, entry.conn.Bytes()) {
			if frame.opcode == protocol.OpM2C_ChangeMap {
				t.Fatalf("player %d received a same-scene reload: %v",
					entry.member.session.playerID, recordedOpcodes(t, entry.conn.Bytes()))
			}
		}
	}
}

func assertMainStoryAIChapterLoop(t *testing.T) {
	loadOnlineTablesForTest(t)
	server := &Server{conns: make(map[int64]*channel)}
	ch, conn := consistencyChannel(990002, 1001110)
	ch.session.tasks = make(map[int32]int32)
	ch.session.killCount = make(map[int32]int32)
	ch.session.bag = make(map[int32]*bagItem)
	server.conns[ch.id] = ch
	ch.session.skills = map[int32]int32{100001: 1}
	ch.session.skillOrder = []int32{100001}
	response := server.onStartMainStoryAI(ch, &protocol.C2M_StartMainStoryAI{Index: 0})
	if response.(*protocol.M2C_StartMainStoryAI).Message != "" {
		t.Fatalf("start final-layer AI response = %+v", response)
	}
	ch.session.battleMu.Lock()
	battle := ch.session.battle
	for _, monster := range battle.monsters {
		monster.hp, monster.alive = 0, false
	}
	server.emitVictory(ch, battle)
	ch.session.battleMu.Unlock()
	if running, _ := ch.session.mainStoryAISnapshot(); !running {
		t.Fatal("final-layer AI stopped instead of preserving the chapter loop")
	}

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		ch.session.battleMu.Lock()
		mapID, nextBattle, autoRunning := ch.session.mapID, ch.session.battle != nil, ch.session.autoBattle
		ch.session.battleMu.Unlock()
		if mapID == 1001101 && nextBattle && autoRunning {
			frames := decodeRecordedFrames(t, conn.Bytes())
			var changes []int32
			cityRun := false
			for _, frame := range frames {
				if frame.opcode == protocol.OpM2C_PathfindingResult {
					var path protocol.M2C_PathfindingResult
					if err := proto.Unmarshal(frame.body, &path); err != nil {
						t.Fatal(err)
					}
					if path.X < 0 && path.TX > 0 {
						cityRun = true
					}
				}
				if frame.opcode != protocol.OpM2C_ChangeMap {
					continue
				}
				var change protocol.M2C_ChangeMap
				if err := proto.Unmarshal(frame.body, &change); err != nil {
					t.Fatal(err)
				}
				changes = append(changes, change.MapId)
			}
			if len(changes) < 2 || changes[len(changes)-2] != wireMapID(10004) || changes[len(changes)-1] != 1001101 {
				t.Fatalf("chapter loop changes=%v, want city then same chapter layer one", changes)
			}
			if !cityRun {
				t.Fatal("chapter restart entered the map without running from the city's right portal")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("AI did not restart same chapter: map=%d battle=%v auto=%v",
		ch.session.mapID, ch.session.battle != nil, ch.session.autoBattle)
}

func TestMainStoryAIAutoTransitionUsesNativeDelay(t *testing.T) {
	if got := mainStoryAIAutoTransitionDelay(); got != mainStoryAITransitionDelay {
		t.Fatalf("auto transition delay=%v, want native transition delay=%v", got, mainStoryAITransitionDelay)
	}
}

func TestMainStoryAIArmsEveryPartyMemberWithoutPersistingAutoPreference(t *testing.T) {
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
		ch, _ := consistencyChannel(int64(8801+index), 1001101)
		ch.session.teamID = 1
		ch.session.skills = map[int32]int32{100001: 1}
		ch.session.skillOrder = []int32{100001}
		ch.session.autoBattleEnabled = index < 3
		server.conns[ch.id] = ch
		members = append(members, ch)
		ids = append(ids, ch.session.playerID)
	}
	teamMu.Lock()
	teams[1] = &teamState{LeaderId: ids[0], Members: append([]int64(nil), ids...)}
	teamMu.Unlock()
	t.Cleanup(func() {
		for _, member := range members {
			member.session.stopAutoBattle()
			member.session.stopMainStoryAI()
		}
	})

	leader := members[0]
	response := server.onStartMainStoryAI(leader, &protocol.C2M_StartMainStoryAI{RpcId: 1, Index: 0})
	if response.(*protocol.M2C_StartMainStoryAI).Message != "" {
		t.Fatalf("start AI response = %+v", response)
	}
	if !leader.session.mainStoryAIRunning {
		t.Fatal("leader main-story AI was not marked running")
	}
	party := leader.session.battle.party
	if party == nil || len(party.memberIDs) != maxTeamMembers {
		t.Fatalf("party members = %v, want %d", party, maxTeamMembers)
	}
	for index, member := range members {
		ss := member.session
		if ss.battle == nil || ss.battle.party != party {
			t.Fatalf("member %d is not in the shared AI fight", ss.playerID)
		}
		if !ss.autoBattle {
			t.Fatalf("member %d autoBattle=false during AI run", ss.playerID)
		}
		if want := index < 3; ss.autoBattleEnabled != want {
			t.Fatalf("member %d autoBattleEnabled=%v, want persisted %v", ss.playerID, ss.autoBattleEnabled, want)
		}
	}
}
