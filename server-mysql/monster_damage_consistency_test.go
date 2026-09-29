package main

import (
	"fmt"
	"math"
	"sort"
	"testing"
)

func TestEveryEncounterMonsterHasAttackAndDamagingSkill(t *testing.T) {
	loadOnlineTablesForTest(t)
	catalog := loadTestSkillLogic(t)
	uses := configuredEncounterMonsterUses()
	monsterIDs := make([]int, 0, len(uses))
	for monsterID := range uses {
		monsterIDs = append(monsterIDs, int(monsterID))
	}
	sort.Ints(monsterIDs)

	checkedGroups := make(map[int64]bool)
	for _, rawID := range monsterIDs {
		monsterID := int32(rawID)
		row := tables.monsterBase[int64(monsterID)]
		if row == nil {
			t.Errorf("%s references missing MonsterBase %d", uses[monsterID][0], monsterID)
			continue
		}
		if num(row["PhyAtk"]) <= 0 && num(row["SpiAtk"]) <= 0 {
			t.Errorf("MonsterBase %d (%v) has no attack; used by %v", monsterID, row["NickName"], uses[monsterID])
		}
		groupID := num(row["SkillGroupId"])
		group := tables.skillGroup[groupID]
		if groupID <= 0 || group == nil || len(arrOf(group["SkillsArr"])) == 0 {
			t.Errorf("MonsterBase %d has invalid SkillGroupId %d; used by %v", monsterID, groupID, uses[monsterID])
			continue
		}
		if checkedGroups[groupID] {
			continue
		}
		checkedGroups[groupID] = true
		hasDamage := false
		for _, raw := range arrOf(group["SkillsArr"]) {
			entry, _ := raw.(map[string]interface{})
			skillID := int32(num(entry["Skills_Id"]))
			plan, err := catalog.Plan(skillID, 1)
			if err != nil {
				t.Errorf("SkillGroupBase %d skill %d cannot build: %v", groupID, skillID, err)
				continue
			}
			if monsterPlanHasDamage(plan.Effects) {
				hasDamage = true
			}
		}
		if !hasDamage {
			t.Errorf("SkillGroupBase %d used by MonsterBase %d has no damage skill", groupID, monsterID)
		}
	}

	if len(uses) != len(tables.monsterBase) {
		t.Errorf("encounter coverage checked %d of %d MonsterBase rows", len(uses), len(tables.monsterBase))
	}
	if len(checkedGroups) != len(tables.skillGroup) {
		t.Errorf("encounter coverage checked %d of %d SkillGroupBase rows", len(checkedGroups), len(tables.skillGroup))
	}
}

// TestEveryEncounterMonsterAttributeUsesOnlineUnits makes the percentage
// boundary explicit for every encounter monster.  MonsterBase stores rate
// fields as fractions (0.25 = 25%), while the combat runtime exposes them as
// client-facing percentages.  A regression here changes crit, anti-crit,
// damage reduction, hit and resistance together, so checking only one monster
// or one map is not sufficient.
func TestEveryEncounterMonsterAttributeUsesOnlineUnits(t *testing.T) {
	loadOnlineTablesForTest(t)
	uses := configuredEncounterMonsterUses()
	for rawID, sources := range uses {
		row := tables.monsterBase[int64(rawID)]
		if row == nil {
			t.Errorf("MonsterBase %d missing; used by %v", rawID, sources)
			continue
		}
		ss := &session{playerID: 1, transBonus: make(map[int32]float32)}
		monster := ss.buildMonsterUnitsFromRoster([]int32{rawID}, []int{1})
		if len(monster) != 1 {
			t.Errorf("MonsterBase %d could not build a combat unit", rawID)
			continue
		}
		battle := &battleState{owner: ss, playerHP: 1000, playerMaxHP: 1000, monsters: monster}
		battle.runtime = NewCombatRuntime(battle, ss.playerID, nil)
		ref := MonsterCombatUnit(monster[0].id)
		for field, numeric := range equipFieldToNumeric {
			want := numf(row[field])
			if want < 0 || math.IsNaN(want) || math.IsInf(want, 0) {
				t.Errorf("MonsterBase %d field %s has invalid value %v", rawID, field, want)
			}
			got := monster[0].extraNumeric[numeric]
			if math.Abs(got-want) > 1e-9 {
				t.Errorf("MonsterBase %d field %s numeric %d = %v, want %v", rawID, field, numeric, got, want)
			}
			if ratioNumericType(numeric) {
				if gotPercent, expected := battle.runtime.EffectivePercentAttribute(ref, combatAttributeForNumeric(numeric)), want*100; math.Abs(gotPercent-expected) > 1e-9 {
					t.Errorf("MonsterBase %d field %s percent = %v, want %v", rawID, field, gotPercent, expected)
				}
			}
		}
	}
}

// TestEveryEncounterMonsterSkillHasExecutableDamagePath executes every skill
// slot used by every configured encounter family with both ordinary and
// extreme rolls.  It also evaluates each damage node against a very high
// player Resistance.  Monster direct damage must still enter the damage
// formula; only harmful status effects are allowed to lose their contest.
func TestEveryEncounterMonsterSkillHasExecutableDamagePath(t *testing.T) {
	loadOnlineTablesForTest(t)
	catalog := loadTestSkillLogic(t)
	uses := configuredEncounterMonsterUses()
	checkedSkills := make(map[int32]bool)
	checkedSlots := 0
	for monsterID := range uses {
		row := tables.monsterBase[int64(monsterID)]
		if row == nil {
			continue
		}
		groupID := int32(num(row["SkillGroupId"]))
		group := tables.skillGroup[int64(groupID)]
		if group == nil {
			continue
		}
		for _, raw := range arrOf(group["SkillsArr"]) {
			entry, _ := raw.(map[string]interface{})
			skillID := int32(num(entry["Skills_Id"]))
			if skillID <= 0 {
				continue
			}
			checkedSkills[skillID] = true
			checkedSlots++
			plan, err := catalog.Plan(skillID, 1)
			if err != nil {
				t.Errorf("MonsterBase %d SkillGroupBase %d skill %d: %v", monsterID, groupID, skillID, err)
				continue
			}
			newBattle := func() (*battleState, CombatUnitRef, CombatUnitRef) {
				ss := &session{playerID: 1, transBonus: map[int32]float32{1033: 6906.2544}}
				units := ss.buildMonsterUnitsFromRoster([]int32{monsterID}, []int{1})
				if len(units) != 1 {
					return nil, CombatUnitRef{}, CombatUnitRef{}
				}
				battle := &battleState{
					owner: ss, playerHP: 100000000, playerMaxHP: 100000000,
					monsters: units,
				}
				battle.runtime = NewCombatRuntime(battle, ss.playerID, nil)
				return battle, MonsterCombatUnit(units[0].id), battle.runtime.Player()
			}
			for _, roll := range []float64{0, 0.999999} {
				battle, source, player := newBattle()
				if battle == nil {
					t.Errorf("MonsterBase %d skill %d could not build unit", monsterID, skillID)
					break
				}
				primary := player
				if plan.TeamType == 2 {
					primary = source
				}
				if _, err := battle.executeSkillPlan(plan, source, primary, func() float64 { return roll }); err != nil {
					t.Errorf("MonsterBase %d skill %d roll %.6f failed: %v", monsterID, skillID, roll, err)
				}
			}
			battle, source, player := newBattle()
			if battle == nil {
				continue
			}
			primary := player
			if plan.TeamType == 2 {
				primary = source
			}
			for _, effect := range flattenedSkillEffects(plan.Effects) {
				if effect.Kind != SkillEffectDamage || effect.Damage == nil {
					continue
				}
				targets := battle.resolveSkillTargets(effect.Target, source, primary, primary)
				for _, target := range targets {
					if target != player {
						continue
					}
					if directDamageUsesAccuracy(effect.Damage) && !battle.directDamageHits(effect.Damage, source, target, newSkillExecutionContext(battle, func() float64 { return 0.999999 })) {
						t.Errorf("MonsterBase %d skill %d direct damage was rejected by high player Resistance", monsterID, skillID)
					}
					if amount, _ := battle.damageOutcome(effect.Damage, source, target, newSkillExecutionContext(battle, func() float64 { return 0 })); amount <= 0 {
						t.Errorf("MonsterBase %d skill %d player damage = %d, plan=%+v", monsterID, skillID, amount, effect.Damage)
					}
				}
			}
		}
	}
	if checkedSlots == 0 || len(checkedSkills) == 0 {
		t.Fatal("no encounter monster skill was audited")
	}
}

// Every hostile monster status is exercised separately from direct damage.
// This protects the online PvE rule: player Resistance changes neither a
// monster status's configured chance nor direct damage from the same cast.
func TestEveryEncounterMonsterHarmfulStatusIgnoresResistance(t *testing.T) {
	loadOnlineTablesForTest(t)
	catalog := loadTestSkillLogic(t)
	uses := configuredEncounterMonsterUses()
	skillMonster := make(map[int32]int32)
	for monsterID := range uses {
		row := tables.monsterBase[int64(monsterID)]
		if row == nil {
			continue
		}
		group := tables.skillGroup[int64(num(row["SkillGroupId"]))]
		for _, raw := range arrOf(group["SkillsArr"]) {
			entry, _ := raw.(map[string]interface{})
			skillID := int32(num(entry["Skills_Id"]))
			if skillID > 0 {
				skillMonster[skillID] = monsterID
			}
		}
	}
	checked := 0
	for skillID, monsterID := range skillMonster {
		for level := int32(1); ; level++ {
			skill, ok := catalog.Skills[skillID]
			if !ok || level > skill.MaxLevel {
				break
			}
			plan, err := catalog.Plan(skillID, level)
			if err != nil {
				t.Errorf("monster skill %d level %d: %v", skillID, level, err)
				continue
			}
			if !monsterPlanHasHarmfulPlayerStatus(plan) {
				continue
			}
			checked++
			makeBattle := func(resistance float32) (*battleState, CombatUnitRef, CombatUnitRef) {
				ss := &session{playerID: 1, transBonus: map[int32]float32{1033: resistance}}
				units := ss.buildMonsterUnitsFromRoster([]int32{monsterID}, []int{1})
				if len(units) != 1 {
					return nil, CombatUnitRef{}, CombatUnitRef{}
				}
				battle := &battleState{owner: ss, playerHP: 2000000000, playerMaxHP: 2000000000, monsters: units}
				battle.runtime = NewCombatRuntime(battle, ss.playerID, nil)
				return battle, MonsterCombatUnit(units[0].id), battle.runtime.Player()
			}
			battle, source, player := makeBattle(0)
			if battle == nil {
				t.Errorf("monster %d skill %d level %d could not build unit", monsterID, skillID, level)
				continue
			}
			primary := player
			if plan.TeamType == 2 {
				primary = source
			}
			events, err := battle.executeSkillPlan(plan, source, primary, func() float64 { return 0 })
			if err != nil {
				t.Errorf("monster %d skill %d level %d status execution: %v", monsterID, skillID, level, err)
				continue
			}
			statusEvents := 0
			for _, event := range events {
				if event.Target == player && (event.Type == CombatEventEffectApplied || event.Type == CombatEventEffectRefreshed || event.Type == CombatEventEffectStacked) {
					statusEvents++
				}
			}
			if statusEvents == 0 {
				t.Errorf("monster %d skill %d level %d harmful status did not apply at zero resistance", monsterID, skillID, level)
			}
			// A zero roll makes every positive configured chance deterministic;
			// even extreme PvE Resistance must not reject it.
			battle, source, player = makeBattle(1_000_000)
			if battle == nil {
				continue
			}
			before := battle.playerHP
			events, err = battle.executeSkillPlan(plan, source, player, func() float64 { return 0 })
			if err != nil {
				t.Errorf("monster %d skill %d level %d high-resistance execution: %v", monsterID, skillID, level, err)
				continue
			}
			if battle.playerHP >= before && monsterPlanHasDamageTargetingPlayer(plan, battle, source, player) {
				t.Errorf("monster %d skill %d level %d lost direct damage under high Resistance", monsterID, skillID, level)
			}
			statusEvents = 0
			for _, event := range events {
				if event.Target == player && (event.Type == CombatEventEffectApplied || event.Type == CombatEventEffectRefreshed || event.Type == CombatEventEffectStacked) {
					statusEvents++
				}
			}
			if statusEvents == 0 {
				t.Errorf("monster %d skill %d level %d status was rejected by high PvE Resistance", monsterID, skillID, level)
			}
		}
	}
	if checked == 0 {
		t.Fatalf("no hostile monster status path was audited; skills=%d", len(skillMonster))
	}
}

func monsterPlanHasHarmfulPlayerStatus(plan SkillPlan) bool {
	for _, effect := range flattenedSkillEffects(plan.Effects) {
		if effect.Kind == SkillEffectStatus && effect.Status != nil && statusPolarity(effect.Status) == CombatEffectHarmful {
			if effect.Target.Side == SkillTargetCastTarget || effect.Target.Side == SkillTargetEnemy || effect.Target.Side == SkillTargetAny || effect.Target.Side == SkillTargetHolder || effect.Target.Side == "" {
				return true
			}
		}
	}
	return false
}

func monsterPlanHasDamageTargetingPlayer(plan SkillPlan, battle *battleState, source, player CombatUnitRef) bool {
	if battle == nil {
		return false
	}
	for _, effect := range flattenedSkillEffects(plan.Effects) {
		if effect.Kind != SkillEffectDamage || effect.Damage == nil || effect.Damage.Periodic {
			continue
		}
		for _, target := range battle.resolveSkillTargets(effect.Target, source, player, player) {
			if target == player {
				return true
			}
		}
	}
	return false
}

func flattenedSkillEffects(effects []SkillEffect) []SkillEffect {
	var out []SkillEffect
	var walk func([]SkillEffect)
	walk = func(list []SkillEffect) {
		for _, effect := range list {
			out = append(out, effect)
			walk(effect.Success)
			walk(effect.Failure)
			walk(effect.Children)
		}
	}
	walk(effects)
	return out
}

func combatAttributeForNumeric(numeric int32) CombatAttribute {
	for _, candidate := range []CombatAttribute{
		CombatAttributeCritRate, CombatAttributePhysicalCritRate, CombatAttributeSpiritualCritRate,
		CombatAttributeCritValue, CombatAttributePhysicalCritValue, CombatAttributeSpiritualCritValue,
		CombatAttributeAntiCritRate, CombatAttributePhysicalAntiCritRate, CombatAttributeSpiritualAntiCritRate,
		CombatAttributeAntiCritValue, CombatAttributePhysicalAntiCritValue, CombatAttributeSpiritualAntiCritValue,
		CombatAttributeDamageReduction, CombatAttributePhysicalDamageReduction, CombatAttributeSpiritualDamageReduction,
		CombatAttributeAuxiliary,
		CombatAttributeSpeed, CombatAttributeHit, CombatAttributeResistance,
	} {
		if combatAttributeNumericType(candidate) == numeric {
			return candidate
		}
	}
	return CombatAttributeNone
}

func configuredEncounterMonsterUses() map[int32][]string {
	uses := make(map[int32][]string)
	add := func(source string, monsterID int32) {
		if monsterID > 0 {
			uses[monsterID] = append(uses[monsterID], source)
		}
	}
	addArray := func(source string, row map[string]interface{}, field string) {
		for _, raw := range arrOf(row[field]) {
			add(source, int32(num(raw)))
		}
	}
	for id, row := range tables.mainStory {
		ids, _ := rosterFromMainStoryConfig(row)
		for _, monsterID := range ids {
			add(fmt.Sprintf("MainStory[%d]", id), monsterID)
		}
	}
	for id, row := range tables.trialCopy {
		add(fmt.Sprintf("TrialCopy[%d]", id), int32(num(row["MonsterId"])))
	}
	for id, row := range tables.bossBase {
		add(fmt.Sprintf("BossBase[%d]", id), int32(num(row["MonsterId"])))
	}
	for id, row := range tables.familyBossConfig {
		add(fmt.Sprintf("FamilyBossConfig[%d]", id), int32(num(row["MonsterId"])))
	}
	for id, row := range tables.manulEquipMonsterConfig {
		add(fmt.Sprintf("ManulEquipMonsterConfig[%d]", id), int32(num(row["MonsterId"])))
	}
	for id, row := range tables.spaceTravelConfig {
		addArray(fmt.Sprintf("SpaceTravelConfig[%d]", id), row, "MonsterIdArr")
	}
	for id, row := range tables.starSoulCopy {
		addArray(fmt.Sprintf("StarSoulCopyConfig[%d]", id), row, "MonsterIdArr")
	}
	for id, row := range tables.worldBossConfig {
		addArray(fmt.Sprintf("WorldBossConfig[%d]", id), row, "MonsterId")
	}
	for id, row := range tables.journeyOfDeathConfig {
		addArray(fmt.Sprintf("JourneyOfDeathCopyConfig[%d]", id), row, "MonsterIdArr")
	}
	return uses
}

func monsterPlanHasDamage(effects []SkillEffect) bool {
	for _, effect := range effects {
		if effect.Kind == SkillEffectDamage || monsterPlanHasDamage(effect.Success) ||
			monsterPlanHasDamage(effect.Failure) || monsterPlanHasDamage(effect.Children) {
			return true
		}
	}
	return false
}
