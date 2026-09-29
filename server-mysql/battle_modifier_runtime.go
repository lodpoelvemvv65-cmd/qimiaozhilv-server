package main

const (
	modifierAttributeStackable   int32 = 1 << 2
	modifierAttributeRefreshable int32 = 1 << 3
)

func modifierEffectSpec(status *SkillStatusPlan, spec EffectSpec) EffectSpec {
	if status != nil {
		spec.ModifierID = status.ModifierID
		spec.ModifierTag = status.Tag
	}
	return spec
}

func (battle *battleState) modifierBlocked(target CombatUnitRef, status *SkillStatusPlan) bool {
	if battle == nil || battle.runtime == nil || status == nil {
		return false
	}
	tags := modifierImmuneTags(status)
	return len(tags) > 0 && battle.runtime.hasModifierTag(target, status.ModifierID, tags)
}

func modifierImmuneTags(status *SkillStatusPlan) []int32 {
	if status == nil {
		return nil
	}
	seen := make(map[int32]struct{}, len(status.ImmuneTags)+1)
	tags := make([]int32, 0, len(status.ImmuneTags)+1)
	for _, tag := range append([]int32{status.ImmuneTag}, status.ImmuneTags...) {
		if tag <= 0 {
			continue
		}
		if _, duplicate := seen[tag]; duplicate {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	return tags
}

func (r *CombatRuntime) hasModifierTag(target CombatUnitRef, modifierID int64, tags []int32) bool {
	if r == nil || len(tags) == 0 {
		return false
	}
	target = r.normalize(target)
	wanted := make(map[int32]struct{}, len(tags))
	for _, tag := range tags {
		if tag > 0 {
			wanted[tag] = struct{}{}
		}
	}
	now := r.clock.Now()
	for _, effect := range r.state().effects {
		if effect.target != target || effect.modifierID == modifierID || effect.modifierTag <= 0 {
			continue
		}
		if !effect.expiresAt.IsZero() && !effect.expiresAt.After(now) {
			continue
		}
		if _, conflict := wanted[effect.modifierTag]; conflict {
			return true
		}
	}
	return false
}

func (battle *battleState) modifierImmuneEvent(source, target CombatUnitRef) CombatEvent {
	return battle.runtime.event(battle.runtime.clock.Now(), CombatEventImmune, source, target)
}

func stackMode(status *SkillStatusPlan) CombatEffectStackMode {
	if status == nil {
		return CombatEffectReplace
	}
	if status.Attribute&modifierAttributeStackable != 0 {
		return CombatEffectStack
	}
	if status.Attribute&modifierAttributeRefreshable != 0 {
		return CombatEffectRefresh
	}
	return CombatEffectReplace
}
