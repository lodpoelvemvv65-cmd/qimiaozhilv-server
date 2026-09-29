package main

import (
	"path/filepath"
	"testing"

	"mhqserver/protocol"
)

// familySelfhealTestServer 造一个「family_id 指向已不存在的家族」的角色。
//
// 复现实测库形态：players.id=2 的 family_id=69，families 里没有 69，
// family_members 里也没有该玩家，但 family_boss_states 残留了 family_id=69。
//
// 这个残留形状是外键不对称直接决定的，不是巧合：
//
//	family_members.family_id  → families.id ON DELETE CASCADE   （自动清）
//	family_requests.family_id → families.id ON DELETE CASCADE   （自动清）
//	players.family_id         无外键                            （残留 → 悬空）
//	family_boss_states.family_id 无外键                          （残留）
//
// 所以任何直接 DELETE FROM families 都会把成员表和申请表级联清掉，却把
// players.family_id 和 family_boss_* 留在原地。成员行这里插不进去也不该插：
// 外键会直接拒绝，这正是线上看不到孤儿成员行的原因。
func familySelfhealTestServer(t *testing.T) (*Server, *channel, int64, int64) {
	t.Helper()
	store, err := OpenStore(mysqlTestDSN(t, filepath.Join(t.TempDir(), "family-selfheal.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)

	const (
		playerID = int64(501)
		deadID   = int64(999) // 已不存在的家族
	)
	if _, err := store.db.Exec(`INSERT INTO players (id, account_id, name, job_id, skin_id, family_id, family_contribute, personal_contribute)
		VALUES (?, 1, 'stuck', 1, 1, ?, 100, 300)`, playerID, deadID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO family_boss_states (family_id, boss_id, hp, max_hp, has_reward, dead_at)
		VALUES (?, 1, 100, 200, 0, 0)`, deadID); err != nil {
		t.Fatal(err)
	}

	server := &Server{store: store, conns: make(map[int64]*channel)}
	ss := newSession()
	ss.state, ss.playerID, ss.name = sessInGame, playerID, "stuck"
	ss.jobID, ss.skinID = 1, 1
	ss.familyID, ss.familyContribute, ss.personalContribute = deadID, 100, 300
	ch := &channel{id: playerID, session: ss, conn: &recordingConn{}}
	server.conns[playerID] = ch
	return server, ch, playerID, deadID
}

func playerFamilyRow(t *testing.T, server *Server, playerID int64) (familyID, contribute, personal int64) {
	t.Helper()
	if err := server.store.db.QueryRow(`SELECT family_id, family_contribute, personal_contribute FROM players WHERE id = ?`, playerID).
		Scan(&familyID, &contribute, &personal); err != nil {
		t.Fatal(err)
	}
	return familyID, contribute, personal
}

func rowCount(t *testing.T, server *Server, query string, args ...any) int {
	t.Helper()
	var n int
	if err := server.store.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// 打开家族面板是玩家遇到悬空引用时最先走到的路径：20125 → 「家族不存在」。
// 修复后应当返回「没有家族」，并把库里的悬空引用连同孤儿行一起清掉。
func TestHealDanglingFamilyRefOnGetFamily(t *testing.T) {
	server, ch, playerID, deadID := familySelfhealTestServer(t)

	resp := server.onGetFamily(ch, &protocol.C2M_GetFamily{RpcId: 1}).(*protocol.M2C_GetFamily)
	if resp.Message != "没有家族" {
		t.Fatalf("get family message = %q, want 没有家族", resp.Message)
	}
	if ch.session.familyID != 0 || ch.session.familyContribute != 0 || ch.session.personalContribute != 0 {
		t.Fatalf("session still attached: family=%d contribute=%d personal=%d",
			ch.session.familyID, ch.session.familyContribute, ch.session.personalContribute)
	}
	familyID, contribute, personal := playerFamilyRow(t, server, playerID)
	if familyID != 0 || contribute != 0 || personal != 0 {
		t.Fatalf("players row still attached: family=%d contribute=%d personal=%d", familyID, contribute, personal)
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM family_members WHERE family_id = ?`,
		`SELECT COUNT(*) FROM family_requests WHERE family_id = ?`,
		`SELECT COUNT(*) FROM family_boss_states WHERE family_id = ?`,
	} {
		if n := rowCount(t, server, q, deadID); n != 0 {
			t.Fatalf("orphan rows left for family %d: %d", deadID, n)
		}
	}
}

// 修复必须可重入：第二次打开面板不能再报错，也不能把已清干净的状态再改一遍。
func TestHealDanglingFamilyRefIsIdempotent(t *testing.T) {
	server, ch, playerID, _ := familySelfhealTestServer(t)

	for i := 0; i < 2; i++ {
		resp := server.onGetFamily(ch, &protocol.C2M_GetFamily{RpcId: int32(i + 1)}).(*protocol.M2C_GetFamily)
		if resp.Message != "没有家族" {
			t.Fatalf("round %d message = %q, want 没有家族", i+1, resp.Message)
		}
	}
	if familyID, _, _ := playerFamilyRow(t, server, playerID); familyID != 0 {
		t.Fatalf("players.family_id = %d, want 0", familyID)
	}
}

// 用户报的另一个症状：点创建家族被自己的悬空引用挡成「已有家族」。
func TestHealDanglingFamilyRefAllowsCreate(t *testing.T) {
	server, ch, playerID, deadID := familySelfhealTestServer(t)

	resp := server.onCrateFamily(ch, &protocol.C2M_CrateFamily{RpcId: 1, Name: "新家族"}).(*protocol.M2C_CrateFamily)
	if resp.Message != "" {
		t.Fatalf("create family message = %q, want empty", resp.Message)
	}
	if resp.Info == nil || resp.Info.Name != "新家族" {
		t.Fatalf("create family info = %+v", resp.Info)
	}
	familyID, _, _ := playerFamilyRow(t, server, playerID)
	if familyID == 0 || familyID == deadID {
		t.Fatalf("players.family_id = %d, want a new family id", familyID)
	}
	if n := rowCount(t, server, `SELECT COUNT(*) FROM families WHERE id = ?`, familyID); n != 1 {
		t.Fatalf("new family rows = %d, want 1", n)
	}
	if n := rowCount(t, server, `SELECT COUNT(*) FROM family_members WHERE family_id = ? AND player_id = ?`, familyID, playerID); n != 1 {
		t.Fatalf("new family member rows = %d, want 1", n)
	}
}

// 第三个症状：申请加入任何家族都被「已有家族」挡住。
func TestHealDanglingFamilyRefAllowsJoin(t *testing.T) {
	server, ch, playerID, _ := familySelfhealTestServer(t)

	const targetID = int64(77)
	if _, err := server.store.db.Exec(`INSERT INTO families (id, name, leader) VALUES (?, 'target', 601)`, targetID); err != nil {
		t.Fatal(err)
	}

	resp := server.onRequestEnterFamily(ch, &protocol.C2M_RequestEnterFamily{RpcId: 1, Name: "target"}).(*protocol.M2C_RequestEnterFamily)
	if resp.Message != "" {
		t.Fatalf("request enter family message = %q, want empty", resp.Message)
	}
	if n := rowCount(t, server, `SELECT COUNT(*) FROM family_requests WHERE family_id = ? AND player_id = ?`, targetID, playerID); n != 1 {
		t.Fatalf("family_requests rows = %d, want 1", n)
	}
	if familyID, _, _ := playerFamilyRow(t, server, playerID); familyID != 0 {
		t.Fatalf("players.family_id = %d, want 0 before approval", familyID)
	}
}

// 自愈不能误伤活着的家族：成员行、申请行、BOSS 进度都得原样留着。
func TestHealLeavesHealthyFamilyAlone(t *testing.T) {
	server, ch, playerID, _ := familySelfhealTestServer(t)

	const liveID = int64(88)
	// 成员表和申请表都有外键，必须先有 families 行才插得进去。
	if _, err := server.store.db.Exec(`INSERT INTO families (id, name, leader) VALUES (?, 'live', ?)`, liveID, playerID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.store.db.Exec(`INSERT INTO family_members (family_id, player_id, name, job_id, level, last_login)
		VALUES (?, ?, 'stuck', 1, 1, 0)`, liveID, playerID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.store.db.Exec(`INSERT INTO family_requests (family_id, player_id) VALUES (?, ?)`, liveID, playerID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.store.db.Exec(`INSERT INTO family_boss_states (family_id, boss_id, hp, max_hp, has_reward, dead_at)
		VALUES (?, 2, 100, 200, 0, 0)`, liveID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.store.db.Exec(`UPDATE players SET family_id = ? WHERE id = ?`, liveID, playerID); err != nil {
		t.Fatal(err)
	}
	ch.session.familyID = liveID

	if healed := server.healDanglingFamily(ch); healed {
		t.Fatal("healDanglingFamily touched a healthy family")
	}
	if ch.session.familyID != liveID {
		t.Fatalf("session family = %d, want %d", ch.session.familyID, liveID)
	}
	if familyID, _, _ := playerFamilyRow(t, server, playerID); familyID != liveID {
		t.Fatalf("players.family_id = %d, want %d", familyID, liveID)
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM family_members WHERE family_id = ?`,
		`SELECT COUNT(*) FROM family_requests WHERE family_id = ?`,
		`SELECT COUNT(*) FROM family_boss_states WHERE family_id = ?`,
	} {
		if n := rowCount(t, server, q, liveID); n != 1 {
			t.Fatalf("healthy family rows = %d, want 1 (%s)", n, q)
		}
	}
}

// 摘除：把角色从活着的家族里拿出来，剩下的人接任族长，家族不散。
func TestDetachPlayerFromFamilyPromotesRemainingMember(t *testing.T) {
	server, ch, playerID, _ := familySelfhealTestServer(t)

	const (
		liveID  = int64(88)
		otherID = int64(602)
	)
	if _, err := server.store.db.Exec(`INSERT INTO families (id, name, leader) VALUES (?, 'live', ?)`, liveID, playerID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.store.db.Exec(`INSERT INTO family_members (family_id, player_id, name, job_id, level, last_login)
		VALUES (?, ?, 'stuck', 1, 1, 0), (?, ?, 'other', 1, 1, 0)`, liveID, playerID, liveID, otherID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.store.db.Exec(`UPDATE players SET family_id = ? WHERE id = ?`, liveID, playerID); err != nil {
		t.Fatal(err)
	}
	ch.session.familyID = liveID

	result, err := server.detachPlayerFromFamily(playerID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Dissolved {
		t.Fatalf("family dissolved but %d still inside: %+v", otherID, result)
	}
	if result.PromotedLeader != otherID {
		t.Fatalf("promoted leader = %d, want %d", result.PromotedLeader, otherID)
	}
	if familyID, _, _ := playerFamilyRow(t, server, playerID); familyID != 0 {
		t.Fatalf("detached player family_id = %d, want 0", familyID)
	}
	if n := rowCount(t, server, `SELECT COUNT(*) FROM family_members WHERE family_id = ?`, liveID); n != 1 {
		t.Fatalf("remaining members = %d, want 1", n)
	}
	if n := rowCount(t, server, `SELECT COUNT(*) FROM families WHERE id = ? AND leader = ?`, liveID, otherID); n != 1 {
		t.Fatalf("family leader not promoted: %d", n)
	}
}

// 摘除最后一个成员时家族应当整体解散，不能留下空壳家族占着名字。
func TestDetachLastMemberDissolvesFamily(t *testing.T) {
	server, ch, playerID, _ := familySelfhealTestServer(t)

	const liveID = int64(88)
	if _, err := server.store.db.Exec(`INSERT INTO families (id, name, leader) VALUES (?, 'solo', ?)`, liveID, playerID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.store.db.Exec(`INSERT INTO family_members (family_id, player_id, name, job_id, level, last_login)
		VALUES (?, ?, 'stuck', 1, 1, 0)`, liveID, playerID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.store.db.Exec(`INSERT INTO family_boss_states (family_id, boss_id, hp, max_hp, has_reward, dead_at)
		VALUES (?, 2, 100, 200, 0, 0)`, liveID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.store.db.Exec(`UPDATE players SET family_id = ? WHERE id = ?`, liveID, playerID); err != nil {
		t.Fatal(err)
	}
	ch.session.familyID = liveID

	result, err := server.detachPlayerFromFamily(playerID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Dissolved {
		t.Fatalf("family not dissolved: %+v", result)
	}
	if n := rowCount(t, server, `SELECT COUNT(*) FROM families WHERE id = ?`, liveID); n != 0 {
		t.Fatalf("families rows = %d, want 0", n)
	}
	if n := rowCount(t, server, `SELECT COUNT(*) FROM family_boss_states WHERE family_id = ?`, liveID); n != 0 {
		t.Fatalf("family_boss_states rows = %d, want 0", n)
	}
	if familyID, _, _ := playerFamilyRow(t, server, playerID); familyID != 0 {
		t.Fatalf("players.family_id = %d, want 0", familyID)
	}
}

// 解散命令要能治「家族行不在、但还有角色挂在它上面」这种形态——正是线上那个角色。
func TestDissolveFamilyClearsDanglingMembers(t *testing.T) {
	server, _, playerID, deadID := familySelfhealTestServer(t)

	result, err := server.dissolveFamily(deadID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Dissolved {
		t.Fatalf("result = %+v", result)
	}
	if len(result.RemovedMembers) != 1 || result.RemovedMembers[0] != playerID {
		t.Fatalf("removed members = %v, want [%d]", result.RemovedMembers, playerID)
	}
	if familyID, contribute, personal := playerFamilyRow(t, server, playerID); familyID != 0 || contribute != 0 || personal != 0 {
		t.Fatalf("players row = family=%d contribute=%d personal=%d", familyID, contribute, personal)
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM family_members WHERE family_id = ?`,
		`SELECT COUNT(*) FROM family_boss_states WHERE family_id = ?`,
	} {
		if n := rowCount(t, server, q, deadID); n != 0 {
			t.Fatalf("rows left for family %d: %d (%s)", deadID, n, q)
		}
	}
}
