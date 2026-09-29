package main

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// 命令作用域（envelope.targetType）穿过真实 Redis Stream + 真实游戏服消费端的
// 回归。单测只能覆盖 executeGMCommand 的分发，覆盖不到「字段有没有活着到游戏服」
// 这一段，所以这里直接把 envelope 写进流里，再读回结果。
//
// 用一个确定没有注册的动作来探测：全服作用域应当回 unknown_action，单角色作用域
// 在角色 ID 为空时应当回 invalid_target。这两条恰好把作用域分流点夹住 —— 只要
// 游戏服没读到 targetType，全服那条就会变成 invalid_target 而失败。
//
// 不建角色、不写玩家数据，只用 Redis；opt-in 避免影响常规 go test ./...。
func TestGMServerScopeLiveRedis(t *testing.T) {
	if os.Getenv("MHQ_TEST_GM_SERVER_SCOPE_REDIS") != "1" {
		t.Skip("requires local Redis and a running game server consuming " + gmCommandStream)
	}
	client := redis.NewClient(&redis.Options{Addr: envOrDefault("MHQ_REDIS_ADDR", "127.0.0.1:6379"), Password: os.Getenv("MHQ_REDIS_PASSWORD")})
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("Redis 不可用: %v", err)
	}
	// 确认游戏服在消费：组不存在说明控制面没起来，此时失败会把「没跑游戏服」
	// 误报成「作用域分流坏了」。
	groups, err := client.XInfoGroups(ctx, gmCommandStream).Result()
	if err != nil {
		t.Fatalf("读取命令流消费组失败（游戏服没在消费 %s？）: %v", gmCommandStream, err)
	}
	found := false
	for _, group := range groups {
		if group.Name == "game" {
			found = true
		}
	}
	if !found {
		t.Fatalf("命令流 %s 上没有 game 消费组，先启动本地游戏服", gmCommandStream)
	}

	probe := func(name, targetType, targetPlayerID, action string) map[string]any {
		t.Helper()
		requestID := "scope-probe-" + requestID()[:16]
		envelope := `{"requestId":"` + requestID + `","action":"` + action + `","targetType":"` + targetType +
			`","targetPlayerId":"` + targetPlayerID + `","operatorId":"0","payload":{},"expiresAt":` +
			strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10) + `}`
		if err := client.XAdd(ctx, &redis.XAddArgs{Stream: gmCommandStream, Values: map[string]any{"payload": envelope, "reason": ""}}).Err(); err != nil {
			t.Fatalf("%s: 写入命令流失败: %v", name, err)
		}
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			raw, err := client.Get(ctx, "mhq:gm:results:"+requestID).Result()
			if err == nil && raw != "" {
				var result map[string]any
				if err := json.Unmarshal([]byte(raw), &result); err != nil {
					t.Fatalf("%s: 结果不是合法 JSON: %v", name, err)
				}
				// 游戏服只认已确认的结果，但同一 requestId 复用 TTL 内的键；
				// 结果里带 requestId 字段，据此确认读到的是本次探测。
				if got, _ := result["requestId"].(string); got != requestID {
					t.Fatalf("%s: 结果 requestId = %q, want %q", name, got, requestID)
				}
				return result
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("%s: 15s 内没有拿到命令结果（游戏服没消费？）", name)
		return nil
	}

	const unknownAction = "server.no_such_action_probe"

	scoped := probe("全服作用域", gmTargetTypeServer, "", unknownAction)
	if got, _ := scoped["errorCode"].(string); got != "unknown_action" {
		t.Fatalf("全服作用域的 errorCode = %q, want unknown_action（说明游戏服没读到 targetType，落回了单角色分支）", got)
	}
	if got, _ := scoped["status"].(string); got != "rejected" {
		t.Fatalf("全服作用域的 status = %q, want rejected", got)
	}

	// 同一动作放到单角色作用域、且不带角色 ID：必须仍然走角色校验。
	player := probe("单角色作用域", gmTargetTypePlayer, "", unknownAction)
	if got, _ := player["errorCode"].(string); got != "invalid_target" {
		t.Fatalf("单角色作用域空角色 ID 的 errorCode = %q, want invalid_target", got)
	}

	// 老 GM API 不发 targetType。缺省必须等价于单角色，而不是被当成全服。
	legacy := probe("缺省作用域", "", "", unknownAction)
	if got, _ := legacy["errorCode"].(string); got != "invalid_target" {
		t.Fatalf("缺省 targetType 的 errorCode = %q, want invalid_target（缺省必须等价 player）", got)
	}
}
