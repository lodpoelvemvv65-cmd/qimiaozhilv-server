package main

import (
	"database/sql"
	"fmt"
	"sort"
)

const (
	itemLocationBag   = 1
	itemLocationWorn  = 2
	itemLocationStore = 3
)

type playerRelations struct {
	skills     map[int32]int32
	skillOrder []int32
	autoSkills []int32
	tasks      map[int32]int32
	killCounts map[int32]int32
	bag        map[int32]*bagItem
	worn       map[int32]*bagItem
	store      map[int32]*bagItem
	pet        *petState
	mails      []*mailMsg
	friends    map[int64]*friendInfo
	signin     *signinState
	mainUI     [mainUISlotCount]mainUISlot
	itemBuffs  map[int32]*activeItemBuff
	starSoul   *starSoulBag
}

func savePlayerRelationsTx(tx *sql.Tx, playerID int64, state playerRelations) error {
	deleteTables := []string{
		"player_skills", "player_auto_skills", "player_tasks", "player_kill_counts",
		"player_items", "player_pets", "player_mails", "player_friends", "player_activity",
		"player_month_rewards", "player_task_kill_progress", "player_task_any_kill_progress",
		"player_task_flag_progress", "player_mainui_slots", "player_item_buffs",
	}
	for _, table := range deleteTables {
		if _, err := tx.Exec("DELETE FROM "+table+" WHERE player_id = ?", playerID); err != nil {
			return fmt.Errorf("clear %s: %w", table, err)
		}
	}

	for position, skillID := range state.skillOrder {
		level, ok := state.skills[skillID]
		if !ok || skillID <= 0 {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO player_skills (player_id, skill_id, level, sort_order) VALUES (?, ?, ?, ?)`,
			playerID, skillID, level, position); err != nil {
			return err
		}
	}
	for position, skillID := range state.autoSkills {
		if skillID <= 0 {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO player_auto_skills (player_id, position, skill_id) VALUES (?, ?, ?)`,
			playerID, position, skillID); err != nil {
			return err
		}
	}
	for taskID, taskState := range state.tasks {
		if taskID <= 0 {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO player_tasks (player_id, task_id, state, kill_count) VALUES (?, ?, ?, 0)`,
			playerID, taskID, taskState); err != nil {
			return err
		}
	}
	for monsterID, count := range state.killCounts {
		if monsterID <= 0 {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO player_kill_counts (player_id, monster_id, kill_count) VALUES (?, ?, ?)`,
			playerID, monsterID, count); err != nil {
			return err
		}
	}

	if err := saveItemsTx(tx, playerID, itemLocationBag, state.bag); err != nil {
		return err
	}
	if err := saveItemsTx(tx, playerID, itemLocationWorn, state.worn); err != nil {
		return err
	}
	if err := saveItemsTx(tx, playerID, itemLocationStore, state.store); err != nil {
		return err
	}

	if state.pet != nil {
		p := state.pet
		if _, err := tx.Exec(`INSERT INTO player_pets
			(player_id, pet_id, level, exp, intimacy, name, is_show, active, eat_count, pet_state, action_end, last_day, rewarded)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			playerID, p.PetId, p.Level, p.Exp, p.Intimacy, p.Name, p.IsShow, p.Active, p.EatCount,
			p.PetState, p.ActionEnd, p.LastDay, p.Rewarded); err != nil {
			return err
		}
	}
	for _, mail := range state.mails {
		if mail == nil || mail.Id <= 0 {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO player_mails
			(player_id, mail_id, title, content, sender_name, state, remain_time) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			playerID, mail.Id, mail.Title, mail.Content, mail.SenderName, mail.State, mail.RemainTime); err != nil {
			return err
		}
		for position, item := range mail.Items {
			if _, err := tx.Exec(`INSERT INTO player_mail_items
				(player_id, mail_id, position, item_id, item_count, is_locked, is_has_item) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				playerID, mail.Id, position, item.ItemId, item.Count, item.IsLock, item.IsHasItem); err != nil {
				return err
			}
			if item.PurchaseSource != purchaseSourceNone {
				if _, err := tx.Exec(`INSERT INTO player_mail_item_purchase_origins
					(player_id, mail_id, position, purchase_source, purchase_currency, purchase_unit_price) VALUES (?, ?, ?, ?, ?, ?)`,
					playerID, mail.Id, position, item.PurchaseSource, item.PurchaseCurrency, item.PurchaseUnitPrice); err != nil {
					return err
				}
			}
		}
	}
	for friendID, friend := range state.friends {
		if friend == nil || friendID <= 0 {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO player_friends
			(player_id, friend_id, name, job_id, level, last_login) VALUES (?, ?, ?, ?, ?, ?)`,
			playerID, friendID, friend.Name, friend.Job, friend.Level, friend.LastLogin); err != nil {
			return err
		}
	}
	if err := saveSigninTx(tx, playerID, state.signin); err != nil {
		return err
	}
	for slot, value := range state.mainUI {
		if value.Type == 0 && value.Id == 0 {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO player_mainui_slots (player_id, slot_index, slot_type, object_id) VALUES (?, ?, ?, ?)`,
			playerID, slot, value.Type, value.Id); err != nil {
			return err
		}
	}
	for category, buff := range state.itemBuffs {
		if buff == nil || category <= 0 {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO player_item_buffs
			(player_id, category, item_id, effect_type, expires_at, multiplier, capacity) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			playerID, category, buff.ItemID, buff.EffectType, buff.ExpiresAt, buff.Multiplier, buff.Capacity); err != nil {
			return err
		}
	}
	return nil
}

func saveItemsTx(tx *sql.Tx, playerID int64, location int, items map[int32]*bagItem) error {
	indexes := make([]int, 0, len(items))
	for index, item := range items {
		if item != nil {
			indexes = append(indexes, int(index))
		}
	}
	sort.Ints(indexes)
	for _, rawIndex := range indexes {
		index := int32(rawIndex)
		item := items[index]
		if _, err := tx.Exec(`INSERT INTO player_items
			(player_id, location, slot_index, item_id, item_type, server_id, item_count, is_locked,
			 quality, star, strength_level, special_key, special_id, get_source)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			playerID, location, index, item.ItemId, item.ItemType, item.ServerId, item.Count, item.IsLock,
			item.Quality, item.Star, item.Level, item.SpecialKey, item.SpecialId, item.GetSource); err != nil {
			return err
		}
		if item.PurchaseSource != purchaseSourceNone {
			if _, err := tx.Exec(`INSERT INTO player_item_purchase_origins
				(player_id, location, slot_index, purchase_source, purchase_currency, purchase_unit_price) VALUES (?, ?, ?, ?, ?, ?)`,
				playerID, location, index, item.PurchaseSource, item.PurchaseCurrency, item.PurchaseUnitPrice); err != nil {
				return err
			}
		}
		for key, value := range item.MainAttr {
			if _, err := tx.Exec(`INSERT INTO player_item_main_attributes
				(player_id, location, slot_index, attribute_type, value) VALUES (?, ?, ?, ?, ?)`,
				playerID, location, index, key, value); err != nil {
				return err
			}
		}
		for position, attributeID := range item.RandomAttrs {
			if _, err := tx.Exec(`INSERT INTO player_item_random_attributes
				(player_id, location, slot_index, position, attribute_id) VALUES (?, ?, ?, ?, ?)`,
				playerID, location, index, position, attributeID); err != nil {
				return err
			}
		}
		for position, affixID := range item.AddAttrs {
			if _, err := tx.Exec(`INSERT INTO player_item_affixes
				(player_id, location, slot_index, position, affix_id) VALUES (?, ?, ?, ?, ?)`,
				playerID, location, index, position, affixID); err != nil {
				return err
			}
		}
		for position, gemID := range item.GemList {
			if _, err := tx.Exec(`INSERT INTO player_item_gems
				(player_id, location, slot_index, position, gem_item_id) VALUES (?, ?, ?, ?, ?)`,
				playerID, location, index, position, gemID); err != nil {
				return err
			}
		}
	}
	return nil
}

func saveSigninTx(tx *sql.Tx, playerID int64, state *signinState) error {
	if state == nil {
		return nil
	}
	state.ensureTaskProgress()
	if _, err := tx.Exec(`INSERT INTO player_activity
		(player_id, last_day, last_month, month_count, month_got_month, map_coin_day, map_coin_extra,
		 map_coin_claimed, trial_highest_id, trial_reward_day, manual_equip_tier_1, manual_equip_tier_2,
		 manual_equip_tier_3, manual_rare_craft_count, manual_rare_pity_at, manual_epic_craft_count, manual_epic_pity_at, quiz_score, quiz_time_ms, quiz_completed,
		 pvp_score, pvp_battle_day, pvp_battle_count, pvp_match_count, pvp_is_matching, dungeon_quota_day,
		 space_travel_remaining, death_tower_remaining, family_boss_keys, normal_run_day, online_reward_accumulated_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		playerID, state.LastDay, state.LastMonth, state.MonthCount, state.MonthGotMonth, state.MapCoinDay,
		state.MapCoinExtra, state.MapCoinClaimed, state.TrialHighestID, state.TrialRewardDay,
		state.ManualEquipTier1, state.ManualEquipTier2, state.ManualEquipTier3,
		state.ManualRareCraftCount, state.ManualRarePityAt, state.ManualEpicCraftCount, state.ManualEpicPityAt,
		state.QuizScore, state.QuizTimeMS, state.QuizCompleted, state.PVPScore, state.PVPBattleDay, state.PVPBattleCount,
		state.PVPMatchCount, state.PVPIsMatching, state.DungeonQuotaDay, state.SpaceTravelRemaining,
		state.DeathTowerRemaining, state.FamilyBossKeys, state.NormalRunDay, state.OnlineRewardMS); err != nil {
		return err
	}
	for _, rewardID := range state.MonthGotIDs {
		if _, err := tx.Exec(`INSERT INTO player_month_rewards (player_id, reward_id) VALUES (?, ?)`, playerID, rewardID); err != nil {
			return err
		}
	}
	for taskID, monsters := range state.TaskKillProgress {
		for monsterID, count := range monsters {
			if _, err := tx.Exec(`INSERT INTO player_task_kill_progress
				(player_id, task_id, monster_id, kill_count) VALUES (?, ?, ?, ?)`,
				playerID, taskID, monsterID, count); err != nil {
				return err
			}
		}
	}
	for taskID, count := range state.TaskAnyKillProgress {
		if _, err := tx.Exec(`INSERT INTO player_task_any_kill_progress (player_id, task_id, kill_count) VALUES (?, ?, ?)`,
			playerID, taskID, count); err != nil {
			return err
		}
	}
	for taskID, done := range state.TaskDialogProgress {
		if done {
			if _, err := tx.Exec(`INSERT INTO player_task_flag_progress (player_id, task_id, progress_type) VALUES (?, ?, 1)`, playerID, taskID); err != nil {
				return err
			}
		}
	}
	for taskID, done := range state.TaskQuizProgress {
		if done {
			if _, err := tx.Exec(`INSERT INTO player_task_flag_progress (player_id, task_id, progress_type) VALUES (?, ?, 2)`, playerID, taskID); err != nil {
				return err
			}
		}
	}
	return nil
}

func saveStarSoulTx(tx *sql.Tx, playerID int64, bag *starSoulBag) error {
	if _, err := tx.Exec(`DELETE FROM player_star_soul_slots WHERE player_id = ?`, playerID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM player_star_souls WHERE player_id = ?`, playerID); err != nil {
		return err
	}
	bag = bag.normalize()
	for id, item := range bag.Items {
		if item == nil || id <= 0 {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO player_star_souls
			(player_id, star_soul_id, type_id, level, exp, pos_type, quality, main_attribute, vice_growth_level, is_locked)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, playerID, id, item.TypeID, item.Level, item.Exp,
			item.PosType, item.Quality, item.Main, item.ViceLevel, item.IsLocked); err != nil {
			return err
		}
		for position, attributeType := range item.Vice {
			var value float32
			if position < len(item.ViceAdd) {
				value = item.ViceAdd[position]
			}
			if _, err := tx.Exec(`INSERT INTO player_star_soul_vice_attributes
				(player_id, star_soul_id, position, attribute_type, attribute_add) VALUES (?, ?, ?, ?, ?)`,
				playerID, id, position, attributeType, value); err != nil {
				return err
			}
		}
	}
	for slot, id := range bag.Used {
		if id <= 0 {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO player_star_soul_slots (player_id, slot_index, star_soul_id) VALUES (?, ?, ?)`,
			playerID, slot, id); err != nil {
			return err
		}
	}
	return nil
}

func (st *Store) SaveFriendRequests(playerID int64, from, to map[int64]bool) error {
	st.persistMu.Lock()
	defer st.persistMu.Unlock()
	tx, err := st.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM player_friend_requests WHERE requester_id = ? OR target_id = ?`, playerID, playerID); err != nil {
		return err
	}
	for requesterID, exists := range from {
		if exists && requesterID > 0 {
			if _, err := tx.Exec(`INSERT IGNORE INTO player_friend_requests (requester_id, target_id, created_at) VALUES (?, ?, UNIX_TIMESTAMP())`, requesterID, playerID); err != nil {
				return err
			}
		}
	}
	for targetID, exists := range to {
		if exists && targetID > 0 {
			if _, err := tx.Exec(`INSERT IGNORE INTO player_friend_requests (requester_id, target_id, created_at) VALUES (?, ?, UNIX_TIMESTAMP())`, playerID, targetID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (st *Store) AddFriendRequest(requesterID, targetID int64) error {
	_, err := st.db.Exec(`INSERT INTO player_friend_requests (requester_id, target_id, created_at)
		VALUES (?, ?, UNIX_TIMESTAMP()) ON DUPLICATE KEY UPDATE created_at = VALUES(created_at)`, requesterID, targetID)
	return err
}

func (st *Store) DeleteFriendRequest(requesterID, targetID int64) error {
	_, err := st.db.Exec(`DELETE FROM player_friend_requests WHERE requester_id = ? AND target_id = ?`, requesterID, targetID)
	return err
}

func (st *Store) loadPlayerRelations(player *Player) error {
	if player == nil || player.ID <= 0 {
		return nil
	}
	player.FriendReqFrom = make(map[int64]bool)
	player.FriendReqTo = make(map[int64]bool)
	relations := playerRelations{}

	var skills = make(map[int32]int32)
	var skillOrder []int32
	rows, err := st.db.Query(`SELECT skill_id, level FROM player_skills WHERE player_id = ? ORDER BY sort_order, skill_id`, player.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, level int32
		if err := rows.Scan(&id, &level); err != nil {
			rows.Close()
			return err
		}
		skills[id] = level
		skillOrder = append(skillOrder, id)
	}
	if err := closeRows(rows); err != nil {
		return err
	}
	relations.skills = skills
	relations.skillOrder = skillOrder

	rows, err = st.db.Query(`SELECT skill_id FROM player_auto_skills WHERE player_id = ? ORDER BY position`, player.ID)
	if err != nil {
		return err
	}
	var auto []int32
	for rows.Next() {
		var id int32
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		auto = append(auto, id)
	}
	if err := closeRows(rows); err != nil {
		return err
	}
	relations.autoSkills = auto

	tasks := make(map[int32]int32)
	kills := make(map[int32]int32)
	rows, err = st.db.Query(`SELECT task_id, state FROM player_tasks WHERE player_id = ?`, player.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, state int32
		if err := rows.Scan(&id, &state); err != nil {
			rows.Close()
			return err
		}
		tasks[id] = state
	}
	if err := closeRows(rows); err != nil {
		return err
	}
	rows, err = st.db.Query(`SELECT monster_id, kill_count FROM player_kill_counts WHERE player_id = ?`, player.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, count int32
		if err := rows.Scan(&id, &count); err != nil {
			rows.Close()
			return err
		}
		kills[id] = count
	}
	if err := closeRows(rows); err != nil {
		return err
	}
	relations.tasks = tasks
	relations.killCounts = kills

	bag, worn, store, err := st.loadItems(player.ID)
	if err != nil {
		return err
	}
	relations.bag = bag
	relations.worn = worn
	relations.store = store

	starSoul, err := st.loadStarSoul(player.ID)
	if err != nil {
		return err
	}
	relations.starSoul = starSoul

	var pet petState
	err = st.db.QueryRow(`SELECT pet_id, level, exp, intimacy, name, is_show, active, eat_count, pet_state,
		action_end, last_day, rewarded FROM player_pets WHERE player_id = ?`, player.ID).Scan(
		&pet.PetId, &pet.Level, &pet.Exp, &pet.Intimacy, &pet.Name, &pet.IsShow, &pet.Active,
		&pet.EatCount, &pet.PetState, &pet.ActionEnd, &pet.LastDay, &pet.Rewarded)
	if err == nil {
		relations.pet = &pet
	} else if err != sql.ErrNoRows {
		return err
	}

	mails := make([]*mailMsg, 0)
	mailByID := make(map[int64]*mailMsg)
	rows, err = st.db.Query(`SELECT mail_id, title, content, sender_name, state, remain_time
		FROM player_mails WHERE player_id = ? ORDER BY mail_id`, player.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		mail := &mailMsg{}
		if err := rows.Scan(&mail.Id, &mail.Title, &mail.Content, &mail.SenderName, &mail.State, &mail.RemainTime); err != nil {
			rows.Close()
			return err
		}
		mails = append(mails, mail)
		mailByID[mail.Id] = mail
	}
	if err := closeRows(rows); err != nil {
		return err
	}
	rows, err = st.db.Query(`SELECT mi.mail_id, mi.item_id, mi.item_count, mi.is_locked, mi.is_has_item,
		COALESCE(origin.purchase_source, 0), COALESCE(origin.purchase_currency, 0), COALESCE(origin.purchase_unit_price, 0)
		FROM player_mail_items AS mi
		LEFT JOIN player_mail_item_purchase_origins AS origin
			ON origin.player_id = mi.player_id AND origin.mail_id = mi.mail_id AND origin.position = mi.position
		WHERE mi.player_id = ? ORDER BY mi.mail_id, mi.position`, player.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var mailID int64
		var item mailItemMsg
		if err := rows.Scan(&mailID, &item.ItemId, &item.Count, &item.IsLock, &item.IsHasItem,
			&item.PurchaseSource, &item.PurchaseCurrency, &item.PurchaseUnitPrice); err != nil {
			rows.Close()
			return err
		}
		if mail := mailByID[mailID]; mail != nil {
			mail.Items = append(mail.Items, item)
		}
	}
	if err := closeRows(rows); err != nil {
		return err
	}
	relations.mails = mails

	friends := make(map[int64]*friendInfo)
	rows, err = st.db.Query(`SELECT friend_id, name, job_id, level, last_login FROM player_friends WHERE player_id = ?`, player.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		friend := &friendInfo{}
		if err := rows.Scan(&friend.Id, &friend.Name, &friend.Job, &friend.Level, &friend.LastLogin); err != nil {
			rows.Close()
			return err
		}
		friends[friend.Id] = friend
	}
	if err := closeRows(rows); err != nil {
		return err
	}
	relations.friends = friends

	signin, err := st.loadSignin(player.ID)
	if err != nil {
		return err
	}
	relations.signin = signin

	var mainUI [mainUISlotCount]mainUISlot
	rows, err = st.db.Query(`SELECT slot_index, slot_type, object_id FROM player_mainui_slots WHERE player_id = ?`, player.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var slot int
		var value mainUISlot
		if err := rows.Scan(&slot, &value.Type, &value.Id); err != nil {
			rows.Close()
			return err
		}
		if slot >= 0 && slot < len(mainUI) {
			mainUI[slot] = value
		}
	}
	if err := closeRows(rows); err != nil {
		return err
	}
	relations.mainUI = mainUI

	buffs := make(map[int32]*activeItemBuff)
	rows, err = st.db.Query(`SELECT category, item_id, effect_type, expires_at, multiplier, capacity
		FROM player_item_buffs WHERE player_id = ?`, player.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var category int32
		buff := &activeItemBuff{}
		if err := rows.Scan(&category, &buff.ItemID, &buff.EffectType, &buff.ExpiresAt, &buff.Multiplier, &buff.Capacity); err != nil {
			rows.Close()
			return err
		}
		buffs[category] = buff
	}
	if err := closeRows(rows); err != nil {
		return err
	}
	relations.itemBuffs = buffs

	rows, err = st.db.Query(`SELECT requester_id, target_id FROM player_friend_requests
		WHERE requester_id = ? OR target_id = ?`, player.ID, player.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var requesterID, targetID int64
		if err := rows.Scan(&requesterID, &targetID); err != nil {
			rows.Close()
			return err
		}
		if targetID == player.ID {
			player.FriendReqFrom[requesterID] = true
		}
		if requesterID == player.ID {
			player.FriendReqTo[targetID] = true
		}
	}
	if err := closeRows(rows); err != nil {
		return err
	}
	relations.friends = friends
	player.Relations = relations
	return nil
}

// loadRankingRelations batch-loads only the normalized state used by
// rankingValue: worn equipment with all attribute lists and equipped star
// souls. The query count is constant regardless of the number of players.
func (st *Store) loadRankingRelations(players []*Player) error {
	byID := make(map[int64]*Player, len(players))
	for _, player := range players {
		if player == nil || player.ID <= 0 {
			continue
		}
		if player.Relations.worn == nil {
			player.Relations.worn = make(map[int32]*bagItem)
		}
		if player.Relations.starSoul == nil {
			player.Relations.starSoul = newStarSoulBag()
		}
		byID[player.ID] = player
	}
	if len(byID) == 0 {
		return nil
	}

	rows, err := st.db.Query(`SELECT player_id, slot_index, item_id, item_type, server_id, item_count, is_locked,
		quality, star, strength_level, special_key, special_id, get_source
		FROM player_items WHERE location = ? ORDER BY player_id, slot_index`, itemLocationWorn)
	if err != nil {
		return err
	}
	for rows.Next() {
		var playerID int64
		var slot int32
		item := &bagItem{}
		if err := rows.Scan(&playerID, &slot, &item.ItemId, &item.ItemType, &item.ServerId, &item.Count,
			&item.IsLock, &item.Quality, &item.Star, &item.Level, &item.SpecialKey, &item.SpecialId,
			&item.GetSource); err != nil {
			rows.Close()
			return err
		}
		if player := byID[playerID]; player != nil {
			player.Relations.worn[slot] = item
		}
	}
	if err := closeRows(rows); err != nil {
		return err
	}

	findItem := func(playerID int64, slot int32) *bagItem {
		if player := byID[playerID]; player != nil {
			return player.Relations.worn[slot]
		}
		return nil
	}
	rows, err = st.db.Query(`SELECT player_id, slot_index, attribute_type, value
		FROM player_item_main_attributes WHERE location = ?`, itemLocationWorn)
	if err != nil {
		return err
	}
	for rows.Next() {
		var playerID int64
		var slot, attributeType int32
		var value float32
		if err := rows.Scan(&playerID, &slot, &attributeType, &value); err != nil {
			rows.Close()
			return err
		}
		if item := findItem(playerID, slot); item != nil {
			if item.MainAttr == nil {
				item.MainAttr = make(map[int32]float32)
			}
			item.MainAttr[attributeType] = value
		}
	}
	if err := closeRows(rows); err != nil {
		return err
	}

	loadItemList := func(query string, appendValue func(*bagItem, int32)) error {
		listRows, queryErr := st.db.Query(query, itemLocationWorn)
		if queryErr != nil {
			return queryErr
		}
		for listRows.Next() {
			var playerID int64
			var slot, value int32
			var position int
			if scanErr := listRows.Scan(&playerID, &slot, &position, &value); scanErr != nil {
				listRows.Close()
				return scanErr
			}
			if item := findItem(playerID, slot); item != nil {
				appendValue(item, value)
			}
		}
		return closeRows(listRows)
	}
	if err := loadItemList(`SELECT player_id, slot_index, position, attribute_id
		FROM player_item_random_attributes WHERE location = ? ORDER BY player_id, slot_index, position`,
		func(item *bagItem, value int32) { item.RandomAttrs = append(item.RandomAttrs, value) }); err != nil {
		return err
	}
	if err := loadItemList(`SELECT player_id, slot_index, position, affix_id
		FROM player_item_affixes WHERE location = ? ORDER BY player_id, slot_index, position`,
		func(item *bagItem, value int32) { item.AddAttrs = append(item.AddAttrs, value) }); err != nil {
		return err
	}
	if err := loadItemList(`SELECT player_id, slot_index, position, gem_item_id
		FROM player_item_gems WHERE location = ? ORDER BY player_id, slot_index, position`,
		func(item *bagItem, value int32) { item.GemList = append(item.GemList, value) }); err != nil {
		return err
	}

	rows, err = st.db.Query(`SELECT slots.player_id, slots.slot_index,
		souls.star_soul_id, souls.type_id, souls.level, souls.exp, souls.pos_type,
		souls.quality, souls.main_attribute, souls.vice_growth_level, souls.is_locked
		FROM player_star_soul_slots slots
		JOIN player_star_souls souls ON souls.player_id = slots.player_id
			AND souls.star_soul_id = slots.star_soul_id
		ORDER BY slots.player_id, slots.slot_index`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var playerID int64
		var slot int
		item := &starSoulItem{}
		if err := rows.Scan(&playerID, &slot, &item.ID, &item.TypeID, &item.Level, &item.Exp,
			&item.PosType, &item.Quality, &item.Main, &item.ViceLevel, &item.IsLocked); err != nil {
			rows.Close()
			return err
		}
		if player := byID[playerID]; player != nil && slot >= 0 && slot < starSoulSlotCount {
			player.Relations.starSoul.Items[item.ID] = item
			player.Relations.starSoul.Used[slot] = item.ID
		}
	}
	if err := closeRows(rows); err != nil {
		return err
	}

	rows, err = st.db.Query(`SELECT player_id, star_soul_id, attribute_type, attribute_add
		FROM player_star_soul_vice_attributes ORDER BY player_id, star_soul_id, position`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var playerID, starSoulID int64
		var attributeType int32
		var attributeAdd float32
		if err := rows.Scan(&playerID, &starSoulID, &attributeType, &attributeAdd); err != nil {
			rows.Close()
			return err
		}
		if player := byID[playerID]; player != nil {
			if item := player.Relations.starSoul.Items[starSoulID]; item != nil {
				item.Vice = append(item.Vice, attributeType)
				item.ViceAdd = append(item.ViceAdd, attributeAdd)
			}
		}
	}
	return closeRows(rows)
}

func (st *Store) loadItems(playerID int64) (map[int32]*bagItem, map[int32]*bagItem, map[int32]*bagItem, error) {
	locations := map[int]map[int32]*bagItem{
		itemLocationBag: make(map[int32]*bagItem), itemLocationWorn: make(map[int32]*bagItem), itemLocationStore: make(map[int32]*bagItem),
	}
	rows, err := st.db.Query(`SELECT item.location, item.slot_index, item.item_id, item.item_type, item.server_id,
		item.item_count, item.is_locked, item.quality, item.star, item.strength_level, item.special_key,
		item.special_id, item.get_source, COALESCE(origin.purchase_source, 0),
		COALESCE(origin.purchase_currency, 0), COALESCE(origin.purchase_unit_price, 0)
		FROM player_items AS item
		LEFT JOIN player_item_purchase_origins AS origin
			ON origin.player_id = item.player_id AND origin.location = item.location AND origin.slot_index = item.slot_index
		WHERE item.player_id = ? ORDER BY item.location, item.slot_index`, playerID)
	if err != nil {
		return nil, nil, nil, err
	}
	for rows.Next() {
		var location int
		var slot int32
		item := &bagItem{}
		if err := rows.Scan(&location, &slot, &item.ItemId, &item.ItemType, &item.ServerId, &item.Count,
			&item.IsLock, &item.Quality, &item.Star, &item.Level, &item.SpecialKey, &item.SpecialId, &item.GetSource,
			&item.PurchaseSource, &item.PurchaseCurrency, &item.PurchaseUnitPrice); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		if target := locations[location]; target != nil {
			target[slot] = item
		}
	}
	if err := closeRows(rows); err != nil {
		return nil, nil, nil, err
	}
	find := func(location int, slot int32) *bagItem {
		if target := locations[location]; target != nil {
			return target[slot]
		}
		return nil
	}

	rows, err = st.db.Query(`SELECT location, slot_index, attribute_type, value
		FROM player_item_main_attributes WHERE player_id = ?`, playerID)
	if err != nil {
		return nil, nil, nil, err
	}
	for rows.Next() {
		var location int
		var slot, key int32
		var value float32
		if err := rows.Scan(&location, &slot, &key, &value); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		if item := find(location, slot); item != nil {
			if item.MainAttr == nil {
				item.MainAttr = make(map[int32]float32)
			}
			item.MainAttr[key] = value
		}
	}
	if err := closeRows(rows); err != nil {
		return nil, nil, nil, err
	}

	if err := st.loadItemList(playerID, `SELECT location, slot_index, position, attribute_id
		FROM player_item_random_attributes WHERE player_id = ? ORDER BY location, slot_index, position`,
		find, func(item *bagItem, value int32) { item.RandomAttrs = append(item.RandomAttrs, value) }); err != nil {
		return nil, nil, nil, err
	}
	if err := st.loadItemList(playerID, `SELECT location, slot_index, position, affix_id
		FROM player_item_affixes WHERE player_id = ? ORDER BY location, slot_index, position`,
		find, func(item *bagItem, value int32) { item.AddAttrs = append(item.AddAttrs, value) }); err != nil {
		return nil, nil, nil, err
	}
	if err := st.loadItemList(playerID, `SELECT location, slot_index, position, gem_item_id
		FROM player_item_gems WHERE player_id = ? ORDER BY location, slot_index, position`,
		find, func(item *bagItem, value int32) { item.GemList = append(item.GemList, value) }); err != nil {
		return nil, nil, nil, err
	}
	return locations[itemLocationBag], locations[itemLocationWorn], locations[itemLocationStore], nil
}

func (st *Store) loadItemList(playerID int64, query string, find func(int, int32) *bagItem, appendValue func(*bagItem, int32)) error {
	rows, err := st.db.Query(query, playerID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var location, position int
		var slot, value int32
		if err := rows.Scan(&location, &slot, &position, &value); err != nil {
			rows.Close()
			return err
		}
		if item := find(location, slot); item != nil {
			appendValue(item, value)
		}
	}
	return closeRows(rows)
}

func (st *Store) loadStarSoul(playerID int64) (*starSoulBag, error) {
	bag := newStarSoulBag()
	rows, err := st.db.Query(`SELECT star_soul_id, type_id, level, exp, pos_type, quality, main_attribute, vice_growth_level, is_locked
		FROM player_star_souls WHERE player_id = ?`, playerID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		item := &starSoulItem{}
		if err := rows.Scan(&item.ID, &item.TypeID, &item.Level, &item.Exp, &item.PosType, &item.Quality, &item.Main, &item.ViceLevel, &item.IsLocked); err != nil {
			rows.Close()
			return nil, err
		}
		bag.Items[item.ID] = item
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	rows, err = st.db.Query(`SELECT star_soul_id, attribute_type, attribute_add
		FROM player_star_soul_vice_attributes WHERE player_id = ? ORDER BY star_soul_id, position`, playerID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var attribute int32
		var value float32
		if err := rows.Scan(&id, &attribute, &value); err != nil {
			rows.Close()
			return nil, err
		}
		if item := bag.Items[id]; item != nil {
			item.Vice = append(item.Vice, attribute)
			item.ViceAdd = append(item.ViceAdd, value)
		}
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	rows, err = st.db.Query(`SELECT slot_index, star_soul_id FROM player_star_soul_slots WHERE player_id = ?`, playerID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var slot int
		var id int64
		if err := rows.Scan(&slot, &id); err != nil {
			rows.Close()
			return nil, err
		}
		if slot >= 0 && slot < len(bag.Used) {
			bag.Used[slot] = id
		}
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	return bag.normalize(), nil
}

func (st *Store) loadSignin(playerID int64) (*signinState, error) {
	state := &signinState{}
	err := st.db.QueryRow(`SELECT last_day, last_month, month_count, month_got_month, map_coin_day,
		map_coin_extra, map_coin_claimed, trial_highest_id, trial_reward_day, manual_equip_tier_1,
		manual_equip_tier_2, manual_equip_tier_3, manual_rare_craft_count, manual_rare_pity_at, manual_epic_craft_count, manual_epic_pity_at, quiz_score, quiz_time_ms,
		quiz_completed, pvp_score, pvp_battle_day, pvp_battle_count, pvp_match_count, pvp_is_matching,
		dungeon_quota_day, space_travel_remaining, death_tower_remaining, family_boss_keys,
		normal_run_day,
		online_reward_accumulated_ms
		FROM player_activity WHERE player_id = ?`, playerID).Scan(
		&state.LastDay, &state.LastMonth, &state.MonthCount, &state.MonthGotMonth, &state.MapCoinDay,
		&state.MapCoinExtra, &state.MapCoinClaimed, &state.TrialHighestID, &state.TrialRewardDay,
		&state.ManualEquipTier1, &state.ManualEquipTier2, &state.ManualEquipTier3,
		&state.ManualRareCraftCount, &state.ManualRarePityAt, &state.ManualEpicCraftCount, &state.ManualEpicPityAt,
		&state.QuizScore, &state.QuizTimeMS, &state.QuizCompleted, &state.PVPScore, &state.PVPBattleDay, &state.PVPBattleCount,
		&state.PVPMatchCount, &state.PVPIsMatching, &state.DungeonQuotaDay, &state.SpaceTravelRemaining,
		&state.DeathTowerRemaining, &state.FamilyBossKeys, &state.NormalRunDay, &state.OnlineRewardMS)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	state.ensureTaskProgress()
	rows, err := st.db.Query(`SELECT reward_id FROM player_month_rewards WHERE player_id = ? ORDER BY reward_id`, playerID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int32
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		state.MonthGotIDs = append(state.MonthGotIDs, id)
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	rows, err = st.db.Query(`SELECT task_id, monster_id, kill_count FROM player_task_kill_progress WHERE player_id = ?`, playerID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var taskID, monsterID, count int32
		if err := rows.Scan(&taskID, &monsterID, &count); err != nil {
			rows.Close()
			return nil, err
		}
		if state.TaskKillProgress[taskID] == nil {
			state.TaskKillProgress[taskID] = make(map[int32]int32)
		}
		state.TaskKillProgress[taskID][monsterID] = count
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	rows, err = st.db.Query(`SELECT task_id, kill_count FROM player_task_any_kill_progress WHERE player_id = ?`, playerID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var taskID, count int32
		if err := rows.Scan(&taskID, &count); err != nil {
			rows.Close()
			return nil, err
		}
		state.TaskAnyKillProgress[taskID] = count
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	rows, err = st.db.Query(`SELECT task_id, progress_type FROM player_task_flag_progress WHERE player_id = ?`, playerID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var taskID, progressType int32
		if err := rows.Scan(&taskID, &progressType); err != nil {
			rows.Close()
			return nil, err
		}
		if progressType == 1 {
			state.TaskDialogProgress[taskID] = true
		} else if progressType == 2 {
			state.TaskQuizProgress[taskID] = true
		}
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	return state, nil
}

func closeRows(rows *sql.Rows) error {
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	return rows.Close()
}
