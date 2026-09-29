package main

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"mhqserver/protocol"
)

func drainCombatPhases(t *testing.T, phases *queuedCombatPhases) {
	t.Helper()
	for phases.len() > 0 {
		phases.runNext(t, phases.items[0].delay)
	}
}

func newRoundTestBattle(t *testing.T) (*Server, *queuedCombatPhases, *channel, time.Time) {
	t.Helper()
	loadOnlineTablesForTest(t)
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = loadTestSkillLogic(t)
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })
	ss := newSession()
	ss.playerID, ss.jobID, ss.level = 100, 1, 100
	ss.state = sessInGame
	ss.skills = map[int32]int32{100001: 1}
	ss.skillOrder = []int32{100001}
	battle := &battleState{owner: ss, playerHP: 100000, playerMaxHP: 100000, playerMP: 100000, playerMaxMP: 100000, phyAtk: 100,
		monsters: []*monsterUnit{{id: 200, monsterID: 1, hp: 100000, maxHP: 100000, phyAtk: 100, alive: true}}}
	battle.runtime = NewCombatRuntime(battle, ss.playerID, nil)
	ss.battle = battle
	start := time.Unix(1800000000, 0)
	initializeBattleOpeningCadence(ss, battle, start)
	ch := &channel{id: 100, session: ss, conn: &recordingConn{}}
	server, phases := newQueuedCombatServer()
	return server, phases, ch, start
}

func TestAutomaticRoundLaunchAndDamageAreBatched(t *testing.T) {
	server, phases, ch, start := newRoundTestBattle(t)
	ss, battle := ch.session, ch.session.battle
	ss.setAutoBattle(true)
	playerInterval := playerPublicActionInterval(ss)
	for round := 0; round < 5; round++ {
		due := start.Add(5*time.Second + time.Duration(round)*6*time.Second)
		actedAt := due.Add(37 * time.Millisecond)
		playerHP, monsterHP := battle.playerHP, battle.monsters[0].hp
		server.combatTickChannelAt(ch, actedAt)
		if phases.len() != 1 || battle.playerHP != playerHP || battle.monsters[0].hp != monsterHP {
			t.Fatalf("round %d did not queue one batch without early damage", round)
		}
		phases.runNext(t, time.Second)
		if phases.len() != 1 || battle.playerHP != playerHP || battle.monsters[0].hp != monsterHP {
			t.Fatal("damage applied at launch instead of shared impact")
		}
		visuals := recordedSkillVisuals(t, ch.conn.(*recordingConn).Bytes())
		if len(visuals) < 2 {
			t.Fatal("both projectiles were not emitted")
		}
		last := visuals[len(visuals)-2:]
		if last[0].UnitId == last[1].UnitId || last[0].Time != last[1].Time {
			t.Fatalf("projectiles do not share travel duration: %v", last)
		}
		drainCombatPhases(t, phases)
		if battle.playerHP >= playerHP || battle.monsters[0].hp >= monsterHP {
			t.Fatal("both sides did not take damage in the impact batch")
		}
		if !ss.battleActionReadyAt.Equal(due.Add(playerInterval)) || !battle.monsterReadyAt.Equal(due.Add(monsterActionInterval)) {
			t.Fatalf("automatic round drifted after scheduler latency: player=%s want=%s monster=%s want=%s",
				ss.battleActionReadyAt, due.Add(playerInterval), battle.monsterReadyAt, due.Add(monsterActionInterval))
		}
		battle.runtime.clearUnitEffects(battle.runtime.Player(), "test-reset", time.Now())
		battle.runtime.clearUnitEffects(MonsterCombatUnit(200), "test-reset", time.Now())
		battle.modifierHooks = nil
	}
}

func TestManualOpeningIdleAndModeSwitch(t *testing.T) {
	server, phases, ch, start := newRoundTestBattle(t)
	ss, battle := ch.session, ch.session.battle
	cast := func(at time.Time) bool {
		_, accepted := server.castSkillLockedWithLogAt(ch, 100001, &protocol.M2C_UseMainUISkill{}, true, at)
		return accepted
	}
	if cast(start) || !cast(start.Add(5*time.Second)) {
		t.Fatal("manual cast did not respect the five-second opening")
	}
	drainCombatPhases(t, phases)
	server.combatTickChannelAt(ch, start.Add(5*time.Second))
	drainCombatPhases(t, phases)
	if cast(start.Add(8*time.Second)) || !cast(start.Add(11*time.Second)) {
		t.Fatal("manual player did not keep its own public action interval")
	}
	drainCombatPhases(t, phases)
	ss.setAutoBattle(true)
	server.combatTickChannelAt(ch, start.Add(14*time.Second))
	drainCombatPhases(t, phases)
	if !ss.battleActionReadyAt.Equal(start.Add(11 * time.Second).Add(playerPublicActionInterval(ss))) {
		t.Fatal("switch to automatic bypassed manual cooldown")
	}
	response := server.onUseMainUISkill(ch, &protocol.C2M_UseMainUISkill{SlotId: 0}).(*protocol.M2C_UseMainUISkill)
	if response.Message == "" {
		t.Fatal("automatic mode accepted manual skill request")
	}
	server.combatTickChannelAt(ch, start.Add(17*time.Second))
	drainCombatPhases(t, phases)
	ss.setAutoBattle(false)
	if cast(start.Add(19*time.Second)) || !cast(start.Add(23*time.Second)) {
		t.Fatal("automatic to manual did not preserve remaining cooldown")
	}
	drainCombatPhases(t, phases)
	if !battle.monsterReadyAt.Equal(start.Add(23 * time.Second)) {
		t.Fatal("manual attack changed monster clock")
	}
}

func TestLastMonsterLaunchedAttackDelaysVictory(t *testing.T) {
	server, phases, ch, start := newRoundTestBattle(t)
	battle := ch.session.battle
	server.combatTickChannelAt(ch, start.Add(5*time.Second))
	phases.runNext(t, time.Second)
	monster := battle.monsters[0]
	_, err := battle.runtime.ApplyDamage(CombatDamageRequest{Source: battle.runtime.Player(), Target: MonsterCombatUnit(monster.id), Amount: monster.hp})
	if err != nil {
		t.Fatal(err)
	}
	if server.settleCombatLocked(ch, battle) || battle.ended {
		t.Fatal("victory cancelled the last monster's launched attack")
	}
	oldHP := battle.playerHP
	drainCombatPhases(t, phases)
	if battle.playerHP >= oldHP || !battle.ended {
		t.Fatal("last monster attack was not settled before victory")
	}
	server.closed.Store(true)
}

func TestImmediateHealPrecedesMonsterProjectileDamage(t *testing.T) {
	server, phases, ch, start := newRoundTestBattle(t)
	ss, battle := ch.session, ch.session.battle
	ss.jobID = 5
	ss.skills = map[int32]int32{310301: 1}
	ss.autoSkills = []int32{310301}
	battle.spiAtk, battle.playerHP = 10000, 50000
	ss.setAutoBattle(true)
	server.combatTickChannelAt(ch, start.Add(5*time.Second))
	healedHP := battle.playerHP
	if healedHP <= 50000 {
		t.Fatal("support heal was not immediate")
	}
	phases.runNext(t, time.Second)
	if battle.playerHP != healedHP {
		t.Fatal("monster damaged player before projectile impact")
	}
	drainCombatPhases(t, phases)
	if battle.playerHP >= healedHP {
		t.Fatal("monster projectile did not damage healed player")
	}
	for _, frame := range decodeRecordedFrames(t, ch.conn.(*recordingConn).Bytes()) {
		if frame.opcode == protocol.OpM2C_PlaySkillEffect {
			var effect protocol.M2C_PlaySkillEffect
			if err := proto.Unmarshal(frame.body, &effect); err != nil {
				t.Fatal(err)
			}
			if effect.UnitId == ss.playerID && configuredEffectType(effect.EffectId) == 1 {
				t.Fatal("heal incorrectly created a projectile")
			}
		}
	}
}

func TestFiveAutomaticMembersShareOneLaunchAndImpact(t *testing.T) {
	server, phases, first, start := newRoundTestBattle(t)
	server.conns = map[int64]*channel{first.id: first}
	party := &partyBattle{members: make(map[int64]*battleState), monsterReadyAt: start.Add(5 * time.Second)}
	monsters := first.session.battle.monsters
	for index := 0; index < 5; index++ {
		member := first
		if index != 0 {
			ss := newSession()
			ss.playerID, ss.jobID, ss.level = int64(100+index), 1, 100
			ss.state = sessInGame
			ss.skills = map[int32]int32{100001: 1}
			ss.battle = &battleState{owner: ss, monsters: monsters, playerHP: 100000, playerMaxHP: 100000,
				playerMP: 100000, playerMaxMP: 100000, phyAtk: 100}
			ss.battle.runtime = NewCombatRuntime(ss.battle, ss.playerID, nil)
			initializeBattleOpeningCadence(ss, ss.battle, start)
			member = &channel{id: ss.playerID, session: ss, conn: &recordingConn{}}
			server.conns[member.id] = member
		}
		member.session.autoBattle = true
		member.session.battle.party = party
		party.memberIDs = append(party.memberIDs, member.session.playerID)
		party.members[member.session.playerID] = member.session.battle
	}
	party.shareCombatState()
	partyPlayerIDs := make(map[int64]bool, len(party.memberIDs))
	for _, playerID := range party.memberIDs {
		partyPlayerIDs[playerID] = true
	}
	for _, member := range server.conns {
		server.combatTickChannelAt(member, start.Add(5*time.Second))
	}
	if phases.len() != 1 {
		t.Fatalf("five members queued %d attack batches, want 1", phases.len())
	}
	for _, recipient := range server.conns {
		casts := make(map[int64]int)
		playerCastPositions := make([]int, 0, 5)
		for position, frame := range decodeRecordedFrames(t, recipient.conn.(*recordingConn).Bytes()) {
			if frame.opcode == protocol.OpM2C_PlaySkill {
				assertPlaySkillWireFields(t, frame.body)
				var cast protocol.M2C_PlaySkill
				if err := proto.Unmarshal(frame.body, &cast); err != nil {
					t.Fatal(err)
				}
				casts[cast.UnitId]++
				if partyPlayerIDs[cast.UnitId] {
					playerCastPositions = append(playerCastPositions, position)
				}
			}
		}
		if len(casts) != 6 {
			t.Fatalf("recipient %d sees casts=%v, want five players and one monster", recipient.id, casts)
		}
		for _, count := range casts {
			if count != 1 {
				t.Fatal("duplicate action in one round")
			}
		}
		if len(playerCastPositions) != 5 {
			t.Fatalf("recipient %d player cast positions=%v, want five", recipient.id, playerCastPositions)
		}
		for index := 1; index < len(playerCastPositions); index++ {
			if playerCastPositions[index] != playerCastPositions[index-1]+1 {
				t.Fatalf("recipient %d player casts were split across the action batch: %v",
					recipient.id, playerCastPositions)
			}
		}
	}
	phases.runNext(t, time.Second)
	if phases.len() != 1 || monsters[0].hp != 100000 {
		t.Fatal("party damage occurred before shared impact")
	}
	drainCombatPhases(t, phases)
	if monsters[0].hp >= 100000 {
		t.Fatal("five-player impact did not damage monster")
	}
	hurtMembers := 0
	for _, member := range server.conns {
		if member.session.battle.playerHP < 100000 {
			hurtMembers++
		}
		if _, cast := member.session.skillCooldowns[100001]; !cast {
			t.Fatalf("automatic player %d missed the round", member.id)
		}
	}
	if hurtMembers == 0 {
		t.Fatal("monster impact did not damage any configured target")
	}
}

func TestLethalAutomaticRoundFinishesBothAttacksBeforeDefeat(t *testing.T) {
	server, phases, ch, start := newRoundTestBattle(t)
	battle := ch.session.battle
	ch.session.setAutoBattle(true)
	battle.playerHP, battle.monsters[0].hp = 1, 1
	server.combatTickChannelAt(ch, start.Add(5*time.Second))
	phases.runNext(t, time.Second)
	if battle.ended || battle.playerHP != 1 || battle.monsters[0].hp != 1 {
		t.Fatal("lethal damage was settled before impact")
	}
	drainCombatPhases(t, phases)
	if battle.monsters[0].hp != 0 || battle.monsters[0].alive {
		t.Fatal("player's launched attack was lost after receiving lethal damage")
	}
	damageTargets := make(map[int64]bool)
	for _, frame := range decodeRecordedFrames(t, ch.conn.(*recordingConn).Bytes()) {
		if frame.opcode == protocol.OpM2C_BattleSkillRet {
			var damage protocol.M2C_BattleSkillRet
			if err := proto.Unmarshal(frame.body, &damage); err != nil {
				t.Fatal(err)
			}
			if damage.ChangeHpValue < 0 {
				damageTargets[damage.UnitId] = true
			}
		}
	}
	if !damageTargets[ch.session.playerID] || !damageTargets[battle.monsters[0].id] || !battle.ended {
		t.Fatalf("lethal round did not finish both hits: targets=%v ended=%v", damageTargets, battle.ended)
	}
	server.closed.Store(true)
}

func TestEnablingAutomaticAfterDuePlayerWindowActsImmediately(t *testing.T) {
	server, phases, ch, start := newRoundTestBattle(t)
	enabledAt := start.Add(5500 * time.Millisecond)
	ch.session.setAutoBattleAt(true, enabledAt)
	server.combatTickChannelAt(ch, enabledAt)
	if _, cast := ch.session.skillCooldowns[100001]; !cast {
		t.Fatal("automatic did not act when the player's action window was already due")
	}
	if want := enabledAt.Add(playerPublicActionInterval(ch.session)); !ch.session.battleActionReadyAt.Equal(want) {
		t.Fatalf("next player action = %s, want %s", ch.session.battleActionReadyAt, want)
	}
	drainCombatPhases(t, phases)
}

func TestRoundCooldownResetTargetsCastingMember(t *testing.T) {
	loadOnlineTablesForTest(t)
	server, _, first, second := newSharedPartyBattleForTest(t)
	first.session.skillOrder = []int32{100001}
	second.session.skillOrder = []int32{100001}
	firstBefore := len(decodeRecordedFrames(t, first.conn.(*recordingConn).Bytes()))
	secondBefore := len(decodeRecordedFrames(t, second.conn.(*recordingConn).Bytes()))
	wave := &monsterAttackWave{origin: first, battle: first.session.battle}
	server.emitAttackWaveResults(wave, []CombatEvent{{Type: CombatEventGlobalCooldown,
		Source: PlayerCombatUnit(second.session.playerID), Target: PlayerCombatUnit(second.session.playerID)}})
	for _, frame := range decodeRecordedFrames(t, first.conn.(*recordingConn).Bytes())[firstBefore:] {
		if frame.opcode == protocol.OpM2C_StartCD {
			t.Fatal("round origin received another member's cooldown reset")
		}
	}
	found := false
	for _, frame := range decodeRecordedFrames(t, second.conn.(*recordingConn).Bytes())[secondBefore:] {
		found = found || frame.opcode == protocol.OpM2C_StartCD
	}
	if !found {
		t.Fatal("casting member did not receive cooldown reset")
	}
}
