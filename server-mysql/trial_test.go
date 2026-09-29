package main

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/internal/operationsconfig"
	"mhqserver/protocol"
)

type recordedFrame struct {
	opcode uint16
	body   []byte
}

func decodeRecordedFrames(t *testing.T, data []byte) []recordedFrame {
	t.Helper()
	var frames []recordedFrame
	for len(data) > 0 {
		if len(data) < 4 {
			t.Fatalf("truncated frame: %x", data)
		}
		length := int(binary.LittleEndian.Uint16(data[:2]))
		if length < 2 || len(data) < length+2 {
			t.Fatalf("invalid frame length %d in %x", length, data)
		}
		frames = append(frames, recordedFrame{
			opcode: binary.LittleEndian.Uint16(data[2:4]),
			body:   append([]byte(nil), data[4:length+2]...),
		})
		data = data[length+2:]
	}
	return frames
}

func TestTrialNativeMapAndStartFlow(t *testing.T) {
	oldTables := tables
	if err := loadDatatables("../datatable_json"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tables = oldTables })

	ss := newSession()
	ss.playerID = 7
	ss.jobID = 1
	ss.level = 100
	ss.mapID = 1000901
	ss.bag = make(map[int32]*bagItem)
	ss.worn = make(map[int32]*bagItem)
	ss.skills = make(map[int32]int32)
	ss.tasks = make(map[int32]int32)
	ss.killCount = make(map[int32]int32)
	ss.signin = &signinState{}

	mapConn := &recordingConn{}
	server := &Server{}
	mapChannel := &channel{id: 1, conn: mapConn, session: ss}
	server.pushFieldMonsters(mapChannel)
	mapFrames := decodeRecordedFrames(t, mapConn.Bytes())
	if len(mapFrames) != 1 || mapFrames[0].opcode != protocol.OpM2C_InitTrialCopyMap {
		t.Fatalf("trial map frames = %+v, want only opcode %d", mapFrames, protocol.OpM2C_InitTrialCopyMap)
	}
	initMessage := &protocol.M2C_InitTrialCopyMap{}
	if err := proto.Unmarshal(mapFrames[0].body, initMessage); err != nil {
		t.Fatal(err)
	}
	if initMessage.TrialCopyId != 1001 {
		t.Fatalf("TrialCopyId = %d, want 1001", initMessage.TrialCopyId)
	}

	fightConn := &recordingConn{}
	fightChannel := &channel{id: 2, conn: fightConn, session: ss}
	response := server.onStartTrialCopyFight(fightChannel, &protocol.C2M_StartTrialCopyFight{RpcId: 55})
	start, ok := response.(*protocol.M2C_StartTrialCopyFight)
	if !ok || start.Message != "" || start.Error != 0 {
		t.Fatalf("start response = %#v", response)
	}
	if start.RpcId != 55 || start.TrialCopyId != 1001 || len(start.UnitIdList) != 2 {
		t.Fatalf("start response fields = %+v", start)
	}
	if ss.battle == nil || ss.battle.battleType != trialBattleType || ss.battle.trialCopyID != 1001 {
		t.Fatalf("trial battle state = %+v", ss.battle)
	}

	frames := decodeRecordedFrames(t, fightConn.Bytes())
	var presentation *protocol.M2C_MainStoryMonsterInfo
	for _, frame := range frames {
		if frame.opcode == protocol.OpM2C_InitMainStoryMap {
			t.Fatalf("trial start recreated the field presentation with opcode %d", frame.opcode)
		}
		if frame.opcode == protocol.OpM2C_MainStoryMonsterInfo {
			if presentation != nil {
				t.Fatal("trial start emitted more than one combat presentation")
			}
			presentation = &protocol.M2C_MainStoryMonsterInfo{}
			if err := proto.Unmarshal(frame.body, presentation); err != nil {
				t.Fatal(err)
			}
		}
	}
	if presentation == nil || len(presentation.MonsterUnitInfoList) != len(start.UnitIdList) {
		t.Fatalf("trial combat presentation = %+v, response units = %v", presentation, start.UnitIdList)
	}
	wantMonsterID := int32(num(tables.trialCopy[int64(start.TrialCopyId)]["MonsterId"]))
	for index, info := range presentation.MonsterUnitInfoList {
		if info.Id != start.UnitIdList[index] || info.MonsterId != wantMonsterID {
			t.Fatalf("trial combat unit[%d] = %+v, response units = %v", index, info, start.UnitIdList)
		}
	}
}

func TestRestorePersistedTrialLayerAfterServerRestart(t *testing.T) {
	oldTables := tables
	if err := loadDatatables("../datatable_json"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tables = oldTables })

	ss := newSession()
	ss.playerID = 7
	ss.mapID = 1000901
	ss.x, ss.y = -4, -2
	ss.signin = &signinState{TrialHighestID: 1003}

	if !restorePersistedTrialLayer(ss) {
		t.Fatal("restart restore did not repair the stale first-layer map")
	}
	if ss.mapID != 1000904 {
		t.Fatalf("restart restored map %d, want pending layer map 1000904", ss.mapID)
	}
	if restorePersistedTrialLayer(ss) {
		t.Fatal("restart restore was not idempotent")
	}
}

func TestNativeTrialEntryRequestResumesPersistedLayer(t *testing.T) {
	oldTables := tables
	if err := loadDatatables("../datatable_json"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tables = oldTables })

	ss := newSession()
	ss.playerID = 8
	ss.jobID = 1
	ss.level = 100
	ss.mapID = 10004
	ss.signin = &signinState{TrialHighestID: 1003}
	conn := &recordingConn{}
	ch := &channel{id: 8, conn: conn, session: ss}

	server := &Server{}
	response, transition := server.onRequestEnterMap(ch, &protocol.C2M_RequestEnterMap{
		RpcId: 44,
		MapId: 1000901,
	})
	if response.Error != 0 || response.Message != "" {
		t.Fatalf("native trial entry response = %+v", response)
	}
	if transition == nil {
		t.Fatal("native trial entry produced no map transition")
	}
	transition()
	if ss.mapID != 1000904 {
		t.Fatalf("native entry saved map %d, want resumed map 1000904", ss.mapID)
	}
	seq, _ := ss.currentMapSceneVersion()
	if !server.finishMapStartup(ch, ss.mapID, seq, false) {
		t.Fatal("native trial scene startup did not complete")
	}

	var change *protocol.M2C_ChangeMap
	var initTrial *protocol.M2C_InitTrialCopyMap
	for _, frame := range decodeRecordedFrames(t, conn.Bytes()) {
		switch frame.opcode {
		case protocol.OpM2C_ChangeMap:
			change = &protocol.M2C_ChangeMap{}
			if err := proto.Unmarshal(frame.body, change); err != nil {
				t.Fatal(err)
			}
		case protocol.OpM2C_InitTrialCopyMap:
			initTrial = &protocol.M2C_InitTrialCopyMap{}
			if err := proto.Unmarshal(frame.body, initTrial); err != nil {
				t.Fatal(err)
			}
		}
	}
	if change == nil || change.MapId != 1000904 {
		t.Fatalf("native entry ChangeMap = %+v, want map 1000904", change)
	}
	if initTrial == nil || initTrial.TrialCopyId != 1004 {
		t.Fatalf("native entry InitTrialCopyMap = %+v, want trial 1004", initTrial)
	}
}

func TestTrialLayerSurvivesTownReturnAndReentry(t *testing.T) {
	oldTables := tables
	if err := loadDatatables("../datatable_json"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tables = oldTables })

	ss := newSession()
	ss.state = sessInGame
	ss.playerID, ss.jobID, ss.level = 901, 1, 100
	ss.mapID = 1000901
	ss.signin = &signinState{}
	ss.bag = make(map[int32]*bagItem)
	ss.worn = make(map[int32]*bagItem)
	ss.skills = make(map[int32]int32)
	ss.tasks = make(map[int32]int32)
	ss.killCount = make(map[int32]int32)
	ch := &channel{id: ss.playerID, conn: &recordingConn{}, session: ss}
	server := &Server{}

	// A completed first layer records the progress before the player returns
	// to town. The next entry request sent by the client still uses 1000901.
	ss.signin.TrialHighestID = 1001
	response, transition := server.onRequestEnterMap(ch, &protocol.C2M_RequestEnterMap{
		RpcId: 1, MapId: 10004,
	})
	if response.Error != 0 || response.Message != "" || transition == nil {
		t.Fatalf("town return response=%+v transition=%v", response, transition != nil)
	}
	transition()
	if ss.mapID != 10004 {
		t.Fatalf("town return map=%d, want 10004", ss.mapID)
	}

	response, transition = server.onRequestEnterMap(ch, &protocol.C2M_RequestEnterMap{
		RpcId: 2, MapId: 1000901,
	})
	if response.Error != 0 || response.Message != "" || transition == nil {
		t.Fatalf("trial reentry response=%+v transition=%v", response, transition != nil)
	}
	transition()
	if ss.mapID != 1000902 {
		t.Fatalf("trial reentry map=%d, want next layer 1000902", ss.mapID)
	}

	// Repeating the same fixed entry request must not rewind the player to the
	// first layer while the persisted highest layer remains 1001.
	response, transition = server.onRequestEnterMap(ch, &protocol.C2M_RequestEnterMap{
		RpcId: 3, MapId: 1000901,
	})
	if response.Error != 0 || response.Message != "" || transition != nil {
		t.Fatalf("trial repeated entry response=%+v transition=%v", response, transition != nil)
	}
	if ss.mapID != 1000902 {
		t.Fatalf("repeated trial entry rewound map=%d, want 1000902", ss.mapID)
	}
}

func TestTrialEntryMovesOnlyRequestingPartyMember(t *testing.T) {
	oldTables := tables
	if err := loadDatatables("../datatable_json"); err != nil {
		t.Fatal(err)
	}
	teamMu.Lock()
	oldTeams, oldSeq := teams, teamSeq
	teams, teamSeq = make(map[int64]*teamState), 1
	teamMu.Unlock()
	t.Cleanup(func() {
		tables = oldTables
		teamMu.Lock()
		teams, teamSeq = oldTeams, oldSeq
		teamMu.Unlock()
	})

	makePlayer := func(id int64) (*session, *channel, *recordingConn) {
		ss := newSession()
		ss.state = sessInGame
		ss.playerID, ss.jobID, ss.level, ss.mapID, ss.teamID = id, 1, 100, 10004, 1
		ss.signin = &signinState{}
		conn := &recordingConn{}
		return ss, &channel{id: id, conn: conn, session: ss}, conn
	}
	leaderSession, leader, leaderConn := makePlayer(101)
	memberSession, member, memberConn := makePlayer(202)
	server := &Server{conns: map[int64]*channel{leader.id: leader, member.id: member}}
	teamMu.Lock()
	teams[1] = &teamState{LeaderId: 101, Members: []int64{101, 202}}
	teamMu.Unlock()

	response, transition := server.onRequestEnterMap(leader, &protocol.C2M_RequestEnterMap{
		RpcId: 81, MapId: 1000901,
	})
	if response.Error != 0 || response.Message != "" {
		t.Fatalf("personal trial entry response=%+v", response)
	}
	if transition == nil {
		t.Fatal("personal trial entry produced no map transition")
	}
	transition()
	if leaderSession.mapID != 1000901 || memberSession.mapID != 10004 {
		t.Fatalf("party maps after personal entry=%d/%d, want 1000901/10004",
			leaderSession.mapID, memberSession.mapID)
	}
	if sameMapSession(leaderSession, memberSession) {
		t.Fatal("trial entrant remained visible to party member")
	}
	teamMu.Lock()
	_, teamStillExists := teams[1]
	teamMu.Unlock()
	if teamStillExists || leaderSession.teamID != 0 || memberSession.teamID != 0 {
		t.Fatalf("trial leader entry did not disband party: exists=%v ids=%d/%d",
			teamStillExists, leaderSession.teamID, memberSession.teamID)
	}

	assertSelfOnlyTeam := func(playerID int64, conn *recordingConn) {
		t.Helper()
		var snapshot *protocol.M2C_TeamMember
		for _, frame := range decodeRecordedFrames(t, conn.Bytes()) {
			if frame.opcode == protocol.OpM2C_ChangeMap && playerID == memberSession.playerID {
				t.Fatal("non-requesting party member was moved into personal trial")
			}
			if frame.opcode != protocol.OpM2C_TeamMember {
				continue
			}
			snapshot = &protocol.M2C_TeamMember{}
			if err := proto.Unmarshal(frame.body, snapshot); err != nil {
				t.Fatal(err)
			}
		}
		if snapshot == nil || snapshot.LeaderId != playerID ||
			len(snapshot.UnitIds) != 1 || snapshot.UnitIds[0] != playerID {
			t.Fatalf("player %d private-scene team snapshot=%+v", playerID, snapshot)
		}
	}
	assertSelfOnlyTeam(leaderSession.playerID, leaderConn)
	assertSelfOnlyTeam(memberSession.playerID, memberConn)
}

func TestTrialDropProcessesEverySubset(t *testing.T) {
	oldTables := tables
	tables = &datatables{
		parentset: map[int64]map[string]interface{}{
			1: {"SubsetArr": []interface{}{
				map[string]interface{}{"_Id": int64(11)},
				map[string]interface{}{"_Id": int64(12)},
			}},
		},
		sonSet: map[int64]map[string]interface{}{
			11: {"DropArr": []interface{}{map[string]interface{}{
				"_Id": int64(110203), "Weight": int64(1), "MinCount": int64(5), "MaxCount": int64(5),
			}}},
			12: {"DropArr": []interface{}{map[string]interface{}{
				"_Id": int64(20001), "Weight": int64(1), "MinCount": int64(2), "MaxCount": int64(2),
			}}},
		},
	}
	t.Cleanup(func() { tables = oldTables })

	reward := newTrialReward()
	rollParentset(1, &reward)
	if reward.coin != 5 || reward.items[20001] != 2 {
		t.Fatalf("drop reward = %+v", reward)
	}
}

func TestTrialVictoryRecordsRewardAndForcesNextLayer(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.Progression.ExperienceGainPercent = 200
	})
	oldTables := tables
	if err := loadDatatables("../datatable_json"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tables = oldTables })

	ss := newSession()
	ss.playerID = 10
	ss.jobID = 1
	ss.level = 4000
	ss.mapID = 1000901
	ss.bag = make(map[int32]*bagItem)
	ss.worn = make(map[int32]*bagItem)
	ss.skills = make(map[int32]int32)
	ss.tasks = make(map[int32]int32)
	ss.killCount = make(map[int32]int32)
	ss.signin = &signinState{}
	monsterRow := tables.monsterBase[30021]
	monsterExp := num(monsterRow["Exp"])
	battle := &battleState{
		region:      1001,
		mapID:       1000901,
		battleType:  trialBattleType,
		trialCopyID: 1001,
		playerHP:    ss.playerMaxHp(),
		playerMaxHP: ss.playerMaxHp(),
		playerMP:    ss.playerMaxMp(),
		playerMaxMP: ss.playerMaxMp(),
		owner:       ss,
		monsters: []*monsterUnit{
			{id: 1000000001, monsterID: 30021, exp: monsterExp, hp: 0, maxHP: 500, alive: false},
			{id: 1000000002, monsterID: 30021, exp: monsterExp, hp: 0, maxHP: 500, alive: false},
		},
	}
	ss.battle = battle
	conn := &recordingConn{}
	ch := &channel{id: 5, conn: conn, session: ss}
	server := &Server{}
	server.emitVictory(ch, battle)

	if ss.battle != nil || ss.signin.TrialHighestID != 1001 || ss.mapID != 1000902 {
		t.Fatalf("victory state battle=%p signin=%+v map=%d", ss.battle, ss.signin, ss.mapID)
	}
	seq, _ := ss.currentMapSceneVersion()
	if !server.finishMapStartup(ch, ss.mapID, seq, false) {
		t.Fatal("next trial scene startup did not complete")
	}
	wantLevel, wantExp := int32(4000), experienceAfterMultiplier(ss, monsterExp*2)
	for wantExp >= expNeed(wantLevel) && wantLevel < transLevelCap(0) {
		wantExp -= expNeed(wantLevel)
		wantLevel++
	}
	if ss.level != wantLevel || ss.exp != wantExp {
		t.Fatalf("trial progress level=%d exp=%d, want level=%d exp=%d", ss.level, ss.exp, wantLevel, wantExp)
	}
	foundVictory := false
	foundNextTrial := false
	for _, frame := range decodeRecordedFrames(t, conn.Bytes()) {
		switch frame.opcode {
		case protocol.OpM2C_BattleVictory:
			message := &protocol.M2C_BattleVictory{}
			if err := proto.Unmarshal(frame.body, message); err != nil {
				t.Fatal(err)
			}
			foundVictory = message.BattleType == trialBattleType
		case protocol.OpM2C_InitTrialCopyMap:
			message := &protocol.M2C_InitTrialCopyMap{}
			if err := proto.Unmarshal(frame.body, message); err != nil {
				t.Fatal(err)
			}
			foundNextTrial = message.TrialCopyId == 1002
		}
	}
	if !foundVictory || !foundNextTrial {
		t.Fatalf("victory frames missing: victory=%v nextTrial=%v opcodes=%v",
			foundVictory, foundNextTrial, recordedOpcodes(t, conn.Bytes()))
	}
}

func TestTrialRemainsPersonalForPartyMembersOnSameLayer(t *testing.T) {
	oldTables := tables
	if err := loadDatatables("../datatable_json"); err != nil {
		t.Fatal(err)
	}
	teamMu.Lock()
	oldTeams, oldSeq := teams, teamSeq
	teams, teamSeq = make(map[int64]*teamState), 1
	teamMu.Unlock()
	t.Cleanup(func() {
		tables = oldTables
		teamMu.Lock()
		teams, teamSeq = oldTeams, oldSeq
		teamMu.Unlock()
	})

	makeTrialPlayer := func(playerID int64) (*session, *channel, *recordingConn) {
		ss := newSession()
		ss.playerID = playerID
		ss.jobID = 1
		ss.level = 4000
		ss.mapID = 1000901
		ss.teamID = 1
		ss.bag = make(map[int32]*bagItem)
		ss.worn = make(map[int32]*bagItem)
		ss.skills = make(map[int32]int32)
		ss.tasks = make(map[int32]int32)
		ss.killCount = make(map[int32]int32)
		ss.signin = &signinState{}
		conn := &recordingConn{}
		return ss, &channel{id: playerID, conn: conn, session: ss}, conn
	}
	leaderSession, leader, leaderConn := makeTrialPlayer(101)
	followerSession, follower, followerConn := makeTrialPlayer(202)
	server := &Server{conns: map[int64]*channel{leader.id: leader, follower.id: follower}}
	teamMu.Lock()
	teams[1] = &teamState{LeaderId: 101, Members: []int64{101, 202}}
	teamMu.Unlock()

	if sameMapSession(leaderSession, followerSession) {
		t.Fatal("two players on one trial layer shared a visible scene")
	}
	server.broadcastUnitsInMapExcept(follower)
	if len(leaderConn.Bytes()) != 0 {
		t.Fatalf("private trial player received another player's scene packet: %v",
			recordedOpcodes(t, leaderConn.Bytes()))
	}

	trialRow := tables.trialCopy[1001]
	monsterID := int32(num(trialRow["MonsterId"]))
	monsterCount := int(num(trialRow["MonsterCount"]))
	if monsterCount < 1 {
		monsterCount = 1
	}
	leaderStart := server.onStartTrialCopyFight(leader, &protocol.C2M_StartTrialCopyFight{RpcId: 71}).(*protocol.M2C_StartTrialCopyFight)
	followerStart := server.onStartTrialCopyFight(follower, &protocol.C2M_StartTrialCopyFight{RpcId: 72}).(*protocol.M2C_StartTrialCopyFight)
	for name, start := range map[string]*protocol.M2C_StartTrialCopyFight{
		"leader": leaderStart, "follower": followerStart,
	} {
		if start.Message != "" || start.Error != 0 || start.TrialCopyId != 1001 || len(start.UnitIdList) != monsterCount {
			t.Fatalf("%s personal trial start response=%+v", name, start)
		}
	}
	if leaderSession.battle == nil || followerSession.battle == nil {
		t.Fatalf("personal trial battle states leader=%p follower=%p", leaderSession.battle, followerSession.battle)
	}
	if leaderSession.battle.party != nil || followerSession.battle.party != nil {
		t.Fatal("personal trial unexpectedly installed a party battle")
	}
	if leaderSession.battle.monsters[0] == followerSession.battle.monsters[0] {
		t.Fatal("personal trials shared a monster state object")
	}
	followerHP := followerSession.battle.monsters[0].hp
	leaderSession.battle.monsters[0].hp--
	if followerSession.battle.monsters[0].hp != followerHP {
		t.Fatal("damage in leader trial changed follower monster HP")
	}

	for _, recipient := range []*channel{leader, follower} {
		presentations := 0
		for _, frame := range decodeRecordedFrames(t, recipient.conn.(*recordingConn).Bytes()) {
			if frame.opcode != protocol.OpM2C_MainStoryMonsterInfo {
				continue
			}
			presentations++
			message := &protocol.M2C_MainStoryMonsterInfo{}
			if err := proto.Unmarshal(frame.body, message); err != nil {
				t.Fatal(err)
			}
			if len(message.MonsterUnitInfoList) != monsterCount {
				t.Fatalf("recipient %d presentation=%+v", recipient.session.playerID, message)
			}
			for _, unit := range message.MonsterUnitInfoList {
				if unit.MonsterId != monsterID {
					t.Fatalf("recipient %d combat unit=%+v", recipient.session.playerID, unit)
				}
			}
		}
		if presentations != 1 {
			t.Fatalf("recipient %d combat presentations=%d, want 1",
				recipient.session.playerID, presentations)
		}
	}

	for _, monster := range leaderSession.battle.monsters {
		monster.hp = 0
		monster.alive = false
	}
	server.emitVictory(leader, leaderSession.battle)

	if leaderSession.battle != nil || leaderSession.mapID != 1000902 || leaderSession.signin.TrialHighestID != 1001 {
		t.Fatalf("leader personal victory battle=%p map=%d signin=%+v",
			leaderSession.battle, leaderSession.mapID, leaderSession.signin)
	}
	if followerSession.battle == nil || followerSession.mapID != 1000901 ||
		followerSession.signin.TrialHighestID != 0 || followerSession.exp != 0 {
		t.Fatalf("leader victory changed follower battle=%p map=%d exp=%d signin=%+v",
			followerSession.battle, followerSession.mapID, followerSession.exp, followerSession.signin)
	}
	for _, frame := range decodeRecordedFrames(t, followerConn.Bytes()) {
		if frame.opcode == protocol.OpM2C_BattleVictory || frame.opcode == protocol.OpM2C_SendReward ||
			frame.opcode == protocol.OpM2C_ChangeMap {
			t.Fatalf("follower received leader settlement opcode %d", frame.opcode)
		}
	}
}

func TestTrialBagStagingIsAtomicWhenFull(t *testing.T) {
	oldTables := tables
	tables = &datatables{materialBase: map[int64]map[string]interface{}{
		20001: {"MaxAmount": int64(999)},
		20002: {"MaxAmount": int64(255)},
	}}
	t.Cleanup(func() { tables = oldTables })

	ss := newSession()
	ss.bag = make(map[int32]*bagItem)
	for i := int32(0); i < bagSlotCount; i++ {
		ss.bag[i] = &bagItem{ItemId: 20002, ItemType: int32(protocol.ItemType_MaterialsItem), Count: 255}
	}
	staged, ok := stageTrialItems(ss, map[int32]int64{20001: 1})
	if ok || staged != nil {
		t.Fatalf("full bag staging unexpectedly succeeded: ok=%v slots=%d", ok, len(staged))
	}
	if len(ss.bag) != int(bagSlotCount) {
		t.Fatalf("live bag changed to %d slots", len(ss.bag))
	}
	for i := int32(0); i < bagSlotCount; i++ {
		if ss.bag[i].ItemId != 20002 || ss.bag[i].Count != 255 {
			t.Fatalf("live bag slot %d was mutated: %+v", i, ss.bag[i])
		}
	}
}

func TestApplyTrialRewardMailsItemsWhenBagIsFull(t *testing.T) {
	ss := newSession()
	ss.playerID = 7
	ss.bag = make(map[int32]*bagItem)
	for index := int32(0); index < bagSlotCount; index++ {
		ss.bag[index] = &bagItem{ItemId: 20002, ItemType: int32(protocol.ItemType_MaterialsItem), Count: 255}
	}
	conn := &recordingConn{}
	ch := &channel{id: 7, conn: conn, session: ss}
	server := &Server{}
	reward := trialReward{coin: 13, yuanBao: 5, items: map[int32]int64{20001: 2, 20003: 1}}
	got := server.applyTrialReward(ch, reward, false)
	if len(got) != 3 {
		t.Fatalf("reward popup item count=%d, want 3 popup items", len(got))
	}
	if ss.coin != 13 || ss.yuanBao != 5 {
		t.Fatalf("currencies coin/yuanbao=%d/%d, want 13/5", ss.coin, ss.yuanBao)
	}
	if len(ss.mails) != 1 || ss.mails[0].State != 0 || len(ss.mails[0].Items) != 2 {
		t.Fatalf("fallback mail=%+v, want one unclaimed mail with two items", ss.mails)
	}
	if ss.mails[0].Items[0].ItemId != 20001 || ss.mails[0].Items[0].Count != 2 ||
		ss.mails[0].Items[1].ItemId != 20003 || ss.mails[0].Items[1].Count != 1 {
		t.Fatalf("fallback mail items=%+v", ss.mails[0].Items)
	}
	if len(ss.bag) != int(bagSlotCount) {
		t.Fatalf("full bag changed slot count=%d", len(ss.bag))
	}
}

func TestTrialDailyRewardOnceAndBagFailureDoesNotConsumeClaim(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.Progression.ExperienceGainPercent = 200
	})
	oldTables := tables
	baseTables := &datatables{
		trialCopy: map[int64]map[string]interface{}{
			1001: {"MonsterId": int64(30021), "MonsterCount": int64(1)},
		},
		monsterBase: map[int64]map[string]interface{}{
			30021: {"Exp": int64(10)},
		},
	}
	tables = baseTables
	t.Cleanup(func() { tables = oldTables })

	ss := newSession()
	ss.playerID = 8
	ss.level = 100
	ss.bag = make(map[int32]*bagItem)
	ss.worn = make(map[int32]*bagItem)
	ss.signin = &signinState{TrialHighestID: 1001}
	ss.expMult = 4
	ss.expMultUntil = time.Now().Add(time.Hour).UnixMilli()
	conn := &recordingConn{}
	ch := &channel{id: 3, conn: conn, session: ss}
	server := &Server{}

	response := server.onGetAllTrialCopyReward(ch, &protocol.C2M_GetAllTrialCopyReword{RpcId: 66}).(*protocol.M2C_GetAllTrialCopyReword)
	if !strings.Contains(response.Message, "成功") || ss.exp != 80 || ss.signin.TrialRewardDay != time.Now().Format("20060102") {
		t.Fatalf("first claim response=%+v exp=%d signin=%+v", response, ss.exp, ss.signin)
	}
	foundScaledReward := false
	for _, frame := range decodeRecordedFrames(t, conn.Bytes()) {
		if frame.opcode != protocol.OpM2C_SendReward {
			continue
		}
		message := &protocol.M2C_SendReward{}
		if err := proto.Unmarshal(frame.body, message); err != nil {
			t.Fatal(err)
		}
		foundScaledReward = true
		if message.Exp != 80 {
			t.Fatalf("reward UI exp = %d, want 200%% rate and 4x card scaled 80", message.Exp)
		}
	}
	if !foundScaledReward {
		t.Fatal("daily claim did not emit M2C_SendReward")
	}
	response = server.onGetAllTrialCopyReward(ch, &protocol.C2M_GetAllTrialCopyReword{RpcId: 67}).(*protocol.M2C_GetAllTrialCopyReword)
	if !strings.Contains(response.Message, "已经领取") || ss.exp != 80 {
		t.Fatalf("second claim response=%+v exp=%d", response, ss.exp)
	}

	// A deterministic material drop requires a new slot. With all 200 slots
	// full, no exp/date/item mutation may occur.
	tables.parentset = map[int64]map[string]interface{}{
		1: {"SubsetArr": []interface{}{map[string]interface{}{"_Id": int64(11)}}},
	}
	tables.sonSet = map[int64]map[string]interface{}{
		11: {"DropArr": []interface{}{map[string]interface{}{
			"_Id": int64(20001), "Weight": int64(1), "MinCount": int64(1), "MaxCount": int64(1),
		}}},
	}
	tables.monsterBase[30021]["Dropasubset"] = int64(1)
	tables.materialBase = map[int64]map[string]interface{}{
		20001: {"MaxAmount": int64(999)},
		20002: {"MaxAmount": int64(255)},
	}
	full := newSession()
	full.playerID = 9
	full.level = 100
	full.bag = make(map[int32]*bagItem)
	full.worn = make(map[int32]*bagItem)
	full.signin = &signinState{TrialHighestID: 1001}
	for i := int32(0); i < bagSlotCount; i++ {
		full.bag[i] = &bagItem{ItemId: 20002, ItemType: int32(protocol.ItemType_MaterialsItem), Count: 255}
	}
	fullChannel := &channel{id: 4, conn: &recordingConn{}, session: full}
	failed := server.onGetAllTrialCopyReward(fullChannel, &protocol.C2M_GetAllTrialCopyReword{RpcId: 68}).(*protocol.M2C_GetAllTrialCopyReword)
	if !strings.Contains(failed.Message, "背包空间不足") || full.exp != 0 || full.signin.TrialRewardDay != "" {
		t.Fatalf("failed claim mutated state: response=%+v exp=%d signin=%+v", failed, full.exp, full.signin)
	}
}
