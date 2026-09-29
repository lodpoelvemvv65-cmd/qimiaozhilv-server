package main

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"strings"
)

type playerSummary struct {
	ID        int64
	Account   string
	Name      string
	JobID     int32
	Level     int32
	Trans     int32
	FamilyID  int64
	MapID     int32
	LastLogin int64
	Coin      int64
	YuanBao   int64
	Voucher   int64
	Honor     int64
	PVP       int64
	StoreCoin int64
}

func (a *App) handleAccounts(w http.ResponseWriter, r *http.Request, rid string, session *sessionData) {
	if r.Method != http.MethodGet || session == nil {
		writeError(w, http.StatusUnauthorized, rid, "unauthorized", "请先登录")
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("query"))
	limit := clampLimit(r.URL.Query().Get("limit"), 25)
	cursor := parseInt64Param(r.URL.Query().Get("cursor"), 0)
	ctx, cancel := a.requestContext(r)
	defer cancel()
	rows, err := a.db.QueryContext(ctx, `SELECT id, account, create_time,
		(SELECT COUNT(*) FROM players p WHERE p.account_id = a.id) AS player_count
		FROM accounts a WHERE a.id > ? AND (? = '' OR account LIKE CONCAT('%', ?, '%'))
		ORDER BY a.id LIMIT ?`, cursor, query, query, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, rid, "query_failed", "账号查询失败")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0, limit)
	for rows.Next() {
		var id, created, playerCount int64
		var account string
		if err := rows.Scan(&id, &account, &created, &playerCount); err != nil {
			writeError(w, http.StatusInternalServerError, rid, "query_failed", "账号查询失败")
			return
		}
		items = append(items, map[string]any{"id": strconvI64(id), "account": account,
			"createdAt": strconvI64(created), "playerCount": strconvI64(playerCount)})
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, rid, "query_failed", "账号查询失败")
		return
	}
	writeJSON(w, http.StatusOK, rid, map[string]any{"items": items, "nextCursor": nextCursor(items)}, nil)
}

func (a *App) handlePlayers(w http.ResponseWriter, r *http.Request, rid string, session *sessionData) {
	if r.Method != http.MethodGet || session == nil {
		writeError(w, http.StatusUnauthorized, rid, "unauthorized", "请先登录")
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("query"))
	limit := clampLimit(r.URL.Query().Get("limit"), 25)
	cursor := parseInt64Param(r.URL.Query().Get("cursor"), 0)
	onlineFilter := strings.TrimSpace(r.URL.Query().Get("online"))
	ctx, cancel := a.requestContext(r)
	defer cancel()
	queryLimit := limit
	if onlineFilter == "true" || onlineFilter == "false" {
		// Online state is stored in Redis and cannot be filtered by the SQL
		// LIMIT. Read a wider page so a matching player is not hidden behind
		// the first batch of offline rows.
		queryLimit = limit * 100
		if queryLimit < 250 {
			queryLimit = 250
		}
	}
	playersQuery := `SELECT p.id, a.account, p.name, p.job_id, p.level, p.trans,
		p.family_id, p.map_id, p.last_login, p.coin, p.yuan_bao, p.voucher, p.honor,
		p.pvp_currency, p.store_coin FROM players p JOIN accounts a ON a.id = p.account_id
		WHERE p.id > ? AND (? = '' OR p.name LIKE CONCAT('%', ?, '%') OR a.account LIKE CONCAT('%', ?, '%'))
		`
	playerArgs := []any{cursor, query, query, query}
	if isSubGM(session) {
		playersQuery += ` AND EXISTS (SELECT 1 FROM gm_sub_gm_bindings b WHERE b.account_id = p.account_id AND b.sub_gm_id = ?)`
		playerArgs = append(playerArgs, session.AdminID)
	}
	playersQuery += ` ORDER BY p.id LIMIT ?`
	playerArgs = append(playerArgs, queryLimit)
	rows, err := a.db.QueryContext(ctx, playersQuery, playerArgs...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, rid, "query_failed", "角色查询失败")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0, limit)
	for rows.Next() {
		player, err := scanPlayerSummary(rows)
		if err != nil {
			writeError(w, http.StatusInternalServerError, rid, "query_failed", "角色查询失败")
			return
		}
		online, onlineErr := a.isOnline(ctx, player.ID)
		if onlineErr != nil {
			online = false
		}
		if onlineFilter == "true" && !online || onlineFilter == "false" && online {
			continue
		}
		if len(items) < limit {
			items = append(items, playerSummaryJSON(player, online))
		}
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, rid, "query_failed", "角色查询失败")
		return
	}
	writeJSON(w, http.StatusOK, rid, map[string]any{"items": items, "nextCursor": nextCursor(items)}, nil)
}

type scanner interface{ Scan(...any) error }

func scanPlayerSummary(row scanner) (playerSummary, error) {
	var p playerSummary
	err := row.Scan(&p.ID, &p.Account, &p.Name, &p.JobID, &p.Level, &p.Trans, &p.FamilyID, &p.MapID,
		&p.LastLogin, &p.Coin, &p.YuanBao, &p.Voucher, &p.Honor, &p.PVP, &p.StoreCoin)
	return p, err
}

func playerSummaryJSON(p playerSummary, online bool) map[string]any {
	return map[string]any{
		"id": strconvI64(p.ID), "account": p.Account, "name": p.Name, "jobId": p.JobID,
		"level": p.Level, "trans": p.Trans, "familyId": strconvI64(p.FamilyID), "mapId": p.MapID,
		"lastLogin": strconvI64(p.LastLogin), "coin": strconvI64(p.Coin), "yuanBao": strconvI64(p.YuanBao),
		"voucher": strconvI64(p.Voucher), "honor": strconvI64(p.Honor), "pvpCurrency": strconvI64(p.PVP),
		"storeCoin": strconvI64(p.StoreCoin), "online": online,
	}
}

func (a *App) isOnline(ctx context.Context, playerID int64) (bool, error) {
	if a.redis == nil {
		return false, sql.ErrConnDone
	}
	count, err := a.redis.Exists(ctx, "mhq:online:player:"+strconv.FormatInt(playerID, 10)).Result()
	return count > 0, err
}

func (a *App) handlePlayerDetail(w http.ResponseWriter, r *http.Request, rid string, session *sessionData, playerID int64, subresource string) {
	if r.Method != http.MethodGet || session == nil {
		writeError(w, http.StatusUnauthorized, rid, "unauthorized", "请先登录")
		return
	}
	ctx, cancel := a.requestContext(r)
	defer cancel()
	visible, err := a.subGMCanViewPlayer(ctx, session, playerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, rid, "query_failed", "角色权限查询失败")
		return
	}
	if !visible {
		writeError(w, http.StatusNotFound, rid, "player_not_found", "角色不存在")
		return
	}
	switch subresource {
	case "", "detail":
		a.playerDetail(w, r, rid, ctx, playerID)
	case "inventory", "equipment", "skills", "tasks", "activity", "mails", "pets":
		a.playerSubresource(w, rid, ctx, playerID, subresource)
	default:
		if extraPlayerSubresources[subresource] == "" {
			writeError(w, http.StatusNotFound, rid, "not_found", "资源不存在")
			return
		}
		a.playerSubresource(w, rid, ctx, playerID, subresource)
	}
}

func (a *App) playerDetail(w http.ResponseWriter, _ *http.Request, rid string, ctx context.Context, playerID int64) {
	row := a.db.QueryRowContext(ctx, `SELECT p.id, a.account, p.name, p.job_id, p.skin_id, p.title_id,
		p.level, p.exp, p.energy, p.map_id, p.pos_x, p.pos_y, p.char_point, p.skill_point,
		p.str_add, p.quk_add, p.spi_add, p.wim_add, p.phy_add, p.sta_add, p.auto_battle,
		p.coin, p.yuan_bao, p.voucher, p.honor, p.pvp_currency, p.store_coin, p.store_pages,
		p.trans, p.family_id, p.family_contribute, p.personal_contribute, p.last_login
		FROM players p JOIN accounts a ON a.id = p.account_id WHERE p.id = ?`, playerID)
	var id, exp, coin, yuanBao, voucher, honor, pvp, storeCoin, familyID int64
	var account, name string
	var jobID, skinID, titleID, level, energy, mapID, charPoint, skillPoint, strAdd, qukAdd, spiAdd, wimAdd, phyAdd, staAdd, autoBattle, storePages, trans, familyContribute, personalContribute int32
	var posX, posY float64
	var lastLogin int64
	err := row.Scan(&id, &account, &name, &jobID, &skinID, &titleID, &level, &exp, &energy, &mapID, &posX, &posY,
		&charPoint, &skillPoint, &strAdd, &qukAdd, &spiAdd, &wimAdd, &phyAdd, &staAdd, &autoBattle, &coin,
		&yuanBao, &voucher, &honor, &pvp, &storeCoin, &storePages, &trans, &familyID, &familyContribute,
		&personalContribute, &lastLogin)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, rid, "player_not_found", "角色不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, rid, "query_failed", "角色详情查询失败")
		return
	}
	online, _ := a.isOnline(ctx, id)
	data := map[string]any{"id": strconvI64(id), "account": account, "name": name, "jobId": jobID,
		"skinId": skinID, "titleId": titleID, "level": level, "exp": strconvI64(exp), "energy": energy,
		"mapId": mapID, "posX": posX, "posY": posY, "charPoint": charPoint, "skillPoint": skillPoint,
		"attributes": map[string]int32{"str": strAdd, "quk": qukAdd, "spi": spiAdd, "wim": wimAdd, "phy": phyAdd, "sta": staAdd},
		"autoBattle": autoBattle, "coin": strconvI64(coin), "yuanBao": strconvI64(yuanBao), "voucher": strconvI64(voucher),
		"honor": strconvI64(honor), "pvpCurrency": strconvI64(pvp), "storeCoin": strconvI64(storeCoin),
		"storePages": storePages, "trans": trans, "familyId": strconvI64(familyID), "familyContribute": familyContribute,
		"personalContribute": personalContribute, "lastLogin": strconvI64(lastLogin), "online": online,
		"realtimeOnline": nil, "sessionState": map[bool]string{true: "redis-marked", false: "offline"}[online]}
	writeJSON(w, http.StatusOK, rid, data, nil)
}

func (a *App) playerSubresource(w http.ResponseWriter, rid string, ctx context.Context, playerID int64, resource string) {
	queries := map[string]string{
		"inventory": `SELECT location, slot_index, item_id, item_type, server_id, item_count, is_locked, quality, star, strength_level, special_key, special_id, get_source FROM player_items WHERE player_id = ? ORDER BY location, slot_index`,
		"equipment": `SELECT location, slot_index, item_id, item_type, server_id, item_count, is_locked, quality, star, strength_level, special_key, special_id, get_source FROM player_items WHERE player_id = ? AND location = 1 ORDER BY slot_index`,
		"skills":    `SELECT skill_id, level, sort_order FROM player_skills WHERE player_id = ? ORDER BY sort_order, skill_id`,
		"tasks":     `SELECT task_id, state, kill_count FROM player_tasks WHERE player_id = ? ORDER BY task_id`,
		"activity":  `SELECT last_day, last_month, month_count, map_coin_day, map_coin_extra, map_coin_claimed, trial_highest_id, trial_reward_day, pvp_score, pvp_battle_day, pvp_battle_count, pvp_match_count, dungeon_quota_day, space_travel_remaining, death_tower_remaining, family_boss_keys, normal_run_day FROM player_activity WHERE player_id = ?`,
		"mails":     `SELECT mail_id, title, content, sender_name, state, remain_time FROM player_mails WHERE player_id = ? ORDER BY mail_id DESC`,
		"pets":      `SELECT pet_id, name, level, exp, intimacy, is_show, active, eat_count, pet_state, action_end, last_day, rewarded FROM player_pets WHERE player_id = ?`,
	}
	query := queries[resource]
	if query == "" {
		query = extraPlayerSubresources[resource]
	}
	rows, err := a.db.QueryContext(ctx, query, playerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, rid, "query_failed", "角色子资源查询失败")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		values, err := scanDynamic(rows)
		if err != nil {
			writeError(w, http.StatusInternalServerError, rid, "query_failed", "角色子资源查询失败")
			return
		}
		items = append(items, values)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, rid, "query_failed", "角色子资源查询失败")
		return
	}
	if resource == "skills" {
		a.attachSkillNames(ctx, items)
	}
	writeJSON(w, http.StatusOK, rid, map[string]any{"resource": resource, "items": items}, nil)
}

func scanDynamic(rows *sql.Rows) (map[string]any, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	values := make([]any, len(columns))
	dest := make([]any, len(columns))
	for i := range values {
		dest[i] = &values[i]
	}
	if err := rows.Scan(dest...); err != nil {
		return nil, err
	}
	result := make(map[string]any, len(values))
	for i, column := range columns {
		result[column] = normalizeSQLValue(values[i])
	}
	return result, nil
}

func normalizeSQLValue(value any) any {
	switch v := value.(type) {
	case int64:
		return strconvI64(v)
	case []byte:
		return string(v)
	default:
		return v
	}
}
