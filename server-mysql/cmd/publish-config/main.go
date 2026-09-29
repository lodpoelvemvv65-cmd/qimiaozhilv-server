// publish-config validates one YAML configuration file, commits it to the
// MySQL configuration tree, records a revision, and broadcasts a Redis reload
// notification. The running server never reads this file directly.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"gopkg.in/yaml.v3"
	"mhqserver/internal/mysqlschema"
	"mhqserver/internal/operationsconfig"
)

const reloadChannel = "mhq:config:reload"

func main() {
	name := flag.String("name", "", "configuration name, e.g. ShopBase or WorldBossConfig")
	source := flag.String("source", "", "YAML/YML source file")
	dsn := flag.String("mysql", mysqlschema.DefaultDSN(), "target MySQL DSN")
	redisAddr := flag.String("redis", envOrDefault("MHQ_REDIS_ADDR", "127.0.0.1:6379"), "Redis address")
	redisPassword := flag.String("redis-password", os.Getenv("MHQ_REDIS_PASSWORD"), "Redis password")
	redisDB := flag.Int("redis-db", 0, "Redis database")
	publisher := flag.String("publisher", "cli", "operator/audit identity")
	note := flag.String("note", "", "change note")
	validateOnly := flag.Bool("validate-only", false, "validate the YAML without connecting to MySQL or Redis")
	flag.Parse()
	if strings.TrimSpace(*name) == "" || strings.TrimSpace(*source) == "" {
		log.Fatal("-name and -source are required")
	}
	value, err := decodeSource(*source)
	if err != nil {
		log.Fatalf("decode %s: %v", *source, err)
	}
	if err := validateConfig(*name, value); err != nil {
		log.Fatalf("validate %s: %v", *name, err)
	}
	if *validateOnly {
		log.Printf("validated %s source=%s", *name, *source)
		return
	}
	db, description, err := mysqlschema.Open(*dsn)
	if err != nil {
		log.Fatalf("open MySQL: %v", err)
	}
	defer db.Close()
	redisClient := redis.NewClient(&redis.Options{Addr: *redisAddr, Password: *redisPassword, DB: *redisDB})
	defer redisClient.Close()
	pingCtx, cancelPing := context.WithTimeout(context.Background(), 5*time.Second)
	if err := redisClient.Ping(pingCtx).Err(); err != nil {
		cancelPing()
		log.Fatalf("connect Redis: %v", err)
	}
	cancelPing()
	revision, err := replaceConfig(db, *name, value, *publisher, *note)
	if err != nil {
		log.Fatalf("publish %s: %v", *name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	payload := strconv.FormatInt(revision, 10) + "|" + *name
	if err := redisClient.Publish(ctx, reloadChannel, payload).Err(); err != nil {
		log.Fatalf("publish reload notification: %v", err)
	}
	log.Printf("published %s revision=%d source=%s mysql=%s", *name, revision, *source, description)
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func decodeSource(path string) (interface{}, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".yaml" && ext != ".yml" {
		return nil, fmt.Errorf("source must be a YAML/YML file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var value interface{}
	if err := yaml.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return normalizeYAML(value), nil
}

func normalizeYAML(value interface{}) interface{} {
	switch v := value.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(v))
		for key, child := range v {
			out[key] = normalizeYAML(child)
		}
		return out
	case map[interface{}]interface{}:
		out := make(map[string]interface{}, len(v))
		for key, child := range v {
			out[fmt.Sprint(key)] = normalizeYAML(child)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(v))
		for i, child := range v {
			out[i] = normalizeYAML(child)
		}
		return out
	case int:
		return int64(v)
	case int8:
		return int64(v)
	case int16:
		return int64(v)
	case int32:
		return int64(v)
	case uint8:
		return int64(v)
	case uint16:
		return int64(v)
	case uint32:
		return int64(v)
	case float32:
		return float64(v)
	default:
		return value
	}
}

func validateConfig(name string, value interface{}) error {
	if name == "Gameplay" {
		_, err := operationsconfig.Decode(value)
		return err
	}
	if name == operationsconfig.AffixWashConfigName {
		_, err := operationsconfig.DecodeAffixWash(value)
		return err
	}
	if name == "SkillLogicConfig" {
		if _, ok := value.(map[string]interface{}); !ok {
			return fmt.Errorf("root must be an object")
		}
		return nil
	}
	rows, ok := value.([]interface{})
	if !ok {
		return fmt.Errorf("datatable root must be an array of [id, object] pairs")
	}
	for index, row := range rows {
		pair, ok := row.([]interface{})
		if !ok || len(pair) < 2 {
			return fmt.Errorf("row %d is not [id, value]", index)
		}
		if _, err := numberInt(pair[0]); err != nil {
			return fmt.Errorf("row %d key: %w", index, err)
		}
		object, ok := pair[1].(map[string]interface{})
		if !ok {
			return fmt.Errorf("row %d value must be object", index)
		}
		if name == "ShopBase" || name == "MarketBase" || name == "MultiShop" {
			if enabled, exists := object["Enabled"]; exists {
				switch enabled.(type) {
				case bool, string, int64, int, int32, float64:
				default:
					return fmt.Errorf("row %d Enabled must be true/false", index)
				}
			}
		}
	}
	return nil
}

func replaceConfig(db *sql.DB, name string, value interface{}, publisher, note string) (int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM game_config_nodes WHERE config_name = ?`, name); err != nil {
		return 0, err
	}
	if rows, ok := value.([]interface{}); ok {
		for _, raw := range rows {
			pair := raw.([]interface{})
			id, _ := numberInt(pair[0])
			if _, err := insertNode(tx, name, 0, "", int(id), pair[1]); err != nil {
				return 0, err
			}
		}
	} else if _, err := insertNode(tx, name, 0, "", -1, value); err != nil {
		return 0, err
	}
	result, err := tx.Exec(`INSERT INTO game_config_revisions (published_at, publisher, note) VALUES (?, ?, ?)`,
		time.Now().Unix(), publisher, note)
	if err != nil {
		return 0, err
	}
	revision, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return revision, nil
}

func numberInt(value interface{}) (int64, error) {
	switch v := value.(type) {
	case int:
		return int64(v), nil
	case int8:
		return int64(v), nil
	case int16:
		return int64(v), nil
	case int32:
		return int64(v), nil
	case int64:
		return v, nil
	case uint:
		if uint64(v) <= math.MaxInt64 {
			return int64(v), nil
		}
	case uint8:
		return int64(v), nil
	case uint16:
		return int64(v), nil
	case uint32:
		return int64(v), nil
	case uint64:
		if v <= math.MaxInt64 {
			return int64(v), nil
		}
	case float64:
		if math.Trunc(v) == v && v >= math.MinInt64 && v <= math.MaxInt64 {
			return int64(v), nil
		}
	}
	return 0, fmt.Errorf("%v is not an integer", value)
}

func insertNode(tx *sql.Tx, name string, parentID int64, field string, index int, value interface{}) (int64, error) {
	kind, valueType, intValue, floatValue, stringValue, boolValue := classify(value)
	result, err := tx.Exec(`INSERT INTO game_config_nodes
		(config_name, parent_id, field_name, array_index, node_kind, value_type, int_value, float_value, string_value, bool_value)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, name, parentID, field, index, kind, valueType,
		intValue, floatValue, stringValue, boolValue)
	if err != nil {
		return 0, err
	}
	nodeID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	switch v := value.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if _, err := insertNode(tx, name, nodeID, key, -1, v[key]); err != nil {
				return 0, err
			}
		}
	case []interface{}:
		for i, child := range v {
			if _, err := insertNode(tx, name, nodeID, "", i, child); err != nil {
				return 0, err
			}
		}
	}
	return nodeID, nil
}

func classify(value interface{}) (int, int, interface{}, interface{}, interface{}, interface{}) {
	switch v := value.(type) {
	case map[string]interface{}:
		return 1, 0, nil, nil, nil, nil
	case []interface{}:
		return 2, 0, nil, nil, nil, nil
	case int:
		return 3, 1, int64(v), nil, nil, nil
	case int8:
		return 3, 1, int64(v), nil, nil, nil
	case int16:
		return 3, 1, int64(v), nil, nil, nil
	case int32:
		return 3, 1, int64(v), nil, nil, nil
	case int64:
		return 3, 1, v, nil, nil, nil
	case uint:
		return 3, 1, int64(v), nil, nil, nil
	case uint8:
		return 3, 1, int64(v), nil, nil, nil
	case uint16:
		return 3, 1, int64(v), nil, nil, nil
	case uint32:
		return 3, 1, int64(v), nil, nil, nil
	case uint64:
		return 3, 1, int64(v), nil, nil, nil
	case float32:
		return 3, 2, nil, float64(v), nil, nil
	case float64:
		return 3, 2, nil, v, nil, nil
	case string:
		return 3, 3, nil, nil, v, nil
	case bool:
		if v {
			return 3, 4, nil, nil, nil, 1
		}
		return 3, 4, nil, nil, nil, 0
	case nil:
		return 3, 0, nil, nil, nil, nil
	}
	return 3, 3, nil, nil, fmt.Sprint(value), nil
}
