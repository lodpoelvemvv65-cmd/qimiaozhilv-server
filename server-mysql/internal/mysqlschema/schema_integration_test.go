package mysqlschema

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func TestSchemaIntegration(t *testing.T) {
	dsn := os.Getenv("MHQ_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("MHQ_TEST_MYSQL_DSN is not set")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DBName = fmt.Sprintf("mhq_test_schema_%d", os.Getpid())
	dropTestDatabase(t, cfg)
	t.Cleanup(func() { dropTestDatabase(t, cfg) })

	db, _, err := Open(cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var tableCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME IN
		('accounts', 'players', 'families', 'world_boss_states',
		 'family_boss_states', 'family_boss_reward_claims', 'consignment_items', 'server_meta')`).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 8 {
		t.Fatalf("created %d persistent tables, want 8", tableCount)
	}
	var purchaseOriginTables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME IN
		('player_item_purchase_origins', 'player_mail_item_purchase_origins', 'consignment_item_purchase_origins')`).Scan(&purchaseOriginTables); err != nil {
		t.Fatal(err)
	}
	if purchaseOriginTables != 3 {
		t.Fatalf("created %d purchase-origin tables, want 3", purchaseOriginTables)
	}
	var participationColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'family_boss_damage'
		AND COLUMN_NAME IN ('treat', 'participated')`).Scan(&participationColumns); err != nil {
		t.Fatal(err)
	}
	if participationColumns != 2 {
		t.Fatalf("family boss participation columns = %d, want 2", participationColumns)
	}
	var itemBuffCapacityType string
	if err := db.QueryRow(`SELECT DATA_TYPE FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'player_item_buffs' AND COLUMN_NAME = 'capacity'`).Scan(&itemBuffCapacityType); err != nil {
		t.Fatal(err)
	}
	if itemBuffCapacityType != "bigint" {
		t.Fatalf("player item buff capacity type = %q, want bigint", itemBuffCapacityType)
	}
	// Exercise the in-place upgrade used by existing local/remote databases,
	// not only the fresh-table DDL above.
	if _, err := db.Exec(`ALTER TABLE player_item_buffs MODIFY COLUMN capacity INT NOT NULL`); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(context.Background(), db); err != nil {
		t.Fatalf("upgrade legacy item buff capacity: %v", err)
	}
	if err := db.QueryRow(`SELECT DATA_TYPE FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'player_item_buffs' AND COLUMN_NAME = 'capacity'`).Scan(&itemBuffCapacityType); err != nil {
		t.Fatal(err)
	}
	if itemBuffCapacityType != "bigint" {
		t.Fatalf("upgraded player item buff capacity type = %q, want bigint", itemBuffCapacityType)
	}
	var configTables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'game_config_nodes'`).Scan(&configTables); err != nil {
		t.Fatal(err)
	}
	if configTables != 1 {
		t.Fatal("game_config_nodes table was not created")
	}
	var nameClaimTables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'player_name_claims'`).Scan(&nameClaimTables); err != nil {
		t.Fatal(err)
	}
	if nameClaimTables != 1 {
		t.Fatal("player_name_claims table was not created")
	}
	var reservationTables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'player_id_reservations'`).Scan(&reservationTables); err != nil {
		t.Fatal(err)
	}
	if reservationTables != 1 {
		t.Fatal("player_id_reservations table was not created")
	}

	result, err := db.Exec(`INSERT INTO accounts (account, password, create_time) VALUES (?, ?, ?)`, "测试账号", "secret", 1)
	if err != nil {
		t.Fatal(err)
	}
	accountID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO players (account_id, name, job_id, skin_id, store_pages)
		VALUES (?, ?, ?, ?, 2)`, accountID, "迁移角色", 1, 1); err != nil {
		t.Fatal(err)
	}
	var level, titleID int
	if err := db.QueryRow(`SELECT level, title_id FROM players WHERE account_id = ?`, accountID).
		Scan(&level, &titleID); err != nil {
		t.Fatal(err)
	}
	if level != 1 || titleID != 0 {
		t.Fatalf("unexpected MySQL defaults: level=%d title=%d", level, titleID)
	}
	if _, err := db.Exec(`ALTER TABLE players MODIFY COLUMN title_id INT NOT NULL DEFAULT 120889`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE players SET title_id = 120889 WHERE account_id = ?`, accountID); err != nil {
		t.Fatal(err)
	}
	legacyResult, err := db.Exec(`INSERT INTO accounts (account, password, create_time) VALUES (?, ?, ?)`, "无称号旧账号", "secret", 1)
	if err != nil {
		t.Fatal(err)
	}
	legacyAccountID, err := legacyResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO players (account_id, name, job_id, skin_id, store_pages)
		VALUES (?, ?, ?, ?, 2)`, legacyAccountID, "无称号旧角色", 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO player_items
		(player_id, location, slot_index, item_id, item_type, get_source)
		VALUES (?, 2, 4, 120888, 1, 'legacy title')`, accountID); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var reservedPlayerID int64
	if err := db.QueryRow(`SELECT player_id FROM player_id_reservations WHERE account_id = ?`, accountID).
		Scan(&reservedPlayerID); err != nil {
		t.Fatal(err)
	}
	var migratedPlayerID int64
	if err := db.QueryRow(`SELECT id FROM players WHERE account_id = ?`, accountID).Scan(&migratedPlayerID); err != nil {
		t.Fatal(err)
	}
	if reservedPlayerID != migratedPlayerID {
		t.Fatalf("seeded reservation=%d, want existing player=%d", reservedPlayerID, migratedPlayerID)
	}
	if err := db.QueryRow(`SELECT title_id FROM players WHERE account_id = ?`, accountID).
		Scan(&titleID); err != nil {
		t.Fatal(err)
	}
	if titleID != 120888 {
		t.Fatalf("legacy worn title was not restored: title=%d", titleID)
	}
	if err := db.QueryRow(`SELECT title_id FROM players WHERE account_id = ?`, legacyAccountID).Scan(&titleID); err != nil {
		t.Fatal(err)
	}
	if titleID != 0 {
		t.Fatalf("legacy unworn title was not cleared: title=%d", titleID)
	}
	var titleDefault sql.NullString
	if err := db.QueryRow(`SELECT COLUMN_DEFAULT FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'players' AND COLUMN_NAME = 'title_id'`).Scan(&titleDefault); err != nil {
		t.Fatal(err)
	}
	if !titleDefault.Valid || titleDefault.String != "0" {
		t.Fatalf("title_id default=%q, want 0", titleDefault.String)
	}
	var legacyColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME IN ('players', 'families', 'consignment_items')
		AND COLUMN_NAME IN ('bag_json', 'worn_json', 'starsoul_json', 'store_json', 'pet_json',
		'mails_json', 'friends_json', 'signin_json', 'mainui_json', 'item_buffs_json',
		'members_json', 'requests_json', 'gems')`).Scan(&legacyColumns); err != nil {
		t.Fatal(err)
	}
	if legacyColumns != 0 {
		t.Fatalf("legacy JSON columns still present: %d", legacyColumns)
	}
	var dungeonQuotaColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'player_activity'
		AND COLUMN_NAME IN ('dungeon_quota_day', 'space_travel_remaining',
		'death_tower_remaining', 'family_boss_keys')`).Scan(&dungeonQuotaColumns); err != nil {
		t.Fatal(err)
	}
	if dungeonQuotaColumns != 4 {
		t.Fatalf("player_activity dungeon quota columns = %d, want 4", dungeonQuotaColumns)
	}
	var personalPVPColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'player_activity'
		AND COLUMN_NAME = 'pvp_battle_day'`).Scan(&personalPVPColumns); err != nil {
		t.Fatal(err)
	}
	if personalPVPColumns != 1 {
		t.Fatalf("player_activity personal PVP columns = %d, want 1", personalPVPColumns)
	}
	var manualEquipColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'player_activity'
		AND COLUMN_NAME IN ('manual_equip_tier_1', 'manual_equip_tier_2',
		'manual_equip_tier_3')`).Scan(&manualEquipColumns); err != nil {
		t.Fatal(err)
	}
	if manualEquipColumns != 3 {
		t.Fatalf("player_activity manual equipment columns = %d, want 3", manualEquipColumns)
	}
	var onlineRewardColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'player_activity'
		AND COLUMN_NAME = 'online_reward_accumulated_ms'`).Scan(&onlineRewardColumns); err != nil {
		t.Fatal(err)
	}
	if onlineRewardColumns != 1 {
		t.Fatalf("player_activity online reward columns = %d, want 1", onlineRewardColumns)
	}
	var manualCraftPityColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'player_activity'
		AND COLUMN_NAME IN ('manual_rare_craft_count', 'manual_rare_pity_at',
		'manual_epic_craft_count', 'manual_epic_pity_at')`).Scan(&manualCraftPityColumns); err != nil {
		t.Fatal(err)
	}
	if manualCraftPityColumns != 4 {
		t.Fatalf("player_activity manual craft pity columns = %d, want 4", manualCraftPityColumns)
	}

	if _, err := db.Exec(`INSERT INTO family_boss_states
		(family_id, boss_id, hp, max_hp, has_reward) VALUES (9001, 1, 0, 1000, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE family_boss_states DROP COLUMN dead_at`); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var deadAtColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'family_boss_states'
		AND COLUMN_NAME = 'dead_at'`).Scan(&deadAtColumns); err != nil {
		t.Fatal(err)
	}
	if deadAtColumns != 1 {
		t.Fatalf("family_boss_states.dead_at columns = %d, want 1", deadAtColumns)
	}
	var familyBossDeadAt int64
	if err := db.QueryRow(`SELECT dead_at FROM family_boss_states WHERE family_id = 9001 AND boss_id = 1`).Scan(&familyBossDeadAt); err != nil {
		t.Fatal(err)
	}
	if familyBossDeadAt != 0 {
		t.Fatalf("migrated family boss dead_at=%d, want 0", familyBossDeadAt)
	}
	if _, err := db.Exec(`INSERT INTO family_boss_damage
		(family_id, boss_id, player_id, damage, treat, participated)
		VALUES (9001, 1, 7001, 900, 100, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var claimed bool
	if err := db.QueryRow(`SELECT claimed FROM family_boss_reward_claims
		WHERE family_id = 9001 AND boss_id = 1 AND player_id = 7001`).Scan(&claimed); err != nil || claimed {
		t.Fatalf("legacy family boss claim backfill claimed=%v err=%v", claimed, err)
	}
	if _, err := db.Exec(`UPDATE family_boss_reward_claims SET claimed = 1
		WHERE family_id = 9001 AND boss_id = 1 AND player_id = 7001`); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT claimed FROM family_boss_reward_claims
		WHERE family_id = 9001 AND boss_id = 1 AND player_id = 7001`).Scan(&claimed); err != nil || !claimed {
		t.Fatalf("consumed family boss claim was reopened claimed=%v err=%v", claimed, err)
	}

	if _, err := db.Exec(`INSERT INTO world_boss_states (layer, dead_at, killer) VALUES (?, ?, ?)
		ON DUPLICATE KEY UPDATE dead_at=VALUES(dead_at), killer=VALUES(killer)`, 1, 10, "甲"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO world_boss_states (layer, dead_at, killer) VALUES (?, ?, ?)
		ON DUPLICATE KEY UPDATE dead_at=VALUES(dead_at), killer=VALUES(killer)`, 1, 20, "乙"); err != nil {
		t.Fatal(err)
	}
	var deadAt int64
	var killer string
	if err := db.QueryRow(`SELECT dead_at, killer FROM world_boss_states WHERE layer = 1`).Scan(&deadAt, &killer); err != nil {
		t.Fatal(err)
	}
	if deadAt != 20 || killer != "乙" {
		t.Fatalf("upsert stored dead_at=%d killer=%q", deadAt, killer)
	}
}

func dropTestDatabase(t *testing.T, cfg *mysql.Config) {
	t.Helper()
	bootstrap := *cfg
	bootstrap.DBName = ""
	db, err := sql.Open("mysql", bootstrap.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, "DROP DATABASE IF EXISTS "+quoteIdentifier(cfg.DBName)); err != nil {
		t.Fatal(err)
	}
}
