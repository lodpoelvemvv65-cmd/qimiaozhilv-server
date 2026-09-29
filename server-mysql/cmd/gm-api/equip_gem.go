package main

// 宝石镶嵌（player.equip_gem）走角色详情页的指令通道，路由映射见
// player_command_registry.go 的 extraPlayerCommandRoutes。
//
// 沿用 players.equip 权限，所以不重复 grantRolePermission：
// equip_adjust.go 已经给 operator / subgm 授过这个权限，subgm 仍然受
// subGMCanViewPlayer 的账号绑定限制。
//
// 候选池不新增接口：宝石表 / 部位规则 / 中文名全部来自现成的
// GET /api/v1/equip-manual/options[?itemId=N]（equip_manual_options.go），
// 与这个 action 同权限。两处共用一份候选池，宝石名称与部位规则不会各说各话。
func init() {
	registerPlayerCommandRoute("equip-gem", "player.equip_gem", "players.equip")
}
