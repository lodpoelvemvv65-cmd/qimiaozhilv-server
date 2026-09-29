package main

func init() {
	registerPlayerCommandRoute("skills-adjust", "player.skills_adjust", "players.skills")
	grantRolePermission("operator", "players.skills")
	grantRolePermission("subgm", "players.skills")
}
