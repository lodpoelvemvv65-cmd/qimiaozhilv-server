package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"mhqserver/internal/mysqlschema"
)

var (
	gmListen  = flag.String("listen", envOrDefault("GM_LISTEN", "127.0.0.1:8080"), "GM API 监听地址")
	gmDSN     = flag.String("mysql", mysqlschema.DefaultDSN(), "GM API MySQL DSN")
	gmRedis   = flag.String("redis", envOrDefault("MHQ_REDIS_ADDR", "127.0.0.1:6379"), "Redis 地址")
	gmRedisPW = flag.String("redis-password", os.Getenv("MHQ_REDIS_PASSWORD"), "Redis 密码")
	gmRedisDB = flag.Int("redis-db", 0, "Redis DB")
	gmGame    = flag.String("game", envOrDefault("GM_GAME_ADDR", "127.0.0.1:7756"), "游戏服控制/健康检查地址")
)

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func main() {
	flag.Parse()
	db, description, err := mysqlschema.Open(*gmDSN)
	if err != nil {
		log.Fatalf("open GM MySQL failed: %v", err)
	}
	defer db.Close()
	log.Printf("GM MySQL ready: %s", description)

	redisClient := redis.NewClient(&redis.Options{Addr: *gmRedis, Password: *gmRedisPW, DB: *gmRedisDB,
		DialTimeout: 5 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, PoolSize: 20})
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	redisErr := redisClient.Ping(pingCtx).Err()
	cancel()
	if redisErr != nil {
		_ = redisClient.Close()
		redisClient = nil
		log.Printf("WARNING: Redis unavailable: %v", redisErr)
	}
	allowMemory := envBool("GM_ALLOW_MEMORY_SESSIONS", false)
	app := newApp(db, redisClient, strings.TrimSpace(*gmGame), allowMemory)
	logStartup(app)

	server := &http.Server{Addr: strings.TrimSpace(*gmListen), Handler: app, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	stopCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-stopCtx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	log.Printf("gm-api listening on %s", server.Addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("gm-api failed: %v", err)
	}
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rid := requestID()
	w.Header().Set("X-Request-Id", rid)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	if r.Method == http.MethodOptions {
		w.Header().Set("Allow", "GET, POST, DELETE, OPTIONS")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/api/v1") {
		writeError(w, http.StatusNotFound, rid, "not_found", "接口不存在")
		return
	}
	if path == "/api/v1/health" {
		a.handleHealth(w, r, rid)
		return
	}
	if path == "/api/v1/auth/login" {
		a.handleLogin(w, r, rid)
		return
	}
	session, authErr := a.requireSession(r)
	if authErr != nil {
		writeJSON(w, http.StatusUnauthorized, rid, nil, authErr)
		return
	}
	if path == "/api/v1/auth/logout" {
		a.handleLogout(w, r, rid, session)
		return
	}
	if path == "/api/v1/auth/me" {
		a.handleMe(w, r, rid, session)
		return
	}
	if path == "/api/v1/sub-gm/bindings" {
		a.handleSubGMSelf(w, r, rid, session, path)
		return
	}
	if path == "/api/v1/sub-gms" || strings.HasPrefix(path, "/api/v1/sub-gms/") {
		a.handleSubGMs(w, r, rid, session, path)
		return
	}
	if path == "/api/v1/items" || path == "/api/v1/items/export" {
		a.handleItemCatalog(w, r, rid, session, path == "/api/v1/items/export")
		return
	}
	if path == "/api/v1/skills" {
		a.handleSkillCatalog(w, r, rid, session)
		return
	}
	// 手工装备面板的候选池（随机属性/洗练词缀/宝石/部位规则）。只读，GET 不需要 CSRF，
	// 权限在 handler 内校验为 players.equip，与 player.equip_manual 指令同权限。
	if path == "/api/v1/equip-manual/options" {
		a.handleEquipManualOptions(w, r, rid, session)
		return
	}
	if path == "/api/v1/dashboard" {
		if !hasPermission(session, "dashboard.read") {
			writeError(w, http.StatusForbidden, rid, "forbidden", "没有执行此操作的权限")
			return
		}
		a.handleDashboard(w, r, rid, session)
		return
	}
	if path == "/api/v1/configs" || strings.HasPrefix(path, "/api/v1/configs/") {
		a.handleConfigs(w, r, rid, session)
		return
	}
	if path == "/api/v1/accounts" {
		if !hasPermission(session, "accounts.read") {
			writeError(w, http.StatusForbidden, rid, "forbidden", "没有执行此操作的权限")
			return
		}
		a.handleAccounts(w, r, rid, session)
		return
	}
	if path == "/api/v1/players" {
		if !hasPermission(session, "players.read") {
			writeError(w, http.StatusForbidden, rid, "forbidden", "没有执行此操作的权限")
			return
		}
		a.handlePlayers(w, r, rid, session)
		return
	}
	if strings.HasPrefix(path, "/api/v1/players/") {
		commandParts := strings.Split(strings.TrimPrefix(path, "/api/v1/players/"), "/")
		if len(commandParts) == 2 || len(commandParts) == 3 {
			commandPlayerID := parseInt64Param(commandParts[0], 0)
			commandPath := commandParts[1]
			if len(commandParts) == 3 {
				commandPath += "/" + commandParts[2]
			}
			commandAction := map[string]struct {
				action     string
				permission string
			}{
				"kick":            {"player.kick", "players.kick"},
				"reload":          {"player.reload", "players.reload"},
				"currency-adjust": {"player.currency_adjust", "players.currency"},
				"level-adjust":    {"player.level_adjust", "players.level"},
				"pet-adjust":      {"player.pet_adjust", "players.pet"},
				"mail":            {"player.mail", "players.mail"},
				"items/grant":     {"player.items.grant", "players.items"},
			}[commandPath]
			if commandAction.action == "" {
				if extra, ok := extraPlayerCommandRoutes[commandPath]; ok {
					commandAction.action = extra.action
					commandAction.permission = extra.permission
				}
			}
			if commandPlayerID > 0 && commandAction.action != "" {
				a.handlePlayerCommand(w, r, rid, commandPlayerID, commandAction.action, commandAction.permission)
				return
			}
		}
		if !hasPermission(session, "players.read") {
			writeError(w, http.StatusForbidden, rid, "forbidden", "没有执行此操作的权限")
			return
		}
		parts := strings.Split(strings.TrimPrefix(path, "/api/v1/players/"), "/")
		if len(parts) > 2 || parts[0] == "" {
			writeError(w, http.StatusNotFound, rid, "not_found", "角色资源不存在")
			return
		}
		playerID := parseInt64Param(parts[0], 0)
		if playerID <= 0 {
			writeError(w, http.StatusBadRequest, rid, "invalid_player_id", "角色 ID 无效")
			return
		}
		sub := ""
		if len(parts) == 2 {
			sub = parts[1]
		}
		a.handlePlayerDetail(w, r, rid, session, playerID, sub)
		return
	}
	if path == "/api/v1/audit" {
		if !hasPermission(session, "audit.read") {
			writeError(w, http.StatusForbidden, rid, "forbidden", "没有执行此操作的权限")
			return
		}
		a.handleAudit(w, r, rid, session)
		return
	}
	if path == "/api/v1/families" {
		if !hasPermission(session, "families.read") {
			writeError(w, http.StatusForbidden, rid, "forbidden", "没有执行此操作的权限")
			return
		}
		a.handleFamilies(w, r, rid, session)
		return
	}
	if path == "/api/v1/consignments" {
		if !hasPermission(session, "consignments.read") {
			writeError(w, http.StatusForbidden, rid, "forbidden", "没有执行此操作的权限")
			return
		}
		a.handleConsignments(w, r, rid, session)
		return
	}
	if path == "/api/v1/world-bosses" || path == "/api/v1/bosses" {
		if !hasPermission(session, "bosses.read") {
			writeError(w, http.StatusForbidden, rid, "forbidden", "没有执行此操作的权限")
			return
		}
		a.handleBosses(w, r, rid, session)
		return
	}
	if strings.HasPrefix(path, "/api/v1/commands") {
		if !hasPermission(session, "commands.read") {
			writeError(w, http.StatusForbidden, rid, "forbidden", "没有执行此操作的权限")
			return
		}
		request := strings.TrimPrefix(path, "/api/v1/commands")
		request = strings.TrimPrefix(request, "/")
		a.handleCommands(w, r, rid, session, request)
		return
	}
	// 全服作用域操作（公告 / 邮件）。权限在 handler 内校验：两个子路径共用
	// server.broadcast，与单角色命令按 action 分别授权不同，这里不需要按路径分表。
	if path == "/api/v1/broadcast" || strings.HasPrefix(path, "/api/v1/broadcast/") {
		a.handleBroadcast(w, r, rid, session, path)
		return
	}
	writeError(w, http.StatusNotFound, rid, "not_found", "接口不存在")
}
