package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"reflect"
	"strconv"
	"strings"
	"time"

	"mhqserver/internal/equipattributes"
	"mhqserver/internal/mysqlschema"
)

type equipment struct {
	player               int64
	location, slot, item int32
	attributes           map[int32]float64
}

func playerSet(text string) (map[int64]bool, error) {
	result := make(map[int64]bool)
	if text == "" {
		return result, nil
	}
	for _, part := range strings.Split(text, ",") {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid player ID %q", part)
		}
		result[id] = true
	}
	return result, nil
}

func swapVerified(attributes map[int32]float64, expected20, expected21 float64) (map[int32]float64, error) {
	value20, has20 := attributes[20]
	value21, has21 := attributes[21]
	if !has20 || !has21 {
		return nil, fmt.Errorf("historical pair missing")
	}
	if value20 == expected21 && value21 == expected20 {
		return attributes, nil
	}
	if value20 != expected20 || value21 != expected21 {
		return nil, fmt.Errorf("historical fingerprint mismatch: %v/%v", value20, value21)
	}
	result := make(map[int32]float64, len(attributes))
	for key, value := range attributes {
		result[key] = value
	}
	result[20], result[21] = value21, value20
	return result, nil
}

func main() {
	dsn := flag.String("mysql", mysqlschema.DefaultDSN(), "MySQL DSN")
	players := flag.String("normalize-players", "", "explicit cloned player IDs, comma separated")
	swapPlayers := flag.String("swap-players", "", "players with verified historical swapped pairs")
	swapItem := flag.Int("swap-item", 0, "verified historical template ID")
	expected20 := flag.Float64("expected-20", 0, "exact original key 20 value from historical evidence")
	expected21 := flag.Float64("expected-21", 0, "exact original key 21 value from historical evidence")
	execute := flag.Bool("execute", false, "apply after stopping game server and backing up database")
	flag.Parse()
	normalizeIDs, err := playerSet(*players)
	if err != nil {
		log.Fatal(err)
	}
	swapIDs, err := playerSet(*swapPlayers)
	if err != nil {
		log.Fatal(err)
	}
	if len(normalizeIDs)+len(swapIDs) == 0 {
		log.Fatal("explicit player IDs required")
	}
	if len(swapIDs) > 0 && (*swapItem <= 0 || *expected20 == *expected21) {
		log.Fatal("swap requires item and two distinct historical values")
	}
	db, err := sql.Open("mysql", *dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	catalog, err := equipattributes.LoadTemplates(ctx, db)
	if err != nil {
		log.Fatal(err)
	}
	if err := run(ctx, db, catalog, normalizeIDs, swapIDs, int32(*swapItem), *expected20, *expected21, *execute); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, db *sql.DB, catalog map[int32]equipattributes.Template, normalizeIDs, swapIDs map[int64]bool, swapItem int32, expected20, expected21 float64, execute bool) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var items []*equipment
	allIDs := make(map[int64]bool)
	for id := range normalizeIDs {
		allIDs[id] = true
	}
	for id := range swapIDs {
		allIDs[id] = true
	}
	for player := range allIDs {
		rows, err := tx.QueryContext(ctx, `SELECT item.location,item.slot_index,item.item_id,attribute.attribute_type,attribute.value
			FROM player_items item JOIN player_item_main_attributes attribute USING(player_id,location,slot_index)
			WHERE item.player_id=? AND item.item_type=1 ORDER BY item.location,item.slot_index,attribute.attribute_type FOR UPDATE`, player)
		if err != nil {
			return err
		}
		for rows.Next() {
			var location, slot, item, key int32
			var value float64
			if err := rows.Scan(&location, &slot, &item, &key, &value); err != nil {
				rows.Close()
				return err
			}
			if len(items) == 0 || items[len(items)-1].player != player || items[len(items)-1].location != location || items[len(items)-1].slot != slot {
				items = append(items, &equipment{player: player, location: location, slot: slot, item: item, attributes: make(map[int32]float64)})
			}
			items[len(items)-1].attributes[key] = value
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	changed := 0
	matched := make(map[int64]bool)
	for _, item := range items {
		after := item.attributes
		if swapIDs[item.player] && item.item == swapItem {
			matched[item.player] = true
			after, err = swapVerified(after, expected20, expected21)
			if err != nil {
				return fmt.Errorf("player=%d item=%d: %w", item.player, item.item, err)
			}
		}
		if normalizeIDs[item.player] {
			template, ok := catalog[item.item]
			if !ok {
				return fmt.Errorf("missing template %d", item.item)
			}
			unsupported := false
			for key := range after {
				if template.Base[key] == 0 && key != template.SpecialKey {
					unsupported = true
				}
			}
			if unsupported {
				after = equipattributes.Normalize(after, template)
			}
		}
		if reflect.DeepEqual(after, item.attributes) {
			continue
		}
		changed++
		log.Printf("equipment repair player=%d location=%d slot=%d item=%d before=%v after=%v", item.player, item.location, item.slot, item.item, item.attributes, after)
		if execute {
			if _, err := tx.ExecContext(ctx, `DELETE FROM player_item_main_attributes WHERE player_id=? AND location=? AND slot_index=?`, item.player, item.location, item.slot); err != nil {
				return err
			}
			if err := equipattributes.Insert(ctx, tx, item.player, item.location, item.slot, after); err != nil {
				return err
			}
		}
	}
	for player := range swapIDs {
		if !matched[player] {
			return fmt.Errorf("verified item %d not found for player %d", swapItem, player)
		}
	}
	if execute {
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	log.Printf("equipment repair execute=%v changed_items=%d", execute, changed)
	return nil
}
