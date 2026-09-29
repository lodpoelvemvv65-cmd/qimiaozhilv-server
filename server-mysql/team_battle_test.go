package main

import (
	"reflect"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func TestPartyBossSkillAndSettlementAreBroadcastToBothClients(t *testing.T) {
	loadOnlineTablesForTest(t)
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = loadTestSkillLogic(t)
	oldDelay := battleVictoryDelay
	battleVictoryDelay = 0
	t.Cleanup(func() {
		skillLogicCatalog = oldCatalog
		battleVictoryDelay = oldDelay
	})

	server, party, first, second := newSharedPartyBattleForTest(t)
	phases := installQueuedCombatScheduler(server)
	for _, member := range []*channel{first, second} {
		member.session.killCount = make(map[int32]int32)
		member.session.tasks = make(map[int32]int32)
	}
	monster := party.members[first.session.playerID].monsters[0]
	monster.hp, monster.maxHP, monster.alive = 1, 1, true
	for _, member := range []*channel{first, second} {
		member.session.skills = map[int32]int32{100001: 1}
		member.session.skillOrder = []int32{100001}
	}

	second.session.battleMu.Lock()
	_, cast := server.castSkillLocked(second, 100001, &protocol.M2C_UseMainUISkill{})
	second.session.battleMu.Unlock()
	if !cast {
		t.Fatal("second party member's boss skill was rejected")
	}
	for phases.len() > 0 {
		phases.runNext(t, phases.items[0].delay)
	}
	if monster.hp != 0 || monster.alive {
		t.Fatalf("authoritative boss state after kill = hp=%d alive=%v", monster.hp, monster.alive)
	}

	deadline := time.Now().Add(time.Second)
	for (first.session.battle != nil || second.session.battle != nil) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if first.session.battle != nil || second.session.battle != nil || !party.settled {
		t.Fatalf("party settlement incomplete: first=%p second=%p settled=%v", first.session.battle, second.session.battle, party.settled)
	}

	for _, recipient := range []*channel{first, second} {
		seenPlay, seenEffect, seenDamage, seenHP, seenDead, seenVictory := false, false, false, false, false, false
		for _, frame := range decodeRecordedFrames(t, recipient.conn.(*recordingConn).Bytes()) {
			switch frame.opcode {
			case protocol.OpM2C_PlaySkill:
				var message protocol.M2C_PlaySkill
				if err := proto.Unmarshal(frame.body, &message); err != nil {
					t.Fatal(err)
				}
				if message.UnitId == second.session.playerID && message.SkillId == 100001 {
					assertPlaySkillWireFields(t, frame.body)
					seenPlay = true
				}
			case protocol.OpM2C_PlaySkillEffect:
				var message protocol.M2C_PlaySkillEffect
				if err := proto.Unmarshal(frame.body, &message); err != nil {
					t.Fatal(err)
				}
				if message.UnitId == second.session.playerID && message.TargetId == monster.id && message.EffectId > 0 {
					seenEffect = true
				}
			case protocol.OpM2C_BattleSkillRet:
				var message protocol.M2C_BattleSkillRet
				if err := proto.Unmarshal(frame.body, &message); err != nil {
					t.Fatal(err)
				}
				if message.UnitId == monster.id && message.ChangeHpValue < 0 {
					seenDamage = true
				}
			case protocol.OpM2C_SyncUnitAttribute:
				var message protocol.M2C_SyncUnitAttribute
				if err := proto.Unmarshal(frame.body, &message); err != nil {
					t.Fatal(err)
				}
				if message.UnitId == monster.id && message.NumericType == 1001 && message.Value == 0 {
					seenHP = true
				}
			case protocol.OpM2C_MainstoryMonsterDead:
				var message protocol.M2C_MainstoryMonsterDead
				if err := proto.Unmarshal(frame.body, &message); err != nil {
					t.Fatal(err)
				}
				seenDead = message.UnitId == monster.id
			case protocol.OpM2C_BattleVictory:
				seenVictory = true
			}
		}
		if !seenPlay || !seenEffect || !seenDamage || !seenHP || !seenDead || !seenVictory {
			t.Fatalf("recipient %d packets play/effect/damage/hp/dead/victory = %v/%v/%v/%v/%v/%v",
				recipient.session.playerID, seenPlay, seenEffect, seenDamage, seenHP, seenDead, seenVictory)
		}
	}
}

func TestPartyFamilyBossVictorySettlesEveryMember(t *testing.T) {
	oldTables := tables
	tables = &datatables{
		familyBossConfig: map[int64]map[string]interface{}{1: {
			"MonsterId": 50001, "Contribute": 100, "PersonalContribute": 150,
			"BonusDropChance": 0,
		}},
		monsterBase: map[int64]map[string]interface{}{50001: {"Hp": 1000}},
	}
	t.Cleanup(func() { tables = oldTables })
	server, party, first, second := newSharedPartyBattleForTest(t)
	const familyID int64 = 22334455
	for _, member := range []*channel{first, second} {
		member.session.familyID = familyID
		member.session.bag = make(map[int32]*bagItem)
		member.session.killCount = make(map[int32]int32)
		member.session.tasks = make(map[int32]int32)
		battle := party.members[member.session.playerID]
		battle.mapID = -1
		battle.ended = true
		battle.monsters[0].hp = 0
		battle.monsters[0].alive = false
	}
	familyBossStates.mu.Lock()
	delete(familyBossStates.m, familyID)
	familyBossStates.mu.Unlock()
	t.Cleanup(func() {
		familyBossStates.mu.Lock()
		delete(familyBossStates.m, familyID)
		familyBossStates.mu.Unlock()
	})

	server.finishPartyVictory(party)
	for _, member := range []*channel{first, second} {
		if member.session.familyContribute != 100 || member.session.personalContribute != 150 {
			t.Fatalf("member %d contributions family=%d personal=%d",
				member.session.playerID, member.session.familyContribute, member.session.personalContribute)
		}
	}
	state := familyBossState(familyID, 1)
	if state.Hp != 0 || !state.HasReward {
		t.Fatalf("party victory state = %+v", state)
	}
	for _, member := range []*channel{first, second} {
		if !server.canClaimFamilyBossReward(familyID, 1, member.session.playerID) {
			t.Fatalf("member %d did not receive an independent reward claim", member.session.playerID)
		}
	}
}

func TestFiveMemberPartyBattleBroadcastsCombatAndSettlesOnce(t *testing.T) {
	loadOnlineTablesForTest(t)
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = loadTestSkillLogic(t)
	oldDelay := battleVictoryDelay
	battleVictoryDelay = 0
	t.Cleanup(func() {
		skillLogicCatalog = oldCatalog
		battleVictoryDelay = oldDelay
	})

	teamMu.Lock()
	oldTeams, oldSeq := teams, teamSeq
	teams, teamSeq = make(map[int64]*teamState), 1
	teamMu.Unlock()
	t.Cleanup(func() {
		teamMu.Lock()
		teams, teamSeq = oldTeams, oldSeq
		teamMu.Unlock()
	})

	server := &Server{conns: make(map[int64]*channel)}
	members := make([]*channel, 0, maxTeamMembers)
	ids := make([]int64, 0, maxTeamMembers)
	for index := 0; index < maxTeamMembers; index++ {
		ss := newSession()
		ss.state = sessInGame
		ss.playerID = int64(100 + index)
		ss.name = "party-member"
		ss.jobID, ss.level = 1, 20
		ss.mapID, ss.teamID = 10004, 1
		ss.energy = 100
		ss.tasks = make(map[int32]int32)
		ss.killCount = make(map[int32]int32)
		ss.signin = &signinState{}
		ss.hp, ss.mp = ss.playerMaxHp(), ss.playerMaxMp()
		ss.skills = map[int32]int32{100001: 1}
		ss.skillOrder = []int32{100001}
		ch := &channel{id: ss.playerID, conn: &recordingConn{}, session: ss}
		server.conns[ch.id] = ch
		members = append(members, ch)
		ids = append(ids, ss.playerID)
	}
	teamMu.Lock()
	teams[1] = &teamState{LeaderId: ids[0], Members: append([]int64(nil), ids...)}
	teamMu.Unlock()

	monster := &monsterUnit{id: 90001, monsterID: 1001, hp: 1_000_000, maxHP: 1_000_000,
		phyAtk: 1, spiAtk: 1, phyDef: 1, spiDef: 1, alive: true}
	leader := members[0]
	leader.session.battleMu.Lock()
	started, _ := server.finishStartBattleWithPresentation(leader, 1001, []*monsterUnit{monster}, 0,
		battlePresentation{kind: presentationMainStory})
	leader.session.battleMu.Unlock()
	if !started {
		t.Fatal("five-member battle did not start")
	}
	party := leader.session.battle.party
	if party == nil || len(party.memberIDs) != maxTeamMembers || len(party.members) != maxTeamMembers {
		t.Fatalf("party members = ids:%v map:%d, want %d", party.memberIDs, len(party.members), maxTeamMembers)
	}
	for _, member := range members {
		if member.session.battle == nil || member.session.battle.party != party || member.session.battle.monsters[0] != monster {
			t.Fatalf("member %d is not in the shared encounter", member.session.playerID)
		}
	}

	// The fifth member casts once. Every recipient must see the same skill,
	// damage event and authoritative monster HP update.
	fifth := members[len(members)-1]
	phases := installQueuedCombatScheduler(server)
	fifth.session.battleMu.Lock()
	_, cast := server.castSkillLockedWithLogAt(
		fifth, 100001, &protocol.M2C_UseMainUISkill{}, true, party.monsterReadyAt,
	)
	fifth.session.battleMu.Unlock()
	if !cast {
		t.Fatal("fifth member skill was rejected")
	}
	for phases.len() > 0 {
		phases.runNext(t, phases.items[0].delay)
	}
	for _, recipient := range members {
		plays, effects, damages, hpSyncs := 0, 0, 0, 0
		for _, frame := range decodeRecordedFrames(t, recipient.conn.(*recordingConn).Bytes()) {
			switch frame.opcode {
			case protocol.OpM2C_PlaySkill:
				var message protocol.M2C_PlaySkill
				if err := proto.Unmarshal(frame.body, &message); err != nil {
					t.Fatal(err)
				}
				if message.UnitId == fifth.session.playerID && message.SkillId == 100001 {
					assertPlaySkillWireFields(t, frame.body)
					plays++
				}
			case protocol.OpM2C_PlaySkillEffect:
				var message protocol.M2C_PlaySkillEffect
				if err := proto.Unmarshal(frame.body, &message); err != nil {
					t.Fatal(err)
				}
				if message.UnitId == fifth.session.playerID && message.TargetId == monster.id && message.EffectId > 0 {
					effects++
				}
			case protocol.OpM2C_BattleSkillRet:
				var message protocol.M2C_BattleSkillRet
				if err := proto.Unmarshal(frame.body, &message); err != nil {
					t.Fatal(err)
				}
				if message.UnitId == monster.id && message.ChangeHpValue < 0 {
					damages++
				}
			case protocol.OpM2C_SyncUnitAttribute:
				var message protocol.M2C_SyncUnitAttribute
				if err := proto.Unmarshal(frame.body, &message); err != nil {
					t.Fatal(err)
				}
				if message.UnitId == monster.id && message.NumericType == 1001 {
					hpSyncs++
				}
			}
		}
		if plays != 1 || effects == 0 || damages == 0 || hpSyncs == 0 {
			t.Fatalf("recipient %d fifth skill packets play/effect/damage/hp=%d/%d/%d/%d",
				recipient.session.playerID, plays, effects, damages, hpSyncs)
		}
	}

	monster.hp, monster.alive = 0, false
	party.mu.Lock()
	party.settled = true
	for _, battle := range party.members {
		battle.ended = true
	}
	party.mu.Unlock()
	server.finishPartyVictory(party)
	for _, member := range members {
		if member.session.battle != nil {
			t.Fatalf("member %d battle was not cleared", member.session.playerID)
		}
		victories := 0
		for _, frame := range decodeRecordedFrames(t, member.conn.(*recordingConn).Bytes()) {
			if frame.opcode == protocol.OpM2C_BattleVictory {
				victories++
			}
		}
		if victories != 1 {
			t.Fatalf("member %d victory packets=%d, want 1", member.session.playerID, victories)
		}
	}
}

func TestPartyFamilyBossPreparesEveryClientBeforePresentation(t *testing.T) {
	loadOnlineTablesForTest(t)
	teamMu.Lock()
	oldTeams, oldSeq := teams, teamSeq
	teams = make(map[int64]*teamState)
	teamSeq = 1
	teamMu.Unlock()
	t.Cleanup(func() {
		teamMu.Lock()
		teams, teamSeq = oldTeams, oldSeq
		teamMu.Unlock()
	})

	makeMember := func(id int64, name string) (*session, *channel) {
		ss := newSession()
		ss.state = sessInGame
		ss.playerID, ss.name = id, name
		ss.jobID, ss.skinID, ss.level = 1, 1, 20
		ss.mapID, ss.familyID, ss.teamID = 10004, 77, 1
		ss.hp, ss.mp = ss.playerMaxHp(), ss.playerMaxMp()
		return ss, &channel{id: id, conn: &recordingConn{}, session: ss}
	}
	firstSession, first := makeMember(101, "first")
	secondSession, second := makeMember(202, "second")
	secondSession.hp, secondSession.mp = 31, 17
	server := &Server{conns: map[int64]*channel{first.id: first, second.id: second}}
	teamMu.Lock()
	teams[1] = &teamState{LeaderId: firstSession.playerID, Members: []int64{firstSession.playerID, secondSession.playerID}}
	teamMu.Unlock()

	monster := &monsterUnit{id: 9001, monsterID: 1001, hp: 5000, maxHP: 5000, alive: true}
	firstSession.battleMu.Lock()
	started, _ := server.finishStartBattleWithPresentation(first, 1001, []*monsterUnit{monster}, -1, battlePresentation{
		kind: presentationFamilyBoss, familyBossID: 1, deferFamilyBossForPlayerID: firstSession.playerID,
	})
	firstSession.battleMu.Unlock()
	if !started || firstSession.battle == nil || secondSession.battle == nil {
		t.Fatalf("party family boss did not enroll both members: started=%v first=%p second=%p",
			started, firstSession.battle, secondSession.battle)
	}
	if firstSession.battle.party == nil || firstSession.battle.party != secondSession.battle.party ||
		firstSession.battle.monsters[0] != secondSession.battle.monsters[0] {
		t.Fatal("party family boss does not share one authoritative encounter")
	}
	server.pushStartedFamilyBossPresentation(first)
	wantResources := map[int64]map[int32]float32{
		101: {
			1001: float32(firstSession.hp), 1003: float32(firstSession.mp),
		},
		202: {
			1001: 31, 1003: 17,
		},
	}
	for _, ss := range []*session{firstSession, secondSession} {
		wantResources[ss.playerID][1009] = float32(ss.playerPhyAtk())
		wantResources[ss.playerID][1010] = float32(ss.playerSpiAtk())
		wantResources[ss.playerID][1011] = float32(ss.playerPhyDef())
		wantResources[ss.playerID][1012] = float32(ss.playerSpiDef())
		wantResources[ss.playerID][1013] = float32(ss.playerExtraNumeric(1013))
		wantResources[ss.playerID][1015] = float32(ss.playerExtraNumeric(1015))
		wantResources[ss.playerID][1016] = float32(ss.playerExtraNumeric(1016))
	}
	wantBattleTypes := []int32{1001, 1003, 1009, 1010, 1011, 1012, 1013, 1015, 1016}

	for _, recipient := range []*channel{first, second} {
		frames := decodeRecordedFrames(t, recipient.conn.(*recordingConn).Bytes())
		teamIndex, bossIndex := -1, -1
		batchCount := map[int64]int{}
		attributesBeforeBoss := map[int64]map[int32]float32{}
		attributeTypesBeforeBoss := map[int64][]int32{}
		for index, frame := range frames {
			switch frame.opcode {
			case protocol.OpM2C_TeamMember:
				if teamIndex < 0 {
					teamIndex = index
					var message protocol.M2C_TeamMember
					if err := proto.Unmarshal(frame.body, &message); err != nil {
						t.Fatal(err)
					}
					if message.LeaderId != firstSession.playerID ||
						!reflect.DeepEqual(message.UnitIds, []int64{101, 202}) {
						t.Fatalf("team snapshot = %+v", &message)
					}
				}
			case protocol.OpM2C_SendFamilyBossInfo, protocol.OpM2C_ReSendFamilyBossInfo:
				if bossIndex < 0 {
					bossIndex = index
					wantOpcode := uint16(protocol.OpM2C_SendFamilyBossInfo)
					if recipient == second {
						wantOpcode = protocol.OpM2C_ReSendFamilyBossInfo
					}
					if frame.opcode != wantOpcode {
						t.Fatalf("recipient %d family boss opcode=%d, want %d",
							recipient.session.playerID, frame.opcode, wantOpcode)
					}
				}
			case protocol.OpM2C_SyncUnitAttributeList:
				if bossIndex >= 0 {
					continue
				}
				var message protocol.M2C_SyncUnitAttributeList
				if err := proto.Unmarshal(frame.body, &message); err != nil {
					t.Fatal(err)
				}
				if message.ActorId != recipient.session.playerID {
					t.Fatalf("recipient %d batch actor=%d", recipient.session.playerID, message.ActorId)
				}
				batchCount[message.UnitId]++
				if attributesBeforeBoss[message.UnitId] == nil {
					attributesBeforeBoss[message.UnitId] = make(map[int32]float32)
				}
				attributes := decodeAttributeMapList(t, frame.body, 2)
				for attributeIndex := range attributes {
					attribute := &attributes[attributeIndex]
					if _, duplicate := attributesBeforeBoss[message.UnitId][attribute.Key]; duplicate {
						t.Fatalf("recipient %d player %d duplicate numeric %d",
							recipient.session.playerID, message.UnitId, attribute.Key)
					}
					attributesBeforeBoss[message.UnitId][attribute.Key] = attribute.Value
					attributeTypesBeforeBoss[message.UnitId] = append(attributeTypesBeforeBoss[message.UnitId], attribute.Key)
				}
			}
		}
		if teamIndex < 0 || bossIndex < 0 || teamIndex >= bossIndex {
			t.Fatalf("recipient %d packet order team=%d boss=%d", recipient.session.playerID, teamIndex, bossIndex)
		}
		for _, id := range []int64{101, 202} {
			for numeric, want := range wantResources[id] {
				if got, ok := attributesBeforeBoss[id][numeric]; !ok || got != want {
					t.Fatalf("recipient %d player %d numeric %d=%v present=%v, want %v before boss presentation",
						recipient.session.playerID, id, numeric, got, ok, want)
				}
			}
			if batchCount[id] != 1 {
				t.Fatalf("recipient %d player %d batch count=%d, want 1",
					recipient.session.playerID, id, batchCount[id])
			}
			if !reflect.DeepEqual(attributeTypesBeforeBoss[id], wantBattleTypes) {
				t.Fatalf("recipient %d player %d battle numeric types=%v, want %v",
					recipient.session.playerID, id, attributeTypesBeforeBoss[id], wantBattleTypes)
			}
		}
	}
}

func newSharedPartyBattleForTest(t *testing.T) (*Server, *partyBattle, *channel, *channel) {
	t.Helper()
	firstSession := newSession()
	firstSession.state = sessInGame
	firstSession.playerID = 101
	firstSession.hp, firstSession.mp = 1000, 500
	secondSession := newSession()
	secondSession.state = sessInGame
	secondSession.playerID = 202
	secondSession.hp, secondSession.mp = 900, 400
	first := &channel{id: 1, conn: &recordingConn{}, session: firstSession}
	second := &channel{id: 2, conn: &recordingConn{}, session: secondSession}
	monster := &monsterUnit{id: 9001, monsterID: 1001, hp: 5000, maxHP: 5000, alive: true}
	firstBattle := &battleState{
		monsters: []*monsterUnit{monster}, playerHP: 1000, playerMaxHP: 1000,
		playerMP: 500, playerMaxMP: 500, owner: firstSession,
	}
	secondBattle := &battleState{
		monsters: []*monsterUnit{monster}, playerHP: 900, playerMaxHP: 900,
		playerMP: 400, playerMaxMP: 400, owner: secondSession,
	}
	firstBattle.runtime = NewCombatRuntime(firstBattle, firstSession.playerID, nil)
	secondBattle.runtime = NewCombatRuntime(secondBattle, secondSession.playerID, nil)
	party := &partyBattle{
		members: map[int64]*battleState{
			firstSession.playerID: firstBattle, secondSession.playerID: secondBattle,
		},
		memberIDs: []int64{firstSession.playerID, secondSession.playerID},
	}
	firstBattle.party, secondBattle.party = party, party
	firstSession.battle, secondSession.battle = firstBattle, secondBattle
	party.shareCombatState()
	server := &Server{conns: map[int64]*channel{first.id: first, second.id: second}}
	return server, party, first, second
}

func TestPartyCombatSharesEffectsButKeepsMemberIdentity(t *testing.T) {
	_, _, first, second := newSharedPartyBattleForTest(t)
	firstBattle, secondBattle := first.session.battle, second.session.battle
	if firstBattle.runtime.Player() != PlayerCombatUnit(101) || secondBattle.runtime.Player() != PlayerCombatUnit(202) {
		t.Fatalf("party runtime lost member identity: first=%+v second=%+v",
			firstBattle.runtime.Player(), secondBattle.runtime.Player())
	}
	_, err := firstBattle.runtime.ApplyEffect(CombatEffectContext{
		Source: PlayerCombatUnit(101), Target: PlayerCombatUnit(202),
	}, EffectSpec{Key: "team-shield", Kind: CombatEffectShield, Value: 250, Duration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot, ok := secondBattle.runtime.Unit(PlayerCombatUnit(202)); !ok || snapshot.Shield != 250 {
		t.Fatalf("teammate runtime did not observe shared shield: ok=%v snapshot=%+v", ok, snapshot)
	}
	_, err = firstBattle.runtime.ApplyEffect(CombatEffectContext{
		Source: PlayerCombatUnit(101), Target: PlayerCombatUnit(202),
	}, EffectSpec{Key: "team-stun", Kind: CombatEffectStatus, Status: CombatStatusStunned, Duration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if secondBattle.runtime.CanCast(PlayerCombatUnit(202)) {
		t.Fatal("teammate control status was not enforced by the target runtime")
	}
}

func TestPartySettlementPrefersDefeatWhenLastMonsterDiesWithAllMembersDown(t *testing.T) {
	server, party, first, second := newSharedPartyBattleForTest(t)
	// Keep the asynchronous defeat finalizer from finding live channels; this
	// test only exercises the settlement decision made under the combat locks.
	server.conns = nil
	first.session.battle.playerHP = 0
	second.session.battle.playerHP = 0
	monster := first.session.battle.monsters[0]
	monster.hp, monster.alive = 0, false

	first.session.battleMu.Lock()
	party.mu.Lock()
	settled := server.settlePartyCombatLocked(first, first.session.battle)
	party.mu.Unlock()
	first.session.battleMu.Unlock()

	if !settled || !party.settled {
		t.Fatalf("party settlement = settled=%v party.settled=%v, want defeat settlement", settled, party.settled)
	}
	for _, member := range party.members {
		if !member.ended || !member.playerDefeated || member.playerHP != 0 {
			t.Fatalf("member defeat state = ended=%v defeated=%v hp=%d, want ended=true defeated=true hp=0",
				member.ended, member.playerDefeated, member.playerHP)
		}
	}
}

func TestPartyHeadRebuildRefreshesTransAndCumulativeLevel(t *testing.T) {
	server, _, first, second := newSharedPartyBattleForTest(t)
	first.session.level, first.session.trans = 17007, 2
	second.session.level, second.session.trans = 13004, 1
	server.preparePartyBattleClients([]*channel{first, second}, first.session.playerID)
	for _, recipient := range []*channel{first, second} {
		frames := decodeRecordedFrames(t, recipient.conn.(*recordingConn).Bytes())
		teamIndex := -1
		for index, frame := range frames {
			if frame.opcode == protocol.OpM2C_TeamMember {
				teamIndex = index
				break
			}
		}
		if teamIndex < 0 {
			t.Fatalf("recipient %d missing team snapshot", recipient.session.playerID)
		}
		positions := make(map[int64][]int32)
		for _, frame := range frames[teamIndex+1:] {
			if frame.opcode != protocol.OpM2C_SyncUnitAttribute {
				continue
			}
			var attr protocol.M2C_SyncUnitAttribute
			if err := proto.Unmarshal(frame.body, &attr); err != nil {
				t.Fatal(err)
			}
			if attr.NumericType == 1029 || attr.NumericType == 1026 {
				positions[attr.UnitId] = append(positions[attr.UnitId], attr.NumericType)
			}
		}
		for _, owner := range []*channel{first, second} {
			want := []int32{1029, 1026, 1026}
			if !reflect.DeepEqual(positions[owner.session.playerID], want) {
				t.Fatalf("recipient %d owner %d level refresh=%v, want %v", recipient.session.playerID, owner.session.playerID, positions[owner.session.playerID], want)
			}
		}
	}
}

func TestPartyDetachRemovesMemberAndTargetEffects(t *testing.T) {
	server, party, first, second := newSharedPartyBattleForTest(t)
	events, err := first.session.battle.runtime.ApplyEffect(CombatEffectContext{
		Source: PlayerCombatUnit(202), Target: PlayerCombatUnit(101),
	}, EffectSpec{Key: "leaving-buff", Kind: CombatEffectAttribute,
		Attribute: CombatAttributePhysicalAttack, Percent: 50, Duration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if first.session.battle.effectMeta == nil {
		first.session.battle.effectMeta = make(map[string]battleEffectMetadata)
	}
	first.session.battle.effectMeta["leaving-buff"] = battleEffectMetadata{
		modifierID: 42, durationMS: 60000, iconID: "bufficon_atkAdd", isBuff: true,
	}
	server.emitCombatEvents(first, first.session.battle, events)

	first.session.battleMu.Lock()
	detached := server.detachCurrentBattleLocked(first, "test")
	first.session.battleMu.Unlock()
	if detached == nil || first.session.battle != nil {
		t.Fatalf("leaving member battle not cleared: detached=%p active=%p", detached, first.session.battle)
	}
	if _, exists := party.members[101]; exists || len(party.memberIDs) != 1 || party.memberIDs[0] != 202 {
		t.Fatalf("party membership not filtered: ids=%v members=%v", party.memberIDs, party.members)
	}
	if second.session.battle == nil || second.session.battle.ended || party.settled {
		t.Fatalf("remaining member fight was incorrectly ended: battle=%+v settled=%v", second.session.battle, party.settled)
	}
	if second.session.battle.runtime.HasEffect(PlayerCombatUnit(101), "leaving-buff") {
		t.Fatal("effects targeting detached member were retained")
	}
	if !hasRecordedReduceState(t, first.conn.(*recordingConn), first.session.playerID) {
		t.Fatal("leaving member did not receive HUD Reduce before party deletion")
	}
	if !hasRecordedReduceState(t, second.conn.(*recordingConn), first.session.playerID) {
		t.Fatal("remaining member did not observe HUD Reduce for the leaver")
	}
}

func TestPartyFirstAidUsesOnlineCostTargetValueAndProtocol(t *testing.T) {
	loadOnlineTablesForTest(t)
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = loadTestSkillLogic(t)
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })

	server, _, first, second := newSharedPartyBattleForTest(t)
	first.session.level, second.session.level = 100, 1000
	first.session.skills = map[int32]int32{310301: 1}
	first.session.skillOrder = []int32{310301}
	first.session.battle.spiAtk = 1000
	second.session.battle.playerHP = 1000
	second.session.battle.playerMaxHP = 5000
	second.session.hp = 1000
	first.session.battle.selectedAllyID = second.session.playerID
	expectedCooldown := clientCombatCooldownMS(7200)

	// Keep the monster's response turn from obscuring the resource assertions.
	monsterRef := MonsterCombatUnit(first.session.battle.monsters[0].id)
	if _, err := first.session.battle.runtime.ApplyEffect(CombatEffectContext{
		Source: PlayerCombatUnit(first.session.playerID), Target: monsterRef,
	}, EffectSpec{Key: "party-test-stun", Kind: CombatEffectStatus, Status: CombatStatusStunned, Duration: time.Minute}); err != nil {
		t.Fatal(err)
	}

	_, cast := server.castSkillLocked(first, 310301, &protocol.M2C_UseMainUISkill{})
	if !cast {
		t.Fatal("online first-aid skill was rejected in party combat")
	}
	if first.session.battle.playerMP != 475 || second.session.battle.playerMP != 400 {
		t.Fatalf("party MP after first aid = %d/%d, want caster-only 475/400",
			first.session.battle.playerMP, second.session.battle.playerMP)
	}
	if second.session.battle.playerHP != 2002 {
		t.Fatalf("first-aid target HP = %d, want 1000 + 1002 healing including level-100 natural primary growth", second.session.battle.playerHP)
	}
	if first.session.battle.playerHP != 1000 {
		t.Fatalf("first aid changed caster HP to %d", first.session.battle.playerHP)
	}

	assertRecipient := func(t *testing.T, recipient *channel, expectCooldown bool) {
		t.Helper()
		plays, heals, mpSyncs, cooldowns := 0, 0, 0, 0
		for _, frame := range decodeRecordedFrames(t, recipient.conn.(*recordingConn).Bytes()) {
			switch frame.opcode {
			case protocol.OpM2C_PlaySkill:
				message := &protocol.M2C_PlaySkill{}
				if err := proto.Unmarshal(frame.body, message); err != nil {
					t.Fatal(err)
				}
				if message.UnitId == first.session.playerID && message.SkillId == 310301 {
					assertPlaySkillWireFields(t, frame.body)
					plays++
				}
			case protocol.OpM2C_BattleSkillRet:
				message := &protocol.M2C_BattleSkillRet{}
				if err := proto.Unmarshal(frame.body, message); err != nil {
					t.Fatal(err)
				}
				if message.UnitId == second.session.playerID && message.ChangeHpValue == 1002 {
					heals++
				}
			case protocol.OpM2C_SyncUnitAttribute:
				message := &protocol.M2C_SyncUnitAttribute{}
				if err := proto.Unmarshal(frame.body, message); err != nil {
					t.Fatal(err)
				}
				if message.UnitId == first.session.playerID && message.NumericType == 1003 && message.Value == 475 {
					mpSyncs++
				}
			case protocol.OpM2C_StartCD:
				message := &protocol.M2C_StartCD{}
				if err := proto.Unmarshal(frame.body, message); err != nil {
					t.Fatal(err)
				}
				if message.Id == 310301 && message.SkillCD == expectedCooldown {
					cooldowns++
				}
			}
		}
		if plays != 1 || heals != 1 || mpSyncs != 1 {
			t.Fatalf("party first-aid packets play/heal/mp = %d/%d/%d, want 1/1/1", plays, heals, mpSyncs)
		}
		wantCooldowns := 0
		if expectCooldown {
			wantCooldowns = 1
		}
		if cooldowns != wantCooldowns {
			t.Fatalf("party first-aid cooldown packets = %d, want %d", cooldowns, wantCooldowns)
		}
	}
	assertRecipient(t, first, true)
	assertRecipient(t, second, false)
}

func TestPartyDevilTrainingBuffsAlliesButNeverMonster(t *testing.T) {
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = loadTestSkillLogic(t)
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })

	_, _, first, second := newSharedPartyBattleForTest(t)
	battle := first.session.battle
	plan, err := skillLogicCatalog.Plan(110301, 1)
	if err != nil {
		t.Fatal(err)
	}
	monster := MonsterCombatUnit(battle.monsters[0].id)
	if _, err = battle.executeSkillPlan(plan, PlayerCombatUnit(first.session.playerID),
		PlayerCombatUnit(second.session.playerID), func() float64 { return 0 }); err != nil {
		t.Fatal(err)
	}
	if got := battle.runtime.EffectiveAttribute(PlayerCombatUnit(first.session.playerID), CombatAttributeMaxHP); got != 1030 {
		t.Fatalf("caster max HP after level-1 devil training = %d, want SkillLogicConfig's 3%% = 1030", got)
	}
	if got := battle.runtime.EffectiveAttribute(PlayerCombatUnit(second.session.playerID), CombatAttributeMaxHP); got != 927 {
		t.Fatalf("teammate max HP after level-1 devil training = %d, want SkillLogicConfig's 3%% = 927", got)
	}
	if got := battle.runtime.EffectiveAttribute(monster, CombatAttributeMaxHP); got != 5000 {
		t.Fatalf("ally buff leaked to monster max HP: %d", got)
	}
}

func TestTeamBattleRejectsDefeatedMemberBeforeEnergyCharge(t *testing.T) {
	loadOnlineTablesForTest(t)
	teamMu.Lock()
	oldTeams, oldSeq := teams, teamSeq
	teams, teamSeq = make(map[int64]*teamState), 1
	teamMu.Unlock()
	t.Cleanup(func() {
		teamMu.Lock()
		teams, teamSeq = oldTeams, oldSeq
		teamMu.Unlock()
	})

	server := &Server{conns: make(map[int64]*channel)}
	members := make([]*channel, 0, 2)
	for index := 0; index < 2; index++ {
		ss := newSession()
		ss.state = sessInGame
		ss.playerID = int64(8800 + index)
		ss.name = "member"
		ss.jobID, ss.level, ss.mapID, ss.teamID = 1, 20, 10004, 1
		ss.energy = 100
		ss.hp, ss.mp = ss.playerMaxHp(), ss.playerMaxMp()
		ch := &channel{id: ss.playerID, conn: &recordingConn{}, session: ss}
		server.conns[ch.id] = ch
		members = append(members, ch)
	}
	teams[1] = &teamState{LeaderId: members[0].session.playerID, Members: []int64{
		members[0].session.playerID, members[1].session.playerID,
	}}
	members[1].session.hp = 0
	monster := &monsterUnit{id: 99001, monsterID: 10001, hp: 100, maxHP: 100, alive: true}

	members[0].session.battleMu.Lock()
	started, _ := server.finishStartBattleWithPresentation(members[0], 1001, []*monsterUnit{monster}, 0,
		battlePresentation{kind: presentationMainStory, copyID: 10010})
	members[0].session.battleMu.Unlock()
	if started {
		t.Fatal("team battle started with a defeated member")
	}
	if members[0].session.battle != nil || members[1].session.battle != nil {
		t.Fatalf("rejected battle installed state: first=%+v second=%+v",
			members[0].session.battle, members[1].session.battle)
	}
	if members[0].session.energy != 100 || members[1].session.energy != 100 {
		t.Fatalf("rejected battle charged energy: first=%d second=%d",
			members[0].session.energy, members[1].session.energy)
	}
}
