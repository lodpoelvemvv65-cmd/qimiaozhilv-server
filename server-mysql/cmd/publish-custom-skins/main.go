package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"gopkg.in/yaml.v3"
	"mhqserver/internal/mysqlschema"
)

type sourceFile struct {
	Version int                    `yaml:"version"`
	Tables  map[string][]sourceRow `yaml:"tables"`
}

type sourceRow struct {
	ID     int64                  `yaml:"id"`
	Fields map[string]interface{} `yaml:"fields"`
}

const (
	nodeObject = 1
	nodeArray  = 2
	nodeScalar = 3

	valueNull   = 0
	valueInt    = 1
	valueFloat  = 2
	valueString = 3
	valueBool   = 4
)

func main() {
	source := flag.String("source", "config/operations/CustomSkins.yaml", "YAML overlay source")
	dsn := flag.String("mysql", mysqlschema.DefaultDSN(), "MySQL DSN")
	redisAddr := flag.String("redis", envOrDefault("MHQ_REDIS_ADDR", "127.0.0.1:6379"), "Redis address")
	redisPassword := flag.String("redis-password", os.Getenv("MHQ_REDIS_PASSWORD"), "Redis password")
	redisDB := flag.Int("redis-db", 0, "Redis database")
	publisher := flag.String("publisher", "local-admin", "audit publisher")
	note := flag.String("note", "publish custom skin rows", "audit note")
	flag.Parse()

	raw, err := os.ReadFile(*source)
	if err != nil {
		log.Fatalf("read source: %v", err)
	}
	var file sourceFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		log.Fatalf("decode source: %v", err)
	}
	if file.Version != 1 || len(file.Tables) == 0 {
		log.Fatal("CustomSkins.yaml must contain version 1 and tables")
	}

	db, description, err := mysqlschema.Open(*dsn)
	if err != nil {
		log.Fatalf("open MySQL: %v", err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.Fatalf("begin transaction: %v", err)
	}
	defer tx.Rollback()

	tables := make([]string, 0, len(file.Tables))
	for name := range file.Tables {
		tables = append(tables, name)
	}
	sort.Strings(tables)
	rowsWritten := 0
	for _, name := range tables {
		for _, row := range file.Tables[name] {
			if row.ID <= 0 || len(row.Fields) == 0 {
				log.Fatalf("invalid %s row id=%d", name, row.ID)
			}
			if err := replaceRow(ctx, tx, name, row.ID, normalize(row.Fields)); err != nil {
				log.Fatalf("publish %s[%d]: %v", name, row.ID, err)
			}
			rowsWritten++
		}
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO game_config_revisions (published_at, publisher, note) VALUES (?, ?, ?)`, time.Now().Unix(), *publisher, *note)
	if err != nil {
		log.Fatalf("write revision: %v", err)
	}
	revision, err := result.LastInsertId()
	if err != nil {
		log.Fatalf("read revision: %v", err)
	}
	if err := tx.Commit(); err != nil {
		log.Fatalf("commit: %v", err)
	}
	redisClient := redis.NewClient(&redis.Options{Addr: *redisAddr, Password: *redisPassword, DB: *redisDB})
	defer redisClient.Close()
	reloadCtx, reloadCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer reloadCancel()
	if err := redisClient.Ping(reloadCtx).Err(); err != nil {
		log.Fatalf("connect Redis: %v", err)
	}
	payload := strconv.FormatInt(revision, 10) + "|EquipBase,SkinBase,Sys_Prefab"
	if err := redisClient.Publish(reloadCtx, "mhq:config:reload", payload).Err(); err != nil {
		log.Fatalf("publish reload notification: %v", err)
	}
	fmt.Printf("MySQL=%s revision=%d rows=%d\n", description, revision, rowsWritten)
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func replaceRow(ctx context.Context, tx *sql.Tx, name string, id int64, value interface{}) error {
	var rootID int64
	err := tx.QueryRowContext(ctx, `SELECT node_id FROM game_config_nodes WHERE config_name = ? AND parent_id = 0 AND field_name = '' AND array_index = ?`, name, id).Scan(&rootID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		if err := deleteNodeTree(ctx, tx, rootID); err != nil {
			return err
		}
	}
	_, err = insertNode(ctx, tx, name, 0, "", int(id), value)
	return err
}

func deleteNodeTree(ctx context.Context, tx *sql.Tx, nodeID int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT node_id FROM game_config_nodes WHERE parent_id = ?`, nodeID)
	if err != nil {
		return err
	}
	var children []int64
	for rows.Next() {
		var child int64
		if err := rows.Scan(&child); err != nil {
			rows.Close()
			return err
		}
		children = append(children, child)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, child := range children {
		if err := deleteNodeTree(ctx, tx, child); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM game_config_nodes WHERE node_id = ?`, nodeID)
	return err
}

func insertNode(ctx context.Context, tx *sql.Tx, name string, parentID int64, field string, index int, value interface{}) (int64, error) {
	kind, valueType, intValue, floatValue, stringValue, boolValue := classify(value)
	result, err := tx.ExecContext(ctx, `INSERT INTO game_config_nodes
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
			if _, err := insertNode(ctx, tx, name, nodeID, key, -1, v[key]); err != nil {
				return 0, err
			}
		}
	case []interface{}:
		for i, child := range v {
			if _, err := insertNode(ctx, tx, name, nodeID, "", i, child); err != nil {
				return 0, err
			}
		}
	}
	return nodeID, nil
}

func classify(value interface{}) (int, int, interface{}, interface{}, interface{}, interface{}) {
	switch v := value.(type) {
	case map[string]interface{}:
		return nodeObject, valueNull, nil, nil, nil, nil
	case []interface{}:
		return nodeArray, valueNull, nil, nil, nil, nil
	case int:
		return nodeScalar, valueInt, int64(v), nil, nil, nil
	case int64:
		return nodeScalar, valueInt, v, nil, nil, nil
	case uint64:
		if v <= math.MaxInt64 {
			return nodeScalar, valueInt, int64(v), nil, nil, nil
		}
	case float64:
		return nodeScalar, valueFloat, nil, v, nil, nil
	case string:
		return nodeScalar, valueString, nil, nil, v, nil
	case bool:
		if v {
			return nodeScalar, valueBool, nil, nil, nil, int64(1)
		}
		return nodeScalar, valueBool, nil, nil, nil, int64(0)
	}
	return nodeScalar, valueNull, nil, nil, nil, nil
}

func normalize(value interface{}) interface{} {
	switch v := value.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(v))
		for key, child := range v {
			out[key] = normalize(child)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(v))
		for i, child := range v {
			out[i] = normalize(child)
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
	case uint:
		return uint64(v)
	case uint8:
		return uint64(v)
	case uint16:
		return uint64(v)
	case uint32:
		return uint64(v)
	default:
		return value
	}
}
