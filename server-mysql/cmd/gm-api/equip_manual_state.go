package main

// 手工装备面板要回填装备当前的随机属性 / 洗练词缀 / 宝石槽，但这三张子表
// （player_item_random_attributes / player_item_affixes / player_item_gems）从来没
// 通过 GM API 暴露过：通用的 player_items 子资源查询（query.go 的 queries 表）只 SELECT
// 主表字段，而 extraPlayerSubresources 注册的是**无参数** SQL，没法按 serverId 过滤。
//
// 这里按子资源注册三条只读查询，前端拿到全部行后按 location + slot_index 归并到具体
// 装备上（这三张表的主键就是 player_id + location + slot_index + position，没有 serverId）。
// 权限沿用 players.read / subGMCanViewPlayer，与其它角色子资源完全一致。
//
// ⚠ 读的是 MySQL 里**最后一次保存**的状态。equip.go 的登录修复、ensureEquipmentAffixCount
// 的裁剪与补齐都发生在游戏服内存里，落库要等 persistGMSession / 正常保存。所以在线角色
// 尚未保存的改动这里是看不到的，面板上必须写清楚这一点。
func init() {
	registerPlayerSubresource("random-attributes",
		`SELECT location, slot_index, position, attribute_id FROM player_item_random_attributes WHERE player_id = ? ORDER BY location, slot_index, position`)
	registerPlayerSubresource("affixes",
		`SELECT location, slot_index, position, affix_id FROM player_item_affixes WHERE player_id = ? ORDER BY location, slot_index, position`)
	registerPlayerSubresource("gems",
		`SELECT location, slot_index, position, gem_item_id FROM player_item_gems WHERE player_id = ? ORDER BY location, slot_index, position`)
}
