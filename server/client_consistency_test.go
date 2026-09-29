package main

import (
	"fmt"
	"math"
	"testing"

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
	for level, want := range map[int32]int64{1: 100, 4: 6400, 5: 12500, 10: 100000, 100: 100000000, 4000: 6400000000000} {
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
			if math.Abs(float64(message.Value)-0.4321) > 0.00001 {
				t.Fatalf("wire exp = %v, want scaled 0.4321", message.Value)
			}
		}
	}
	if !found {
		t.Fatal("missing NumericType 1027 progress push")
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
		wantMP := statSum(baseMaxMp(jobID, 1), int32(math.Round(numf(mpRow["Spi"])*float64(ss.playerSpi()))))
		if got := ss.playerMaxMp(); got != wantMP {
			t.Errorf("job %d max MP = %d, want CharacterGrowth result %d", jobID, got, wantMP)
		}

		before := ss.playerPhyAtk()
		ss.strAdd = 10
		coefficient := numf(tables.characterGrowth[int64(70+profession)]["Str"])
		wantIncrease := int32(math.Round(coefficient * 10))
		if got := ss.playerPhyAtk() - before; got != wantIncrease {
			t.Errorf("job %d +10 Str changed physical attack by %d, want %d", jobID, got, wantIncrease)
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
