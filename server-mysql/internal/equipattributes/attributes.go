package equipattributes

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

var Fields = map[string]int32{
	"Hp": 1, "Mp": 2, "Str": 3, "Quk": 4, "Spi": 5, "Wim": 6,
	"PhyAtk": 7, "SpiAtk": 8, "PhyDef": 9, "SpiDef": 10,
	"Pcrir": 11, "Mcrir": 12, "Pcri": 13, "Mcri": 14,
	"Rpcrir": 15, "Rmcrir": 16, "Rpcri": 17, "Rmcri": 18, "Dvo": 19,
	"Phy": 20, "Sta": 21, "Nphyi": 22, "Nmeni": 23, "Spd": 24,
	"Hit": 25, "Res": 26, "SuckR": 27, "SuckV": 28, "HpRecover": 29, "PhyDA": 30, "MicDA": 31,
}

type Template struct {
	Base         map[int32]float64
	SpecialKey   int32
	SpecialValue float64
}

func Normalize(attributes map[int32]float64, target Template) map[int32]float64 {
	result := make(map[int32]float64)
	for key, base := range target.Base {
		if base != 0 {
			result[key] = attributes[key]
		}
	}
	for _, pair := range [][2]int32{{7, 8}, {11, 12}, {13, 14}} {
		for _, direction := range [][2]int32{pair, {pair[1], pair[0]}} {
			from, to := direction[0], direction[1]
			value, exists := attributes[from]
			_, hasTarget := attributes[to]
			if exists && !hasTarget && target.Base[from] == 0 && target.Base[to] != 0 {
				result[to] = value
			}
		}
	}
	if len(result) == 0 && target.SpecialKey > 0 && target.SpecialValue != 0 {
		result[target.SpecialKey] = attributes[target.SpecialKey]
	}
	return result
}

func LoadTemplates(ctx context.Context, db *sql.DB) (map[int32]Template, error) {
	rows, err := db.QueryContext(ctx, `SELECT id.int_value, field.field_name, COALESCE(field.float_value, field.int_value, 0)
		FROM game_config_nodes id JOIN game_config_nodes field ON field.parent_id=id.parent_id AND field.config_name=id.config_name
		WHERE id.config_name='EquipBase' AND id.field_name='_id'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[int32]Template)
	for rows.Next() {
		var itemID int32
		var field string
		var value float64
		if err := rows.Scan(&itemID, &field, &value); err != nil {
			return nil, err
		}
		template := result[itemID]
		if template.Base == nil {
			template.Base = make(map[int32]float64)
		}
		if key, exists := Fields[field]; exists && value != 0 {
			template.Base[key] = value
		}
		if field == "SpecialKey" {
			template.SpecialKey = int32(value)
		}
		if field == "SpecialValue" {
			template.SpecialValue = value
		}
		result[itemID] = template
	}
	return result, rows.Err()
}

func Insert(ctx context.Context, tx *sql.Tx, playerID int64, location, slot int32, attributes map[int32]float64) error {
	keys := make([]int, 0, len(attributes))
	for key := range attributes {
		keys = append(keys, int(key))
	}
	sort.Ints(keys)
	for _, key := range keys {
		if _, err := tx.ExecContext(ctx, `INSERT INTO player_item_main_attributes
			(player_id, location, slot_index, attribute_type, value) VALUES (?, ?, ?, ?, ?)`, playerID, location, slot, key, attributes[int32(key)]); err != nil {
			return fmt.Errorf("insert equipment attribute player=%d location=%d slot=%d key=%d: %w", playerID, location, slot, key, err)
		}
	}
	return nil
}
