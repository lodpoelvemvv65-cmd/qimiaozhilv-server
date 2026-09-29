package main

// family_reset.go：家族归属重置（player.family_reset）的 GM 路由与权限点。
//
// 指令实现见 server-mysql/gm_control_family.go。路由走角色详情的指令通道，
// 和 job-adjust / starsoul-adjust 同一种接法。
//
// 权限点用 players.family 而不是复用已有的 families.read：read 类权限现在挂在
// support / auditor / readonly 上，而这个命令会把角色摘出家族、必要时还会解散整个
// 家族，属于写操作，不能因为「能看家族列表」就顺带拿到。

func init() {
	registerPlayerCommandRoute("family-reset", "player.family_reset", "players.family")
	grantRolePermission("operator", "players.family")
}
