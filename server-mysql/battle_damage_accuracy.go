package main

func directDamageUsesAccuracy(plan *SkillDamagePlan) bool {
	return plan != nil && !plan.Periodic &&
		(plan.Type == SkillDamagePhysical || plan.Type == SkillDamageSpiritual)
}

func (battle *battleState) directDamageHits(plan *SkillDamagePlan, source, target CombatUnitRef, ctx *skillExecutionContext) bool {
	if !directDamageUsesAccuracy(plan) {
		return true
	}
	// Online MonsterBase has no Hit/Res fields on any encounter monster.
	// Applying a player's percentage-form Resistance to direct monster damage
	// therefore makes late-game monsters miss almost forever. Online PvE
	// captures also show harmful statuses use configured probability directly;
	// direct monster damage is independently guaranteed to land here.
	if source.Side == CombatSideMonster && target.Side == CombatSidePlayer {
		return true
	}
	if ctx == nil {
		ctx = newSkillExecutionContext(battle, nil)
	}
	hit := ctx.roll()*100 < battle.ratingContestChance(100, source, target)
	if hit {
		delete(ctx.missedDirectDamage, target)
		return true
	}
	if ctx.missedDirectDamage == nil {
		ctx.missedDirectDamage = make(map[CombatUnitRef]bool)
	}
	ctx.missedDirectDamage[target] = true
	return false
}

func (ctx *skillExecutionContext) directDamageMissed(target CombatUnitRef) bool {
	return ctx != nil && ctx.missedDirectDamage[target]
}
