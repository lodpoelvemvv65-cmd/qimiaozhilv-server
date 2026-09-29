package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestGMTargetTypeNormalizeAndKnown(t *testing.T) {
	for _, tc := range []struct {
		in    string
		want  string
		known bool
	}{
		{"", gmTargetTypePlayer, true},
		{"  ", gmTargetTypePlayer, true},
		{"player", gmTargetTypePlayer, true},
		{" server ", gmTargetTypeServer, true},
		// 未知作用域不能被静默当成 player，否则一个全服动作会拿着空角色 ID 去执行。
		{"guild", "guild", false},
	} {
		if got := gmNormalizeTargetType(tc.in); got != tc.want {
			t.Fatalf("gmNormalizeTargetType(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if got := gmTargetTypeKnown(gmNormalizeTargetType(tc.in)); got != tc.known {
			t.Fatalf("gmTargetTypeKnown(%q) = %v, want %v", tc.in, got, tc.known)
		}
	}
}

// 过期判定只发生在命令被消费的那一刻，所以窗口要覆盖「在队列里能排多久」。
// 全服动作要遍历全部角色，比单角色更容易积压，允许的排队时间放宽到 5 分钟。
func TestGMCommandExpiryByScope(t *testing.T) {
	if got := gmCommandExpiry(gmTargetTypeServer); got != 5*time.Minute {
		t.Fatalf("全服作用域有效期 = %v, want 5m", got)
	}
	for _, scope := range []string{gmTargetTypePlayer, ""} {
		if got := gmCommandExpiry(scope); got != 60*time.Second {
			t.Fatalf("作用域 %q 的有效期 = %v, want 60s", scope, got)
		}
	}
}

// 单角色命令的兼容性要求不在「报文逐字节一致」，而在「老字段照旧可用」：
// 老游戏服只读 targetPlayerId，多余的 targetType 会被 json.Unmarshal 忽略。
// 反过来（老 GM API 不带 targetType）由游戏服的 gmNormalizeTargetType 兜底。
// 所以这里锁的是 targetPlayerId 仍然是角色 ID 的十进制字符串。
func TestGMCommandEnvelopeKeepsPlayerTargetField(t *testing.T) {
	raw, err := json.Marshal(gmCommandEnvelope{RequestID: "r1", Action: "player.kick", TargetPlayerID: "7",
		TargetType: gmTargetTypePlayer, OperatorID: "1", Payload: json.RawMessage(`{}`), ExpiresAt: 99})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"targetPlayerId":"7"`) {
		t.Fatalf("单角色 envelope 缺少 targetPlayerId：%s", raw)
	}
	if !strings.Contains(string(raw), `"targetType":"player"`) {
		t.Fatalf("单角色 envelope 缺少显式作用域：%s", raw)
	}
	// targetType 只在全服命令里才会缺省（见下一组用例），单角色命令必须显式带上，
	// 否则审计与排障时无法从报文本身判断作用域。
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["targetPlayerId"] != "7" {
		t.Fatalf("targetPlayerId 解码 = %v, want \"7\"", decoded["targetPlayerId"])
	}
}

// 全服命令的 envelope 要带上作用域，且 targetPlayerId 为空（不是 0）——
// 游戏服据此在解析角色 ID 之前就把命令交给全服注册表。
func TestGMCommandEnvelopeCarriesServerScope(t *testing.T) {
	raw, err := json.Marshal(gmCommandEnvelope{RequestID: "r2", Action: "server.announce", TargetType: gmTargetTypeServer,
		TargetPlayerID: "", OperatorID: "1", Payload: json.RawMessage(`{"lines":["停机维护"]}`), ExpiresAt: 99})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"targetType":"server"`) {
		t.Fatalf("全服 envelope 缺少 targetType：%s", raw)
	}
	if !strings.Contains(string(raw), `"targetPlayerId":""`) {
		t.Fatalf("全服 envelope 的 targetPlayerId 应为空串：%s", raw)
	}
}

func TestCommandErrorCodeForInvalidTargetType(t *testing.T) {
	if got := commandErrorCode(errInvalidTargetType); got != "invalid_target_type" {
		t.Fatalf("commandErrorCode(errInvalidTargetType) = %q, want invalid_target_type", got)
	}
}
