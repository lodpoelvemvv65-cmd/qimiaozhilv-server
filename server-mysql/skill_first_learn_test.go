package main

import (
	"testing"

	"mhqserver/protocol"
)

func TestFirstSkillLearningRequiresBooksWithoutSpendingPointOnFailure(t *testing.T) {
	loadOnlineTablesForTest(t)
	ss := newSession()
	ss.playerID, ss.jobID, ss.level, ss.skillPoint = 9001, 1, 15000, 3
	ss.skills = map[int32]int32{100001: 1}
	ss.skillOrder = []int32{100001}
	ss.bag = make(map[int32]*bagItem)
	ch := &channel{id: 9001, session: ss, conn: &recordingConn{}}
	server := &Server{}
	materials := skillLearnMaterials(110101, 0)
	if len(materials) == 0 {
		t.Fatal("first learning has no configured book")
	}
	response := server.onLearnSkill(ch, &protocol.C2M_LearnSkill{SkillId: 110101}).(*protocol.M2C_LearnSkill)
	if response.Message == "" || ss.skillPoint != 3 || ss.skills[110101] != 0 {
		t.Fatalf("missing book consumed point or learned skill: %+v points=%d skills=%v", response, ss.skillPoint, ss.skills)
	}
	for index, material := range materials {
		ss.bag[int32(index)] = &bagItem{ItemId: material.itemID, Count: material.count, ItemType: int32(protocol.ItemType_MaterialsItem)}
	}
	response = server.onLearnSkill(ch, &protocol.C2M_LearnSkill{SkillId: 110101}).(*protocol.M2C_LearnSkill)
	if response.Message != "" || ss.skillPoint != 2 || ss.skills[110101] != 1 {
		t.Fatalf("learn with book failed: %+v", response)
	}
	for _, material := range materials {
		if ss.bagCountOf(material.itemID) != 0 {
			t.Fatal("learning did not consume configured book")
		}
	}
}
