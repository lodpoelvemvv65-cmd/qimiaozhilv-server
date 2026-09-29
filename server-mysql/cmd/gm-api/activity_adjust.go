package main

func init() {
	registerPlayerCommandRoute("activity-adjust", "player.activity_adjust", "players.activity")
	grantRolePermission("operator", "players.activity")
	grantRolePermission("subgm", "players.activity")
}
