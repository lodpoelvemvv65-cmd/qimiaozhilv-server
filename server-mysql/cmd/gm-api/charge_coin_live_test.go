package main

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"mhqserver/internal/mysqlschema"
)

// Opt-in live regression for the star-coin exchange direction (20314/20315):
// the GM API only stages the fixture (star coins cannot be obtained by any
// non-GM path), while the exchange itself and every assertion go through TCP.
// Uses a temporary HTTP test server with an in-memory test administrator.
func TestChargeCoinLiveTCP(t *testing.T) {
	if os.Getenv("MHQ_TEST_GM_CHARGECOIN_TCP") != "1" {
		t.Skip("requires local MySQL, Redis and game TCP 7756")
	}
	db, err := sql.Open("mysql", mysqlschema.DefaultDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	client := redis.NewClient(&redis.Options{Addr: envOrDefault("MHQ_REDIS_ADDR", "127.0.0.1:6379"), Password: os.Getenv("MHQ_REDIS_PASSWORD")})
	defer client.Close()
	app := &App{db: db, redis: client, sessions: newMemorySessions()}
	session := &sessionData{ID: requestID(), Username: "gm-charge-coin-test", Roles: []string{"operator"}, CSRF: requestID(), ExpiresAt: time.Now().Add(time.Hour).Unix()}
	if err := app.sessions.Put(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app)
	defer server.Close()
	account := "chargecoin" + requestID()[:16]
	defer func() {
		// The Python test deletes its own role through TCP first. Only remove
		// the account when that deletion succeeded; retain failed fixtures.
		_, err := db.Exec(`DELETE FROM accounts WHERE account = ? AND NOT EXISTS (SELECT 1 FROM players WHERE players.account_id = accounts.id)`, account)
		if err != nil {
			t.Logf("test account cleanup: %v", err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python", "../../test/verify_charge_coin.py")
	cmd.Env = append(os.Environ(), "GM_TEST_API_BASE="+server.URL, "GM_TEST_SESSION="+session.ID, "GM_TEST_CSRF="+session.CSRF, "GM_TEST_ACCOUNT="+account)
	output, err := cmd.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatalf("GM API / TCP charge coin regression: %v", err)
	}
}
