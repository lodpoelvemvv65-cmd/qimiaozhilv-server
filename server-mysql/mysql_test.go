package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// mysqlTestDSN maps the historical per-test filename argument to an isolated
// MySQL database. Storage tests are skipped unless an administrator explicitly
// supplies a test server; gameplay tests remain database-independent.
func mysqlTestDSN(t *testing.T, identity string) string {
	t.Helper()
	baseDSN := os.Getenv("MHQ_TEST_MYSQL_DSN")
	if baseDSN == "" {
		t.Skip("MHQ_TEST_MYSQL_DSN is not set")
	}
	cfg, err := mysql.ParseDSN(baseDSN)
	if err != nil {
		t.Fatalf("parse MHQ_TEST_MYSQL_DSN: %v", err)
	}
	hash := sha256.Sum256([]byte(identity))
	cfg.DBName = fmt.Sprintf("mhq_test_%x", hash[:8])
	t.Cleanup(func() { dropMySQLTestDatabase(t, cfg) })
	return cfg.FormatDSN()
}

func dropMySQLTestDatabase(t *testing.T, cfg *mysql.Config) {
	t.Helper()
	bootstrap := *cfg
	bootstrap.DBName = ""
	db, err := sql.Open("mysql", bootstrap.FormatDSN())
	if err != nil {
		t.Errorf("open MySQL test cleanup connection: %v", err)
		return
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, "DROP DATABASE IF EXISTS `"+cfg.DBName+"`"); err != nil {
		t.Errorf("drop MySQL test database %s: %v", cfg.DBName, err)
	}
}
