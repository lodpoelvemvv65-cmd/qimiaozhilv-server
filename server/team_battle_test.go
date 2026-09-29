package main

import (
	"reflect"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func TestPartyBossSkillAndSettlementAreBroadcastToBothClients(t *testing.T) {
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = loadTestSkillLogic(t)
	oldDelay := battleVictoryDelay
	battleVictoryDelay = 0
	t.Cleanup(func() {
		skillLogicCatalog = oldCatalog
		battleVictoryDelay = oldDelay
	})

	server, party, first, second := newSharedPartyBattleForTest(t)
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
		seenPlay, seenDamage, seenHP, seenDead, seenVictory := false, false, false, false, false
		for _, frame := range decodeRecordedFrames(t, recipient.conn.(*recordingConn).Bytes()) {
			switch frame.opcode {
			case protocol.OpM2C_PlaySkill:
				var message protocol.M2C_PlaySkill
				if err := proto.Unmarshal(frame.body, &message); err != nil {
					t.Fatal(err)
				}
				if message.UnitId == second.session.playerID && message.SkillId == 100001 && message.TargetId == monster.id {
					seenPlay = true
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
		if !seenPlay || !seenDamage || !seenHP || !seenDead || !seenVictory {
			t.Fatalf("recipient %d packets play/damage/hp/dead/victory = %v/%v/%v/%v/%v",
				recipient.session.playerID, seenPlay, seenDamage, seenHP, seenDead, seenVictory)
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
	fifth.session.battleMu.Lock()
	_, cast := server.castSkillLocked(fifth, 100001, &protocol.M2C_UseMainUISkill{})
	fifth.session.battleMu.Unlock()
	if !cast {
		t.Fatal("fifth member skill was rejected")
	}
	for _, recipient := range members {
		plays, damages, hpSyncs := 0, 0, 0
		for _, frame := range decodeRecordedFrames(t, recipient.conn.(*recordingConn).Bytes()) {
			switch frame.opcode {
			case protocol.OpM2C_PlaySkill:
				var message protocol.M2C_PlaySkill
				if err := proto.Unmarshal(frame.body, &message); err != nil {
					t.Fatal(err)
				}
				if message.UnitId == fifth.session.playerID && message.SkillId == 100001 && message.TargetId == monster.id {
					plays++
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
		if plays != 1 || damages == 0 || hpSyncs == 0 {
			t.Fatalf("recipient %d fifth skill packets play/damage/hp=%d/%d/%d",
				recipient.session.playerID, plays, damages, hpSyncs)
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
		ss.playerID, ss.name = id, name
		ss.jobID, ss.skinID, ss.level = 1, 1, 20
		ss.mapID, ss.familyID, ss.teamID = 10004, 77, 1
		ss.hp, ss.mp = ss.playerMaxHp(), ss.playerMaxMp()
		return ss, &channel{id: id, conn: &recordingConn{}, session: ss}
	}
	firstSession, first := makeMember(101, "first")
	secondSession, second := makeMember(202, "second")
	server := &Server{conns: map[int64]*channel{first.id: first, second.id: second}}
	teamMu.Lock()
	teams[1] = &teamState{LeaderId: firstSession.playerID, Members: []int64{firstSession.playerID, secondSession.playerID}}
	teamMu.Unlock()

	monster := &monsterUnit{id: 9001, monsterID: 1001, hp: 5000, maxHP: 5000, alive: true}
	firstSession.battleMu.Lock()
	started, _ := server.finishStartDedicatedBossBattle(first, 1001, []*monsterUnit{monster}, -1, 1)
	firstSession.battleMu.Unlock()
	if !started || firstSession.battle == nil || secondSession.battle == nil {
		t.Fatalf("party family boss did not enroll both members: started=%v first=%p second=%p",
			started, firstSession.battle, secondSession.battle)
	}
	if firstSession.battle.party == nil || firstSession.battle.party != secondSession.battle.party ||
		firstSession.battle.monsters[0] != secondSession.battle.monsters[0] {
		t.Fatal("party family boss does not share one authoritative encounter")
	}

	for _, recipient := range []*channel{first, second} {
		frames := decodeRecordedFrames(t, recipient.conn.(*recordingConn).Bytes())
		teamIndex, bossIndex := -1, -1
		resourceBeforeBoss := map[int64]map[int32]bool{}
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
			case protocol.OpM2C_SendFamilyBossInfo:
				if bossIndex < 0 {
					bossIndex = index
				}
			case protocol.OpM2C_SyncUnitAttribute:
				if bossIndex >= 0 {
					continue
				}
				var message protocol.M2C_SyncUnitAttribute
				if err := proto.Unmarshal(frame.body, &message); err != nil {
					t.Fatal(err)
				}
				if message.NumericType >= 1001 && message.NumericType <= 1004 {
					if resourceBeforeBoss[message.UnitId] == nil {
						resourceBeforeBoss[message.UnitId] = make(map[int32]bool)
					}
					resourceBeforeBoss[message.UnitId][message.NumericType] = true
				}
			}
		}
		if teamIndex < 0 || bossIndex < 0 || teamIndex >= bossIndex {
			t.Fatalf("recipient %d packet order team=%d boss=%d", recipient.session.playerID, teamIndex, bossIndex)
		}
		for _, id := range []int64{101, 202} {
			for numeric := int32(1001); numeric <= 1004; numeric++ {
				if !resourceBeforeBoss[id][numeric] {
					t.Fatalf("recipient %d missing player %d numeric %d before boss presentation",
						recipient.session.playerID, id, numeric)
				}
			}
		}
	}
}

func newSharedPartyBattleForTest(t *testing.T) (*Server, *partyBattle, *channel, *channel) {
	t.Helper()
	firstSession := newSession()
	firstSession.playerID = 101
	firstSession.hp, firstSession.mp = 1000, 500
	secondSession := newSession()
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

func TestPartyDetachRemovesMemberAndTargetEffects(t *testing.T) {
	server, party, first, second := newSharedPartyBattleForTest(t)
	_, err := first.session.battle.runtime.ApplyEffect(CombatEffectContext{
		Source: PlayerCombatUnit(202), Target: PlayerCombatUnit(101),
	}, EffectSpec{Key: "leaving-buff", Kind: CombatEffectAttribute,
		Attribute: CombatAttributePhysicalAttack, Percent: 50, Duration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}

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
}

func TestPartyFirstAidUsesOnlineCostTargetValueAndProtocol(t *testing.T) {
	loadOnlineTablesForTest(t)
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = loadTestSkillLogic(t)
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })

	server, _, first, second := newSharedPartyBattleForTest(t)
	first.session.skills = map[int32]int32{310301: 1}
	first.session.skillOrder = []int32{310301}
	first.session.battle.spiAtk = 1000
	second.session.battle.playerHP = 1000
	second.session.battle.playerMaxHP = 5000
	second.session.hp = 1000
	first.session.battle.selectedAllyID = second.session.playerID

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
	if second.session.battle.playerHP != 2000 {
		t.Fatalf("first-aid target HP = %d, want SkillLogicConfig's 100%% spiritual attack = 2000", second.session.battle.playerHP)
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
				if message.UnitId == first.session.playerID && message.SkillId == 310301 &&
					message.TargetId == second.session.playerID && message.MpCost == 25 {
					plays++
				}
			case protocol.OpM2C_BattleSkillRet:
				message := &protocol.M2C_BattleSkillRet{}
				if err := proto.Unmarshal(frame.body, message); err != nil {
					t.Fatal(err)
				}
				if message.UnitId == second.session.playerID && message.ChangeHpValue == 1000 {
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
				if message.Id == 310301 && message.SkillCD == 7200 {
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
