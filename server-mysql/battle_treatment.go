package main

import (
	"math"
	"math/rand"
)

func (battle *battleState) treatmentAmount(plan *SkillTreatmentPlan, source, target CombatUnitRef) int32 {
	if plan == nil {
		return 0
	}
	amount := float64(battle.formulaBase(plan.Self.Stat, source))*plan.Self.Percent/100 +
		float64(battle.formulaBase(plan.Target.Stat, target))*plan.Target.Percent/100
	base := positiveCombatAmount(amount)
	if base == 0 {
		return 0
	}
	auxiliary := battle.runtime.EffectivePercentAttribute(source, CombatAttributeAuxiliary)
	auxiliary = math.Max(-100, math.Min(1000, auxiliary))
	return positiveCombatAmount(math.Round(float64(base) * (1 + auxiliary/100)))
}

func (battle *battleState) treatmentOutcome(plan *SkillTreatmentPlan, source, target CombatUnitRef, ctx *skillExecutionContext) (int32, bool) {
	amount := battle.treatmentAmount(plan, source, target)
	if plan == nil || !plan.CanCrit || amount <= 0 {
		return amount, false
	}
	chance, multiplier := battle.treatmentCriticalStats(source)
	roll := rand.Float64
	if ctx != nil && ctx.roll != nil {
		roll = ctx.roll
	}
	critical := chance > 0 && roll()*100 < chance
	if !critical {
		return amount, false
	}
	return positiveCombatAmount(float64(amount) * multiplier), true
}

func (battle *battleState) treatmentCriticalStats(source CombatUnitRef) (float64, float64) {
	chance := battle.runtime.EffectivePercentAttribute(source, CombatAttributeCritRate) +
		battle.runtime.EffectivePercentAttribute(source, CombatAttributeSpiritualCritRate)
	value := battle.runtime.EffectivePercentAttribute(source, CombatAttributeCritValue) +
		battle.runtime.EffectivePercentAttribute(source, CombatAttributeSpiritualCritValue)
	chance = math.Max(0, math.Min(100, chance))
	multiplier := math.Max(1, math.Min(10, 1.5+value/100))
	return chance, multiplier
}

func markTreatmentEventsCritical(events []CombatEvent, critical bool) {
	if !critical {
		return
	}
	for index := range events {
		switch events[index].Type {
		case CombatEventHeal, CombatEventEffectApplied, CombatEventEffectRefreshed, CombatEventEffectStacked:
			events[index].IsCrit = true
		}
	}
}
