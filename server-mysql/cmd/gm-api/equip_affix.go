package main

// GM API 的「调整词缀」入口（player.equip_affix）。
//
// 沿用 players.equip 权限，与「调整装备」「手工装备」「镶嵌宝石」同一档，所以不需要
// 给 operator / subgm 补角色权限（equip_adjust.go 已经授过）；subgm 仍然受
// subGMCanViewPlayer 的角色绑定限制。
//
// 候选池不新开接口：词缀池和槽位数跟着品质走，前端已经从
// GET /api/v1/equip-manual/options 拿到了（affixes / affixPools /
// limits.bonusCountByQuality 三个字段），装备列表用的是角色资源接口。
func init() {
	registerPlayerCommandRoute("equip-affix", "player.equip_affix", "players.equip")
}
