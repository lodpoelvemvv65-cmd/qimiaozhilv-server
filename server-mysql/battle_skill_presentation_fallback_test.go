package main

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"mhqserver/protocol"
)

func TestConfiguredPresentationAddsOnlineHurtEffectsToSupportSkills(t *testing.T) {
	loadOnlineTablesForTest(t)
	catalog := loadTestSkillLogic(t)

	cases := []struct {
		skillID  int32
		effectID int32
	}{
		{skillID: 110301, effectID: 2107}, // 魔鬼式训练
		{skillID: 110303, effectID: 2121}, // 团结一心
		{skillID: 310401, effectID: 2308}, // 高级急救
		{skillID: 310501, effectID: 2309}, // 连锁注射
		{skillID: 310503, effectID: 2324}, // 嗜血
		{skillID: 310601, effectID: 2310}, // 人体学（逻辑中已有独立特效）
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("skill-%d", tc.skillID), func(t *testing.T) {
			plan, err := catalog.Plan(tc.skillID, 1)
			if err != nil {
				t.Fatal(err)
			}
			plan = withConfiguredSkillPresentation(plan)
			visuals := skillPlanVisualIDs(plan.Effects)
			if len(visuals) != 1 || visuals[0] != tc.effectID {
				t.Fatalf("skill %d visuals=%v, want [%d]", tc.skillID, visuals, tc.effectID)
			}

			monster := &monsterUnit{id: 200, hp: 1000, maxHP: 1000, alive: true}
			battle := &battleState{
				playerHP: 1000, playerMaxHP: 1000, playerMP: 1000, playerMaxMP: 1000,
				monsters: []*monsterUnit{monster},
			}
			battle.runtime = NewCombatRuntime(battle, 100, nil)
			source := battle.runtime.Player()
			primary := source
			if tc.skillID == 310503 {
				primary = MonsterCombatUnit(monster.id)
			}
			events, err := battle.executeSkillPlan(plan, source, primary, func() float64 { return 0 })
			if err != nil {
				t.Fatal(err)
			}
			var found []CombatEvent
			for _, event := range events {
				if event.Type == CombatEventVisual && event.EffectID == tc.effectID {
					found = append(found, event)
				}
			}
			if len(found) != 1 || found[0].Target != source {
				t.Fatalf("skill %d visual events=%+v, want one event on caster", tc.skillID, found)
			}
		})
	}
}

func TestSupportVisualsShareActualRandomTreatmentTargets(t *testing.T) {
	loadOnlineTablesForTest(t)
	catalog := loadTestSkillLogic(t)
	for _, chooseMaximum := range []bool{false, true} {
		_, party, first, _ := newSharedPartyBattleForTest(t)
		battle := first.session.battle
		for id := int64(303); id <= 505; id += 101 {
			ss := &session{playerID: id}
			member := &battleState{owner: ss, party: party, monsters: battle.monsters}
			ss.battle = member
			member.runtime = NewCombatRuntime(member, id, nil)
			party.members[id] = member
			party.memberIDs = append(party.memberIDs, id)
		}
		for _, member := range party.members {
			member.playerHP, member.playerMaxHP = 1, 10000
		}
		party.shareCombatState()
		clock := &fakeCombatClock{now: time.Unix(1000, 0)}
		for _, member := range party.members {
			member.runtime.clock = clock
		}
		battle.spiAtk = 1000
		original, err := catalog.Plan(310501, 1)
		if err != nil {
			t.Fatal(err)
		}
		// Exercise the table fallback with the online random selector and
		// treatment, but without its optional explicit visual node.
		original.Effects = withoutSkillVisualNodesForTest(original.Effects)
		plan := withConfiguredSkillPresentation(original)
		plan = withConfiguredSkillPresentation(plan)
		if len(skillPlanVisualIDs(original.Effects)) != 0 || len(skillPlanVisualIDs(plan.Effects)) != 1 {
			t.Fatalf("presentation mutated original or duplicated: before=%v after=%v", skillPlanVisualIDs(original.Effects), skillPlanVisualIDs(plan.Effects))
		}
		ctx := newSkillExecutionContext(battle, func() float64 { return .999999 })
		ctx.targetIntn = func(limit int) int {
			if chooseMaximum {
				return limit - 1
			}
			return 0
		}
		events, err := battle.executeSkillPlanWithContext(plan, battle.runtime.Player(), battle.runtime.Player(), ctx)
		if err != nil {
			t.Fatal(err)
		}
		clock.Add(maxPublicActionInterval)
		events = append(events, battle.runtime.Tick()...)
		healed, shown := map[int64]int{}, map[int64]int{}
		for _, event := range events {
			if event.Type == CombatEventHeal && event.Amount > 0 {
				healed[event.Target.ID]++
			}
			if event.Type == CombatEventVisual && event.EffectID == 2309 {
				shown[event.Target.ID]++
			}
		}
		want := 3
		if chooseMaximum {
			want = 5
		}
		if len(healed) != want || !reflect.DeepEqual(healed, shown) {
			t.Fatalf("random maximum=%v healed=%v shown=%v, want the same %d targets", chooseMaximum, healed, shown, want)
		}
	}
}

func withoutSkillVisualNodesForTest(effects []SkillEffect) []SkillEffect {
	var result []SkillEffect
	for _, effect := range effects {
		if effect.Kind == SkillEffectVisual {
			continue
		}
		effect.Success = withoutSkillVisualNodesForTest(effect.Success)
		effect.Failure = withoutSkillVisualNodesForTest(effect.Failure)
		effect.Children = withoutSkillVisualNodesForTest(effect.Children)
		result = append(result, effect)
	}
	return result
}

func TestConfiguredPresentationCannotActivatePassiveTriggers(t *testing.T) {
	loadOnlineTablesForTest(t)
	catalog := loadTestSkillLogic(t)
	for id, skill := range catalog.Skills {
		for level := int32(1); level <= skill.MaxLevel; level++ {
			plan, err := catalog.Plan(id, level)
			if err != nil {
				t.Fatal(err)
			}
			if !skillPlanCanCast(plan) && skillPlanCanCast(withConfiguredSkillPresentation(plan)) {
				t.Fatalf("presentation made passive skill %d level %d castable", id, level)
			}
		}
	}
}

func TestSupportVisualIsImmediateWithoutPendingProjectile(t *testing.T) {
	loadOnlineTablesForTest(t)
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = loadTestSkillLogic(t)
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })
	for _, test := range []struct {
		skill, effect int32
		leave         bool
	}{
		{310401, 2308, false}, {310501, 2309, false}, {310601, 2310, false}, {310401, 2308, true},
	} {
		ss := newSession()
		ss.playerID, ss.level = 100, 100
		ss.skills = map[int32]int32{test.skill: 1}
		battle := &battleState{
			owner: ss, playerHP: 1000, playerMaxHP: 1000, playerMP: 10000, playerMaxMP: 10000,
			spiAtk: 1000, monsters: []*monsterUnit{{id: 200, hp: 10000, maxHP: 10000, alive: true}},
			monsterReadyAt: time.Now().Add(time.Hour),
		}
		ss.battle = battle
		ch := &channel{session: ss, conn: &recordingConn{}}
		server, phases := newQueuedCombatServer()
		if _, cast := server.castSkillLocked(ch, test.skill, &protocol.M2C_UseMainUISkill{}); !cast {
			t.Fatalf("skill %d rejected", test.skill)
		}
		conn := ch.conn.(*recordingConn)
		if phases.len() != 0 {
			t.Fatal("support cast incorrectly scheduled a projectile")
		}
		if test.leave {
			ss.battle = nil
		}
		visuals := recordedSkillVisuals(t, conn.Bytes())
		if len(visuals) != 1 || visuals[0].EffectId != test.effect || visuals[0].Time != 0 ||
			visuals[0].UnitId != ss.playerID || visuals[0].TargetId != ss.playerID {
			t.Fatalf("skill %d delayed visual=%v", test.skill, visuals)
		}
	}
}

func TestConfiguredPresentationUsesAlliesForGroupSupportVisuals(t *testing.T) {
	loadOnlineTablesForTest(t)
	catalog := loadTestSkillLogic(t)
	_, _, first, second := newSharedPartyBattleForTest(t)
	battle := first.session.battle
	plan, err := catalog.Plan(110301, 1)
	if err != nil {
		t.Fatal(err)
	}
	plan = withConfiguredSkillPresentation(plan)
	events, err := battle.executeSkillPlan(
		plan,
		PlayerCombatUnit(first.session.playerID),
		PlayerCombatUnit(second.session.playerID),
		func() float64 { return 0 },
	)
	if err != nil {
		t.Fatal(err)
	}
	targets := make(map[int64]bool)
	for _, event := range events {
		if event.Type == CombatEventVisual && event.EffectID == 2107 {
			targets[event.Target.ID] = true
		}
	}
	if len(targets) != 2 || !targets[first.session.playerID] || !targets[second.session.playerID] {
		t.Fatalf("devil training visual targets=%v, want both party members", targets)
	}
}
