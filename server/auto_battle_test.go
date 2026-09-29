package main

import (
	"encoding/json"
	"io"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	"mhqserver/protocol"
)

func TestAutoBattleSlotsUseConfiguredOrderAndBasicFallback(t *testing.T) {
	ss := &session{
		jobID:      1,
		skills:     map[int32]int32{100001: 1, 110101: 1, 110201: 1},
		skillOrder: []int32{100001, 110101, 110201},
		autoSkills: []int32{100001, 110201, 999999, 110101, 110201},
	}
	ss.battleMu.Lock()
	got := ss.autoBattleSkillIDsLocked()
	ss.battleMu.Unlock()
	// autoSkills 顺序去重（排除未学 999999 与基础 100001）+ 基础兜底
	if want := []int32{110201, 110101, 100001}; !reflect.DeepEqual(got, want) {
		t.Fatalf("auto skills = %v, want %v", got, want)
	}
}

func TestAutoBattleSlotsRotateAfterSuccessfulSkill(t *testing.T) {
	ss := &session{
		jobID:                 1,
		skills:                map[int32]int32{100001: 1, 110101: 1, 110201: 1},
		autoSkills:            []int32{110201, 110101},
		autoBattleSkillCursor: 1,
	}
	ss.battleMu.Lock()
	got := ss.autoBattleSkillIDsLocked()
	ss.advanceAutoBattleCursorLocked(110101)
	cursor := ss.autoBattleSkillCursor
	ss.battleMu.Unlock()
	if want := []int32{110101, 110201, 100001}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rotated auto skills = %v, want %v", got, want)
	}
	if cursor != 0 {
		t.Fatalf("cursor after second skill = %d, want 0", cursor)
	}
}

func TestAutoBattleFallsBackToBasicAttackOnResourceFailure(t *testing.T) {
	oldTables := tables
	oldCatalog := skillLogicCatalog
	t.Cleanup(func() {
		tables = oldTables
		skillLogicCatalog = oldCatalog
	})
	skillLogicCatalog = loadTestSkillLogic(t)

	const (
		basic   int32 = 100001
		special int32 = 110101
	)
	specialBase := testRow(map[string]int64{"CD": 5000, "CastType": 1})
	specialBase["CastValue"] = json.Number("0.50")
	tables = &datatables{
		skillConfig: map[int64]map[string]interface{}{
			int64(basic) * 100:     testRow(map[string]int64{"CD": 1000, "CastType": 0}),
			int64(basic)*100 + 1:   testRow(map[string]int64{"Args1": 100}),
			int64(special) * 100:   specialBase,
			int64(special)*100 + 1: testRow(map[string]int64{"Args0": 1, "Args1": 500}),
		},
		monsterBase: map[int64]map[string]interface{}{},
		skillGroup:  map[int64]map[string]interface{}{},
	}

	monster := &monsterUnit{id: 1001, hp: 1000, maxHP: 1000, alive: true}
	ss := newSession()
	ss.playerID = 7
	ss.jobID = 1
	ss.skills = map[int32]int32{basic: 1, special: 1}
	ss.skillOrder = []int32{basic, special}
	// Including the basic attack first must not move it ahead of configured skills.
	ss.autoSkills = []int32{basic, special}
	ss.battle = &battleState{
		monsters: []*monsterUnit{monster}, playerHP: 100, playerMaxHP: 100,
		playerMP: 1, playerMaxMP: 100, phyAtk: 50,
	}

	client, server := net.Pipe()
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	go io.Copy(io.Discard, client)
	ch := &channel{id: 1, conn: server, session: ss}
	epoch := ss.setAutoBattle(true)

	if keepRunning := (&Server{}).autoBattleStep(ch, epoch); !keepRunning {
		t.Fatal("active automatic battle worker stopped")
	}
	if monster.hp >= 1000 {
		t.Fatalf("basic fallback did not damage the monster: hp=%d", monster.hp)
	}
	if ss.battle.playerMP != 1 {
		t.Fatalf("resource-rejected skill consumed MP: %d", ss.battle.playerMP)
	}
	if _, started := ss.skillCooldowns[special]; started {
		t.Fatal("resource-rejected skill started cooldown")
	}
	if _, started := ss.skillCooldowns[basic]; !started {
		t.Fatal("basic fallback did not start its cooldown")
	}

	// A skill that passes its resource gate but is still cooling down must also
	// fall through without consuming MP.
	monster = &monsterUnit{id: 1002, hp: 1000, maxHP: 1000, alive: true}
	ss.battle = &battleState{
		monsters: []*monsterUnit{monster}, playerHP: 100, playerMaxHP: 100,
		playerMP: 100, playerMaxMP: 100, phyAtk: 50,
	}
	ss.autoBattleNextCastAt = time.Time{}
	ss.skillCooldowns = map[int32]time.Time{special: time.Now().Add(time.Minute)}
	if keepRunning := (&Server{}).autoBattleStep(ch, epoch); !keepRunning {
		t.Fatal("active automatic battle worker stopped on cooldown fallback")
	}
	if monster.hp >= 1000 {
		t.Fatalf("cooldown fallback did not damage the monster: hp=%d", monster.hp)
	}
	if ss.battle.playerMP != 100 {
		t.Fatalf("cooldown-rejected skill consumed MP: %d", ss.battle.playerMP)
	}
}

func TestAutoBattleUsesBasicAttackCadenceNotVisualDelay(t *testing.T) {
	oldTables := tables
	oldCatalog := skillLogicCatalog
	t.Cleanup(func() {
		tables = oldTables
		skillLogicCatalog = oldCatalog
	})
	skillLogicCatalog = loadTestSkillLogic(t)

	const (
		basic   int32 = 100001
		special int32 = 110101
	)
	specialBase := testRow(map[string]int64{"CD": 5500, "CastType": 1, "DelayTime": 1000})
	specialBase["CastValue"] = json.Number("0.02")
	tables = &datatables{skillConfig: map[int64]map[string]interface{}{
		int64(basic) * 100:     testRow(map[string]int64{"CD": 5000, "CastType": 0, "DelayTime": 1000}),
		int64(basic)*100 + 1:   testRow(map[string]int64{"Args1": 100}),
		int64(special) * 100:   specialBase,
		int64(special)*100 + 1: testRow(map[string]int64{"Args0": 1, "Args1": 100}),
	}}

	monster := &monsterUnit{id: 1001, hp: 10000, maxHP: 10000, alive: true}
	ss := newSession()
	ss.playerID, ss.jobID = 7, 1
	ss.skills = map[int32]int32{basic: 1, special: 1}
	ss.autoSkills = []int32{special}
	ss.battle = &battleState{
		monsters: []*monsterUnit{monster}, playerHP: 100, playerMaxHP: 100,
		playerMP: 10000, playerMaxMP: 10000, phyAtk: 50,
	}

	client, server := net.Pipe()
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	go io.Copy(io.Discard, client)
	ch := &channel{id: 1, conn: server, session: ss}
	epoch := ss.setAutoBattle(true)
	now := time.Now()
	if !(&Server{}).autoBattleStepAt(ch, epoch, now) {
		t.Fatal("automatic battle stopped on first cast")
	}
	hpAfterFirst := monster.hp
	mpAfterFirst := ss.battle.playerMP
	if mpAfterFirst != 9800 {
		t.Fatalf("first skill MP = %d, want 9800 after 2%% cost", mpAfterFirst)
	}
	if !(&Server{}).autoBattleStepAt(ch, epoch, now.Add(4999*time.Millisecond)) {
		t.Fatal("automatic battle stopped while waiting for action interval")
	}
	if monster.hp != hpAfterFirst || ss.battle.playerMP != mpAfterFirst {
		t.Fatalf("cast occurred before five-second action interval: hp=%d/%d mp=%d/%d",
			monster.hp, hpAfterFirst, ss.battle.playerMP, mpAfterFirst)
	}
	if !(&Server{}).autoBattleStepAt(ch, epoch, now.Add(5*time.Second)) {
		t.Fatal("automatic battle stopped after action interval")
	}
	if monster.hp >= hpAfterFirst {
		t.Fatalf("basic fallback did not cast after action interval: hp=%d", monster.hp)
	}
}

func TestAutoBattleUsesAuthoritativeControlGate(t *testing.T) {
	oldCatalog := skillLogicCatalog
	t.Cleanup(func() { skillLogicCatalog = oldCatalog })
	skillLogicCatalog = loadTestSkillLogic(t)

	const (
		basic   int32 = 100001
		special int32 = 110101
	)
	monster := &monsterUnit{id: 1001, hp: 1000, maxHP: 1000, alive: true}
	battle := &battleState{
		monsters: []*monsterUnit{monster}, playerHP: 100, playerMaxHP: 100,
		playerMP: 100, playerMaxMP: 100, phyAtk: 50,
	}
	ss := newSession()
	ss.playerID = 7
	ss.jobID = 1
	ss.skills = map[int32]int32{basic: 1, special: 1}
	ss.skillOrder = []int32{basic, special}
	ss.autoSkills = []int32{special}
	ss.battle = battle
	battle.runtime = NewCombatRuntime(battle, ss.playerID, nil)
	_, err := battle.runtime.ApplyEffects(
		CombatEffectContext{Source: MonsterCombatUnit(monster.id), Target: battle.runtime.Player()},
		[]CombatEffect{EffectSpec{
			Key: "auto-stun", Kind: CombatEffectStatus, Status: CombatStatusStunned,
			Duration: time.Minute,
		}},
	)
	if err != nil {
		t.Fatalf("apply stun: %v", err)
	}
	epoch := ss.setAutoBattle(true)
	if keepRunning := (&Server{}).autoBattleStep(&channel{session: ss}, epoch); !keepRunning {
		t.Fatal("control-gated automatic battle worker stopped")
	}
	if monster.hp != 1000 || battle.playerMP != 100 || len(ss.skillCooldowns) != 0 {
		t.Fatalf("control-gated auto cast changed state: monster=%d mp=%d cooldowns=%v",
			monster.hp, battle.playerMP, ss.skillCooldowns)
	}
}

func TestAutoBattleSlotsFallbackWhenNoAutoSkillsConfigured(t *testing.T) {
	ss := &session{
		jobID:      3,
		skills:     map[int32]int32{200001: 1, 210101: 1},
		skillOrder: []int32{200001, 210101},
	}
	ss.battleMu.Lock()
	got := ss.autoBattleSkillIDsLocked()
	ss.battleMu.Unlock()
	// 无自动技能 → 仅基础攻击兜底
	if want := []int32{200001}; !reflect.DeepEqual(got, want) {
		t.Fatalf("auto skills = %v, want %v", got, want)
	}
}

func TestAutoBattleGenerationInvalidatesOldWorker(t *testing.T) {
	ss := &session{}
	first := ss.setAutoBattle(true)
	second := ss.setAutoBattle(true)
	if second == first {
		t.Fatalf("epoch did not advance: %d", first)
	}
	ss.battleMu.Lock()
	active := ss.autoBattle && ss.autoBattleEpoch == second
	ss.battleMu.Unlock()
	if !active {
		t.Fatal("latest automatic battle generation is not active")
	}
	ss.stopAutoBattle()
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	if ss.autoBattle || ss.autoBattleEpoch == second {
		t.Fatal("stopAutoBattle did not disable and invalidate the worker")
	}
}

func TestRunAutoBattleRepeatedEnableAndStopCleanly(t *testing.T) {
	ss := newSession()
	ss.battle = &battleState{monsters: []*monsterUnit{{id: 1, hp: 100, maxHP: 100, alive: true}}}
	ch := &channel{session: ss}
	server := &Server{}

	first := ss.setAutoBattle(true)
	firstDone := make(chan struct{})
	go func() {
		server.runAutoBattle(ch, first)
		close(firstDone)
	}()
	second := ss.setAutoBattle(true)
	secondDone := make(chan struct{})
	go func() {
		server.runAutoBattle(ch, second)
		close(secondDone)
	}()

	select {
	case <-firstDone:
	case <-time.After(2 * autoBattlePollInterval):
		t.Fatal("old automatic battle generation did not exit")
	}
	select {
	case <-secondDone:
		t.Fatal("latest automatic battle generation exited while enabled")
	default:
	}
	ss.stopAutoBattle()
	select {
	case <-secondDone:
	case <-time.After(2 * autoBattlePollInterval):
		t.Fatal("automatic battle worker did not exit after stop")
	}
}

func TestAutoBattleStopsOutsideBattle(t *testing.T) {
	ss := newSession()
	epoch := ss.setAutoBattle(true)
	if (&Server{}).autoBattleStep(&channel{session: ss}, epoch) {
		t.Fatal("automatic battle worker kept running without an active battle")
	}
	// 新语义：无战斗时 worker 退出但【保留开关】——玩家开自动战斗后打完当前怪，
	// 点下一个怪开新战斗时 finishStartBattle 重新拉起 worker（"自动战斗持续"）。
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	if !ss.autoBattle {
		t.Fatal("automatic battle switch should persist across fights (resumed on next fight)")
	}
	if ss.autoBattleEpoch == epoch {
		t.Fatal("automatic battle worker generation should be invalidated")
	}
}

func TestAutoBattleToggleOutsideFightIsSavedAsNextFightPreference(t *testing.T) {
	ss := newSession()
	ss.playerID = 7
	ch := &channel{session: ss}
	server := &Server{}

	response := server.onAutoBattle(ch, &protocol.C2M_AutoBattle{RpcId: 1, IsAuto: true})
	if response.(*protocol.M2C_AutoBattle).Message != "" {
		t.Fatalf("enable response = %+v", response)
	}
	ss.battleMu.Lock()
	enabled, running := ss.autoBattleEnabled, ss.autoBattle
	ss.battleMu.Unlock()
	if !enabled || running {
		t.Fatalf("outside-fight toggle: enabled=%v running=%v, want true/false", enabled, running)
	}
	setting := server.onGetMainUISetting(ch, &protocol.C2M_GetMainUISetting{}).(*protocol.M2C_GetMainUISetting)
	if !setting.IsAutoSkill {
		t.Fatal("main UI did not reflect the saved automatic-battle preference")
	}

	server.onAutoBattle(ch, &protocol.C2M_AutoBattle{RpcId: 2, IsAuto: false})
	if ss.autoBattleEnabled || ss.autoBattle {
		t.Fatalf("disable left enabled=%v running=%v", ss.autoBattleEnabled, ss.autoBattle)
	}
}

type blockingWriteConn struct {
	started   chan struct{}
	closed    chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
}

func newBlockingWriteConn() *blockingWriteConn {
	return &blockingWriteConn{started: make(chan struct{}), closed: make(chan struct{})}
}

func (c *blockingWriteConn) Read([]byte) (int, error) { return 0, net.ErrClosed }
func (c *blockingWriteConn) Write([]byte) (int, error) {
	c.startOnce.Do(func() { close(c.started) })
	<-c.closed
	return 0, net.ErrClosed
}
func (c *blockingWriteConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}
func (c *blockingWriteConn) LocalAddr() net.Addr              { return testNetAddr("local") }
func (c *blockingWriteConn) RemoteAddr() net.Addr             { return testNetAddr("remote") }
func (c *blockingWriteConn) SetDeadline(time.Time) error      { return nil }
func (c *blockingWriteConn) SetReadDeadline(time.Time) error  { return nil }
func (c *blockingWriteConn) SetWriteDeadline(time.Time) error { return nil }

type testNetAddr string

func (a testNetAddr) Network() string { return "test" }
func (a testNetAddr) String() string  { return string(a) }

func TestRemoveChannelClosesTransportBeforeWaitingForBattleLock(t *testing.T) {
	conn := newBlockingWriteConn()
	ss := newSession()
	ss.setAutoBattle(true)
	ch := &channel{id: 9, conn: conn, session: ss}
	server := &Server{conns: map[int64]*channel{ch.id: ch}}

	writeDone := make(chan struct{})
	go func() {
		ss.battleMu.Lock()
		_, _ = conn.Write([]byte("battle push"))
		ss.battleMu.Unlock()
		close(writeDone)
	}()
	select {
	case <-conn.started:
	case <-time.After(time.Second):
		t.Fatal("test write did not block")
	}

	removeDone := make(chan struct{})
	go func() {
		server.mu.Lock()
		server.removeChannelLocked(ch)
		server.mu.Unlock()
		close(removeDone)
	}()
	select {
	case <-removeDone:
	case <-time.After(time.Second):
		t.Fatal("disconnect cleanup deadlocked behind a blocked battle write")
	}
	select {
	case <-writeDone:
	case <-time.After(time.Second):
		t.Fatal("closing the transport did not release the battle writer")
	}
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	if ss.autoBattle {
		t.Fatal("disconnect cleanup left automatic battle enabled")
	}
}
