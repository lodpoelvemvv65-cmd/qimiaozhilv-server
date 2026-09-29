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

// 手工装备沿用 players.equip，与 equip-adjust 同一条权限线：
// 只有 superadmin / operator / subgm 能进，support 及以下直接 forbidden。
func TestEquipManualPermissionMatrix(t *testing.T) {
	for _, tc := range []struct {
		role string
		want bool
	}{
		{"superadmin", true}, {"operator", true}, {"subgm", true},
		{"support", false}, {"auditor", false}, {"readonly", false},
	} {
		session := &sessionData{Roles: []string{tc.role}}
		if got := hasPermission(session, "players.equip"); got != tc.want {
			t.Fatalf("role %s players.equip = %v, want %v", tc.role, got, tc.want)
		}
	}
}

// 指令路由：无 CSRF 头时 operator/subgm/superadmin 得 csrf_failed，support 得 forbidden。
func TestEquipManualCommandRoutePermissionBoundary(t *testing.T) {
	app := &App{sessions: newMemorySessions()}
	for _, tc := range []struct {
		role string
		code string
	}{
		{"support", "forbidden"}, {"subgm", "csrf_failed"}, {"operator", "csrf_failed"}, {"superadmin", "csrf_failed"},
	} {
		session := &sessionData{ID: "manual-" + tc.role, Roles: []string{tc.role}, CSRF: "csrf", ExpiresAt: time.Now().Add(time.Hour).Unix()}
		if err := app.sessions.Put(context.Background(), session); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/players/1/equip-manual",
			strings.NewReader(`{"payload":{"star":"3","randomAttrs":[101,102,103]}}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "equip-manual-key-1")
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

// 候选池接口是只读 GET：未登录 401、无权 403、方法不对 405。
func TestEquipManualOptionsAccessBoundary(t *testing.T) {
	app := &App{sessions: newMemorySessions()}
	for _, tc := range []struct {
		role, method string
		status       int
	}{
		{"", http.MethodGet, http.StatusUnauthorized},
		{"auditor", http.MethodGet, http.StatusForbidden},
		{"readonly", http.MethodGet, http.StatusForbidden},
		{"support", http.MethodPost, http.StatusMethodNotAllowed},
		{"operator", http.MethodDelete, http.StatusMethodNotAllowed},
	} {
		req := httptest.NewRequest(tc.method, "/api/v1/equip-manual/options", nil)
		if tc.role != "" {
			session := &sessionData{ID: "options-" + tc.role + tc.method, Roles: []string{tc.role}, ExpiresAt: time.Now().Add(time.Hour).Unix()}
			if err := app.sessions.Put(context.Background(), session); err != nil {
				t.Fatal(err)
			}
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
		}
		rr := httptest.NewRecorder()
		app.ServeHTTP(rr, req)
		if rr.Code != tc.status {
			t.Fatalf("%s %s: got %d want %d body=%s", tc.role, tc.method, rr.Code, tc.status, rr.Body.String())
		}
	}
}
