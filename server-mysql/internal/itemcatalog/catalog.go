// Package itemcatalog exposes the item IDs and Chinese names published to MySQL.
// It shares the same three configuration tables as the game's GM grant path.
package itemcatalog

import (
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
)

type Item struct {
	ID            int32  `json:"id"`
	Name          string `json:"name"`
	Category      string `json:"category"`
	ItemType      int32  `json:"itemType"`
	Source        string `json:"source"`
	Level         int32  `json:"level"`
	Quality       int32  `json:"quality"`
	JobType       int32  `json:"jobType"`
	JobName       string `json:"jobName"`
	EquipSlot     int32  `json:"equipSlot"`
	EquipSlotName string `json:"equipSlotName"`
	Description   string `json:"description"`
	Delivery      string `json:"delivery"`
}

// Load reads only direct scalar fields of datatable roots. Nested reward IDs
// must never appear as separate grantable items. One query gives a consistent
// snapshot, including changes made by the existing YAML publishing commands.
func Load(ctx context.Context, db *sql.DB) ([]Item, error) {
	rows, err := db.QueryContext(ctx, `SELECT root.array_index, root.config_name,
		COALESCE(MAX(CASE WHEN field.field_name = 'Name' THEN field.string_value END), ''),
		COALESCE(MAX(CASE WHEN field.field_name = 'Description' THEN field.string_value END), ''),
		COALESCE(MAX(CASE WHEN field.field_name IN ('UseLevel', 'UsedLevel') THEN field.int_value END), 0),
		COALESCE(MAX(CASE WHEN field.field_name = 'Quality' THEN field.int_value END), 0),
		COALESCE(MAX(CASE WHEN field.field_name = 'MaterialType' THEN field.int_value END), 0),
		COALESCE(MAX(CASE WHEN field.field_name = 'JobId' THEN field.int_value END), 0),
		COALESCE(MAX(CASE WHEN field.field_name = 'Type' THEN field.int_value END), 0)
		FROM game_config_nodes root
		LEFT JOIN game_config_nodes field ON field.parent_id = root.node_id
			AND field.config_name = root.config_name AND field.node_kind = 3
			AND field.field_name IN ('Name', 'Description', 'UseLevel', 'UsedLevel', 'Quality', 'MaterialType', 'JobId', 'Type')
		WHERE root.parent_id = 0 AND root.node_kind = 1 AND root.array_index > 0
			AND root.config_name IN ('EquipBase', 'GoodsBase', 'MaterialBase')
		GROUP BY root.node_id, root.array_index, root.config_name
		ORDER BY root.array_index, root.config_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Item, 0)
	for rows.Next() {
		var item Item
		var materialType int32
		if err := rows.Scan(&item.ID, &item.Source, &item.Name, &item.Description, &item.Level, &item.Quality, &materialType, &item.JobType, &item.EquipSlot); err != nil {
			return nil, err
		}
		classify(&item, materialType)
		items = append(items, item)
	}
	return items, rows.Err()
}

// EquipSlotNames 是 EquipBase.Type（ET.EquipType）的中文名，下标即 Type。
// GM 后台的装备部位、宝石可镶嵌部位都按这张表显示，不要再各写一份。
//
// ⚠ EquipBase.Type 缺失时读出来是 0，也就是「武器」——996 行里有 216 行没有 Type，
// 所以看到某件饰品显示成武器时，先怀疑配置缺字段，而不是这里映射错了。
// 4 是称号/勋章位。
var EquipSlotNames = []string{"武器", "背景", "形象", "护腕", "称号", "戒指", "帽子", "项链", "衣服", "裤子", "手套", "鞋子"}

// EquipSlotName 把 EquipBase.Type 转成中文名；越界时回退成「装备部位 N」。
func EquipSlotName(slot int32) string {
	if slot >= 0 && int(slot) < len(EquipSlotNames) {
		return EquipSlotNames[slot]
	}
	return fmt.Sprintf("装备部位 %d", slot)
}

func classify(item *Item, materialType int32) {
	// EquipBase.JobId is a profession family (1..4), whereas players.job_id
	// is a male/female character ID (1..8). Do not apply the character mapping.
	switch item.JobType {
	case 0:
		item.JobName = "通用"
	case 1:
		item.JobName = "军官"
	case 2:
		item.JobName = "运动员"
	case 3:
		item.JobName = "护士"
	case 4:
		item.JobName = "超能力"
	default:
		item.JobName = fmt.Sprintf("职业 %d", item.JobType)
	}
	switch item.Source {
	case "EquipBase":
		item.ItemType, item.Category = 1, "装备"
		item.EquipSlotName = EquipSlotName(item.EquipSlot)
	case "GoodsBase":
		item.ItemType, item.Category = 2, "道具"
	case "MaterialBase":
		item.ItemType, item.Category = 3, "材料"
		if materialType == 2 {
			item.Category = "宝石"
		}
	}
	if item.Source != "EquipBase" {
		item.EquipSlot, item.EquipSlotName = -1, ""
	}
	item.Name = strings.TrimSpace(item.Name)
	if item.Name == "" {
		item.Name = fmt.Sprintf("未命名物品（%d）", item.ID)
	}
	switch item.ID {
	case 110201, 110202, 110203, 110204:
		item.Category = "货币 / 经验"
		item.Delivery = "邮件领取时增加对应余额或经验；直接发放会进入背包，调整余额请使用“调整货币 / 等级”"
	case 110205:
		item.Category = "货币 / 经验"
		item.Delivery = "星币通过隐藏货币堆叠保存，不占普通背包格"
	}
}

// WriteCSV includes a UTF-8 BOM so Windows Excel displays Chinese correctly.
func WriteCSV(w io.Writer, items []Item) error {
	if _, err := io.WriteString(w, "\uFEFF"); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	cw.UseCRLF = true
	if err := cw.Write([]string{"物品ID", "中文名称", "分类", "装备部位", "适用职业", "职业类型", "使用等级", "品质", "物品类型", "配置表", "物品说明", "发放说明"}); err != nil {
		return err
	}
	for _, item := range items {
		n := func(v int32) string { return strconv.FormatInt(int64(v), 10) }
		if err := cw.Write([]string{n(item.ID), csvText(item.Name), item.Category, item.EquipSlotName, item.JobName, n(item.JobType), n(item.Level), n(item.Quality), n(item.ItemType), item.Source, csvText(item.Description), csvText(item.Delivery)}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func csvText(value string) string {
	if strings.HasPrefix(value, "\t") || strings.HasPrefix(value, "\r") || strings.HasPrefix(value, "\n") || strings.ContainsAny(firstRune(strings.TrimSpace(value)), "=+-@") {
		return "'" + value
	}
	return value
}

func firstRune(value string) string {
	for _, r := range value {
		return string(r)
	}
	return ""
}
