package main

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"
)

type auditRecord struct {
	RequestID      string
	AdminID        int64
	Action         string
	TargetType     string
	TargetID       string
	BeforeJSON     string
	AfterJSON      string
	Reason         string
	ClientIP       string
	UserAgent      string
	IdempotencyKey string
	Result         string
	ErrorCode      string
	DurationMS     int64
	CreatedAt      int64
}

func clientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	// Nginx is the only trusted public entry point. The API records the first
	// forwarded address and falls back to RemoteAddr for local development.
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		value := strings.TrimSpace(strings.Split(forwarded, ",")[0])
		if net.ParseIP(value) != nil {
			return value
		}
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && net.ParseIP(host) != nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func (a *App) writeAudit(ctx context.Context, record auditRecord) error {
	if a == nil || a.db == nil || strings.TrimSpace(record.RequestID) == "" {
		return nil
	}
	if record.CreatedAt == 0 {
		record.CreatedAt = time.Now().Unix()
	}
	_, err := a.db.ExecContext(ctx, `INSERT INTO gm_audit_logs
		(request_id, admin_id, action, target_type, target_id, before_json, after_json, reason,
		 client_ip, user_agent, idempotency_key, result, error_code, duration_ms, created_at)
		VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE result = VALUES(result), error_code = VALUES(error_code), duration_ms = VALUES(duration_ms)`,
		record.RequestID, record.AdminID, record.Action, record.TargetType, record.TargetID,
		record.BeforeJSON, record.AfterJSON, truncate(record.Reason, 500), truncate(record.ClientIP, 64),
		truncate(record.UserAgent, 500), truncate(record.IdempotencyKey, 191), truncate(record.Result, 32),
		truncate(record.ErrorCode, 64), record.DurationMS, record.CreatedAt)
	return err
}

func truncate(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return value[:max]
}

func (a *App) auditRows(ctx context.Context, limit int, cursor int64) ([]map[string]any, error) {
	if limit <= 0 {
		limit = 25
	}
	query := `SELECT id, request_id, admin_id, action, target_type, target_id, reason,
		result, error_code, duration_ms, created_at FROM gm_audit_logs WHERE id > ? ORDER BY id LIMIT ?`
	rows, err := a.db.QueryContext(ctx, query, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]map[string]any, 0, limit)
	for rows.Next() {
		var id, adminID, duration, created int64
		var request, action, targetType, targetID, reason, outcome, errorCode string
		if err := rows.Scan(&id, &request, &adminID, &action, &targetType, &targetID, &reason, &outcome, &errorCode, &duration, &created); err != nil {
			return nil, err
		}
		result = append(result, map[string]any{
			"id": strconvI64(id), "requestId": request, "adminId": strconvI64(adminID), "action": action,
			"targetType": targetType, "targetId": targetID, "reason": reason, "result": outcome,
			"errorCode": errorCode, "durationMs": strconvI64(duration), "createdAt": strconvI64(created),
		})
	}
	return result, rows.Err()
}

func (a *App) handleAudit(w http.ResponseWriter, r *http.Request, rid string, session *sessionData) {
	if r.Method != http.MethodGet || session == nil {
		writeError(w, http.StatusUnauthorized, rid, "unauthorized", "请先登录")
		return
	}
	limit := clampLimit(r.URL.Query().Get("limit"), 25)
	cursor := parseInt64Param(r.URL.Query().Get("cursor"), 0)
	ctx, cancel := a.requestContext(r)
	// Keep the audit view consistent with the command view when an older game
	// process left accepted commands without a result.
	_ = a.expireStaleCommands(ctx)
	rows, err := a.auditRows(ctx, limit, cursor)
	cancel()
	if err != nil {
		writeError(w, http.StatusInternalServerError, rid, "query_failed", "审计日志查询失败")
		return
	}
	writeJSON(w, http.StatusOK, rid, map[string]any{"items": rows, "nextCursor": nextCursor(rows)}, nil)
}

func nextCursor(rows []map[string]any) string {
	if len(rows) == 0 {
		return ""
	}
	if value, ok := rows[len(rows)-1]["id"].(string); ok {
		return value
	}
	return ""
}
