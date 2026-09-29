package main

func init() {
	registerPlayerCommandRoute("starsoul-adjust", "player.starsoul_adjust", "players.starsoul")
	registerPlayerSubresource("starsouls", `SELECT souls.star_soul_id, souls.type_id, souls.level, souls.exp, souls.pos_type, souls.quality, souls.main_attribute, souls.vice_growth_level, souls.is_locked, slots.slot_index FROM player_star_souls souls LEFT JOIN player_star_soul_slots slots ON slots.player_id = souls.player_id AND slots.star_soul_id = souls.star_soul_id WHERE souls.player_id = ? ORDER BY souls.star_soul_id`)
	grantRolePermission("operator", "players.starsoul")
	grantRolePermission("subgm", "players.starsoul")
}
