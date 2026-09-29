package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
)

var (
	configReloadMu sync.Mutex
	// Every runtime entry point that can dereference tables or
	// skillLogicCatalog holds a read lock. Reloading swaps both pointers while
	// holding the write lock, eliminating partial snapshots and Go data races.
	configStateMu sync.RWMutex
)

// reloadRuntimeConfig builds every configuration object off to the side and
// swaps the global pointers only after all tables and SkillLogicConfig have
// passed validation. In-flight handlers therefore keep using a complete old
// snapshot, while new handlers observe a complete new snapshot.
func reloadRuntimeConfig(db *sql.DB) error {
	configReloadMu.Lock()
	defer configReloadMu.Unlock()
	t, err := buildDatatablesFromDB(db)
	if err != nil {
		return err
	}
	catalog, err := LoadSkillLogicCatalogFromDB(db)
	if err != nil {
		return err
	}
	gameplay, err := buildGameplayConfigFromDB(db)
	if err != nil {
		return err
	}
	affixWash, err := buildAffixWashConfigFromDB(db)
	if err != nil {
		return err
	}
	configStateMu.Lock()
	tables = t
	skillLogicCatalog = catalog
	storeGameplayConfig(gameplay)
	storeAffixWashConfig(affixWash)
	configStateMu.Unlock()
	// Configuration changes are visible to already connected clients as well:
	// refresh the unified shop directory (including prices and Enabled state)
	// immediately instead of waiting for the next login or shop open.
	if globalServer != nil {
		globalServer.pushRuntimeConfigToOnline()
	}
	return nil
}

func (s *Server) pushRuntimeConfigToOnline() {
	if s == nil {
		return
	}
	for _, ch := range s.onlineChannels() {
		if ch == nil || ch.session == nil {
			continue
		}
		s.pushActiveInfo(ch)
		// NetItem.Description is part of every bag snapshot.  Re-send it on
		// datatable reload so an already logged-in client immediately sees a
		// changed GoodsBase/EquipBase/MaterialBase description.
		s.pushBagSnapshot(ch)
		// MarketUI already listens for M2C_GetMarket; this unsolicited snapshot
		// keeps its per-item prices synchronized while it is open.
		s.pushMarketSnapshot(ch)
	}
}

func latestConfigRevision(db *sql.DB) (int64, error) {
	var revision sql.NullInt64
	if err := db.QueryRow(`SELECT MAX(revision) FROM game_config_revisions`).Scan(&revision); err != nil {
		return 0, err
	}
	if !revision.Valid {
		return 0, nil
	}
	return revision.Int64, nil
}

func parseConfigReloadPayload(payload string) (int64, []string, error) {
	parts := strings.SplitN(strings.TrimSpace(payload), "|", 2)
	if len(parts) == 0 || parts[0] == "" {
		return 0, nil, fmt.Errorf("empty config reload revision")
	}
	revision, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || revision <= 0 {
		return 0, nil, fmt.Errorf("invalid config reload revision %q", parts[0])
	}
	if len(parts) == 1 || strings.TrimSpace(parts[1]) == "" {
		return revision, nil, nil
	}
	rawNames := strings.Split(parts[1], ",")
	names := make([]string, 0, len(rawNames))
	for _, raw := range rawNames {
		name := strings.TrimSpace(raw)
		if name != "" {
			names = append(names, name)
		}
	}
	return revision, names, nil
}

func watchConfigReload(ctx context.Context, db *sql.DB, cache *RedisCache) {
	if cache == nil {
		return
	}
	pubsub := cache.SubscribeConfigReload(ctx)
	if pubsub == nil {
		return
	}
	defer pubsub.Close()
	if _, err := pubsub.Receive(ctx); err != nil {
		if ctx.Err() == nil {
			log.Printf("config reload subscribe failed: %v", err)
		}
		return
	}
	channel := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case message, ok := <-channel:
			if !ok {
				return
			}
			revision, names, err := parseConfigReloadPayload(message.Payload)
			if err != nil {
				log.Printf("invalid config reload message: %v", err)
				continue
			}
			if err := reloadRuntimeConfig(db); err != nil {
				log.Printf("config reload revision=%d names=%v failed; old snapshot kept: %v", revision, names, err)
				continue
			}
			log.Printf("config reloaded revision=%d names=%v", revision, names)
		}
	}
}
