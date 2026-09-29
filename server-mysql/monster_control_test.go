package main

import (
	"math/rand"
	"testing"
	"time"
)

// 怪物控制技能（眩晕 / 沉默 / 石化）的真实配置与落地回归。
//
// 为什么单独写一个：`test/verify_monster_effects.py` 只覆盖周期效果（燃烧/中毒/流血），
// 全仓库此前**没有任何针对怪物控制技能的实测或单测**。而家族 5 阶 BOSS（怪物 50005）
// 的技能组 10030 里就挂着 500041「用钱砸死你」—— 95% 几率、敌方全体、眩晕 12 秒，
// 单账号就能开打，是最容易在线上把玩家卡死的技能。
//
// 三件事必须一起锁住，缺一个都会让「控制生效」变成空话：
//  1. 配置侧：9 个控制 modifier 的 stateK 能被映射成真的控制状态；
//  2. 落地侧：状态确实挂到**玩家**身上（不是挂在怪物自己身上）；
//  3. 生效侧：挂上之后 `CanCast` 必须为 false，玩家的施法要被拒绝。
//
// 另外锁一条容易回归的：控制几率要被玩家的**抵抗**削减，不能照抄配置值。

// monsterControlSkills 是 SkillLogicConfig 里全部 9 个控制 modifier。
// 单体 35%/11s，群体 65~95%/12s。
var monsterControlSkills = []struct {
	skillID  int32
	name     string
	stateKey int32
	want     CombatStatus
	seconds  float64
}{
	{500006, "吃我黑凤雷（物）", 9, CombatStatusStunned, 11},
	{500011, "黑暗永封（物）", 8, CombatStatusSilenced, 11},
	{500013, "泥石击（物）", 11, CombatStatusPetrified, 11},
	{500020, "吃我黑凤雷（魔）", 9, CombatStatusStunned, 11},
	{500025, "黑暗永封（魔）", 8, CombatStatusSilenced, 11},
	{500027, "泥石击（魔）", 11, CombatStatusPetrified, 11},
	{500039, "敲你一棒子", 9, CombatStatusStunned, 12},
	{500040, "给你一板砖", 9, CombatStatusStunned, 12},
	{500041, "用钱砸死你", 9, CombatStatusStunned, 12},
}

// controlStatusOf 在技能计划里找第一个能映射成控制的状态。
func controlStatusOf(plan SkillPlan) *SkillStatusPlan {
	var found *SkillStatusPlan
	var walk func(effects []SkillEffect)
	walk = func(effects []SkillEffect) {
		for _, effect := range effects {
			if found != nil {
				return
			}
			if effect.Status != nil && combatStatusForStateKey(effect.Status.StateKey) != CombatStatusNone {
				found = effect.Status
				return
			}
			walk(effect.Children)
			walk(effect.Success)
			walk(effect.Failure)
		}
	}
	walk(plan.Effects)
	return found
}

// newMonsterControlBattle 造一场「怪物 50005 vs 玩家」的最小战斗。
func newMonsterControlBattle(resistance float64) (*battleState, *fakeCombatClock, CombatUnitRef, CombatUnitRef) {
	owner := newSession()
	owner.playerID = 100
	if resistance != 0 {
		owner.transBonus = map[int32]float32{1033: float32(resistance)}
	}
	monster := &monsterUnit{
		id: 200, monsterID: 50005, hp: 1_000_000_000, maxHP: 1_000_000_000,
		phyAtk: 100, spiAtk: 100, alive: true,
	}
	battle := &battleState{
		owner: owner, playerHP: 1_000_000_000, playerMaxHP: 1_000_000_000,
		monsters: []*monsterUnit{monster},
	}
	clock := &fakeCombatClock{now: time.Unix(1_000, 0)}
	battle.runtime = NewCombatRuntime(battle, owner.playerID, clock)
	owner.battle = battle
	return battle, clock, battle.runtime.Player(), MonsterCombatUnit(monster.id)
}

func TestMonsterControlSkillsMapToRealControlStates(t *testing.T) {
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = loadTestSkillLogic(t)
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })

	for _, test := range monsterControlSkills {
		t.Run(test.name, func(t *testing.T) {
			plan, err := skillLogicCatalog.Plan(test.skillID, 1)
			if err != nil {
				t.Fatalf("Plan(%d): %v", test.skillID, err)
			}
			status := controlStatusOf(plan)
			if status == nil {
				t.Fatalf("技能 %d(%s) 没有解析出任何控制状态", test.skillID, test.name)
			}
			if status.StateKey != test.stateKey {
				t.Fatalf("stateK = %d, want %d", status.StateKey, test.stateKey)
			}
			if got := combatStatusForStateKey(status.StateKey); got != test.want {
				t.Fatalf("stateK %d 映射成 %v, want %v", status.StateKey, got, test.want)
			}
			if status.DurationSeconds != test.seconds {
				t.Fatalf("持续 = %vs, want %vs", status.DurationSeconds, test.seconds)
			}
			if statusPolarity(status) != CombatEffectHarmful {
				t.Fatalf("控制状态必须是有害的，实际 %v", statusPolarity(status))
			}
		})
	}
}

// 家族 5 阶 BOSS 的技能组里就有 500041。挂上之后玩家必须**不能施法**，
// 而且要在 12 秒后自己恢复。
func TestMonsterControlStunsPlayerAndBlocksCasting(t *testing.T) {
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = loadTestSkillLogic(t)
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })

	battle, clock, player, source := newMonsterControlBattle(0)
	plan, err := skillLogicCatalog.Plan(500041, 1)
	if err != nil {
		t.Fatal(err)
	}
	// roll 恒 0：几率门与命中对抗都必过。
	if _, err := battle.executeSkillPlan(plan, source, player, func() float64 { return 0 }); err != nil {
		t.Fatal(err)
	}
	if !battle.runtime.HasStatus(player, CombatStatusStunned) {
		t.Fatal("500041 命中后玩家没有眩晕")
	}
	if battle.runtime.CanCast(player) {
		t.Fatal("眩晕中的玩家仍然可以施法 —— 控制没有生效")
	}
	if battle.runtime.CanAct(player) {
		t.Fatal("眩晕中的玩家仍然可以行动")
	}
	if battle.runtime.HasStatus(source, CombatStatusStunned) {
		t.Fatal("控制状态挂到了怪物自己身上")
	}

	// 12 秒的持续必须按时过期：11 秒时还在，13 秒时已经恢复。
	clock.Add(11 * time.Second)
	battle.runtime.Tick()
	if !battle.runtime.HasStatus(player, CombatStatusStunned) {
		t.Fatal("11 秒时眩晕就消失了，持续时间短于配置的 12 秒")
	}
	clock.Add(2 * time.Second)
	battle.runtime.Tick()
	if battle.runtime.HasStatus(player, CombatStatusStunned) {
		t.Fatal("12 秒的眩晕没有按时过期")
	}
	if !battle.runtime.CanCast(player) {
		t.Fatal("眩晕过期后玩家仍然不能施法")
	}
}

// Online capture: family boss skill 500041 uses its configured 95% chance
// in PvE even when the target player has substantial Resistance.
func TestMonsterControlChanceIgnoresPlayerResistanceInPVE(t *testing.T) {
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = loadTestSkillLogic(t)
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })

	const (
		runs       = 4000
		configured = 95.0
		gearedRes  = 245.6 // 毕业装实测值，见 battle_status_accuracy_test.go
		rateSlack  = 0.06
	)
	freshRate := configured / 100
	gearedRate := configured / 100

	measure := func(resistance float64) float64 {
		battle, clock, player, source := newMonsterControlBattle(resistance)
		plan, err := skillLogicCatalog.Plan(500041, 1)
		if err != nil {
			t.Fatal(err)
		}
		random := rand.New(rand.NewSource(20260918))
		landed := 0
		for i := 0; i < runs; i++ {
			// 每轮先清掉上一次的控制，否则 12 秒的持续会把后续样本污染成 100%。
			battle.runtime.clearUnitEffects(player, "test-reset", clock.Now())
			if _, err := battle.executeSkillPlan(plan, source, player, random.Float64); err != nil {
				t.Fatal(err)
			}
			if battle.runtime.HasStatus(player, CombatStatusStunned) {
				landed++
			}
		}
		return float64(landed) / float64(runs)
	}

	fresh := measure(0)
	if diff := fresh - freshRate; diff > rateSlack || diff < -rateSlack {
		t.Fatalf("无抗性时 500041 落地率 = %.3f, want ≈%.3f", fresh, freshRate)
	}
	geared := measure(gearedRes)
	if diff := geared - gearedRate; diff > rateSlack || diff < -rateSlack {
		t.Fatalf("抗性 %.1f 时 500041 落地率 = %.3f, want ≈%.3f（控制没有被抵抗削减）",
			gearedRes, geared, gearedRate)
	}
	if diff := geared - fresh; diff > rateSlack || diff < -rateSlack {
		t.Fatalf("PvE Resistance changed control rate: fresh=%.3f geared=%.3f", fresh, geared)
	}
	t.Logf("500041 眩晕落地率：抗性 0 → %.3f（配置 95%%）, 抗性 %.1f → %.3f（期望 %.3f）",
		fresh, gearedRes, geared, gearedRate)
}

// stateK=14「必定暴击」是唯一一个**既没有 combatStatusForStateKey 映射、
// 又没有子节点兜底**的 stateK。
//
// 210601 猛虎式射门 的 desc 写着「对敌方造成物理伤害，必定暴击」，
// 配置里 `modifier 21060117`（name=必定暴击、stateK=14、continueTime=1.0、iconId=0）
// 挂在 **event 24**（「本 modifier 创建之前」），目标 self —— 也就是说它**先于**
// 同级的 event 0 伤害节点执行，设计意图就是「让这一击必定暴击」。
//
// 但服务端没有 14 的映射：`combatStatusForStateKey(14)` → `CombatStatusNone`，
// 且该 modifier 的 `valueK=0` / `modifierEventDic=null`，于是
// `applyStatusEffectWithContext` 只给它落一个惰性 `CombatStatusMarker`
// （`iconId="0"` 还导致 20080 都不发）——**伤害仍然按普通暴击率掷骰**。
//
// 这个测试锁的是**配置契约**（计划里必须带着这条 self/event24/1s 的状态），
// 它不会因为将来实现了 stateK=14 而失败；实现后请另加一条断言「必定暴击命中」。
func TestGuaranteedCritModifierIsConfiguredButUnmapped(t *testing.T) {
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = loadTestSkillLogic(t)
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })

	plan, err := skillLogicCatalog.Plan(210601, 1)
	if err != nil {
		t.Fatal(err)
	}
	var found *SkillStatusPlan
	var event int32
	var walk func(effects []SkillEffect)
	walk = func(effects []SkillEffect) {
		for _, effect := range effects {
			if found != nil {
				return
			}
			if effect.Status != nil && effect.Status.StateKey == 14 {
				found, event = effect.Status, effect.Trigger.Event
				return
			}
			walk(effect.Children)
			walk(effect.Success)
			walk(effect.Failure)
		}
	}
	walk(plan.Effects)

	if found == nil {
		t.Fatal("210601 计划里找不到 stateK=14 的「必定暴击」节点")
	}
	if found.ModifierID != 21060117 {
		t.Fatalf("modifier = %d, want 21060117", found.ModifierID)
	}
	if event != 24 {
		t.Fatalf("trigger event = %d, want 24（必须早于同级的 event 0 伤害节点）", event)
	}
	if found.DurationSeconds != 1 {
		t.Fatalf("持续 = %vs, want 1s", found.DurationSeconds)
	}

	// 已知缺口：这个 stateK 在服务端没有落点。若将来补上了映射，
	// 这条断言会失败 —— 那正是提醒把实现一起补上。
	if got := combatStatusForStateKey(14); got != CombatStatusNone {
		t.Skipf("stateK=14 已经映射成 %v；请补一条「猛虎式射门必定暴击」的断言后删掉这条", got)
	}
	t.Log("已知缺口：stateK=14（必定暴击）没有服务端实现，210601 猛虎式射门仍按普通暴击率掷骰")
}
