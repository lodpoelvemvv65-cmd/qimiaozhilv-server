package main

import (
	"fmt"
	"math"
	"reflect"
	"sort"
	"testing"

	"google.golang.org/protobuf/proto"

	"mhqserver/internal/operationsconfig"
	"mhqserver/protocol"
)

func sortedConfigIDs(table map[int64]map[string]interface{}) []int64 {
	ids := make([]int64, 0, len(table))
	for id := range table {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func rosterFromMainStoryRow(row map[string]interface{}) ([]int32, []int) {
	var ids []int32
	var counts []int
	for group := 1; group <= 6; group++ {
		for _, raw := range arrOf(row[fmt.Sprintf("Monster_%dArr", group)]) {
			entry, _ := raw.(map[string]interface{})
			monsterID := int32(num(entry[fmt.Sprintf("Monster_%d_Id", group)]))
			count := int(num(entry[fmt.Sprintf("Monster_%d_Count", group)]))
			if monsterID > 0 && count > 0 {
				ids = append(ids, monsterID)
				counts = append(counts, count)
			}
		}
	}
	return ids, counts
}

func expandedRoster(ids []int32, counts []int) []int32 {
	var expanded []int32
	for index, id := range ids {
		count := 1
		if index < len(counts) && counts[index] > 0 {
			count = counts[index]
		}
		for i := 0; i < count; i++ {
			expanded = append(expanded, id)
		}
	}
	return expanded
}

func assertOnlineMonsterUnits(t *testing.T, units []*monsterUnit, ids []int32, counts []int) {
	t.Helper()
	wantIDs := expandedRoster(ids, counts)
	if len(units) != len(wantIDs) {
		t.Fatalf("unit count = %d, want %d for roster %v/%v", len(units), len(wantIDs), ids, counts)
	}
	for index, unit := range units {
		monsterID := wantIDs[index]
		row := tables.monsterBase[int64(monsterID)]
		if row == nil {
			t.Fatalf("unit %d references missing MonsterBase %d", index, monsterID)
		}
		name, _ := row["NickName"].(string)
		if name == "" || int32(num(row["PrefabId"])) <= 0 || int32(num(row["Level"])) <= 0 {
			t.Errorf("MonsterBase %d lacks client name/model/level: name=%q prefab=%d level=%d",
				monsterID, name, num(row["PrefabId"]), num(row["Level"]))
		}
		if unit.monsterID != monsterID || unit.hp != int32(num(row["Hp"])) || unit.maxHP != int32(num(row["Hp"])) ||
			unit.phyAtk != int32(num(row["PhyAtk"])) || unit.spiAtk != int32(num(row["SpiAtk"])) ||
			unit.phyDef != int32(num(row["PhyDef"])) || unit.spiDef != int32(num(row["SpiDef"])) ||
			unit.exp != num(row["Exp"]) || !unit.alive {
			t.Errorf("unit %d does not match MonsterBase %d: %+v", index, monsterID, unit)
		}
		for field, numericType := range equipFieldToNumeric {
			if got, want := unit.extraNumeric[numericType], numf(row[field]); got != want {
				t.Errorf("MonsterBase %d field %s numeric %d = %v, want %v", monsterID, field, numericType, got, want)
			}
		}
	}
}

func consistencyChannel(playerID int64, mapID int32) (*channel, *recordingConn) {
	ss := newSession()
	ss.state = sessInGame
	ss.playerID, ss.jobID, ss.skinID, ss.level, ss.energy = playerID, 1, 1, 1, math.MaxInt32
	ss.phyAdd, ss.spiAdd = 1, 1
	ss.mapID = mapID
	ss.worn = make(map[int32]*bagItem)
	conn := &recordingConn{}
	return &channel{id: playerID, conn: conn, session: ss}, conn
}

func findRecordedFrame(t *testing.T, data []byte, opcode uint16) recordedFrame {
	t.Helper()
	for _, frame := range decodeRecordedFrames(t, data) {
		if frame.opcode == opcode {
			return frame
		}
	}
	t.Fatalf("missing recorded opcode %d; got %v", opcode, recordedOpcodes(t, data))
	return recordedFrame{}
}

func TestEveryMainStoryMapUsesOnlineRosterAndNativePresentation(t *testing.T) {
	loadOnlineTablesForTest(t)
	server := &Server{}
	for _, configID := range sortedConfigIDs(tables.mainStory) {
		row := tables.mainStory[configID]
		mapID := int32(num(row["SceneId"]))*100 + int32(num(row["Layer"]))
		wantIDs, wantCounts := rosterFromMainStoryRow(row)
		region, gotIDs, gotCounts, positions, ok := mainStoryRosterForMap(mapID)
		if !ok || int64(region) != configID || !reflect.DeepEqual(gotIDs, wantIDs) || !reflect.DeepEqual(gotCounts, wantCounts) {
			t.Fatalf("map %d resolves region=%d roster=%v/%v, want %d %v/%v", mapID, region, gotIDs, gotCounts, configID, wantIDs, wantCounts)
		}
		if len(positions) != len(arrOf(row["MonsterPosArr"])) {
			t.Fatalf("MainStory %d position groups = %d, want %d", configID, len(positions), len(arrOf(row["MonsterPosArr"])))
		}

		ch, conn := consistencyChannel(100000+configID, mapID)
		server.pushFieldMonsters(ch)
		initFrame := findRecordedFrame(t, conn.Bytes(), protocol.OpM2C_InitMainStoryMap)
		initMessage := &protocol.M2C_InitMainStoryMap{}
		if err := proto.Unmarshal(initFrame.body, initMessage); err != nil || int64(initMessage.MainStoryId) != configID {
			t.Fatalf("map %d native presentation = %+v err=%v", mapID, initMessage, err)
		}
		for _, opcode := range recordedOpcodes(t, conn.Bytes()) {
			if opcode == protocol.OpM2C_CreateMapMonster {
				t.Fatalf("MainStory %d emitted a duplicate field monster", configID)
			}
		}

		units := ch.session.buildMonsterUnitsFromRoster(wantIDs, wantCounts)
		assertOnlineMonsterUnits(t, units, wantIDs, wantCounts)
		if started, _ := server.finishStartSingleBattleWithPresentation(ch, int32(configID), units, 0, battlePresentation{kind: presentationMainStory}); !started {
			t.Fatalf("MainStory %d did not start", configID)
		}
		if wantCopy := battleCopyConfig(int32(configID), 0, battlePresentation{kind: presentationMainStory}); ch.session.battle.copyID != wantCopy {
			t.Fatalf("MainStory %d battle copy = %d, want %d", configID, ch.session.battle.copyID, wantCopy)
		}
		fightFrame := findRecordedFrame(t, conn.Bytes(), protocol.OpM2C_MainStoryMonsterInfo)
		fightMessage := &protocol.M2C_MainStoryMonsterInfo{}
		if err := proto.Unmarshal(fightFrame.body, fightMessage); err != nil {
			t.Fatal(err)
		}
		var wireIDs []int32
		for _, unit := range fightMessage.MonsterUnitInfoList {
			wireIDs = append(wireIDs, unit.MonsterId)
		}
		if want := expandedRoster(wantIDs, wantCounts); !reflect.DeepEqual(wireIDs, want) {
			t.Fatalf("MainStory %d wire monsters = %v, want %v", configID, wireIDs, want)
		}
	}
}

func TestEveryTrialCopyUsesOnlineMapMonsterAndBattleUnits(t *testing.T) {
	loadOnlineTablesForTest(t)
	server := &Server{}
	for _, configID := range sortedConfigIDs(tables.trialCopy) {
		row := tables.trialCopy[configID]
		mapID := int32(num(row["MapId"]))
		monsterID := int32(num(row["MonsterId"]))
		count := int(num(row["MonsterCount"]))
		ch, conn := consistencyChannel(200000+configID, mapID)
		server.pushFieldMonsters(ch)
		initFrame := findRecordedFrame(t, conn.Bytes(), protocol.OpM2C_InitTrialCopyMap)
		initMessage := &protocol.M2C_InitTrialCopyMap{}
		if err := proto.Unmarshal(initFrame.body, initMessage); err != nil || int64(initMessage.TrialCopyId) != configID {
			t.Fatalf("TrialCopy %d native presentation = %+v err=%v", configID, initMessage, err)
		}

		response := server.onStartTrialCopyFight(ch, &protocol.C2M_StartTrialCopyFight{RpcId: 1}).(*protocol.M2C_StartTrialCopyFight)
		if response.Message != "" || int64(response.TrialCopyId) != configID || len(response.UnitIdList) != count {
			t.Fatalf("TrialCopy %d response = %+v, want %d units", configID, response, count)
		}
		assertOnlineMonsterUnits(t, ch.session.battle.monsters, []int32{monsterID}, []int{count})
		var combatPresentations int
		for _, opcode := range recordedOpcodes(t, conn.Bytes()) {
			if opcode == protocol.OpM2C_InitMainStoryMap || opcode == protocol.OpM2C_CreateMapMonster {
				t.Fatalf("TrialCopy %d emitted incompatible field presentation opcode %d", configID, opcode)
			}
			if opcode == protocol.OpM2C_MainStoryMonsterInfo {
				combatPresentations++
			}
		}
		if combatPresentations != 1 {
			t.Fatalf("TrialCopy %d combat presentations = %d, want 1", configID, combatPresentations)
		}
		fightFrame := findRecordedFrame(t, conn.Bytes(), protocol.OpM2C_MainStoryMonsterInfo)
		fightMessage := &protocol.M2C_MainStoryMonsterInfo{}
		if err := proto.Unmarshal(fightFrame.body, fightMessage); err != nil {
			t.Fatal(err)
		}
		for index, info := range fightMessage.MonsterUnitInfoList {
			if info.MonsterId != monsterID || info.Id != response.UnitIdList[index] {
				t.Fatalf("TrialCopy %d combat unit[%d] = %+v, response=%v", configID, index, info, response.UnitIdList)
			}
		}
	}
}

func TestEveryWorldAndFamilyBossUsesCorrectConfigIDDomain(t *testing.T) {
	loadOnlineTablesForTest(t)
	oldGlobal := globalServer
	globalServer = nil
	worldBossMu.Lock()
	oldStates := worldBossStates
	worldBossStates = make(map[int32]*worldBossState)
	worldBossMu.Unlock()
	t.Cleanup(func() {
		globalServer = oldGlobal
		worldBossMu.Lock()
		worldBossStates = oldStates
		worldBossMu.Unlock()
	})

	server := &Server{}
	for _, bossBaseID := range sortedConfigIDs(tables.bossBase) {
		row := tables.bossBase[bossBaseID]
		monsterID := int32(num(row["MonsterId"]))
		mapID := int32(1001000 + bossBaseID - 1000)
		ch, conn := consistencyChannel(300000+bossBaseID, mapID)
		server.pushFieldMonsters(ch)
		fieldFrame := findRecordedFrame(t, conn.Bytes(), protocol.OpM2C_BossRefresh)
		fieldMessage := &protocol.M2C_BossRefresh{}
		if err := proto.Unmarshal(fieldFrame.body, fieldMessage); err != nil || int64(fieldMessage.BossId) != bossBaseID {
			t.Fatalf("BossBase %d exterior = %+v err=%v", bossBaseID, fieldMessage, err)
		}
		if started, _ := server.startSceneBattleByMap(ch, mapID); !started {
			t.Fatalf("BossBase %d did not start", bossBaseID)
		}
		assertOnlineMonsterUnits(t, ch.session.battle.monsters, []int32{monsterID}, []int{1})
		fightFrame := findRecordedFrame(t, conn.Bytes(), protocol.OpM2C_SendBossInfo)
		fightMessage := &protocol.M2C_SendBossInfo{}
		if err := proto.Unmarshal(fightFrame.body, fightMessage); err != nil || fightMessage.BossId != monsterID {
			t.Fatalf("BossBase %d fight = %+v, want MonsterBase %d err=%v", bossBaseID, fightMessage, monsterID, err)
		}
		for _, opcode := range recordedOpcodes(t, conn.Bytes()) {
			if opcode == protocol.OpM2C_CreateMapMonster || opcode == protocol.OpM2C_MainStoryMonsterInfo {
				t.Fatalf("BossBase %d emitted duplicate presentation opcode %d", bossBaseID, opcode)
			}
		}
	}

	for _, familyBossID := range sortedConfigIDs(tables.familyBossConfig) {
		row := tables.familyBossConfig[familyBossID]
		monsterID := int32(num(row["MonsterId"]))
		ch, conn := consistencyChannel(400000+familyBossID, -int32(familyBossID))
		units := ch.session.buildMonsterUnitsFromRoster([]int32{monsterID}, []int{1})
		assertOnlineMonsterUnits(t, units, []int32{monsterID}, []int{1})
		if got := familyBossMaxHP(int32(familyBossID)); got != units[0].maxHP {
			t.Fatalf("FamilyBoss %d list HP = %d, fight HP = %d", familyBossID, got, units[0].maxHP)
		}
		if started, _ := server.finishStartSingleBattleWithPresentation(ch, 1001, units, -int32(familyBossID), battlePresentation{kind: presentationFamilyBoss, familyBossID: int32(familyBossID)}); !started {
			t.Fatalf("FamilyBoss %d did not start", familyBossID)
		}
		frame := findRecordedFrame(t, conn.Bytes(), protocol.OpM2C_SendFamilyBossInfo)
		message := &protocol.M2C_SendFamilyBossInfo{}
		if err := proto.Unmarshal(frame.body, message); err != nil || int64(message.BossId) != familyBossID || message.Hp != units[0].hp {
			t.Fatalf("FamilyBoss %d fight = %+v err=%v", familyBossID, message, err)
		}
	}
}

func TestEveryManualAndSpaceTravelEncounterUsesOnlineClientIDs(t *testing.T) {
	loadOnlineTablesForTest(t)
	server := &Server{}
	for tier := int32(1); tier <= 3; tier++ {
		for layer := int32(1); layer <= manualEquipLayerCount; layer++ {
			mapID := (10032+tier)*100 + layer
			fieldMonsters := sceneFieldMonsters(mapID)
			if len(fieldMonsters) != 1 {
				t.Fatalf("manual tier %d layer %d exterior count = %d, want 1", tier, layer, len(fieldMonsters))
			}
			field := fieldMonsters[0]
			fieldConfigID := int32(1000 + layer)
			if field.configID != fieldConfigID || field.mapType != mapMonsterTypeManualEquip {
				t.Fatalf("manual tier %d layer %d exterior = %+v", tier, layer, field)
			}
			leaderConfigID := tier*1000 + layer*100 + 1
			leaderConfig := tables.manulEquipMonsterConfig[int64(leaderConfigID)]
			leaderMonster := tables.monsterBase[num(leaderConfig["MonsterId"])]
			fieldConfig := tables.mapMonsterConfig[int64(fieldConfigID)]
			if fieldConfig == nil || leaderConfig == nil || leaderMonster == nil ||
				fmt.Sprint(fieldConfig["Desc"]) != fmt.Sprint(leaderConfig["Name"]) ||
				int32(num(fieldConfig["PrefabId"])) != int32(num(leaderMonster["PrefabId"])) {
				t.Fatalf("manual tier %d layer %d exterior and leader disagree", tier, layer)
			}
			if field.x != float32(numf(fieldConfig["X"])) || field.y != float32(numf(fieldConfig["Y"])) {
				t.Fatalf("manual tier %d layer %d exterior position = (%v,%v), want (%v,%v)",
					tier, layer, field.x, field.y, fieldConfig["X"], fieldConfig["Y"])
			}

			ch, conn := consistencyChannel(500000+int64(tier*10+layer), mapID)
			configIDs := []int32{leaderConfigID, leaderConfigID + 1, leaderConfigID + 2}
			monsterIDs := make([]int32, 0, 3)
			for index, configID := range configIDs {
				config := tables.manulEquipMonsterConfig[int64(configID)]
				monsterID := int32(num(config["MonsterId"]))
				monster := tables.monsterBase[int64(monsterID)]
				if config == nil || monster == nil || fmt.Sprint(config["Name"]) != fmt.Sprint(monster["NickName"]) {
					t.Fatalf("manual tier %d layer %d slot %d name mismatch", tier, layer, index)
				}
				wantDropGroup := int64(0)
				if index == 0 {
					wantDropGroup = int64(10055 + (tier-1)*manualEquipLayerCount + layer)
					parent := tables.parentset[wantDropGroup]
					if parent == nil {
						t.Fatalf("manual tier %d layer %d missing online drop group %d", tier, layer, wantDropGroup)
					}
					subsets := arrOf(parent["SubsetArr"])
					if len(subsets) == 0 {
						t.Fatalf("manual tier %d layer %d drop group %d has no subsets", tier, layer, wantDropGroup)
					}
					for _, rawSubset := range subsets {
						subset, _ := rawSubset.(map[string]interface{})
						sonID := num(subset["_Id"])
						son := tables.sonSet[sonID]
						if son == nil || len(arrOf(son["DropArr"])) == 0 {
							t.Fatalf("manual tier %d layer %d drop group %d missing SonSet %d items",
								tier, layer, wantDropGroup, sonID)
						}
					}
				}
				if got := num(monster["Dropasubset"]); got != wantDropGroup {
					t.Fatalf("manual tier %d layer %d slot %d drop group = %d, want %d",
						tier, layer, index, got, wantDropGroup)
				}
				monsterIDs = append(monsterIDs, monsterID)
			}
			units := ch.session.buildMonsterUnitsFromRoster(monsterIDs, []int{1, 1, 1})
			if started, count := server.finishStartSingleBattleWithPresentation(ch, 1001, units, mapID, battlePresentation{
				kind: presentationManualEquip, manualConfigIDs: configIDs,
			}); !started || count != 3 {
				t.Fatalf("manual tier %d layer %d start=(%v,%d)", tier, layer, started, count)
			}
			assertOnlineMonsterUnits(t, ch.session.battle.monsters, monsterIDs, []int{1, 1, 1})
			frame := findRecordedFrame(t, conn.Bytes(), protocol.OpM2C_SendManulEquipMonsterInfo)
			message := &protocol.M2C_SendManulEquipMonsterInfo{}
			if err := proto.Unmarshal(frame.body, message); err != nil {
				t.Fatal(err)
			}
			var wireConfigIDs []int32
			for _, unit := range message.UnitIdList {
				wireConfigIDs = append(wireConfigIDs, unit.MonsterId)
			}
			if !reflect.DeepEqual(wireConfigIDs, configIDs) {
				t.Fatalf("manual tier %d layer %d wire configs = %v, want %v", tier, layer, wireConfigIDs, configIDs)
			}
		}
	}

	for sceneIndex := int32(0); sceneIndex < 6; sceneIndex++ {
		mapID := (10039+sceneIndex)*100 + 1
		fieldMonsters := sceneFieldMonsters(mapID)
		fieldConfigID := 1006 + sceneIndex
		if len(fieldMonsters) != 1 || fieldMonsters[0].configID != fieldConfigID {
			t.Fatalf("space scene %d exterior = %+v, want config %d", sceneIndex+1, fieldMonsters, fieldConfigID)
		}
		fieldConfig := tables.mapMonsterConfig[int64(fieldConfigID)]
		if fieldMonsters[0].x != float32(numf(fieldConfig["X"])) || fieldMonsters[0].y != float32(numf(fieldConfig["Y"])) {
			t.Fatalf("space scene %d exterior position = (%v,%v), want table (%v,%v)", sceneIndex+1,
				fieldMonsters[0].x, fieldMonsters[0].y, fieldConfig["X"], fieldConfig["Y"])
		}
		config := tables.spaceTravelConfig[int64(1001+sceneIndex)]
		var monsterIDs []int32
		for _, raw := range arrOf(config["MonsterIdArr"]) {
			monsterIDs = append(monsterIDs, int32(num(raw)))
		}
		ch, conn := consistencyChannel(600000+int64(sceneIndex), mapID)
		if started, count := server.startSceneBattleByMap(ch, mapID); !started || count != len(monsterIDs) {
			t.Fatalf("space scene %d start=(%v,%d), want %d", sceneIndex+1, started, count, len(monsterIDs))
		}
		assertOnlineMonsterUnits(t, ch.session.battle.monsters, monsterIDs, nil)
		frame := findRecordedFrame(t, conn.Bytes(), protocol.OpM2C_MainStoryMonsterInfo)
		message := &protocol.M2C_MainStoryMonsterInfo{}
		if err := proto.Unmarshal(frame.body, message); err != nil {
			t.Fatal(err)
		}
		var wireIDs []int32
		for _, unit := range message.MonsterUnitInfoList {
			wireIDs = append(wireIDs, unit.MonsterId)
		}
		if !reflect.DeepEqual(wireIDs, monsterIDs) {
			t.Fatalf("space scene %d wire monsters = %v, want %v", sceneIndex+1, wireIDs, monsterIDs)
		}
	}
}

func TestBeachTutorialReturnUsesOnlineMilestoneAndReachesLevel131(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.Progression.ExperienceGainPercent = 100
	})
	loadOnlineTablesForTest(t)
	server := &Server{}
	ch, _ := consistencyChannel(700001, 1000604)
	ch.session.level, ch.session.exp = 23, 0
	ch.session.tasks = map[int32]int32{10016: taskStateRunning}
	ch.session.killCount = map[int32]int32{beachHighestLayerKey: 3}

	if !server.grantBeachReturnProgress(ch) {
		t.Fatal("beach return milestone was not granted")
	}
	if ch.session.level != 131 || ch.session.charPoint != 27 || ch.session.skillPoint != 0 {
		t.Fatalf("beach return = level %d points %d/%d, want 131/27/0",
			ch.session.level, ch.session.charPoint, ch.session.skillPoint)
	}
	if server.grantBeachReturnProgress(ch) {
		t.Fatal("beach return milestone was granted twice")
	}
}
