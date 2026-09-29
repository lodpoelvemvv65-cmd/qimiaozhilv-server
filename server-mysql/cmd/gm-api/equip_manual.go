package main

// 手工装备（player.equip_manual）走角色详情页的指令通道，
// 路由映射见 player_command_registry.go 的 extraPlayerCommandRoutes。
//
// 沿用 players.equip 权限，所以不重复 grantRolePermission：
// equip_adjust.go 已经给 operator / subgm 授过这个权限，subgm 仍然受
// subGMCanViewPlayer 的账号绑定限制。候选池接口 /api/v1/equip-manual/options
// 用的是同一个权限，见 equip_manual_options.go。
func init() {
	registerPlayerCommandRoute("equip-manual", "player.equip_manual", "players.equip")
}
