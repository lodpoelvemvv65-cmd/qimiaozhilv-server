package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"mhqserver/internal/mysqlschema"
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,64}$`)

var builtinRoles = map[string]string{
	"superadmin":    "全部管理权限",
	"operator":      "运营角色操作、发奖和配置发布",
	"support":       "客服查询和恢复类操作",
	"config-editor": "运营配置编辑和发布",
	"auditor":       "全量只读和审计查询",
	"readonly":      "白名单只读查询",
	"subgm":         "仅可管理已绑定游戏账号的角色",
}

func main() {
	username := flag.String("username", "", "管理员登录名（3-64 位 ASCII 字母、数字、_.-）")
	password := flag.String("password", os.Getenv("GM_ADMIN_PASSWORD"), "管理员密码；也可用 GM_ADMIN_PASSWORD")
	role := flag.String("role", "superadmin", "初始角色")
	displayName := flag.String("display-name", "", "显示名称")
	dsn := flag.String("mysql", mysqlschema.DefaultDSN(), "MySQL DSN")
	flag.Parse()
	if !usernamePattern.MatchString(*username) {
		log.Fatal("invalid -username")
	}
	if len(*password) < 12 || len(*password) > 128 {
		log.Fatal("-password must be 12-128 characters")
	}
	if _, ok := builtinRoles[*role]; !ok {
		log.Fatalf("unknown role %q", *role)
	}
	db, description, err := mysqlschema.Open(*dsn)
	if err != nil {
		log.Fatalf("open MySQL failed: %v", err)
	}
	defer db.Close()
	hash, err := bcrypt.GenerateFromPassword([]byte(*password), bcrypt.DefaultCost)
	if err != nil {
		log.Fatalf("hash password: %v", err)
	}
	now := time.Now().Unix()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		log.Fatal(err)
	}
	defer tx.Rollback()
	for name, description := range builtinRoles {
		if _, err := tx.Exec(`INSERT INTO gm_roles (name, description) VALUES (?, ?) ON DUPLICATE KEY UPDATE description = VALUES(description)`, name, description); err != nil {
			log.Fatalf("ensure role %s: %v", name, err)
		}
	}
	var adminID int64
	err = tx.QueryRow(`SELECT id FROM gm_admin_users WHERE username = ?`, *username).Scan(&adminID)
	if errors.Is(err, sql.ErrNoRows) {
		result, insertErr := tx.Exec(`INSERT INTO gm_admin_users (username, password_hash, display_name, status, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?)`, *username, string(hash), strings.TrimSpace(*displayName), now, now)
		if insertErr != nil {
			log.Fatalf("create admin: %v", insertErr)
		}
		adminID, err = result.LastInsertId()
	} else if err == nil {
		if _, err = tx.Exec(`UPDATE gm_admin_users SET password_hash = ?, display_name = ?, status = 1, updated_at = ? WHERE id = ?`, string(hash), strings.TrimSpace(*displayName), now, adminID); err != nil {
			log.Fatalf("update admin: %v", err)
		}
	} else {
		log.Fatal(err)
	}
	var roleID int64
	if err := tx.QueryRow(`SELECT id FROM gm_roles WHERE name = ?`, *role).Scan(&roleID); err != nil {
		log.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT IGNORE INTO gm_admin_roles (admin_id, role_id) VALUES (?, ?)`, adminID, roleID); err != nil {
		log.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("GM admin ready: id=%d username=%s role=%s mysql=%s\n", adminID, *username, *role, description)
}
