package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"mhqserver/internal/mysqlschema"
)

const (
	bagLocation = 1
	bagSlots    = 48
)

func main() {
	account := flag.String("account", "", "target account")
	itemsArg := flag.String("items", "130001,130002", "comma-separated EquipBase item IDs")
	dsn := flag.String("mysql", mysqlschema.DefaultDSN(), "MySQL DSN")
	flag.Parse()
	if strings.TrimSpace(*account) == "" {
		log.Fatal("-account is required")
	}
	itemIDs, err := parseIDs(*itemsArg)
	if err != nil || len(itemIDs) == 0 {
		log.Fatalf("invalid -items: %v", err)
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

	var playerID int64
	if err := tx.QueryRowContext(ctx, `SELECT p.id FROM accounts a JOIN players p ON p.account_id = a.id WHERE a.account = ? ORDER BY p.id LIMIT 1`, *account).Scan(&playerID); err != nil {
		log.Fatalf("find account %q: %v", *account, err)
	}
	var nextServerID int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(server_id), 0) FROM player_items FOR UPDATE`).Scan(&nextServerID); err != nil {
		log.Fatalf("read server id sequence: %v", err)
	}
	used := make(map[int32]bool)
	rows, err := tx.QueryContext(ctx, `SELECT slot_index FROM player_items WHERE player_id = ? AND location = ?`, playerID, bagLocation)
	if err != nil {
		log.Fatalf("read bag: %v", err)
	}
	for rows.Next() {
		var slot int32
		if err := rows.Scan(&slot); err != nil {
			rows.Close()
			log.Fatal(err)
		}
		used[slot] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		log.Fatalf("read bag: %v", err)
	}
	rows.Close()

	granted := 0
	for _, itemID := range itemIDs {
		var configCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM game_config_nodes WHERE config_name = 'EquipBase' AND parent_id = 0 AND field_name = '' AND array_index = ?`, itemID).Scan(&configCount); err != nil {
			log.Fatalf("verify EquipBase[%d]: %v", itemID, err)
		}
		if configCount != 1 {
			log.Fatalf("EquipBase[%d] is not present in MySQL", itemID)
		}
		var existing int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM player_items WHERE player_id = ? AND item_id = ?`, playerID, itemID).Scan(&existing); err != nil {
			log.Fatalf("check existing item %d: %v", itemID, err)
		}
		if existing > 0 {
			fmt.Printf("skip existing item=%d\n", itemID)
			continue
		}
		slot := nextFreeSlot(used)
		if slot < 0 {
			log.Fatal("bag is full")
		}
		used[slot] = true
		nextServerID++
		if _, err := tx.ExecContext(ctx, `INSERT INTO player_items
			(player_id, location, slot_index, item_id, item_type, server_id, item_count,
			 is_locked, quality, star, strength_level, special_key, special_id, get_source)
			VALUES (?, ?, ?, ?, 1, ?, 1, 0, 4, 6, 0, 9, 0, ?)`,
			playerID, bagLocation, slot, itemID, nextServerID, "custom-skin-grant"); err != nil {
			log.Fatalf("grant item %d: %v", itemID, err)
		}
		granted++
		fmt.Printf("granted item=%d player_id=%d slot=%d server_id=%d\n", itemID, playerID, slot, nextServerID)
	}
	if err := tx.Commit(); err != nil {
		log.Fatalf("commit: %v", err)
	}
	fmt.Printf("MySQL=%s account=%s granted=%d\n", description, *account, granted)
}

func parseIDs(value string) ([]int32, error) {
	var out []int32
	seen := make(map[int32]bool)
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.ParseInt(part, 10, 32)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("invalid item id %q", part)
		}
		id := int32(n)
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

func nextFreeSlot(used map[int32]bool) int32 {
	for slot := int32(0); slot < bagSlots; slot++ {
		if !used[slot] {
			return slot
		}
	}
	return -1
}
