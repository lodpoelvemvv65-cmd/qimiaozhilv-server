package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// configFileWatcher polls operation YAML files from the game process. A
// settled save is published directly by this process: YAML is decoded and
// validated, MySQL is updated in one transaction, and Redis notifies all game
// processes to rebuild their in-memory snapshot. No shell, PowerShell, or
// child Go process is involved in the hot-reload path.
type configFileStamp struct {
	modTime int64
	size    int64
}

type watchedConfigFile struct {
	stamp     configFileStamp
	changedAt time.Time
	initial   bool
}

func startConfigFileWatcher(ctx context.Context, directory string, db *sql.DB, cache *RedisCache,
	publisher, note string, interval, quiet time.Duration) {
	root, err := filepath.Abs(directory)
	if err != nil {
		log.Printf("config watcher resolve directory failed: %v", err)
		return
	}
	if info, statErr := os.Stat(root); statErr != nil || !info.IsDir() {
		if statErr == nil {
			statErr = fmt.Errorf("not a directory")
		}
		log.Printf("config watcher directory %s unavailable: %v", root, statErr)
		return
	}
	states := make(map[string]watchedConfigFile)
	if err := scanConfigFiles(ctx, root, db, cache, states, publisher, note, quiet, time.Now(), true); err != nil {
		log.Printf("config watcher initial scan failed: %v", err)
		return
	}
	log.Printf("config watcher enabled directory=%s interval=%s quiet=%s", root, interval, quiet)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("config watcher stopped")
			return
		case now := <-ticker.C:
			if err := scanConfigFiles(ctx, root, db, cache, states, publisher, note, quiet, now, false); err != nil {
				log.Printf("config watcher scan failed: %v", err)
			}
		}
	}
}

func scanConfigFiles(ctx context.Context, root string, db *sql.DB, cache *RedisCache,
	states map[string]watchedConfigFile, publisher, note string,
	quiet time.Duration, now time.Time, initial bool) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		path := filepath.Join(root, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		seen[path] = struct{}{}
		stamp := configFileStamp{modTime: info.ModTime().UnixNano(), size: info.Size()}
		state, known := states[path]
		if !known {
			state = watchedConfigFile{stamp: stamp, initial: initial}
			if initial {
				// 首次扫描是唯一一次能发现「服务端没在跑的时候改的 YAML」的机会：
				// 只记录指纹就 continue 的话，这种改动永远发布不出去，服务端会安静地
				// 继续用 MySQL 里的旧值（实例见 文档/14 与 Gameplay.yaml 的 town_idle_exp）。
				// 但也不能无条件发布，否则每次重启都给每个 operation 文件刷一条 revision。
				// 所以这里比对内容，只有真的不同才发布，见 config_file_sync.go。
				if err := initialConfigSync(ctx, db, cache, path, publisher, note); err != nil {
					log.Printf("config watcher initial sync %s failed: %v", path, err)
					// 留一个 changedAt，让普通轮询在静默窗口后按原路径重试一次。
					state.changedAt = now
				}
			} else {
				state.changedAt = now
			}
			states[path] = state
			continue
		}
		if state.stamp != stamp {
			state.stamp = stamp
			state.changedAt = now
			state.initial = false
			states[path] = state
			continue
		}
		if initial {
			continue
		}
		if state.initial {
			state.initial = false
			states[path] = state
			continue
		}
		if state.changedAt.IsZero() || now.Sub(state.changedAt) < quiet {
			continue
		}
		if err := publishConfigFile(ctx, db, cache, path, publisher, note); err != nil {
			log.Printf("config watcher publish %s failed: %v", path, err)
			state.changedAt = now
			states[path] = state
			continue
		}
		state.changedAt = time.Time{}
		states[path] = state
	}
	for path := range states {
		if _, ok := seen[path]; !ok {
			delete(states, path)
		}
	}
	return nil
}

func publishConfigFile(ctx context.Context, db *sql.DB, cache *RedisCache, path, publisher, note string) error {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if db == nil {
		return fmt.Errorf("MySQL database is not initialized")
	}
	value, err := decodeRuntimeConfigSource(path)
	if err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	if name == "CustomSkins" {
		return publishCustomSkinsRuntime(ctx, db, cache, value, publisher, note, path)
	}
	if err := validateRuntimeConfig(name, value); err != nil {
		return fmt.Errorf("validate %s: %w", name, err)
	}
	revision, err := replaceRuntimeConfig(ctx, db, name, value, publisher, note)
	if err != nil {
		return fmt.Errorf("publish %s: %w", name, err)
	}
	if cache != nil {
		if err := cache.PublishConfigReload(revision, []string{name}); err != nil {
			// MySQL is already committed atomically. Keep the file marked as
			// published and let the next service restart/reload recover if Redis
			// is temporarily unavailable; otherwise a save would be published
			// repeatedly and create revisions on every poll.
			log.Printf("config watcher Redis notification failed name=%s revision=%d: %v", name, revision, err)
		}
	}
	log.Printf("config watcher published name=%s revision=%d source=%s", name, revision, path)
	return nil
}
