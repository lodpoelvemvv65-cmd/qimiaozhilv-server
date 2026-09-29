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

// 全服公告影响每一个在线玩家，权限只能是显式授予的角色。subgm（子管理员）在
// 单角色命令上有很宽的权限，但绝不该有全服权限 —— 他们只能操作自己绑定的角色。
//
// 两个权限一起过这张表：角色边界的判据是「谁能碰全服」，公告和全服邮件在这件事上
// 没有区别，分开断言只会让日后再加一个 server.* 权限时漏掉一处。
func TestBroadcastPermissionBoundary(t *testing.T) {
	for _, permission := range []string{permissionServerBroadcast, permissionServerMailAll} {
		for _, tc := range []struct {
			role string
			want bool
		}{
			{"superadmin", true},
			{"operator", true},
			{"support", false},
			{"subgm", false},
			{"auditor", false},
			{"readonly", false},
			{"config-editor", false},
		} {
			session := &sessionData{Roles: []string{tc.role}}
			if got := hasPermission(session, permission); got != tc.want {
				t.Fatalf("role %s %s = %v, want %v", tc.role, permission, got, tc.want)
			}
		}
	}
}

// 路由边界：无权限的角色必须在 handler 内被挡下（这两个路径没有列在 main.go 的
// 权限分发表里，漏掉校验会直接放行）。
func TestBroadcastRouteRejectsRolesWithoutPermission(t *testing.T) {
	app := &App{sessions: newMemorySessions()}
	for _, route := range []struct{ path, method string }{
		{"/api/v1/broadcast/audience", http.MethodGet},
		{"/api/v1/broadcast/announce", http.MethodPost},
		{"/api/v1/broadcast/mail", http.MethodPost},
	} {
		path, method := route.path, route.method
		for _, role := range []string{"support", "subgm", "auditor", "readonly", "config-editor"} {
			session := &sessionData{ID: role + "-" + strings.TrimPrefix(path, "/api/v1/broadcast/"), Roles: []string{role}, CSRF: "csrf", ExpiresAt: time.Now().Add(time.Hour).Unix()}
			if err := app.sessions.Put(context.Background(), session); err != nil {
				t.Fatal(err)
			}
			payload := `{"payload":{"lines":["测试"]}}`
			if strings.HasSuffix(path, "mail") {
				payload = `{"payload":{"title":"测试","content":"测试"}}`
			}
			req := httptest.NewRequest(method, path, strings.NewReader(payload))
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
			rr := httptest.NewRecorder()
			app.ServeHTTP(rr, req)
			var body struct {
				Error *apiError `json:"error"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatalf("%s %s decode: %v body=%s", role, path, err, rr.Body.String())
			}
			if rr.Code != http.StatusForbidden || body.Error == nil || body.Error.Code != "forbidden" {
				t.Fatalf("%s %s status=%d error=%v body=%s", role, path, rr.Code, body.Error, rr.Body.String())
			}
		}
	}
}

// 有权限但缺 CSRF / 幂等键时的边界。全服公告不可撤回，这三种残缺请求必须在
// 落到命令通道之前就被拒绝。
func TestBroadcastAnnounceRequestValidation(t *testing.T) {
	app := &App{sessions: newMemorySessions()}
	for _, tc := range []struct {
		name string
		// 会话 ID 会进 cookie，必须是 ASCII：http 包会直接丢掉含非 ASCII 字节的
		// cookie，导致请求变成「未登录」而不是测出真正想测的那个错误码。
		sessionID string
		method    string
		csrf      bool
		key       string
		wantCode  string
	}{
		{"方法不对", "wrong-method", http.MethodGet, true, strings.Repeat("k", 16), "method_not_allowed"},
		{"缺 CSRF", "no-csrf", http.MethodPost, false, strings.Repeat("k", 16), "csrf_failed"},
		{"缺幂等键", "no-key", http.MethodPost, true, "", "idempotency_required"},
		{"幂等键过短", "short-key", http.MethodPost, true, "short", "idempotency_required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const csrf = "csrf-token-value"
			session := &sessionData{ID: tc.sessionID, Roles: []string{"operator"}, CSRF: csrf, ExpiresAt: time.Now().Add(time.Hour).Unix()}
			if err := app.sessions.Put(context.Background(), session); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(tc.method, "/api/v1/broadcast/announce", strings.NewReader(`{"payload":{"lines":["停机维护"]}}`))
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
			if tc.csrf {
				req.Header.Set("X-CSRF-Token", csrf)
				req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
			}
			if tc.key != "" {
				req.Header.Set("Idempotency-Key", tc.key)
			}
			rr := httptest.NewRecorder()
			app.ServeHTTP(rr, req)
			var body struct {
				Error *apiError `json:"error"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v body=%s", err, rr.Body.String())
			}
			if body.Error == nil || body.Error.Code != tc.wantCode {
				t.Fatalf("error=%v want %s status=%d body=%s", body.Error, tc.wantCode, rr.Code, rr.Body.String())
			}
		})
	}
}

// 校验全过之后会走到 submitCommand；Redis 不可用时必须是 503，绝不能把命令
// 当成已下发（否则运营以为公告发了，实际谁也没收到）。
func TestBroadcastAnnounceWithoutRedisIsServiceUnavailable(t *testing.T) {
	app := &App{sessions: newMemorySessions()}
	const csrf = "csrf-token-value"
	session := &sessionData{ID: "no-redis", Roles: []string{"operator"}, CSRF: csrf, ExpiresAt: time.Now().Add(time.Hour).Unix()}
	if err := app.sessions.Put(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/broadcast/announce", strings.NewReader(`{"payload":{"lines":["停机维护"]}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", strings.Repeat("k", 16))
	req.Header.Set("X-CSRF-Token", csrf)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
	rr := httptest.NewRecorder()
	app.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 body=%s", rr.Code, rr.Body.String())
	}
}

// 公告和全服邮件的影响面差得远：公告只进聊天频道，全服邮件往每个角色的邮件箱写
// 物品，发错了只能一个个角色去删。两者的权限必须能分开授予，否则「给某个角色开
// 公告权」会顺带把全服邮件也开出去。
//
// 判据用「不存在的权限码」把两个请求的失败点错开：过了权限这一关的会被 CSRF 拦下
// （csrf_failed），没过权限的直接就是 forbidden。只看 forbidden 是分不清「权限挡的」
// 和「别处挡的」的。
func TestBroadcastMailAndAnnouncePermissionsAreSeparate(t *testing.T) {
	for _, tc := range []struct {
		name       string
		grant      string
		wantDenied string
		wantPassed string
	}{
		{"只授公告权", permissionServerBroadcast, "/api/v1/broadcast/mail", "/api/v1/broadcast/announce"},
		{"只授全服邮件权", permissionServerMailAll, "/api/v1/broadcast/announce", "/api/v1/broadcast/mail"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			role := "test-role-" + tc.grant
			grantRolePermission(role, tc.grant)
			app := &App{sessions: newMemorySessions()}
			session := &sessionData{ID: "perm-split", Roles: []string{role}, CSRF: "csrf", ExpiresAt: time.Now().Add(time.Hour).Unix()}
			if err := app.sessions.Put(context.Background(), session); err != nil {
				t.Fatal(err)
			}
			send := func(path string) *apiError {
				// 故意不带 CSRF：请求能不能走到 CSRF 校验，就说明权限有没有放行。
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"payload":{"title":"标题","lines":["公告"]}}`))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Idempotency-Key", strings.Repeat("k", 16))
				req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
				rr := httptest.NewRecorder()
				app.ServeHTTP(rr, req)
				var body struct {
					Error *apiError `json:"error"`
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
					t.Fatalf("%s decode: %v body=%s", path, err, rr.Body.String())
				}
				if body.Error == nil {
					t.Fatalf("%s 没有返回错误体：%s", path, rr.Body.String())
				}
				return body.Error
			}
			if got := send(tc.wantDenied); got.Code != "forbidden" {
				t.Fatalf("%s 应当被权限挡下，实际 errorCode = %q", tc.wantDenied, got.Code)
			}
			if got := send(tc.wantPassed); got.Code != "csrf_failed" {
				t.Fatalf("%s 应当通过权限、被 CSRF 挡下，实际 errorCode = %q", tc.wantPassed, got.Code)
			}
		})
	}
}
