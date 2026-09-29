package main

import (
	"testing"
	"time"

	"mhqserver/protocol"
)

func TestMonsterAttackAnimationCompatibilityUsesPrefab(t *testing.T) {
	oldTables := tables
	tables = &datatables{monsterBase: map[int64]map[string]interface{}{
		10439: {"PrefabId": int64(monsterPrefabWithoutAttackAnimation)},
		10431: {"PrefabId": int64(277)},
	}}
	t.Cleanup(func() { tables = oldTables })

	if monsterAttackAnimationAvailable(10439) {
		t.Fatal("Monster80 prefab was marked as having an Attack animation")
	}
	if !monsterAttackAnimationAvailable(10431) {
		t.Fatal("a normal monster prefab was incorrectly marked as missing Attack")
	}
	if !monsterAttackAnimationAvailable(99999) {
		t.Fatal("unknown monster should retain the normal presentation path")
	}
}

func TestMonsterWithoutAttackAnimationStillRunsCombatWithoutPlaySkill(t *testing.T) {
	oldTables := tables
	tables = &datatables{monsterBase: map[int64]map[string]interface{}{
		10439: {"PrefabId": int64(monsterPrefabWithoutAttackAnimation)},
	}}
	t.Cleanup(func() { tables = oldTables })
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = loadTestSkillLogic(t)
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })

	ss := newSession()
	ss.playerID = 100
	monster := &monsterUnit{
		id: 200, monsterID: 10439, hp: 1000, maxHP: 1000,
		phyAtk: 100, alive: true,
	}
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
	if got := recordedOpcodes(t, conn.Bytes()); hasOpcode(got, protocol.OpM2C_PlaySkill) {
		t.Fatalf("missing-Attack monster emitted PlaySkill: %v", got)
	}
	if phases.len() != 1 {
		t.Fatalf("missing-Attack monster did not schedule combat resolution: phases=%d", phases.len())
	}
	phases.runNext(t, time.Second)
	for phases.len() > 0 {
		phases.runNext(t, phases.items[0].delay)
	}
	if battle.playerHP >= battle.playerMaxHP {
		t.Fatalf("missing-Attack monster did not retain authoritative damage: hp=%d/%d", battle.playerHP, battle.playerMaxHP)
	}
}

func TestMonsterAttackTimerRunsWhilePlayerAutoBattleIsDisabled(t *testing.T) {
	oldTables := tables
	tables = &datatables{monsterBase: map[int64]map[string]interface{}{}}
	t.Cleanup(func() { tables = oldTables })
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = loadTestSkillLogic(t)
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })

	ss := newSession()
	ss.playerID = 100
	ss.autoBattle = false
	monster := &monsterUnit{
		id: 200, monsterID: 1, hp: 1000, maxHP: 1000,
		phyAtk: 100, alive: true,
	}
	battle := &battleState{
		owner: ss, playerHP: 1000, playerMaxHP: 1000, phyDef: 10,
		monsters: []*monsterUnit{monster},
	}
	battle.runtime = NewCombatRuntime(battle, ss.playerID, nil)
	ss.battle = battle
	readyAt := time.Unix(1_800_000_000, 0)
	battle.monsterReadyAt = readyAt
	conn := &recordingConn{}
	ch := &channel{id: 1, conn: conn, session: ss}
	server, phases := newQueuedCombatServer()

	server.combatTickChannelAt(ch, readyAt.Add(-time.Millisecond))
	if phases.len() != 0 || len(conn.Bytes()) != 0 {
		t.Fatalf("monster acted before its deadline: phases=%d opcodes=%v",
			phases.len(), recordedOpcodes(t, conn.Bytes()))
	}
	server.combatTickChannelAt(ch, readyAt)
	if phases.len() != 1 || !hasOpcode(recordedOpcodes(t, conn.Bytes()), protocol.OpM2C_PlaySkill) {
		t.Fatalf("monster did not attack at deadline with auto battle off: phases=%d opcodes=%v",
			phases.len(), recordedOpcodes(t, conn.Bytes()))
	}
	if want := readyAt.Add(monsterActionInterval); !battle.monsterReadyAt.Equal(want) {
		t.Fatalf("next monster action = %s, want %s", battle.monsterReadyAt, want)
	}

	for phases.len() > 0 {
		phases.runNext(t, phases.items[0].delay)
	}
	if battle.playerHP >= battle.playerMaxHP {
		t.Fatalf("independent monster attack did not deal damage: hp=%d/%d",
			battle.playerHP, battle.playerMaxHP)
	}
}

func TestIdleManualPlayerCanActBetweenMonsterRounds(t *testing.T) {
	installPublicActionWindowTestConfig(t)
	ss := newSession()
	ss.playerID, ss.jobID = 100, 1
	ss.skills = map[int32]int32{100001: 1}
	monster := &monsterUnit{id: 200, monsterID: 1, hp: 1_000_000_000, maxHP: 1_000_000_000, alive: true}
	battle := &battleState{
		owner: ss, playerHP: 1_000_000, playerMaxHP: 1_000_000,
		playerMP: 1_000_000, playerMaxMP: 1_000_000,
		monsters: []*monsterUnit{monster},
	}
	battle.runtime = NewCombatRuntime(battle, ss.playerID, nil)
	ss.battle = battle
	start := time.Unix(1_800_000_000, 0)
	ss.battleActionReadyAt = start
	battle.monsterReadyAt = start
	conn := &recordingConn{}
	ch := &channel{id: 1, conn: conn, session: ss}
	server, phases := newQueuedCombatServer()

	server.combatTickChannelAt(ch, start)
	wantNext := start.Add(9 * time.Second)
	if !ss.battleActionReadyAt.Equal(start) {
		t.Fatalf("player deadline after idle monster wave = %s, want %s",
			ss.battleActionReadyAt, wantNext)
	}
	if _, cast := server.castSkillLockedWithLogAt(ch, 100001, &protocol.M2C_UseMainUISkill{}, false,
		start.Add(3*time.Second)); !cast {
		t.Fatal("idle manual player could not act between monster rounds")
	}
	// 手动出手不再经过任何排队字段：成功即生成自己的 wave。
	// 这里断言的是"怪物那一波 + 玩家自己那一波"两条相位同时存在。
	if phases.len() != 2 {
		t.Fatalf("independent player and monster phases = %d, want 2", phases.len())
	}

	for phases.len() > 0 {
		phases.runNext(t, phases.items[0].delay)
	}
	if _, cast := server.castSkillLockedWithLogAt(ch, 100001, &protocol.M2C_UseMainUISkill{}, false, wantNext); !cast {
		t.Fatal("player could not act on the next shared combat round")
	}
	drainCombatPhases(t, phases)
}

func TestManualClickIsNotQueuedByMonsterClock(t *testing.T) {
	installPublicActionWindowTestConfig(t)
	ss := newSession()
	ss.playerID, ss.jobID = 100, 1
	ss.skills = map[int32]int32{100001: 1}
	monster := &monsterUnit{id: 200, monsterID: 1, hp: 1_000_000_000, maxHP: 1_000_000_000, alive: true}
	battle := &battleState{
		owner: ss, playerHP: 1_000_000, playerMaxHP: 1_000_000,
		playerMP: 1_000_000, playerMaxMP: 1_000_000,
		monsters: []*monsterUnit{monster},
	}
	battle.runtime = NewCombatRuntime(battle, ss.playerID, nil)
	ss.battle = battle
	start := time.Unix(1_800_000_000, 0)
	ss.battleActionReadyAt = start
	battle.monsterReadyAt = start
	ch := &channel{id: 1, conn: &recordingConn{}, session: ss}
	server, phases := newQueuedCombatServer()

	server.combatTickChannelAt(ch, start)
	if _, cast := server.castSkillLockedWithLogAt(ch, 100001, &protocol.M2C_UseMainUISkill{}, false,
		start.Add(3*time.Second)); !cast {
		t.Fatal("manual click blocked between monster rounds")
	}
	// 手动出手必须**立即结算**，而不是攒到怪物的下一轮。结算这一批相位后
	// 怪物就该掉血；如果它被排队，这里会是 0 伤害。
	hpBefore := monster.hp
	drainCombatPhases(t, phases)
	if monster.hp >= hpBefore {
		t.Fatal("手动出手没有立即结算（怪物未掉血），说明它被排队到了下一轮")
	}

	wantNext := start.Add(monsterActionInterval)
	server.combatTickChannelAt(ch, wantNext)
	if !ss.battleActionReadyAt.Equal(start.Add(9 * time.Second)) {
		t.Fatalf("player deadline after queued cast = %s, want %s",
			ss.battleActionReadyAt, wantNext.Add(maxPublicActionInterval))
	}
	if !battle.monsterReadyAt.Equal(wantNext.Add(monsterActionInterval)) {
		t.Fatalf("monster deadline after shared round = %s, want %s",
			battle.monsterReadyAt, wantNext.Add(monsterActionInterval))
	}
}

func TestMonsterTimerDoesNotDeferAutomaticCastAlreadyDue(t *testing.T) {
	installPublicActionWindowTestConfig(t)
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = loadTestSkillLogic(t)
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })

	ss := newSession()
	ss.playerID, ss.jobID = 100, 1
	ss.skills = map[int32]int32{100001: 1}
	ss.autoBattle = true
	monster := &monsterUnit{id: 200, monsterID: 1, hp: 1_000_000_000, maxHP: 1_000_000_000, alive: true}
	battle := &battleState{
		owner: ss, playerHP: 1_000_000, playerMaxHP: 1_000_000,
		playerMP: 1_000_000, playerMaxMP: 1_000_000,
		monsters: []*monsterUnit{monster},
	}
	battle.runtime = NewCombatRuntime(battle, ss.playerID, nil)
	ss.battle = battle
	now := time.Unix(1_800_000_000, 0)
	ss.battleActionReadyAt = now
	ss.autoBattleNextCastAt = now
	battle.monsterReadyAt = now
	server, phases := newQueuedCombatServer()
	ch := &channel{id: 1, conn: &recordingConn{}, session: ss}

	server.monstersAttackAt(ch, battle, now)
	if !ss.battleActionReadyAt.Equal(now.Add(6*time.Second)) || !ss.autoBattleNextCastAt.Equal(now.Add(6*time.Second)) {
		t.Fatalf("monster wave deferred due automatic action: player=%s auto=%s want=%s",
			ss.battleActionReadyAt, ss.autoBattleNextCastAt, now)
	}
	if phases.len() != 1 {
		t.Fatalf("monster wave phases=%d, want 1", phases.len())
	}
	if !server.autoBattleStepAt(ch, ss.autoBattleEpoch, now) {
		t.Fatal("automatic worker stopped at the shared action boundary")
	}
	drainCombatPhases(t, phases)
	if monster.hp >= monster.maxHP {
		t.Fatal("automatic cast did not run at the shared action boundary")
	}
}

func TestAutomaticCooldownEndingAfterMonsterBoundaryUsesPlayerClock(t *testing.T) {
	installPublicActionWindowTestConfig(t)
	oldCatalog := skillLogicCatalog
	skillLogicCatalog = loadTestSkillLogic(t)
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })

	ss := newSession()
	ss.playerID, ss.jobID = 100, 1
	ss.skills = map[int32]int32{100001: 1}
	ss.autoBattle = true
	monster := &monsterUnit{id: 200, monsterID: 1, hp: 1_000_000_000, maxHP: 1_000_000_000, alive: true}
	battle := &battleState{
		owner: ss, playerHP: 1_000_000, playerMaxHP: 1_000_000,
		playerMP: 1_000_000, playerMaxMP: 1_000_000,
		monsters: []*monsterUnit{monster},
	}
	battle.runtime = NewCombatRuntime(battle, ss.playerID, nil)
	ss.battle = battle
	now := time.Unix(1_800_000_000, 0)
	playerReady := now.Add(250 * time.Millisecond)
	ss.battleActionReadyAt = playerReady
	ss.autoBattleNextCastAt = playerReady
	battle.monsterReadyAt = now
	server, phases := newQueuedCombatServer()
	ch := &channel{id: 1, conn: &recordingConn{}, session: ss}

	server.monstersAttackAt(ch, battle, now)
	if !ss.battleActionReadyAt.Equal(playerReady) || !ss.autoBattleNextCastAt.Equal(playerReady) {
		t.Fatalf("monster wave skipped a late automatic round: player=%s auto=%s want=%s",
			ss.battleActionReadyAt, ss.autoBattleNextCastAt, playerReady)
	}
	if !server.autoBattleStepAt(ch, ss.autoBattleEpoch, playerReady) {
		t.Fatal("automatic worker stopped after the shared monster wave")
	}
	if monster.hp != monster.maxHP {
		t.Fatal("deferred automatic cast dealt damage before impact")
	}
	drainCombatPhases(t, phases)
	if monster.hp >= monster.maxHP {
		t.Fatal("automatic cast did not run on the independent player clock")
	}
	if want := playerReady.Add(playerPublicActionInterval(ss)); !ss.battleActionReadyAt.Equal(want) {
		t.Fatalf("player deadline after late automatic cast = %s, want %s",
			ss.battleActionReadyAt, want)
	}
}

func TestManualCastKeepsItsOwnSixSecondCooldown(t *testing.T) {
	installPublicActionWindowTestConfig(t)
	ss := newSession()
	ss.playerID, ss.jobID = 100, 1
	ss.skills = map[int32]int32{100001: 1}
	monster := &monsterUnit{id: 200, monsterID: 1, hp: 1_000_000_000, maxHP: 1_000_000_000, alive: true}
	battle := &battleState{
		owner: ss, playerHP: 1_000_000, playerMaxHP: 1_000_000,
		playerMP: 1_000_000, playerMaxMP: 1_000_000,
		monsters: []*monsterUnit{monster},
	}
	battle.runtime = NewCombatRuntime(battle, ss.playerID, nil)
	ss.battle = battle
	start := time.Unix(1_800_000_000, 0)
	ss.battleActionReadyAt = start
	battle.monsterReadyAt = start.Add(monsterActionInterval)
	ch := &channel{id: 1, conn: &recordingConn{}, session: ss}
	server, phases := newQueuedCombatServer()
	if _, cast := server.castSkillLockedWithLogAt(ch, 100001, &protocol.M2C_UseMainUISkill{}, false,
		start.Add(250*time.Millisecond)); !cast {
		t.Fatal("player could not cast 250ms after the monster round")
	}
	if want := start.Add(maxPublicActionInterval + 250*time.Millisecond); !ss.battleActionReadyAt.Equal(want) {
		t.Fatalf("player deadline after late cast = %s, want monster round %s",
			ss.battleActionReadyAt, want)
	}
	drainCombatPhases(t, phases)
}

func hasOpcode(opcodes []uint16, want uint16) bool {
	for _, opcode := range opcodes {
		if opcode == want {
			return true
		}
	}
	return false
}
