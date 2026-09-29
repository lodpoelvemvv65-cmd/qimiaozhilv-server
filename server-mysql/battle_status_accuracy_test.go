package main

import "testing"

// 比率型字段（暴击率/抗暴率/闪避/免伤…）在表里存的是小数，要 ×100 才是百分比。
// 而 Hit/Res 不在这份名单里：EquipBase 里它们是 1–200 / 1–170 的整数点数，
// 与 PhyAtk/Hp 同量纲；线上抓包也从不以小数下发这两个键。
func TestOnlyRateFieldsBecomeCombatPercentages(t *testing.T) {
	owner := &session{transBonus: map[int32]float32{1032: 200, 1013: 0.06}}
	monster := &monsterUnit{
		id: 200, hp: 1000, maxHP: 1000, alive: true,
		extraNumeric: map[int32]float64{1033: 170, 1013: 0.03},
	}
	battle := &battleState{owner: owner, playerHP: 1000, playerMaxHP: 1000, monsters: []*monsterUnit{monster}}
	battle.runtime = NewCombatRuntime(battle, 100, nil)
	player := battle.runtime.Player()
	target := MonsterCombatUnit(monster.id)

	// 比率型：0.06 → 6%（用具体枚举 PhysicalCritRate=1013；通用的
	// CombatAttributeCritRate 不在 combatAttributeNumericType 映射里）
	if got := battle.runtime.EffectivePercentAttribute(player, CombatAttributePhysicalCritRate); got < 5.999 || got > 6.001 {
		t.Fatalf("0.06 crit rate = %v%%, want 6%%", got)
	}
	if got := battle.runtime.EffectivePercentAttribute(target, CombatAttributePhysicalCritRate); got != 3 {
		t.Fatalf("0.03 monster crit rate = %v%%, want 3%%", got)
	}
	// 点数型：原样，不放大
	if got := battle.runtime.EffectivePercentAttribute(player, CombatAttributeHit); got != 200 {
		t.Fatalf("Hit 200 被算成 %v，它不是比率型字段，不该 ×100", got)
	}
	if got := battle.runtime.EffectivePercentAttribute(target, CombatAttributeResistance); got != 170 {
		t.Fatalf("怪物抗性 170 被算成 %v，它不是比率型字段，不该 ×100", got)
	}
}

func TestPVPHitAndResistanceContestHarmfulStatusChance(t *testing.T) {
	tests := []struct {
		name       string
		hit        float32
		resistance float64
		roll       float64
		wantStatus bool
	}{
		{name: "equal ratings preserve configured chance", hit: 100, resistance: 100, roll: 0.49, wantStatus: true},
		{name: "hit raises configured chance", hit: 200, resistance: 100, roll: 0.70, wantStatus: true},
		{name: "resistance lowers configured chance", resistance: 100, roll: 0.30, wantStatus: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			owner := &session{transBonus: map[int32]float32{1032: test.hit}}
			monster := &monsterUnit{
				id: 200, hp: 1000, maxHP: 1000, alive: true,
				extraNumeric: map[int32]float64{1033: test.resistance},
			}
			battle := &battleState{
				owner: owner, playerHP: 1000, playerMaxHP: 1000, monsters: []*monsterUnit{monster},
				pvp: &pvpBattle{members: map[int64]*battleState{}},
			}
			battle.runtime = NewCombatRuntime(battle, 100, nil)
			target := MonsterCombatUnit(monster.id)
			status := &SkillStatusPlan{ModifierID: 9001, DurationSeconds: 10, StateKey: 9, IsDebuff: true, IsControl: true}
			plan := SkillPlan{Effects: []SkillEffect{triggeredStatusChance(status)}}
			rolls := 0
			_, err := battle.executeSkillPlan(plan, battle.runtime.Player(), target, func() float64 {
				rolls++
				return test.roll
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := battle.runtime.HasStatus(target, CombatStatusStunned); got != test.wantStatus {
				t.Fatalf("stunned=%t, want %t", got, test.wantStatus)
			}
			if rolls != 1 {
				t.Fatalf("status accuracy rolls=%d, want one combined roll", rolls)
			}
		})
	}
}

func TestPVPUnwrappedHarmfulStatusStillUsesResistance(t *testing.T) {
	monster := &monsterUnit{
		id: 200, hp: 1000, maxHP: 1000, alive: true,
		extraNumeric: map[int32]float64{1033: 100},
	}
	battle := &battleState{
		owner: &session{}, playerHP: 1000, playerMaxHP: 1000, monsters: []*monsterUnit{monster},
		pvp: &pvpBattle{members: map[int64]*battleState{}},
	}
	battle.runtime = NewCombatRuntime(battle, 100, nil)
	target := MonsterCombatUnit(monster.id)
	status := &SkillStatusPlan{ModifierID: 9002, DurationSeconds: 10, StateKey: 9, IsDebuff: true, IsControl: true}
	effect := SkillEffect{
		Kind: SkillEffectStatus, Trigger: SkillTrigger{Scope: "skill", Event: 1},
		Target: SkillTargetPlan{Kind: SkillTargetSingle, Side: SkillTargetCastTarget},
		Status: status, ModifierID: status.ModifierID,
	}
	if _, err := battle.executeSkillPlan(SkillPlan{Effects: []SkillEffect{effect}}, battle.runtime.Player(), target, func() float64 { return 0.75 }); err != nil {
		t.Fatal(err)
	}
	if battle.runtime.HasStatus(target, CombatStatusStunned) {
		t.Fatal("100 resistance did not reduce an unwrapped harmful status to 50%")
	}
}

func TestOnlineStateKeysHaveCorrectPolarity(t *testing.T) {
	tests := []struct {
		name string
		key  int32
		want CombatEffectPolarity
	}{
		{name: "silence", key: 8, want: CombatEffectHarmful},
		{name: "stun", key: 9, want: CombatEffectHarmful},
		{name: "freeze", key: 10, want: CombatEffectHarmful},
		{name: "petrify", key: 11, want: CombatEffectHarmful},
		{name: "invincible", key: 1, want: CombatEffectBeneficial},
		{name: "undying", key: 4, want: CombatEffectBeneficial},
		{name: "revive", key: 5, want: CombatEffectBeneficial},
		{name: "guaranteed crit", key: 14, want: CombatEffectBeneficial},
		{name: "invisible", key: 15, want: CombatEffectBeneficial},
		{name: "shield", key: 100, want: CombatEffectBeneficial},
		{name: "reflect", key: 101, want: CombatEffectBeneficial},
		{name: "counter", key: 102, want: CombatEffectBeneficial},
		{name: "damage reduction", key: 107, want: CombatEffectBeneficial},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status := &SkillStatusPlan{StateKey: test.key, IsControl: true}
			if got := statusPolarity(status); got != test.want {
				t.Fatalf("state key %d polarity = %v, want %v", test.key, got, test.want)
			}
		})
	}
	if got := statusPolarity(&SkillStatusPlan{StateKey: 999, IsControl: true}); got != CombatEffectNeutral {
		t.Fatalf("unknown state key polarity = %v, want neutral", got)
	}
}

func TestBeneficialStateIsNotRejectedByHighResistance(t *testing.T) {
	owner := &session{playerID: 1, transBonus: map[int32]float32{1033: 1_000_000}}
	monster := &monsterUnit{id: 200, hp: 1000, maxHP: 1000, alive: true}
	battle := &battleState{owner: owner, playerHP: 1000, playerMaxHP: 1000, monsters: []*monsterUnit{monster}}
	battle.runtime = NewCombatRuntime(battle, owner.playerID, nil)
	player := battle.runtime.Player()
	effect := SkillEffect{
		Kind: SkillEffectStatus, ModifierID: 9101,
		Target: SkillTargetPlan{Kind: SkillTargetSingle, Side: SkillTargetSelf},
		Status: &SkillStatusPlan{ModifierID: 9101, StateKey: 1, DurationSeconds: 10, IsControl: true},
	}
	events, err := battle.applyStatusEffect(effect, player, player, player, func() float64 { return 0.999999 })
	if err != nil {
		t.Fatal(err)
	}
	if !battle.runtime.HasStatus(player, CombatStatusInvincible) {
		t.Fatalf("invincibility was rejected by Resistance: events=%+v", events)
	}
}

func TestPVPEnemyCopiesSpeedHitAndResistance(t *testing.T) {
	target := &session{transBonus: map[int32]float32{1031: 7, 1032: 11, 1033: 13}}
	enemy := pvpEnemyUnit(target)
	for numericType, want := range map[int32]float64{1031: 7, 1032: 11, 1033: 13} {
		if got := enemy.extraNumeric[numericType]; got != want {
			t.Errorf("PVP numeric %d = %v, want %v", numericType, got, want)
		}
	}
}

func triggeredStatusChance(status *SkillStatusPlan) SkillEffect {
	return SkillEffect{
		Kind: SkillEffectChance, Trigger: SkillTrigger{Scope: "skill", Event: 1}, ChancePercent: 50,
		Success: []SkillEffect{{
			Kind:   SkillEffectStatus,
			Target: SkillTargetPlan{Kind: SkillTargetSingle, Side: SkillTargetCastTarget},
			Status: status, ModifierID: status.ModifierID,
		}},
	}
}

// Online family-boss capture evidence proves that PvE monster status
// probability is not reduced by the player's Resistance rating.
func TestMonsterHarmfulStatusUsesConfiguredChanceInPVE(t *testing.T) {
	monster := &monsterUnit{id: 200, hp: 1000, maxHP: 1000, alive: true} // 线上怪物：没有评分

	// A geared player's measured Resistance does not change the configured chance.
	geared := &session{playerID: 100, transBonus: map[int32]float32{1033: 245.6}}
	battle := &battleState{owner: geared, playerHP: 10_000, playerMaxHP: 10_000, monsters: []*monsterUnit{monster}}
	battle.runtime = NewCombatRuntime(battle, geared.playerID, nil)
	source := MonsterCombatUnit(monster.id)
	player := battle.runtime.Player()

	if got := battle.runtime.EffectivePercentAttribute(player, CombatAttributeResistance); got < 245.5 || got > 245.7 {
		t.Fatalf("fixture drifted: player resistance = %v, want 245.6（点数，不再 ×100）", got)
	}
	if got := battle.statusContestChance(60, source, player); got != 60 {
		t.Fatalf("PvE configured 60%% changed by Resistance to %v%%", got)
	}

	// A fresh player's lower Resistance produces the same configured chance.
	fresh := &session{playerID: 101, transBonus: map[int32]float32{1033: 15.60364}}
	freshBattle := &battleState{owner: fresh, playerHP: 10_000, playerMaxHP: 10_000, monsters: []*monsterUnit{monster}}
	freshBattle.runtime = NewCombatRuntime(freshBattle, fresh.playerID, nil)
	if got := freshBattle.statusContestChance(60, MonsterCombatUnit(monster.id), freshBattle.runtime.Player()); got != 60 {
		t.Fatalf("PvE configured 60%% changed by low Resistance to %v%%", got)
	}
}

// PvE 豁免不能顺带把 PvP 也豁免掉：PvP 的"敌方"是 CombatSideMonster 单位，
// 但它的 Hit/Resistance 是从对位玩家拷贝的真实数值（pvpEnemyUnit），对抗有意义。
func TestPVPEnemyHarmfulStatusStillContestsResistance(t *testing.T) {
	owner := &session{playerID: 100, transBonus: map[int32]float32{1033: 15.60364}}
	monster := &monsterUnit{id: 200, hp: 1000, maxHP: 1000, alive: true}
	battle := &battleState{
		owner: owner, playerHP: 1000, playerMaxHP: 1000, monsters: []*monsterUnit{monster},
		pvp: &pvpBattle{members: map[int64]*battleState{}},
	}
	battle.runtime = NewCombatRuntime(battle, owner.playerID, nil)
	player := battle.runtime.Player()

	status := &SkillStatusPlan{ModifierID: 9103, DurationSeconds: 10, StateKey: 9, IsDebuff: true, IsControl: true}
	plan := SkillPlan{Effects: []SkillEffect{triggeredStatusChance(status)}}
	if _, err := battle.executeSkillPlan(plan, MonsterCombatUnit(monster.id), player, func() float64 { return 0.49 }); err != nil {
		t.Fatal(err)
	}
	if battle.runtime.HasStatus(player, CombatStatusStunned) {
		t.Fatal("PvP 里敌方对玩家的有害状态不再受抗性对抗；PvP 敌方的命中/抗性是拷贝来的真实数值，必须保留")
	}
}

// PvE player-to-monster status also uses the configured probability directly.
func TestPlayerHarmfulStatusIgnoresMonsterLackOfResistance(t *testing.T) {
	// 毕业装实测 Hit≈191.29（装备表里 Hit 是 1–200 的整数点数，不是比率）。
	owner := &session{playerID: 100, transBonus: map[int32]float32{1032: 191.29122}}
	// 线上怪物：extraNumeric 里既没有 1032 也没有 1033。
	monster := &monsterUnit{id: 200, hp: 1_000_000, maxHP: 1_000_000, alive: true}
	battle := &battleState{owner: owner, playerHP: 1000, playerMaxHP: 1000, monsters: []*monsterUnit{monster}}
	battle.runtime = NewCombatRuntime(battle, owner.playerID, nil)
	player := battle.runtime.Player()
	target := MonsterCombatUnit(monster.id)

	if got := battle.runtime.EffectivePercentAttribute(player, CombatAttributeHit); got < 191.2 || got > 191.4 {
		t.Fatalf("fixture drifted: player hit = %v, want the measured 191.29（点数）", got)
	}
	if got := battle.runtime.EffectivePercentAttribute(target, CombatAttributeResistance); got != 0 {
		t.Fatalf("fixture drifted: monster resistance = %v, want 0 (online MonsterBase has no Res field)", got)
	}

	if got := battle.statusContestChance(18, player, target); got != 18 {
		t.Fatalf("配置 18%% 被算成 %v%%；怪物没有抗性数据时应当直接按配置走", got)
	}
	// Injected ratings do not silently turn an ordinary PvE battle into PvP.
	monster.extraNumeric = map[int32]float64{1033: 1}
	if got := battle.statusContestChance(18, player, target); got != 18 {
		t.Fatalf("PvE configured 18%% changed by injected monster Resistance to %v%%", got)
	}
}
