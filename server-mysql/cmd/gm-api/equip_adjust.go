package main

func init() {
	registerPlayerCommandRoute("equip-adjust", "player.equip_adjust", "players.equip")
	grantRolePermission("operator", "players.equip")
	grantRolePermission("subgm", "players.equip")
}
