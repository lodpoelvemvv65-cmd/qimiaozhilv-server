package main

import (
	"database/sql"
	"fmt"
	"log"
)

type equipmentAttributeMove struct {
	from int32
	to   int32
}

func legacyEquipmentAttributeMoves(item *bagItem, catalog *datatables) []equipmentAttributeMove {
	if item == nil || item.ItemType != 1 || catalog == nil || len(item.MainAttr) == 0 {
		return nil
	}
	row := catalog.equipBase[int64(item.ItemId)]
	if row == nil {
		return nil
	}
	var moves []equipmentAttributeMove
	for _, pair := range []struct {
		first, second           int32
		firstField, secondField string
	}{
		{20, 21, "Phy", "Sta"},
		{22, 30, "Nphyi", "PhyDA"},
		{23, 31, "Nmeni", "MicDA"},
	} {
		firstSupported := numf(row[pair.firstField]) != 0 || int32(num(row["SpecialKey"])) == pair.first
		secondSupported := numf(row[pair.secondField]) != 0 || int32(num(row["SpecialKey"])) == pair.second
		_, firstStored := item.MainAttr[pair.first]
		_, secondStored := item.MainAttr[pair.second]
		if !firstSupported && secondSupported && firstStored && !secondStored {
			moves = append(moves, equipmentAttributeMove{from: pair.first, to: pair.second})
		} else if firstSupported && !secondSupported && !firstStored && secondStored {
			moves = append(moves, equipmentAttributeMove{from: pair.second, to: pair.first})
		}
	}
	return moves
}

func repairLegacyEquipmentAttributes(item *bagItem) bool {
	moves := legacyEquipmentAttributeMoves(item, tables)
	for _, move := range moves {
		value := item.MainAttr[move.from]
		item.MainAttr[move.to] = value
		delete(item.MainAttr, move.from)
		log.Printf("[EQUIP-ATTR-MIGRATION] item=%d instance=%d key=%d->%d delta=%.9g", item.ItemId, item.ServerId, move.from, move.to, value)
	}
	return len(moves) > 0
}

func migrateLegacyEquipmentAttributes(db *sql.DB, catalog *datatables) (int, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT item.player_id, item.location, item.slot_index, item.item_id,
		attribute.attribute_type, attribute.value
		FROM player_items AS item JOIN player_item_main_attributes AS attribute
		ON attribute.player_id = item.player_id AND attribute.location = item.location AND attribute.slot_index = item.slot_index
		WHERE item.item_type = 1 AND attribute.attribute_type IN (20, 21, 22, 23, 30, 31)
		ORDER BY item.player_id, item.location, item.slot_index, attribute.attribute_type`)
	if err != nil {
		return 0, err
	}
	type position struct {
		playerID int64
		location int32
		slot     int32
	}
	type snapshot struct {
		position position
		item     bagItem
	}
	var items []*snapshot
	for rows.Next() {
		var current position
		var itemID, key int32
		var value float32
		if err := rows.Scan(&current.playerID, &current.location, &current.slot, &itemID, &key, &value); err != nil {
			rows.Close()
			return 0, err
		}
		if len(items) == 0 || items[len(items)-1].position != current {
			items = append(items, &snapshot{position: current, item: bagItem{ItemId: itemID, ItemType: 1, MainAttr: make(map[int32]float32)}})
		}
		items[len(items)-1].item.MainAttr[key] = value
	}
	if err := closeRows(rows); err != nil {
		return 0, err
	}
	var audit []string
	for _, item := range items {
		for _, move := range legacyEquipmentAttributeMoves(&item.item, catalog) {
			result, err := tx.Exec(`UPDATE player_item_main_attributes SET attribute_type = ?
				WHERE player_id = ? AND location = ? AND slot_index = ? AND attribute_type = ?`,
				move.to, item.position.playerID, item.position.location, item.position.slot, move.from)
			if err != nil {
				return 0, err
			}
			count, err := result.RowsAffected()
			if err != nil || count != 1 {
				return 0, fmt.Errorf("equipment attribute migration affected %d rows: %v", count, err)
			}
			audit = append(audit, fmt.Sprintf("player=%d location=%d slot=%d item=%d key=%d->%d delta=%.9g",
				item.position.playerID, item.position.location, item.position.slot, item.item.ItemId, move.from, move.to, item.item.MainAttr[move.from]))
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	for _, entry := range audit {
		log.Printf("[EQUIP-ATTR-MIGRATION] %s", entry)
	}
	return len(audit), nil
}
