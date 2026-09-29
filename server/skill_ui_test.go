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
	if learned.Message != "" || ss.skills[skillID] != 1 || ss.skillPoint != 4 || ss.bagCountOf(bookID) != 3 {
		t.Fatalf("learn result=%+v level=%d points=%d books=%d", learned, ss.skills[skillID], ss.skillPoint, ss.bagCountOf(bookID))
	}

	blocked := server.onLearnSkill(ch, &protocol.C2M_LearnSkill{RpcId: 2, SkillId: skillID}).(*protocol.M2C_LearnSkill)
	if blocked.Message != "需要等级6001" || ss.skills[skillID] != 1 || ss.skillPoint != 4 || ss.bagCountOf(bookID) != 3 {
		t.Fatalf("level gate result=%+v level=%d points=%d books=%d", blocked, ss.skills[skillID], ss.skillPoint, ss.bagCountOf(bookID))
	}

	ss.level = 6001
	upgraded := server.onLearnSkill(ch, &protocol.C2M_LearnSkill{RpcId: 3, SkillId: skillID}).(*protocol.M2C_LearnSkill)
	if upgraded.Message != "" || ss.skills[skillID] != 2 || ss.skillPoint != 4 || ss.bagCountOf(bookID) != 1 {
		t.Fatalf("upgrade result=%+v level=%d points=%d books=%d", upgraded, ss.skills[skillID], ss.skillPoint, ss.bagCountOf(bookID))
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
	if freeSession.skillPoint != 5 || len(freeSession.skills) != 1 || freeSession.bagCountOf(20217) != 0 || len(freeSession.autoSkills) != 0 {
		t.Fatalf("free reset state: points=%d skills=%v books=%d auto=%v",
			freeSession.skillPoint, freeSession.skills, freeSession.bagCountOf(20217), freeSession.autoSkills)
	}

	paidSession, paidChannel := newCharacter(11)
	server := &Server{}
	price := server.onGetReSetSkillPrice(paidChannel, &protocol.C2M_GetReSetSkillPrice{RpcId: 2}).(*protocol.M2C_GetReSetSkillPrice)
	if price.Voucher != 1 {
		t.Fatalf("paid reset price = %d, want one learned skill", price.Voucher)
	}
	result := server.onReSetSkill(paidChannel, &protocol.C2M_ReSetSkill{RpcId: 3}).(*protocol.M2C_ReSetSkill)
	if result.Message != "" || paidSession.skillPoint != 5 || paidSession.voucher != 9 ||
		paidSession.bagCountOf(20217) != 2 || len(paidSession.skills) != 1 || len(paidSession.autoSkills) != 0 {
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
