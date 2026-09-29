package main

import (
	"fmt"
	"math"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func loadOnlineTablesForTest(t *testing.T) {
	t.Helper()
	old := tables
	if err := loadDatatables("../datatable_json"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tables = old })
}

func TestClientJobIdsMapToFourProfessionFamilies(t *testing.T) {
	loadOnlineTablesForTest(t)
	wantTypes := []int32{1, 1, 2, 2, 3, 3, 4, 4}
	wantSkills := []int32{100001, 100001, 200001, 200001, 300001, 300001, 400001, 400001}
	for index := range wantTypes {
		jobID := int32(index + 1)
		if got := jobTypeOf(jobID); got != wantTypes[index] {
			t.Errorf("jobTypeOf(%d) = %d, want %d", jobID, got, wantTypes[index])
		}
		if got := baseSkillOfJob(jobID); got != wantSkills[index] {
			t.Errorf("baseSkillOfJob(%d) = %d, want %d", jobID, got, wantSkills[index])
		}
		if got, want := numf(roleRow(jobID)["Hp1"]), numf(tables.roleGrowth[int64(wantTypes[index])]["Hp1"]); got != want {
			t.Errorf("job %d did not use RoleGrowth %d", jobID, wantTypes[index])
		}
		ss := newSession()
		ss.playerID, ss.jobID, ss.skinID, ss.level = 100+int64(jobID), jobID, jobID, 1
		ss.worn = make(map[int32]*bagItem)
		character := buildUnitCharacter(ss)
		if character.JobId != jobID || character.SkinId != jobID {
			t.Errorf("job %d lost concrete identity: JobId=%d SkinId=%d", jobID, character.JobId, character.SkinId)
		}
	}
}

func TestClientExperienceCurveAndWireUnit(t *testing.T) {
	// 线上 1~4 级门槛为 0：抓包四个整分钟节拍点（每跳 24560，1 级新角色
	// 第 1/2/3/4 跳后 56/67/74/79 级、余 868/264/757/3348）只有
	// floor(Lv³/100) 不设下限能全部满足，详见 expNeed 的说明。
	for level, want := range map[int32]int64{
		1: 0, 4: 0, 5: 100, 7: 300, 8: 500, 9: 700,
		10: 1000, 100: 1000000, 4000: 64000000000,
	} {
		if got := expNeed(level); got != want {
			t.Errorf("expNeed(%d) = %d, want %d", level, got, want)
		}
	}
	ss := newSession()
	ss.playerID, ss.level, ss.exp = 7, 100, 4321
	conn := &recordingConn{}
	ch := &channel{id: 1, conn: conn, session: ss}
	(&Server{}).pushPlayerProgress(ch)
	found := false
	for _, frame := range decodeRecordedFrames(t, conn.Bytes()) {
		if frame.opcode != protocol.OpM2C_SyncUnitAttribute {
			continue
		}
		message := &protocol.M2C_SyncUnitAttribute{}
		if err := proto.Unmarshal(frame.body, message); err != nil {
			t.Fatal(err)
		}
		if message.NumericType == 1027 {
			found = true
			if math.Abs(float64(message.Value)-43.21) > 0.00001 {
				t.Fatalf("wire exp = %v, want scaled 43.21", message.Value)
			}
		}
	}
	if !found {
		t.Fatal("missing NumericType 1027 progress push")
	}
}

func TestUnitCharacterCarriesAuthoritativeCurrentHealthAndMana(t *testing.T) {
	loadOnlineTablesForTest(t)
	ss := newSession()
	ss.playerID, ss.jobID, ss.skinID, ss.level = 7, 1, 1, 25
	ss.worn = make(map[int32]*bagItem)
	ss.hp, ss.mp = 31, 17

	character := buildUnitCharacter(ss)
	if character.Hp != 31 || character.Mp != 17 {
		t.Fatalf("UnitCharacter current resources=%d/%d, want 31/17", character.Hp, character.Mp)
	}
	if character.MaxHp != ss.playerMaxHp() || character.MaxMp != ss.playerMaxMp() {
		t.Fatalf("UnitCharacter max resources=%d/%d, want %d/%d",
			character.MaxHp, character.MaxMp, ss.playerMaxHp(), ss.playerMaxMp())
	}
}

func TestUnitCharacterLeaderIdUsesPartyLeader(t *testing.T) {
	teamMu.Lock()
	oldTeams, oldSeq := teams, teamSeq
	teams = map[int64]*teamState{
		1: {LeaderId: 101, Members: []int64{101, 202}},
	}
	teamMu.Unlock()
	t.Cleanup(func() {
		teamMu.Lock()
		teams, teamSeq = oldTeams, oldSeq
		teamMu.Unlock()
	})

	leader := newSession()
	leader.playerID = 101
	member := newSession()
	member.playerID = 202
	solo := newSession()
	solo.playerID = 303

	if got := buildUnitCharacter(leader).LeaderId; got != 101 {
		t.Fatalf("leader UnitCharacter.LeaderId=%d, want 101", got)
	}
	if got := buildUnitCharacter(member).LeaderId; got != 101 {
		t.Fatalf("member UnitCharacter.LeaderId=%d, want party leader 101", got)
	}
	if got := buildUnitCharacter(solo).LeaderId; got != 303 {
		t.Fatalf("solo UnitCharacter.LeaderId=%d, want self", got)
	}
}

func TestPlayerLevelSyncSendsTransmigrationBeforeLevel(t *testing.T) {
	loadOnlineTablesForTest(t)

	for _, fullSnapshot := range []bool{false, true} {
		name := "progress"
		if fullSnapshot {
			name = "full_snapshot"
		}
		t.Run(name, func(t *testing.T) {
			ss := newSession()
			ss.playerID, ss.jobID, ss.skinID = 7, 1, 1
			ss.trans, ss.level = 2, 17004
			ss.worn = make(map[int32]*bagItem)
			conn := &recordingConn{}
			ch := &channel{id: 1, conn: conn, session: ss}
			server := &Server{}
			if fullSnapshot {
				server.pushPlayerAttrsTo(ch, ss, ss.playerMaxHp(), ss.playerMaxMp())
			} else {
				server.pushPlayerProgress(ch)
			}

			transIndex, levelIndex := -1, -1
			for index, frame := range decodeRecordedFrames(t, conn.Bytes()) {
				if frame.opcode != protocol.OpM2C_SyncUnitAttribute {
					continue
				}
				message := &protocol.M2C_SyncUnitAttribute{}
				if err := proto.Unmarshal(frame.body, message); err != nil {
					t.Fatal(err)
				}
				switch message.NumericType {
				case 1029:
					transIndex = index
					if message.Value != 2 {
						t.Fatalf("wire transmigration = %v, want 2", message.Value)
					}
				case 1026:
					levelIndex = index
					if message.Value != 17004 {
						t.Fatalf("wire cumulative level = %v, want 17004", message.Value)
					}
				}
			}
			if transIndex < 0 || levelIndex < 0 {
				t.Fatalf("missing level sync: transmigration index=%d level index=%d", transIndex, levelIndex)
			}
			if transIndex >= levelIndex {
				t.Fatalf("transmigration must precede level: transmigration index=%d level index=%d", transIndex, levelIndex)
			}
		})
	}
}

func TestInitialPlayerAttributesUseOnlineBatchSnapshot(t *testing.T) {
	loadOnlineTablesForTest(t)
	ss := newSession()
	ss.playerID, ss.jobID, ss.skinID = 7, 1, 1
	ss.trans, ss.level, ss.exp, ss.energy = 2, 17004, 4321, 991
	ss.coin, ss.yuanBao, ss.voucher = 2085, 12, 34
	ss.hp, ss.mp = 31, 17
	ss.worn = make(map[int32]*bagItem)
	conn := &recordingConn{}
	ch := &channel{id: 1, conn: conn, session: ss}
	(&Server{}).pushInitialPlayerAttrs(ch)

	frames := decodeRecordedFrames(t, conn.Bytes())
	if len(frames) != 1 || frames[0].opcode != protocol.OpM2C_SyncUnitAttributeList {
		t.Fatalf("initial numeric frames = %+v, want one 20170 frame", frames)
	}
	attributes := decodeAttributeMapList(t, frames[0].body, 2)
	want := map[int32]float32{
		1001: 31, 1002: float32(ss.playerMaxHp()), 1003: 17, 1004: float32(ss.playerMaxMp()),
		1026: 17004, 1027: 43.21, 1028: 2085, 1029: 2, 1039: 991,
	}
	indexes := make(map[int32]int)
	for index := range attributes {
		key := attributes[index].Key
		indexes[key] = index
		value, ok := want[key]
		if !ok {
			continue
		}
		if math.Abs(float64(attributes[index].Value-value)) > 0.00001 {
			t.Errorf("NumericType %d = %v, want %v", key, attributes[index].Value, value)
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Fatalf("initial snapshot is missing attributes %v", want)
	}
	if indexes[1029] >= indexes[1026] {
		t.Fatalf("transmigration index=%d must precede level index=%d", indexes[1029], indexes[1026])
	}
}

func TestExistingRoleLoginMapSnapshotClearsSceneBeforeChangeMap(t *testing.T) {
	loadOnlineTablesForTest(t)
	ss := newSession()
	ss.playerID, ss.jobID, ss.skinID, ss.level = 7, 1, 1, 15
	ss.mapID = 10004
	ss.worn = make(map[int32]*bagItem)
	conn := &recordingConn{}
	ch := &channel{id: 1, conn: conn, session: ss}
	(&Server{}).pushLoginMapSnapshot(ch, &protocol.M2C_ChangeMap{
		MapId: wireMapID(ss.mapID), ActorId: ss.playerID,
	}, false)

	frames := decodeRecordedFrames(t, conn.Bytes())
	want := []uint16{protocol.OpM2C_UnitsInMap, protocol.OpM2C_ChangeMap}
	if len(frames) != len(want) {
		t.Fatalf("login map frame count = %d, want %d", len(frames), len(want))
	}
	for index, opcode := range want {
		if frames[index].opcode != opcode {
			t.Fatalf("login map frame %d opcode = %d, want %d", index, frames[index].opcode, opcode)
		}
	}
	var clear protocol.M2C_UnitsInMap
	if err := proto.Unmarshal(frames[0].body, &clear); err != nil {
		t.Fatal(err)
	}
	if clear.ActorId != ss.playerID || len(bytesFields(t, frames[0].body, 1)) != 0 ||
		len(bytesFields(t, frames[0].body, 2)) != 0 {
		t.Fatalf("login scene clear = %+v, want empty lists for player %d", &clear, ss.playerID)
	}
}

func TestFirstEntryLoginMapSnapshotMatchesOnlineOrder(t *testing.T) {
	loadOnlineTablesForTest(t)
	ss := newSession()
	ss.playerID, ss.jobID, ss.skinID, ss.level = 7, 1, 1, 1
	ss.mapID = 1000601
	ss.worn = make(map[int32]*bagItem)
	conn := &recordingConn{}
	ch := &channel{id: 1, conn: conn, session: ss}
	server := &Server{}
	seq := ss.markMapChange()

	server.pushLoginMapSnapshot(ch, &protocol.M2C_ChangeMap{
		MapId: ss.mapID, ActorId: ss.playerID,
	}, true)
	if !server.finishMapStartupWithOptions(ch, ss.mapID, seq, mapStartupOptions{}) {
		t.Fatal("first-entry delayed startup did not complete")
	}

	frames := decodeRecordedFrames(t, conn.Bytes())
	want := []uint16{
		protocol.OpM2C_UnitsInMap,
		protocol.OpM2C_ChangeMap,
		protocol.OpM2C_SyncUnitAttributeList,
		protocol.OpM2C_InitMainStoryMap,
		protocol.OpM2C_SyncUnitAttribute,
		protocol.OpM2C_SyncUnitAttribute,
		protocol.OpM2C_SyncUnitAttribute,
		protocol.OpM2C_SyncUnitAttribute,
	}
	if len(frames) != len(want) {
		t.Fatalf("first-entry frame count = %d, want %d: %+v", len(frames), len(want), frames)
	}
	for index, opcode := range want {
		if frames[index].opcode != opcode {
			t.Fatalf("first-entry frame %d opcode = %d, want %d", index, frames[index].opcode, opcode)
		}
	}
	if len(bytesFields(t, frames[0].body, 1)) != 0 || len(bytesFields(t, frames[0].body, 2)) != 0 {
		t.Fatal("first-entry UnitsInMap must have empty Unit and PvpUnit lists")
	}
	var change protocol.M2C_ChangeMap
	if err := proto.Unmarshal(frames[1].body, &change); err != nil {
		t.Fatal(err)
	}
	if change.MapId != 1000601 || change.X != 0 || change.Y != 0 {
		t.Fatalf("first-entry ChangeMap = %+v, want map 1000601 at origin", &change)
	}
	var story protocol.M2C_InitMainStoryMap
	if err := proto.Unmarshal(frames[3].body, &story); err != nil {
		t.Fatal(err)
	}
	if story.MainStoryId != 1001 {
		t.Fatalf("first-entry MainStoryId = %d, want 1001", story.MainStoryId)
	}
}

func TestFirstEntryReadyStateDefersComponentDependentPushes(t *testing.T) {
	loadOnlineTablesForTest(t)
	var initialTaskID int32
	for id, row := range tables.taskBase {
		if int32(num(row["PreTaskId"])) == 0 && int32(num(row["GiveNPCId"])) == 0 &&
			int32(num(row["SubmitNPCId"])) != 0 && int32(num(row["TargetType"])) != 0 {
			initialTaskID = int32(id)
			break
		}
	}
	if initialTaskID == 0 {
		t.Fatal("online tables contain no initial task")
	}
	now := time.Now().UTC()
	ss := newSession()
	ss.playerID, ss.jobID, ss.skinID, ss.level = 7, 1, 1, 1
	ss.initialTaskUIPending = true
	ss.tasks = map[int32]int32{initialTaskID: taskStateWaiting}
	ss.pet = newPet()
	ss.signin = &signinState{LastMonth: now.Format("200601"), MonthGotMonth: now.Format("200601")}
	ss.worn = make(map[int32]*bagItem)
	conn := &recordingConn{}
	ch := &channel{id: 1, conn: conn, session: ss}

	(&Server{}).pushFirstEntryReadyState(ch)

	frames := decodeRecordedFrames(t, conn.Bytes())
	want := []uint16{
		protocol.OpM2C_OpenTaskUI,
		protocol.OpM2C_SyncPet,
		protocol.OpM2C_SendActiveInfo,
		protocol.OpM2C_SyncUnitAttributeList,
	}
	if len(frames) != len(want) {
		t.Fatalf("first-entry ready frame count = %d, want %d: %+v", len(frames), len(want), frames)
	}
	for index, opcode := range want {
		if frames[index].opcode != opcode {
			t.Fatalf("first-entry ready frame %d opcode = %d, want %d", index, frames[index].opcode, opcode)
		}
	}
	if ss.initialTaskUIPending {
		t.Fatal("first-entry ready batch was not consumed")
	}
}

func TestLoginSceneStartupSendsNumericSnapshotBeforeTransferPoints(t *testing.T) {
	loadOnlineTablesForTest(t)
	ss := newSession()
	ss.playerID, ss.jobID, ss.skinID, ss.level = 7, 1, 1, 15
	ss.mapID = 10004
	ss.worn = make(map[int32]*bagItem)
	seq := ss.markMapChange()
	conn := &recordingConn{}
	ch := &channel{id: 1, conn: conn, session: ss}
	server := &Server{}

	server.scheduleMapStartup(ch, ss.mapID, seq, true)
	time.Sleep(mapStartupDelay + 100*time.Millisecond)

	frames := decodeRecordedFrames(t, conn.Bytes())
	want := []uint16{
		protocol.OpM2C_SyncUnitAttributeList,
		protocol.OpM2C_StartupTransPoint,
	}
	if len(frames) != len(want) {
		t.Fatalf("login startup frame count = %d, want %d", len(frames), len(want))
	}
	for index, opcode := range want {
		if frames[index].opcode != opcode {
			t.Fatalf("login startup frame %d opcode = %d, want %d", index, frames[index].opcode, opcode)
		}
	}
}

func TestLoginNumericSnapshotCarriesAcrossSupersedingMapChange(t *testing.T) {
	loadOnlineTablesForTest(t)
	ss := newSession()
	ss.state, ss.playerID = sessInGame, 7
	ss.jobID, ss.skinID, ss.level = 1, 1, 15
	ss.mapID = 1001024
	ss.worn = make(map[int32]*bagItem)
	conn := &recordingConn{}
	ch := &channel{id: 1, conn: conn, session: ss}
	server := &Server{conns: map[int64]*channel{ch.id: ch}}

	firstSeq := ss.markMapChange()
	server.scheduleLoginMapStartup(ch, ss.mapID, firstSeq, false)
	// This is the launcher-team path: the login scene is superseded before
	// its delayed startup callback can create the client's local HUD.
	secondSeq := server.changeMap(ch, 10004, 0.5, -1.72)
	if secondSeq == firstSeq {
		t.Fatalf("superseding map change did not advance sequence: %d", secondSeq)
	}
	time.Sleep(mapStartupDelay + 150*time.Millisecond)

	frames := decodeRecordedFrames(t, conn.Bytes())
	snapshots := 0
	for _, frame := range frames {
		if frame.opcode == protocol.OpM2C_SyncUnitAttributeList {
			snapshots++
		}
	}
	if snapshots != 1 {
		t.Fatalf("superseding login map produced %d numeric snapshots, want 1: %v", snapshots, recordedOpcodes(t, conn.Bytes()))
	}
	if ss.loginNumericPending.Load() {
		t.Fatal("login numeric snapshot remained pending after replacement map startup")
	}
}

func decodeAttributeMapList(t *testing.T, raw []byte, field protowire.Number) []protocol.AttributeMap {
	t.Helper()
	var attributes []protocol.AttributeMap
	for len(raw) > 0 {
		number, wireType, tagLen := protowire.ConsumeTag(raw)
		if tagLen < 0 {
			t.Fatal(protowire.ParseError(tagLen))
		}
		raw = raw[tagLen:]
		if number == field && wireType == protowire.BytesType {
			payload, valueLen := protowire.ConsumeBytes(raw)
			if valueLen < 0 {
				t.Fatal(protowire.ParseError(valueLen))
			}
			attributes = append(attributes, protocol.AttributeMap{})
			if err := proto.Unmarshal(payload, &attributes[len(attributes)-1]); err != nil {
				t.Fatal(err)
			}
			raw = raw[valueLen:]
			continue
		}
		valueLen := protowire.ConsumeFieldValue(number, wireType, raw)
		if valueLen < 0 {
			t.Fatal(protowire.ParseError(valueLen))
		}
		raw = raw[valueLen:]
	}
	return attributes
}

func TestPlayerTitleUsesUnitCharacterAndNumericWatcherProtocols(t *testing.T) {
	loadOnlineTablesForTest(t)
	ss := newSession()
	ss.playerID, ss.jobID, ss.skinID, ss.titleID, ss.level = 77, 1, 1, 120889, 1
	ss.name = "title-player"
	ss.worn = make(map[int32]*bagItem)
	conn := &recordingConn{}
	ch := &channel{id: 1, conn: conn, session: ss}
	server := &Server{}
	server.pushUnitCharacter(ch)
	server.pushPlayerAttrsTo(ch, ss, ss.playerMaxHp(), ss.playerMaxMp())

	seenCharacter := false
	seenNumeric := false
	for _, frame := range decodeRecordedFrames(t, conn.Bytes()) {
		switch frame.opcode {
		case protocol.OpM2C_SendUnitInfo:
			message := &protocol.M2C_SendUnitInfo{}
			if err := proto.Unmarshal(frame.body, message); err != nil {
				t.Fatal(err)
			}
			if message.UnitCharacter == nil || message.UnitCharacter.Id != ss.playerID ||
				message.UnitCharacter.Title != ss.titleID {
				t.Fatalf("title UnitCharacter = %+v", message.UnitCharacter)
			}
			seenCharacter = true
		case protocol.OpM2C_SyncUnitAttribute:
			message := &protocol.M2C_SyncUnitAttribute{}
			if err := proto.Unmarshal(frame.body, message); err != nil {
				t.Fatal(err)
			}
			if message.NumericType == 1038 {
				if message.UnitId != ss.playerID || message.Value != float32(ss.titleID) {
					t.Fatalf("title Numeric sync = %+v", message)
				}
				seenNumeric = true
			}
		}
	}
	if !seenCharacter || !seenNumeric {
		t.Fatalf("title protocol sync: UnitCharacter=%t Numeric1038=%t", seenCharacter, seenNumeric)
	}
}

// TestOnlineCaptureExperienceCurve：用线上挂机抓包的四个整分钟节拍点反推升级曲线。
// 角色从 1 级 0 经验开始，每跳固定 24560 显示经验，抓包里第 1/2/3/4 跳后分别是
// 56/67/74/79 级、余 868/264/757/3348 —— 即 Σ_{Lv=1}^{升级前等级-1} expNeed(Lv)
// 必须等于 跳数*24560 - 余数。这是升级阈值公式唯一的线上实测依据。
func TestOnlineCaptureExperienceCurve(t *testing.T) {
	const tick = 24560
	for _, test := range []struct {
		ticks int64
		level int32
		rest  int64
	}{
		{ticks: 1, level: 56, rest: 868},
		{ticks: 2, level: 67, rest: 264},
		{ticks: 3, level: 74, rest: 757},
		{ticks: 4, level: 79, rest: 3348},
	} {
		var total int64
		for level := int32(1); level < test.level; level++ {
			total += expNeed(level) / 100 // 显示单位
		}
		if want := test.ticks*tick - test.rest; total != want {
			t.Errorf("ticks=%d level=%d: sum(expNeed) = %d, want %d from the online capture",
				test.ticks, test.level, total, want)
		}
	}
}

func TestBeachFirstFourLayersUseOnlineMonsterExperience(t *testing.T) {
	loadOnlineTablesForTest(t)
	wantGain := []int64{1344, 25008, 59616, 136200}
	// 等级/余数按线上 ⌊Lv³/100⌋（1~4 级为 0）逐层累加推导；经验值为 100 倍
	// 存储单位。第一层因线上低等级门槛为 0 而多升一级（8→9）。
	wantLevel := []int32{9, 18, 24, 31}
	wantExp := []int64{244, 3552, 10668, 7168}
	level := int32(1)
	exp := int64(0)
	for index := 0; index < 4; index++ {
		row := tables.mainStory[int64(1001+index)]
		entries := arrOf(row["Monster_1Arr"])
		if len(entries) == 0 {
			t.Fatalf("beach layer %d has no Monster_1Arr", index+1)
		}
		entry, _ := entries[0].(map[string]interface{})
		monsterID := int32(num(entry["Monster_1_Id"]))
		count := num(entry["Monster_1_Count"])
		gain := num(tables.monsterBase[int64(monsterID)]["Exp"]) * count * 2
		if gain != wantGain[index] {
			t.Fatalf("beach layer %d two-encounter exp = %d, want %d", index+1, gain, wantGain[index])
		}
		exp += gain
		for exp >= expNeed(level) {
			exp -= expNeed(level)
			level++
		}
		if level != wantLevel[index] || exp != wantExp[index] {
			t.Fatalf("after beach layer %d = level %d exp %d, want level %d exp %d",
				index+1, level, exp, wantLevel[index], wantExp[index])
		}
	}
}

func TestCharacterGrowthConvertsPrimaryAttributes(t *testing.T) {
	loadOnlineTablesForTest(t)
	for jobID := int32(1); jobID <= 8; jobID++ {
		ss := newSession()
		ss.jobID, ss.level = jobID, 1
		ss.worn = make(map[int32]*bagItem)
		profession := jobTypeOf(jobID)

		mpRow := tables.characterGrowth[int64(20+profession)]
		wantMP := int32(math.Round(numf(mpRow["Spi"]) * float64(ss.playerSpi())))
		if got := ss.playerMaxMp(); got != wantMP {
			t.Errorf("job %d max MP = %d, want CharacterGrowth result %d", jobID, got, wantMP)
		}

		before := ss.playerPhyAtk()
		ss.strAdd = 1
		coefficient := numf(tables.characterGrowth[int64(70+profession)]["Str"])
		wantIncrease := int32(math.Round(coefficient))
		if got := ss.playerPhyAtk() - before; got != wantIncrease {
			t.Errorf("job %d +1 Str changed physical attack by %d, want %d", jobID, got, wantIncrease)
		}

		beforeHPGrowth := characterGrowthValue(ss, 1)
		beforePhy := ss.playerExtraNumeric(1034)
		ss.phyAdd = 1
		if got := ss.playerExtraNumeric(1034) - beforePhy; got != 1 {
			t.Errorf("job %d +1 Phy panel delta = %v, want 1", jobID, got)
		}
		wantPhyGrowth := numf(tables.characterGrowth[int64(10+profession)]["Phy"])
		if got := characterGrowthValue(ss, 1) - beforeHPGrowth; got != wantPhyGrowth {
			t.Errorf("job %d +1 Phy changed HP growth by %v, want %v", jobID, got, wantPhyGrowth)
		}

		beforeDefGrowth := characterGrowthValue(ss, 9)
		beforeSta := ss.playerExtraNumeric(1035)
		ss.staAdd = 1
		if got := ss.playerExtraNumeric(1035) - beforeSta; got != 1 {
			t.Errorf("job %d +1 Sta panel delta = %v, want 1", jobID, got)
		}
		wantStaGrowth := numf(tables.characterGrowth[int64(90+profession)]["Sta"])
		if got := characterGrowthValue(ss, 9) - beforeDefGrowth; got != wantStaGrowth {
			t.Errorf("job %d +1 Sta changed physical-defence growth by %v, want %v", jobID, got, wantStaGrowth)
		}

		ss.qukAdd = 100
		if got := ss.playerExtraNumeric(1031); got <= 0 {
			t.Errorf("job %d speed did not include CharacterGrowth: %v", jobID, got)
		}
		if got := ss.playerExtraNumeric(1032); got <= 0 {
			t.Errorf("job %d hit did not include CharacterGrowth: %v", jobID, got)
		}
	}
}

func TestEveryConfiguredDungeonMonsterExists(t *testing.T) {
	loadOnlineTablesForTest(t)
	assertMonster := func(source string, owner int64, monsterID int32) {
		t.Helper()
		row := tables.monsterBase[int64(monsterID)]
		if row == nil {
			t.Errorf("%s %d references missing MonsterBase %d", source, owner, monsterID)
			return
		}
		if int32(num(row["Level"])) <= 0 || int32(num(row["PrefabId"])) <= 0 || int32(num(row["Hp"])) <= 0 {
			t.Errorf("%s %d monster %d has invalid level/prefab/hp", source, owner, monsterID)
		}
	}
	for id, row := range tables.mainStory {
		for group := 1; group <= 6; group++ {
			for _, raw := range arrOf(row[fmt.Sprintf("Monster_%dArr", group)]) {
				entry, _ := raw.(map[string]interface{})
				if monsterID := int32(num(entry[fmt.Sprintf("Monster_%d_Id", group)])); monsterID > 0 {
					assertMonster("MainStory", id, monsterID)
				}
			}
		}
	}
	for id, row := range tables.trialCopy {
		assertMonster("TrialCopy", id, int32(num(row["MonsterId"])))
	}
	for id, row := range tables.bossBase {
		assertMonster("BossBase", id, int32(num(row["MonsterId"])))
	}
	for id, row := range tables.familyBossConfig {
		assertMonster("FamilyBossConfig", id, int32(num(row["MonsterId"])))
	}
	for id, row := range tables.manulEquipMonsterConfig {
		assertMonster("ManulEquipMonsterConfig", id, int32(num(row["MonsterId"])))
	}
	for id, row := range tables.spaceTravelConfig {
		for _, raw := range arrOf(row["MonsterIdArr"]) {
			assertMonster("SpaceTravelConfig", id, int32(num(raw)))
		}
	}
}

func TestDungeonBattleTypesMatchCopyConfig(t *testing.T) {
	loadOnlineTablesForTest(t)
	tests := []struct {
		name         string
		region       int32
		mapID        int32
		presentation battlePresentation
		wantCopy     int64
		wantType     int32
	}{
		{"main", 1001, 0, battlePresentation{kind: presentationMainStory}, 10001, 1},
		{"main-boss", 1010, 0, battlePresentation{kind: presentationMainStory}, 10003, 1},
		{"trial", 1001, 1000901, battlePresentation{kind: presentationTrial}, 10004, 2},
		{"family", 1001, -1, battlePresentation{kind: presentationFamilyBoss}, 10006, 5},
		{"manual", 1001, 1003301, battlePresentation{kind: presentationManualEquip}, 10007, 6},
		{"space", 1001, 1003901, battlePresentation{kind: presentationMainStory}, 10013, 22},
		{"boss-layer", 1001, 1001001, battlePresentation{kind: presentationWorldBoss}, 10005, 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			copyID := battleCopyConfig(test.region, test.mapID, test.presentation)
			if copyID != test.wantCopy {
				t.Fatalf("copy = %d, want %d", copyID, test.wantCopy)
			}
			if got := int32(num(tables.copyConfig[copyID]["BattleType"])); got != test.wantType {
				t.Fatalf("battle type = %d, want %d", got, test.wantType)
			}
		})
	}
}

func TestOnlineCopyRewardDropFlagsMatchOriginalClientConfig(t *testing.T) {
	loadOnlineTablesForTest(t)
	want := map[int64]bool{
		10001: true, 10002: true, 10003: true, 10004: true,
		10005: true, 10006: false, 10007: true, 10008: false,
		10009: false, 10010: true, 10011: true, 10012: true,
		10013: true, 10014: false, 10015: false, 10016: false,
	}
	for copyID, expected := range want {
		if got := copyVictoryAwardsMonsterDrops(copyID); got != expected {
			t.Errorf("CopyConfig %d generic monster drops = %v, want %v", copyID, got, expected)
		}
	}
}

func TestEveryOnlineCopyUsesConfiguredEnergyCost(t *testing.T) {
	loadOnlineTablesForTest(t)
	want := map[int64]int32{
		10001: 3, 10002: 3, 10003: 1, 10004: 0,
		10005: 1, 10006: 0, 10007: 0, 10008: 0,
		10009: 0, 10010: 3, 10011: 3, 10012: 1,
		10013: 0, 10014: 10, 10015: 0, 10016: 0,
	}
	for copyID, expected := range want {
		if got := battleEnergyCost(copyID); got != expected {
			t.Errorf("CopyConfig %d energy = %d, want %d", copyID, got, expected)
		}
	}
}
