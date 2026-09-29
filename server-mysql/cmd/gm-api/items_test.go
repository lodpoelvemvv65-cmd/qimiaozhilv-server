package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"mhqserver/internal/itemcatalog"
	"mhqserver/internal/mysqlschema"
)

func catalogRequest(t *testing.T, app *App, path, method, role string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if role != "" {
		session := &sessionData{ID: role, Roles: []string{role}, ExpiresAt: time.Now().Add(time.Hour).Unix()}
		if err := app.sessions.Put(context.Background(), session); err != nil {
			t.Fatal(err)
		}
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
	}
	rr := httptest.NewRecorder()
	app.ServeHTTP(rr, req)
	return rr
}

func TestItemCatalogAccessBoundary(t *testing.T) {
	app := &App{sessions: newMemorySessions()}
	for _, path := range []string{"/api/v1/items", "/api/v1/items/export"} {
		for _, tc := range []struct {
			role, method string
			status       int
		}{
			{"", http.MethodGet, http.StatusUnauthorized},
			{"config-editor", http.MethodGet, http.StatusForbidden},
			{"support", http.MethodPost, http.StatusMethodNotAllowed},
			{"readonly", http.MethodDelete, http.StatusMethodNotAllowed},
		} {
			if rr := catalogRequest(t, app, path, tc.method, tc.role); rr.Code != tc.status {
				t.Errorf("%s %s %s: got %d want %d", tc.role, tc.method, path, rr.Code, tc.status)
			}
		}
	}
}

// Read-only integration against published configuration; no schema migration,
// player changes, administrator changes or configuration publication occurs.
func TestItemCatalogPublishedMySQL(t *testing.T) {
	if os.Getenv("MHQ_TEST_ITEM_CATALOG_MYSQL") != "1" {
		t.Skip("set MHQ_TEST_ITEM_CATALOG_MYSQL=1 to check published local config")
	}
	db, err := sql.Open("mysql", mysqlschema.DefaultDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := &App{db: db, sessions: newMemorySessions()}
	rr := catalogRequest(t, app, "/api/v1/items", http.MethodGet, "support")
	if rr.Code != http.StatusOK {
		t.Fatalf("catalog status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Data struct {
			Items []itemcatalog.Item `json:"items"`
			Total int                `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var expected int
	if err := db.QueryRow(`SELECT COUNT(*) FROM game_config_nodes WHERE parent_id = 0 AND node_kind = 1 AND array_index > 0 AND config_name IN ('EquipBase', 'GoodsBase', 'MaterialBase')`).Scan(&expected); err != nil {
		t.Fatal(err)
	}
	if expected == 0 || body.Data.Total != expected || len(body.Data.Items) != expected {
		t.Fatalf("items=%d total=%d roots=%d", len(body.Data.Items), body.Data.Total, expected)
	}
	seen := map[int32]bool{}
	foundNurse, foundTitle, foundClothes := false, false, false
	for _, item := range body.Data.Items {
		if item.Name == "" || item.JobName == "" || seen[item.ID] {
			t.Fatalf("missing label/duplicate item: %+v", item)
		}
		seen[item.ID] = true
		if item.ID == 120211 {
			foundNurse = item.Name == "练习针筒" && item.JobType == 3 && item.JobName == "护士" && item.EquipSlot == 0 && item.EquipSlotName == "武器"
		}
		if item.ID == 120367 {
			foundTitle = item.EquipSlot == 4 && item.EquipSlotName == "称号"
		}
		if item.ID == 120243 {
			foundClothes = item.EquipSlot == 8 && item.EquipSlotName == "衣服"
		}
	}
	if !foundNurse {
		t.Fatal("published nurse equipment not mapped correctly")
	}
	if !foundTitle || !foundClothes {
		t.Fatal("published title/clothes equipment not mapped correctly")
	}
	rr = catalogRequest(t, app, "/api/v1/items/export", http.MethodGet, "readonly")
	if rr.Code != http.StatusOK || !strings.HasPrefix(rr.Body.String(), "\uFEFF") {
		t.Fatalf("export failed: %d", rr.Code)
	}
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(rr.Body.String(), "\uFEFF"))).ReadAll()
	if err != nil || len(rows) != expected+1 {
		t.Fatalf("export rows=%d err=%v", len(rows), err)
	}
	t.Logf("verified %d published items, Chinese names, profession mapping and CSV export", expected)
}
