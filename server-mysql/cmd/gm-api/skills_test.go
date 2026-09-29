package main

import (
	"net/http"
	"testing"
)

func TestSkillCatalogAccessBoundary(t *testing.T) {
	app := &App{sessions: newMemorySessions()}
	for _, tc := range []struct {
		role, method string
		status       int
	}{
		{"", http.MethodGet, http.StatusUnauthorized},
		{"config-editor", http.MethodGet, http.StatusForbidden},
		{"support", http.MethodPost, http.StatusMethodNotAllowed},
		{"readonly", http.MethodDelete, http.StatusMethodNotAllowed},
	} {
		if rr := catalogRequest(t, app, "/api/v1/skills", tc.method, tc.role); rr.Code != tc.status {
			t.Errorf("%s %s: got %d want %d", tc.role, tc.method, rr.Code, tc.status)
		}
	}
}

func TestSkillIDFromResourceAcceptsStoredJSONNumbers(t *testing.T) {
	if got := skillIDFromResource("110101"); got != 110101 {
		t.Fatalf("string id = %d", got)
	}
	if got := skillIDFromResource(int64(200001)); got != 200001 {
		t.Fatalf("int64 id = %d", got)
	}
}
