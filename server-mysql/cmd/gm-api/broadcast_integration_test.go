package main

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"mhqserver/internal/mysqlschema"
)

// 全服公告 + 全服邮件的真实链路测试：GM HTTP → gm_command_records → Redis Stream →
// 游戏服 → 真实 TCP 客户端。用临时 HTTP 测试服务器和内存会话（审计 admin_id=0），
// 不读用户浏览器会话，也不创建任何真实管理员；角色走协议新建、测完走原生删除角色
// 协议删掉，投递出去的全服邮件按 mail_id 删干净。
func TestGMBroadcastLiveTCP(t *testing.T) {
	if os.Getenv("MHQ_TEST_GM_BROADCAST_TCP") != "1" {
		t.Skip("requires local MySQL, Redis and game TCP 7756")
	}
	db, err := sql.Open("mysql", mysqlschema.DefaultDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	client := redis.NewClient(&redis.Options{Addr: envOrDefault("MHQ_REDIS_ADDR", "127.0.0.1:6379"), Password: os.Getenv("MHQ_REDIS_PASSWORD")})
	defer client.Close()
	app := &App{db: db, redis: client, sessions: newMemorySessions()}
	operator := &sessionData{ID: requestID(), Username: "gm-broadcast-test", Roles: []string{"operator"}, CSRF: requestID(), ExpiresAt: time.Now().Add(time.Hour).Unix()}
	// support 能执行单角色命令，但没有 server.broadcast。两个会话同时下发，才能证明
	// 被拒的原因是权限而不是别的：同一条请求换个角色就成功了。
	support := &sessionData{ID: requestID(), Username: "gm-broadcast-noperm", Roles: []string{"support"}, CSRF: requestID(), ExpiresAt: time.Now().Add(time.Hour).Unix()}
	// 只授公告权的临时角色：拿它证明全服邮件是被 server.mail_all 单独把守的，而不是
	// 「有任何一个全服权限就行」。授予走 grantRolePermission，与线上运营授权同一条路。
	announceOnlyRole := "live-announce-only-" + requestID()[:8]
	grantRolePermission(announceOnlyRole, permissionServerBroadcast)
	announceOnly := &sessionData{ID: requestID(), Username: "gm-broadcast-announceonly", Roles: []string{announceOnlyRole}, CSRF: requestID(), ExpiresAt: time.Now().Add(time.Hour).Unix()}
	for _, session := range []*sessionData{operator, support, announceOnly} {
		if err := app.sessions.Put(context.Background(), session); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(app)
	defer server.Close()
	// 两个账号各自建一个角色：一个账号只能同时进一个角色，两个角色在线才能区分
	// 「真的广播了」和「恰好发给了某一个」。
	accounts := []string{"gmbcasta" + requestID()[:12], "gmbcastb" + requestID()[:12]}
	defer func() {
		// Python 脚本先用自己的协议删掉角色。只有删干净了才移除账号，失败时保留现场。
		for _, account := range accounts {
			if _, err := db.Exec(`DELETE FROM accounts WHERE account = ? AND NOT EXISTS (SELECT 1 FROM players WHERE players.account_id = accounts.id)`, account); err != nil {
				t.Logf("test account cleanup %s: %v", account, err)
			}
		}
	}()
	// 受众预览要对得上真实角色数。本机测试期间没有其他人登录，取一次基线即可，
	// 脚本自己会再建两个角色（+2）。
	var baseline int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM players`).Scan(&baseline); err != nil {
		t.Fatal(err)
	}
	// 全服邮件的收件人是库里每一个角色，测试角色只是其中两个。脚本每投递一封就把
	// mail_id 打到 stdout，这里按 id 精确删回去：本机库还有上百个别的角色，不清理
	// 就是往他们邮箱里塞测试垃圾。删 player_mails 会按外键级联带走附件行。
	//
	// 注册在账号清理之前 → 它先执行：先把邮件行删掉，再删那两个测试角色。
	var mailIDs []int64
	defer func() {
		for _, id := range mailIDs {
			if _, err := db.Exec(`DELETE FROM player_mails WHERE mail_id = ?`, id); err != nil {
				t.Logf("broadcast mail cleanup %d: %v", id, err)
			}
		}
		if len(mailIDs) > 0 {
			t.Logf("cleaned %d broadcast mail(s): %v", len(mailIDs), mailIDs)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python", "../../test/verify_gm_broadcast.py")
	cmd.Env = append(os.Environ(),
		"GM_TEST_API_BASE="+server.URL,
		"GM_TEST_SESSION="+operator.ID, "GM_TEST_CSRF="+operator.CSRF,
		"GM_TEST_NOPERM_SESSION="+support.ID, "GM_TEST_NOPERM_CSRF="+support.CSRF,
		"GM_TEST_ANNOUNCEONLY_SESSION="+announceOnly.ID, "GM_TEST_ANNOUNCEONLY_CSRF="+announceOnly.CSRF,
		"GM_TEST_ACCOUNT_A="+accounts[0], "GM_TEST_ACCOUNT_B="+accounts[1],
		"GM_TEST_PLAYERS_BEFORE="+strconv.FormatInt(baseline, 10))
	output, err := cmd.CombinedOutput()
	mailIDs = parseBroadcastMailIDs(string(output))
	t.Log(string(output))
	if err != nil {
		t.Fatalf("GM broadcast / TCP regression: %v", err)
	}
	if len(mailIDs) == 0 {
		// 一个 id 都没解析到，说明脚本没有走到任何一次成功投递 —— 上面那句 err 却没报错，
		// 那就是断言被削弱了。这里明确失败，别让清理逻辑的静默掩盖测试失去覆盖。
		t.Fatal("没有解析到任何 BROADCAST_MAIL_ID，全服邮件断言可能没有真正执行")
	}
}

// parseBroadcastMailIDs 从脚本输出里收集这一轮投递出去的全服邮件 id。
// 逐行独立解析：脚本崩在中间时，已经打出来的那些仍然要能被清理。
func parseBroadcastMailIDs(output string) []int64 {
	var ids []int64
	for _, line := range strings.Split(output, "\n") {
		raw, ok := strings.CutPrefix(strings.TrimSpace(line), "BROADCAST_MAIL_ID=")
		if !ok {
			continue
		}
		id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}
