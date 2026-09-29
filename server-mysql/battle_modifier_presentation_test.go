package main

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"mhqserver/protocol"
)

func TestMonsterModifierOnlyVisualsUseConfiguredTargetsAndLifetime(t *testing.T) {
	loadOnlineTablesForTest(t)
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = loadTestSkillLogic(t)
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })
	for _, test := range []struct {
		skill, effect, lifetime int32
		self                    bool
	}{
		{500029, 3025, 21000, false}, {500030, 3025, 21000, false},
		{500031, 3027, 11000, true}, {500032, 3028, 11000, true},
	} {
		server, _, first, second := newSharedPartyBattleForTest(t)
		phases := installQueuedCombatScheduler(server)
		battle := first.session.battle
		monster := battle.monsters[0]
		monster.monsterID, monster.phyAtk, monster.spiAtk = 990001, 10, 10
		tables.monsterBase[990001] = map[string]interface{}{"SkillGroupId": int64(990001)}
		tables.skillGroup[990001] = map[string]interface{}{"SkillsArr": []interface{}{
			map[string]interface{}{"Skills_Id": test.skill},
		}}
		server.monstersAttackAt(first, battle, time.Now())
		if len(recordedSkillVisuals(t, first.conn.(*recordingConn).Bytes())) != 0 {
			t.Fatal("monster modifier visual played before the attack windup")
		}
		phases.runNext(t, time.Second)
		phases.runNext(t, 0)
		for _, recipient := range []*channel{first, second} {
			visuals := recordedSkillVisuals(t, recipient.conn.(*recordingConn).Bytes())
			wantCount := 2
			if test.self {
				wantCount = 1
			}
			if len(visuals) != wantCount {
				t.Fatalf("skill %d recipient %d visuals=%v", test.skill, recipient.id, visuals)
			}
			targets := map[int64]bool{}
			for _, visual := range visuals {
				if visual.UnitId != monster.id || visual.EffectId != test.effect || visual.Time != test.lifetime {
					t.Fatalf("skill %d visual=%v; want effect %d lasting %dms", test.skill, visual, test.effect, test.lifetime)
				}
				targets[visual.TargetId] = true
			}
			if test.self && !targets[monster.id] || !test.self && (!targets[first.session.playerID] || !targets[second.session.playerID]) {
				t.Fatalf("skill %d wrong visual targets %v", test.skill, targets)
			}
			for _, frame := range decodeRecordedFrames(t, recipient.conn.(*recordingConn).Bytes()) {
				if frame.opcode != protocol.OpM2C_BattleChangeState {
					continue
				}
				var state protocol.M2C_BattleChangeState
				if err := proto.Unmarshal(frame.body, &state); err != nil {
					t.Fatal(err)
				}
				if state.TargetUnitId == monster.id {
					t.Fatal("monster received unsupported BuffComponent message")
				}
			}
		}
	}
}

func TestMonsterProjectileKeepsSecondaryModifierVisuals(t *testing.T) {
	loadOnlineTablesForTest(t)
	catalog := loadTestSkillLogic(t)
	for _, test := range []struct{ skill, secondary int32 }{
		{500006, 2211}, {500007, 2222}, {500011, 2116},
	} {
		server, _, first, _ := newSharedPartyBattleForTest(t)
		battle := first.session.battle
		battle.monsters[0].phyAtk = 10
		plan, err := catalog.Plan(test.skill, 1)
		if err != nil {
			t.Fatal(err)
		}
		cast := monsterSkillCast{plan: withMonsterSkillPresentation(plan), source: MonsterCombatUnit(battle.monsters[0].id), primary: battle.runtime.Player()}
		launches, impacts, _ := battle.monsterProjectilePresentation(cast)
		events, err := battle.executeSkillPlan(cast.plan, cast.source, cast.primary, func() float64 { return 0 })
		if err != nil {
			t.Fatal(err)
		}
		remaining := withoutPresentedCombatVisuals(events, launches, impacts)
		server.emitCombatEvents(first, battle, append(append(launches, impacts...), remaining...))
		visuals := recordedSkillVisuals(t, first.conn.(*recordingConn).Bytes())
		counts := map[int32]int{}
		for _, visual := range visuals {
			counts[visual.EffectId]++
			if visual.EffectId == test.secondary && visual.Time != 11000 {
				t.Fatalf("skill %d secondary loop duration=%d, want 11000", test.skill, visual.Time)
			}
		}
		if len(visuals) != 3 || counts[test.secondary] != 1 || counts[launches[0].EffectID] != 1 || counts[impacts[0].EffectID] != 1 {
			t.Fatalf("skill %d lost secondary visual or duplicated projectile/impact: %v", test.skill, visuals)
		}
	}
}

func TestConditionalCommonEffectDoesNotInheritStatusDuration(t *testing.T) {
	loadOnlineTablesForTest(t)
	plan, err := loadTestSkillLogic(t).Plan(500009, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, _, first, _ := newSharedPartyBattleForTest(t)
	battle := first.session.battle
	cast := monsterSkillCast{plan: withMonsterSkillPresentation(plan), source: MonsterCombatUnit(battle.monsters[0].id), primary: battle.runtime.Player()}
	_, impacts, travel := battle.monsterProjectilePresentation(cast)
	if travel != time.Second || len(impacts) != 0 {
		t.Fatalf("conditional effect played before its chance check: travel=%v visuals=%v", travel, impacts)
	}
	events, err := battle.executeSkillPlan(cast.plan, cast.source, cast.primary, func() float64 { return 0 })
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Type == CombatEventVisual && event.EffectID == 3014 {
			count++
			if event.DelayMS != 0 {
				t.Fatalf("common effect Time=%d, want 0 despite the 14-second status", event.DelayMS)
			}
		}
	}
	if count != 1 {
		t.Fatalf("conditional effect count=%d, want one", count)
	}
	_, _, second, _ := newSharedPartyBattleForTest(t)
	battle = second.session.battle
	cast.source = MonsterCombatUnit(battle.monsters[0].id)
	cast.primary = battle.runtime.Player()
	events, err = battle.executeSkillPlan(cast.plan, cast.source, cast.primary, func() float64 { return 1 })
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == CombatEventVisual && event.EffectID == 3014 {
			t.Fatal("failed chance check played an effect")
		}
	}
}
