package mysqlschema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// The local deployment was provisioned with the credentials supplied for this
// server. Deployments should override it through MHQ_MYSQL_DSN instead of
// committing a different credential into source.
const fallbackDSN = "root:123456@tcp(127.0.0.1:3306)/mhq?charset=utf8mb4&collation=utf8mb4_bin&parseTime=true&loc=Local"

// DefaultDSN allows deployments to keep credentials out of command lines.
func DefaultDSN() string {
	if dsn := strings.TrimSpace(os.Getenv("MHQ_MYSQL_DSN")); dsn != "" {
		return dsn
	}
	return fallbackDSN
}

// Open connects to MySQL, creates the configured database when it does not
// exist, and applies the idempotent server schema.
func Open(dsn string) (*sql.DB, string, error) {
	cfg, err := mysql.ParseDSN(strings.TrimSpace(dsn))
	if err != nil {
		return nil, "", fmt.Errorf("parse MySQL DSN: %w", err)
	}
	if cfg.DBName == "" {
		return nil, "", errors.New("MySQL DSN must include a database name")
	}
	if cfg.Collation == "" {
		cfg.Collation = "utf8mb4_bin"
	}
	cfg.ParseTime = true
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}

	databaseName := cfg.DBName
	db, err := openConfigured(cfg)
	if err != nil {
		var mysqlErr *mysql.MySQLError
		if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1049 {
			return nil, "", err
		}
		if err := createDatabase(cfg, databaseName); err != nil {
			return nil, "", err
		}
		db, err = openConfigured(cfg)
		if err != nil {
			return nil, "", err
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := Ensure(ctx, db); err != nil {
		db.Close()
		return nil, "", fmt.Errorf("initialize MySQL schema: %w", err)
	}
	return db, fmt.Sprintf("%s(%s)/%s", cfg.Net, cfg.Addr, databaseName), nil
}

func openConfigured(cfg *mysql.Config) (*sql.DB, error) {
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(3 * time.Minute)
	db.SetConnMaxIdleTime(time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect MySQL: %w", err)
	}
	return db, nil
}

func createDatabase(cfg *mysql.Config, databaseName string) error {
	bootstrap := *cfg
	bootstrap.DBName = ""
	db, err := openConfigured(&bootstrap)
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	query := "CREATE DATABASE IF NOT EXISTS " + quoteIdentifier(databaseName) +
		" CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"
	if _, err := db.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("create MySQL database %q: %w", databaseName, err)
	}
	return nil
}

// Ensure creates every table and adds columns introduced by later server
// versions. All text uses a binary UTF-8 collation for case-sensitive account
// and character-name comparisons.
func Ensure(ctx context.Context, db *sql.DB) error {
	for _, ddl := range schemaDDL {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	for _, ddl := range gmSchemaDDL {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	for _, column := range playerColumns {
		if err := ensureColumn(ctx, db, "players", column.name, column.definition); err != nil {
			return err
		}
	}
	for _, column := range consignmentColumns {
		if err := ensureColumn(ctx, db, "consignment_items", column.name, column.definition); err != nil {
			return err
		}
	}
	for _, column := range playerActivityColumns {
		if err := ensureColumn(ctx, db, "player_activity", column.name, column.definition); err != nil {
			return err
		}
	}
	for _, column := range starSoulColumns {
		if err := ensureColumn(ctx, db, "player_star_souls", column.name, column.definition); err != nil {
			return err
		}
	}
	for _, column := range familyBossStateColumns {
		if err := ensureColumn(ctx, db, "family_boss_states", column.name, column.definition); err != nil {
			return err
		}
	}
	for _, column := range familyBossDamageColumns {
		if err := ensureColumn(ctx, db, "family_boss_damage", column.name, column.definition); err != nil {
			return err
		}
	}
	if err := ensureItemBuffCapacityType(ctx, db); err != nil {
		return err
	}
	if err := backfillFamilyBossRewardClaims(ctx, db); err != nil {
		return err
	}
	if err := repairPlayerTitles(ctx, db); err != nil {
		return err
	}
	if err := ensurePlayerNameClaims(ctx, db); err != nil {
		return err
	}
	if err := ensurePlayerIDReservations(ctx, db); err != nil {
		return err
	}
	return nil
}

func ensureItemBuffCapacityType(ctx context.Context, db *sql.DB) error {
	var dataType string
	if err := db.QueryRowContext(ctx, `SELECT DATA_TYPE
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'player_item_buffs' AND COLUMN_NAME = 'capacity'`).Scan(&dataType); err != nil {
		return fmt.Errorf("read player_item_buffs.capacity type: %w", err)
	}
	if strings.EqualFold(dataType, "bigint") {
		return nil
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE player_item_buffs MODIFY COLUMN capacity BIGINT NOT NULL`); err != nil {
		return fmt.Errorf("widen player_item_buffs.capacity: %w", err)
	}
	return nil
}

// repairPlayerTitles makes the worn title slot authoritative. HudCharacter
// clears its text for Title=0 and resolves non-zero values through EquipBase.
func repairPlayerTitles(ctx context.Context, db *sql.DB) error {
	var columnDefault sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT COLUMN_DEFAULT
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'players' AND COLUMN_NAME = 'title_id'`).Scan(&columnDefault); err != nil {
		return fmt.Errorf("read players.title_id default: %w", err)
	}
	if !columnDefault.Valid || columnDefault.String != "0" {
		if _, err := db.ExecContext(ctx, `ALTER TABLE players MODIFY COLUMN title_id INT NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("set players.title_id default: %w", err)
		}
	}
	_, err := db.ExecContext(ctx, `UPDATE players AS p
		LEFT JOIN player_items AS i ON i.player_id = p.id
			AND i.location = 2 AND i.slot_index = 4
		SET p.title_id = COALESCE(i.item_id, 0)
		WHERE p.title_id <> COALESCE(i.item_id, 0)`)
	if err != nil {
		return fmt.Errorf("repair player titles from worn equipment: %w", err)
	}
	return nil
}

func backfillFamilyBossRewardClaims(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `INSERT IGNORE INTO family_boss_reward_claims
		(family_id, boss_id, player_id, claimed)
		SELECT d.family_id, d.boss_id, d.player_id, 0
		FROM family_boss_damage AS d
		INNER JOIN family_boss_states AS s
			ON s.family_id = d.family_id AND s.boss_id = d.boss_id
		WHERE s.hp <= 0 AND s.has_reward <> 0`)
	if err != nil {
		return fmt.Errorf("backfill family boss reward claims: %w", err)
	}
	return nil
}

var schemaDDL = []string{
	`CREATE TABLE IF NOT EXISTS accounts (
		id BIGINT NOT NULL AUTO_INCREMENT,
		account VARCHAR(191) NOT NULL,
		password TEXT NOT NULL,
		create_time BIGINT NOT NULL,
		PRIMARY KEY (id),
		UNIQUE KEY uq_accounts_account (account)
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS players (
		id BIGINT NOT NULL AUTO_INCREMENT,
		account_id BIGINT NOT NULL,
		name VARCHAR(191) NOT NULL,
		job_id INT NOT NULL DEFAULT 1,
		skin_id INT NOT NULL DEFAULT 0,
		title_id INT NOT NULL DEFAULT 0,
		level INT NOT NULL DEFAULT 1,
		exp BIGINT NOT NULL DEFAULT 0,
		energy INT NOT NULL DEFAULT 1000,
		map_id INT NOT NULL DEFAULT 1000601,
		pos_x DOUBLE NOT NULL DEFAULT -1.8,
		pos_y DOUBLE NOT NULL DEFAULT -0.84,
		current_hp INT NOT NULL DEFAULT -1,
		current_mp INT NOT NULL DEFAULT -1,
		char_point INT NOT NULL DEFAULT 0,
		skill_point INT NOT NULL DEFAULT 0,
		str_add INT NOT NULL DEFAULT 0,
		quk_add INT NOT NULL DEFAULT 0,
		spi_add INT NOT NULL DEFAULT 0,
		wim_add INT NOT NULL DEFAULT 0,
		phy_add INT NOT NULL DEFAULT 0,
		sta_add INT NOT NULL DEFAULT 0,
		auto_battle INT NOT NULL DEFAULT 0,
		last_login BIGINT NOT NULL DEFAULT 0,
		coin BIGINT NOT NULL DEFAULT 100000,
		yuan_bao BIGINT NOT NULL DEFAULT 100,
		voucher BIGINT NOT NULL DEFAULT 100,
		honor BIGINT NOT NULL DEFAULT 0,
		pvp_currency BIGINT NOT NULL DEFAULT 0,
		store_coin BIGINT NOT NULL DEFAULT 0,
		store_pages INT NOT NULL DEFAULT 2,
		trans INT NOT NULL DEFAULT 0,
		family_id BIGINT NOT NULL DEFAULT 0,
		family_contribute INT NOT NULL DEFAULT 0,
		personal_contribute INT NOT NULL DEFAULT 0,
		PRIMARY KEY (id),
		KEY idx_players_account (account_id)
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS player_id_reservations (
		player_id BIGINT NOT NULL AUTO_INCREMENT,
		account_id BIGINT NOT NULL,
		PRIMARY KEY (player_id),
		UNIQUE KEY uq_player_id_reservations_account (account_id),
		CONSTRAINT fk_player_id_reservations_account FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS player_name_claims (
		name VARCHAR(191) NOT NULL,
		player_id BIGINT NOT NULL,
		PRIMARY KEY (name),
		UNIQUE KEY uq_player_name_claims_player (player_id),
		CONSTRAINT fk_player_name_claims_player FOREIGN KEY (player_id) REFERENCES players(id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS families (
		id BIGINT NOT NULL AUTO_INCREMENT,
		name VARCHAR(191) NOT NULL,
		leader BIGINT NOT NULL,
		level INT NOT NULL DEFAULT 1,
		hornor BIGINT NOT NULL DEFAULT 0,
		notice LONGTEXT NOT NULL DEFAULT (''),
		PRIMARY KEY (id),
		UNIQUE KEY uq_families_name (name)
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS world_boss_states (
		layer INT NOT NULL,
		dead_at BIGINT NOT NULL DEFAULT 0,
		killer VARCHAR(191) NOT NULL DEFAULT '',
		PRIMARY KEY (layer)
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS family_boss_states (
		family_id BIGINT NOT NULL,
		boss_id INT NOT NULL,
		hp BIGINT NOT NULL,
		max_hp BIGINT NOT NULL,
		has_reward INT NOT NULL DEFAULT 0,
		dead_at BIGINT NOT NULL DEFAULT 0,
		PRIMARY KEY (family_id, boss_id)
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS family_boss_damage (
		family_id BIGINT NOT NULL,
		boss_id INT NOT NULL,
		player_id BIGINT NOT NULL,
		damage BIGINT NOT NULL DEFAULT 0,
		treat BIGINT NOT NULL DEFAULT 0,
		participated TINYINT NOT NULL DEFAULT 1,
		PRIMARY KEY (family_id, boss_id, player_id),
		KEY idx_family_boss_damage_player (player_id)
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS family_boss_reward_claims (
		family_id BIGINT NOT NULL,
		boss_id INT NOT NULL,
		player_id BIGINT NOT NULL,
		claimed TINYINT NOT NULL DEFAULT 0,
		PRIMARY KEY (family_id, boss_id, player_id),
		KEY idx_family_boss_reward_claims_player (player_id)
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS consignment_items (
		id BIGINT NOT NULL,
		seller_id BIGINT NOT NULL,
		seller_name VARCHAR(191) NOT NULL DEFAULT '',
		item_id INT NOT NULL,
		item_type INT NOT NULL DEFAULT 2,
		count INT NOT NULL DEFAULT 1,
		price BIGINT NOT NULL DEFAULT 0,
		need_pwd INT NOT NULL DEFAULT 0,
		pwd VARCHAR(255) NOT NULL DEFAULT '',
		put_at BIGINT NOT NULL DEFAULT 0,
		special_key BIGINT NOT NULL DEFAULT 0,
		is_lock INT NOT NULL DEFAULT 0,
		star INT NOT NULL DEFAULT 0,
		quality INT NOT NULL DEFAULT 0,
		level INT NOT NULL DEFAULT 0,
		special_id INT NOT NULL DEFAULT 0,
		PRIMARY KEY (id)
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS consignment_item_purchase_origins (
		consignment_id BIGINT NOT NULL,
		purchase_source INT NOT NULL,
		purchase_currency INT NOT NULL,
		purchase_unit_price BIGINT NOT NULL,
		PRIMARY KEY (consignment_id),
		CONSTRAINT fk_consignment_purchase_origin_parent FOREIGN KEY (consignment_id) REFERENCES consignment_items(id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS player_skills (
		player_id BIGINT NOT NULL,
		skill_id INT NOT NULL,
		level INT NOT NULL,
		sort_order INT NOT NULL,
		PRIMARY KEY (player_id, skill_id),
		KEY idx_player_skills_order (player_id, sort_order),
		CONSTRAINT fk_player_skills_player FOREIGN KEY (player_id) REFERENCES players(id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS player_auto_skills (
		player_id BIGINT NOT NULL,
		position INT NOT NULL,
		skill_id INT NOT NULL,
		PRIMARY KEY (player_id, position),
		CONSTRAINT fk_player_auto_skills_player FOREIGN KEY (player_id) REFERENCES players(id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS player_tasks (
		player_id BIGINT NOT NULL,
		task_id INT NOT NULL,
		state INT NOT NULL,
		kill_count INT NOT NULL DEFAULT 0,
		PRIMARY KEY (player_id, task_id),
		CONSTRAINT fk_player_tasks_player FOREIGN KEY (player_id) REFERENCES players(id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS player_kill_counts (
		player_id BIGINT NOT NULL,
		monster_id INT NOT NULL,
		kill_count INT NOT NULL,
		PRIMARY KEY (player_id, monster_id),
		CONSTRAINT fk_player_kill_counts_player FOREIGN KEY (player_id) REFERENCES players(id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS player_items (
		player_id BIGINT NOT NULL,
		location TINYINT NOT NULL,
		slot_index INT NOT NULL,
		item_id INT NOT NULL,
		item_type INT NOT NULL,
		server_id BIGINT NOT NULL DEFAULT 0,
		item_count INT NOT NULL DEFAULT 1,
		is_locked TINYINT NOT NULL DEFAULT 0,
		quality INT NOT NULL DEFAULT 0,
		star INT NOT NULL DEFAULT 0,
		strength_level INT NOT NULL DEFAULT 0,
		special_key INT NOT NULL DEFAULT 0,
		special_id INT NOT NULL DEFAULT 0,
		get_source TEXT NOT NULL,
		PRIMARY KEY (player_id, location, slot_index),
		KEY idx_player_items_template (item_id),
		CONSTRAINT fk_player_items_player FOREIGN KEY (player_id) REFERENCES players(id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS player_item_purchase_origins (
		player_id BIGINT NOT NULL,
		location TINYINT NOT NULL,
		slot_index INT NOT NULL,
		purchase_source INT NOT NULL,
		purchase_currency INT NOT NULL,
		purchase_unit_price BIGINT NOT NULL,
		PRIMARY KEY (player_id, location, slot_index),
		CONSTRAINT fk_item_purchase_origin_parent FOREIGN KEY (player_id, location, slot_index) REFERENCES player_items(player_id, location, slot_index) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS player_item_main_attributes (
		player_id BIGINT NOT NULL,
		location TINYINT NOT NULL,
		slot_index INT NOT NULL,
		attribute_type INT NOT NULL,
		value DOUBLE NOT NULL,
		PRIMARY KEY (player_id, location, slot_index, attribute_type),
		CONSTRAINT fk_item_main_parent FOREIGN KEY (player_id, location, slot_index) REFERENCES player_items(player_id, location, slot_index) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS player_item_random_attributes (
		player_id BIGINT NOT NULL,
		location TINYINT NOT NULL,
		slot_index INT NOT NULL,
		position INT NOT NULL,
		attribute_id INT NOT NULL,
		PRIMARY KEY (player_id, location, slot_index, position),
		CONSTRAINT fk_item_random_parent FOREIGN KEY (player_id, location, slot_index) REFERENCES player_items(player_id, location, slot_index) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS player_item_affixes (
		player_id BIGINT NOT NULL,
		location TINYINT NOT NULL,
		slot_index INT NOT NULL,
		position INT NOT NULL,
		affix_id INT NOT NULL,
		PRIMARY KEY (player_id, location, slot_index, position),
		CONSTRAINT fk_item_affix_parent FOREIGN KEY (player_id, location, slot_index) REFERENCES player_items(player_id, location, slot_index) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS player_item_gems (
		player_id BIGINT NOT NULL,
		location TINYINT NOT NULL,
		slot_index INT NOT NULL,
		position INT NOT NULL,
		gem_item_id INT NOT NULL,
		PRIMARY KEY (player_id, location, slot_index, position),
		CONSTRAINT fk_item_gem_parent FOREIGN KEY (player_id, location, slot_index) REFERENCES player_items(player_id, location, slot_index) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS player_star_souls (
		player_id BIGINT NOT NULL,
		star_soul_id BIGINT NOT NULL,
		type_id INT NOT NULL,
		level INT NOT NULL,
		exp INT NOT NULL,
		pos_type INT NOT NULL,
		quality INT NOT NULL,
		main_attribute INT NOT NULL,
		vice_growth_level INT NOT NULL DEFAULT 0,
		is_locked TINYINT NOT NULL DEFAULT 0,
		PRIMARY KEY (player_id, star_soul_id),
		CONSTRAINT fk_star_souls_player FOREIGN KEY (player_id) REFERENCES players(id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS player_star_soul_vice_attributes (
		player_id BIGINT NOT NULL,
		star_soul_id BIGINT NOT NULL,
		position INT NOT NULL,
		attribute_type INT NOT NULL,
		attribute_add DOUBLE NOT NULL DEFAULT 0,
		PRIMARY KEY (player_id, star_soul_id, position),
		CONSTRAINT fk_star_soul_vice_parent FOREIGN KEY (player_id, star_soul_id) REFERENCES player_star_souls(player_id, star_soul_id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS player_star_soul_slots (
		player_id BIGINT NOT NULL,
		slot_index INT NOT NULL,
		star_soul_id BIGINT NOT NULL,
		PRIMARY KEY (player_id, slot_index),
		CONSTRAINT fk_star_soul_slots_player FOREIGN KEY (player_id) REFERENCES players(id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS player_pets (
		player_id BIGINT NOT NULL,
		pet_id INT NOT NULL,
		level INT NOT NULL,
		exp INT NOT NULL,
		intimacy INT NOT NULL,
		name VARCHAR(191) NOT NULL,
		is_show TINYINT NOT NULL,
		active INT NOT NULL,
		eat_count INT NOT NULL,
		pet_state INT NOT NULL,
		action_end BIGINT NOT NULL,
		last_day VARCHAR(8) NOT NULL,
		rewarded TINYINT NOT NULL,
		PRIMARY KEY (player_id),
		CONSTRAINT fk_player_pets_player FOREIGN KEY (player_id) REFERENCES players(id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS player_mails (
		player_id BIGINT NOT NULL,
		mail_id BIGINT NOT NULL,
		title TEXT NOT NULL,
		content LONGTEXT NOT NULL,
		sender_name VARCHAR(191) NOT NULL,
		state INT NOT NULL,
		remain_time BIGINT NOT NULL,
		PRIMARY KEY (player_id, mail_id),
		CONSTRAINT fk_player_mails_player FOREIGN KEY (player_id) REFERENCES players(id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS player_mail_items (
		player_id BIGINT NOT NULL,
		mail_id BIGINT NOT NULL,
		position INT NOT NULL,
		item_id INT NOT NULL,
		item_count INT NOT NULL,
		is_locked TINYINT NOT NULL,
		is_has_item TINYINT NOT NULL,
		PRIMARY KEY (player_id, mail_id, position),
		CONSTRAINT fk_mail_items_parent FOREIGN KEY (player_id, mail_id) REFERENCES player_mails(player_id, mail_id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS player_mail_item_purchase_origins (
		player_id BIGINT NOT NULL,
		mail_id BIGINT NOT NULL,
		position INT NOT NULL,
		purchase_source INT NOT NULL,
		purchase_currency INT NOT NULL,
		purchase_unit_price BIGINT NOT NULL,
		PRIMARY KEY (player_id, mail_id, position),
		CONSTRAINT fk_mail_item_purchase_origin_parent FOREIGN KEY (player_id, mail_id, position) REFERENCES player_mail_items(player_id, mail_id, position) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS player_friends (
		player_id BIGINT NOT NULL,
		friend_id BIGINT NOT NULL,
		name VARCHAR(191) NOT NULL,
		job_id INT NOT NULL,
		level INT NOT NULL,
		last_login BIGINT NOT NULL,
		PRIMARY KEY (player_id, friend_id),
		CONSTRAINT fk_player_friends_player FOREIGN KEY (player_id) REFERENCES players(id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS player_friend_requests (
		requester_id BIGINT NOT NULL,
		target_id BIGINT NOT NULL,
		created_at BIGINT NOT NULL,
		PRIMARY KEY (requester_id, target_id),
		KEY idx_friend_requests_target (target_id),
		CONSTRAINT fk_friend_requester FOREIGN KEY (requester_id) REFERENCES players(id) ON DELETE CASCADE,
		CONSTRAINT fk_friend_target FOREIGN KEY (target_id) REFERENCES players(id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS player_activity (
		player_id BIGINT NOT NULL,
		last_day VARCHAR(8) NOT NULL,
		last_month VARCHAR(6) NOT NULL,
		month_count INT NOT NULL,
		month_got_month VARCHAR(6) NOT NULL,
		map_coin_day VARCHAR(8) NOT NULL,
		map_coin_extra INT NOT NULL,
		map_coin_claimed INT NOT NULL,
		trial_highest_id INT NOT NULL,
		trial_reward_day VARCHAR(8) NOT NULL,
		manual_equip_tier_1 INT NOT NULL DEFAULT 0,
		manual_equip_tier_2 INT NOT NULL DEFAULT 0,
		manual_equip_tier_3 INT NOT NULL DEFAULT 0,
		manual_rare_craft_count INT NOT NULL DEFAULT 0,
		manual_rare_pity_at INT NOT NULL DEFAULT 0,
		manual_epic_craft_count INT NOT NULL DEFAULT 0,
		manual_epic_pity_at INT NOT NULL DEFAULT 0,
		quiz_score INT NOT NULL,
		quiz_time_ms BIGINT NOT NULL,
		quiz_completed TINYINT NOT NULL,
		pvp_score INT NOT NULL,
		pvp_battle_day VARCHAR(8) NOT NULL DEFAULT '',
		pvp_battle_count INT NOT NULL,
		pvp_match_count INT NOT NULL,
		pvp_is_matching TINYINT NOT NULL,
		dungeon_quota_day VARCHAR(8) NOT NULL DEFAULT '',
		space_travel_remaining INT NOT NULL DEFAULT 50,
		death_tower_remaining INT NOT NULL DEFAULT 10,
		family_boss_keys INT NOT NULL DEFAULT 2,
		normal_run_day VARCHAR(8) NOT NULL DEFAULT '',
		online_reward_accumulated_ms BIGINT NOT NULL DEFAULT 0,
		PRIMARY KEY (player_id),
		CONSTRAINT fk_player_activity_player FOREIGN KEY (player_id) REFERENCES players(id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS player_month_rewards (
		player_id BIGINT NOT NULL,
		reward_id INT NOT NULL,
		PRIMARY KEY (player_id, reward_id),
		CONSTRAINT fk_month_rewards_player FOREIGN KEY (player_id) REFERENCES players(id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS player_task_kill_progress (
		player_id BIGINT NOT NULL,
		task_id INT NOT NULL,
		monster_id INT NOT NULL,
		kill_count INT NOT NULL,
		PRIMARY KEY (player_id, task_id, monster_id),
		CONSTRAINT fk_task_kill_progress_player FOREIGN KEY (player_id) REFERENCES players(id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS player_task_any_kill_progress (
		player_id BIGINT NOT NULL,
		task_id INT NOT NULL,
		kill_count INT NOT NULL,
		PRIMARY KEY (player_id, task_id),
		CONSTRAINT fk_task_any_kill_player FOREIGN KEY (player_id) REFERENCES players(id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS player_task_flag_progress (
		player_id BIGINT NOT NULL,
		task_id INT NOT NULL,
		progress_type TINYINT NOT NULL,
		PRIMARY KEY (player_id, task_id, progress_type),
		CONSTRAINT fk_task_flag_progress_player FOREIGN KEY (player_id) REFERENCES players(id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS player_mainui_slots (
		player_id BIGINT NOT NULL,
		slot_index INT NOT NULL,
		slot_type INT NOT NULL,
		object_id INT NOT NULL,
		PRIMARY KEY (player_id, slot_index),
		CONSTRAINT fk_mainui_slots_player FOREIGN KEY (player_id) REFERENCES players(id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS player_item_buffs (
		player_id BIGINT NOT NULL,
		category INT NOT NULL,
		item_id INT NOT NULL,
		effect_type INT NOT NULL,
		expires_at BIGINT NOT NULL,
		multiplier DOUBLE NOT NULL,
		capacity BIGINT NOT NULL,
		PRIMARY KEY (player_id, category),
		CONSTRAINT fk_item_buffs_player FOREIGN KEY (player_id) REFERENCES players(id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS family_members (
		family_id BIGINT NOT NULL,
		player_id BIGINT NOT NULL,
		name VARCHAR(191) NOT NULL,
		job_id INT NOT NULL,
		level INT NOT NULL,
		last_login BIGINT NOT NULL,
		PRIMARY KEY (family_id, player_id),
		CONSTRAINT fk_family_members_family FOREIGN KEY (family_id) REFERENCES families(id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS family_requests (
		family_id BIGINT NOT NULL,
		player_id BIGINT NOT NULL,
		PRIMARY KEY (family_id, player_id),
		CONSTRAINT fk_family_requests_family FOREIGN KEY (family_id) REFERENCES families(id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS consignment_gems (
		consignment_id BIGINT NOT NULL,
		position INT NOT NULL,
		gem_item_id INT NOT NULL,
		PRIMARY KEY (consignment_id, position),
		CONSTRAINT fk_consignment_gems_parent FOREIGN KEY (consignment_id) REFERENCES consignment_items(id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS server_meta (
		name VARCHAR(191) NOT NULL,
		value BIGINT NOT NULL DEFAULT 0,
		PRIMARY KEY (name)
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS game_config_nodes (
		node_id BIGINT NOT NULL AUTO_INCREMENT,
		config_name VARCHAR(191) NOT NULL,
		parent_id BIGINT NOT NULL DEFAULT 0,
		field_name VARCHAR(191) NOT NULL DEFAULT '',
		array_index INT NOT NULL DEFAULT -1,
		node_kind TINYINT NOT NULL,
		value_type TINYINT NOT NULL DEFAULT 0,
		int_value BIGINT NULL,
		float_value DOUBLE NULL,
		string_value LONGTEXT NULL,
		bool_value TINYINT NULL,
		PRIMARY KEY (node_id),
		UNIQUE KEY uq_game_config_node (config_name, parent_id, field_name, array_index),
		KEY idx_game_config_parent (config_name, parent_id)
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	`CREATE TABLE IF NOT EXISTS game_config_revisions (
		revision BIGINT NOT NULL AUTO_INCREMENT,
		published_at BIGINT NOT NULL,
		publisher VARCHAR(191) NOT NULL DEFAULT '',
		note VARCHAR(255) NOT NULL DEFAULT '',
		PRIMARY KEY (revision)
	) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
}

type columnDefinition struct {
	name       string
	definition string
}

var playerColumns = []columnDefinition{
	{"title_id", "INT NOT NULL DEFAULT 0"},
	{"auto_battle", "INT NOT NULL DEFAULT -1"},
	{"exp", "BIGINT NOT NULL DEFAULT 0"},
	{"energy", "INT NOT NULL DEFAULT 1000"},
	{"map_id", "INT NOT NULL DEFAULT 1000601"},
	{"pos_x", "DOUBLE NOT NULL DEFAULT -1.8"},
	{"pos_y", "DOUBLE NOT NULL DEFAULT -0.84"},
	{"current_hp", "INT NOT NULL DEFAULT -1"},
	{"current_mp", "INT NOT NULL DEFAULT -1"},
	{"char_point", "INT NOT NULL DEFAULT 0"},
	{"skill_point", "INT NOT NULL DEFAULT 0"},
	{"str_add", "INT NOT NULL DEFAULT 0"},
	{"quk_add", "INT NOT NULL DEFAULT 0"},
	{"spi_add", "INT NOT NULL DEFAULT 0"},
	{"wim_add", "INT NOT NULL DEFAULT 0"},
	{"phy_add", "INT NOT NULL DEFAULT 0"},
	{"sta_add", "INT NOT NULL DEFAULT 0"},
	{"coin", "BIGINT NOT NULL DEFAULT 100000"},
	{"yuan_bao", "BIGINT NOT NULL DEFAULT 100"},
	{"voucher", "BIGINT NOT NULL DEFAULT 100"},
	{"honor", "BIGINT NOT NULL DEFAULT 0"},
	{"pvp_currency", "BIGINT NOT NULL DEFAULT 0"},
	{"store_coin", "BIGINT NOT NULL DEFAULT 0"},
	{"store_pages", "INT NOT NULL DEFAULT 2"},
	{"trans", "INT NOT NULL DEFAULT 0"},
	{"family_id", "BIGINT NOT NULL DEFAULT 0"},
	{"family_contribute", "INT NOT NULL DEFAULT 0"},
	{"personal_contribute", "INT NOT NULL DEFAULT 0"},
}

var consignmentColumns = []columnDefinition{
	{"special_key", "BIGINT NOT NULL DEFAULT 0"},
	{"is_lock", "INT NOT NULL DEFAULT 0"},
	{"star", "INT NOT NULL DEFAULT 0"},
	{"quality", "INT NOT NULL DEFAULT 0"},
	{"level", "INT NOT NULL DEFAULT 0"},
	{"special_id", "INT NOT NULL DEFAULT 0"},
}

var playerActivityColumns = []columnDefinition{
	{"pvp_battle_day", "VARCHAR(8) NOT NULL DEFAULT ''"},
	{"manual_equip_tier_1", "INT NOT NULL DEFAULT 0"},
	{"manual_equip_tier_2", "INT NOT NULL DEFAULT 0"},
	{"manual_equip_tier_3", "INT NOT NULL DEFAULT 0"},
	{"manual_rare_craft_count", "INT NOT NULL DEFAULT 0"},
	{"manual_rare_pity_at", "INT NOT NULL DEFAULT 0"},
	{"manual_epic_craft_count", "INT NOT NULL DEFAULT 0"},
	{"manual_epic_pity_at", "INT NOT NULL DEFAULT 0"},
	{"dungeon_quota_day", "VARCHAR(8) NOT NULL DEFAULT ''"},
	{"space_travel_remaining", "INT NOT NULL DEFAULT 50"},
	{"death_tower_remaining", "INT NOT NULL DEFAULT 10"},
	{"family_boss_keys", "INT NOT NULL DEFAULT 2"},
	{"normal_run_day", "VARCHAR(8) NOT NULL DEFAULT ''"},
	{"online_reward_accumulated_ms", "BIGINT NOT NULL DEFAULT 0"},
}

var starSoulColumns = []columnDefinition{
	{"vice_growth_level", "INT NOT NULL DEFAULT 0"},
}

var familyBossStateColumns = []columnDefinition{
	{"dead_at", "BIGINT NOT NULL DEFAULT 0"},
}

var familyBossDamageColumns = []columnDefinition{
	{"treat", "BIGINT NOT NULL DEFAULT 0"},
	{"participated", "TINYINT NOT NULL DEFAULT 1"},
}

func ensureColumn(ctx context.Context, db *sql.DB, table, column, definition string) error {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?`, table, column).Scan(&count)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	_, err = db.ExecContext(ctx, "ALTER TABLE "+quoteIdentifier(table)+" ADD COLUMN "+
		quoteIdentifier(column)+" "+definition)
	if err != nil {
		return fmt.Errorf("add column %s.%s: %w", table, column, err)
	}
	return nil
}

// ensurePlayerNameClaims reserves every historical name. Old test/import
// databases can contain duplicate names; keeping those rows intact is safer
// than silently renaming players during startup, while the claim prevents any
// additional player from taking the same name.
func ensurePlayerNameClaims(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `INSERT IGNORE INTO player_name_claims (name, player_id)
		SELECT p.name, MIN(p.id)
		FROM players p
		GROUP BY p.name`)
	if err != nil {
		return fmt.Errorf("seed player name claims: %w", err)
	}
	return nil
}

// ensurePlayerIDReservations imports existing identities and advances the
// reservation sequence beyond every historical player id. New accounts then
// receive one stable id before role creation, matching the client's login flow.
func ensurePlayerIDReservations(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `INSERT IGNORE INTO player_id_reservations (player_id, account_id)
		SELECT p.id, p.account_id
		FROM players p
		INNER JOIN (
			SELECT account_id, MIN(id) AS player_id
			FROM players
			GROUP BY account_id
		) first_player ON first_player.player_id = p.id`)
	if err != nil {
		return fmt.Errorf("seed player id reservations: %w", err)
	}
	var nextPlayerID int64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) + 1 FROM players`).Scan(&nextPlayerID); err != nil {
		return fmt.Errorf("read next player id: %w", err)
	}
	if nextPlayerID < 1 {
		nextPlayerID = 1
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf(
		"ALTER TABLE player_id_reservations AUTO_INCREMENT = %d", nextPlayerID)); err != nil {
		return fmt.Errorf("advance player id reservation sequence: %w", err)
	}
	return nil
}

func quoteIdentifier(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}
