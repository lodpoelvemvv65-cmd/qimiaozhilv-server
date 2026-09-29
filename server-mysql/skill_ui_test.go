package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"mhqserver/protocol"
)

func TestLearnSkillMissingBookReturnsMessageAndCurrentState(t *testing.T) {
	oldTables := tables
	t.Cleanup(func() { tables = oldTables })

	const (
		skillID int32 = 110101
		bookID  int32 = 20217
	)
	tables = &datatables{
		skillConfig: map[int64]map[string]interface{}{
			int64(skillID) * 100: testRow(map[string]int64{"MaxLevel": 10}),
		},
		skillLearn: map[int64]map[string]interface{}{
			int64(skillID)*100 + 1: {
				"MaterialArr": []interface{}{map[string]interface{}{
					"_Id": json.Number("20217"), "Count": json.Number("1"),
				}},
			},
		},
		materialBase: map[int64]map[string]interface{}{
			int64(bookID): {"Name": "军官技能秘籍"},
		},
	}

	ss := newSession()
	ss.jobID = int32(protocol.JobType_Officer)
	ss.skills = map[int32]int32{100001: 1, skillID: 1}
	ss.skillPoint = 1
	ss.skillOrder = []int32{100001, skillID}
	ss.autoSkills = []int32{skillID}
	ss.bag = map[int32]*bagItem{}
	ch := &channel{id: 1, session: ss}

	got := (&Server{}).onLearnSkill(ch, &protocol.C2M_LearnSkill{RpcId: 7, SkillId: skillID}).(*protocol.M2C_LearnSkill)
	if got.Error != 0 {
		t.Fatalf("Error = %d, want 0 so the client RPC stays connected", got.Error)
	}
	if got.Message != "缺少军官技能秘籍 x1" {
		t.Fatalf("Message = %q", got.Message)
	}
	if gotLevel := ss.skills[skillID]; gotLevel != 1 {
		t.Fatalf("skill level changed without a book: %d", gotLevel)
	}
	if !reflect.DeepEqual(got.AutoSkillList, ss.autoSkills) {
		t.Fatalf("AutoSkillList = %v, want %v", got.AutoSkillList, ss.autoSkills)
	}
	var returnedLevel int32 = -1
	for _, info := range got.SkillInfoLsit {
		if info.SkillId == skillID {
			returnedLevel = info.SkillLevel
			break
		}
	}
	if returnedLevel != 1 {
		t.Fatalf("response skill level = %d, want current level 1", returnedLevel)
	}
}

func TestLoadDataRepairsIncompleteSkillOrderWithoutDroppingLearnedSkills(t *testing.T) {
	loadOnlineTablesForTest(t)
	player := &Player{
		ID: 9001, JobID: 1, Level: 20, Energy: 1000, MapID: 10004,
		Relations: playerRelations{
			skills:     map[int32]int32{110101: 2, 110201: 1},
			skillOrder: nil, // legacy row: skills were saved without order metadata
			autoSkills: []int32{110201, 999999, 110101},
			bag:        make(map[int32]*bagItem),
			worn:       make(map[int32]*bagItem),
		},
	}
	ss := newSession()
	ss.playerID, ss.jobID = player.ID, player.JobID
	if !ss.loadData(player) {
		t.Fatal("incomplete skill order was not marked for migration")
	}
	if !reflect.DeepEqual(ss.skillOrder, []int32{100001, 110101, 110201}) {
		t.Fatalf("repaired skill order = %v", ss.skillOrder)
	}
	if !reflect.DeepEqual(ss.autoSkills, []int32{110201, 110101}) {
		t.Fatalf("filtered auto skills = %v", ss.autoSkills)
	}
	if ss.skills[110101] != 2 || ss.skills[110201] != 1 {
		t.Fatalf("learned skills dropped during repair: %v", ss.skills)
	}
}

func TestSkillLearningCostsPointAndUpgradeUsesCurrentLevelBook(t *testing.T) {
	oldTables := tables
	t.Cleanup(func() { tables = oldTables })

	const (
		skillID int32 = 120201
		bookID  int32 = 20225
	)
	tables = &datatables{
		skillConfig: map[int64]map[string]interface{}{
			int64(skillID) * 100:   testRow(map[string]int64{"MaxLevel": 4, "PreSkillId": 120101}),
			int64(skillID)*100 + 1: testRow(map[string]int64{"LearnLevel": 1}),
			int64(skillID)*100 + 2: testRow(map[string]int64{"LearnLevel": 6001}),
		},
		skillLearn: map[int64]map[string]interface{}{
			int64(skillID) * 100: {
				"MaterialArr": []interface{}{map[string]interface{}{
					"_Id": json.Number("20225"), "Count": json.Number("1"),
				}},
			},
			int64(skillID)*100 + 1: {
				"MaterialArr": []interface{}{map[string]interface{}{
					"_Id": json.Number("20225"), "Count": json.Number("2"),
				}},
			},
		},
		materialBase: map[int64]map[string]interface{}{int64(bookID): {"Name": "军官上古卷轴"}},
	}

	ss := newSession()
	ss.jobID, ss.level, ss.skillPoint = int32(protocol.JobType_Officer), 6000, 5
	ss.skills = map[int32]int32{100001: 1, 120101: 1}
	ss.skillOrder = []int32{100001, 120101}
	ss.bag = map[int32]*bagItem{0: {ItemId: bookID, ItemType: int32(protocol.ItemType_MaterialsItem), Count: 3}}
	ch := &channel{id: 2, conn: &recordingConn{}, session: ss}
	server := &Server{}

	learned := server.onLearnSkill(ch, &protocol.C2M_LearnSkill{RpcId: 1, SkillId: skillID}).(*protocol.M2C_LearnSkill)
	if learned.Message != "" || ss.skills[skillID] != 1 || ss.skillPoint != 4 || ss.bagCountOf(bookID) != 2 {
		t.Fatalf("learn result=%+v level=%d points=%d books=%d", learned, ss.skills[skillID], ss.skillPoint, ss.bagCountOf(bookID))
	}

	blocked := server.onLearnSkill(ch, &protocol.C2M_LearnSkill{RpcId: 2, SkillId: skillID}).(*protocol.M2C_LearnSkill)
	if blocked.Message != "需要等级6001" || ss.skills[skillID] != 1 || ss.skillPoint != 4 || ss.bagCountOf(bookID) != 2 {
		t.Fatalf("level gate result=%+v level=%d points=%d books=%d", blocked, ss.skills[skillID], ss.skillPoint, ss.bagCountOf(bookID))
	}

	ss.level = 6001
	upgraded := server.onLearnSkill(ch, &protocol.C2M_LearnSkill{RpcId: 3, SkillId: skillID}).(*protocol.M2C_LearnSkill)
	// 升级和学习一样要扣技能点：4 → 3。
	if upgraded.Message != "" || ss.skills[skillID] != 2 || ss.skillPoint != 3 || ss.bagCountOf(bookID) != 0 {
		t.Fatalf("upgrade result=%+v level=%d points=%d books=%d", upgraded, ss.skills[skillID], ss.skillPoint, ss.bagCountOf(bookID))
	}
}

// TestSkillUpgradeNeedsSkillPointEvenWithBooks：技能点用完后，即使技能书管够也升不动，
// 且失败时不扣书、不改等级（客户端靠 Message 提示，Error 必须保持 0）。
func TestSkillUpgradeNeedsSkillPointEvenWithBooks(t *testing.T) {
	oldTables := tables
	t.Cleanup(func() { tables = oldTables })

	const skillID int32 = 110101
	tables = &datatables{
		skillConfig: map[int64]map[string]interface{}{
			int64(skillID) * 100:   testRow(map[string]int64{"MaxLevel": 10}),
			int64(skillID)*100 + 1: testRow(map[string]int64{"LearnLevel": 1}),
		},
		skillLearn: map[int64]map[string]interface{}{
			int64(skillID)*100 + 1: {
				"MaterialArr": []interface{}{map[string]interface{}{
					"_Id": json.Number("20217"), "Count": json.Number("1"),
				}},
			},
		},
		materialBase: map[int64]map[string]interface{}{20217: {"Name": "军官技能秘籍"}},
	}

	ss := newSession()
	ss.playerID, ss.jobID, ss.level = 12, 1, 6000
	ss.skills = map[int32]int32{100001: 1, skillID: 3}
	ss.skillOrder = []int32{100001, skillID}
	ss.skillPoint = 0
	ss.bag = map[int32]*bagItem{0: {ItemId: 20217, ItemType: int32(protocol.ItemType_MaterialsItem), Count: 5}}
	ch := &channel{id: 12, conn: &recordingConn{}, session: ss}

	got := (&Server{}).onLearnSkill(ch, &protocol.C2M_LearnSkill{RpcId: 4, SkillId: skillID}).(*protocol.M2C_LearnSkill)
	if got.Error != 0 {
		t.Fatalf("Error = %d, want 0 so the client RPC stays connected", got.Error)
	}
	if got.Message != "技能点不足" {
		t.Fatalf("Message = %q, want 技能点不足", got.Message)
	}
	if ss.skills[skillID] != 3 || ss.bagCountOf(20217) != 5 {
		t.Fatalf("rejected upgrade changed state: level=%d books=%d", ss.skills[skillID], ss.bagCountOf(20217))
	}
}

func TestSkillResetMatchesClientMaterialPromises(t *testing.T) {
	oldTables := tables
	t.Cleanup(func() { tables = oldTables })
	tables = &datatables{skillLearn: map[int64]map[string]interface{}{
		11010100: {"MaterialArr": []interface{}{map[string]interface{}{"_Id": json.Number("20217"), "Count": json.Number("1")}}},
		11010101: {"MaterialArr": []interface{}{map[string]interface{}{"_Id": json.Number("20217"), "Count": json.Number("2")}}},
	}}

	newCharacter := func(playerID int64) (*session, *channel) {
		ss := newSession()
		ss.playerID, ss.jobID, ss.skillPoint, ss.voucher = playerID, int32(protocol.JobType_Officer), 4, 10
		ss.skills = map[int32]int32{100001: 1, 110101: 2}
		ss.skillOrder = []int32{100001, 110101}
		ss.autoSkills = []int32{110101}
		ss.bag = map[int32]*bagItem{}
		return ss, &channel{id: playerID, conn: &recordingConn{}, session: ss}
	}

	freeSession, freeChannel := newCharacter(10)
	(&Server{}).onFreeReSetSkill(freeChannel, &protocol.C2M_FreeReSetSkill{RpcId: 1})
	// 110101 是 2 级，逐级扣点共投入 2 点，全部返还：4 → 6。
	if freeSession.skillPoint != 6 || len(freeSession.skills) != 1 || freeSession.bagCountOf(20217) != 0 || len(freeSession.autoSkills) != 0 {
		t.Fatalf("free reset state: points=%d skills=%v books=%d auto=%v",
			freeSession.skillPoint, freeSession.skills, freeSession.bagCountOf(20217), freeSession.autoSkills)
	}

	paidSession, paidChannel := newCharacter(11)
	server := &Server{}
	price := server.onGetReSetSkillPrice(paidChannel, &protocol.C2M_GetReSetSkillPrice{RpcId: 2}).(*protocol.M2C_GetReSetSkillPrice)
	if price.Voucher != 2 {
		t.Fatalf("paid reset price = %d, want one voucher per invested skill point", price.Voucher)
	}
	result := server.onReSetSkill(paidChannel, &protocol.C2M_ReSetSkill{RpcId: 3}).(*protocol.M2C_ReSetSkill)
	if result.Message != "" || paidSession.skillPoint != 6 || paidSession.voucher != 8 ||
		paidSession.bagCountOf(20217) != 3 || len(paidSession.skills) != 1 || len(paidSession.autoSkills) != 0 {
		t.Fatalf("paid reset result=%+v points=%d voucher=%d books=%d skills=%v auto=%v",
			result, paidSession.skillPoint, paidSession.voucher, paidSession.bagCountOf(20217), paidSession.skills, paidSession.autoSkills)
	}
}

func TestMissingSkillBookCheckDoesNotPartiallyConsumeMaterials(t *testing.T) {
	oldTables := tables
	t.Cleanup(func() { tables = oldTables })

	tables = &datatables{skillLearn: map[int64]map[string]interface{}{
		11010102: {
			"MaterialArr": []interface{}{
				map[string]interface{}{"_Id": json.Number("20217"), "Count": json.Number("1")},
				map[string]interface{}{"_Id": json.Number("20218"), "Count": json.Number("2")},
			},
		},
	}}
	ss := newSession()
	ss.bag = map[int32]*bagItem{1: {ItemId: 20217, Count: 1}}

	mat, missing, ok := missingSkillLearnMaterial(ss, 110101, 2)
	if !ok || mat.itemID != 20218 || missing != 2 {
		t.Fatalf("shortage = (%+v, %d, %v), want item 20218 x2", mat, missing, ok)
	}
	if (&Server{}).takeSkillLearnMaterials(ss, 110101, 2) {
		t.Fatal("deduction succeeded with an incomplete material set")
	}
	if got := ss.bagCountOf(20217); got != 1 {
		t.Fatalf("available book was partially consumed: count=%d", got)
	}
}

// TestSkillPrerequisiteMustBeMaxedBeforeLearningNext：线上 SkillConfig.level-0 的
// PreSkillId 是一条解锁链（普攻 → 110101 → 110201 → 110301 …），前置技能必须学满到
// 它自己的 MaxLevel 才能学下一个。旧实现只要求前置「学过」（≥1 级），于是学一级就能
// 跳着把整条链解锁。
func TestSkillPrerequisiteMustBeMaxedBeforeLearningNext(t *testing.T) {
	loadOnlineTablesForTest(t)
	tables.skillLearn = nil // 材料不是本用例的关注点：无配置时按免费处理

	const (
		first  int32 = 110101 // 爆炸汽油弹：MaxLevel 7，前置 100001（普攻 MaxLevel 1）
		second int32 = 110201 // 强化攻击：MaxLevel 6，前置 110101
	)
	const firstLevels int32 = 7

	ss := newSession()
	ss.jobID, ss.level, ss.skillPoint = int32(protocol.JobType_Officer), 3000, 50
	ss.skills = map[int32]int32{100001: 1}
	ss.skillOrder = []int32{100001}
	ss.bag = map[int32]*bagItem{}
	ch := &channel{id: 1, conn: &recordingConn{}, session: ss}
	server := &Server{}

	notLearned := server.onLearnSkill(ch, &protocol.C2M_LearnSkill{RpcId: 1, SkillId: second}).(*protocol.M2C_LearnSkill)
	if notLearned.Error != 0 || notLearned.Message != "请先学习前置技能" {
		t.Fatalf("prereq never learned: %+v", notLearned)
	}

	learned := server.onLearnSkill(ch, &protocol.C2M_LearnSkill{RpcId: 2, SkillId: first}).(*protocol.M2C_LearnSkill)
	if learned.Message != "" || ss.skills[first] != 1 || ss.skillPoint != 49 {
		t.Fatalf("learn first skill: %+v level=%d points=%d", learned, ss.skills[first], ss.skillPoint)
	}

	blocked := server.onLearnSkill(ch, &protocol.C2M_LearnSkill{RpcId: 3, SkillId: second}).(*protocol.M2C_LearnSkill)
	if blocked.Error != 0 || blocked.Message != "请先把爆炸汽油弹学满" {
		t.Fatalf("prereq only level 1: %+v", blocked)
	}
	if _, ok := ss.skills[second]; ok || ss.skillPoint != 49 {
		t.Fatalf("rejected learn mutated state: skills=%v points=%d", ss.skills, ss.skillPoint)
	}

	for ss.skills[first] < firstLevels {
		upgraded := server.onLearnSkill(ch, &protocol.C2M_LearnSkill{RpcId: 4, SkillId: first}).(*protocol.M2C_LearnSkill)
		if upgraded.Message != "" {
			t.Fatalf("upgrade %d rejected: %+v", first, upgraded)
		}
	}
	if ss.skills[first] != firstLevels || ss.skillPoint != 50-firstLevels {
		t.Fatalf("prereq level=%d points=%d, want %d/%d",
			ss.skills[first], ss.skillPoint, firstLevels, 50-firstLevels)
	}

	unlocked := server.onLearnSkill(ch, &protocol.C2M_LearnSkill{RpcId: 5, SkillId: second}).(*protocol.M2C_LearnSkill)
	if unlocked.Message != "" || ss.skills[second] != 1 || ss.skillPoint != 50-firstLevels-1 {
		t.Fatalf("learn with a maxed prereq: %+v level=%d points=%d",
			unlocked, ss.skills[second], ss.skillPoint)
	}
}
