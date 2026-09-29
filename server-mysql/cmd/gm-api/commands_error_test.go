package main

import (
	"net/http"
	"testing"
)

// 游戏服的业务拒绝（status=rejected）过去只写进 data，error 恒为 nil，
// 前端拿不到中文原因，只能显示「请求失败（422）」。这些用例锁住新的拆分规则。
func TestCommandResponseRejectedCarriesApiError(t *testing.T) {
	result := map[string]any{
		"status":    "rejected",
		"errorCode": "invalid_equip",
		"message":   "装备已强化至最高等级",
		"data":      map[string]any{"before": map[string]any{"strength": 20}},
	}
	data, apiErr := commandResponse(result, http.StatusUnprocessableEntity)
	if apiErr == nil {
		t.Fatal("rejected result must produce an apiError")
	}
	if apiErr.Code != "invalid_equip" {
		t.Fatalf("error code = %q, want invalid_equip", apiErr.Code)
	}
	if apiErr.Message != "装备已强化至最高等级" {
		t.Fatalf("error message = %q, want the game server text", apiErr.Message)
	}
	if data == nil {
		t.Fatal("structured result must still be returned as data")
	}
	if _, ok := data["data"]; !ok {
		t.Fatal("before/after payload must survive the error mapping")
	}
}

func TestCommandResponseRejectedWithoutMessage(t *testing.T) {
	data, apiErr := commandResponse(map[string]any{"status": "rejected"}, http.StatusUnprocessableEntity)
	if apiErr == nil {
		t.Fatal("rejected result must produce an apiError")
	}
	if apiErr.Code != "command_rejected" {
		t.Fatalf("error code = %q, want command_rejected", apiErr.Code)
	}
	if apiErr.Message == "" {
		t.Fatal("error message must never be empty; the UI would fall back to a bare status code")
	}
	if data == nil {
		t.Fatal("data must be passed through even when the message is missing")
	}
}

func TestCommandResponseCompletedAndPendingPassThrough(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"completed", http.StatusOK},
		{"pending", http.StatusAccepted},
	} {
		result := map[string]any{"status": "ok", "message": "角色当前不在线"}
		data, apiErr := commandResponse(result, tc.status)
		if apiErr != nil {
			t.Fatalf("%s must not produce an apiError, got %+v", tc.name, apiErr)
		}
		if data["message"] != "角色当前不在线" {
			t.Fatalf("%s must pass the result through unchanged", tc.name)
		}
	}
}
