package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"mhqserver/internal/mysqlschema"
)

// 前端手工装备面板按这三个名字拉装备当前的随机属性 / 词缀 / 宝石槽。
// 名字写错了只会变成 404，很难在联调前发现，所以在这里钉住。
func TestEquipManualStateSubresourcesRegistered(t *testing.T) {
	for _, name := range []string{"random-attributes", "affixes", "gems"} {
		query := extraPlayerSubresources[name]
		if strings.TrimSpace(query) == "" {
			t.Fatalf("subresource %q is not registered", name)
		}
		// 这三条必须是只读的：注册表是给角色子资源用的，混进一条写语句会被
		// 任何有 players.read 的角色触发。
		lowered := strings.ToLower(strings.TrimSpace(query))
		if !strings.HasPrefix(lowered, "select") {
			t.Fatalf("subresource %q is not a SELECT: %s", name, query)
		}
		for _, forbidden := range []string{"insert ", "update ", "delete ", "drop ", "alter ", ";"} {
			if strings.Contains(lowered, forbidden) {
				t.Fatalf("subresource %q contains %q: %s", name, forbidden, query)
			}
		}
		// SQL 里的占位符必须只有一个：playerSubresource 只传 playerID 一个参数。
		if count := strings.Count(query, "?"); count != 1 {
			t.Fatalf("subresource %q has %d placeholders, want exactly 1: %s", name, count, query)
		}
	}
}

// 子资源名字不能和 query.go 里写死的 map 撞车，撞了会永远走不到注册表这一支。
func TestEquipManualStateSubresourcesDoNotShadowBuiltins(t *testing.T) {
	for _, builtin := range []string{"inventory", "equipment", "skills", "tasks", "activity", "mails", "pets"} {
		if extraPlayerSubresources[builtin] != "" {
			t.Fatalf("subresource %q shadows the built-in query", builtin)
		}
	}
}

// 只读集成：真的把三条 SQL 打到本机 MySQL 上，确认表名和列名都对得上（空结果也算通过，
// 这里验的是语句能执行而不是有数据）。与 items_test.go 一样用环境变量门控。
func TestEquipManualStateSubresourcesRunOnMySQL(t *testing.T) {
	if os.Getenv("MHQ_TEST_EQUIP_MANUAL_MYSQL") != "1" {
		t.Skip("set MHQ_TEST_EQUIP_MANUAL_MYSQL=1 to run against local MySQL")
	}
	db, err := sql.Open("mysql", mysqlschema.DefaultDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := &App{db: db, sessions: newMemorySessions()}
	for _, name := range []string{"random-attributes", "affixes", "gems"} {
		session := &sessionData{ID: "state-" + name, Roles: []string{"operator"}, ExpiresAt: time.Now().Add(time.Hour).Unix()}
		if err := app.sessions.Put(context.Background(), session); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/players/1/"+name, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
		rr := httptest.NewRecorder()
		app.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s", name, rr.Code, rr.Body.String())
		}
		var body struct {
			Data struct {
				Resource string           `json:"resource"`
				Items    []map[string]any `json:"items"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s decode: %v", name, err)
		}
		if body.Data.Resource != name {
			t.Fatalf("%s: resource=%q body=%s", name, body.Data.Resource, rr.Body.String())
		}
	}
}
