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

func TestSkillPermissionAllowsGMAndSubGM(t *testing.T) {
	cases := []struct {
		role string
		want bool
	}{
		{"superadmin", true},
		{"operator", true},
		{"subgm", true},
		{"support", false},
		{"auditor", false},
		{"readonly", false},
	}
	for _, tc := range cases {
		session := &sessionData{Roles: []string{tc.role}}
		if got := hasPermission(session, "players.skills"); got != tc.want {
			t.Fatalf("role %s players.skills = %v, want %v", tc.role, got, tc.want)
		}
	}
}

func TestSkillsAdjustRoutePermissionBoundary(t *testing.T) {
	app := &App{sessions: newMemorySessions()}
	for _, tc := range []struct {
		role string
		code string
	}{
		{"support", "forbidden"},
		{"subgm", "csrf_failed"},
		{"operator", "csrf_failed"},
		{"superadmin", "csrf_failed"},
	} {
		session := &sessionData{ID: "skill-" + tc.role, Roles: []string{tc.role}, CSRF: "csrf", ExpiresAt: time.Now().Add(time.Hour).Unix()}
		if err := app.sessions.Put(context.Background(), session); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/players/1/skills-adjust", strings.NewReader(`{"payload":{"skillPoint":"3"}}`))
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
