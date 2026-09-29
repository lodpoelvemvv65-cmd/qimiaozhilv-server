package main

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"mhqserver/protocol"
)

func TestLoginCredentialsRemainStrictAcrossRoleDeletion(t *testing.T) {
	// Unity.Model ErrorCode.IsRpcNeedThrowException only returns false for
	// zero, -1, or values above 200000. LoginHelper can display Message only
	// when Session.OnRead delivers the response instead of raising an RPC error.
	for name, code := range map[string]int32{
		"credentials": errPwdError,
		"voucher":     errKeyInvalid,
	} {
		if code <= 200000 {
			t.Fatalf("%s error %d is not client-visible", name, code)
		}
	}
	store, err := OpenStore(mysqlTestDSN(t, "login-role-delete"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := &Server{store: store, conns: make(map[int64]*channel)}
	accountID, err := store.CreateAccount("login-owner", "correct-password")
	if err != nil {
		t.Fatal(err)
	}
	playerID, err := store.CreatePlayer(accountID, "登录校验角色", 1, 1)
	if err != nil {
		t.Fatal(err)
	}

	wrongSession := newSession()
	wrong := server.onLogin(&channel{id: 1, session: wrongSession}, &protocol.C2R_Login{
		Account: "login-owner", Password: "wrong-password", RpcId: 1,
	}).(*protocol.R2C_Login)
	if wrong.Error != errPwdError || wrong.Message != "账号或密码错误" || wrong.Key != 0 || wrongSession.accountID != 0 {
		t.Fatalf("wrong-password login = %+v sessionAccount=%d", wrong, wrongSession.accountID)
	}

	missingSession := newSession()
	missing := server.onLogin(&channel{id: 11, session: missingSession}, &protocol.C2R_Login{
		Account: "missing-owner", Password: "any-password", RpcId: 11,
	}).(*protocol.R2C_Login)
	if missing.Error != errPwdError || missing.Message != "账号或密码错误" || missing.Key != 0 || missingSession.accountID != 0 {
		t.Fatalf("missing-account login = %+v sessionAccount=%d", missing, missingSession.accountID)
	}

	loginSession := newSession()
	login := server.onLogin(&channel{id: 2, session: loginSession}, &protocol.C2R_Login{
		Account: "login-owner", Password: "correct-password", RpcId: 2,
	}).(*protocol.R2C_Login)
	voucherAccount, voucherKey := parseLoginVoucher(login.LoginVoucher)
	if login.Error != 0 || login.Key <= 0 || voucherKey <= 0 || voucherAccount != "login-owner" {
		t.Fatalf("valid login credentials = %+v parsed=%q/%d", login, voucherAccount, voucherKey)
	}
	if consumedAccount, ok := store.ConsumeKey(login.Key); !ok || consumedAccount != accountID {
		t.Fatalf("gate key account=%d ok=%v", consumedAccount, ok)
	}

	reconnectSession := newSession()
	reconnect := server.onLogin(&channel{id: 3, session: reconnectSession}, &protocol.C2R_Login{
		Account: "login-owner", Password: login.LoginVoucher,
		LoginType: protocol.LoginType_Voucher, RpcId: 3,
	}).(*protocol.R2C_Login)
	if reconnect.Error != 0 || reconnect.Key <= 0 || reconnect.LoginVoucher == login.LoginVoucher || reconnectSession.accountID != accountID {
		t.Fatalf("voucher reconnect = %+v sessionAccount=%d", reconnect, reconnectSession.accountID)
	}
	if _, err := store.CreateAccount("different-owner", "different-password"); err != nil {
		t.Fatal(err)
	}
	mismatched := server.onLogin(&channel{id: 31, session: newSession()}, &protocol.C2R_Login{
		Account: "different-owner", Password: reconnect.LoginVoucher,
		LoginType: protocol.LoginType_Voucher, RpcId: 31,
	}).(*protocol.R2C_Login)
	if mismatched.Error != errPwdError || mismatched.Message != "账号或密码错误" || mismatched.Key != 0 {
		t.Fatalf("account-mismatched voucher = %+v", mismatched)
	}

	replayed := server.onLogin(&channel{id: 4, session: newSession()}, &protocol.C2R_Login{
		Account: "login-owner", Password: login.LoginVoucher,
		LoginType: protocol.LoginType_Voucher, RpcId: 4,
	}).(*protocol.R2C_Login)
	if replayed.Error != errPwdError || replayed.Message != "账号或密码错误" || replayed.Key != 0 {
		t.Fatalf("replayed voucher = %+v", replayed)
	}

	passwordFallback := server.onLogin(&channel{id: 41, session: newSession()}, &protocol.C2R_Login{
		Account: "login-owner", Password: "correct-password",
		LoginType: protocol.LoginType_Voucher, RpcId: 41,
	}).(*protocol.R2C_Login)
	if passwordFallback.Error != 0 || passwordFallback.Key <= 0 {
		t.Fatalf("password fallback from stale voucher UI = %+v", passwordFallback)
	}

	deleteSession := newSession()
	deleteSession.accountID = accountID
	deleteSession.playerID = playerID
	deleteSession.state = sessLogged
	deleted := server.onDelRole(&channel{id: 5, session: deleteSession}, &protocol.C2R_DelRole{
		UserId: playerID, RpcId: 5,
	}).(*protocol.R2C_DelRole)
	if deleted.Error != 0 || deleted.Message != "" || deleteSession.accountID != accountID ||
		deleteSession.playerID != 0 || deleteSession.reservedPlayerID != playerID {
		t.Fatalf("delete role = %+v sessionAccount/player/reserved=%d/%d/%d",
			deleted, deleteSession.accountID, deleteSession.playerID, deleteSession.reservedPlayerID)
	}
	storedAccountID, storedPassword, err := store.FindAccount("login-owner")
	if err != nil || storedAccountID != accountID || storedPassword != "correct-password" {
		t.Fatalf("account after role delete id/password/err=%d/%q/%v", storedAccountID, storedPassword, err)
	}
	if _, err := store.FirstPlayer(accountID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("player still exists after deletion: %v", err)
	}

	wrongAfterDelete := server.onLogin(&channel{id: 6, session: newSession()}, &protocol.C2R_Login{
		Account: "login-owner", Password: "wrong-password", RpcId: 6,
	}).(*protocol.R2C_Login)
	if wrongAfterDelete.Error != errPwdError || wrongAfterDelete.Message != "账号或密码错误" || wrongAfterDelete.Key != 0 {
		t.Fatalf("wrong password after role delete = %+v", wrongAfterDelete)
	}
}

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
	store, err := OpenStore(mysqlTestDSN(t, "delete-role"))
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
	result, err := store.db.Exec(`INSERT INTO families (name, leader) VALUES (?, ?)`, "删除测试家族", player1)
	if err != nil {
		t.Fatal(err)
	}
	familyID, _ := result.LastInsertId()
	for _, member := range []*familyMember{{ID: player1, Name: "甲", Job: 1, Level: 1}, {ID: player2, Name: "乙", Job: 2, Level: 1}} {
		if _, err := store.db.Exec(`INSERT INTO family_members (family_id, player_id, name, job_id, level, last_login) VALUES (?, ?, ?, ?, ?, ?)`,
			familyID, member.ID, member.Name, member.Job, member.Level, member.LastLogin); err != nil {
			t.Fatal(err)
		}
	}
	for _, requesterID := range []int64{player1, player3} {
		if _, err := store.db.Exec(`INSERT INTO family_requests (family_id, player_id) VALUES (?, ?)`, familyID, requesterID); err != nil {
			t.Fatal(err)
		}
	}
	store.db.Exec(`UPDATE players SET family_id = ? WHERE id IN (?, ?)`, familyID, player1, player2)
	store.db.Exec(`INSERT INTO player_friends (player_id, friend_id, name, job_id, level, last_login) VALUES (?, ?, ?, 1, 1, 0)`, player2, player1, "甲")
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
	store.db.QueryRow(`SELECT COUNT(*) FROM player_friends WHERE player_id = ? AND friend_id = ?`, player2, player1).Scan(&count)
	if count != 0 {
		t.Fatal("deleted player remains in friend list")
	}
	store.db.QueryRow(`SELECT COUNT(*) FROM consignment_items WHERE seller_id = ?`, player1).Scan(&count)
	if count != 0 {
		t.Fatal("deleted player's consignment remains")
	}
}
