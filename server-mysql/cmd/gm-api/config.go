package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-json"
	"gopkg.in/yaml.v3"
	"mhqserver/internal/operationsconfig"
)

var editableGMConfigs = map[string]bool{
	"Gameplay": true, "ShopBase": true, "MarketBase": true, "MultiShop": true,
	"ActivePerDayConfig": true, "WorldBossConfig": true, "FamilyBossConfig": true, "CustomSkins": true,
}

type configDraftRequest struct {
	ContentYAML  string `json:"contentYaml"`
	BaseRevision int64  `json:"baseRevision"`
}

func (a *App) handleConfigs(w http.ResponseWriter, r *http.Request, rid string, session *sessionData) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/configs/"), "/")
	if path == "/api/v1/configs" {
		if r.Method != http.MethodGet || !hasPermission(session, "configs.read") {
			writeError(w, http.StatusForbidden, rid, "forbidden", "没有配置查询权限")
			return
		}
		a.listConfigs(w, r, rid)
		return
	}
	if len(parts) == 0 || parts[0] == "" || !editableGMConfigs[parts[0]] {
		writeError(w, http.StatusNotFound, rid, "config_not_found", "配置不存在或不允许编辑")
		return
	}
	name := parts[0]
	if len(parts) == 1 && r.Method == http.MethodGet {
		if !hasPermission(session, "configs.read") {
			writeError(w, http.StatusForbidden, rid, "forbidden", "没有配置查询权限")
			return
		}
		a.getConfig(w, r, rid, name)
		return
	}
	if len(parts) >= 2 && parts[1] == "revisions" && r.Method == http.MethodGet {
		if !hasPermission(session, "configs.read") {
			writeError(w, http.StatusForbidden, rid, "forbidden", "没有配置查询权限")
			return
		}
		a.configRevisions(w, r, rid, name)
		return
	}
	if len(parts) >= 2 && parts[1] == "drafts" {
		if len(parts) == 2 && r.Method == http.MethodPost {
			if !hasPermission(session, "configs.publish") {
				writeError(w, http.StatusForbidden, rid, "forbidden", "没有配置发布权限")
				return
			}
			if !a.checkCSRF(r, session) {
				writeError(w, http.StatusForbidden, rid, "csrf_failed", "请求校验失败")
				return
			}
			a.createDraft(w, r, rid, name, session)
			return
		}
		if len(parts) == 3 && r.Method == http.MethodGet {
			if !hasPermission(session, "configs.read") {
				writeError(w, http.StatusForbidden, rid, "forbidden", "没有配置查询权限")
				return
			}
			a.getDraft(w, r, rid, name, parseInt64Param(parts[2], 0))
			return
		}
	}
	if len(parts) >= 2 && parts[1] == "validate" && r.Method == http.MethodPost {
		if !hasPermission(session, "configs.publish") {
			writeError(w, http.StatusForbidden, rid, "forbidden", "没有配置发布权限")
			return
		}
		if !a.checkCSRF(r, session) {
			writeError(w, http.StatusForbidden, rid, "csrf_failed", "请求校验失败")
			return
		}
		a.validateConfigBody(w, r, rid, name)
		return
	}
	if len(parts) >= 2 && parts[1] == "publish" && r.Method == http.MethodPost {
		if !hasPermission(session, "configs.publish") {
			writeError(w, http.StatusForbidden, rid, "forbidden", "没有配置发布权限")
			return
		}
		if !a.checkCSRF(r, session) {
			writeError(w, http.StatusForbidden, rid, "csrf_failed", "请求校验失败")
			return
		}
		a.publishConfig(w, r, rid, name, session)
		return
	}
	if len(parts) == 4 && parts[1] == "revisions" && parts[2] != "" && parts[3] == "rollback" && r.Method == http.MethodPost {
		if !hasPermission(session, "configs.publish") {
			writeError(w, http.StatusForbidden, rid, "forbidden", "没有配置发布权限")
			return
		}
		if !a.checkCSRF(r, session) {
			writeError(w, http.StatusForbidden, rid, "csrf_failed", "请求校验失败")
			return
		}
		a.rollbackConfig(w, r, rid, name, parseInt64Param(parts[2], 0), session)
		return
	}
	writeError(w, http.StatusNotFound, rid, "not_found", "配置接口不存在")
}

func (a *App) listConfigs(w http.ResponseWriter, r *http.Request, rid string) {
	ctx, cancel := a.requestContext(r)
	defer cancel()
	rows, err := a.db.QueryContext(ctx, `SELECT config_name, COUNT(*) AS nodes FROM game_config_nodes GROUP BY config_name ORDER BY config_name`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, rid, "query_failed", "配置列表查询失败")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var name string
		var nodes int64
		if err := rows.Scan(&name, &nodes); err != nil {
			writeError(w, 500, rid, "query_failed", "配置列表查询失败")
			return
		}
		if editableGMConfigs[name] {
			items = append(items, map[string]any{"name": name, "nodes": strconvI64(nodes)})
		}
	}
	writeJSON(w, http.StatusOK, rid, map[string]any{"items": items}, nil)
}

func (a *App) getConfig(w http.ResponseWriter, r *http.Request, rid, name string) {
	ctx, cancel := a.requestContext(r)
	defer cancel()
	value, err := a.readConfig(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, rid, "config_not_found", "配置尚未发布")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, rid, "query_failed", "配置读取失败")
		return
	}
	encoded, _ := yaml.Marshal(value)
	var revision sql.NullInt64
	_ = a.db.QueryRowContext(ctx, `SELECT MAX(revision) FROM game_config_revisions WHERE note LIKE CONCAT('[GM:', ?, ']%')`, name).Scan(&revision)
	writeJSON(w, http.StatusOK, rid, map[string]any{"name": name, "revision": strconvNullInt64(revision), "contentYaml": string(encoded), "content": value}, nil)
}

func (a *App) createDraft(w http.ResponseWriter, r *http.Request, rid, name string, session *sessionData) {
	var req configDraftRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil || strings.TrimSpace(req.ContentYAML) == "" {
		writeError(w, http.StatusBadRequest, rid, "invalid_yaml", "请输入 YAML 配置")
		return
	}
	value, err := parseConfigYAML(req.ContentYAML)
	if err == nil {
		err = validateGMConfig(name, value)
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, rid, "config_invalid", err.Error())
		return
	}
	hash := fmt.Sprintf("%x", sha256Bytes([]byte(req.ContentYAML)))
	now := time.Now().Unix()
	ctx, cancel := a.requestContext(r)
	defer cancel()
	result, err := a.db.ExecContext(ctx, `INSERT INTO gm_config_drafts (config_name, content_yaml, content_hash, base_revision, created_by, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 'draft', ?, ?)`, name, req.ContentYAML, hash, req.BaseRevision, session.AdminID, now, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, rid, "draft_failed", "配置草稿保存失败")
		return
	}
	draftID, _ := result.LastInsertId()
	writeJSON(w, http.StatusCreated, rid, map[string]any{"draftId": strconvI64(draftID), "name": name, "contentHash": hash, "baseRevision": strconvI64(req.BaseRevision)}, nil)
}

func (a *App) getDraft(w http.ResponseWriter, r *http.Request, rid, name string, draftID int64) {
	ctx, cancel := a.requestContext(r)
	defer cancel()
	var yamlText, hash, status string
	var base, createdBy, created, updated int64
	err := a.db.QueryRowContext(ctx, `SELECT content_yaml, content_hash, base_revision, created_by, status, created_at, updated_at FROM gm_config_drafts WHERE draft_id = ? AND config_name = ?`, draftID, name).Scan(&yamlText, &hash, &base, &createdBy, &status, &created, &updated)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, rid, "draft_not_found", "草稿不存在")
		return
	}
	if err != nil {
		writeError(w, 500, rid, "query_failed", "草稿读取失败")
		return
	}
	writeJSON(w, http.StatusOK, rid, map[string]any{"draftId": strconvI64(draftID), "name": name, "contentYaml": yamlText, "contentHash": hash, "baseRevision": strconvI64(base), "createdBy": strconvI64(createdBy), "status": status, "createdAt": strconvI64(created), "updatedAt": strconvI64(updated)}, nil)
}

func (a *App) validateConfigBody(w http.ResponseWriter, r *http.Request, rid, name string) {
	var req configDraftRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		writeError(w, 400, rid, "invalid_yaml", "请输入 YAML 配置")
		return
	}
	value, err := parseConfigYAML(req.ContentYAML)
	if err == nil {
		err = validateGMConfig(name, value)
	}
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, rid, nil, &apiError{Code: "config_invalid", Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, rid, map[string]any{"valid": true, "contentHash": fmt.Sprintf("%x", sha256Bytes([]byte(req.ContentYAML)))}, nil)
}

func (a *App) publishConfig(w http.ResponseWriter, r *http.Request, rid, name string, session *sessionData) {
	var req struct {
		DraftID     int64  `json:"draftId"`
		ContentYAML string `json:"contentYaml"`
		Note        string `json:"note"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		writeError(w, 400, rid, "invalid_json", "请求格式错误")
		return
	}
	if req.DraftID > 0 && strings.TrimSpace(req.ContentYAML) == "" {
		_ = a.db.QueryRowContext(r.Context(), `SELECT content_yaml FROM gm_config_drafts WHERE draft_id = ? AND config_name = ?`, req.DraftID, name).Scan(&req.ContentYAML)
	}
	value, err := parseConfigYAML(req.ContentYAML)
	if err == nil {
		err = validateGMConfig(name, value)
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, rid, "config_invalid", "配置校验失败: "+err.Error())
		return
	}
	ctx, cancel := a.requestContext(r)
	defer cancel()
	revision, err := replaceGMConfig(ctx, a.db, name, value, session.Username, req.Note)
	if err != nil {
		writeError(w, 500, rid, "publish_failed", "配置发布失败")
		return
	}
	hash := fmt.Sprintf("%x", sha256Bytes([]byte(req.ContentYAML)))
	_, _ = a.db.ExecContext(ctx, `INSERT INTO gm_config_snapshots (revision, config_name, content_yaml, content_hash, publisher, note, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, revision, name, req.ContentYAML, hash, session.Username, req.Note, time.Now().Unix())
	if a.redis != nil {
		_ = a.redis.Publish(ctx, "mhq:config:reload", fmt.Sprintf("%d|%s", revision, name)).Err()
	}
	if req.DraftID > 0 {
		_, _ = a.db.ExecContext(ctx, `UPDATE gm_config_drafts SET status = 'published', updated_at = ? WHERE draft_id = ?`, time.Now().Unix(), req.DraftID)
	}
	_ = a.writeAudit(ctx, auditRecord{RequestID: rid, AdminID: session.AdminID, Action: "config.publish", TargetType: "config", TargetID: name, Reason: req.Note, Result: "completed", ClientIP: clientIP(r), UserAgent: r.UserAgent()})
	writeJSON(w, http.StatusOK, rid, map[string]any{"name": name, "revision": strconvI64(revision), "contentHash": hash}, nil)
}

func (a *App) configRevisions(w http.ResponseWriter, r *http.Request, rid, name string) {
	ctx, cancel := a.requestContext(r)
	defer cancel()
	rows, err := a.db.QueryContext(ctx, `SELECT revision, published_at, publisher, note FROM game_config_revisions WHERE note LIKE CONCAT('[GM:', ?, ']%') ORDER BY revision DESC LIMIT 100`, name)
	if err != nil {
		writeError(w, 500, rid, "query_failed", "配置版本查询失败")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var revision, published int64
		var publisher, note string
		if err := rows.Scan(&revision, &published, &publisher, &note); err == nil {
			items = append(items, map[string]any{"revision": strconvI64(revision), "publishedAt": strconvI64(published), "publisher": publisher, "note": strings.TrimPrefix(note, "[GM:"+name+"] ")})
		}
	}
	writeJSON(w, 200, rid, map[string]any{"items": items}, nil)
}

func (a *App) rollbackConfig(w http.ResponseWriter, r *http.Request, rid, name string, revision int64, session *sessionData) {
	if revision <= 0 {
		writeError(w, 400, rid, "invalid_revision", "版本号无效")
		return
	}
	ctx, cancel := a.requestContext(r)
	defer cancel()
	var yamlText string
	if err := a.db.QueryRowContext(ctx, `SELECT content_yaml FROM gm_config_snapshots WHERE revision = ? AND config_name = ?`, revision, name).Scan(&yamlText); err != nil {
		writeError(w, http.StatusNotFound, rid, "snapshot_not_found", "找不到可回滚的配置快照")
		return
	}
	value, err := parseConfigYAML(yamlText)
	if err == nil {
		err = validateGMConfig(name, value)
	}
	if err != nil {
		writeError(w, 422, rid, "config_invalid", "快照校验失败")
		return
	}
	newRevision, err := replaceGMConfig(ctx, a.db, name, value, session.Username, fmt.Sprintf("rollback from %d", revision))
	if err != nil {
		writeError(w, 500, rid, "rollback_failed", "配置回滚失败")
		return
	}
	hash := fmt.Sprintf("%x", sha256Bytes([]byte(yamlText)))
	_, _ = a.db.ExecContext(ctx, `INSERT INTO gm_config_snapshots (revision, config_name, content_yaml, content_hash, publisher, note, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, newRevision, name, yamlText, hash, session.Username, fmt.Sprintf("rollback from %d", revision), time.Now().Unix())
	if a.redis != nil {
		_ = a.redis.Publish(ctx, "mhq:config:reload", fmt.Sprintf("%d|%s", newRevision, name)).Err()
	}
	writeJSON(w, 200, rid, map[string]any{"name": name, "revision": strconvI64(newRevision), "rolledBackFrom": strconvI64(revision)}, nil)
}

func parseConfigYAML(text string) (any, error) {
	var value any
	if err := yaml.Unmarshal([]byte(text), &value); err != nil {
		return nil, err
	}
	return normalizeYAMLValue(value), nil
}
func normalizeYAMLValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, child := range v {
			out[k] = normalizeYAMLValue(child)
		}
		return out
	case map[any]any:
		out := map[string]any{}
		for k, child := range v {
			out[fmt.Sprint(k)] = normalizeYAMLValue(child)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = normalizeYAMLValue(child)
		}
		return out
	case int:
		return int64(v)
	case int32:
		return int64(v)
	case uint:
		return uint64(v)
	case float32:
		return float64(v)
	default:
		return value
	}
}

func validateGMConfig(name string, value any) error {
	if !editableGMConfigs[name] {
		return errors.New("configuration is not editable")
	}
	if name == "Gameplay" {
		_, err := operationsconfig.Decode(value)
		return err
	}
	if name == "CustomSkins" {
		root, ok := value.(map[string]any)
		if !ok || len(root) == 0 {
			return errors.New("CustomSkins must be an object")
		}
		return nil
	}
	rows, ok := value.([]any)
	if !ok {
		return errors.New("datatable root must be an array")
	}
	if len(rows) > 100000 {
		return errors.New("too many rows")
	}
	return nil
}

func replaceGMConfig(ctx context.Context, db *sql.DB, name string, value any, publisher, note string) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM game_config_nodes WHERE config_name = ?`, name); err != nil {
		return 0, err
	}
	if rows, ok := value.([]any); ok {
		for i, raw := range rows {
			pair, ok := raw.([]any)
			if !ok || len(pair) < 2 {
				return 0, fmt.Errorf("row %d invalid", i)
			}
			id, ok := pair[0].(int64)
			if !ok {
				return 0, fmt.Errorf("row %d id invalid", i)
			}
			if _, err := insertGMNode(ctx, tx, name, 0, "", int(id), pair[1]); err != nil {
				return 0, err
			}
		}
	} else if _, err := insertGMNode(ctx, tx, name, 0, "", -1, value); err != nil {
		return 0, err
	}
	note = "[GM:" + name + "] " + truncate(note, 240)
	result, err := tx.ExecContext(ctx, `INSERT INTO game_config_revisions (published_at, publisher, note) VALUES (?, ?, ?)`, time.Now().Unix(), publisher, note)
	if err != nil {
		return 0, err
	}
	revision, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return revision, nil
}

func insertGMNode(ctx context.Context, tx *sql.Tx, name string, parent int64, field string, index int, value any) (int64, error) {
	kind, vt, iv, fv, sv, bv := classifyGMValue(value)
	result, err := tx.ExecContext(ctx, `INSERT INTO game_config_nodes (config_name, parent_id, field_name, array_index, node_kind, value_type, int_value, float_value, string_value, bool_value) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, name, parent, field, index, kind, vt, iv, fv, sv, bv)
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	switch v := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if _, err := insertGMNode(ctx, tx, name, id, k, -1, v[k]); err != nil {
				return 0, err
			}
		}
	case []any:
		for i, child := range v {
			if _, err := insertGMNode(ctx, tx, name, id, "", i, child); err != nil {
				return 0, err
			}
		}
	}
	return id, nil
}
func classifyGMValue(value any) (int, int, any, any, any, any) {
	switch v := value.(type) {
	case map[string]any:
		return 1, 0, nil, nil, nil, nil
	case []any:
		return 2, 0, nil, nil, nil, nil
	case int64:
		return 3, 1, v, nil, nil, nil
	case uint64:
		return 3, 1, int64(v), nil, nil, nil
	case float64:
		return 3, 2, nil, v, nil, nil
	case bool:
		if v {
			return 3, 4, nil, nil, nil, int64(1)
		}
		return 3, 4, nil, nil, nil, int64(0)
	case string:
		return 3, 3, nil, nil, v, nil
	case nil:
		return 3, 0, nil, nil, nil, nil
	default:
		return 3, 3, nil, nil, fmt.Sprint(v), nil
	}
}

func (a *App) readConfig(ctx context.Context, name string) (any, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT node_id, parent_id, field_name, array_index, node_kind, value_type, int_value, float_value, string_value, bool_value FROM game_config_nodes WHERE config_name = ? ORDER BY node_id`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := map[int64]*configNode{}
	children := map[int64][]*configNode{}
	for rows.Next() {
		n := &configNode{}
		if err := rows.Scan(&n.id, &n.parent, &n.field, &n.index, &n.kind, &n.valueType, &n.intValue, &n.floatValue, &n.stringValue, &n.boolValue); err != nil {
			return nil, err
		}
		nodes[n.id] = n
		children[n.parent] = append(children[n.parent], n)
	}
	if len(nodes) == 0 {
		return nil, sql.ErrNoRows
	}
	roots := children[0]
	if len(roots) == 0 {
		return nil, sql.ErrNoRows
	}
	if len(roots) == 1 && roots[0].index == -1 {
		return decodeGMNode(roots[0], children), nil
	}
	result := make([]any, 0, len(roots))
	for _, root := range roots {
		if root.index < 0 {
			continue
		}
		result = append(result, []any{int64(root.index), decodeGMNode(root, children)})
	}
	return result, nil
}

type configNode struct {
	id, parent             int64
	field                  string
	index, kind, valueType int
	intValue               sql.NullInt64
	floatValue             sql.NullFloat64
	stringValue            sql.NullString
	boolValue              sql.NullInt64
}

func decodeGMNode(n *configNode, children map[int64][]*configNode) any {
	if n.kind == 1 {
		result := map[string]any{}
		for _, child := range children[n.id] {
			result[child.field] = decodeGMNode(child, children)
		}
		return result
	}
	if n.kind == 2 {
		result := make([]any, 0, len(children[n.id]))
		for _, child := range children[n.id] {
			for len(result) <= child.index {
				result = append(result, nil)
			}
			result[child.index] = decodeGMNode(child, children)
		}
		return result
	}
	switch n.valueType {
	case 1:
		return n.intValue.Int64
	case 2:
		return n.floatValue.Float64
	case 3:
		return n.stringValue.String
	case 4:
		return n.boolValue.Int64 != 0
	default:
		return nil
	}
}
func sha256Bytes(value []byte) []byte { sum := sha256.Sum256(value); return sum[:] }
