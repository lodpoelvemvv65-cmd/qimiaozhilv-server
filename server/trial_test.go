package main

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

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
	(&Server{}).emitVictory(ch, battle)

	if ss.battle != nil || ss.signin.TrialHighestID != 1001 || ss.mapID != 1000902 {
		t.Fatalf("victory state battle=%p signin=%+v map=%d", ss.battle, ss.signin, ss.mapID)
	}
	wantLevel, wantExp := int32(4000), monsterExp*2
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

func TestPartyTrialStartsBothClientsAndSettlesOneSharedFight(t *testing.T) {
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
	rejected := server.onStartTrialCopyFight(follower, &protocol.C2M_StartTrialCopyFight{RpcId: 70}).(*protocol.M2C_StartTrialCopyFight)
	if rejected.Message == "" || rejected.Error != 0 || followerSession.battle != nil {
		t.Fatalf("follower trial start was not safely rejected: response=%+v battle=%+v", rejected, followerSession.battle)
	}

	trialRow := tables.trialCopy[1001]
	monsterID := int32(num(trialRow["MonsterId"]))
	monsterCount := int(num(trialRow["MonsterCount"]))
	if monsterCount < 1 {
		monsterCount = 1
	}
	soloUnits := leaderSession.buildMonsterUnitsFromRoster([]int32{monsterID}, []int{monsterCount})
	start := server.onStartTrialCopyFight(leader, &protocol.C2M_StartTrialCopyFight{RpcId: 71}).(*protocol.M2C_StartTrialCopyFight)
	if start.Message != "" || start.Error != 0 || start.TrialCopyId != 1001 || len(start.UnitIdList) != monsterCount {
		t.Fatalf("leader trial start response=%+v", start)
	}
	if leaderSession.battle == nil || followerSession.battle == nil {
		t.Fatalf("party trial battle states leader=%p follower=%p", leaderSession.battle, followerSession.battle)
	}
	party := leaderSession.battle.party
	if party == nil || followerSession.battle.party != party ||
		leaderSession.battle.monsters[0] != followerSession.battle.monsters[0] {
		t.Fatal("party trial clients do not share one authoritative encounter")
	}
	for i, monster := range leaderSession.battle.monsters {
		wantHP := int32(float64(soloUnits[i].maxHP)*1.5 + 0.5)
		if monster.maxHP != wantHP || monster.hp != wantHP {
			t.Fatalf("team-scaled monster[%d] hp=%d/%d, want %d", i, monster.hp, monster.maxHP, wantHP)
		}
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
			if len(message.MonsterUnitInfoList) != len(start.UnitIdList) {
				t.Fatalf("recipient %d presentation=%+v", recipient.session.playerID, message)
			}
			for i, unit := range message.MonsterUnitInfoList {
				if unit.Id != start.UnitIdList[i] || unit.MonsterId != monsterID {
					t.Fatalf("recipient %d combat unit[%d]=%+v response=%v",
						recipient.session.playerID, i, unit, start.UnitIdList)
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
	party.mu.Lock()
	party.settled = true
	for _, battle := range party.members {
		battle.ended = true
	}
	party.mu.Unlock()
	server.finishPartyVictory(party)

	if leaderSession.battle != nil || followerSession.battle != nil {
		t.Fatalf("party trial battle was not cleared: leader=%p follower=%p", leaderSession.battle, followerSession.battle)
	}
	if leaderSession.mapID != 1000902 || followerSession.mapID != 1000902 {
		t.Fatalf("party maps = %d/%d, want both 1000902", leaderSession.mapID, followerSession.mapID)
	}
	for _, member := range []*session{leaderSession, followerSession} {
		if member.exp == 0 || member.signin.TrialHighestID != 1001 {
			t.Fatalf("member %d progress level=%d exp=%d signin=%+v",
				member.playerID, member.level, member.exp, member.signin)
		}
	}
	for index, conn := range []*recordingConn{leaderConn, followerConn} {
		member := []*session{leaderSession, followerSession}[index]
		foundReward, foundVictory, foundChange, foundTrialMap := false, false, false, false
		for _, frame := range decodeRecordedFrames(t, conn.Bytes()) {
			switch frame.opcode {
			case protocol.OpM2C_SendReward:
				message := &protocol.M2C_SendReward{}
				if err := proto.Unmarshal(frame.body, message); err != nil {
					t.Fatal(err)
				}
				foundReward = message.Exp > 0 && message.ActorId == member.playerID
			case protocol.OpM2C_BattleVictory:
				foundVictory = true
			case protocol.OpM2C_ChangeMap:
				message := &protocol.M2C_ChangeMap{}
				if err := proto.Unmarshal(frame.body, message); err != nil {
					t.Fatal(err)
				}
				foundChange = message.MapId == wireMapID(1000902)
			case protocol.OpM2C_InitTrialCopyMap:
				message := &protocol.M2C_InitTrialCopyMap{}
				if err := proto.Unmarshal(frame.body, message); err != nil {
					t.Fatal(err)
				}
				foundTrialMap = message.TrialCopyId == 1002
			}
		}
		if !foundReward || !foundVictory || !foundChange || !foundTrialMap {
			t.Fatalf("member %d frames reward=%v victory=%v change=%v trialMap=%v opcodes=%v",
				member.playerID, foundReward, foundVictory, foundChange, foundTrialMap, recordedOpcodes(t, conn.Bytes()))
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

func TestTrialDailyRewardOnceAndBagFailureDoesNotConsumeClaim(t *testing.T) {
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
	if !strings.Contains(response.Message, "成功") || ss.exp != 40 || ss.signin.TrialRewardDay != time.Now().Format("20060102") {
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
		if message.Exp != 40 {
			t.Fatalf("reward UI exp = %d, want experience-card-scaled 40", message.Exp)
		}
	}
	if !foundScaledReward {
		t.Fatal("daily claim did not emit M2C_SendReward")
	}
	response = server.onGetAllTrialCopyReward(ch, &protocol.C2M_GetAllTrialCopyReword{RpcId: 67}).(*protocol.M2C_GetAllTrialCopyReword)
	if !strings.Contains(response.Message, "已经领取") || ss.exp != 40 {
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
