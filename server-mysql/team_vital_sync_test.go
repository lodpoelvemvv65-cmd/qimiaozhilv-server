package main

import (
	"testing"
	"time"
)

// vitalSyncParty 造一支两人队伍（4201 打怪、4202 是队友），并把队伍登记进 teams。
func vitalSyncParty(t *testing.T) (*Server, *channel, *channel, map[int64]*recordingConn) {
	t.Helper()
	loadOnlineTablesForTest(t)
	server := &Server{conns: map[int64]*channel{}}
	conns := map[int64]*recordingConn{}
	add := func(playerID int64) *channel {
		ss := newSession()
		ss.state = sessInGame
		ss.playerID, ss.name = playerID, "vital"
		ss.jobID, ss.level, ss.trans, ss.mapID = 7, 17007, 2, 10004
		ss.bag = make(map[int32]*bagItem)
		ss.worn = make(map[int32]*bagItem)
		ss.hp, ss.mp = ss.playerMaxHp(), ss.playerMaxMp()
		conn := &recordingConn{}
		ch := &channel{id: playerID, conn: conn, session: ss}
		server.conns[playerID] = ch
		conns[playerID] = conn
		return ch
	}
	fighter := add(4201)
	teammate := add(4202)
	teamMu.Lock()
	teams = map[int64]*teamState{1: {LeaderId: 4201, Members: []int64{4201, 4202}}}
	teamMu.Unlock()
	t.Cleanup(func() {
		teamMu.Lock()
		teams = map[int64]*teamState{}
		teamMu.Unlock()
	})
	return server, fighter, teammate, conns
}

// waitTeamVitalSync 等补推落地（合并间隔 50ms，留足余量）。
func waitTeamVitalSync() { time.Sleep(4 * teamVitalSyncDelay) }

func TestScheduleTeamVitalSyncPushesAuthoritativeVitalsToTeammate(t *testing.T) {
	server, fighter, _, conns := vitalSyncParty(t)
	// 战斗收尾写回的残血：只在会话里改血蓝，不推给任何人。
	fighter.session.hp = fighter.session.playerMaxHp() / 3
	fighter.session.mp = fighter.session.playerMaxMp() / 2

	server.scheduleTeamVitalSync(fighter)
	waitTeamVitalSync()

	pushes := recordedVitals(t, conns[4202].Bytes())
	hp := vitalsFor(pushes, 4201, 1001)
	mp := vitalsFor(pushes, 4201, 1003)
	if len(hp) != 1 || hp[0] != float32(fighter.session.hp) {
		t.Fatalf("队友收到的血量推送 %v, want [%d]", hp, fighter.session.hp)
	}
	if len(mp) != 1 || mp[0] != float32(fighter.session.mp) {
		t.Fatalf("队友收到的蓝量推送 %v, want [%d]", mp, fighter.session.mp)
	}
}

func TestScheduleTeamVitalSyncCoalescesBurstsAndReadsLatestValue(t *testing.T) {
	server, fighter, _, conns := vitalSyncParty(t)
	maxHP := fighter.session.playerMaxHp()

	// 模拟一次单人战斗里连续挨打：每次都改血并请求补推。
	for _, hp := range []int32{maxHP - 1, maxHP - 10, maxHP - 100} {
		fighter.session.hp = hp
		for i := 0; i < 20; i++ {
			server.scheduleTeamVitalSync(fighter)
		}
	}
	fighter.session.hp = maxHP - 150
	server.scheduleTeamVitalSync(fighter)
	waitTeamVitalSync()

	pushes := recordedVitals(t, conns[4202].Bytes())
	hp := vitalsFor(pushes, 4201, 1001)
	if len(hp) != 1 {
		t.Fatalf("61 次请求只应合并成 1 次补推, got %d 条: %v", len(hp), hp)
	}
	if hp[0] != float32(maxHP-150) {
		t.Fatalf("补推的不是触发时刻的最新血量 %v, want %d", hp[0], maxHP-150)
	}
}

func TestScheduleTeamVitalSyncSkipsSoloPlayerAndStaleSession(t *testing.T) {
	server, fighter, _, conns := vitalSyncParty(t)
	fighter.session.hp = 1

	// 没有队伍：不该推给任何人的队友连接。
	teamMu.Lock()
	teams = map[int64]*teamState{}
	teamMu.Unlock()
	server.scheduleTeamVitalSync(fighter)
	waitTeamVitalSync()
	if got := vitalsFor(recordedVitals(t, conns[4202].Bytes()), 4201, 1001); len(got) != 0 {
		t.Fatalf("无队伍时不应补推, got %v", got)
	}

	// 会话已被顶替：同样不推。
	teamMu.Lock()
	teams = map[int64]*teamState{1: {LeaderId: 4201, Members: []int64{4201, 4202}}}
	teamMu.Unlock()
	fighter.session.superseded.Store(true)
	server.scheduleTeamVitalSync(fighter)
	waitTeamVitalSync()
	if got := vitalsFor(recordedVitals(t, conns[4202].Bytes()), 4201, 1001); len(got) != 0 {
		t.Fatalf("会话被顶替后不应补推, got %v", got)
	}
}
