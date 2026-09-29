package main

import (
	"fmt"
	"math"
)

// Cal.CastBaseType in the original Hotfix.dll. SkillConfig.CastValue is UI
// metadata; it must not overwrite SkillLogicConfig's value or its cost basis.
const (
	skillCastBaseLevel int32 = iota
	skillCastBaseCurrent
	skillCastBaseMax
	skillCastBaseAttribute
)

func (b *battleState) skillResourceCost(cast SkillCast) (mpCost, hpCost int32, err error) {
	if b == nil || cast.SkillCast < 0 || math.IsNaN(cast.SkillCast) || math.IsInf(cast.SkillCast, 0) {
		return 0, 0, fmt.Errorf("invalid resource cost")
	}
	if cast.SkillCastType == 0 {
		return 0, 0, nil
	}
	if cast.SkillCastType != 1 && cast.SkillCastType != 2 {
		return 0, 0, fmt.Errorf("unsupported cast type")
	}
	base, err := b.skillResourceBase(cast)
	if err != nil {
		return 0, 0, err
	}
	// SkillLogic stores C# Single values (e.g. 0.20000000298023224). Multiply
	// as Single before rounding, otherwise integral costs gain a spurious 1 MP.
	cost := float64(float32(base) * float32(cast.SkillCast))
	if cast.SkillCastType == 1 {
		cost *= gameplaySkillMPCostMultiplier()
	}
	cost = math.Ceil(cost)
	if math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 || cost > math.MaxInt32 {
		return 0, 0, fmt.Errorf("invalid resource cost")
	}
	if cost == 0 {
		return 0, 0, nil
	}
	if cast.SkillCastType == 1 {
		mpCost = int32(cost)
		if mpCost > b.playerMP {
			return 0, 0, fmt.Errorf("not enough MP")
		}
		return mpCost, 0, nil
	}
	if b.playerHP <= 1 {
		return 0, 0, fmt.Errorf("not enough HP")
	}
	// Preserve the nonlethal resource gate; HP skills use their own current/max
	// basis and are never discounted by the operations MP multiplier.
	hpCost = int32(cost)
	if hpCost >= b.playerHP {
		hpCost = b.playerHP - 1
	}
	return 0, hpCost, nil
}

func (b *battleState) skillResourceBase(cast SkillCast) (int32, error) {
	switch cast.CastBaseType {
	case skillCastBaseLevel:
		if b.owner == nil {
			return 0, fmt.Errorf("missing caster level")
		}
		return max(1, b.owner.level), nil
	case skillCastBaseCurrent:
		if cast.SkillCastType == 1 {
			return b.playerMP, nil
		}
		return b.playerHP, nil
	case skillCastBaseMax:
		attribute, base := CombatAttributeMaxHP, b.playerMaxHP
		if cast.SkillCastType == 1 {
			attribute, base = CombatAttributeMaxMP, b.playerMaxMP
		}
		if b.runtime != nil {
			base = b.runtime.EffectiveAttribute(b.runtime.Player(), attribute)
		}
		return base, nil
	case skillCastBaseAttribute:
		attributes := combatAttributesForValueKey(cast.AttributeType)
		if b.runtime != nil && len(attributes) == 1 {
			return b.runtime.EffectiveAttribute(b.runtime.Player(), attributes[0]), nil
		}
		return 0, fmt.Errorf("unsupported resource attribute %d", cast.AttributeType)
	default:
		return 0, fmt.Errorf("unsupported cast basis %d", cast.CastBaseType)
	}
}
