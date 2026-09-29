package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// players.family 是写权限，只挂在 operator 上。support / auditor / readonly 现在
// 都有 families.read（能看家族列表），不能被顺带当成能改归属。
func TestFamilyResetPermissionIsWriteOnly(t *testing.T) {
	cases := []struct {
		role string
		want bool
	}{
		{"superadmin", true},
		{"operator", true},
		{"subgm", false},
		{"support", false},
		{"auditor", false},
		{"readonly", false},
		{"config-editor", false},
	}
	for _, tc := range cases {
		session := &sessionData{Roles: []string{tc.role}}
		if got := hasPermission(session, "players.family"); got != tc.want {
			t.Fatalf("role %s players.family = %v, want %v", tc.role, got, tc.want)
		}
	}
}

// 家族列表的读权限不能顺带给出重置权限 —— 这正是 players.family 单独开门的原因。
// auditor 是唯一「有 families.read 但没有这个写权限」的角色；support 连读都没有。
func TestFamilyResetDoesNotFollowReadPermission(t *testing.T) {
	session := &sessionData{Roles: []string{"auditor"}}
	if !hasPermission(session, "families.read") {
		t.Fatal("auditor lost families.read; this test is stale")
	}
	if hasPermission(session, "players.family") {
		t.Fatal("auditor can read families yet also reset them")
	}
}

func TestFamilyResetRouteIsRegistered(t *testing.T) {
	route, ok := extraPlayerCommandRoutes["family-reset"]
	if !ok {
		t.Fatal("family-reset route not registered")
	}
	if route.action != "player.family_reset" {
		t.Fatalf("action = %q, want player.family_reset", route.action)
	}
	if route.permission != "players.family" {
		t.Fatalf("permission = %q, want players.family", route.permission)
	}
}

// 路由必须真的挂在 /api/v1/players/<id>/family-reset 上，而不是只写进了注册表：
// 权限不足时要在到达指令队列之前就被拒。
func TestFamilyResetRoutePermissionBoundary(t *testing.T) {
	app := &App{sessions: newMemorySessions()}
	for _, tc := range []struct {
		role string
		code string
	}{
		{"support", "forbidden"},
		{"auditor", "forbidden"},
		{"readonly", "forbidden"},
		// subgm 是受限子账号，没给这个权限：它会解散家族，不该下放。
		{"subgm", "forbidden"},
		{"operator", "csrf_failed"},
		{"superadmin", "csrf_failed"},
	} {
		session := &sessionData{ID: "family-" + tc.role, Roles: []string{tc.role}, CSRF: "csrf", ExpiresAt: time.Now().Add(time.Hour).Unix()}
		if err := app.sessions.Put(context.Background(), session); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/players/1/family-reset", strings.NewReader(`{"payload":{"mode":"detach"}}`))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
		rr := httptest.NewRecorder()
		app.ServeHTTP(rr, req)
		var body struct {
			Error *apiError `json:"error"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s decode: %v body=%s", tc.role, err, rr.Body.String())
		}
		if body.Error == nil || body.Error.Code != tc.code {
			t.Fatalf("%s error=%v want %s status=%d body=%s", tc.role, body.Error, tc.code, rr.Code, rr.Body.String())
		}
	}
}

// 没注册的路由会掉进「角色资源不存在」分支。注册之后不能再落回那里 ——
// 说明 main.go 的 extraPlayerCommandRoutes 查找确实生效了。
func TestFamilyResetRouteBeatsSubresourceFallback(t *testing.T) {
	app := &App{sessions: newMemorySessions()}
	session := &sessionData{ID: "family-nf", Roles: []string{"operator"}, CSRF: "csrf", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	if err := app.sessions.Put(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/players/1/family-reset", strings.NewReader(`{"payload":{"mode":"detach"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
	rr := httptest.NewRecorder()
	app.ServeHTTP(rr, req)
	if strings.Contains(rr.Body.String(), "角色资源不存在") {
		t.Fatalf("family-reset fell through to the subresource handler: %s", rr.Body.String())
	}
}
