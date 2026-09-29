package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mhqserver/internal/mysqlschema"
)

var (
	listenAddr    = flag.String("listen", ":7756", "TCP 监听地址")
	advertiseAddr = flag.String("advertise", envOrDefault("MHQ_PUBLIC_ADDR", "127.0.0.1:7756"), "返回给客户端的外部网关地址")
	dbDSN         = flag.String("db", mysqlschema.DefaultDSN(), "MySQL DSN（也可通过 MHQ_MYSQL_DSN 设置）")
	redisAddr     = flag.String("redis", envOrDefault("MHQ_REDIS_ADDR", "127.0.0.1:6379"), "Redis 地址")
	redisPassword = flag.String("redis-password", os.Getenv("MHQ_REDIS_PASSWORD"), "Redis 密码（也可通过 MHQ_REDIS_PASSWORD 设置）")
	redisDB       = flag.Int("redis-db", 0, "Redis DB")
	// Keep the Go watcher enabled by default so an operator can edit a YAML
	// file and have prices/Enabled flags published without a process restart.
	// Pass -watch-config=false for a strictly manual production rollout.
	watchConfig         = flag.Bool("watch-config", true, "watch config/operations YAML changes and publish automatically")
	watchConfigDir      = flag.String("watch-config-dir", "config/operations", "operation YAML directory to watch")
	watchConfigInterval = flag.Duration("watch-config-interval", 500*time.Millisecond, "config watcher poll interval")
	watchConfigQuiet    = flag.Duration("watch-config-quiet", 500*time.Millisecond, "quiet period before publishing a save")
	// Accepted only so older service wrappers keep starting. Both values are
	// ignored: runtime configuration always comes from MySQL.
	deprecatedTableFlag = flag.String("table", "", "已弃用且忽略：运行配置来自 MySQL")
	deprecatedSkillFlag = flag.String("skill-logic", "", "已弃用且忽略：技能配置来自 MySQL")
)

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func normalizeAdvertiseAddress(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	host, portText, err := net.SplitHostPort(value)
	if err != nil {
		return "", fmt.Errorf("expected host:port: %w", err)
	}
	if strings.TrimSpace(host) == "" {
		return "", fmt.Errorf("host is empty")
	}
	if strings.ContainsAny(host, " /\\?#@") {
		return "", fmt.Errorf("invalid host %q", host)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid port %q", portText)
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func main() {
	flag.Parse()
	publicAddress, err := normalizeAdvertiseAddress(*advertiseAddr)
	if err != nil {
		log.Fatalf("invalid -advertise/MHQ_PUBLIC_ADDR %q: %v", *advertiseAddr, err)
	}
	gateAddress = publicAddress

	// MySQL 存取层
	st, err := OpenStore(*dbDSN)
	if err != nil {
		log.Fatalf("open MySQL store failed: %v", err)
	}
	defer st.Close()
	log.Printf("MySQL store ready: %s", st.Description())
	cache, err := OpenRedisCache(*redisAddr, *redisPassword, *redisDB)
	if err != nil {
		log.Fatalf("open Redis cache failed: %v", err)
	}
	defer cache.Close()
	st.AttachCache(cache)
	log.Printf("Redis cache ready: %s db=%d", *redisAddr, *redisDB)

	// 所有运行配置均先在锁外完整构建，再一次性切换，避免启动或热更时出现半套快照。
	loadedTables, err := buildDatatablesFromDB(st.db)
	if err != nil {
		log.Fatalf("load MySQL datatables failed: %v", err)
	}
	_ = deprecatedTableFlag
	_ = deprecatedSkillFlag
	loadedSkillLogic, err := LoadSkillLogicCatalogFromDB(st.db)
	if err != nil {
		log.Fatalf("load SkillLogicConfig from MySQL failed: %v", err)
	}
	loadedGameplay, err := buildGameplayConfigFromDB(st.db)
	if err != nil {
		log.Fatalf("load Gameplay config from MySQL failed: %v", err)
	}
	loadedAffixWash, err := buildAffixWashConfigFromDB(st.db)
	if err != nil {
		log.Fatalf("load AffixWash config from MySQL failed: %v", err)
	}
	configStateMu.Lock()
	tables = loadedTables
	skillLogicCatalog = loadedSkillLogic
	storeGameplayConfig(loadedGameplay)
	storeAffixWashConfig(loadedAffixWash)
	configStateMu.Unlock()
	if migrated, err := migrateLegacyEquipmentAttributes(st.db, loadedTables); err != nil {
		log.Fatalf("migrate legacy equipment attributes: %v", err)
	} else {
		log.Printf("legacy equipment attribute migration: %d corrected keys", migrated)
	}
	log.Printf("SkillLogicConfig loaded: %d skills, %d modifiers", len(skillLogicCatalog.Skills), len(skillLogicCatalog.Modifiers))
	log.Printf("Gameplay config loaded: quiz=%d pet-tiers=%d fallback(play/explore/experience)=%d/%d/%dms market=%d..%d%%",
		loadedGameplay.Quiz.QuestionsPerRound,
		len(loadedGameplay.Pet.DurationTiers),
		loadedGameplay.Pet.PlayDurationMS, loadedGameplay.Pet.ExploreDurationMS, loadedGameplay.Pet.ExperienceDurationMS,
		loadedGameplay.Market.MinimumPercent, loadedGameplay.Market.MaximumPercent)
	log.Printf("AffixWash config loaded: empty=%.2f%% six-dim=%.2f%% six-dim-full=%.2f%% tiers=%v",
		loadedAffixWash.Affix.EmptyPercent, loadedAffixWash.Affix.SixDimensionPercent,
		loadedAffixWash.Affix.SixDimensionFullPercent, loadedAffixWash.Affix.SixDimensionTiers)
	if revision, revisionErr := latestConfigRevision(st.db); revisionErr == nil {
		log.Printf("configuration revision=%d", revision)
	}
	reloadCtx, cancelReload := context.WithCancel(context.Background())
	defer cancelReload()
	go watchConfigReload(reloadCtx, st.db, cache)
	if *watchConfig {
		go startConfigFileWatcher(reloadCtx, *watchConfigDir, st.db, cache,
			"server", "automatic YAML change", *watchConfigInterval, *watchConfigQuiet)
	}

	srv, err := NewServer(*listenAddr)
	if err != nil {
		log.Fatalf("listen %s failed: %v", *listenAddr, err)
	}
	srv.store = st
	globalServer = srv       // 供世界 BOSS/家族 BOSS/寄售等全局状态走 DB
	loadConsignmentsFromDB() // 寄售条目从 DB 恢复（重启不丢）
	log.Printf("public gate address: %s", gateAddress)
	// channel 关闭（断开/超时/退出）→ 角色下线标记
	srv.onClose = func(ch *channel) {
		configStateMu.RLock()
		defer configStateMu.RUnlock()
		srv.cleanupPlayerSession(ch)
	}
	log.Printf("mhqserver listening on tcp %s", srv.Addr())

	go srv.serveLoop()
	go srv.tickerLoop()
	go srv.combatTickerLoop()
	go srv.bossSweepLoop() // 世界 BOSS 刷新（BossBase.RefreshInterval 到点广播 BossRefresh）
	go srv.tradeSweepLoop()
	go srv.launcherTeamPlanLoop()
	go srv.townIdleExpLoop() // 主城挂机经验（线上整分钟节拍，只在城镇地图生效）
	go startGMCommandConsumer(reloadCtx, srv)
	// 周期清理过期登录 Key
	go func() {
		tick := time.NewTicker(2 * time.Minute)
		defer tick.Stop()
		for range tick.C {
			st.CleanupKeys()
		}
	}()

	// 等待退出信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	log.Println("shutting down...")
	cancelReload()
	srv.Close()
}
