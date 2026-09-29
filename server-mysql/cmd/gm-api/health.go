package main

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

type dependencyHealth struct {
	Status    string `json:"status"`
	LatencyMS string `json:"latencyMs"`
	Detail    string `json:"detail,omitempty"`
}

func (a *App) health(ctx context.Context) map[string]any {
	result := map[string]any{"status": "ok", "startedAt": strconvI64(a.startedAt.Unix()), "checkedAt": strconvI64(time.Now().Unix())}
	dbHealth := dependencyHealth{Status: "down"}
	if a.db != nil {
		start := time.Now()
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := a.db.PingContext(pingCtx)
		cancel()
		dbHealth.LatencyMS = strconvI64(time.Since(start).Milliseconds())
		if err == nil {
			dbHealth.Status = "up"
		} else {
			dbHealth.Detail = "database unavailable"
		}
	}
	redisHealth := dependencyHealth{Status: "down"}
	if a.redis != nil {
		start := time.Now()
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := a.redis.Ping(pingCtx).Err()
		cancel()
		redisHealth.LatencyMS = strconvI64(time.Since(start).Milliseconds())
		if err == nil {
			redisHealth.Status = "up"
		} else {
			redisHealth.Detail = "redis unavailable"
		}
	}
	gameHealth := dependencyHealth{Status: "down"}
	if strings.TrimSpace(a.gameAddr) != "" {
		start := time.Now()
		conn, err := net.DialTimeout("tcp", a.gameAddr, 2*time.Second)
		gameHealth.LatencyMS = strconvI64(time.Since(start).Milliseconds())
		if err == nil {
			gameHealth.Status = "up"
			_ = conn.Close()
		} else {
			gameHealth.Detail = "game server unavailable"
		}
	}
	result["mysql"] = dbHealth
	result["redis"] = redisHealth
	result["game"] = gameHealth
	if dbHealth.Status != "up" || redisHealth.Status != "up" || gameHealth.Status != "up" {
		result["status"] = "degraded"
	}
	return result
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request, rid string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, rid, "method_not_allowed", "请求方法不支持")
		return
	}
	ctx, cancel := a.requestContext(r)
	result := a.health(ctx)
	cancel()
	status := http.StatusOK
	if result["status"] == "degraded" {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, rid, result, nil)
}

func (a *App) handleDashboard(w http.ResponseWriter, r *http.Request, rid string, session *sessionData) {
	if r.Method != http.MethodGet || session == nil {
		writeError(w, http.StatusUnauthorized, rid, "unauthorized", "请先登录")
		return
	}
	ctx, cancel := a.requestContext(r)
	defer cancel()
	var accounts, players, recent int64
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts`).Scan(&accounts); err != nil {
		writeError(w, http.StatusInternalServerError, rid, "query_failed", "账号统计查询失败")
		return
	}
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM players`).Scan(&players); err != nil {
		writeError(w, http.StatusInternalServerError, rid, "query_failed", "角色统计查询失败")
		return
	}
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM players WHERE last_login >= ?`, time.Now().Add(-24*time.Hour).Unix()).Scan(&recent); err != nil {
		writeError(w, http.StatusInternalServerError, rid, "query_failed", "登录统计查询失败")
		return
	}
	online, onlineErr := a.onlineCount(ctx)
	if onlineErr != nil {
		online = -1
	}
	var revision sql.NullInt64
	_ = a.db.QueryRowContext(ctx, `SELECT MAX(revision) FROM game_config_revisions`).Scan(&revision)
	data := map[string]any{
		"accounts": strconvI64(accounts), "players": strconvI64(players), "online": strconvI64(online),
		"recentLogins24h": strconvI64(recent), "configRevision": strconvI64(revision.Int64),
		"onlineSource": "redis-markers", "health": a.health(ctx),
	}
	writeJSON(w, http.StatusOK, rid, data, nil)
}

func (a *App) onlineCount(ctx context.Context) (int64, error) {
	if a.redis == nil {
		return 0, fmt.Errorf("redis unavailable")
	}
	var cursor uint64
	var count int64
	for {
		keys, next, err := a.redis.Scan(ctx, cursor, "mhq:online:player:*", 500).Result()
		if err != nil {
			return 0, err
		}
		count += int64(len(keys))
		cursor = next
		if cursor == 0 {
			return count, nil
		}
	}
}
