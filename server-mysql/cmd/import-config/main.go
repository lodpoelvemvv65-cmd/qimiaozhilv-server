package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	"mhqserver/internal/mysqlschema"
)

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

var datatableNames = []string{
	"MainStory", "MonsterBase", "MapMonsterConfig", "SkillConfig", "SkillGroupBase", "TaskBase",
	"NPCBase", "SkillLearn", "RoleGrowth", "CharacterGrowth", "EquipBase", "Strengthentable",
	"StrengthPlusConfig", "ItemUpgrade", "EquipAffixConfig", "ManulEquip", "ManulEquipAttribute", "SuitConfig", "GoodsBase",
	"CopyConfig", "ShopBase", "MarketBase", "MultiShop", "PetConfig", "PetLevelConfig",
	"PetExploreConfig", "SpaceTravelConfig", "ActivePerDayConfig", "StarSoulCopyConfig",
	"WorldBossConfig", "JourneyOfDeathCopyConfig", "TrialCopy", "BossBase", "ManulEquipMonsterConfig",
	"TransmigrationAddConfig", "SignInRewardConfig", "SignInRewardMonth", "SkinBase", "SceneTransConfig",
	"MaterialBase", "GemPriceConfig", "GemInlayConfig", "FamilyBossConfig", "Parentset", "SonSet",
	"EffectConfig", "EquipForge", "StarSoulTypeConfig", "StarSoulAttributeConfig",
	"StarSoulEquipAttributeTypeConfig", "StarSoulLevelConfig", "QuestConfig",
}

func main() {
	tableDir := flag.String("table", "../datatable_yaml", "YAML datatable source directory")
	skillPath := flag.String("skill-logic", "", "SkillLogicConfig YAML source")
	dsn := flag.String("mysql", mysqlschema.DefaultDSN(), "target MySQL DSN")
	only := flag.String("only", "", "comma-separated datatables to replace without importing other configuration")
	flag.Parse()

	db, description, err := mysqlschema.Open(*dsn)
	if err != nil {
		log.Fatalf("open MySQL: %v", err)
	}
	defer db.Close()
	log.Printf("target MySQL ready: %s", description)

	names := datatableNames
	if strings.TrimSpace(*only) != "" {
		names = nil
		for _, name := range strings.Split(*only, ",") {
			name = strings.TrimSpace(name)
			if name != "" {
				names = append(names, name)
			}
		}
		if len(names) == 0 {
			log.Fatal("-only did not contain a datatable name")
		}
	}
	imported := make(map[string]bool, len(names))
	for _, name := range names {
		path, err := sourcePath(*tableDir, name)
		if err != nil {
			log.Fatal(err)
		}
		value, err := decodeSourceFile(path)
		if err != nil {
			log.Fatalf("decode %s: %v", path, err)
		}
		rows, ok := value.([]interface{})
		if !ok {
			log.Fatalf("datatable %s must be an array of [id, object] pairs", path)
		}
		if err := replaceDatatable(db, name, rows); err != nil {
			log.Fatalf("import %s: %v", name, err)
		}
		imported[name] = true
		log.Printf("imported %s rows=%d", name, len(rows))
	}
	if strings.TrimSpace(*only) != "" {
		return
	}
	// Keep every online table editable in MySQL, including tables that are
	// not currently consumed by a gameplay subsystem. The runtime only loads
	// the subset declared in config_store.go, while the importer deliberately
	// imports all array-of-[id,value] sources so an admin can enable future
	// features without another storage migration.
	entries, err := os.ReadDir(*tableDir)
	if err != nil {
		log.Fatalf("scan datatable directory: %v", err)
	}
	extraNames := make(map[string]struct{})
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		extraNames[name] = struct{}{}
	}
	extraList := make([]string, 0, len(extraNames))
	for name := range extraNames {
		if !imported[name] {
			extraList = append(extraList, name)
		}
	}
	sort.Strings(extraList)
	for _, name := range extraList {
		path, err := sourcePath(*tableDir, name)
		if err != nil {
			log.Fatalf("find extra %s: %v", name, err)
		}
		value, err := decodeSourceFile(path)
		if err != nil {
			log.Fatalf("decode %s: %v", path, err)
		}
		rows, ok := value.([]interface{})
		if !ok {
			log.Printf("skip %s: not an array datatable", path)
			continue
		}
		if err := replaceDatatable(db, name, rows); err != nil {
			log.Fatalf("import %s: %v", name, err)
		}
		imported[name] = true
		log.Printf("imported extra %s rows=%d", name, len(rows))
	}

	if *skillPath == "" {
		*skillPath = filepath.Join(*tableDir, "SkillLogicConfig")
	}
	if path, err := firstExisting(*skillPath, *skillPath+".yaml", *skillPath+".yml"); err == nil {
		value, err := decodeSourceFile(path)
		if err != nil {
			log.Fatalf("decode SkillLogicConfig: %v", err)
		}
		if err := replaceTree(db, "SkillLogicConfig", value, "", -1, true); err != nil {
			log.Fatalf("import SkillLogicConfig: %v", err)
		}
		log.Printf("imported SkillLogicConfig from %s", path)
	} else {
		log.Fatalf("SkillLogicConfig source not found: %s", *skillPath)
	}
}

func sourcePath(dir, name string) (string, error) {
	return firstExisting(
		filepath.Join(dir, name+".yaml"),
		filepath.Join(dir, name+".yml"),
	)
}

func firstExisting(paths ...string) (string, error) {
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", fmt.Errorf("none of the source files exist: %s", strings.Join(paths, ", "))
}

func decodeSourceFile(path string) (interface{}, error) {
	ext := strings.ToLower(filepath.Ext(path))
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var value interface{}
	switch ext {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("source must be YAML or YML: %s", path)
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

func replaceDatatable(db *sql.DB, name string, rows []interface{}) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM game_config_nodes WHERE config_name = ?`, name); err != nil {
		return err
	}
	for _, raw := range rows {
		pair, ok := raw.([]interface{})
		if !ok || len(pair) < 2 {
			continue
		}
		id, err := numberInt(pair[0])
		if err != nil {
			return fmt.Errorf("invalid key: %w", err)
		}
		if _, err := insertNode(tx, name, 0, "", int(id), pair[1]); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func replaceTree(db *sql.DB, name string, value interface{}, field string, index int, root bool) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM game_config_nodes WHERE config_name = ?`, name); err != nil {
		return err
	}
	if _, err := insertNode(tx, name, 0, field, index, value); err != nil {
		return err
	}
	return tx.Commit()
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

func classify(value interface{}) (kind, valueType int, intValue interface{}, floatValue interface{}, stringValue interface{}, boolValue interface{}) {
	switch v := value.(type) {
	case map[string]interface{}:
		return nodeObject, valueNull, nil, nil, nil, nil
	case []interface{}:
		return nodeArray, valueNull, nil, nil, nil, nil
	case int:
		return nodeScalar, valueInt, int64(v), nil, nil, nil
	case int8:
		return nodeScalar, valueInt, int64(v), nil, nil, nil
	case int16:
		return nodeScalar, valueInt, int64(v), nil, nil, nil
	case int32:
		return nodeScalar, valueInt, int64(v), nil, nil, nil
	case int64:
		return nodeScalar, valueInt, v, nil, nil, nil
	case uint:
		return nodeScalar, valueInt, int64(v), nil, nil, nil
	case uint8:
		return nodeScalar, valueInt, int64(v), nil, nil, nil
	case uint16:
		return nodeScalar, valueInt, int64(v), nil, nil, nil
	case uint32:
		return nodeScalar, valueInt, int64(v), nil, nil, nil
	case uint64:
		return nodeScalar, valueInt, int64(v), nil, nil, nil
	case float32:
		return nodeScalar, valueFloat, nil, float64(v), nil, nil
	case float64:
		return nodeScalar, valueFloat, nil, v, nil, nil
	case string:
		return nodeScalar, valueString, nil, nil, v, nil
	case bool:
		if v {
			return nodeScalar, valueBool, nil, nil, nil, 1
		}
		return nodeScalar, valueBool, nil, nil, nil, 0
	case nil:
		return nodeScalar, valueNull, nil, nil, nil, nil
	}
	return nodeScalar, valueString, nil, nil, fmt.Sprint(value), nil
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
	return 0, fmt.Errorf("%T is not an integer", value)
}
