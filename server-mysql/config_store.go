package main

// config_store.go contains the runtime configuration reader.  Game data is
// imported into MySQL as a typed tree; the server never reads configuration
// files at runtime. A tree keeps nested arrays and objects editable by an
// administration tool without opaque document columns.

import (
	"database/sql"
	"fmt"
	"sort"
)

const (
	configNodeObject = 1
	configNodeArray  = 2
	configNodeScalar = 3

	configValueNull   = 0
	configValueInt    = 1
	configValueFloat  = 2
	configValueString = 3
	configValueBool   = 4
)

type configNodeRow struct {
	id          int64
	parentID    int64
	fieldName   string
	arrayIndex  int
	kind        int
	valueType   int
	intValue    sql.NullInt64
	floatValue  sql.NullFloat64
	stringValue sql.NullString
	boolValue   sql.NullInt64
}

// loadConfigTableFromDB reconstructs one legacy [id, object] datatable from
// the relational configuration tree.
func loadConfigTableFromDB(db *sql.DB, name string) (map[int64]map[string]interface{}, error) {
	rows, err := db.Query(`SELECT node_id, parent_id, field_name, array_index,
		node_kind, value_type, int_value, float_value, string_value, bool_value
		FROM game_config_nodes WHERE config_name = ? ORDER BY node_id`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := make(map[int64]*configNodeRow)
	children := make(map[int64][]*configNodeRow)
	for rows.Next() {
		n := &configNodeRow{}
		if err := rows.Scan(&n.id, &n.parentID, &n.fieldName, &n.arrayIndex,
			&n.kind, &n.valueType, &n.intValue, &n.floatValue, &n.stringValue, &n.boolValue); err != nil {
			return nil, err
		}
		nodes[n.id] = n
		children[n.parentID] = append(children[n.parentID], n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, list := range children {
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].arrayIndex != list[j].arrayIndex {
				return list[i].arrayIndex < list[j].arrayIndex
			}
			return list[i].fieldName < list[j].fieldName
		})
	}
	out := make(map[int64]map[string]interface{})
	for _, root := range children[0] {
		if root.kind != configNodeObject || root.arrayIndex < 0 {
			continue
		}
		value, err := buildConfigNode(root, children)
		if err != nil {
			return nil, fmt.Errorf("config %s id %d: %w", name, root.arrayIndex, err)
		}
		object, ok := value.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("config %s id %d is not an object", name, root.arrayIndex)
		}
		out[int64(root.arrayIndex)] = object
	}
	return out, nil
}

func buildConfigRootFromDB(db *sql.DB, name string) (interface{}, error) {
	rows, err := db.Query(`SELECT node_id, parent_id, field_name, array_index,
		node_kind, value_type, int_value, float_value, string_value, bool_value
		FROM game_config_nodes WHERE config_name = ? ORDER BY node_id`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	children := make(map[int64][]*configNodeRow)
	for rows.Next() {
		n := &configNodeRow{}
		if err := rows.Scan(&n.id, &n.parentID, &n.fieldName, &n.arrayIndex,
			&n.kind, &n.valueType, &n.intValue, &n.floatValue, &n.stringValue, &n.boolValue); err != nil {
			return nil, err
		}
		children[n.parentID] = append(children[n.parentID], n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	roots := children[0]
	if len(roots) != 1 {
		return nil, fmt.Errorf("config %s has %d roots", name, len(roots))
	}
	return buildConfigNode(roots[0], children)
}

func buildConfigNode(node *configNodeRow, children map[int64][]*configNodeRow) (interface{}, error) {
	if node == nil {
		return nil, nil
	}
	switch node.kind {
	case configNodeObject:
		out := make(map[string]interface{})
		for _, child := range children[node.id] {
			value, err := buildConfigNode(child, children)
			if err != nil {
				return nil, err
			}
			out[child.fieldName] = value
		}
		return out, nil
	case configNodeArray:
		list := make([]interface{}, 0, len(children[node.id]))
		for _, child := range children[node.id] {
			for len(list) <= child.arrayIndex {
				list = append(list, nil)
			}
			value, err := buildConfigNode(child, children)
			if err != nil {
				return nil, err
			}
			list[child.arrayIndex] = value
		}
		return list, nil
	case configNodeScalar:
		switch node.valueType {
		case configValueInt:
			if node.intValue.Valid {
				return node.intValue.Int64, nil
			}
		case configValueFloat:
			if node.floatValue.Valid {
				return node.floatValue.Float64, nil
			}
		case configValueString:
			if node.stringValue.Valid {
				return node.stringValue.String, nil
			}
		case configValueBool:
			return node.boolValue.Valid && node.boolValue.Int64 != 0, nil
		case configValueNull:
			return nil, nil
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown node kind %d", node.kind)
	}
}

func buildDatatablesFromDB(db *sql.DB) (*datatables, error) {
	t := &datatables{}
	for _, p := range []struct {
		name string
		dst  *map[int64]map[string]interface{}
	}{
		{"MainStory", &t.mainStory},
		{"MainStoryExp", &t.mainStoryExp},
		{"MonsterBase", &t.monsterBase},
		{"MapMonsterConfig", &t.mapMonsterConfig},
		{"SkillConfig", &t.skillConfig},
		{"SkillGroupBase", &t.skillGroup},
		{"TaskBase", &t.taskBase},
		{"NPCBase", &t.npcBase},
		{"SkillLearn", &t.skillLearn},
		{"RoleGrowth", &t.roleGrowth},
		{"CharacterGrowth", &t.characterGrowth},
		{"EquipBase", &t.equipBase},
		{"Strengthentable", &t.strengthen},
		{"StrengthPlusConfig", &t.strengthPlus},
		{"ItemUpgrade", &t.itemUpgrade},
		{"EquipAffixConfig", &t.equipAffix},
		{"ManulEquip", &t.manulEquip},
		{"ManulEquipAttribute", &t.manulEquipAttribute},
		{"SuitConfig", &t.suitConfig},
		{"GoodsBase", &t.goodsBase},
		{"CopyConfig", &t.copyConfig},
		{"ShopBase", &t.shopBase},
		{"MarketBase", &t.marketBase},
		{"MultiShop", &t.multiShop},
		{"PetConfig", &t.petConfig},
		{"PetLevelConfig", &t.petLevelConfig},
		{"PetExploreConfig", &t.petExploreConfig},
		{"SpaceTravelConfig", &t.spaceTravelConfig},
		{"ActivePerDayConfig", &t.activePerDay},
		{"StarSoulCopyConfig", &t.starSoulCopy},
		{"WorldBossConfig", &t.worldBossConfig},
		{"JourneyOfDeathCopyConfig", &t.journeyOfDeathConfig},
		{"TrialCopy", &t.trialCopy},
		{"BossBase", &t.bossBase},
		{"ManulEquipMonsterConfig", &t.manulEquipMonsterConfig},
		{"TransmigrationAddConfig", &t.transmigrationAdd},
		{"SignInRewardConfig", &t.signInReward},
		{"SignInRewardMonth", &t.signInRewardMonth},
		{"SkinBase", &t.skinBase},
		{"SceneTransConfig", &t.sceneTrans},
		{"MaterialBase", &t.materialBase},
		{"GemPriceConfig", &t.gemPrice},
		{"GemInlayConfig", &t.gemInlay},
		{"FamilyBossConfig", &t.familyBossConfig},
		{"Parentset", &t.parentset},
		{"SonSet", &t.sonSet},
		{"EffectConfig", &t.effectConfig},
		{"EquipForge", &t.equipForge},
		{"StarSoulTypeConfig", &t.starSoulType},
		{"StarSoulAttributeConfig", &t.starSoulAttribute},
		{"StarSoulEquipAttributeTypeConfig", &t.starSoulEquipAttr},
		{"StarSoulLevelConfig", &t.starSoulLevel},
		{"QuestConfig", &t.questConfig},
	} {
		m, err := loadConfigTableFromDB(db, p.name)
		if err != nil {
			return nil, fmt.Errorf("load MySQL config %s: %w", p.name, err)
		}
		if len(m) == 0 {
			return nil, fmt.Errorf("MySQL config %s is empty; run cmd/import-config", p.name)
		}
		*p.dst = m
	}
	return t, nil
}

func loadDatatablesFromDB(db *sql.DB) error {
	t, err := buildDatatablesFromDB(db)
	if err != nil {
		return err
	}
	tables = t
	return nil
}
