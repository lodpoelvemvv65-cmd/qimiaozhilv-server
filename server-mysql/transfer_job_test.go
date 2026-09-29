package main

import (
	"reflect"
	"testing"

	"mhqserver/protocol"
)

func TestLoadDataRestoresEveryProfessionBaseSkill(t *testing.T) {
	loadOnlineTablesForTest(t)
	specialSkills := map[int32]int32{
		1: 110101,
		2: 210101,
		3: 310101,
		4: 410101,
	}
	for jobID := int32(1); jobID <= 8; jobID++ {
		jobID := jobID
		t.Run(string(rune('0'+jobID)), func(t *testing.T) {
			base := baseSkillOfJob(jobID)
			special := specialSkills[jobTypeOf(jobID)]
			player := &Player{
				ID: 100 + int64(jobID), JobID: jobID, Level: 10, Energy: 1000,
				MapID: 10004,
				Relations: playerRelations{
					skills:     map[int32]int32{special: 2},
					skillOrder: []int32{special},
					autoSkills: []int32{special},
					bag:        make(map[int32]*bagItem),
					worn:       make(map[int32]*bagItem),
				},
			}
			ss := newSession()
			ss.playerID, ss.jobID = player.ID, player.JobID
			if !ss.loadData(player) {
				t.Fatal("missing base skill was not marked for persistence migration")
			}
			if ss.skills[base] != 1 || ss.skills[special] != 2 {
				t.Fatalf("job %d migrated skills=%v", jobID, ss.skills)
			}
			if !reflect.DeepEqual(ss.skillOrder, []int32{base, special}) {
				t.Fatalf("job %d migrated skill order=%v", jobID, ss.skillOrder)
			}
			if !reflect.DeepEqual(ss.autoSkills, []int32{special}) {
				t.Fatalf("job %d migrated auto skills=%v", jobID, ss.autoSkills)
			}
			if slot := ss.mainUISlots[0]; slot != (mainUISlot{}) {
				t.Fatalf("job %d shortcut=%+v, want empty", jobID, slot)
			}
			if ss.ensureCurrentJobBaseSkill() {
				t.Fatal("base-skill migration was not idempotent")
			}
		})
	}
}

func TestTransferJobPersistsProfessionAndLearnedBaseSkill(t *testing.T) {
	loadOnlineTablesForTest(t)
	store, err := OpenStore(mysqlTestDSN(t, "transfer-job-base-skill"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	accountID, err := store.CreateAccount("transfer-owner", "password")
	if err != nil {
		t.Fatal(err)
	}
	playerID, err := store.CreatePlayer(accountID, "转职持久化", 1, 1)
	if err != nil {
		t.Fatal(err)
	}

	ss := newSession()
	ss.playerID, ss.jobID, ss.skinID, ss.level = playerID, 1, 1, 100
	ss.skillPoint = 7
	ss.skills = map[int32]int32{100001: 1, 110101: 2}
	ss.skillOrder = []int32{100001, 110101}
	ss.autoSkills = []int32{110101}
	ss.mainUISlots[0] = mainUISlot{Type: 1, Id: 100001}
	ss.mainUISlots[1] = mainUISlot{Type: 1, Id: 110101}
	conn := &recordingConn{}
	ch := &channel{id: 1, conn: conn, session: ss}
	server := &Server{store: store, conns: map[int64]*channel{1: ch}}

	response := server.onTransferJob(ch, &protocol.C2M_TransferJob{
		RpcId: 9, JobType: int32(protocol.JobType_Nurse),
	}).(*protocol.M2C_TransferJob)
	if response.Error != 0 || response.Message != "" {
		t.Fatalf("transfer response=%+v", response)
	}
	if ss.jobID != 5 || ss.skinID != 5 {
		t.Fatalf("transferred identity job/skin=%d/%d, want 5/5", ss.jobID, ss.skinID)
	}
	if !reflect.DeepEqual(ss.skills, map[int32]int32{300001: 1}) ||
		!reflect.DeepEqual(ss.skillOrder, []int32{300001}) || len(ss.autoSkills) != 0 {
		t.Fatalf("transferred skills=%v order=%v auto=%v", ss.skills, ss.skillOrder, ss.autoSkills)
	}
	// 110101 是 2 级（投入 2 点），转职时按等级全额返还：7 + 2 = 9；
	// 旧职业免费普攻 100001 不返还。
	if ss.skillPoint != 9 {
		t.Fatalf("skill points=%d, want 9; free old basic attack must not be refunded", ss.skillPoint)
	}
	if slot := ss.mainUISlots[0]; slot != (mainUISlot{}) {
		t.Fatalf("transferred shortcut=%+v, want incompatible old slot cleared", slot)
	}

	stored, err := store.FirstPlayer(accountID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.JobID != 5 || stored.Relations.skills[300001] != 1 ||
		!reflect.DeepEqual(stored.Relations.skillOrder, []int32{300001}) {
		t.Fatalf("stored transfer job=%d skills=%v order=%v",
			stored.JobID, stored.Relations.skills, stored.Relations.skillOrder)
	}
	restored := newSession()
	restored.playerID, restored.jobID = stored.ID, stored.JobID
	restored.loadData(stored)
	if restored.jobID != 5 || restored.skills[300001] != 1 ||
		!reflect.DeepEqual(restored.skillOrder, []int32{300001}) {
		t.Fatalf("reloaded transfer job=%d skills=%v order=%v",
			restored.jobID, restored.skills, restored.skillOrder)
	}
}
