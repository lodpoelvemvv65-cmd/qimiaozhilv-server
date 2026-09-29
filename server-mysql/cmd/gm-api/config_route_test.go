package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleConfigsRejectsIncompleteRevisionPath(t *testing.T) {
	app := &App{}
	session := &sessionData{AdminID: 1, Username: "tester", Roles: []string{"superadmin"}, CSRF: "csrf"}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/configs/Gameplay/revisions", nil)
	recorder := httptest.NewRecorder()

	app.handleConfigs(recorder, request, "test-request", session)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}
