package main

import (
	"fmt"
	"math"
	"time"
)

// 本文件集中处理周期效果（持续伤害 DOT / 持续治疗 HOT）：节拍、时长、
// 以及客户端状态元数据（20080 用的 stateKey/iconId/effectId）的登记。

// periodicTickInterval is how often a 持续伤害/持续治疗 effect takes effect. It
// comes from the hot-reloaded Gameplay combat defaults and per-modifier overrides,
// not from the client tables.
//
// Nothing in the shipped tables states this cadence. The event 8「循环执行定时器」
// node carries only the damage/heal formula, selectTarget, isCritEvent and
// damageType — no interval and no hit count. modifier.thinkInterval is unusable:
// it is 5 seconds on nine of the nineteen periodic modifiers and skillSourcetype 0
// (empty) on the other ten, so the empty ones would never tick at all. The
// skill-level duration and interval fields are 0 on every periodic skill, and none
// of the 47 DOT clauses in SkillConfig.Desc contains 「每」.
//
// The cadence therefore had to be measured from the online server. In
// 参考数据/抓包归档/online-skill-effects-20260906.pcapng the 护士 24295 casts
// 310501 连锁注射 three times, and each time the 20080 that adds bufficon_constrea
// (Time=14000) is followed by that same unit's own 20078 heals on one identical
// beat: +4.00s for the first tick, +8.00s for the second, +12.00s for the third.
// The +16.00s fourth tick is past the 14-second duration and never happened, on all
// three casts. So the rule is "first tick one interval late, then one per interval,
// stopping before the tick that would fall outside the duration" — the tick count is
// floor(duration / interval): 7s→1, 10s and 11s→2, 12s and 14s→3, 26s→6.
//
// The first tick is deliberately NOT immediate: no heal frame is sent at the moment
// the buff lands, and the three captures above confirm the one-interval delay. A
// tick also pushes only 20078 plus the 20169/20170 attribute sync — no 20077 visual
// and no 20080 re-send (battle_skill_runtime.go skips visuals for Periodic effects).
//
// The one-second timer this replaced was carried over from the retired SQLite
// server and had no client basis: it made a ten-second bleed land ten hits inside a
// single six-second round. The six-second round that replaced it was wrong in the
// other direction: it gave a ten-second bleed one hit instead of the captured two.
func periodicTickInterval(status *SkillStatusPlan) time.Duration {
	if status == nil {
		return gameplayEffectTickInterval()
	}
	return gameplayPeriodicTickInterval(status.ModifierID)
}

func (battle *battleState) applyPeriodic(effect SkillEffect, status *SkillStatusPlan, source, target CombatUnitRef, amount int32, damage bool) ([]CombatEvent, error) {
	kind := CombatEffectHealOverTime
	suffix := "hot"
	if damage {
		kind, suffix = CombatEffectDamageOverTime, "dot"
	}
	interval := periodicTickInterval(status)
	key := battle.registerEffectMeta(status.ModifierID, suffix, status)
	spec := EffectSpec{
		Key: key, Kind: kind, Value: amount, Duration: secondsDuration(status.DurationSeconds), Interval: interval,
		StackMode: stackMode(status), MaxStacks: gameplayEffectMaxStacks(), Polarity: statusPolarity(status),
		Undispellable: !status.CanBeCleared,
	}
	if !damage && effect.Treatment != nil && effect.Treatment.CanCrit {
		spec.CriticalChance, spec.CriticalMultiplier = battle.treatmentCriticalStats(source)
	}
	spec = modifierEffectSpec(status, spec)
	return battle.runtime.ApplyEffects(CombatEffectContext{Source: source, Target: target}, []CombatEffect{spec})
}

func secondsDuration(seconds float64) time.Duration {
	if seconds <= 0 || math.IsNaN(seconds) {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

// overlayBucket returns the shared key two skills of the same 持续伤害家族 live
// under, or "" when the effect keeps its own per-modifier key.
//
// The online overlayType is exactly that family — modifier.attribute bit 4 is
// documented as「可叠加（叠加层数，用于持续伤害）」and the enum is 1 中毒 / 2 流血 /
// 3 燃烧 / 4 治疗. Every modifier in the shipped SkillLogicConfig that sets it is
// one of these periodic families, and each family carries a single iconId and a
// single stateK (1↔104 bufficon_poisoning, 2↔105 bufficon_bleed, 3↔106
// bufficon_light, 4↔107 bufficon_constrea), so the buff bar the client draws
// from StateType already collapses them into one slot. Giving the server effect
// the same key makes it agree: a second 流血 from another skill adds a layer to
// the 流血 already running instead of stacking a second invisible 流血 beside
// it that no icon, no tooltip and no state packet ever mentions.
//
// Two narrowings keep this from over-reaching. The suffix restricts it to the
// two periodic kinds, because overlayType says nothing about any other effect
// kind, and the stateKey requirement drops a modifier the client has no slot
// for (普通攻击 30000114 ships overlayType 1 but stateK 0); such a modifier keeps
// its own key rather than sharing a slot it cannot show up in.
func overlayBucket(status *SkillStatusPlan, suffix string) string {
	if status == nil || status.OverlayType == 0 || status.StateKey == 0 {
		return ""
	}
	switch suffix {
	case "dot", "hot":
		return fmt.Sprintf("overlay:%d:%s", status.OverlayType, suffix)
	}
	return ""
}

// registerEffectMeta records the client-facing metadata for one effect key and
// returns the key the effect should live under.
func (battle *battleState) registerEffectMeta(modifierID int64, suffix string, status *SkillStatusPlan) string {
	if modifierID == 0 && status != nil {
		modifierID = status.ModifierID
	}
	key := fmt.Sprintf("modifier:%d:%s", modifierID, suffix)
	if bucket := overlayBucket(status, suffix); bucket != "" {
		key = bucket
	}
	if battle.effectMeta == nil {
		battle.effectMeta = make(map[string]battleEffectMetadata)
	}
	meta := battleEffectMetadata{modifierID: modifierID}
	if status != nil {
		meta.durationMS = int32(math.Round(status.DurationSeconds * 1000))
		meta.stateKey, meta.isBuff = status.StateKey, !status.IsDebuff
	}
	if skillLogicCatalog != nil {
		if modifier := skillLogicCatalog.Modifiers[modifierID]; modifier != nil {
			meta.stateKey = modifier.StateKey
			meta.iconID, meta.iconDesc = modifier.IconID, modifier.IconDescription
			meta.isBuff = modifier.BuffType == 0
		}
	}
	battle.effectMeta[key] = meta
	return key
}
