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

func TestStatsPermissionGMOnly(t *testing.T) {
	for _, tc := range []struct {
		role string
		want bool
	}{
		{"superadmin", true}, {"operator", true}, {"subgm", false},
		{"support", false}, {"auditor", false}, {"readonly", false},
	} {
		session := &sessionData{Roles: []string{tc.role}}
		if got := hasPermission(session, "players.stats"); got != tc.want {
			t.Fatalf("role %s players.stats = %v, want %v", tc.role, got, tc.want)
		}
	}
}

func TestStatsAdjustRoutePermissionBoundary(t *testing.T) {
	app := &App{sessions: newMemorySessions()}
	for _, tc := range []struct {
		role string
		code string
	}{
		{"support", "forbidden"}, {"subgm", "forbidden"}, {"operator", "csrf_failed"}, {"superadmin", "csrf_failed"},
	} {
		session := &sessionData{ID: "stats-" + tc.role, Roles: []string{tc.role}, CSRF: "csrf", ExpiresAt: time.Now().Add(time.Hour).Unix()}
		if err := app.sessions.Put(context.Background(), session); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/players/1/stats-adjust", strings.NewReader(`{"payload":{"energy":"10"}}`))
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
