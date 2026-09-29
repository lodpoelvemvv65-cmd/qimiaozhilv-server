package main

// This file contains the in-process half of the configuration publisher. It
// intentionally mirrors cmd/publish-config's validation and relational-tree
// format, but does not invoke that command (or any shell) from the game
// server. The watcher can therefore publish a settled YAML save while the
// server is running.

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
	"mhqserver/internal/operationsconfig"
)

func decodeRuntimeConfigSource(path string) (interface{}, error) {
	ext := filepath.Ext(path)
	if ext != ".yaml" && ext != ".yml" && ext != ".YAML" && ext != ".YML" {
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
	return normalizeRuntimeYAML(value), nil
}

func normalizeRuntimeYAML(value interface{}) interface{} {
	switch v := value.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(v))
		for key, child := range v {
			out[key] = normalizeRuntimeYAML(child)
		}
		return out
	case map[interface{}]interface{}:
		out := make(map[string]interface{}, len(v))
		for key, child := range v {
			out[fmt.Sprint(key)] = normalizeRuntimeYAML(child)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(v))
		for i, child := range v {
			out[i] = normalizeRuntimeYAML(child)
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
	case float32:
		return float64(v)
	default:
		return value
	}
}

func validateRuntimeConfig(name string, value interface{}) error {
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
		if _, err := runtimeNumberInt(pair[0]); err != nil {
			return fmt.Errorf("row %d key: %w", index, err)
		}
		object, ok := pair[1].(map[string]interface{})
		if !ok {
			return fmt.Errorf("row %d value must be object", index)
		}
		if name == "ShopBase" || name == "MarketBase" || name == "MultiShop" {
			if enabled, exists := object["Enabled"]; exists {
				switch enabled.(type) {
				case bool, string, int64, int, int32, uint64, float64:
				default:
					return fmt.Errorf("row %d Enabled must be true/false", index)
				}
			}
		}
	}
	return nil
}

func replaceRuntimeConfig(ctx context.Context, db *sql.DB, name string, value interface{}, publisher, note string) (int64, error) {
	if db == nil {
		return 0, fmt.Errorf("MySQL database is not initialized")
	}
	writeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, err := db.BeginTx(writeCtx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(writeCtx, `DELETE FROM game_config_nodes WHERE config_name = ?`, name); err != nil {
		return 0, err
	}
	if rows, ok := value.([]interface{}); ok {
		for index, raw := range rows {
			pair, pairOK := raw.([]interface{})
			if !pairOK || len(pair) < 2 {
				return 0, fmt.Errorf("row %d is not [id, value]", index)
			}
			id, err := runtimeNumberInt(pair[0])
			if err != nil {
				return 0, fmt.Errorf("row %d key: %w", index, err)
			}
			if _, err := insertRuntimeConfigNode(writeCtx, tx, name, 0, "", int(id), pair[1]); err != nil {
				return 0, err
			}
		}
	} else if _, err := insertRuntimeConfigNode(writeCtx, tx, name, 0, "", -1, value); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(writeCtx, `INSERT INTO game_config_revisions (published_at, publisher, note) VALUES (?, ?, ?)`,
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

func runtimeNumberInt(value interface{}) (int64, error) {
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
	case float32:
		f := float64(v)
		if math.Trunc(f) == f && f >= math.MinInt64 && f <= math.MaxInt64 {
			return int64(f), nil
		}
	case float64:
		if math.Trunc(v) == v && v >= math.MinInt64 && v <= math.MaxInt64 {
			return int64(v), nil
		}
	}
	return 0, fmt.Errorf("%v is not an integer", value)
}

func insertRuntimeConfigNode(ctx context.Context, tx *sql.Tx, name string, parentID int64, field string, index int, value interface{}) (int64, error) {
	kind, valueType, intValue, floatValue, stringValue, boolValue := classifyRuntimeConfigValue(value)
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
			if _, err := insertRuntimeConfigNode(ctx, tx, name, nodeID, key, -1, v[key]); err != nil {
				return 0, err
			}
		}
	case []interface{}:
		for i, child := range v {
			if _, err := insertRuntimeConfigNode(ctx, tx, name, nodeID, "", i, child); err != nil {
				return 0, err
			}
		}
	}
	return nodeID, nil
}

func classifyRuntimeConfigValue(value interface{}) (int, int, interface{}, interface{}, interface{}, interface{}) {
	switch v := value.(type) {
	case map[string]interface{}:
		return configNodeObject, configValueNull, nil, nil, nil, nil
	case []interface{}:
		return configNodeArray, configValueNull, nil, nil, nil, nil
	case int:
		return configNodeScalar, configValueInt, int64(v), nil, nil, nil
	case int8:
		return configNodeScalar, configValueInt, int64(v), nil, nil, nil
	case int16:
		return configNodeScalar, configValueInt, int64(v), nil, nil, nil
	case int32:
		return configNodeScalar, configValueInt, int64(v), nil, nil, nil
	case int64:
		return configNodeScalar, configValueInt, v, nil, nil, nil
	case uint:
		return configNodeScalar, configValueInt, int64(v), nil, nil, nil
	case uint8:
		return configNodeScalar, configValueInt, int64(v), nil, nil, nil
	case uint16:
		return configNodeScalar, configValueInt, int64(v), nil, nil, nil
	case uint32:
		return configNodeScalar, configValueInt, int64(v), nil, nil, nil
	case uint64:
		if v <= math.MaxInt64 {
			return configNodeScalar, configValueInt, int64(v), nil, nil, nil
		}
	case float32:
		return configNodeScalar, configValueFloat, nil, float64(v), nil, nil
	case float64:
		return configNodeScalar, configValueFloat, nil, v, nil, nil
	case string:
		return configNodeScalar, configValueString, nil, nil, v, nil
	case bool:
		if v {
			return configNodeScalar, configValueBool, nil, nil, nil, int64(1)
		}
		return configNodeScalar, configValueBool, nil, nil, nil, int64(0)
	case nil:
		return configNodeScalar, configValueNull, nil, nil, nil, nil
	}
	return configNodeScalar, configValueString, nil, nil, fmt.Sprint(value), nil
}

// CustomSkins.yaml is an overlay (version/tables/rows), not a datatable
// [id, object] array. Apply each row without deleting unrelated rows, matching
// cmd/publish-custom-skins semantics.
func publishCustomSkinsRuntime(ctx context.Context, db *sql.DB, cache *RedisCache, value interface{}, publisher, note, source string) error {
	root, ok := value.(map[string]interface{})
	if !ok {
		return fmt.Errorf("CustomSkins root must be an object")
	}
	version, err := runtimeNumberInt(root["version"])
	if err != nil || version != 1 {
		return fmt.Errorf("CustomSkins version must be 1")
	}
	tables, ok := root["tables"].(map[string]interface{})
	if !ok || len(tables) == 0 {
		return fmt.Errorf("CustomSkins tables must be a non-empty object")
	}
	writeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, err := db.BeginTx(writeCtx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	names := make([]string, 0, len(tables))
	for name, rawRows := range tables {
		rows, ok := rawRows.([]interface{})
		if !ok || len(rows) == 0 {
			return fmt.Errorf("CustomSkins table %s must be a non-empty array", name)
		}
		names = append(names, name)
		for index, rawRow := range rows {
			row, ok := rawRow.(map[string]interface{})
			if !ok {
				return fmt.Errorf("CustomSkins table %s row %d must be an object", name, index)
			}
			id, err := runtimeNumberInt(row["id"])
			if err != nil || id <= 0 {
				return fmt.Errorf("CustomSkins table %s row %d has invalid id", name, index)
			}
			fields, ok := row["fields"].(map[string]interface{})
			if !ok || len(fields) == 0 {
				return fmt.Errorf("CustomSkins table %s row %d fields must be a non-empty object", name, index)
			}
			var rootID int64
			err = tx.QueryRowContext(writeCtx, `SELECT node_id FROM game_config_nodes WHERE config_name = ? AND parent_id = 0 AND field_name = '' AND array_index = ?`, name, id).Scan(&rootID)
			if err != nil && err != sql.ErrNoRows {
				return err
			}
			if err == nil {
				if err := deleteRuntimeConfigNodeTree(writeCtx, tx, rootID); err != nil {
					return err
				}
			}
			if _, err := insertRuntimeConfigNode(writeCtx, tx, name, 0, "", int(id), fields); err != nil {
				return err
			}
		}
	}
	sort.Strings(names)
	result, err := tx.ExecContext(writeCtx, `INSERT INTO game_config_revisions (published_at, publisher, note) VALUES (?, ?, ?)`, time.Now().Unix(), publisher, note)
	if err != nil {
		return err
	}
	revision, err := result.LastInsertId()
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if cache != nil {
		if err := cache.PublishConfigReload(revision, names); err != nil {
			log.Printf("config watcher Redis notification failed names=%v revision=%d: %v", names, revision, err)
		}
	}
	log.Printf("config watcher published CustomSkins revision=%d source=%s tables=%v", revision, source, names)
	return nil
}

func deleteRuntimeConfigNodeTree(ctx context.Context, tx *sql.Tx, nodeID int64) error {
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
		if err := deleteRuntimeConfigNodeTree(ctx, tx, child); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM game_config_nodes WHERE node_id = ?`, nodeID)
	return err
}
