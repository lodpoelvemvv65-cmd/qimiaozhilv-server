package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"mhqserver/protocol"
)

func TestTransferTargetJobPreservesClientSexEncoding(t *testing.T) {
	for _, test := range []struct {
		current int32
		family  int32
		want    int32
	}{
		{1, 2, 3}, {2, 2, 4}, {7, 1, 1}, {8, 3, 6}, {1, 0, 0}, {1, 5, 0},
	} {
		if got := transferTargetJobID(test.current, test.family); got != test.want {
			t.Errorf("transferTargetJobID(%d, %d) = %d, want %d", test.current, test.family, got, test.want)
		}
	}
}

func TestGetBattleStateBuffReturnsAuthoritativeRuntimeEffects(t *testing.T) {
	ss := newSession()
	ss.playerID = 77
	battle := &battleState{playerHP: 100, playerMaxHP: 100, owner: ss}
	battle.runtime = NewCombatRuntime(battle, ss.playerID, nil)
	battle.effectMeta = map[string]battleEffectMetadata{
		"test:stun": {stateKey: 9},
	}
	ss.battle = battle
	_, err := battle.runtime.ApplyEffects(CombatEffectContext{
		Source: CombatUnitRef{Side: CombatSidePlayer, ID: ss.playerID},
		Target: CombatUnitRef{Side: CombatSidePlayer, ID: ss.playerID},
	}, []CombatEffect{EffectSpec{
		Key: "test:stun", Kind: CombatEffectStatus, Status: CombatStatusStunned,
		Duration: time.Minute, StackMode: CombatEffectStack, MaxStacks: 3,
	}})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{}
	response := server.onGetBattleStateBuff(&channel{session: ss}, &protocol.C2M_GetBattleStateBuff{
		UnitId: ss.playerID, RpcId: 5,
	}).(*protocol.M2C_GetBattleStateBuff)
	if len(response.InfoList) != 1 || response.InfoList[0].StateType != 9 || response.InfoList[0].Layer != 1 {
		t.Fatalf("state buff response = %+v", response.InfoList)
	}
}

func TestDeletePlayerRelationsRemovesGhostReferences(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "delete-role.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := &Server{store: store, conns: make(map[int64]*channel)}
	account1, _ := store.CreateAccount("delete-a", "pw")
	account2, _ := store.CreateAccount("delete-b", "pw")
	account3, _ := store.CreateAccount("delete-c", "pw")
	player1, _ := store.CreatePlayer(account1, "甲", 1, 1)
	player2, _ := store.CreatePlayer(account2, "乙", 3, 3)
	player3, _ := store.CreatePlayer(account3, "丙", 5, 5)
	members, _ := json.Marshal([]*familyMember{
		{ID: player1, Name: "甲", Job: 1, Level: 1},
		{ID: player2, Name: "乙", Job: 2, Level: 1},
	})
	requests, _ := json.Marshal([]int64{player1, player3})
	result, err := store.db.Exec(`INSERT INTO families (name, leader, members_json, requests_json) VALUES (?, ?, ?, ?)`,
		"删除测试家族", player1, string(members), string(requests))
	if err != nil {
		t.Fatal(err)
	}
	familyID, _ := result.LastInsertId()
	store.db.Exec(`UPDATE players SET family_id = ? WHERE id IN (?, ?)`, familyID, player1, player2)
	friendJSON := friendsToJSON(map[int64]*friendInfo{player1: {Id: player1, Name: "甲"}})
	store.db.Exec(`UPDATE players SET friends_json = ? WHERE id = ?`, friendJSON, player2)
	store.db.Exec(`INSERT INTO consignment_items (id, seller_id, item_id) VALUES (1, ?, 110305)`, player1)
	store.db.Exec(`INSERT INTO family_boss_states (family_id, boss_id, hp, max_hp) VALUES (?, 1, 10, 20)`, familyID)

	if err := server.deletePlayerRelations(player1); err != nil {
		t.Fatal(err)
	}
	var count int
	store.db.QueryRow(`SELECT COUNT(*) FROM players WHERE id = ?`, player1).Scan(&count)
	if count != 0 {
		t.Fatalf("deleted player count = %d", count)
	}
	row := server.loadFamilyRow(familyID)
	if row == nil || row.Leader != player2 || len(row.Members) != 1 || row.Members[0].ID != player2 || len(row.Requests) != 1 || row.Requests[0] != player3 {
		t.Fatalf("family after delete = %+v", row)
	}
	var rawFriends string
	store.db.QueryRow(`SELECT friends_json FROM players WHERE id = ?`, player2).Scan(&rawFriends)
	if _, exists := friendsFromJSON(rawFriends)[player1]; exists {
		t.Fatal("deleted player remains in friend list")
	}
	store.db.QueryRow(`SELECT COUNT(*) FROM consignment_items WHERE seller_id = ?`, player1).Scan(&count)
	if count != 0 {
		t.Fatal("deleted player's consignment remains")
	}
}
