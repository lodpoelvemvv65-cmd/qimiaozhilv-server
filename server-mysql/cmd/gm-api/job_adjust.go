package main

func init() {
	registerPlayerCommandRoute("job-adjust", "player.job_adjust", "players.job")
	grantRolePermission("operator", "players.job")
	grantRolePermission("subgm", "players.job")
}
