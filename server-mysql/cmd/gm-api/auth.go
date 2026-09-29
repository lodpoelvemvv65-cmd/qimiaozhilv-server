package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	json "github.com/goccy/go-json"
	"golang.org/x/crypto/bcrypt"
)

var rolePermissions = map[string]map[string]bool{
	"superadmin": {"*": true},
	"operator": {
		"dashboard.read": true, "accounts.read": true, "players.read": true, "families.read": true,
		"consignments.read": true, "bosses.read": true, "audit.read": true, "commands.read": true,
		"players.kick": true, "players.reload": true, "players.mail": true, "players.items": true,
		"players.currency": true, "players.level": true, "players.pet": true, "players.skills": true, "configs.read": true, "configs.publish": true,
	},
	"support": {
		"dashboard.read": true, "players.read": true, "players.kick": true, "players.reload": true,
		"players.mail": true, "commands.read": true,
	},
	"config-editor": {"dashboard.read": true, "configs.read": true, "configs.publish": true, "audit.read": true},
	"auditor":       {"dashboard.read": true, "accounts.read": true, "players.read": true, "families.read": true, "consignments.read": true, "bosses.read": true, "audit.read": true, "commands.read": true, "configs.read": true},
	"readonly":      {"dashboard.read": true, "players.read": true, "accounts.read": true},
	"subgm": {"players.read": true, "subgm.bind": true,
		"players.kick": true, "players.reload": true, "players.mail": true,
		"players.items": true, "players.currency": true, "players.level": true, "players.pet": true, "players.skills": true},
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type adminRecord struct {
	ID           int64
	Username     string
	PasswordHash string
	DisplayName  string
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request, rid string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, rid, "method_not_allowed", "请求方法不支持")
		return
	}
	if a.db == nil || a.sessions == nil {
		writeError(w, http.StatusServiceUnavailable, rid, "auth_unavailable", "管理认证服务暂不可用")
		return
	}
	var req loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, rid, "invalid_json", "请求格式错误")
		return
	}
	username := strings.TrimSpace(req.Username)
	if username == "" || len(username) > 191 || req.Password == "" {
		writeError(w, http.StatusUnauthorized, rid, "invalid_credentials", "管理员账号或密码错误")
		return
	}
	var admin adminRecord
	var status int
	err := a.db.QueryRowContext(r.Context(), `SELECT id, username, password_hash, display_name, status
		FROM gm_admin_users WHERE username = ? LIMIT 1`, username).
		Scan(&admin.ID, &admin.Username, &admin.PasswordHash, &admin.DisplayName, &status)
	if err != nil || status != 1 || bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte(req.Password)) != nil {
		writeError(w, http.StatusUnauthorized, rid, "invalid_credentials", "管理员账号或密码错误")
		return
	}
	roles, err := a.rolesForAdmin(r.Context(), admin.ID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, rid, "roles_unavailable", "管理员权限暂不可用")
		return
	}
	session, err := a.newSession(admin.ID, admin.Username, admin.DisplayName, roles)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, rid, "session_unavailable", "管理会话暂不可用")
		return
	}
	ctx, cancel := a.requestContext(r)
	err = a.sessions.Put(ctx, session)
	cancel()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, rid, "session_unavailable", "管理会话暂不可用")
		return
	}
	now := time.Now().Unix()
	_, _ = a.db.ExecContext(r.Context(), `UPDATE gm_admin_users SET last_login_at = ?, updated_at = ? WHERE id = ?`, now, now, admin.ID)
	a.setSessionCookies(w, session)
	_ = a.writeAudit(r.Context(), auditRecord{RequestID: rid, AdminID: admin.ID, Action: "auth.login", Result: "completed", ClientIP: clientIP(r), UserAgent: r.UserAgent()})
	writeJSON(w, http.StatusOK, rid, map[string]any{
		"adminId": strconvI64(admin.ID), "username": admin.Username, "displayName": admin.DisplayName,
		"roles": roles, "csrfToken": session.CSRF, "expiresAt": strconvI64(session.ExpiresAt),
	}, nil)
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request, rid string, session *sessionData) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, rid, "method_not_allowed", "请求方法不支持")
		return
	}
	if session == nil || !a.checkCSRF(r, session) {
		writeError(w, http.StatusForbidden, rid, "csrf_failed", "请求校验失败")
		return
	}
	ctx, cancel := a.requestContext(r)
	err := a.sessions.Delete(ctx, session.ID)
	cancel()
	a.clearSessionCookies(w)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, rid, "logout_failed", "退出登录失败")
		return
	}
	_ = a.writeAudit(r.Context(), auditRecord{RequestID: rid, AdminID: session.AdminID, Action: "auth.logout", Result: "completed", ClientIP: clientIP(r), UserAgent: r.UserAgent()})
	writeJSON(w, http.StatusOK, rid, map[string]any{"loggedOut": true}, nil)
}

func (a *App) handleMe(w http.ResponseWriter, r *http.Request, rid string, session *sessionData) {
	if r.Method != http.MethodGet || session == nil {
		writeError(w, http.StatusUnauthorized, rid, "unauthorized", "请先登录")
		return
	}
	writeJSON(w, http.StatusOK, rid, map[string]any{
		"adminId": strconvI64(session.AdminID), "username": session.Username, "displayName": session.DisplayName,
		"roles": session.Roles, "csrfToken": session.CSRF, "expiresAt": strconvI64(session.ExpiresAt),
	}, nil)
}

func (a *App) rolesForAdmin(ctx context.Context, adminID int64) ([]string, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT r.name FROM gm_roles r JOIN gm_admin_roles ar ON ar.role_id = r.id WHERE ar.admin_id = ? ORDER BY r.name`, adminID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var roles []string
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return nil, err
		}
		roles = append(roles, role)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return roles, nil
}

func hasPermission(session *sessionData, permission string) bool {
	if session == nil {
		return false
	}
	for _, role := range session.Roles {
		permissions := rolePermissions[role]
		if permissions["*"] || permissions[permission] || hasExtraPermission(role, permission) {
			return true
		}
	}
	return false
}

func (a *App) requireSession(r *http.Request) (*sessionData, *apiError) {
	session, err := a.sessionFromRequest(r)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, &apiError{Code: "unauthorized", Message: "请先登录"}
		}
		return nil, &apiError{Code: "session_unavailable", Message: "管理会话暂不可用"}
	}
	return session, nil
}

func (a *App) requirePermission(r *http.Request, permission string) (*sessionData, *apiError) {
	session, apiErr := a.requireSession(r)
	if apiErr != nil {
		return nil, apiErr
	}
	if !hasPermission(session, permission) {
		return nil, &apiError{Code: "forbidden", Message: "没有执行此操作的权限"}
	}
	return session, nil
}

func strconvI64(value int64) string { return strconv.FormatInt(value, 10) }
