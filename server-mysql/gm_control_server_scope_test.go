package main

import (
	"testing"

	json "github.com/goccy/go-json"
)

func TestGMNormalizeTargetType(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"", gmTargetTypePlayer},
		{"   ", gmTargetTypePlayer},
		{"player", gmTargetTypePlayer},
		{" player ", gmTargetTypePlayer},
		{"server", gmTargetTypeServer},
		{" server\n", gmTargetTypeServer},
		// 未知作用域不猜测、原样保留，由单角色分支的 TargetPlayerID 解析挡下。
		{"guild", "guild"},
	} {
		if got := gmNormalizeTargetType(tc.in); got != tc.want {
			t.Fatalf("gmNormalizeTargetType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// 发布顺序是「先游戏服、后 GM API」，所以游戏服先上线的窗口里收到的 envelope
// 一定没有 targetType 字段。这里用真实报文文本锁住缺省行为：老报文必须仍然被
// 当成单角色命令，新报文必须识别成全服作用域。
func TestGMCommandJSONTargetTypeDefaultsToPlayer(t *testing.T) {
	var legacy gmCommand
	if err := json.Unmarshal([]byte(`{"requestId":"r1","action":"player.kick","targetPlayerId":"7","expiresAt":1}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if got := gmNormalizeTargetType(legacy.TargetType); got != gmTargetTypePlayer {
		t.Fatalf("老 envelope 的作用域 = %q, want %q", got, gmTargetTypePlayer)
	}

	var scoped gmCommand
	if err := json.Unmarshal([]byte(`{"requestId":"r2","action":"server.announce","targetType":"server","targetPlayerId":"","expiresAt":1}`), &scoped); err != nil {
		t.Fatal(err)
	}
	if got := gmNormalizeTargetType(scoped.TargetType); got != gmTargetTypeServer {
		t.Fatalf("新 envelope 的作用域 = %q, want %q", got, gmTargetTypeServer)
	}
}

// 全服命令没有角色 ID。分流必须发生在解析 TargetPlayerID 之前 —— 否则一条合法的
// 全服命令会拿到 invalid_target（「角色 ID 无效」），把「作用域不对」误报成
// 「目标无效」，运营看到的提示完全指错方向。
func TestGMServerScopeSkipsPlayerIDValidation(t *testing.T) {
	result := executeGMCommand(nil, gmCommand{RequestID: "r", Action: "no.such.server.action", TargetType: gmTargetTypeServer})
	if result.ErrorCode != "unknown_action" {
		t.Fatalf("全服命令未注册动作的 errorCode = %q, want unknown_action（说明被 invalid_target 提前挡下了）", result.ErrorCode)
	}
	if result.Status != "rejected" {
		t.Fatalf("未注册动作的 status = %q, want rejected", result.Status)
	}
}

func TestGMServerScopeDispatchesRegisteredAction(t *testing.T) {
	const action = "test.server_scope_probe"
	registerGMServerAction(action, func(_ *Server, payload json.RawMessage) (string, map[string]any, string, string) {
		return "completed", map[string]any{"echo": string(payload)}, "", ""
	})
	t.Cleanup(func() { delete(gmServerActionRegistry, action) })

	result := executeGMCommand(nil, gmCommand{RequestID: "r", Action: action, TargetType: gmTargetTypeServer, Payload: json.RawMessage(`{"lines":["a"]}`)})
	if result.Status != "completed" {
		t.Fatalf("全服注册动作的 status = %q, want completed", result.Status)
	}
	if got, _ := result.Data["echo"].(string); got != `{"lines":["a"]}` {
		t.Fatalf("全服动作拿到的 payload = %q", got)
	}
}

// 两张注册表互不串门：全服作用域绝不能命中单角色动作（那会把 0 当成 role ID），
// 单角色作用域也不能命中全服动作。
func TestGMRegistriesDoNotCrossScope(t *testing.T) {
	const playerAction = "test.player_scope_probe"
	registerGMAction(playerAction, func(*Server, int64, json.RawMessage) (string, map[string]any, string, string) {
		return "completed", map[string]any{"wrong_registry": true}, "", ""
	})
	t.Cleanup(func() { delete(gmActionRegistry, playerAction) })

	result := executeGMCommand(nil, gmCommand{RequestID: "r", Action: playerAction, TargetType: gmTargetTypeServer})
	if result.ErrorCode != "unknown_action" {
		t.Fatalf("全服作用域命中了单角色注册表：status=%q errorCode=%q data=%v", result.Status, result.ErrorCode, result.Data)
	}

	const serverAction = "test.server_scope_probe_2"
	registerGMServerAction(serverAction, func(*Server, json.RawMessage) (string, map[string]any, string, string) {
		return "completed", map[string]any{"wrong_registry": true}, "", ""
	})
	t.Cleanup(func() { delete(gmServerActionRegistry, serverAction) })

	result = executeGMCommand(nil, gmCommand{RequestID: "r", Action: serverAction, TargetType: gmTargetTypePlayer, TargetPlayerID: "7"})
	if result.ErrorCode != "unknown_action" {
		t.Fatalf("单角色作用域命中了全服注册表：status=%q errorCode=%q data=%v", result.Status, result.ErrorCode, result.Data)
	}
}

// 单角色分支的校验一点没放松：空 targetType（老 envelope）、未知作用域、以及
// 非法角色 ID 都必须仍然是 invalid_target。
func TestGMPlayerScopeStillRequiresPositivePlayerID(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command gmCommand
	}{
		{"空作用域", gmCommand{Action: "player.kick"}},
		{"显式 player", gmCommand{Action: "player.kick", TargetType: gmTargetTypePlayer}},
		{"未知作用域", gmCommand{Action: "player.kick", TargetType: "guild"}},
		{"角色 ID 为零", gmCommand{Action: "player.kick", TargetPlayerID: "0"}},
		{"角色 ID 为负", gmCommand{Action: "player.kick", TargetPlayerID: "-3"}},
		{"角色 ID 非数字", gmCommand{Action: "player.kick", TargetPlayerID: "abc"}},
		{"角色 ID 为空白", gmCommand{Action: "player.kick", TargetPlayerID: "   "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.command.RequestID = "r"
			result := executeGMCommand(nil, tc.command)
			if result.ErrorCode != "invalid_target" {
				t.Fatalf("errorCode = %q, want invalid_target", result.ErrorCode)
			}
		})
	}
}
