package main

// 启动时的 operation 配置对齐。
//
// config_file_watcher.go 的首次扫描只记录文件指纹、不发布：这样重启不会把每个
// operation 文件都重发一遍、把审计表刷满。但副作用是——**服务端没在跑的时候改的
// YAML 永远发布不出去**，服务端会安静地继续用 MySQL 里的旧值（实例见 文档/14 与
// Gameplay.yaml 的 town_idle_exp）。
//
// 这里补上缺失的另一半：启动时把文件和 MySQL 里已发布的配置树比一比，**只有内容
// 真的不同才发布**。既治好「停机改配置静默失效」，又保持重启幂等——一致的配置不会
// 产生新的 revision，也不会覆盖 MySQL 里已有的内容。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math"
	"path/filepath"
	"strings"
)

// initialConfigSync 在启动扫描时对齐单个 operation 文件。
// 返回 nil 表示「已一致、跳过」或「已发布」；返回错误表示对齐本身失败。
func initialConfigSync(ctx context.Context, db *sql.DB, cache *RedisCache, path, publisher, note string) error {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if db == nil {
		return fmt.Errorf("MySQL database is not initialized")
	}
	value, err := decodeRuntimeConfigSource(path)
	if err != nil {
		// 文件本身读不出来。仍然交给 publishConfigFile：它会先校验、把具体原因
		// 打到日志里，校验不过就不会碰 MySQL。
		return publishConfigFile(ctx, db, cache, path, publisher, note)
	}
	same, err := configFileMatchesPublished(ctx, db, name, value)
	if err != nil {
		return err
	}
	if same {
		log.Printf("config watcher initial sync name=%s unchanged source=%s", name, path)
		return nil
	}
	return publishConfigFile(ctx, db, cache, path, publisher, note)
}

// configFileMatchesPublished 判断文件内容是否已经和 MySQL 里的配置树一致。
func configFileMatchesPublished(ctx context.Context, db *sql.DB, name string, value interface{}) (bool, error) {
	if name == "CustomSkins" {
		return customSkinsMatchPublished(ctx, db, value)
	}
	published, err := readPublishedConfig(ctx, db, name)
	if errors.Is(err, sql.ErrNoRows) {
		// 这个配置从来没发布过（AffixWash、town_idle_exp 都踩过），必须发布。
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return configTreeEqual(published, value), nil
}

// customSkinsMatchPublished 比较 CustomSkins 覆盖层。
// CustomSkins 是逐行覆盖而不是整表替换，所以只比对它声明到的那些行和字段，
// 目标表里其他字段、其他行都不参与——覆盖层没写的东西发布时也不会动。
func customSkinsMatchPublished(ctx context.Context, db *sql.DB, value interface{}) (bool, error) {
	root, ok := value.(map[string]interface{})
	if !ok {
		return false, nil
	}
	tables, ok := root["tables"].(map[string]interface{})
	if !ok {
		return false, nil
	}
	for table, rawRows := range tables {
		rows, ok := rawRows.([]interface{})
		if !ok {
			return false, nil
		}
		for _, raw := range rows {
			row, ok := raw.(map[string]interface{})
			if !ok {
				return false, nil
			}
			id, ok := configScalarToInt(row["id"])
			if !ok {
				return false, nil
			}
			fields, ok := row["fields"].(map[string]interface{})
			if !ok {
				return false, nil
			}
			published, err := readPublishedConfigRoot(ctx, db, table, id)
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			object, ok := published.(map[string]interface{})
			if !ok {
				return false, nil
			}
			for key, want := range fields {
				got, exists := object[key]
				if !exists || !configTreeEqual(got, want) {
					return false, nil
				}
			}
		}
	}
	return true, nil
}

// configTreeEqual 深度比较两棵配置树。两侧都已归一化：
// 文件侧走 decodeRuntimeConfigSource 的 normalizeRuntimeYAML（int→int64、
// float32→float64），MySQL 侧由 decodePublishedNode 产出 int64/float64/string/bool/nil。
// 整数和整数相等的浮点数视为相等，避免 1 与 1.0 这类纯粹的类型差异导致无谓重发。
func configTreeEqual(a, b interface{}) bool {
	switch av := a.(type) {
	case map[string]interface{}:
		bv, ok := b.(map[string]interface{})
		if !ok || len(av) != len(bv) {
			return false
		}
		for key, want := range av {
			got, exists := bv[key]
			if !exists || !configTreeEqual(want, got) {
				return false
			}
		}
		return true
	case []interface{}:
		bv, ok := b.([]interface{})
		if !ok || len(av) != len(bv) {
			return false
		}
		for index := range av {
			if !configTreeEqual(av[index], bv[index]) {
				return false
			}
		}
		return true
	}
	return configScalarEqual(a, b)
}

func configScalarEqual(a, b interface{}) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	switch av := a.(type) {
	case int64:
		switch bv := b.(type) {
		case int64:
			return av == bv
		case float64:
			return math.Trunc(bv) == bv && float64(av) == bv
		}
	case float64:
		switch bv := b.(type) {
		case float64:
			return av == bv
		case int64:
			return math.Trunc(av) == av && av == float64(bv)
		}
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	}
	return false
}

// configScalarToInt 取配置里的整数值（YAML 归一化后是 int64）。
func configScalarToInt(value interface{}) (int64, bool) {
	switch v := value.(type) {
	case int64:
		return v, true
	case float64:
		if math.Trunc(v) == v {
			return int64(v), true
		}
	}
	return 0, false
}

// publishedConfigNode 是 game_config_nodes 的一行。与 cmd/gm-api 的 configNode
// 同形，这里独立一份是为了让服务端不依赖 GM API 的进程内类型。
type publishedConfigNode struct {
	id, parent             int64
	field                  string
	index, kind, valueType int
	intValue               sql.NullInt64
	floatValue             sql.NullFloat64
	stringValue            sql.NullString
	boolValue              sql.NullInt64
}

const publishedConfigNodeColumns = `node_id, parent_id, field_name, array_index, node_kind, value_type, int_value, float_value, string_value, bool_value`

func scanPublishedConfigNode(scanner interface{ Scan(...interface{}) error }) (*publishedConfigNode, error) {
	node := &publishedConfigNode{}
	if err := scanner.Scan(&node.id, &node.parent, &node.field, &node.index, &node.kind, &node.valueType,
		&node.intValue, &node.floatValue, &node.stringValue, &node.boolValue); err != nil {
		return nil, err
	}
	return node, nil
}

// readPublishedConfig 按 config_publish_runtime.go 的写入形状还原一棵配置树。
// 与 replaceRuntimeConfig 对称：整表替换的配置（Gameplay 等）根节点只有一个、
// array_index = -1；数据表是 [id, object] 二元组数组。
func readPublishedConfig(ctx context.Context, db *sql.DB, name string) (interface{}, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+publishedConfigNodeColumns+
		` FROM game_config_nodes WHERE config_name = ? ORDER BY node_id`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := map[int64]*publishedConfigNode{}
	children := map[int64][]*publishedConfigNode{}
	for rows.Next() {
		node, err := scanPublishedConfigNode(rows)
		if err != nil {
			return nil, err
		}
		nodes[node.id] = node
		children[node.parent] = append(children[node.parent], node)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, sql.ErrNoRows
	}
	roots := children[0]
	if len(roots) == 0 {
		return nil, sql.ErrNoRows
	}
	if len(roots) == 1 && roots[0].index == -1 {
		return decodePublishedNode(roots[0], children), nil
	}
	result := make([]interface{}, 0, len(roots))
	for _, root := range roots {
		if root.index < 0 {
			continue
		}
		result = append(result, []interface{}{int64(root.index), decodePublishedNode(root, children)})
	}
	return result, nil
}

// readPublishedConfigRoot 只读取数据表里的某一行，用于 CustomSkins 这类逐行覆盖：
// 整表读 EquipBase 要拖 32707 个节点，按行向下走几层就够了。
func readPublishedConfigRoot(ctx context.Context, db *sql.DB, name string, id int64) (interface{}, error) {
	var rootID int64
	err := db.QueryRowContext(ctx, `SELECT node_id FROM game_config_nodes
		WHERE config_name = ? AND parent_id = 0 AND field_name = '' AND array_index = ?`, name, id).Scan(&rootID)
	if err != nil {
		return nil, err
	}
	nodes := map[int64]*publishedConfigNode{}
	children := map[int64][]*publishedConfigNode{}
	if err := collectPublishedNodes(ctx, db, name, rootID, nodes, children); err != nil {
		return nil, err
	}
	root := nodes[rootID]
	if root == nil {
		return nil, sql.ErrNoRows
	}
	return decodePublishedNode(root, children), nil
}

// collectPublishedNodes 从 rootID 向下把整棵子树取出来。
// 查询必须带上 config_name：唯一的二级索引是 (config_name, parent_id)，
// 只按 parent_id 查用不上它，优化器会退化成 skip scan——实测在 22 万行的
// game_config_nodes 上每次要扫 21811 条索引项，而这里是每个节点一次查询，
// CustomSkins 比对 EquipBase 因此要 7 秒。带上 config_name 后是 ref 命中 1 行。
func collectPublishedNodes(ctx context.Context, db *sql.DB, name string, rootID int64,
	nodes map[int64]*publishedConfigNode, children map[int64][]*publishedConfigNode) error {
	root, err := readPublishedNodeByID(ctx, db, rootID)
	if err != nil {
		return err
	}
	nodes[rootID] = root
	rows, err := db.QueryContext(ctx, `SELECT `+publishedConfigNodeColumns+
		` FROM game_config_nodes WHERE config_name = ? AND parent_id = ? ORDER BY node_id`, name, rootID)
	if err != nil {
		return err
	}
	var kids []*publishedConfigNode
	for rows.Next() {
		node, err := scanPublishedConfigNode(rows)
		if err != nil {
			rows.Close()
			return err
		}
		kids = append(kids, node)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	children[rootID] = kids
	for _, kid := range kids {
		if err := collectPublishedNodes(ctx, db, name, kid.id, nodes, children); err != nil {
			return err
		}
	}
	return nil
}

func readPublishedNodeByID(ctx context.Context, db *sql.DB, nodeID int64) (*publishedConfigNode, error) {
	return scanPublishedConfigNode(db.QueryRowContext(ctx, `SELECT `+publishedConfigNodeColumns+
		` FROM game_config_nodes WHERE node_id = ?`, nodeID))
}

// decodePublishedNode 与 config_publish_runtime.go 的 insertRuntimeConfigNode 对称。
func decodePublishedNode(node *publishedConfigNode, children map[int64][]*publishedConfigNode) interface{} {
	switch node.kind {
	case configNodeObject:
		result := map[string]interface{}{}
		for _, child := range children[node.id] {
			result[child.field] = decodePublishedNode(child, children)
		}
		return result
	case configNodeArray:
		var result []interface{}
		for _, child := range children[node.id] {
			for len(result) <= child.index {
				result = append(result, nil)
			}
			result[child.index] = decodePublishedNode(child, children)
		}
		if result == nil {
			result = []interface{}{}
		}
		return result
	}
	switch node.valueType {
	case configValueInt:
		if node.intValue.Valid {
			return node.intValue.Int64
		}
	case configValueFloat:
		if node.floatValue.Valid {
			return node.floatValue.Float64
		}
	case configValueString:
		if node.stringValue.Valid {
			return node.stringValue.String
		}
	case configValueBool:
		if node.boolValue.Valid {
			return node.boolValue.Int64 != 0
		}
	}
	return nil
}
