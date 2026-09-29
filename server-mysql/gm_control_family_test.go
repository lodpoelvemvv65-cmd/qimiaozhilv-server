package main

import (
	"testing"

	json "github.com/goccy/go-json"
)

// gmFamilyResetTestServer 复用 familySelfhealTestServer 的造数：一个在线角色，
// 会话与库里都可能指向一个家族。这里只补一个「把角色放进活着的家族」的助手。
func gmFamilyResetTestServer(t *testing.T) (*Server, *channel, int64) {
	t.Helper()
	server, ch, playerID, _ := familySelfhealTestServer(t)
	// 去掉 fixture 里的悬空引用，让每个用例自己决定角色的归属形态。
	if _, err := server.store.db.Exec(`UPDATE players SET family_id = 0, family_contribute = 0, personal_contribute = 0 WHERE id = ?`, playerID); err != nil {
		t.Fatal(err)
	}
	ch.session.familyID, ch.session.familyContribute, ch.session.personalContribute = 0, 0, 0
	return server, ch, playerID
}

// ensureTestPlayer 补齐 players 行。fixture 只造了 501，其他成员要先有行才能挂归属。
// players 上没有 account_id 唯一键（只有普通索引 idx_players_account），所以可以共用一个账号。
func ensureTestPlayer(t *testing.T, server *Server, playerID int64) {
	t.Helper()
	if _, err := server.store.db.Exec(`INSERT IGNORE INTO players (id, account_id, name, job_id, skin_id, family_id, family_contribute, personal_contribute)
		VALUES (?, 1, ?, 1, 1, 0, 0, 0)`, playerID, "p"+strconvI64(playerID)); err != nil {
		t.Fatal(err)
	}
}

// joinFamily 造一个活着的家族，把 members 里的角色全部放进去，leader 为第一个。
func joinFamily(t *testing.T, server *Server, familyID int64, name string, leader int64, members ...int64) {
	t.Helper()
	if _, err := server.store.db.Exec(`INSERT INTO families (id, name, leader) VALUES (?, ?, ?)`, familyID, name, leader); err != nil {
		t.Fatal(err)
	}
	for _, id := range members {
		ensureTestPlayer(t, server, id)
		if _, err := server.store.db.Exec(`INSERT INTO family_members (family_id, player_id, name, job_id, level, last_login)
			VALUES (?, ?, ?, 1, 1, 0)`, familyID, id, name); err != nil {
			t.Fatal(err)
		}
		if _, err := server.store.db.Exec(`UPDATE players SET family_id = ?, family_contribute = 10, personal_contribute = 20 WHERE id = ?`, familyID, id); err != nil {
			t.Fatal(err)
		}
	}
}

func gmResetCall(t *testing.T, server *Server, playerID int64, raw string) (string, map[string]any, string, string) {
	t.Helper()
	return server.gmResetFamily(playerID, json.RawMessage(raw))
}

// 默认模式 detach：把角色从还有别人的家族里摘出来，族长让给剩下的成员，
// 家族本身不散，库和会话都要清干净。
func TestGMFamilyResetDetachPromotesRemainingMember(t *testing.T) {
	server, ch, playerID := gmFamilyResetTestServer(t)

	const (
		liveID  = int64(88)
		otherID = int64(602)
	)
	joinFamily(t, server, liveID, "live", playerID, playerID, otherID)
	ch.session.familyID, ch.session.familyContribute, ch.session.personalContribute = liveID, 10, 20

	status, data, code, msg := gmResetCall(t, server, playerID, `{}`)
	if status != "completed" || code != "" || msg != "" {
		t.Fatalf("reset = %s code=%q msg=%q", status, code, msg)
	}
	if data["familyId"] != liveID {
		t.Fatalf("familyId = %v, want %d", data["familyId"], liveID)
	}
	if data["familyName"] != "live" {
		t.Fatalf("familyName = %v, want live", data["familyName"])
	}
	if data["dissolved"] != false {
		t.Fatalf("dissolved = %v, want false (family still has %d)", data["dissolved"], otherID)
	}
	if data["promotedLeader"] != otherID {
		t.Fatalf("promotedLeader = %v, want %d", data["promotedLeader"], otherID)
	}
	if ch.session.familyID != 0 || ch.session.familyContribute != 0 || ch.session.personalContribute != 0 {
		t.Fatalf("session still attached: family=%d contribute=%d personal=%d",
			ch.session.familyID, ch.session.familyContribute, ch.session.personalContribute)
	}
	if familyID, contribute, personal := playerFamilyRow(t, server, playerID); familyID != 0 || contribute != 0 || personal != 0 {
		t.Fatalf("players row = family=%d contribute=%d personal=%d", familyID, contribute, personal)
	}
	if n := rowCount(t, server, `SELECT COUNT(*) FROM family_members WHERE family_id = ?`, liveID); n != 1 {
		t.Fatalf("remaining members = %d, want 1", n)
	}
	if n := rowCount(t, server, `SELECT COUNT(*) FROM families WHERE id = ? AND leader = ?`, liveID, otherID); n != 1 {
		t.Fatalf("leader not promoted: %d", n)
	}
}

// detach 摘掉最后一个成员时家族整体解散，family_boss_states 也要跟着清掉。
func TestGMFamilyResetDetachDissolvesLastMember(t *testing.T) {
	server, ch, playerID := gmFamilyResetTestServer(t)

	const liveID = int64(88)
	joinFamily(t, server, liveID, "solo", playerID, playerID)
	if _, err := server.store.db.Exec(`INSERT INTO family_boss_states (family_id, boss_id, hp, max_hp, has_reward, dead_at)
		VALUES (?, 2, 100, 200, 0, 0)`, liveID); err != nil {
		t.Fatal(err)
	}
	ch.session.familyID = liveID

	status, data, code, msg := gmResetCall(t, server, playerID, `{"mode":"detach"}`)
	if status != "completed" || code != "" || msg != "" {
		t.Fatalf("reset = %s code=%q msg=%q", status, code, msg)
	}
	if data["dissolved"] != true {
		t.Fatalf("dissolved = %v, want true", data["dissolved"])
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

// dissolve 解散整个家族：全体成员清归属，其他在线成员的会话也要一起清。
func TestGMFamilyResetDissolveClearsEveryMember(t *testing.T) {
	server, ch, playerID := gmFamilyResetTestServer(t)

	const (
		liveID  = int64(88)
		otherID = int64(602)
	)
	joinFamily(t, server, liveID, "whole", playerID, playerID, otherID)
	ch.session.familyID = liveID

	otherSession := newSession()
	otherSession.state, otherSession.playerID, otherSession.name = sessInGame, otherID, "other"
	otherSession.familyID, otherSession.familyContribute = liveID, 10
	otherCh := &channel{id: otherID, session: otherSession, conn: &recordingConn{}}
	server.conns[otherID] = otherCh

	status, data, code, msg := gmResetCall(t, server, playerID, `{"mode":"dissolve"}`)
	if status != "completed" || code != "" || msg != "" {
		t.Fatalf("reset = %s code=%q msg=%q", status, code, msg)
	}
	if data["dissolved"] != true {
		t.Fatalf("dissolved = %v, want true", data["dissolved"])
	}
	removed, _ := data["removedMembers"].([]int64)
	if len(removed) != 2 {
		t.Fatalf("removedMembers = %v, want both members", removed)
	}
	for _, target := range []struct {
		label string
		sess  *session
		id    int64
	}{
		{"target", ch.session, playerID},
		{"other", otherCh.session, otherID},
	} {
		if target.sess.familyID != 0 || target.sess.familyContribute != 0 || target.sess.personalContribute != 0 {
			t.Fatalf("%s session still attached: family=%d contribute=%d personal=%d",
				target.label, target.sess.familyID, target.sess.familyContribute, target.sess.personalContribute)
		}
		if familyID, contribute, personal := playerFamilyRow(t, server, target.id); familyID != 0 || contribute != 0 || personal != 0 {
			t.Fatalf("%s players row = family=%d contribute=%d personal=%d", target.label, familyID, contribute, personal)
		}
	}
	if n := rowCount(t, server, `SELECT COUNT(*) FROM families WHERE id = ?`, liveID); n != 0 {
		t.Fatalf("families rows = %d, want 0", n)
	}
}

// 库里已经清干净、只剩在线会话还记着旧家族 ID —— 这正是线上实测库的形态
// （players.family_id=0，会话里还是 69，family_boss_states 残留 5 行）。
// 命令必须治得了这一路：清会话、清孤儿行，并按 dangling 回显。
func TestGMFamilyResetClearsStaleSessionOverCleanDatabase(t *testing.T) {
	server, ch, playerID := gmFamilyResetTestServer(t)

	// 999 这个家族 ID 和它那行 family_boss_states 由 familySelfhealTestServer 造好，
	// 正是线上实测库的形态：families 里没有 999，family_boss_states 里还留着。
	const staleID = int64(999)
	ch.session.familyID, ch.session.familyContribute, ch.session.personalContribute = staleID, 100, 300

	status, data, code, msg := gmResetCall(t, server, playerID, `{}`)
	if status != "completed" || code != "" || msg != "" {
		t.Fatalf("reset = %s code=%q msg=%q", status, code, msg)
	}
	if data["familyId"] != staleID {
		t.Fatalf("familyId = %v, want %d (the stale session id)", data["familyId"], staleID)
	}
	if data["dangling"] != true {
		t.Fatalf("dangling = %v, want true", data["dangling"])
	}
	if data["dissolved"] != false {
		t.Fatalf("dissolved = %v, want false (nothing was deleted, the family was already gone)", data["dissolved"])
	}
	if ch.session.familyID != 0 || ch.session.familyContribute != 0 || ch.session.personalContribute != 0 {
		t.Fatalf("stale session survived: family=%d contribute=%d personal=%d",
			ch.session.familyID, ch.session.familyContribute, ch.session.personalContribute)
	}
	if n := rowCount(t, server, `SELECT COUNT(*) FROM family_boss_states WHERE family_id = ?`, staleID); n != 0 {
		t.Fatalf("orphan family_boss_states rows = %d, want 0", n)
	}
}

// 没有家族时返回 completed 而不是 rejected：带 Idempotency-Key 的重试不能因为
// 「第一次已经修好了」就变成失败。
func TestGMFamilyResetWithoutFamilyIsIdempotent(t *testing.T) {
	server, ch, playerID := gmFamilyResetTestServer(t)

	for round := 1; round <= 2; round++ {
		status, data, code, msg := gmResetCall(t, server, playerID, `{}`)
		if status != "completed" || code != "" || msg != "" {
			t.Fatalf("round %d = %s code=%q msg=%q", round, status, code, msg)
		}
		if data["familyId"] != int64(0) {
			t.Fatalf("round %d familyId = %v, want 0", round, data["familyId"])
		}
		if data["message"] != "该角色当前没有家族" {
			t.Fatalf("round %d message = %v", round, data["message"])
		}
	}
	if ch.session.familyID != 0 {
		t.Fatalf("session family = %d, want 0", ch.session.familyID)
	}
}

// dissolve 在角色本来就没有家族时同样只清会话，不报错。
func TestGMFamilyResetDissolveWithoutFamily(t *testing.T) {
	server, _, playerID := gmFamilyResetTestServer(t)

	status, data, code, msg := gmResetCall(t, server, playerID, `{"mode":"dissolve"}`)
	if status != "completed" || code != "" || msg != "" {
		t.Fatalf("reset = %s code=%q msg=%q", status, code, msg)
	}
	if data["familyId"] != int64(0) || data["dissolved"] != false {
		t.Fatalf("data = %+v", data)
	}
}

// 模式只认 detach / dissolve，写错要 4xx，不能悄悄按默认模式执行。
func TestGMFamilyResetRejectsUnknownMode(t *testing.T) {
	server, ch, playerID := gmFamilyResetTestServer(t)

	const liveID = int64(88)
	joinFamily(t, server, liveID, "live", playerID, playerID)
	ch.session.familyID = liveID

	for _, raw := range []string{`{"mode":"nuke"}`, `{"mode":123}`, `{"mode":`} {
		status, _, code, _ := gmResetCall(t, server, playerID, raw)
		if status != "rejected" || code != "invalid_payload" {
			t.Fatalf("payload %s => %s code=%q", raw, status, code)
		}
	}
	if familyID, _, _ := playerFamilyRow(t, server, playerID); familyID != liveID {
		t.Fatalf("rejected call touched the family: players.family_id = %d, want %d", familyID, liveID)
	}
	if n := rowCount(t, server, `SELECT COUNT(*) FROM families WHERE id = ?`, liveID); n != 1 {
		t.Fatalf("rejected call touched the family row: %d", n)
	}
}

// 角色不存在要回 player_not_found，不能当成「没有家族」静默成功。
func TestGMFamilyResetRejectsUnknownPlayer(t *testing.T) {
	server, _, _ := gmFamilyResetTestServer(t)

	status, _, code, msg := gmResetCall(t, server, 987654, `{}`)
	if status != "rejected" || code != "player_not_found" {
		t.Fatalf("reset = %s code=%q msg=%q", status, code, msg)
	}
	if msg != "角色不存在" {
		t.Fatalf("msg = %q", msg)
	}
}

// 离线角色走 gmResolveSession 的临时会话，命令照样要能把库改干净。
func TestGMFamilyResetWorksOffline(t *testing.T) {
	server, ch, playerID := gmFamilyResetTestServer(t)

	const liveID = int64(88)
	joinFamily(t, server, liveID, "offline", playerID, playerID)
	ch.session.familyID = liveID
	// 摘掉在线频道，模拟角色下线。
	delete(server.conns, playerID)

	status, data, code, msg := gmResetCall(t, server, playerID, `{}`)
	if status != "completed" || code != "" || msg != "" {
		t.Fatalf("reset = %s code=%q msg=%q", status, code, msg)
	}
	if data["dissolved"] != true {
		t.Fatalf("dissolved = %v, want true", data["dissolved"])
	}
	if familyID, _, _ := playerFamilyRow(t, server, playerID); familyID != 0 {
		t.Fatalf("players.family_id = %d, want 0", familyID)
	}
	if n := rowCount(t, server, `SELECT COUNT(*) FROM families WHERE id = ?`, liveID); n != 0 {
		t.Fatalf("families rows = %d, want 0", n)
	}
}

// 命令必须注册在 player.* 注册表里，且命名与 GM API 路由约定一致。
func TestGMFamilyResetIsRegistered(t *testing.T) {
	if _, ok := gmActionRegistry["player.family_reset"]; !ok {
		t.Fatal("player.family_reset is not registered")
	}
}
