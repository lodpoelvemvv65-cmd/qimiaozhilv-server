package main

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func returnTeamTestPlayers(t *testing.T) (*Server, *channel, *channel) {
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

	makePlayer := func(channelID, playerID int64) *channel {
		ss := featureTestSession(playerID)
		ss.state, ss.mapID, ss.teamID = sessInGame, 1000601, 1
		ss.resetMovement(4, 5)
		return &channel{id: channelID, conn: &recordingConn{}, session: ss}
	}
	leader := makePlayer(1, 101)
	member := makePlayer(2, 202)
	server := &Server{conns: map[int64]*channel{leader.id: leader, member.id: member}}
	teamMu.Lock()
	teams[1] = &teamState{LeaderId: leader.session.playerID, Members: []int64{
		leader.session.playerID, member.session.playerID,
	}}
	teamMu.Unlock()
	cancelReturnTestMapStartup(t, leader, member)
	return server, leader, member
}

func cancelReturnTestMapStartup(t *testing.T, channels ...*channel) {
	t.Helper()
	t.Cleanup(func() {
		for _, ch := range channels {
			if ch != nil && ch.session != nil {
				ch.session.markMapChange()
			}
		}
	})
}

func installSharedReturnBattle(leader, member *channel) {
	party := &partyBattle{
		memberIDs: []int64{leader.session.playerID, member.session.playerID},
		members:   make(map[int64]*battleState),
	}
	for _, ch := range []*channel{leader, member} {
		battle := &battleState{
			mapID: 1000601, battleType: 1, owner: ch.session, party: party,
			playerHP: 321, playerMaxHP: 900, playerMP: 123, playerMaxMP: 400,
		}
		party.members[ch.session.playerID] = battle
		ch.session.battle = battle
		ch.session.autoBattle = true
		ch.session.idleBattle = true
	}
}

func hasRecordedOpcode(t *testing.T, conn *recordingConn, opcode uint16) bool {
	t.Helper()
	for _, frame := range decodeRecordedFrames(t, conn.Bytes()) {
		if frame.opcode == opcode {
			return true
		}
	}
	return false
}

func recordedOpcodeCount(t *testing.T, conn *recordingConn, opcode uint16) int {
	t.Helper()
	count := 0
	for _, frame := range decodeRecordedFrames(t, conn.Bytes()) {
		if frame.opcode == opcode {
			count++
		}
	}
	return count
}

func TestReturnScrollClearsBattleBeforeChangingMap(t *testing.T) {
	withFeatureTables(t, &datatables{
		goodsBase: map[int64]map[string]interface{}{
			110344: {"_id": int64(110344), "EffectType": int64(5)},
		},
	})
	ss := featureTestSession(101)
	ss.mapID, ss.x, ss.y = 1000601, 4, 5
	ss.hp, ss.mp = 900, 400
	ss.bag[3] = &bagItem{ItemId: 110344, ItemType: int32(protocol.ItemType_GoodsItem), Count: 1}
	ss.battle = &battleState{
		mapID: 1000601, battleType: 1, owner: ss,
		playerHP: 321, playerMaxHP: 900, playerMP: 123, playerMaxMP: 400,
	}
	ss.autoBattle = true
	ss.idleBattle = true
	epoch := ss.autoBattleEpoch
	conn := &recordingConn{}
	ch := &channel{id: 1, conn: conn, session: ss}
	server := &Server{conns: map[int64]*channel{ch.id: ch}}
	cancelReturnTestMapStartup(t, ch)

	response := server.onUseGoods(ch, &protocol.C2M_UseGoods{Index: 3, RpcId: 77})
	if response != nil {
		t.Fatalf("return scroll response = %T, want raw bag response", response)
	}
	if ss.battle != nil || ss.autoBattle || ss.idleBattle || ss.autoBattleEpoch <= epoch {
		t.Fatalf("fight state remained after return scroll: battle=%p auto=%v idle=%v epoch=%d",
			ss.battle, ss.autoBattle, ss.idleBattle, ss.autoBattleEpoch)
	}
	if ss.hp != 321 || ss.mp != 123 {
		t.Fatalf("battle health was not synchronized: hp=%d mp=%d", ss.hp, ss.mp)
	}
	if ss.mapID != 10004 {
		t.Fatalf("return scroll map=%d, want 10004", ss.mapID)
	}
	if _, exists := ss.bag[3]; exists {
		t.Fatal("return scroll was not consumed")
	}

	defeatIndex, changeIndex := -1, -1
	for index, frame := range decodeRecordedFrames(t, conn.Bytes()) {
		switch frame.opcode {
		case protocol.OpM2C_BattleDefeat:
			defeatIndex = index
		case protocol.OpM2C_ChangeMap:
			changeIndex = index
		}
	}
	if defeatIndex < 0 || changeIndex < 0 || defeatIndex >= changeIndex {
		t.Fatalf("battle/map packet order invalid: defeat=%d change=%d", defeatIndex, changeIndex)
	}
}

func TestBackMainCityRebuildsSceneForStaleFightFlags(t *testing.T) {
	ss := featureTestSession(102)
	ss.mapID = 10004
	ss.autoBattle = true
	ss.idleBattle = true
	conn := &recordingConn{}
	ch := &channel{id: 2, conn: conn, session: ss}
	server := &Server{conns: map[int64]*channel{ch.id: ch}}
	cancelReturnTestMapStartup(t, ch)

	response := server.onBackMainCity(ch, &protocol.C2M_BackMainCity{RpcId: 88}).(*protocol.M2C_BackMainCity)
	if response.Error != 0 || ss.autoBattle || ss.idleBattle {
		t.Fatalf("back-main-city response=%+v auto=%v idle=%v", response, ss.autoBattle, ss.idleBattle)
	}
	changes := 0
	for _, frame := range decodeRecordedFrames(t, conn.Bytes()) {
		if frame.opcode == protocol.OpM2C_ChangeMap {
			changes++
		}
	}
	if changes != 1 {
		t.Fatalf("same-city stale fight rebuilds=%d, want 1", changes)
	}
}

func TestBackMainCityUsesDeterministicLeftSpawnAndKeepsNearbyNPCClickable(t *testing.T) {
	loadOnlineTablesForTest(t)
	ss := featureTestSession(103)
	ss.mapID = 1000604
	ss.resetMovement(20, 10)
	conn := &recordingConn{}
	ch := &channel{id: 3, conn: conn, session: ss}
	server := &Server{conns: map[int64]*channel{ch.id: ch}}
	cancelReturnTestMapStartup(t, ch)

	response := server.onBackMainCity(ch, &protocol.C2M_BackMainCity{RpcId: 89}).(*protocol.M2C_BackMainCity)
	wantX, wantY := mainCityReturnSpawn()
	if response.Error != 0 || ss.mapID != 10004 || ss.x != wantX || ss.y != wantY || ss.moving {
		t.Fatalf("back-main-city result=%+v map=%d pos=(%.5f,%.5f) moving=%v, want left spawn (%.5f,%.5f)",
			response, ss.mapID, ss.x, ss.y, ss.moving, wantX, wantY)
	}
	foundChange := false
	for _, frame := range decodeRecordedFrames(t, conn.Bytes()) {
		if frame.opcode != protocol.OpM2C_ChangeMap {
			continue
		}
		var change protocol.M2C_ChangeMap
		if err := proto.Unmarshal(frame.body, &change); err != nil {
			t.Fatalf("decode city ChangeMap: %v", err)
		}
		if change.MapId != 1000401 || change.X != wantX || change.Y != wantY {
			t.Fatalf("city ChangeMap = %+v, want map=1000401 pos=(%.5f,%.5f)", &change, wantX, wantY)
		}
		foundChange = true
	}
	if !foundChange {
		t.Fatal("back-main-city emitted no ChangeMap packet")
	}

	clicked := server.onClickNPC(ch, &protocol.C2M_ClickNPC{RpcId: 90, NPCId: 1004}).(*protocol.M2C_ClickNPC)
	if clicked.Error != 0 || ss.lastNPCID != 1004 {
		t.Fatalf("left-spawn NPC click failed: response=%+v lastNPC=%d", clicked, ss.lastNPCID)
	}
}

func TestTeamMemberCannotReturnByCtrlG(t *testing.T) {
	server, _, member := returnTeamTestPlayers(t)
	member.session.battle = &battleState{
		mapID: 1000601, battleType: 1, owner: member.session,
		playerHP: 321, playerMaxHP: 900, playerMP: 123, playerMaxMP: 400,
	}

	response := server.onBackMainCity(member, &protocol.C2M_BackMainCity{RpcId: 91}).(*protocol.M2C_BackMainCity)
	if response.Error != 0 || !strings.Contains(response.Message, "请先退出队伍") {
		t.Fatalf("member Ctrl+G response=%+v", response)
	}
	if member.session.mapID != 1000601 || member.session.battle == nil {
		t.Fatalf("blocked Ctrl+G changed state: map=%d battle=%p", member.session.mapID, member.session.battle)
	}
	if hasRecordedOpcode(t, member.conn.(*recordingConn), protocol.OpM2C_ChangeMap) {
		t.Fatal("blocked Ctrl+G emitted ChangeMap")
	}
}

func TestTeamMemberCannotConsumeReturnScroll(t *testing.T) {
	withFeatureTables(t, &datatables{goodsBase: map[int64]map[string]interface{}{
		110344: {"_id": int64(110344), "EffectType": int64(5)},
	}})
	tests := []struct {
		name string
		use  func(*Server, *channel) proto.Message
	}{
		{
			name: "bag",
			use: func(server *Server, member *channel) proto.Message {
				return server.onUseGoods(member, &protocol.C2M_UseGoods{Index: 3, RpcId: 92})
			},
		},
		{
			name: "shortcut",
			use: func(server *Server, member *channel) proto.Message {
				member.session.mainUISlots[4] = mainUISlot{Type: 2, Id: 110344}
				return server.onUseMainUIGoods(member, &protocol.C2M_UseMainUIGoods{SlotId: 4, RpcId: 93})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, _, member := returnTeamTestPlayers(t)
			member.session.bag[3] = &bagItem{
				ItemId: 110344, ItemType: int32(protocol.ItemType_GoodsItem), Count: 2,
			}
			response := test.use(server, member)
			if response == nil || !strings.Contains(response.(interface{ GetMessage() string }).GetMessage(), "请先退出队伍") {
				t.Fatalf("member return-scroll response=%+v", response)
			}
			if item := member.session.bag[3]; item == nil || item.Count != 2 {
				t.Fatalf("blocked return scroll count=%v, want 2", item)
			}
			if member.session.mapID != 1000601 {
				t.Fatalf("blocked return scroll map=%d", member.session.mapID)
			}
			conn := member.conn.(*recordingConn)
			if hasRecordedOpcode(t, conn, protocol.OpM2C_ChangeMap) ||
				hasRecordedOpcode(t, conn, protocol.OpM2C_StartCD) {
				t.Fatalf("blocked return scroll opcodes=%v", recordedOpcodes(t, conn.Bytes()))
			}
		})
	}
}

func TestTeamLeaderCtrlGReturnsOnlinePartyFromBattle(t *testing.T) {
	server, leader, member := returnTeamTestPlayers(t)
	installSharedReturnBattle(leader, member)

	response := server.onBackMainCity(leader, &protocol.C2M_BackMainCity{RpcId: 94}).(*protocol.M2C_BackMainCity)
	if response.Error != 0 || response.Message != "" {
		t.Fatalf("leader Ctrl+G response=%+v", response)
	}
	wantX, wantY := mainCityReturnSpawn()
	wantPositions := map[int64][2]float32{
		leader.session.playerID: {wantX, wantY},
		member.session.playerID: {wantX, wantY},
	}
	for _, ch := range []*channel{leader, member} {
		ss := ch.session
		want := wantPositions[ss.playerID]
		if ss.mapID != 10004 || ss.x != want[0] || ss.y != want[1] || ss.battle != nil || ss.autoBattle || ss.idleBattle {
			t.Fatalf("player %d return state: map=%d pos=(%f,%f) battle=%p auto=%v idle=%v",
				ss.playerID, ss.mapID, ss.x, ss.y, ss.battle, ss.autoBattle, ss.idleBattle)
		}
		if !hasRecordedOpcode(t, ch.conn.(*recordingConn), protocol.OpM2C_ChangeMap) {
			t.Fatalf("player %d received no ChangeMap", ss.playerID)
		}
		if hasRecordedOpcode(t, ch.conn.(*recordingConn), protocol.OpM2C_TeamMember) {
			t.Fatalf("player %d received TeamMember before its new scene was ready", ss.playerID)
		}
	}
}

func TestTeamLeaderReturnScrollConsumesOnlyLeaderScroll(t *testing.T) {
	withFeatureTables(t, &datatables{goodsBase: map[int64]map[string]interface{}{
		110344: {"_id": int64(110344), "EffectType": int64(5)},
	}})
	server, leader, member := returnTeamTestPlayers(t)
	leader.session.bag[3] = &bagItem{ItemId: 110344, ItemType: int32(protocol.ItemType_GoodsItem), Count: 2}
	member.session.bag[3] = &bagItem{ItemId: 110344, ItemType: int32(protocol.ItemType_GoodsItem), Count: 2}
	installSharedReturnBattle(leader, member)

	response := server.onUseGoods(leader, &protocol.C2M_UseGoods{Index: 3, RpcId: 95})
	if response != nil {
		t.Fatalf("leader return-scroll response=%+v, want raw bag response", response)
	}
	if leader.session.bag[3].Count != 1 || member.session.bag[3].Count != 2 {
		t.Fatalf("return-scroll counts leader=%d member=%d, want 1/2",
			leader.session.bag[3].Count, member.session.bag[3].Count)
	}
	for _, ch := range []*channel{leader, member} {
		if ch.session.mapID != 10004 || ch.session.battle != nil {
			t.Fatalf("player %d return state: map=%d battle=%p",
				ch.session.playerID, ch.session.mapID, ch.session.battle)
		}
	}
}

func TestTeamLeaderCtrlGReturnsDefeatedPartyOnlyOnce(t *testing.T) {
	server, leader, member := returnTeamTestPlayers(t)
	installSharedReturnBattle(leader, member)
	for _, ch := range []*channel{leader, member} {
		ch.session.battle.playerHP = 0
		ch.session.battle.battleType = trialBattleType
	}

	response := server.onBackMainCity(leader, &protocol.C2M_BackMainCity{RpcId: 96}).(*protocol.M2C_BackMainCity)
	if response.Error != 0 || response.Message != "" {
		t.Fatalf("defeated-party Ctrl+G response=%+v", response)
	}
	time.Sleep(50 * time.Millisecond)
	for _, ch := range []*channel{leader, member} {
		if count := recordedOpcodeCount(t, ch.conn.(*recordingConn), protocol.OpM2C_ChangeMap); count != 1 {
			t.Fatalf("player %d ChangeMap count=%d, want 1", ch.session.playerID, count)
		}
	}
}

func TestPVEDefeatReturnsToCityWithOneHPOrMagicBallRecovery(t *testing.T) {
	loadOnlineTablesForTest(t)
	tests := []struct {
		name     string
		activity *activityBattle
		withBall bool
	}{
		{name: "ordinary battle"},
		{
			name:     "activity battle with health ball",
			activity: &activityBattle{ReturnMap: 1000601, ReturnX: 4, ReturnY: 5},
			withBall: true,
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ss := featureTestSession(int64(9500 + index))
			ss.mapID = 1000601
			ss.resetMovement(4, 5)
			maxHP, maxMP := ss.playerMaxHp(), ss.playerMaxMp()
			battle := &battleState{
				mapID: 1000601, battleType: 1, owner: ss, activity: test.activity,
				playerHP: 0, playerMaxHP: maxHP, playerMP: maxMP / 2, playerMaxMP: maxMP,
				monsters: []*monsterUnit{{id: 99001, hp: 100, maxHP: 100, alive: true}},
			}
			ss.battle = battle
			if test.withBall {
				ss.itemBuffs = make(map[int32]*activeItemBuff)
				ss.itemBuffs[itemBuffHPBall] = &activeItemBuff{
					ItemID: 110327, EffectType: 2, Capacity: int64(maxHP),
				}
			}
			conn := &recordingConn{}
			ch := &channel{id: int64(index + 1), conn: conn, session: ss}
			server := &Server{conns: map[int64]*channel{ch.id: ch}}
			cancelReturnTestMapStartup(t, ch)

			ss.battleMu.Lock()
			settled := server.settleCombatLocked(ch, battle)
			ss.battleMu.Unlock()

			wantHP := int32(1)
			if test.withBall {
				wantHP = maxHP
			}
			wantX, wantY := mainCityReturnSpawn()
			if !settled || ss.battle != nil || ss.hp != wantHP {
				t.Fatalf("defeat settled=%v battle=%p hp=%d, want hp=%d", settled, ss.battle, ss.hp, wantHP)
			}
			if ss.mapID != 10004 || ss.x != wantX || ss.y != wantY {
				t.Fatalf("defeat destination map=%d pos=(%.2f,%.2f), want city (%.2f,%.2f)",
					ss.mapID, ss.x, ss.y, wantX, wantY)
			}
			defeatIndex, changeIndex := -1, -1
			lastHP := float32(-1)
			for frameIndex, frame := range decodeRecordedFrames(t, conn.Bytes()) {
				switch frame.opcode {
				case protocol.OpM2C_BattleDefeat:
					defeatIndex = frameIndex
				case protocol.OpM2C_ChangeMap:
					changeIndex = frameIndex
				case protocol.OpM2C_SyncUnitAttribute:
					message := &protocol.M2C_SyncUnitAttribute{}
					if err := proto.Unmarshal(frame.body, message); err != nil {
						t.Fatal(err)
					}
					if message.UnitId == ss.playerID && message.NumericType == 1001 {
						lastHP = message.Value
					}
				}
			}
			if defeatIndex < 0 || changeIndex < 0 || defeatIndex >= changeIndex {
				t.Fatalf("defeat/map packet order invalid: defeat=%d change=%d", defeatIndex, changeIndex)
			}
			if lastHP != float32(wantHP) {
				t.Fatalf("last HP sync=%v, want %d", lastHP, wantHP)
			}
		})
	}
}

func TestPartyDefeatReturnsEveryMemberToCityAndRecoversHealth(t *testing.T) {
	loadOnlineTablesForTest(t)
	server, leader, member := returnTeamTestPlayers(t)
	installSharedReturnBattle(leader, member)
	for _, ch := range []*channel{leader, member} {
		battle := ch.session.battle
		battle.playerHP = 0
		battle.playerMaxHP = ch.session.playerMaxHp()
		battle.playerMP = ch.session.playerMaxMp() / 2
		battle.playerMaxMP = ch.session.playerMaxMp()
		battle.ended = true
	}
	leaderMaxHP := leader.session.playerMaxHp()
	leader.session.itemBuffs = make(map[int32]*activeItemBuff)
	leader.session.itemBuffs[itemBuffHPBall] = &activeItemBuff{
		ItemID: 110327, EffectType: 2, Capacity: int64(leaderMaxHP),
	}
	party := leader.session.battle.party

	server.finishPartyDefeat(party)

	wantX, wantY := mainCityReturnSpawn()
	for _, ch := range []*channel{leader, member} {
		wantHP := int32(1)
		if ch == leader {
			wantHP = leaderMaxHP
		}
		if ch.session.battle != nil || ch.session.hp != wantHP {
			t.Fatalf("member %d battle=%p hp=%d, want hp=%d",
				ch.session.playerID, ch.session.battle, ch.session.hp, wantHP)
		}
		if ch.session.mapID != 10004 || ch.session.x != wantX || ch.session.y != wantY {
			t.Fatalf("member %d destination map=%d pos=(%.2f,%.2f), want city (%.2f,%.2f)",
				ch.session.playerID, ch.session.mapID, ch.session.x, ch.session.y, wantX, wantY)
		}
		defeatIndex, changeIndex := -1, -1
		for frameIndex, frame := range decodeRecordedFrames(t, ch.conn.(*recordingConn).Bytes()) {
			switch frame.opcode {
			case protocol.OpM2C_BattleDefeat:
				defeatIndex = frameIndex
			case protocol.OpM2C_ChangeMap:
				changeIndex = frameIndex
			}
		}
		if defeatIndex < 0 || changeIndex < 0 || defeatIndex >= changeIndex {
			t.Fatalf("member %d packet order defeat=%d change=%d",
				ch.session.playerID, defeatIndex, changeIndex)
		}
	}
}

func TestFiveMemberPartyDefeatCompletesReturnAndPanelRPCsAcrossMaps(t *testing.T) {
	loadOnlineTablesForTest(t)
	tests := []struct {
		name      string
		sceneMap  int32
		battleMap int32
		copyID    int64
		activity  *activityBattle
	}{
		{name: "family boss", sceneMap: 10004, battleMap: -5},
		{name: "world boss", sceneMap: 1001001, battleMap: 1001001},
		{name: "main story", sceneMap: 1000605, battleMap: 1000605},
		{name: "manual equipment dungeon", sceneMap: 1003303, battleMap: 1003303, copyID: manualEquipCopyID},
		{name: "daily activity", sceneMap: 1005001, battleMap: 1005001, activity: &activityBattle{
			ActiveID: 10022, Method: activeWorldBoss, Stage: 1, ReturnMap: 10004,
		}},
	}

	for testIndex, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			teamMu.Lock()
			oldTeams, oldSeq := teams, teamSeq
			teams, teamSeq = make(map[int64]*teamState), 1
			teamMu.Unlock()
			t.Cleanup(func() {
				teamMu.Lock()
				teams, teamSeq = oldTeams, oldSeq
				teamMu.Unlock()
			})

			party := &partyBattle{members: make(map[int64]*battleState)}
			server := &Server{conns: make(map[int64]*channel), mapCoinRoll: func() float64 { return 1 }}
			members := make([]*channel, 0, maxTeamMembers)
			for memberIndex := 0; memberIndex < maxTeamMembers; memberIndex++ {
				playerID := int64(9600 + testIndex*10 + memberIndex)
				ss := featureTestSession(playerID)
				ss.state, ss.teamID, ss.mapID = sessInGame, 1, test.sceneMap
				ss.resetMovement(4, 5)
				ss.skills = map[int32]int32{100001: 1}
				ss.skillOrder = []int32{100001}
				ss.bag[3] = &bagItem{ItemId: 110344, ItemType: int32(protocol.ItemType_GoodsItem), Count: 2}
				ss.worn[0] = &bagItem{ItemId: 120590, ItemType: int32(protocol.ItemType_EquipItem), Count: 1}
				battle := &battleState{
					owner: ss, party: party, mapID: test.battleMap, copyID: test.copyID,
					battleType: 1, activity: test.activity, ended: true,
					playerHP: 0, playerMaxHP: ss.playerMaxHp(),
					playerMP: ss.playerMaxMp() / 2, playerMaxMP: ss.playerMaxMp(),
					monsters: []*monsterUnit{{id: 99001, hp: 100, maxHP: 100, alive: true}},
				}
				ss.battle = battle
				ch := &channel{id: playerID, conn: &recordingConn{}, session: ss}
				party.memberIDs = append(party.memberIDs, playerID)
				party.members[playerID] = battle
				server.conns[ch.id] = ch
				members = append(members, ch)
			}
			teams[1] = &teamState{LeaderId: party.memberIDs[0], Members: append([]int64(nil), party.memberIDs...)}
			cancelReturnTestMapStartup(t, members...)

			settlementDone := make(chan struct{})
			go func() {
				server.finishPartyDefeat(party)
				close(settlementDone)
			}()
			select {
			case <-settlementDone:
			case <-time.After(2 * time.Second):
				t.Fatal("five-member defeat settlement deadlocked")
			}

			var sceneSeq uint64
			for _, member := range members {
				ss := member.session
				if ss.battle != nil || ss.mapID != 10004 || ss.hp != 1 {
					t.Fatalf("player %d incomplete return: battle=%p map=%d hp=%d", ss.playerID, ss.battle, ss.mapID, ss.hp)
				}
				seq, ready := ss.currentMapSceneVersion()
				if ready || seq == 0 {
					t.Fatalf("player %d scene before startup: seq=%d ready=%v", ss.playerID, seq, ready)
				}
				if sceneSeq == 0 {
					sceneSeq = seq
				} else if seq != sceneSeq {
					t.Fatalf("party scene sequence split: player=%d seq=%d want=%d", ss.playerID, seq, sceneSeq)
				}
				if count := recordedOpcodeCount(t, member.conn.(*recordingConn), protocol.OpM2C_ChangeMap); count != 1 {
					t.Fatalf("player %d ChangeMap count=%d, want 1", ss.playerID, count)
				}
			}

			for _, member := range members {
				if !server.finishMapStartup(member, 10004, sceneSeq, false) {
					t.Fatalf("player %d scene startup failed", member.session.playerID)
				}
			}
			for _, member := range members {
				if !member.session.isCurrentMapStartupComplete(sceneSeq) {
					t.Fatalf("player %d scene startup incomplete", member.session.playerID)
				}
				rpcDone := make(chan proto.Message, 1)
				go func(ch *channel) {
					rpcDone <- server.onGetSkill(ch, &protocol.C2M_GetSkill{RpcId: 1})
				}(member)
				select {
				case response := <-rpcDone:
					if response == nil {
						t.Fatalf("player %d skill response is nil", member.session.playerID)
					}
				case <-time.After(time.Second):
					t.Fatalf("player %d skill request blocked on battleMu", member.session.playerID)
				}
				server.onGetCharacter(member, &protocol.C2M_GetCharacter{RpcId: 2, Id: member.session.playerID})
				server.onGetBag(member, &protocol.C2M_GetBag{RpcId: 3})
				conn := member.conn.(*recordingConn)
				if !hasRecordedOpcode(t, conn, protocol.OpM2C_GetCharacter) ||
					!hasRecordedOpcode(t, conn, protocol.OpM2C_GetBag) ||
					!hasRecordedOpcode(t, conn, protocol.OpM2C_SendBag) {
					t.Fatalf("player %d post-return panel RPCs incomplete: %v",
						member.session.playerID, recordedOpcodes(t, conn.Bytes()))
				}
			}
		})
	}
}
