package main

// clone-accounts creates complete gameplay snapshots from one account. Preview
// is the default; -execute is required before any account/player row is added.

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"mhqserver/internal/equipattributes"
	"mhqserver/internal/mysqlschema"
)

const cloneCount = 4

type cloneSpec struct {
	Account, Role string
	JobID         int32
	JobName       string
}

var cloneJobs = []cloneSpec{
	{Account: "a12312301", Role: "a12312301", JobID: 1, JobName: "军官"},
	{Account: "a12312302", Role: "a12312302", JobID: 3, JobName: "运动员"},
	{Account: "a12312303", Role: "a12312303", JobID: 5, JobName: "护士"},
	{Account: "a12312304", Role: "a12312304", JobID: 7, JobName: "超能力"},
}

type playerSnapshot struct {
	ID, AccountID                                                                     int64
	Name                                                                              string
	JobID, SkinID, TitleID, Level, Energy, MapID                                      int32
	Exp                                                                               int64
	PosX, PosY                                                                        float64
	CharPoint, SkillPoint, StrAdd, QukAdd, SpiAdd, WimAdd, PhyAdd, StaAdd, AutoBattle int32
	LastLogin, Coin, YuanBao, Voucher, Honor, PVPCurrency, StoreCoin                  int64
	StorePages, Trans                                                                 int32
	FamilyID                                                                          int64
	FamilyContribute, PersonalContribute                                              int32
}

type equipTemplate struct{ ID, JobID, DetailType, Type, UseLevel, Quality, Star, SpecialKey, Transmigration int32 }
type itemSnapshot struct {
	Location, Slot, ItemID, ItemType, Count, Quality, Star, Strength int32
	ServerID                                                         int64
	Locked, SpecialKey, SpecialID                                    int32
	Source                                                           string
	PurchaseSource, PurchaseCurrency                                 int32
	PurchaseUnitPrice                                                int64
	MainAttributes                                                   map[int32]float64
}
type skillSnapshot struct{ ID, Level, Order int32 }
type mainUISnapshot struct{ Slot, Type, ID int32 }
type clonePlan struct {
	spec                   cloneSpec
	items                  []itemSnapshot
	skills                 []skillSnapshot
	autoSkills             []skillSnapshot
	mainUI                 []mainUISnapshot
	sourceJob              int32
	nextServerID, playerID int64
}

func main() {
	account := flag.String("account", "a123123", "source account")
	dsn := flag.String("mysql", mysqlschema.DefaultDSN(), "MySQL DSN")
	execute := flag.Bool("execute", false, "write the four cloned accounts")
	flag.Parse()
	db, description, err := mysqlschema.Open(*dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srcAccountID, password, err := loadSourceAccount(ctx, db, *account)
	if err != nil {
		log.Fatal(err)
	}
	src, err := loadSourcePlayer(ctx, db, srcAccountID)
	if err != nil {
		log.Fatal(err)
	}
	equip, err := loadEquipBase(ctx, db)
	if err != nil {
		log.Fatal(err)
	}
	items, err := loadItems(ctx, db, src.ID)
	if err != nil {
		log.Fatal(err)
	}
	skills, err := loadSkills(ctx, db, src.ID)
	if err != nil {
		log.Fatal(err)
	}
	autoSkills, err := loadAutoSkills(ctx, db, src.ID)
	if err != nil {
		log.Fatal(err)
	}
	maxID, err := maxServerID(ctx, db)
	if err != nil {
		log.Fatal(err)
	}
	plans := make([]clonePlan, 0, cloneCount)
	for _, spec := range cloneJobs {
		plan, err := buildPlan(ctx, db, src, spec, items, skills, autoSkills, equip, maxID)
		if err != nil {
			log.Fatal(err)
		}
		maxID, plans = plan.nextServerID, append(plans, plan)
	}
	printPreview(description, *account, src, plans, len(items), len(skills))
	if !*execute {
		fmt.Println("preview only: no database rows were changed; rerun with -execute to create these accounts")
		return
	}
	if err := validateNoConflicts(ctx, db, plans); err != nil {
		log.Fatal(err)
	}
	if err := executePlans(ctx, db, password, src, plans); err != nil {
		log.Fatal(err)
	}
	fmt.Println("created accounts:")
	for _, plan := range plans {
		fmt.Printf("  %s role=%s job=%s player_id=%d\n", plan.spec.Account, plan.spec.Role, plan.spec.JobName, plan.playerID)
	}
	if err := verifyClones(ctx, db, src.ID, plans, len(items)); err != nil {
		log.Fatal(err)
	}
	fmt.Println("verification: source unchanged; cloned gameplay rows, equipment, star souls and attributes match")
}

func loadSourceAccount(ctx context.Context, db *sql.DB, account string) (int64, string, error) {
	var id int64
	var password string
	err := db.QueryRowContext(ctx, `SELECT id, password FROM accounts WHERE account = ?`, account).Scan(&id, &password)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", fmt.Errorf("source account %q does not exist", account)
	}
	return id, password, err
}

func loadSourcePlayer(ctx context.Context, db *sql.DB, accountID int64) (*playerSnapshot, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, account_id, name, job_id, skin_id, title_id, level, exp,
		energy, map_id, pos_x, pos_y, char_point, skill_point, str_add, quk_add, spi_add, wim_add,
		phy_add, sta_add, auto_battle, last_login, coin, yuan_bao, voucher, honor, pvp_currency, store_coin, store_pages,
		trans, family_id, family_contribute, personal_contribute FROM players WHERE account_id = ? ORDER BY id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var players []*playerSnapshot
	for rows.Next() {
		p := &playerSnapshot{}
		if err := rows.Scan(&p.ID, &p.AccountID, &p.Name, &p.JobID, &p.SkinID, &p.TitleID, &p.Level, &p.Exp,
			&p.Energy, &p.MapID, &p.PosX, &p.PosY, &p.CharPoint, &p.SkillPoint, &p.StrAdd, &p.QukAdd,
			&p.SpiAdd, &p.WimAdd, &p.PhyAdd, &p.StaAdd, &p.AutoBattle, &p.LastLogin, &p.Coin, &p.YuanBao,
			&p.Voucher, &p.Honor, &p.PVPCurrency, &p.StoreCoin, &p.StorePages, &p.Trans, &p.FamilyID, &p.FamilyContribute, &p.PersonalContribute); err != nil {
			return nil, err
		}
		players = append(players, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(players) != 1 {
		return nil, fmt.Errorf("source account has %d players; exactly one is required", len(players))
	}
	return players[0], nil
}

func loadItems(ctx context.Context, db *sql.DB, playerID int64) ([]itemSnapshot, error) {
	rows, err := db.QueryContext(ctx, `SELECT item.location, item.slot_index, item.item_id, item.item_type,
		item.server_id, item.item_count, item.is_locked, item.quality, item.star, item.strength_level,
		item.special_key, item.special_id, item.get_source, COALESCE(origin.purchase_source, 0),
		COALESCE(origin.purchase_currency, 0), COALESCE(origin.purchase_unit_price, 0)
		FROM player_items AS item
		LEFT JOIN player_item_purchase_origins AS origin
			ON origin.player_id = item.player_id AND origin.location = item.location AND origin.slot_index = item.slot_index
		WHERE item.player_id = ? ORDER BY item.location, item.slot_index`, playerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []itemSnapshot
	for rows.Next() {
		var it itemSnapshot
		if err := rows.Scan(&it.Location, &it.Slot, &it.ItemID, &it.ItemType, &it.ServerID, &it.Count,
			&it.Locked, &it.Quality, &it.Star, &it.Strength, &it.SpecialKey, &it.SpecialID, &it.Source,
			&it.PurchaseSource, &it.PurchaseCurrency, &it.PurchaseUnitPrice); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range out {
		item := &out[index]
		attributes, err := loadMainAttributes(ctx, db, playerID, item.Location, item.Slot)
		if err != nil {
			return nil, err
		}
		item.MainAttributes = attributes
	}
	return out, nil
}

func loadSkills(ctx context.Context, db *sql.DB, playerID int64) ([]skillSnapshot, error) {
	rows, err := db.QueryContext(ctx, `SELECT skill_id, level, sort_order FROM player_skills WHERE player_id = ? ORDER BY sort_order, skill_id`, playerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []skillSnapshot
	for rows.Next() {
		var s skillSnapshot
		if err := rows.Scan(&s.ID, &s.Level, &s.Order); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func loadAutoSkills(ctx context.Context, db *sql.DB, playerID int64) ([]skillSnapshot, error) {
	rows, err := db.QueryContext(ctx, `SELECT position, skill_id FROM player_auto_skills WHERE player_id = ? ORDER BY position`, playerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []skillSnapshot
	for rows.Next() {
		var s skillSnapshot
		if err := rows.Scan(&s.Order, &s.ID); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func loadMainUI(ctx context.Context, db *sql.DB, playerID int64) ([]mainUISnapshot, error) {
	rows, err := db.QueryContext(ctx, `SELECT slot_index, slot_type, object_id FROM player_mainui_slots WHERE player_id = ? ORDER BY slot_index`, playerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []mainUISnapshot
	for rows.Next() {
		var s mainUISnapshot
		if err := rows.Scan(&s.Slot, &s.Type, &s.ID); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func maxServerID(ctx context.Context, db *sql.DB) (int64, error) {
	var maxID int64
	err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(server_id), 0) FROM player_items`).Scan(&maxID)
	return maxID, err
}

type configNode struct {
	id                          int64
	parentID                    int64
	fieldName                   string
	arrayIndex, kind, valueType int
	intValue                    sql.NullInt64
	floatValue                  sql.NullFloat64
}

// loadEquipBase reads the authoritative MySQL configuration tree. The clone
// utility must use the same rows as the running server, including custom items.
func loadEquipBase(ctx context.Context, db *sql.DB) (map[int32]equipTemplate, error) {
	rows, err := db.QueryContext(ctx, `SELECT node_id, parent_id, field_name, array_index,
		node_kind, value_type, int_value, float_value
		FROM game_config_nodes WHERE config_name = 'EquipBase' ORDER BY node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := make(map[int64]*configNode)
	children := make(map[int64][]*configNode)
	for rows.Next() {
		n := &configNode{}
		if err := rows.Scan(&n.id, &n.parentID, &n.fieldName, &n.arrayIndex, &n.kind, &n.valueType, &n.intValue, &n.floatValue); err != nil {
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
	out := make(map[int32]equipTemplate, len(children[0]))
	for _, root := range children[0] {
		if root.kind != 1 || root.arrayIndex <= 0 {
			continue
		}
		t := equipTemplate{ID: int32(root.arrayIndex)}
		for _, field := range children[root.id] {
			value := configNodeInt(field)
			switch field.fieldName {
			case "JobId":
				t.JobID = value
			case "DetialType":
				t.DetailType = value
			case "Type":
				t.Type = value
			case "UseLevel":
				t.UseLevel = value
			case "Quality":
				t.Quality = value
			case "Star":
				t.Star = value
			case "SpecialKey":
				t.SpecialKey = value
			case "Transmigration":
				t.Transmigration = value
			}
		}
		out[t.ID] = t
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("MySQL EquipBase contains no rows")
	}
	return out, nil
}

func configNodeInt(node *configNode) int32 {
	if node == nil || node.kind != 3 {
		return 0
	}
	if node.valueType == 1 && node.intValue.Valid {
		return int32(node.intValue.Int64)
	}
	if node.valueType == 2 && node.floatValue.Valid {
		return int32(node.floatValue.Float64)
	}
	return 0
}

func buildPlan(ctx context.Context, db *sql.DB, src *playerSnapshot, spec cloneSpec, sourceItems []itemSnapshot, sourceSkills, sourceAutoSkills []skillSnapshot, equip map[int32]equipTemplate, nextID int64) (clonePlan, error) {
	attributeTemplates, err := equipattributes.LoadTemplates(ctx, db)
	if err != nil {
		return clonePlan{}, err
	}
	mainUI, err := loadMainUI(ctx, db, src.ID)
	if err != nil {
		return clonePlan{}, err
	}
	plan := clonePlan{spec: spec, mainUI: mainUI, sourceJob: jobType(src.JobID), nextServerID: nextID}
	sourceJob, targetJob := jobType(src.JobID), jobType(spec.JobID)
	for _, item := range sourceItems {
		copy := item
		if copy.ItemType == 1 {
			plan.nextServerID++
			copy.ServerID = plan.nextServerID
			if template, ok := equip[copy.ItemID]; ok && template.JobID != 0 {
				if template.JobID != targetJob {
					mapped, ok := mapEquipID(copy.ItemID, template, targetJob, equip)
					if !ok {
						return clonePlan{}, fmt.Errorf("no EquipBase equivalent for item %d -> job %d", copy.ItemID, targetJob)
					}
					copy.ItemID = mapped
				}
				copy.SpecialKey = equip[copy.ItemID].SpecialKey
			}
			if copy.ItemID != item.ItemID {
				target, ok := attributeTemplates[copy.ItemID]
				if !ok {
					return clonePlan{}, fmt.Errorf("missing attribute template %d", copy.ItemID)
				}
				copy.MainAttributes = equipattributes.Normalize(item.MainAttributes, target)
			}
		}
		plan.items = append(plan.items, copy)
	}
	for _, skill := range sourceSkills {
		skill.ID = mapSkillID(skill.ID, sourceJob, targetJob)
		plan.skills = append(plan.skills, skill)
	}
	for _, skill := range sourceAutoSkills {
		skill.ID = mapSkillID(skill.ID, sourceJob, targetJob)
		plan.autoSkills = append(plan.autoSkills, skill)
	}
	for i := range plan.mainUI {
		if plan.mainUI[i].Type == 1 {
			plan.mainUI[i].ID = mapSkillID(plan.mainUI[i].ID, sourceJob, targetJob)
		}
	}
	return plan, nil
}

func jobType(jobID int32) int32 {
	if jobID <= 0 {
		return 1
	}
	if jobID <= 8 {
		return (jobID + 1) / 2
	}
	return jobID / 100000
}

func mapSkillID(id, sourceJob, targetJob int32) int32 {
	if id >= 100000 && id/100000 == sourceJob && sourceJob != targetJob {
		return targetJob*100000 + id%100000
	}
	return id
}

func equipFamily(t equipTemplate) string {
	return fmt.Sprintf("%d/%d/%d/%d/%d/%d", t.DetailType, t.Type, t.UseLevel, t.Quality, t.Star, t.Transmigration)
}

func mapEquipID(sourceID int32, source equipTemplate, targetJob int32, all map[int32]equipTemplate) (int32, bool) {
	var sourceGroup, targetGroup []int32
	for id, t := range all {
		if t.JobID == source.JobID && equipFamily(t) == equipFamily(source) {
			sourceGroup = append(sourceGroup, id)
		}
		if t.JobID == targetJob && equipFamily(t) == equipFamily(source) {
			targetGroup = append(targetGroup, id)
		}
	}
	sort.Slice(sourceGroup, func(i, j int) bool { return sourceGroup[i] < sourceGroup[j] })
	sort.Slice(targetGroup, func(i, j int) bool { return targetGroup[i] < targetGroup[j] })
	for i, id := range sourceGroup {
		if id == sourceID && i < len(targetGroup) {
			return targetGroup[i], true
		}
	}
	return 0, false
}

func validateNoConflicts(ctx context.Context, db *sql.DB, plans []clonePlan) error {
	for _, plan := range plans {
		var accountCount, roleCount int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE account = ?`, plan.spec.Account).Scan(&accountCount); err != nil {
			return err
		}
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM players WHERE name = ?`, plan.spec.Role).Scan(&roleCount); err != nil {
			return err
		}
		if accountCount != 0 || roleCount != 0 {
			return fmt.Errorf("target conflict account=%s role=%s", plan.spec.Account, plan.spec.Role)
		}
	}
	return nil
}

func executePlans(ctx context.Context, db *sql.DB, password string, src *playerSnapshot, plans []clonePlan) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i := range plans {
		plan := &plans[i]
		res, err := tx.ExecContext(ctx, `INSERT INTO accounts (account, password, create_time) VALUES (?, ?, ?)`, plan.spec.Account, password, time.Now().Unix())
		if err != nil {
			return fmt.Errorf("insert account %s: %w", plan.spec.Account, err)
		}
		accountID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		res, err = tx.ExecContext(ctx, `INSERT INTO players
			(account_id, name, job_id, skin_id, title_id, level, exp, energy, map_id, pos_x, pos_y, char_point, skill_point,
			 str_add, quk_add, spi_add, wim_add, phy_add, sta_add, auto_battle, last_login, coin, yuan_bao, voucher, honor, pvp_currency,
			 store_coin, store_pages, trans, family_id, family_contribute, personal_contribute)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, 0)`,
			accountID, plan.spec.Role, plan.spec.JobID, src.SkinID, src.TitleID, src.Level, src.Exp, src.Energy, src.MapID,
			src.PosX, src.PosY, src.CharPoint, src.SkillPoint, src.StrAdd, src.QukAdd, src.SpiAdd, src.WimAdd, src.PhyAdd, src.StaAdd,
			src.AutoBattle, src.Coin, src.YuanBao, src.Voucher, src.Honor, src.PVPCurrency, src.StoreCoin, src.StorePages, src.Trans)
		if err != nil {
			return fmt.Errorf("insert player %s: %w", plan.spec.Role, err)
		}
		plan.playerID, err = res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO player_name_claims (name, player_id) VALUES (?, ?)`, plan.spec.Role, plan.playerID); err != nil {
			return err
		}
		if err := copyRelations(ctx, tx, src.ID, plan); err != nil {
			return fmt.Errorf("copy %s: %w", plan.spec.Account, err)
		}
	}
	return tx.Commit()
}

func copyRelations(ctx context.Context, tx *sql.Tx, sourceID int64, plan *clonePlan) error {
	pid := plan.playerID
	for _, skill := range plan.skills {
		if _, err := tx.ExecContext(ctx, `INSERT INTO player_skills (player_id, skill_id, level, sort_order) VALUES (?, ?, ?, ?)`, pid, skill.ID, skill.Level, skill.Order); err != nil {
			return err
		}
	}
	for _, skill := range plan.autoSkills {
		if _, err := tx.ExecContext(ctx, `INSERT INTO player_auto_skills (player_id, position, skill_id) VALUES (?, ?, ?)`, pid, skill.Order, skill.ID); err != nil {
			return err
		}
	}
	for _, it := range plan.items {
		if _, err := tx.ExecContext(ctx, `INSERT INTO player_items (player_id, location, slot_index, item_id, item_type, server_id, item_count, is_locked, quality, star, strength_level, special_key, special_id, get_source) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, pid, it.Location, it.Slot, it.ItemID, it.ItemType, it.ServerID, it.Count, it.Locked, it.Quality, it.Star, it.Strength, it.SpecialKey, it.SpecialID, it.Source); err != nil {
			return err
		}
		if it.PurchaseSource != 0 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO player_item_purchase_origins
				(player_id, location, slot_index, purchase_source, purchase_currency, purchase_unit_price) VALUES (?, ?, ?, ?, ?, ?)`,
				pid, it.Location, it.Slot, it.PurchaseSource, it.PurchaseCurrency, it.PurchaseUnitPrice); err != nil {
				return err
			}
		}
		if err := copyItemChildren(ctx, tx, sourceID, pid, it.Location, it.Slot); err != nil {
			return err
		}
		if err := equipattributes.Insert(ctx, tx, pid, it.Location, it.Slot, it.MainAttributes); err != nil {
			return err
		}
	}
	if err := copyStarSouls(ctx, tx, sourceID, pid); err != nil {
		return err
	}
	for _, sl := range plan.mainUI {
		if _, err := tx.ExecContext(ctx, `INSERT INTO player_mainui_slots (player_id, slot_index, slot_type, object_id) VALUES (?, ?, ?, ?)`, pid, sl.Slot, sl.Type, sl.ID); err != nil {
			return err
		}
	}
	for _, spec := range []struct{ table, columns string }{
		{"player_tasks", "task_id, state, kill_count"}, {"player_kill_counts", "monster_id, kill_count"},
		{"player_activity", "last_day, last_month, month_count, month_got_month, map_coin_day, map_coin_extra, map_coin_claimed, trial_highest_id, trial_reward_day, manual_equip_tier_1, manual_equip_tier_2, manual_equip_tier_3, quiz_score, quiz_time_ms, quiz_completed, pvp_score, pvp_battle_day, pvp_battle_count, pvp_match_count, pvp_is_matching, dungeon_quota_day, space_travel_remaining, death_tower_remaining, family_boss_keys, online_reward_accumulated_ms, manual_rare_craft_count, manual_rare_pity_at, manual_epic_craft_count, manual_epic_pity_at"},
		{"player_month_rewards", "reward_id"}, {"player_task_kill_progress", "task_id, monster_id, kill_count"}, {"player_task_any_kill_progress", "task_id, kill_count"}, {"player_task_flag_progress", "task_id, progress_type"}, {"player_item_buffs", "category, item_id, effect_type, expires_at, multiplier, capacity"},
	} {
		query := fmt.Sprintf("INSERT INTO %s (player_id, %s) SELECT ?, %s FROM %s WHERE player_id = ?", spec.table, spec.columns, spec.columns, spec.table)
		if _, err := tx.ExecContext(ctx, query, pid, sourceID); err != nil {
			return err
		}
	}
	return copyPetMails(ctx, tx, sourceID, pid)
}

func copyItemChildren(ctx context.Context, tx *sql.Tx, sourceID, targetID int64, location, slot int32) error {
	for _, query := range []string{
		`INSERT INTO player_item_random_attributes (player_id, location, slot_index, position, attribute_id) SELECT ?, location, slot_index, position, attribute_id FROM player_item_random_attributes WHERE player_id = ? AND location = ? AND slot_index = ?`,
		`INSERT INTO player_item_affixes (player_id, location, slot_index, position, affix_id) SELECT ?, location, slot_index, position, affix_id FROM player_item_affixes WHERE player_id = ? AND location = ? AND slot_index = ?`,
		`INSERT INTO player_item_gems (player_id, location, slot_index, position, gem_item_id) SELECT ?, location, slot_index, position, gem_item_id FROM player_item_gems WHERE player_id = ? AND location = ? AND slot_index = ?`,
	} {
		if _, err := tx.ExecContext(ctx, query, targetID, sourceID, location, slot); err != nil {
			return err
		}
	}
	return nil
}

func copyStarSouls(ctx context.Context, tx *sql.Tx, sourceID, targetID int64) error {
	for _, query := range []string{
		`INSERT INTO player_star_souls (player_id, star_soul_id, type_id, level, exp, pos_type, quality, main_attribute, vice_growth_level, is_locked) SELECT ?, star_soul_id, type_id, level, exp, pos_type, quality, main_attribute, vice_growth_level, is_locked FROM player_star_souls WHERE player_id = ?`,
		`INSERT INTO player_star_soul_vice_attributes (player_id, star_soul_id, position, attribute_type, attribute_add) SELECT ?, star_soul_id, position, attribute_type, attribute_add FROM player_star_soul_vice_attributes WHERE player_id = ?`,
		`INSERT INTO player_star_soul_slots (player_id, slot_index, star_soul_id) SELECT ?, slot_index, star_soul_id FROM player_star_soul_slots WHERE player_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, query, targetID, sourceID); err != nil {
			return err
		}
	}
	return nil
}

func copyPetMails(ctx context.Context, tx *sql.Tx, sourceID, targetID int64) error {
	for _, query := range []string{
		`INSERT INTO player_pets (player_id, pet_id, level, exp, intimacy, name, is_show, active, eat_count, pet_state, action_end, last_day, rewarded) SELECT ?, pet_id, level, exp, intimacy, name, is_show, active, eat_count, pet_state, action_end, last_day, rewarded FROM player_pets WHERE player_id = ?`,
		`INSERT INTO player_mails (player_id, mail_id, title, content, sender_name, state, remain_time) SELECT ?, mail_id, title, content, sender_name, state, remain_time FROM player_mails WHERE player_id = ?`,
		`INSERT INTO player_mail_items (player_id, mail_id, position, item_id, item_count, is_locked, is_has_item) SELECT ?, mail_id, position, item_id, item_count, is_locked, is_has_item FROM player_mail_items WHERE player_id = ?`,
		`INSERT INTO player_mail_item_purchase_origins (player_id, mail_id, position, purchase_source, purchase_currency, purchase_unit_price) SELECT ?, mail_id, position, purchase_source, purchase_currency, purchase_unit_price FROM player_mail_item_purchase_origins WHERE player_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, query, targetID, sourceID); err != nil {
			return err
		}
	}
	return nil
}

func printPreview(description, sourceAccount string, src *playerSnapshot, plans []clonePlan, itemCount, skillCount int) {
	fmt.Printf("MySQL=%s\nsource=%s role=%s player_id=%d job=%s level=%d trans=%d items=%d skills=%d\n", description, sourceAccount, src.Name, src.ID, jobName(src.JobID), src.Level, src.Trans, itemCount, skillCount)
	for _, plan := range plans {
		changes := make([]string, 0)
		for _, it := range plan.items {
			if it.ItemType == 1 {
				changes = append(changes, fmt.Sprintf("slot%d:%d", it.Slot, it.ItemID))
			}
		}
		fmt.Printf("target account=%s role=%s job=%s job_id=%d equipment templates=%s\n", plan.spec.Account, plan.spec.Role, plan.spec.JobName, plan.spec.JobID, strings.Join(changes, ","))
	}
}

func jobName(jobID int32) string {
	switch jobType(jobID) {
	case 1:
		return "军官"
	case 2:
		return "运动员"
	case 3:
		return "护士"
	case 4:
		return "超能力"
	default:
		return "未知"
	}
}

func verifyClones(ctx context.Context, db *sql.DB, sourceID int64, plans []clonePlan, expectedItems int) error {
	var sourceItems, sourceSouls int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM player_items WHERE player_id = ?`, sourceID).Scan(&sourceItems); err != nil {
		return err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM player_star_souls WHERE player_id = ?`, sourceID).Scan(&sourceSouls); err != nil {
		return err
	}
	if sourceItems != expectedItems {
		return fmt.Errorf("source item count changed: %d", sourceItems)
	}
	for _, plan := range plans {
		var job int32
		var items, souls, vice, slots int
		if err := db.QueryRowContext(ctx, `SELECT job_id FROM players WHERE id = ?`, plan.playerID).Scan(&job); err != nil {
			return err
		}
		if job != plan.spec.JobID {
			return fmt.Errorf("clone %s job=%d want=%d", plan.spec.Account, job, plan.spec.JobID)
		}
		for query, dest := range map[string]*int{`SELECT COUNT(*) FROM player_items WHERE player_id = ?`: &items, `SELECT COUNT(*) FROM player_star_souls WHERE player_id = ?`: &souls, `SELECT COUNT(*) FROM player_star_soul_vice_attributes WHERE player_id = ?`: &vice, `SELECT COUNT(*) FROM player_star_soul_slots WHERE player_id = ?`: &slots} {
			if err := db.QueryRowContext(ctx, query, plan.playerID).Scan(dest); err != nil {
				return err
			}
		}
		if items != sourceItems || souls != sourceSouls || vice != 46 || slots != 12 {
			return fmt.Errorf("clone %s counts items=%d souls=%d vice=%d slots=%d", plan.spec.Account, items, souls, vice, slots)
		}
	}
	return nil
}
