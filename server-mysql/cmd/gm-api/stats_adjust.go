package main

func init() {
	registerPlayerCommandRoute("stats-adjust", "player.stats_adjust", "players.stats")
	grantRolePermission("operator", "players.stats")
}
