package main

import (
	"context"
	"database/sql"
)

func loadMainAttributes(ctx context.Context, db *sql.DB, playerID int64, location, slot int32) (map[int32]float64, error) {
	rows, err := db.QueryContext(ctx, `SELECT attribute_type, value FROM player_item_main_attributes WHERE player_id=? AND location=? AND slot_index=?`, playerID, location, slot)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	attributes := make(map[int32]float64)
	for rows.Next() {
		var key int32
		var value float64
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		attributes[key] = value
	}
	return attributes, rows.Err()
}
