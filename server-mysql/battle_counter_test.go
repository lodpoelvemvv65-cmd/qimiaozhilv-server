package main

import (
	"fmt"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"mhqserver/protocol"
)

func newCounterPartyTest(t *testing.T, job int32) (*Server, *queuedCombatPhases, *channel, *channel) {
	t.Helper()
	loadOnlineTablesForTest(t)
	previous := skillLogicCatalog
	skillLogicCatalog = loadTestSkillLogic(t)
	t.Cleanup(func() { skillLogicCatalog = previous })
	server, _, officer, holder := newSharedPartyBattleForTest(t)
	for _, member := range []*channel{officer, holder} {
		ss, battle := member.session, member.session.battle
		ss.level, ss.jobID = 1, 1
		battle.playerHP, battle.playerMaxHP = 100000, 100000
		battle.phyAtk, battle.spiAtk = 100, 200
	}
	officer.session.battle.phyAtk = 9000 // Must never supply a teammate's attack.
	holder.session.jobID = job
	monster := officer.session.battle.monsters[0]
	monster.hp, monster.maxHP = 1_000_000, 1_000_000
	monster.phyDef, monster.spiDef = 10, 20
	// Cancellation tests can remove the counter's target. Keep another enemy
	// alive so they cannot start an unrelated delayed victory finalizer.
	monsters := append(officer.session.battle.monsters, &monsterUnit{
		id: 9002, monsterID: 1001, hp: 1_000_000, maxHP: 1_000_000, alive: true,
	})
	officer.session.battle.monsters, holder.session.battle.monsters = monsters, monsters
	phases := installQueuedCombatScheduler(server)
	return server, phases, officer, holder
}

func applyCounterPartyBuff(t *testing.T, officer *channel, skill, level int32) {
	t.Helper()
	plan, err := skillLogicCatalog.Plan(skill, level)
	if err != nil {
		t.Fatal(err)
	}
	battle := officer.session.battle
	if _, err := battle.executeSkillPlan(plan, battle.runtime.Player(), battle.runtime.Player(), func() float64 { return .5 }); err != nil {
		t.Fatal(err)
	}
}

func hitCounterHolder(t *testing.T, officer, holder *channel, amount int32) []CombatEvent {
	t.Helper()
	battle := officer.session.battle
	attack := SkillPlan{Effects: []SkillEffect{{
		Kind: SkillEffectDamage, Trigger: SkillTrigger{Scope: "skill", Event: 1},
		Target: SkillTargetPlan{Kind: SkillTargetSingle, Side: SkillTargetCastTarget},
		Damage: &SkillDamagePlan{Type: SkillDamageTrue, Self: SkillFormula{Stat: SkillStatPhysicalAttack, Percent: 100}},
	}}}
	monster := battle.monsters[0]
	monster.phyAtk = amount
	events, err := battle.executeSkillPlan(attack, MonsterCombatUnit(monster.id), PlayerCombatUnit(holder.session.playerID), func() float64 { return .5 })
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func onlyCounterRequest(t *testing.T, events []CombatEvent) CombatEvent {
	t.Helper()
	var found []CombatEvent
	for _, event := range events {
		if event.Type == CombatEventCounter {
			found = append(found, event)
		}
	}
	if len(found) != 1 || found[0].Amount != 0 {
		t.Fatalf("want one zero-damage counter request, got %+v", found)
	}
	return found[0]
}

func TestCounterUsesHoldersConfiguredBasicAttack(t *testing.T) {
	for _, tc := range []struct {
		job, skill int32
		wantDamage int32
	}{
		{1, 100001, 128}, // ceil((100 - 10) * 142%)
		{3, 200001, 128},
		{5, 300001, 255}, // Tactical Lv2 grants 8% spiritual attack: 216.
		{7, 400001, 379},
	} {
		for _, incoming := range []int32{1, 80} {
			t.Run(fmt.Sprintf("job-%d-incoming-%d", tc.job, incoming), func(t *testing.T) {
				server, _, officer, holder := newCounterPartyTest(t, tc.job)
				applyCounterPartyBuff(t, officer, 110604, 2)
				monster := officer.session.battle.monsters[0]
				before := monster.hp
				events := hitCounterHolder(t, officer, holder, incoming)
				request := onlyCounterRequest(t, events)
				if monster.hp != before || request.Source != PlayerCombatUnit(holder.session.playerID) || request.Target != MonsterCombatUnit(monster.id) {
					t.Fatal("receiving damage reflected HP or lost holder/attacker identity")
				}
				cast, ok := server.counterAttackCast(officer, officer.session.battle, request)
				if !ok || cast.actor != holder || cast.battle != holder.session.battle || cast.skillID != tc.skill {
					t.Fatalf("counter chose wrong basic attack: %+v ok=%v", cast, ok)
				}
				ctx := newSkillExecutionContext(cast.battle, func() float64 { return .5 })
				ctx.counterAttack, ctx.targetIntn = true, func(int) int { return 0 }
				if _, err := cast.battle.executeSkillPlanWithContext(cast.plan, cast.source, cast.primary, ctx); err != nil {
					t.Fatal(err)
				}
				if got := before - monster.hp; got != tc.wantDamage {
					t.Fatalf("counter damage=%d want=%d with incoming=%d", got, tc.wantDamage, incoming)
				}
			})
		}
	}
}

func TestCounterBasicAttackBroadcastPhasesAndCooldowns(t *testing.T) {
	server, phases, officer, holder := newCounterPartyTest(t, 3)
	applyCounterPartyBuff(t, officer, 110604, 2)
	battle := officer.session.battle
	monster := battle.monsters[0]
	before := monster.hp
	ready := time.Now().Add(time.Hour)
	ss := holder.session
	ss.battleActionReadyAt, ss.autoBattleNextCastAt = ready, ready
	ss.skillCooldowns[200001] = ready
	mp := ss.battle.playerMP
	events := hitCounterHolder(t, officer, holder, 1)
	onlyCounterRequest(t, events)
	server.emitCombatEvents(officer, battle, events)
	// 反击波次是 immediate：不排 windup，第一个阶段就是 0 延迟的弹道阶段。
	if phases.len() != 1 || phases.items[0].delay != 0 || monster.hp != before ||
		battle.runtime.state().inFlight != 1 {
		t.Fatal("counter must queue one immediate attack wave without early damage")
	}
	for _, recipient := range []*channel{officer, holder} {
		casts, touches, hurts := 0, 0, 0
		for _, frame := range decodeRecordedFrames(t, recipient.conn.(*recordingConn).Bytes()) {
			switch frame.opcode {
			case protocol.OpM2C_PlaySkill, protocol.OpM2C_MonsterPlaySkill:
				// 反击不发任何施法包：20075 会被客户端用 SkillConfig 的 Name 在
				// 单位头上弹技能名（反击的普攻就成了「普通攻击」横幅），20076 虽
				// 然只播 PlayAnimation_Attack，但反击要求连动作都不要。
				casts++
			case protocol.OpM2C_BattleSkillRet:
				var hurt protocol.M2C_BattleSkillRet
				if err := proto.Unmarshal(frame.body, &hurt); err != nil {
					t.Fatal(err)
				}
				if hurt.UnitId == monster.id {
					hurts++
				}
			case protocol.OpM2C_BattleTouchState:
				touches++
			}
		}
		// 20081 也永远不发：客户端处理器是空 async 方法，抓包归档里 0 个样本。
		// 伤害还没落地之前，任何一条指向怪的 20078 都说明结算提前了。
		if casts != 0 || touches != 0 || hurts != 0 {
			t.Fatalf("recipient %d got casts=%d touches=%d monster-hurts=%d",
				recipient.id, casts, touches, hurts)
		}
	}
	phases.runNext(t, 0) // 弹道阶段：普攻的弹道和特效当帧发出，飞行时间用配置值
	if phases.len() != 1 {
		t.Fatal("counter did not schedule its projectile impact")
	}
	flight := phases.items[0].delay
	if monster.hp != before || flight <= 0 {
		t.Fatalf("counter must launch now and resolve after the flight, flight=%s", flight)
	}
	for _, recipient := range []*channel{officer, holder} {
		visuals := recordedSkillVisuals(t, recipient.conn.(*recordingConn).Bytes())
		launch := false
		for _, visual := range visuals {
			if visual.UnitId != ss.playerID || visual.TargetId != monster.id || configuredEffectType(visual.EffectId) != 1 {
				continue
			}
			// Time 是客户端子弹 DOMove 的飞行时长；归零等于让子弹瞬移，
			// 客户端就完全看不到普攻的弹道了。
			if visual.Time != int32(flight/time.Millisecond) {
				t.Fatalf("counter projectile %d flight=%dms, want the basic attack's %s",
					visual.EffectId, visual.Time, flight)
			}
			launch = true
		}
		if !launch {
			t.Fatal("counter's normal attack projectile was not broadcast")
		}
	}
	phases.runNext(t, flight) // 命中阶段：飞行结束后特效与伤害一起结算
	if before-monster.hp <= 1 || battle.runtime.state().inFlight != 0 || phases.len() != 0 {
		t.Fatal("counter failed to resolve its normal attack at the projectile impact")
	}
	if ss.battle.playerMP != mp || ss.battleActionReadyAt != ready || ss.autoBattleNextCastAt != ready || ss.skillCooldowns[200001] != ready {
		t.Fatal("counter consumed MP or changed a normal-action cooldown")
	}
}

func TestCounterCannotStartAnotherCounter(t *testing.T) {
	server, phases, officer, holder := newCounterPartyTest(t, 3)
	applyCounterPartyBuff(t, officer, 110604, 2)
	battle := officer.session.battle
	monster := MonsterCombatUnit(battle.monsters[0].id)
	// Exercise both persistent and configured on-hurt counter paths on the
	// attacker. Neither may answer a counter with yet another counter.
	applyTestEffect(t, battle.runtime, monster, monster, EffectSpec{Key: "enemy-counter", Kind: CombatEffectCounter})
	plan, err := skillLogicCatalog.Plan(110604, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := battle.executeSkillPlan(plan, monster, monster, func() float64 { return .5 }); err != nil {
		t.Fatal(err)
	}
	events := hitCounterHolder(t, officer, holder, 1)
	server.emitCombatEvents(officer, battle, events)
	for steps := 0; phases.len() > 0; steps++ {
		if steps >= 4 {
			t.Fatal("counter attack recursed into another attack wave")
		}
		phases.runNext(t, phases.items[0].delay)
	}
	for _, frame := range decodeRecordedFrames(t, officer.conn.(*recordingConn).Bytes()) {
		if frame.opcode != protocol.OpM2C_MonsterPlaySkill {
			continue
		}
		var cast protocol.M2C_MonsterPlaySkill
		if err := proto.Unmarshal(frame.body, &cast); err != nil {
			t.Fatal(err)
		}
		if cast.UnitId != holder.session.playerID || cast.SkillId != 200001 {
			t.Fatalf("counter generated a chained attack: %v", &cast)
		}
	}
}

func TestCounterCancelsWhenParticipantCannotAttack(t *testing.T) {
	for _, reason := range []string{"dead-before-start", "stunned-before-start", "target-dead-before-launch", "dead-before-impact", "left-before-impact"} {
		t.Run(reason, func(t *testing.T) {
			server, phases, officer, holder := newCounterPartyTest(t, 3)
			applyCounterPartyBuff(t, officer, 110604, 2)
			battle, owner := officer.session.battle, holder.session.battle
			monster := battle.monsters[0]
			before := monster.hp
			events := hitCounterHolder(t, officer, holder, 1)
			onlyCounterRequest(t, events)
			switch reason {
			case "dead-before-start":
				owner.playerHP = 0
			case "stunned-before-start":
				applyTestEffect(t, owner.runtime, owner.runtime.Player(), owner.runtime.Player(), EffectSpec{Key: "stun", Kind: CombatEffectStatus, Status: CombatStatusStunned})
			}
			server.emitCombatEvents(officer, battle, events)
			if reason == "dead-before-start" || reason == "stunned-before-start" {
				if phases.len() != 0 {
					t.Fatal("invalid holder still started a counter attack")
				}
				return
			}
			if reason == "target-dead-before-launch" {
				monster.alive = false
			} else {
				// 反击是 immediate 波次：0 延迟的弹道阶段之后就是命中阶段。
				phases.runNext(t, 0)
				if reason == "dead-before-impact" {
					owner.playerHP = 0
				} else {
					holder.session.battle = nil
				}
			}
			drainCombatPhases(t, phases)
			if monster.hp != before || battle.runtime.state().inFlight != 0 {
				t.Fatal("cancelled counter changed HP or leaked an in-flight attack")
			}
			server.closed.Store(true)
		})
	}
}

func TestTacticalCounterStopsRequestingAtExpiry(t *testing.T) {
	_, _, officer, holder := newCounterPartyTest(t, 3)
	clock := &fakeCombatClock{now: time.Now()}
	officer.session.battle.runtime.clock = clock
	holder.session.battle.runtime.clock = clock
	applyCounterPartyBuff(t, officer, 110604, 2)
	onlyCounterRequest(t, hitCounterHolder(t, officer, holder, 1))
	clock.Add(14 * time.Second)
	officer.session.battle.runtime.Tick()
	if events := hitCounterHolder(t, officer, holder, 1); containsCombatEvent(events, CombatEventCounter) {
		t.Fatalf("expired tactical buff still requested a counter: %+v", events)
	}
}
