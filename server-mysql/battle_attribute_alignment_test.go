package main

import "testing"

func TestMonsterSkill500003UsesMonsterAttackNotPlayerAttack(t *testing.T) {
	loadOnlineTablesForTest(t)
	catalog := loadTestSkillLogic(t)
	row := tables.monsterBase[10023]
	if row == nil {
		t.Fatal("MonsterBase 10023 is missing")
	}
	if int32(num(row["SkillGroupId"])) != 10002 {
		t.Fatalf("MonsterBase 10023 SkillGroupId = %d, want 10002", num(row["SkillGroupId"]))
	}
	group := tables.skillGroup[10002]
	if group == nil {
		t.Fatal("SkillGroupBase 10002 is missing")
	}
	skills := arrOf(group["SkillsArr"])
	if len(skills) != 1 {
		t.Fatalf("SkillGroupBase 10002 slots = %d, want 1", len(skills))
	}
	entry, _ := skills[0].(map[string]interface{})
	if int32(num(entry["Skills_Id"])) != 500003 {
		t.Fatalf("SkillGroupBase 10002 skill = %d, want 500003", num(entry["Skills_Id"]))
	}

	owner := &session{playerID: 1, transBonus: map[int32]float32{}}
	units := owner.buildMonsterUnitsFromRoster([]int32{10023}, []int{1})
	if len(units) != 1 {
		t.Fatal("MonsterBase 10023 did not build a combat unit")
	}
	monster := units[0]
	if monster.phyAtk != 200 || monster.spiAtk != 200 || monster.phyDef != 130 || monster.spiDef != 130 || monster.hp != 10000 {
		t.Fatalf("MonsterBase 10023 unit stats = hp=%d atk=%d/%d def=%d/%d, want 10000/200/200/130/130",
			monster.hp, monster.phyAtk, monster.spiAtk, monster.phyDef, monster.spiDef)
	}

	battle := &battleState{
		owner: owner, playerHP: 800_000_000, playerMaxHP: 800_000_000,
		phyAtk: 10_000_000, spiAtk: 10_000_000, phyDef: 0, spiDef: 0,
		monsters: units,
	}
	battle.runtime = NewCombatRuntime(battle, owner.playerID, nil)
	plan, err := catalog.Plan(500003, 1)
	if err != nil {
		t.Fatal(err)
	}
	source, target := MonsterCombatUnit(monster.id), battle.runtime.Player()
	if got := battle.runtime.EffectiveAttribute(source, CombatAttributePhysicalAttack); got != 200 {
		t.Fatalf("monster phyAtk = %d, want 200", got)
	}
	if got := battle.runtime.EffectiveAttribute(target, CombatAttributePhysicalAttack); got != 10_000_000 {
		t.Fatalf("player phyAtk = %d, want 10000000", got)
	}

	events, err := battle.executeSkillPlan(plan, source, target, func() float64 { return 0.999999 })
	if err != nil {
		t.Fatal(err)
	}
	if got := combatDamageAmount(events, source, target); got != 400 {
		t.Fatalf("500003 damage = %d, want 400 from monster atk 200; events=%+v", got, events)
	}
	if battle.playerHP != 799_999_600 {
		t.Fatalf("player HP after 500003 = %d, want 799999600", battle.playerHP)
	}

	battle.phyDef = 8504
	battle.playerHP = 800_000_000
	events, err = battle.executeSkillPlan(plan, source, target, func() float64 { return 0.999999 })
	if err != nil {
		t.Fatal(err)
	}
	if got := combatDamageAmount(events, source, target); got != 2 {
		t.Fatalf("500003 damage vs 8504 def = %d, want floor 2; events=%+v", got, events)
	}
}

func TestJob4CombatAttributesAlignWithOnlineTables(t *testing.T) {
	loadOnlineTablesForTest(t)
	if got := jobTypeOf(7); got != 4 {
		t.Fatalf("jobTypeOf(7) = %d, want 4", got)
	}
	row := roleRow(7)
	if row == nil {
		t.Fatal("RoleGrowth for job 7 is missing")
	}
	if int32(num(row["_id"])) != 4 {
		t.Fatalf("job 7 RoleGrowth id = %d, want 4", num(row["_id"]))
	}

	ss := newSession()
	ss.playerID, ss.jobID, ss.skinID, ss.level, ss.trans = 3894, 7, 120642, 17007, 2
	ss.pet = nil
	ss.worn = make(map[int32]*bagItem)

	if got, want := ss.playerMaxHp(), int32(208520); got != want {
		t.Fatalf("job7 lv17007 maxHP = %d, want %d", got, want)
	}
	if got, want := ss.playerPhyAtk(), int32(0); got != want {
		t.Fatalf("job7 lv17007 phyAtk = %d, want %d", got, want)
	}
	if got, want := ss.playerPhyDef(), int32(11228); got != want {
		t.Fatalf("job7 lv17007 phyDef = %d, want %d", got, want)
	}
	// Job 4 spiritual attack also converts Wim through CharacterGrowth 84.
	if ss.playerSpiAtk() <= ss.playerPhyAtk() {
		t.Fatalf("job7 lv17007 spiAtk = %d, want more than phyAtk %d after Wim conversion", ss.playerSpiAtk(), ss.playerPhyAtk())
	}

	battle := &battleState{
		owner: ss, playerHP: ss.battleHP(), playerMaxHP: ss.playerMaxHp(),
		playerMP: ss.battleMP(), playerMaxMP: ss.playerMaxMp(),
		phyAtk: ss.playerPhyAtk(), spiAtk: ss.playerSpiAtk(),
		phyDef: ss.playerPhyDef(), spiDef: ss.playerSpiDef(),
	}
	units := ss.buildMonsterUnitsFromRoster([]int32{10023}, []int{1})
	if len(units) != 1 {
		t.Fatal("MonsterBase 10023 did not build a combat unit")
	}
	battle.monsters = units
	battle.runtime = NewCombatRuntime(battle, ss.playerID, nil)
	player := battle.runtime.Player()
	monster := MonsterCombatUnit(units[0].id)
	if got := battle.runtime.EffectiveAttribute(player, CombatAttributeMaxHP); got != battle.playerMaxHP {
		t.Fatalf("runtime maxHP = %d, want snapshot %d", got, battle.playerMaxHP)
	}
	if got := battle.runtime.EffectiveAttribute(player, CombatAttributePhysicalAttack); got != battle.phyAtk {
		t.Fatalf("runtime phyAtk = %d, want snapshot %d", got, battle.phyAtk)
	}
	if got := battle.runtime.EffectiveAttribute(player, CombatAttributePhysicalDefense); got != battle.phyDef {
		t.Fatalf("runtime phyDef = %d, want snapshot %d", got, battle.phyDef)
	}
	if got := battle.runtime.EffectiveAttribute(monster, CombatAttributePhysicalAttack); got != units[0].phyAtk {
		t.Fatalf("runtime monster phyAtk = %d, want unit %d", got, units[0].phyAtk)
	}
	if got := battle.runtime.EffectiveAttribute(monster, CombatAttributePhysicalDefense); got != units[0].phyDef {
		t.Fatalf("runtime monster phyDef = %d, want unit %d", got, units[0].phyDef)
	}
}
