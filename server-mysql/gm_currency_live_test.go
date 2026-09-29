package main

import (
	"context"
	"database/sql"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/redis/go-redis/v9"
	"mhqserver/internal/mysqlschema"
)

// Opt-in live regression for the 金币 unit of player.currency_adjust.
//
// 单元测试只覆盖换算函数本身；真正的链路是 GM 后台 → Redis Stream
// mhq:gm:commands → 运行中的游戏服 gmAdjustCurrency → MySQL，中间任何一环
// 断掉都只有连真实服务端才看得出来。
//
// 夹具角色的余额刻意取非整金（11106365357 铜 = 1110636 金 53 银 57 铜），
// 用来证明换算只放大入参、不吞掉余额里的铜位余数：回归前这里会变成
// 1110636 金（余数被截断丢掉）。
//
// 运行：
//
//	MHQ_TEST_GM_CURRENCY_LIVE=1 go test -run TestGMCurrencyGoldUnitLive -v .
//
// 需要本机 MySQL、Redis 和已启动的游戏服（127.0.0.1:7756）。
func TestGMCurrencyGoldUnitLive(t *testing.T) {
	if os.Getenv("MHQ_TEST_GM_CURRENCY_LIVE") != "1" {
		t.Skip("requires local MySQL, Redis and a running game server on 7756")
	}
	const (
		startCoin = int64(11106365357) // 1110636 金 53 银 57 铜，故意不是整金
		goldDelta = "1000"
		wantCoin  = startCoin + 1000*10000
	)

	db, err := sql.Open("mysql", mysqlschema.DefaultDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	redisAddr := strings.TrimSpace(os.Getenv("MHQ_REDIS_ADDR"))
	if redisAddr == "" {
		redisAddr = "127.0.0.1:6379"
	}
	client := redis.NewClient(&redis.Options{Addr: redisAddr, Password: os.Getenv("MHQ_REDIS_PASSWORD")})
	defer client.Close()

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	account := "gmcoin" + suffix
	accountRes, err := db.Exec(`INSERT INTO accounts (account, password, create_time) VALUES (?, ?, ?)`,
		account, "123456", time.Now().Unix())
	if err != nil {
		t.Fatalf("创建夹具账号失败: %v", err)
	}
	accountID, err := accountRes.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	playerRes, err := db.Exec(`INSERT INTO players (account_id, name, job_id, skin_id, store_pages, coin)
		VALUES (?, ?, 1, 1, 2, ?)`, accountID, "GM金币校验"+suffix, startCoin)
	if err != nil {
		db.Exec(`DELETE FROM accounts WHERE id = ?`, accountID)
		t.Fatalf("创建夹具角色失败: %v", err)
	}
	playerID, err := playerRes.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.Exec(`DELETE FROM players WHERE id = ?`, playerID); err != nil {
			t.Logf("夹具角色清理失败（保留以便排查）: %v", err)
			return
		}
		if _, err := db.Exec(`DELETE FROM accounts WHERE id = ?`, accountID); err != nil {
			t.Logf("夹具账号清理失败（保留以便排查）: %v", err)
		}
	}()

	requestID := "gmcoin-" + suffix
	envelope, err := json.Marshal(map[string]any{
		"requestId":      requestID,
		"action":         "player.currency_adjust",
		"targetPlayerId": strconv.FormatInt(playerID, 10),
		"operatorId":     "0",
		"payload":        map[string]any{"currency": "coin", "delta": goldDelta},
		"expiresAt":      time.Now().Add(2 * time.Minute).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := client.XAdd(ctx, &redis.XAddArgs{Stream: gmCommandStream,
		Values: map[string]any{"payload": string(envelope), "reason": ""}}).Err(); err != nil {
		t.Fatalf("GM 命令入队失败: %v", err)
	}

	result := waitGMLiveResult(t, ctx, client, requestID, 30*time.Second)
	if status, _ := result["status"].(string); status != "completed" {
		t.Fatalf("GM 命令未完成: %v", result)
	}
	data, _ := result["data"].(map[string]any)
	if data == nil {
		t.Fatalf("GM 命令没有返回 data: %v", result)
	}
	// 回显一律按金币口径：填多少金币，before/after/delta 就该是多少金币。
	for key, want := range map[string]string{
		"delta":      goldDelta,
		"before":     "1110636",
		"after":      "1111636",
		"deltaCoin":  "10000000",
		"beforeCoin": strconv.FormatInt(startCoin, 10),
		"afterCoin":  strconv.FormatInt(wantCoin, 10),
	} {
		if got, _ := data[key].(string); got != want {
			t.Errorf("data.%s = %q, want %q（完整返回 %v）", key, got, want, result)
		}
	}

	var stored int64
	if err := db.QueryRow(`SELECT coin FROM players WHERE id = ?`, playerID).Scan(&stored); err != nil {
		t.Fatalf("回读角色余额失败: %v", err)
	}
	if stored != wantCoin {
		t.Fatalf("players.coin = %d, want %d（差 %d，换算前后差了 10000 倍说明仍在按铜币入账）",
			stored, wantCoin, stored-wantCoin)
	}
	t.Logf("金币入账正确：%d 铜 → %d 铜（+%s 金币），余数 5357 保留", startCoin, stored, goldDelta)
}

// waitGMLiveResult 轮询 mhq:gm:results:<requestId>，与 GM API 自己的
// waitCommandResult 读同一个键。
func waitGMLiveResult(t *testing.T, ctx context.Context, client *redis.Client, requestID string, timeout time.Duration) map[string]any {
	t.Helper()
	key := "mhq:gm:results:" + requestID
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		value, err := client.Get(ctx, key).Result()
		if err == nil && value != "" {
			var result map[string]any
			if err := json.Unmarshal([]byte(value), &result); err != nil {
				t.Fatalf("解析 GM 结果失败: %v", err)
			}
			return result
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("GM 命令 %s 在 %s 内没有返回结果：确认本地游戏服在跑、日志里出现过 "+
		"“GM control consumer ready”", requestID, timeout)
	return nil
}
