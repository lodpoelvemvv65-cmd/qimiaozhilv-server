// export-clone-sql exports the four locally cloned gameplay accounts as a
// self-contained MySQL transaction. It intentionally excludes social, family,
// consignment, and live-session relations.
package main

import (
	"database/sql"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"mhqserver/internal/mysqlschema"
)

type accountPlayer struct {
	AccountID, PlayerID                    int64
	Account, Password                      string
	CreateTime                             int64
	Name                                   string
	JobID, SkinID                          int32
	TitleID, Level                         int32
	Exp                                    int64
	Energy, MapID                          int32
	PosX, PosY                             float64
	CharPoint                              int32
	SkillPoint                             int32
	StrAdd, QukAdd                         int32
	SpiAdd, WimAdd                         int32
	PhyAdd, StaAdd                         int32
	AutoBattle                             int32
	LastLogin                              int64
	Coin, YuanBao                          int64
	Voucher, Honor, PVPCurrency, StoreCoin int64
	StorePages, Trans                      int32
	FamilyID                               int64
	FamilyContribute                       int32
	PersonalContribute                     int32
}

type itemRow struct {
	Location, Slot, ItemID, ItemType int32
	ServerID                         int64
	Count                            int32
	Locked                           int32
	Quality, Star, Strength          int32
	SpecialKey, SpecialID            int32
	Source                           string
}

type tableDump struct {
	name    string
	columns []string
	order   string
}

var playerTables = []tableDump{
	{name: "player_skills", columns: []string{"skill_id", "level", "sort_order"}, order: "sort_order, skill_id"},
	{name: "player_auto_skills", columns: []string{"position", "skill_id"}, order: "position"},
	{name: "player_tasks", columns: []string{"task_id", "state", "kill_count"}, order: "task_id"},
	{name: "player_kill_counts", columns: []string{"monster_id", "kill_count"}, order: "monster_id"},
	{name: "player_activity", columns: []string{
		"last_day", "last_month", "month_count", "month_got_month", "map_coin_day", "map_coin_extra", "map_coin_claimed",
		"trial_highest_id", "trial_reward_day", "manual_equip_tier_1", "manual_equip_tier_2", "manual_equip_tier_3", "manual_rare_craft_count", "manual_rare_pity_at", "manual_epic_craft_count", "manual_epic_pity_at",
		"quiz_score", "quiz_time_ms", "quiz_completed", "pvp_score", "pvp_battle_day", "pvp_battle_count",
		"pvp_match_count", "pvp_is_matching", "dungeon_quota_day", "space_travel_remaining", "death_tower_remaining", "family_boss_keys",
		"online_reward_accumulated_ms",
	}, order: "player_id"},
	{name: "player_month_rewards", columns: []string{"reward_id"}, order: "reward_id"},
	{name: "player_task_kill_progress", columns: []string{"task_id", "monster_id", "kill_count"}, order: "task_id, monster_id"},
	{name: "player_task_any_kill_progress", columns: []string{"task_id", "kill_count"}, order: "task_id"},
	{name: "player_task_flag_progress", columns: []string{"task_id", "progress_type"}, order: "task_id, progress_type"},
	{name: "player_item_buffs", columns: []string{"category", "item_id", "effect_type", "expires_at", "multiplier", "capacity"}, order: "category"},
	{name: "player_star_souls", columns: []string{"star_soul_id", "type_id", "level", "exp", "pos_type", "quality", "main_attribute", "vice_growth_level", "is_locked"}, order: "star_soul_id"},
	{name: "player_star_soul_vice_attributes", columns: []string{"star_soul_id", "position", "attribute_type", "attribute_add"}, order: "star_soul_id, position"},
	{name: "player_star_soul_slots", columns: []string{"slot_index", "star_soul_id"}, order: "slot_index"},
	{name: "player_pets", columns: []string{"pet_id", "level", "exp", "intimacy", "name", "is_show", "active", "eat_count", "pet_state", "action_end", "last_day", "rewarded"}, order: "player_id"},
	{name: "player_mails", columns: []string{"mail_id", "title", "content", "sender_name", "state", "remain_time"}, order: "mail_id"},
	{name: "player_mail_items", columns: []string{"mail_id", "position", "item_id", "item_count", "is_locked", "is_has_item"}, order: "mail_id, position"},
	{name: "player_mail_item_purchase_origins", columns: []string{"mail_id", "position", "purchase_source", "purchase_currency", "purchase_unit_price"}, order: "mail_id, position"},
	{name: "player_mainui_slots", columns: []string{"slot_index", "slot_type", "object_id"}, order: "slot_index"},
}

var itemTables = []tableDump{
	{name: "player_item_purchase_origins", columns: []string{"location", "slot_index", "purchase_source", "purchase_currency", "purchase_unit_price"}, order: "location, slot_index"},
	{name: "player_item_main_attributes", columns: []string{"location", "slot_index", "attribute_type", "value"}, order: "location, slot_index, attribute_type"},
	{name: "player_item_random_attributes", columns: []string{"location", "slot_index", "position", "attribute_id"}, order: "location, slot_index, position"},
	{name: "player_item_affixes", columns: []string{"location", "slot_index", "position", "affix_id"}, order: "location, slot_index, position"},
	{name: "player_item_gems", columns: []string{"location", "slot_index", "position", "gem_item_id"}, order: "location, slot_index, position"},
}

func main() {
	dsn := flag.String("mysql", mysqlschema.DefaultDSN(), "local source MySQL DSN")
	outPath := flag.String("out", "", "output SQL path")
	accountsArg := flag.String("accounts", "a12312301,a12312302,a12312303,a12312304", "comma-separated cloned accounts")
	flag.Parse()
	if strings.TrimSpace(*outPath) == "" {
		log.Fatal("-out is required")
	}
	accounts := splitAccounts(*accountsArg)
	if len(accounts) == 0 {
		log.Fatal("-accounts must contain at least one account")
	}

	db, err := sql.Open("mysql", *dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		log.Fatalf("connect local MySQL: %v", err)
	}

	players := make([]accountPlayer, 0, len(accounts))
	for _, account := range accounts {
		p, err := loadAccountPlayer(db, account)
		if err != nil {
			log.Fatal(err)
		}
		players = append(players, p)
	}

	f, err := os.Create(*outPath)
	if err != nil {
		log.Fatal(err)
	}
	if err := writeMigration(f, db, players); err != nil {
		_ = f.Close()
		log.Fatal(err)
	}
	if err := f.Close(); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("exported %d accounts to %s\n", len(players), *outPath)
}

func splitAccounts(value string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, raw := range strings.Split(value, ",") {
		account := strings.TrimSpace(raw)
		if account != "" && !seen[account] {
			seen[account] = true
			out = append(out, account)
		}
	}
	return out
}

func loadAccountPlayer(db *sql.DB, account string) (accountPlayer, error) {
	var p accountPlayer
	err := db.QueryRow(`SELECT a.id, a.account, a.password, a.create_time,
		p.id, p.name, p.job_id, p.skin_id, p.title_id, p.level, p.exp, p.energy, p.map_id,
		p.pos_x, p.pos_y, p.char_point, p.skill_point, p.str_add, p.quk_add, p.spi_add, p.wim_add,
		p.phy_add, p.sta_add, p.auto_battle, p.last_login, p.coin, p.yuan_bao, p.voucher, p.honor, p.pvp_currency, p.store_coin,
		p.store_pages, p.trans, p.family_id, p.family_contribute, p.personal_contribute
		FROM accounts a JOIN players p ON p.account_id = a.id WHERE a.account = ? ORDER BY p.id`, account).Scan(
		&p.AccountID, &p.Account, &p.Password, &p.CreateTime, &p.PlayerID, &p.Name, &p.JobID, &p.SkinID,
		&p.TitleID, &p.Level, &p.Exp, &p.Energy, &p.MapID, &p.PosX, &p.PosY, &p.CharPoint, &p.SkillPoint,
		&p.StrAdd, &p.QukAdd, &p.SpiAdd, &p.WimAdd, &p.PhyAdd, &p.StaAdd, &p.AutoBattle, &p.LastLogin,
		&p.Coin, &p.YuanBao, &p.Voucher, &p.Honor, &p.PVPCurrency, &p.StoreCoin, &p.StorePages, &p.Trans, &p.FamilyID,
		&p.FamilyContribute, &p.PersonalContribute)
	if err != nil {
		return p, fmt.Errorf("load account %q: %w", account, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM players WHERE account_id = ?`, p.AccountID).Scan(&count); err != nil {
		return p, err
	}
	if count != 1 {
		return p, fmt.Errorf("account %q has %d players; exactly one is required", account, count)
	}
	return p, nil
}

func writeMigration(w *os.File, db *sql.DB, players []accountPlayer) error {
	if _, err := fmt.Fprintln(w, "-- Generated by cmd/export-clone-sql. Target IDs are assigned in the transaction."); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "SET NAMES utf8mb4 COLLATE utf8mb4_bin;\nSTART TRANSACTION;\nSET @mhq_next_server_id := (SELECT COALESCE(MAX(server_id), 0) FROM player_items);"); err != nil {
		return err
	}
	for _, p := range players {
		if err := writeAccount(w, db, p); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(w, "COMMIT;")
	return err
}

func writeAccount(w *os.File, db *sql.DB, p accountPlayer) error {
	if _, err := fmt.Fprintf(w, "INSERT INTO `accounts` (`account`,`password`,`create_time`) VALUES (%s,%s,%s);\nSET @mhq_account_id := LAST_INSERT_ID();\n", sqlLiteral(p.Account), sqlLiteral(p.Password), sqlLiteral(p.CreateTime)); err != nil {
		return err
	}
	playerValues := []any{p.Name, p.JobID, p.SkinID, p.TitleID, p.Level, p.Exp, p.Energy, p.MapID, p.PosX, p.PosY,
		p.CharPoint, p.SkillPoint, p.StrAdd, p.QukAdd, p.SpiAdd, p.WimAdd, p.PhyAdd, p.StaAdd, p.AutoBattle,
		p.LastLogin, p.Coin, p.YuanBao, p.Voucher, p.Honor, p.PVPCurrency, p.StoreCoin, p.StorePages, p.Trans, p.FamilyID,
		p.FamilyContribute, p.PersonalContribute}
	cols := []string{"name", "job_id", "skin_id", "title_id", "level", "exp", "energy", "map_id", "pos_x", "pos_y", "char_point", "skill_point", "str_add", "quk_add", "spi_add", "wim_add", "phy_add", "sta_add", "auto_battle", "last_login", "coin", "yuan_bao", "voucher", "honor", "pvp_currency", "store_coin", "store_pages", "trans", "family_id", "family_contribute", "personal_contribute"}
	values := make([]string, 0, len(playerValues)+1)
	values = append(values, "@mhq_account_id")
	for _, value := range playerValues {
		values = append(values, sqlLiteral(value))
	}
	if _, err := fmt.Fprintf(w, "INSERT INTO `players` (`account_id`,%s) VALUES (%s);\nSET @mhq_player_id := LAST_INSERT_ID();\n", quotedColumns(cols), strings.Join(values, ",")); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "INSERT INTO `player_name_claims` (`name`,`player_id`) VALUES (%s,@mhq_player_id);\n", sqlLiteral(p.Name)); err != nil {
		return err
	}
	if err := writeItems(w, db, p.PlayerID); err != nil {
		return err
	}
	for _, table := range itemTables {
		if err := writePlayerRows(w, db, table, p.PlayerID); err != nil {
			return err
		}
	}
	for _, table := range playerTables {
		if err := writePlayerRows(w, db, table, p.PlayerID); err != nil {
			return err
		}
	}
	return nil
}

func writeItems(w *os.File, db *sql.DB, playerID int64) error {
	rows, err := db.Query(`SELECT location, slot_index, item_id, item_type, server_id, item_count, is_locked, quality, star, strength_level, special_key, special_id, get_source FROM player_items WHERE player_id = ? ORDER BY location, slot_index`, playerID)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols := []string{"player_id", "location", "slot_index", "item_id", "item_type", "server_id", "item_count", "is_locked", "quality", "star", "strength_level", "special_key", "special_id", "get_source"}
	for rows.Next() {
		var item itemRow
		if err := rows.Scan(&item.Location, &item.Slot, &item.ItemID, &item.ItemType, &item.ServerID, &item.Count, &item.Locked, &item.Quality, &item.Star, &item.Strength, &item.SpecialKey, &item.SpecialID, &item.Source); err != nil {
			return err
		}
		serverID := sqlLiteral(item.ServerID)
		if item.ItemType == 1 {
			serverID = "(@mhq_next_server_id := @mhq_next_server_id + 1)"
		}
		values := []string{"@mhq_player_id", sqlLiteral(item.Location), sqlLiteral(item.Slot), sqlLiteral(item.ItemID), sqlLiteral(item.ItemType), serverID, sqlLiteral(item.Count), sqlLiteral(item.Locked), sqlLiteral(item.Quality), sqlLiteral(item.Star), sqlLiteral(item.Strength), sqlLiteral(item.SpecialKey), sqlLiteral(item.SpecialID), sqlLiteral(item.Source)}
		if _, err := fmt.Fprintf(w, "INSERT INTO `player_items` (%s) VALUES (%s);\n", quotedColumns(cols), strings.Join(values, ",")); err != nil {
			return err
		}
	}
	return rows.Err()
}

func writePlayerRows(w *os.File, db *sql.DB, table tableDump, playerID int64) error {
	selectColumns := strings.Join(table.columns, ", ")
	query := fmt.Sprintf("SELECT %s FROM `%s` WHERE player_id = ?", selectColumns, table.name)
	if table.order != "" {
		query += " ORDER BY " + table.order
	}
	rows, err := db.Query(query, playerID)
	if err != nil {
		return fmt.Errorf("read %s: %w", table.name, err)
	}
	defer rows.Close()
	cols := append([]string{"player_id"}, table.columns...)
	for rows.Next() {
		values := make([]any, len(table.columns))
		pointers := make([]any, len(values))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return fmt.Errorf("scan %s: %w", table.name, err)
		}
		literals := make([]string, 0, len(values)+1)
		literals = append(literals, "@mhq_player_id")
		for _, value := range values {
			literals = append(literals, sqlLiteral(value))
		}
		if _, err := fmt.Fprintf(w, "INSERT INTO `%s` (%s) VALUES (%s);\n", table.name, quotedColumns(cols), strings.Join(literals, ",")); err != nil {
			return err
		}
	}
	return rows.Err()
}

func quotedColumns(columns []string) string {
	quoted := make([]string, len(columns))
	for i, column := range columns {
		quoted[i] = "`" + strings.ReplaceAll(column, "`", "``") + "`"
	}
	return strings.Join(quoted, ",")
}

func sqlLiteral(value any) string {
	switch v := value.(type) {
	case nil:
		return "NULL"
	case string:
		return stringLiteral(v)
	case []byte:
		if len(v) == 0 {
			return "''"
		}
		return "0x" + hex.EncodeToString(v)
	case int:
		return strconv.Itoa(v)
	case int8:
		return strconv.FormatInt(int64(v), 10)
	case int16:
		return strconv.FormatInt(int64(v), 10)
	case int32:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case uint:
		return strconv.FormatUint(uint64(v), 10)
	case uint8:
		return strconv.FormatUint(uint64(v), 10)
	case uint16:
		return strconv.FormatUint(uint64(v), 10)
	case uint32:
		return strconv.FormatUint(uint64(v), 10)
	case uint64:
		return strconv.FormatUint(v, 10)
	case float32:
		return floatLiteral(float64(v))
	case float64:
		return floatLiteral(v)
	case bool:
		if v {
			return "1"
		}
		return "0"
	case time.Time:
		return stringLiteral(v.Format("2006-01-02 15:04:05.999999"))
	default:
		return stringLiteral(fmt.Sprint(v))
	}
}

func stringLiteral(value string) string {
	if value == "" {
		return "''"
	}
	return "CONVERT(0x" + hex.EncodeToString([]byte(value)) + " USING utf8mb4)"
}

func floatLiteral(value float64) string {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "NULL"
	}
	return strconv.FormatFloat(value, 'g', -1, 64)
}
