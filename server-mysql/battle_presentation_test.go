package main

import (
	"reflect"
	"sort"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func TestEveryDamagingPlayerSkillHasClientPresentation(t *testing.T) {
	loadOnlineTablesForTest(t)
	catalog := loadTestSkillLogic(t)
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = catalog
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })

	ids := make([]int, 0, len(catalog.Skills))
	for id := range catalog.Skills {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	for _, rawID := range ids {
		id := int32(rawID)
		if id >= 500000 {
			continue
		}
		skill := catalog.Skills[id]
		for level := int32(1); level <= skill.MaxLevel; level++ {
			plan, err := catalog.Plan(id, level)
			if err != nil {
				t.Fatalf("Plan(%d,%d): %v", id, level, err)
			}
			if !skillPlanDealsDamage(plan) {
				continue
			}
			plan = withConfiguredSkillPresentation(plan)
			if !skillPlanContainsVisual(plan.Effects) {
				t.Errorf("damaging skill %d level %d has no client visual", id, level)
			}
			for _, roll := range []float64{0, 0.999999} {
				monster := &monsterUnit{
					id: 200, hp: 1_000_000_000, maxHP: 1_000_000_000,
					phyAtk: 1000, spiAtk: 1000, phyDef: 100, spiDef: 100, alive: true,
				}
				battle := &battleState{
					playerHP: 1_000_000_000, playerMaxHP: 1_000_000_000,
					phyAtk: 1000, spiAtk: 1000, phyDef: 100, spiDef: 100,
					monsters: []*monsterUnit{monster},
				}
				battle.runtime = NewCombatRuntime(battle, 100, nil)
				source, target := battle.runtime.Player(), MonsterCombatUnit(monster.id)
				if id >= 500000 {
					source, target = target, source
				}
				events, executeErr := battle.executeSkillPlan(plan, source, target, func() float64 { return roll })
				if executeErr != nil {
					t.Fatalf("execute skill %d level %d roll %.6f: %v", id, level, roll, executeErr)
				}
				if combatEventsContain(events, CombatEventDamage) && !combatEventsContain(events, CombatEventVisual) {
					t.Errorf("skill %d level %d roll %.6f dealt damage without a visual", id, level, roll)
				}
			}
		}
	}
}

func TestMonsterSkillsPreserveProjectilesAndIncludeConfiguredModifierVisuals(t *testing.T) {
	loadOnlineTablesForTest(t)
	catalog := loadTestSkillLogic(t)

	count, visualCount := 0, 0
	for id, skill := range catalog.Skills {
		if id < 500000 {
			continue
		}
		for level := int32(1); level <= skill.MaxLevel; level++ {
			plan, err := catalog.Plan(id, level)
			if err != nil {
				t.Fatalf("Plan(%d,%d): %v", id, level, err)
			}
			if !skillPlanCanCast(plan) {
				continue
			}
			count++
			modelPlan := withMonsterSkillPresentation(plan)
			before, after := skillPlanVisualIDs(plan.Effects), skillPlanVisualIDs(modelPlan.Effects)
			if len(after) != 0 {
				visualCount++
			}
			allowed := make(map[int32]bool)
			for _, effectID := range before {
				allowed[effectID] = true
			}
			for _, modifier := range skill.Modifiers {
				if modifier.ActiveAt(level) {
					allowed[modifier.EffectID] = true
				}
			}
			for _, effectID := range after {
				if !allowed[effectID] {
					t.Errorf("monster skill %d invented unconfigured visual %d", id, effectID)
				}
			}
			for _, effectID := range before {
				if !skillEffectsContainVisualID(modelPlan.Effects, effectID) {
					t.Errorf("monster skill %d lost original visual %d", id, effectID)
				}
			}
			if twice := skillPlanVisualIDs(withMonsterSkillPresentation(modelPlan).Effects); !reflect.DeepEqual(twice, after) {
				t.Errorf("monster skill %d duplicated visuals on rebuild: once=%v twice=%v", id, after, twice)
			}
			for _, roll := range []float64{0, 0.999999} {
				beforePlayer, beforeMonster, err := monsterPlanHealthOutcome(plan, roll)
				if err != nil {
					t.Fatalf("execute original monster skill %d level %d: %v", id, level, err)
				}
				afterPlayer, afterMonster, err := monsterPlanHealthOutcome(modelPlan, roll)
				if err != nil {
					t.Fatalf("execute model monster skill %d level %d: %v", id, level, err)
				}
				if beforePlayer != afterPlayer || beforeMonster != afterMonster {
					t.Errorf("monster skill %d level %d changed health outcome at roll %.6f: before=%d/%d after=%d/%d",
						id, level, roll, beforePlayer, beforeMonster, afterPlayer, afterMonster)
				}
			}
		}
	}
	if count != 42 {
		t.Fatalf("castable monster skill count = %d, want 42", count)
	}
	if visualCount != 32 {
		t.Fatalf("monster skills with online visuals = %d, want 32 including modifier-only casts", visualCount)
	}
}

func monsterPlanHealthOutcome(plan SkillPlan, roll float64) (int32, int32, error) {
	monster := &monsterUnit{
		id: 200, hp: 1_000_000_000, maxHP: 1_000_000_000,
		phyAtk: 1000, spiAtk: 1000, phyDef: 100, spiDef: 100, alive: true,
	}
	battle := &battleState{
		playerHP: 1_000_000_000, playerMaxHP: 1_000_000_000,
		phyAtk: 1000, spiAtk: 1000, phyDef: 100, spiDef: 100,
		monsters: []*monsterUnit{monster},
	}
	battle.runtime = NewCombatRuntime(battle, 100, nil)
	source, primary := MonsterCombatUnit(monster.id), battle.runtime.Player()
	if plan.TeamType == 2 {
		primary = source
	}
	_, err := battle.executeSkillPlan(plan, source, primary, func() float64 { return roll })
	return battle.playerHP, monster.hp, err
}

func skillPlanVisualIDs(effects []SkillEffect) []int32 {
	var ids []int32
	for _, effect := range effects {
		if (effect.Kind == SkillEffectProjectile || effect.Kind == SkillEffectVisual) && effect.EffectID > 0 {
			ids = append(ids, effect.EffectID)
		}
		for _, nested := range [][]SkillEffect{effect.Success, effect.Failure, effect.Children} {
			ids = append(ids, skillPlanVisualIDs(nested)...)
		}
	}
	return ids
}

func combatEventsContain(events []CombatEvent, eventType CombatEventType) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func TestMissingMonsterVisualUsesModelAttackAndHurt(t *testing.T) {
	loadOnlineTablesForTest(t)
	catalog := loadTestSkillLogic(t)
	for _, skillID := range []int32{500039, 500040, 500041} {
		plan, err := catalog.Plan(skillID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if skillPlanContainsVisual(plan.Effects) {
			t.Fatalf("fixture skill %d unexpectedly already has a visual", skillID)
		}
		plan = withMonsterSkillPresentation(plan)
		if skillPlanContainsVisual(plan.Effects) {
			t.Errorf("skill %d invented a visual instead of using model Attack/Hurt", skillID)
		}
	}
}

func TestMonsterPresentationUsesCapturedStagedAttackOrder(t *testing.T) {
	loadOnlineTablesForTest(t)
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = loadTestSkillLogic(t)
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })

	monster := &monsterUnit{id: 200, monsterID: 1, hp: 1000, maxHP: 1000, phyAtk: 100, alive: true}
	ss := newSession()
	ss.playerID = 100
	battle := &battleState{
		owner: ss, playerHP: 1000, playerMaxHP: 1000, phyDef: 10,
		monsters: []*monsterUnit{monster},
	}
	battle.runtime = NewCombatRuntime(battle, ss.playerID, nil)
	ss.battle = battle
	conn := &recordingConn{}
	ch := &channel{id: 1, conn: conn, session: ss}

	server, phases := newQueuedCombatServer()
	server.monstersAttackAt(ch, battle, time.Unix(1_800_000_000, 0))
	assertRecordedOpcodeSequence(t, conn, []uint16{protocol.OpM2C_PlaySkill})
	play := &protocol.M2C_PlaySkill{}
	if err := proto.Unmarshal(decodeRecordedFrames(t, conn.Bytes())[0].body, play); err != nil {
		t.Fatal(err)
	}
	if play.UnitId != monster.id || play.SkillId != 500001 || play.TargetId != 0 {
		t.Fatalf("monster attack start = %+v", play)
	}

	phases.runNext(t, time.Second)
	frames := decodeRecordedFrames(t, conn.Bytes())
	assertRecordedOpcodeSequence(t, conn, []uint16{
		protocol.OpM2C_PlaySkill,
		protocol.OpM2C_PlaySkillEffect,
	})
	launch := &protocol.M2C_PlaySkillEffect{}
	if err := proto.Unmarshal(frames[1].body, launch); err != nil {
		t.Fatal(err)
	}
	if launch.UnitId != monster.id || launch.TargetId != ss.playerID || launch.Time != 1000 || launch.EffectId != 3001 {
		t.Fatalf("monster projectile launch = %+v", launch)
	}
	if battle.playerHP != 1000 {
		t.Fatalf("monster damage settled before projectile impact: hp=%d", battle.playerHP)
	}

	phases.runNext(t, time.Second)
	frames = decodeRecordedFrames(t, conn.Bytes())
	assertRecordedOpcodeSequence(t, conn, []uint16{
		protocol.OpM2C_PlaySkill,
		protocol.OpM2C_PlaySkillEffect,
		protocol.OpM2C_PlaySkillEffect,
		protocol.OpM2C_BattleSkillRet,
		protocol.OpM2C_SyncUnitAttribute,
	})
	impact := &protocol.M2C_PlaySkillEffect{}
	if err := proto.Unmarshal(frames[2].body, impact); err != nil {
		t.Fatal(err)
	}
	if impact.UnitId != monster.id || impact.TargetId != ss.playerID || impact.Time != 0 || impact.EffectId != 3002 {
		t.Fatalf("monster projectile impact = %+v", impact)
	}
	hurt := &protocol.M2C_BattleSkillRet{}
	if err := proto.Unmarshal(frames[3].body, hurt); err != nil {
		t.Fatal(err)
	}
	if hurt.UnitId != ss.playerID || hurt.ChangeHpValue >= 0 {
		t.Fatalf("player hurt/floating damage = %+v", hurt)
	}
	hp := &protocol.M2C_SyncUnitAttribute{}
	if err := proto.Unmarshal(frames[4].body, hp); err != nil {
		t.Fatal(err)
	}
	if hp.UnitId != ss.playerID || hp.NumericType != 1001 || hp.Value != float32(battle.playerHP) || battle.playerHP >= 1000 {
		t.Fatalf("player authoritative HP = %+v battleHP=%d", hp, battle.playerHP)
	}
}

func TestLaunchedMonsterProjectileResolvesAfterCasterDies(t *testing.T) {
	loadOnlineTablesForTest(t)
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = loadTestSkillLogic(t)
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })

	monster := &monsterUnit{id: 200, monsterID: 1, hp: 1000, maxHP: 1000, phyAtk: 100, alive: true}
	ss := newSession()
	ss.playerID = 100
	battle := &battleState{
		owner: ss, playerHP: 1000, playerMaxHP: 1000, phyDef: 10,
		monsters: []*monsterUnit{monster},
	}
	battle.runtime = NewCombatRuntime(battle, ss.playerID, nil)
	ss.battle = battle
	conn := &recordingConn{}
	ch := &channel{id: 1, conn: conn, session: ss}
	server, phases := newQueuedCombatServer()

	server.monstersAttackAt(ch, battle, time.Unix(1_800_000_000, 0))
	phases.runNext(t, time.Second)
	monster.hp = 0
	monster.alive = false
	battle.monsters = append(battle.monsters, &monsterUnit{id: 201, hp: 1000, maxHP: 1000, alive: true})
	if battle.runtime.CanCast(MonsterCombatUnit(monster.id)) {
		t.Fatal("dead projectile caster can still cast")
	}

	phases.runNext(t, time.Second)
	assertRecordedOpcodeSequence(t, conn, []uint16{
		protocol.OpM2C_PlaySkill,
		protocol.OpM2C_PlaySkillEffect,
		protocol.OpM2C_PlaySkillEffect,
		protocol.OpM2C_BattleSkillRet,
		protocol.OpM2C_SyncUnitAttribute,
	})
	if battle.playerHP >= battle.playerMaxHP {
		t.Fatalf("dead caster projectile did not damage player: HP=%d/%d", battle.playerHP, battle.playerMaxHP)
	}
}

func TestMonsterDamageFloatingTextPreservesCriticalFlag(t *testing.T) {
	ss := newSession()
	ss.playerID = 100
	battle := &battleState{owner: ss, playerHP: 75, playerMaxHP: 100}
	conn := &recordingConn{}
	ch := &channel{conn: conn, session: ss}
	event := CombatEvent{
		Type: CombatEventDamage, Source: MonsterCombatUnit(200), Target: PlayerCombatUnit(ss.playerID),
		Amount: 25, HPAfter: 75, IsCrit: true,
	}
	(&Server{}).emitCombatEvents(ch, battle, []CombatEvent{event})
	frames := decodeRecordedFrames(t, conn.Bytes())
	assertRecordedOpcodeSequence(t, conn, []uint16{protocol.OpM2C_BattleSkillRet, protocol.OpM2C_SyncUnitAttribute})
	message := &protocol.M2C_BattleSkillRet{}
	if err := proto.Unmarshal(frames[0].body, message); err != nil {
		t.Fatal(err)
	}
	if message.UnitId != ss.playerID || message.ChangeHpValue != -25 || !message.IsCrit {
		t.Fatalf("critical hurt/floating damage = %+v", message)
	}
}

func TestActivityMonsterDamageEmitsPlayerHurtAndAuthoritativeHP(t *testing.T) {
	loadOnlineTablesForTest(t)
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = loadTestSkillLogic(t)
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })

	tests := []struct {
		name      string
		monsterID int32
		wantSkill int32
	}{
		{name: "star soul first tier", monsterID: 71001, wantSkill: 500002},
		{name: "star soul D tier", monsterID: 71004, wantSkill: 500002},
		{name: "death road", monsterID: 90001, wantSkill: 500003},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ss := newSession()
			ss.playerID = int64(8000 + index)
			monsters := ss.buildMonsterUnitsFromRoster([]int32{test.monsterID}, []int{1})
			if len(monsters) != 1 || monsters[0].monsterID != test.monsterID {
				t.Fatalf("online monster %d was not built: %+v", test.monsterID, monsters)
			}
			battle := &battleState{
				owner: ss, monsters: monsters,
				playerHP: 2_000_000_000, playerMaxHP: 2_000_000_000,
				effectMeta: make(map[string]battleEffectMetadata), modifierHooks: make(map[string]*activeSkillModifier),
			}
			battle.runtime = NewCombatRuntime(battle, ss.playerID, nil)
			ss.battle = battle

			skillID := selectMonsterSkillID(test.monsterID, func(int) int { return 0 })
			if skillID != test.wantSkill {
				t.Fatalf("monster %d first online skill=%d, want %d", test.monsterID, skillID, test.wantSkill)
			}
			plan, err := skillLogicCatalog.Plan(skillID, 1)
			if err != nil {
				t.Fatal(err)
			}
			events, err := battle.executeSkillPlan(withMonsterSkillPresentation(plan),
				MonsterCombatUnit(monsters[0].id), battle.runtime.Player(), func() float64 { return 0 })
			if err != nil {
				t.Fatal(err)
			}
			var damage *CombatEvent
			for eventIndex := range events {
				if events[eventIndex].Type == CombatEventDamage && events[eventIndex].Target == battle.runtime.Player() {
					damage = &events[eventIndex]
					break
				}
			}
			if damage == nil || damage.Amount <= 0 || damage.HPAfter >= battle.playerMaxHP {
				t.Fatalf("monster %d skill %d damage event=%+v events=%+v", test.monsterID, skillID, damage, events)
			}

			conn := &recordingConn{}
			ch := &channel{id: int64(index + 1), conn: conn, session: ss}
			server := &Server{}
			server.emitCombatEventsDeferredHP(ch, battle, events)
			server.syncCombatEventHP(ch, battle, events)
			frames := decodeRecordedFrames(t, conn.Bytes())
			hurtIndex, hpIndex := -1, -1
			for frameIndex, frame := range frames {
				switch frame.opcode {
				case protocol.OpM2C_BattleSkillRet:
					var message protocol.M2C_BattleSkillRet
					if err := proto.Unmarshal(frame.body, &message); err != nil {
						t.Fatal(err)
					}
					if message.UnitId == ss.playerID {
						hurtIndex = frameIndex
						if message.ChangeHpValue != -damage.Amount {
							t.Fatalf("monster %d hurt=%+v damage=%+v", test.monsterID, &message, damage)
						}
					}
				case protocol.OpM2C_SyncUnitAttribute:
					var message protocol.M2C_SyncUnitAttribute
					if err := proto.Unmarshal(frame.body, &message); err != nil {
						t.Fatal(err)
					}
					if message.UnitId == ss.playerID && message.NumericType == 1001 {
						hpIndex = frameIndex
						if message.Value != float32(damage.HPAfter) {
							t.Fatalf("monster %d HP=%+v damage=%+v", test.monsterID, &message, damage)
						}
					}
				}
			}
			if hurtIndex < 0 || hpIndex <= hurtIndex {
				t.Fatalf("monster %d hurt/HP packet order=%d/%d opcodes=%v", test.monsterID, hurtIndex, hpIndex, recordedOpcodes(t, conn.Bytes()))
			}
		})
	}
}

func TestPendingMonsterAttackStopsWhenBattleEnds(t *testing.T) {
	loadOnlineTablesForTest(t)
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = loadTestSkillLogic(t)
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })

	monster := &monsterUnit{id: 200, monsterID: 1, hp: 1000, maxHP: 1000, phyAtk: 100, alive: true}
	ss := newSession()
	ss.playerID = 100
	battle := &battleState{owner: ss, playerHP: 1000, playerMaxHP: 1000, monsters: []*monsterUnit{monster}}
	battle.runtime = NewCombatRuntime(battle, ss.playerID, nil)
	ss.battle = battle
	conn := &recordingConn{}
	ch := &channel{conn: conn, session: ss}
	server, phases := newQueuedCombatServer()
	server.monstersAttackAt(ch, battle, time.Unix(1_800_000_000, 0))
	battle.ended = true
	phases.runNext(t, time.Second)
	assertRecordedOpcodeSequence(t, conn, []uint16{protocol.OpM2C_PlaySkill})
}

func TestPartyMonsterCounterattackSharesSixSecondWindow(t *testing.T) {
	loadOnlineTablesForTest(t)
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = loadTestSkillLogic(t)
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })
	server, _, first, second := newSharedPartyBattleForTest(t)
	phases := installQueuedCombatScheduler(server)
	for _, member := range []*channel{first, second} {
		member.session.battle.playerHP = 1_000_000
		member.session.battle.playerMaxHP = 1_000_000
		member.session.battle.phyDef = 0
	}
	now := time.Unix(1_800_000_000, 0)
	server.monstersAttackAt(first, first.session.battle, now)
	server.monstersAttackAt(second, second.session.battle, now)
	server.monstersAttackAt(second, second.session.battle, now.Add(5999*time.Millisecond))
	assertMonsterWaves(t, first, 1)
	assertMonsterWaves(t, second, 1)

	server.monstersAttackAt(second, second.session.battle, now.Add(6*time.Second))
	assertMonsterWaves(t, first, 2)
	assertMonsterWaves(t, second, 2)
	if phases.len() != 2 {
		t.Fatalf("scheduled monster waves = %d, want 2", phases.len())
	}
}

func TestPartyMonsterDamageBroadcastsHurtAndAuthoritativeHP(t *testing.T) {
	loadOnlineTablesForTest(t)
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = loadTestSkillLogic(t)
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })

	server, _, first, second := newSharedPartyBattleForTest(t)
	monster := first.session.battle.monsters[0]
	monster.monsterID = 1
	monster.phyAtk = 100
	first.session.battle.playerHP = 1_000_000
	first.session.battle.playerMaxHP = 1_000_000
	first.session.battle.phyDef = 0
	phases := installQueuedCombatScheduler(server)

	server.monstersAttackAt(first, first.session.battle, time.Unix(1_800_000_000, 0))
	phases.runNext(t, time.Second)
	phases.runNext(t, time.Second)

	for _, member := range []*channel{first, second} {
		if member.session.battle.playerHP >= member.session.battle.playerMaxHP {
			t.Fatalf("monster did not damage party member %d: hp=%d/%d",
				member.session.playerID, member.session.battle.playerHP, member.session.battle.playerMaxHP)
		}
	}
	for _, recipient := range []*channel{first, second} {
		frames := decodeRecordedFrames(t, recipient.conn.(*recordingConn).Bytes())
		assertRecordedOpcodeSequence(t, recipient.conn.(*recordingConn), []uint16{
			protocol.OpM2C_PlaySkill,
			protocol.OpM2C_PlaySkillEffect,
			protocol.OpM2C_PlaySkillEffect,
			protocol.OpM2C_PlaySkillEffect,
			protocol.OpM2C_PlaySkillEffect,
			protocol.OpM2C_BattleSkillRet,
			protocol.OpM2C_BattleSkillRet,
			protocol.OpM2C_SyncUnitAttribute,
			protocol.OpM2C_SyncUnitAttribute,
		})
		var play protocol.M2C_PlaySkill
		if err := proto.Unmarshal(frames[0].body, &play); err != nil {
			t.Fatal(err)
		}
		if play.UnitId != monster.id || play.SkillId != 500001 || play.TargetId != 0 {
			t.Fatalf("recipient %d monster cast=%+v", recipient.session.playerID, &play)
		}
		hurtByPlayer := make(map[int64]*protocol.M2C_BattleSkillRet)
		hpByPlayer := make(map[int64]*protocol.M2C_SyncUnitAttribute)
		for _, frame := range frames {
			switch frame.opcode {
			case protocol.OpM2C_BattleSkillRet:
				var hurt protocol.M2C_BattleSkillRet
				if err := proto.Unmarshal(frame.body, &hurt); err != nil {
					t.Fatal(err)
				}
				hurtByPlayer[hurt.UnitId] = &hurt
			case protocol.OpM2C_SyncUnitAttribute:
				var hp protocol.M2C_SyncUnitAttribute
				if err := proto.Unmarshal(frame.body, &hp); err != nil {
					t.Fatal(err)
				}
				if hp.NumericType == 1001 {
					hpByPlayer[hp.UnitId] = &hp
				}
			}
		}
		for _, member := range []*channel{first, second} {
			playerID := member.session.playerID
			hurt, hurtOK := hurtByPlayer[playerID]
			hp, hpOK := hpByPlayer[playerID]
			if !hurtOK || hurt.ChangeHpValue >= 0 {
				t.Fatalf("recipient %d member %d hurt=%+v present=%t",
					recipient.session.playerID, playerID, hurt, hurtOK)
			}
			if !hpOK || hp.Value != float32(member.session.battle.playerHP) ||
				hp.ActorId != recipient.session.playerID {
				t.Fatalf("recipient %d member %d HP=%+v present=%t battleHP=%d",
					recipient.session.playerID, playerID, hp, hpOK, member.session.battle.playerHP)
			}
		}
	}
}

func TestPartyMonsterSingleTargetRotatesAcrossLivingMembers(t *testing.T) {
	loadOnlineTablesForTest(t)
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = loadTestSkillLogic(t)
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })
	server, party, first, second := newSharedPartyBattleForTest(t)
	secondMonster := &monsterUnit{id: 9002, monsterID: 1, hp: 5000, maxHP: 5000, phyAtk: 100, alive: true}
	monsters := append(first.session.battle.monsters[:0:0], first.session.battle.monsters[0], secondMonster)
	first.session.battle.monsters = monsters
	second.session.battle.monsters = monsters
	first.session.battle.playerHP, second.session.battle.playerHP = 100000, 100000
	first.session.battle.playerMaxHP, second.session.battle.playerMaxHP = 100000, 100000
	first.session.battle.phyDef, second.session.battle.phyDef = 0, 0
	party.monsterReadyAt = time.Unix(1_800_000_000, 0)
	phases := installQueuedCombatScheduler(server)
	server.monstersAttackAt(first, first.session.battle, party.monsterReadyAt)
	if phases.len() != 1 {
		t.Fatalf("scheduled monster wave count=%d, want 1", phases.len())
	}
	phases.runNext(t, time.Second)
	phases.runNext(t, time.Second)
	if first.session.battle.playerHP >= first.session.battle.playerMaxHP ||
		second.session.battle.playerHP >= second.session.battle.playerMaxHP {
		t.Fatalf("both living party members should be targeted: hp=%d/%d", first.session.battle.playerHP, second.session.battle.playerHP)
	}
	for _, recipient := range []*channel{first, second} {
		targets := make(map[int64]bool)
		for _, frame := range decodeRecordedFrames(t, recipient.conn.(*recordingConn).Bytes()) {
			if frame.opcode != protocol.OpM2C_BattleSkillRet {
				continue
			}
			var ret protocol.M2C_BattleSkillRet
			if err := proto.Unmarshal(frame.body, &ret); err != nil {
				t.Fatal(err)
			}
			if ret.ChangeHpValue < 0 {
				targets[ret.UnitId] = true
			}
		}
		if len(targets) != 2 || !targets[first.session.playerID] || !targets[second.session.playerID] {
			t.Fatalf("recipient %d monster targets=%v, want both party members", recipient.session.playerID, targets)
		}
	}
}

func assertMonsterWaves(t *testing.T, ch *channel, want int) {
	t.Helper()
	got := 0
	for _, frame := range decodeRecordedFrames(t, ch.conn.(*recordingConn).Bytes()) {
		if frame.opcode == protocol.OpM2C_PlaySkill {
			var message protocol.M2C_PlaySkill
			if err := proto.Unmarshal(frame.body, &message); err != nil {
				t.Fatal(err)
			}
			if message.UnitId < 9000 {
				continue
			}
			got++
		}
	}
	if got != want {
		t.Fatalf("monster attack waves = %d, want %d", got, want)
	}
}

type queuedCombatPhase struct {
	delay time.Duration
	run   func()
}

type queuedCombatPhases struct {
	items []queuedCombatPhase
}

func newQueuedCombatServer() (*Server, *queuedCombatPhases) {
	server := &Server{}
	return server, installQueuedCombatScheduler(server)
}

func installQueuedCombatScheduler(server *Server) *queuedCombatPhases {
	phases := &queuedCombatPhases{}
	server.combatScheduler = func(delay time.Duration, run func()) {
		phases.items = append(phases.items, queuedCombatPhase{delay: delay, run: run})
	}
	return phases
}

func (phases *queuedCombatPhases) len() int {
	return len(phases.items)
}

func (phases *queuedCombatPhases) runNext(t *testing.T, wantDelay time.Duration) {
	t.Helper()
	if len(phases.items) == 0 {
		t.Fatal("no scheduled combat phase")
	}
	phase := phases.items[0]
	phases.items = phases.items[1:]
	if phase.delay != wantDelay {
		t.Fatalf("combat phase delay = %s, want %s", phase.delay, wantDelay)
	}
	phase.run()
}

func assertRecordedOpcodeSequence(t *testing.T, conn *recordingConn, want []uint16) {
	t.Helper()
	got := recordedOpcodes(t, conn.Bytes())
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("opcode sequence = %v, want %v", got, want)
	}
}
