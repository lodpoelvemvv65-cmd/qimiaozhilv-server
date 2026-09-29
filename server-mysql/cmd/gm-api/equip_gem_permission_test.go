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

// 宝石镶嵌（equip-gem）和手工装备、调整装备共用 players.equip 这一条权限线。
// 这里单独再断言一次，是因为它是一条**独立**的指令路由：漏注册路由时
// registerPlayerCommandRoute 不会报错，只会让这个 path 永远 404，权限矩阵
// 反而会通过（404 不带 error.code 时用例会先失败，但错因会指向路由而不是权限）。
func TestEquipGemCommandRoutePermissionBoundary(t *testing.T) {
	app := &App{sessions: newMemorySessions()}
	for _, tc := range []struct {
		role string
		code string
	}{
		{"support", "forbidden"}, {"subgm", "csrf_failed"}, {"operator", "csrf_failed"}, {"superadmin", "csrf_failed"},
	} {
		session := &sessionData{ID: "gem-" + tc.role, Roles: []string{tc.role}, CSRF: "csrf", ExpiresAt: time.Now().Add(time.Hour).Unix()}
		if err := app.sessions.Put(context.Background(), session); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/players/1/equip-gem",
			strings.NewReader(`{"payload":{"location":"2","slotIndex":"0","gems":[20101,20102,0,0]}}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "equip-gem-key-1")
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

// 路由必须真的注册过：注册表里没有这条 path 时，带正确 CSRF 的请求会走到 404/405，
// 而不是把 payload 转成游戏服指令。这里只断言「不是 404」这一个下限。
func TestEquipGemRouteIsRegistered(t *testing.T) {
	route, ok := extraPlayerCommandRoutes["equip-gem"]
	if !ok {
		t.Fatal("equip-gem 没有注册进 extraPlayerCommandRoutes")
	}
	if route.action != "player.equip_gem" {
		t.Fatalf("action=%q", route.action)
	}
	if route.permission != "players.equip" {
		t.Fatalf("permission=%q", route.permission)
	}
}
