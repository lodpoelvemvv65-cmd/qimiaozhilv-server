package main

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"mhqserver/internal/mysqlschema"
)

// GM 镶嵌宝石（player.equip_gem）全链路实测：GM HTTP -> Redis -> 游戏服 -> MySQL，
// 再回到游戏 TCP 看客户端可见包。与 TestGMManualEquipLiveTCP 一样用临时 HTTP 测试服务
// 和内存会话（审计 admin_id=0），不碰任何真实管理员，只建一个协议实测角色。
//
// 最关键的两段：
//   - 「只碰宝石」：before/after 里除 gems 之外的字段逐一相等；
//   - 「星级 5 但 0 条随机属性」的新装备能直接镶嵌——手工装备那条 action 会因为这个
//     状态拒绝，宝石这条必须不受影响，这就是两者分开的意义。
//
// ⚠ Go 的测试缓存不跟踪被测试脚本的内容，改完 verify_gm_equip_gem.py 后必须加
// -count=1，否则会拿到上一次 PASS 的缓存结果：
//
//	MHQ_TEST_GM_GEM_TCP=1 go test ./cmd/gm-api -run TestGMEquipGemLiveTCP -v -count=1
func TestGMEquipGemLiveTCP(t *testing.T) {
	if os.Getenv("MHQ_TEST_GM_GEM_TCP") != "1" {
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
	session := &sessionData{ID: requestID(), Username: "gm-equip-gem-test", Roles: []string{"operator"}, CSRF: requestID(), ExpiresAt: time.Now().Add(time.Hour).Unix()}
	if err := app.sessions.Put(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app)
	defer server.Close()
	account := "gmgem" + requestID()[:16]
	defer func() {
		// Python 侧先用自己的协议删掉角色；删干净了才回收账号，失败则保留现场。
		_, err := db.Exec(`DELETE FROM accounts WHERE account = ? AND NOT EXISTS (SELECT 1 FROM players WHERE players.account_id = accounts.id)`, account)
		if err != nil {
			t.Logf("test account cleanup: %v", err)
		}
	}()
	// 一次完整重登要等旧会话被顶下线再加一次 EnterGame。
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python", "../../test/verify_gm_equip_gem.py")
	cmd.Env = append(os.Environ(), "GM_TEST_API_BASE="+server.URL, "GM_TEST_SESSION="+session.ID, "GM_TEST_CSRF="+session.CSRF, "GM_TEST_ACCOUNT="+account)
	output, err := cmd.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatalf("GM equip gem TCP regression: %v", err)
	}
}
