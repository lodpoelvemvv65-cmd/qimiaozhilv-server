package main

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"time"
)

func (a *App) handleFamilies(w http.ResponseWriter, r *http.Request, rid string, session *sessionData) {
	if r.Method != http.MethodGet || session == nil {
		writeError(w, http.StatusUnauthorized, rid, "unauthorized", "请先登录")
		return
	}
	limit := clampLimit(r.URL.Query().Get("limit"), 25)
	cursor := parseInt64Param(r.URL.Query().Get("cursor"), 0)
	query := strings.TrimSpace(r.URL.Query().Get("query"))
	ctx, cancel := a.requestContext(r)
	defer cancel()
	rows, err := a.db.QueryContext(ctx, `SELECT f.id, f.name, f.leader, f.level, f.hornor, f.notice,
		(SELECT COUNT(*) FROM family_members m WHERE m.family_id = f.id) AS member_count
		FROM families f WHERE f.id > ? AND (? = '' OR f.name LIKE CONCAT('%', ?, '%'))
		ORDER BY f.id LIMIT ?`, cursor, query, query, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, rid, "query_failed", "家族查询失败")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0, limit)
	for rows.Next() {
		var id, leader, honor, members int64
		var name, notice string
		var level int32
		if err := rows.Scan(&id, &name, &leader, &level, &honor, &notice, &members); err != nil {
			writeError(w, http.StatusInternalServerError, rid, "query_failed", "家族查询失败")
			return
		}
		items = append(items, map[string]any{"id": strconvI64(id), "name": name, "leader": strconvI64(leader),
			"level": level, "honor": strconvI64(honor), "notice": notice, "memberCount": strconvI64(members)})
	}
	writeJSON(w, http.StatusOK, rid, map[string]any{"items": items, "nextCursor": nextCursor(items)}, nil)
}

func (a *App) handleConsignments(w http.ResponseWriter, r *http.Request, rid string, session *sessionData) {
	if r.Method != http.MethodGet || session == nil {
		writeError(w, http.StatusUnauthorized, rid, "unauthorized", "请先登录")
		return
	}
	limit := clampLimit(r.URL.Query().Get("limit"), 25)
	cursor := parseInt64Param(r.URL.Query().Get("cursor"), 0)
	ctx, cancel := a.requestContext(r)
	defer cancel()
	rows, err := a.db.QueryContext(ctx, `SELECT id, seller_id, seller_name, item_id, item_type, count,
		price, need_pwd, put_at, special_key, is_lock, star, quality, level, special_id
		FROM consignment_items WHERE id > ? ORDER BY id LIMIT ?`, cursor, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, rid, "query_failed", "寄售查询失败")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0, limit)
	for rows.Next() {
		var id, sellerID, itemID, count, price, putAt, specialKey int64
		var seller string
		var itemType, needPwd, lock, star, quality, level, specialID int32
		if err := rows.Scan(&id, &sellerID, &seller, &itemID, &itemType, &count, &price, &needPwd, &putAt,
			&specialKey, &lock, &star, &quality, &level, &specialID); err != nil {
			writeError(w, http.StatusInternalServerError, rid, "query_failed", "寄售查询失败")
			return
		}
		items = append(items, map[string]any{"id": strconvI64(id), "sellerId": strconvI64(sellerID), "sellerName": seller,
			"itemId": strconvI64(itemID), "itemType": itemType, "count": strconvI64(count), "price": strconvI64(price),
			"needPassword": needPwd != 0, "putAt": strconvI64(putAt), "specialKey": strconvI64(specialKey),
			"locked": lock != 0, "star": star, "quality": quality, "level": level, "specialId": specialID})
	}
	writeJSON(w, http.StatusOK, rid, map[string]any{"items": items, "nextCursor": nextCursor(items)}, nil)
}

func (a *App) handleBosses(w http.ResponseWriter, r *http.Request, rid string, session *sessionData) {
	if r.Method != http.MethodGet || session == nil {
		writeError(w, http.StatusUnauthorized, rid, "unauthorized", "请先登录")
		return
	}
	ctx, cancel := a.requestContext(r)
	defer cancel()
	world, err := a.queryBossRows(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, rid, "query_failed", "Boss 状态查询失败")
		return
	}
	writeJSON(w, http.StatusOK, rid, world, nil)
}

func (a *App) queryBossRows(ctx context.Context) (map[string]any, error) {
	result := map[string]any{"world": []map[string]any{}, "family": []map[string]any{}}
	worldRows, err := a.db.QueryContext(ctx, `SELECT layer, dead_at, killer FROM world_boss_states ORDER BY layer`)
	if err != nil {
		return nil, err
	}
	world := make([]map[string]any, 0)
	for worldRows.Next() {
		var layer int32
		var deadAt int64
		var killer string
		if err := worldRows.Scan(&layer, &deadAt, &killer); err != nil {
			worldRows.Close()
			return nil, err
		}
		world = append(world, map[string]any{"layer": layer, "deadAt": strconvI64(deadAt), "killer": killer})
	}
	if err := worldRows.Err(); err != nil {
		worldRows.Close()
		return nil, err
	}
	worldRows.Close()
	familyRows, err := a.db.QueryContext(ctx, `SELECT family_id, boss_id, hp, max_hp, has_reward FROM family_boss_states ORDER BY family_id, boss_id`)
	if err != nil {
		return nil, err
	}
	family := make([]map[string]any, 0)
	for familyRows.Next() {
		var familyID, hp, maxHP int64
		var bossID, reward int32
		if err := familyRows.Scan(&familyID, &bossID, &hp, &maxHP, &reward); err != nil {
			familyRows.Close()
			return nil, err
		}
		family = append(family, map[string]any{"familyId": strconvI64(familyID), "bossId": bossID,
			"hp": strconvI64(hp), "maxHp": strconvI64(maxHP), "hasReward": reward != 0})
	}
	if err := familyRows.Err(); err != nil {
		familyRows.Close()
		return nil, err
	}
	familyRows.Close()
	result["world"] = world
	result["family"] = family
	return result, nil
}

func (a *App) handleCommands(w http.ResponseWriter, r *http.Request, rid string, session *sessionData, request string) {
	if r.Method != http.MethodGet || session == nil {
		writeError(w, http.StatusUnauthorized, rid, "unauthorized", "请先登录")
		return
	}
	ctx, cancel := a.requestContext(r)
	defer cancel()
	// Commands accepted by an older game binary may have no remaining Redis
	// stream entry to consume. Once their TTL passes, converge the database
	// record to a terminal state instead of showing an eternal "pending" row.
	_ = a.expireStaleCommands(ctx)
	if request == "" {
		limit := clampLimit(r.URL.Query().Get("limit"), 25)
		cursor := parseInt64Param(r.URL.Query().Get("cursor"), 0)
		rows, err := a.db.QueryContext(ctx, `SELECT request_id, action, target_id, admin_id, status, payload_hash,
			expected_version, game_revision, error_code, expires_at, created_at, accepted_at, finished_at
			FROM gm_command_records WHERE created_at > ? ORDER BY created_at DESC LIMIT ?`, cursor, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, rid, "query_failed", "命令记录查询失败")
			return
		}
		defer rows.Close()
		items := make([]map[string]any, 0, limit)
		for rows.Next() {
			item, err := scanCommandSummary(rows)
			if err != nil {
				writeError(w, http.StatusInternalServerError, rid, "query_failed", "命令记录查询失败")
				return
			}
			items = append(items, item)
		}
		writeJSON(w, http.StatusOK, rid, map[string]any{"items": items}, nil)
		return
	}
	var action, targetID, status, payloadHash, expectedVersion, errorCode string
	var adminID, revision, expiresAt, createdAt, acceptedAt, finishedAt sql.NullInt64
	var resultJSON sql.NullString
	err := a.db.QueryRowContext(ctx, `SELECT action, target_id, admin_id, status, payload_hash, expected_version,
		game_revision, result_json, error_code, expires_at, created_at, accepted_at, finished_at
		FROM gm_command_records WHERE request_id = ?`, request).Scan(&action, &targetID, &adminID, &status, &payloadHash,
		&expectedVersion, &revision, &resultJSON, &errorCode, &expiresAt, &createdAt, &acceptedAt, &finishedAt)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, rid, "command_not_found", "命令记录不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, rid, "query_failed", "命令记录查询失败")
		return
	}
	writeJSON(w, http.StatusOK, rid, map[string]any{"requestId": request, "action": action, "targetId": targetID,
		"adminId": strconvNullInt64(adminID), "status": status, "payloadHash": payloadHash, "expectedVersion": expectedVersion,
		"gameRevision": strconvNullInt64(revision), "resultJson": nullString(resultJSON), "errorCode": errorCode,
		"expiresAt": strconvNullInt64(expiresAt), "createdAt": strconvNullInt64(createdAt), "acceptedAt": strconvNullInt64(acceptedAt),
		"finishedAt": strconvNullInt64(finishedAt)}, nil)
}

func (a *App) expireStaleCommands(ctx context.Context) error {
	if a == nil || a.db == nil {
		return nil
	}
	_, err := a.db.ExecContext(ctx, `UPDATE gm_command_records
		SET status = 'rejected', error_code = 'command_expired',
			finished_at = COALESCE(finished_at, ?)
		WHERE status IN ('accepted', 'pending') AND expires_at > 0 AND expires_at < ?`, time.Now().Unix(), time.Now().Unix())
	if err != nil {
		return err
	}
	_, err = a.db.ExecContext(ctx, `UPDATE gm_audit_logs a
		JOIN gm_command_records c ON c.request_id = a.idempotency_key
		SET a.result = 'rejected', a.error_code = 'command_expired'
		WHERE c.status = 'rejected' AND c.error_code = 'command_expired' AND a.result = 'pending'`)
	return err
}

func scanCommandSummary(row scanner) (map[string]any, error) {
	var request, action, targetID, status, hash, expected, errorCode string
	var adminID, revision, expiresAt, createdAt, acceptedAt, finishedAt sql.NullInt64
	if err := row.Scan(&request, &action, &targetID, &adminID, &status, &hash, &expected, &revision, &errorCode,
		&expiresAt, &createdAt, &acceptedAt, &finishedAt); err != nil {
		return nil, err
	}
	return map[string]any{"requestId": request, "action": action, "targetId": targetID, "adminId": strconvNullInt64(adminID),
		"status": status, "payloadHash": hash, "expectedVersion": expected, "gameRevision": strconvNullInt64(revision),
		"errorCode": errorCode, "expiresAt": strconvNullInt64(expiresAt), "createdAt": strconvNullInt64(createdAt),
		"acceptedAt": strconvNullInt64(acceptedAt), "finishedAt": strconvNullInt64(finishedAt)}, nil
}

func strconvNullInt64(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return strconvI64(value.Int64)
}

func nullString(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}
