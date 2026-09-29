package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	json "github.com/goccy/go-json"
	"golang.org/x/crypto/bcrypt"
)

const maxSubGMBindings = 5

var subGMUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,64}$`)

type subGMCreateRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName"`
}

type subGMBindRequest struct {
	Account  string `json:"account"`
	Password string `json:"password"`
}

func isSubGM(session *sessionData) bool {
	if session == nil {
		return false
	}
	for _, role := range session.Roles {
		if role == "subgm" {
			return true
		}
	}
	return false
}

func (a *App) handleSubGMs(w http.ResponseWriter, r *http.Request, rid string, session *sessionData, path string) {
	if !hasPermission(session, "subgm.manage") {
		writeError(w, http.StatusForbidden, rid, "forbidden", "没有管理子 GM 的权限")
		return
	}
	trimmed := strings.TrimPrefix(path, "/api/v1/sub-gms")
	trimmed = strings.TrimPrefix(trimmed, "/")
	if trimmed == "" {
		switch r.Method {
		case http.MethodGet:
			a.listSubGMs(w, r, rid)
		case http.MethodPost:
			a.createSubGM(w, r, rid, session)
		default:
			writeError(w, http.StatusMethodNotAllowed, rid, "method_not_allowed", "请求方法不支持")
		}
		return
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) == 2 && parts[1] == "bindings" {
		subGMID := parseInt64Param(parts[0], 0)
		if subGMID <= 0 {
			writeError(w, http.StatusBadRequest, rid, "invalid_sub_gm_id", "子 GM ID 无效")
			return
		}
		switch r.Method {
		case http.MethodGet:
			a.listSubGMBindings(w, r, rid, subGMID)
		default:
			writeError(w, http.StatusMethodNotAllowed, rid, "method_not_allowed", "请求方法不支持")
		}
		return
	}
	if len(parts) == 3 && parts[1] == "bindings" && r.Method == http.MethodDelete {
		subGMID := parseInt64Param(parts[0], 0)
		accountID := parseInt64Param(parts[2], 0)
		if subGMID <= 0 || accountID <= 0 {
			writeError(w, http.StatusBadRequest, rid, "invalid_binding", "绑定 ID 无效")
			return
		}
		a.unbindSubGMAccount(w, r, rid, session, subGMID, accountID)
		return
	}
	writeError(w, http.StatusNotFound, rid, "not_found", "子 GM 资源不存在")
}

func (a *App) handleSubGMSelf(w http.ResponseWriter, r *http.Request, rid string, session *sessionData, path string) {
	if !isSubGM(session) || !hasPermission(session, "subgm.bind") {
		writeError(w, http.StatusForbidden, rid, "forbidden", "没有绑定游戏账号的权限")
		return
	}
	switch path {
	case "/api/v1/sub-gm/bindings":
		switch r.Method {
		case http.MethodGet:
			a.listSubGMBindings(w, r, rid, session.AdminID)
		case http.MethodPost:
			a.bindSubGMAccount(w, r, rid, session)
		default:
			writeError(w, http.StatusMethodNotAllowed, rid, "method_not_allowed", "请求方法不支持")
		}
	default:
		writeError(w, http.StatusNotFound, rid, "not_found", "子 GM 资源不存在")
	}
}

func (a *App) listSubGMs(w http.ResponseWriter, r *http.Request, rid string) {
	ctx, cancel := a.requestContext(r)
	defer cancel()
	rows, err := a.db.QueryContext(ctx, `SELECT u.id, u.username, u.display_name, u.status, u.created_at,
		COUNT(b.id) FROM gm_admin_users u
		JOIN gm_admin_roles ar ON ar.admin_id = u.id
		JOIN gm_roles role ON role.id = ar.role_id AND role.name = 'subgm'
		LEFT JOIN gm_sub_gm_bindings b ON b.sub_gm_id = u.id
		GROUP BY u.id, u.username, u.display_name, u.status, u.created_at ORDER BY u.id`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, rid, "query_failed", "子 GM 查询失败")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, created, count int64
		var username, displayName string
		var status int
		if err := rows.Scan(&id, &username, &displayName, &status, &created, &count); err != nil {
			writeError(w, http.StatusInternalServerError, rid, "query_failed", "子 GM 查询失败")
			return
		}
		items = append(items, map[string]any{"id": strconvI64(id), "username": username, "displayName": displayName,
			"status": status == 1, "createdAt": strconvI64(created), "bindingCount": count})
	}
	writeJSON(w, http.StatusOK, rid, map[string]any{"items": items}, nil)
}

func (a *App) createSubGM(w http.ResponseWriter, r *http.Request, rid string, session *sessionData) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, rid, "method_not_allowed", "请求方法不支持")
		return
	}
	if !a.checkCSRF(r, session) {
		writeError(w, http.StatusForbidden, rid, "csrf_failed", "请求校验失败")
		return
	}
	var req subGMCreateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, rid, "invalid_json", "请求格式错误")
		return
	}
	username := strings.TrimSpace(req.Username)
	password := req.Password
	displayName := strings.TrimSpace(req.DisplayName)
	if !subGMUsernamePattern.MatchString(username) {
		writeError(w, http.StatusBadRequest, rid, "invalid_username", "子 GM 账号只能使用 3-64 位 ASCII 字母、数字、下划线、点或短横线")
		return
	}
	if len(password) < 12 || len(password) > 128 {
		writeError(w, http.StatusBadRequest, rid, "invalid_password", "子 GM 密码长度必须为 12-128 位")
		return
	}
	if displayName == "" {
		displayName = username
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, rid, "password_hash_failed", "子 GM 密码处理失败")
		return
	}
	now := time.Now().Unix()
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, rid, "create_failed", "子 GM 创建失败")
		return
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO gm_roles (name, description) VALUES ('subgm', '仅可管理已绑定游戏账号的角色') ON DUPLICATE KEY UPDATE description = VALUES(description)`); err != nil {
		writeError(w, http.StatusInternalServerError, rid, "create_failed", "子 GM 角色初始化失败")
		return
	}
	result, err := tx.Exec(`INSERT INTO gm_admin_users (username, password_hash, display_name, status, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?)`, username, string(hash), displayName, now, now)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			writeError(w, http.StatusConflict, rid, "username_exists", "子 GM 账号已存在")
		} else {
			writeError(w, http.StatusInternalServerError, rid, "create_failed", "子 GM 创建失败")
		}
		return
	}
	adminID, err := result.LastInsertId()
	if err == nil {
		var roleID int64
		err = tx.QueryRow(`SELECT id FROM gm_roles WHERE name = 'subgm'`).Scan(&roleID)
		if err == nil {
			_, err = tx.Exec(`INSERT INTO gm_admin_roles (admin_id, role_id) VALUES (?, ?)`, adminID, roleID)
		}
	}
	if err != nil || tx.Commit() != nil {
		writeError(w, http.StatusInternalServerError, rid, "create_failed", "子 GM 权限初始化失败")
		return
	}
	_ = a.writeAudit(r.Context(), auditRecord{RequestID: rid, AdminID: session.AdminID, Action: "subgm.create", TargetType: "admin", TargetID: strconvI64(adminID), Result: "completed", ClientIP: clientIP(r), UserAgent: r.UserAgent()})
	writeJSON(w, http.StatusCreated, rid, map[string]any{"id": strconvI64(adminID), "username": username, "displayName": displayName, "role": "subgm"}, nil)
}

func (a *App) listSubGMBindings(w http.ResponseWriter, r *http.Request, rid string, subGMID int64) {
	ctx, cancel := a.requestContext(r)
	defer cancel()
	rows, err := a.db.QueryContext(ctx, `SELECT b.account_id, a.account, COUNT(p.id)
		FROM gm_sub_gm_bindings b JOIN accounts a ON a.id = b.account_id
		LEFT JOIN players p ON p.account_id = a.id WHERE b.sub_gm_id = ?
		GROUP BY b.account_id, a.account ORDER BY MIN(b.id)`, subGMID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, rid, "query_failed", "绑定查询失败")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0, maxSubGMBindings)
	for rows.Next() {
		var accountID, playerCount int64
		var account string
		if err := rows.Scan(&accountID, &account, &playerCount); err != nil {
			writeError(w, http.StatusInternalServerError, rid, "query_failed", "绑定查询失败")
			return
		}
		items = append(items, map[string]any{"accountId": strconvI64(accountID), "account": account, "playerCount": playerCount})
	}
	writeJSON(w, http.StatusOK, rid, map[string]any{"items": items, "limit": maxSubGMBindings}, nil)
}

func (a *App) bindSubGMAccount(w http.ResponseWriter, r *http.Request, rid string, session *sessionData) {
	if !a.checkCSRF(r, session) {
		writeError(w, http.StatusForbidden, rid, "csrf_failed", "请求校验失败")
		return
	}
	var req subGMBindRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, rid, "invalid_json", "请求格式错误")
		return
	}
	account := strings.TrimSpace(req.Account)
	if account == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, rid, "credentials_required", "请输入游戏账号和密码")
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, rid, "bind_failed", "绑定失败")
		return
	}
	defer tx.Rollback()
	// Lock before any consistent read: MySQL repeatable-read snapshots taken
	// before this lock could miss another concurrent binding and exceed five.
	var lockedID int64
	if err = tx.QueryRow(`SELECT id FROM gm_admin_users WHERE id = ? FOR UPDATE`, session.AdminID).Scan(&lockedID); err != nil {
		writeError(w, http.StatusInternalServerError, rid, "bind_failed", "子 GM 账号不存在")
		return
	}
	var accountID int64
	var storedPassword string
	err = tx.QueryRow(`SELECT id, password FROM accounts WHERE account = ?`, account).Scan(&accountID, &storedPassword)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, rid, "account_credentials_invalid", "游戏账号或密码错误")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, rid, "bind_failed", "游戏账号校验失败")
		return
	}
	if subtle.ConstantTimeCompare([]byte(storedPassword), []byte(req.Password)) != 1 {
		writeError(w, http.StatusUnauthorized, rid, "account_credentials_invalid", "游戏账号或密码错误")
		return
	}
	var alreadyBound int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM gm_sub_gm_bindings WHERE sub_gm_id = ? AND account_id = ?`, session.AdminID, accountID).Scan(&alreadyBound); err != nil {
		writeError(w, http.StatusInternalServerError, rid, "bind_failed", "绑定状态查询失败")
		return
	}
	if alreadyBound > 0 {
		writeError(w, http.StatusConflict, rid, "already_bound", "该游戏账号已经绑定")
		return
	}
	var count int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM gm_sub_gm_bindings WHERE sub_gm_id = ?`, session.AdminID).Scan(&count); err != nil {
		writeError(w, http.StatusInternalServerError, rid, "bind_failed", "绑定数量查询失败")
		return
	}
	if count >= maxSubGMBindings {
		writeError(w, http.StatusConflict, rid, "binding_limit", "最多只能绑定 5 个游戏账号")
		return
	}
	_, err = tx.Exec(`INSERT INTO gm_sub_gm_bindings (sub_gm_id, account_id, created_by, created_at) VALUES (?, ?, ?, ?)`, session.AdminID, accountID, session.AdminID, time.Now().Unix())
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			writeError(w, http.StatusConflict, rid, "already_bound", "该游戏账号已经绑定")
		} else {
			writeError(w, http.StatusInternalServerError, rid, "bind_failed", "绑定失败")
		}
		return
	}
	if err = tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, rid, "bind_failed", "绑定失败")
		return
	}
	_ = a.writeAudit(r.Context(), auditRecord{RequestID: rid, AdminID: session.AdminID, Action: "subgm.bind", TargetType: "account", TargetID: strconvI64(accountID), Result: "completed", ClientIP: clientIP(r), UserAgent: r.UserAgent()})
	writeJSON(w, http.StatusCreated, rid, map[string]any{"accountId": strconvI64(accountID), "bound": true, "unbindableBySelf": false}, nil)
}

func (a *App) unbindSubGMAccount(w http.ResponseWriter, r *http.Request, rid string, session *sessionData, subGMID, accountID int64) {
	if r.Method != http.MethodDelete || !a.checkCSRF(r, session) {
		writeError(w, http.StatusForbidden, rid, "csrf_failed", "请求校验失败")
		return
	}
	result, err := a.db.ExecContext(r.Context(), `DELETE FROM gm_sub_gm_bindings WHERE sub_gm_id = ? AND account_id = ?`, subGMID, accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, rid, "unbind_failed", "解绑失败")
		return
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		writeError(w, http.StatusNotFound, rid, "binding_not_found", "绑定不存在")
		return
	}
	_ = a.writeAudit(r.Context(), auditRecord{RequestID: rid, AdminID: session.AdminID, Action: "subgm.unbind", TargetType: "account", TargetID: strconvI64(accountID), Result: "completed", ClientIP: clientIP(r), UserAgent: r.UserAgent()})
	writeJSON(w, http.StatusOK, rid, map[string]any{"unbound": true}, nil)
}

func (a *App) subGMCanViewPlayer(ctx context.Context, session *sessionData, playerID int64) (bool, error) {
	if !isSubGM(session) {
		return true, nil
	}
	var exists int
	err := a.db.QueryRowContext(ctx, `SELECT 1 FROM players p JOIN gm_sub_gm_bindings b ON b.account_id = p.account_id WHERE p.id = ? AND b.sub_gm_id = ?`, playerID, session.AdminID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
