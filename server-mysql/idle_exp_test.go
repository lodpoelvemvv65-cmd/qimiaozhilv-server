package main

import (
	"net"
	"testing"
	"time"

	"mhqserver/internal/operationsconfig"
)

// totalExpOf：把 (等级, 当前经验) 折算成从 1 级起累计获得的总经验，
// 这样断言不受升级边界影响。
func totalExpOf(ss *session) int64 {
	total := ss.exp
	for level := int32(1); level < ss.level; level++ {
		total += expNeed(level)
	}
	return total
}

func townIdleExpSession(playerID int64, mapID int32) *session {
	ss := newSession()
	ss.playerID = playerID
	ss.jobID = 1
	ss.level = 1
	ss.mapID = mapID
	ss.state = sessInGame
	return ss
}

// TestTownIdleExpOnlyGrantsInMainCity：线上抓包实测的挂机经验只在城镇地图下发，
// 每跳固定 24560 点且与等级无关；野外地图和战斗中的角色不发。
func TestTownIdleExpOnlyGrantsInMainCity(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.Progression.ExperienceGainPercent = 100
		config.TownIdleExp = operationsconfig.TownIdleExp{
			Enabled: true, IntervalSeconds: 60, ExpPerTick: 24560,
			MapIDs: []int32{10004, 1000401},
		}
	})
	server := &Server{conns: make(map[int64]*channel)}
	town := townIdleExpSession(11, 10004)
	townCopy := townIdleExpSession(12, 1000401)
	field := townIdleExpSession(13, 1001001)
	inBattle := townIdleExpSession(14, 10004)
	inBattle.battle = &battleState{}
	for _, ss := range []*session{town, townCopy, field, inBattle} {
		server.conns[ss.playerID] = &channel{id: ss.playerID, session: ss, conn: &recordingConn{}}
	}

	server.grantTownIdleExp()

	for _, test := range []struct {
		name string
		ss   *session
		want int64
	}{
		{name: "主城 10004", ss: town, want: 24560 * expStoragePerDisplay},
		{name: "主城副线 1000401", ss: townCopy, want: 24560 * expStoragePerDisplay},
		{name: "野外 1001001", ss: field, want: 0},
		{name: "主城但战斗中", ss: inBattle, want: 0},
	} {
		if got := totalExpOf(test.ss); got != test.want {
			t.Errorf("%s: total exp = %d, want %d (level=%d)", test.name, got, test.want, test.ss.level)
		}
	}
	// 线上 1 级新角色吃到第一跳后到 56 级（online-lv1-idle-20260913.pcapng）。
	if town.level != 56 {
		t.Errorf("fresh role after one tick: level = %d, want 56", town.level)
	}
	if field.level != 1 || field.exp != 0 {
		t.Errorf("field map role progressed: level=%d exp=%d", field.level, field.exp)
	}
}

// TestTownIdleExpAmountIgnoresExperienceMultiplier：那一跳是线上下发的最终值，
// 不能再乘 progression.experience_gain_percent，否则会变成 49120。
func TestTownIdleExpAmountIgnoresExperienceMultiplier(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.Progression.ExperienceGainPercent = 200
		config.TownIdleExp = operationsconfig.TownIdleExp{
			Enabled: true, IntervalSeconds: 60, ExpPerTick: 24560, MapIDs: []int32{10004},
		}
	})
	server := &Server{conns: make(map[int64]*channel)}
	ss := townIdleExpSession(21, 10004)
	server.conns[ss.playerID] = &channel{id: ss.playerID, session: ss, conn: &recordingConn{}}
	server.grantTownIdleExp()
	if got := totalExpOf(ss); got != 24560*expStoragePerDisplay {
		t.Fatalf("total exp = %d, want %d", got, 24560*expStoragePerDisplay)
	}
}

func TestWriteFailureRemovesTownPlayerBeforeIdleExperience(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.Progression.ExperienceGainPercent = 100
		config.TownIdleExp = operationsconfig.TownIdleExp{
			Enabled: true, IntervalSeconds: 60, ExpPerTick: 24560, MapIDs: []int32{10004},
		}
	})
	serverConn, clientConn := net.Pipe()
	if err := clientConn.Close(); err != nil {
		t.Fatal(err)
	}
	ss := townIdleExpSession(22, 10004)
	ch := &channel{id: ss.playerID, session: ss, conn: serverConn}
	removed := make(chan struct{})
	server := &Server{
		conns: map[int64]*channel{ch.id: ch},
		onClose: func(closed *channel) {
			if closed == ch {
				close(removed)
			}
		},
	}

	server.SendToChannel(ch, []byte("closed peer"))
	select {
	case <-removed:
	case <-time.After(time.Second):
		t.Fatal("write failure did not complete channel removal")
	}
	if server.isCurrentOnlineChannel(ch) || len(server.onlineChannels()) != 0 {
		t.Fatal("write-failed channel remained in the online registry")
	}
	server.grantTownIdleExp()
	if got := totalExpOf(ss); got != 0 {
		t.Fatalf("write-failed channel received town idle experience: %d", got)
	}
}

// TestTownIdleExpDisabledAndOutsideWindow：开关关闭时不发经验；同一个节拍窗口
// 只结算一次（下一次窗口才再发）。
func TestTownIdleExpDisabledAndOutsideWindow(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.Progression.ExperienceGainPercent = 100
		config.TownIdleExp = operationsconfig.TownIdleExp{
			Enabled: false, IntervalSeconds: 60, ExpPerTick: 24560, MapIDs: []int32{10004},
		}
	})
	server := &Server{conns: make(map[int64]*channel)}
	ss := townIdleExpSession(31, 10004)
	server.conns[ss.playerID] = &channel{id: ss.playerID, session: ss, conn: &recordingConn{}}
	server.grantTownIdleExp()
	if got := totalExpOf(ss); got != 0 {
		t.Fatalf("disabled config granted exp: %d", got)
	}

	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.Progression.ExperienceGainPercent = 100
		config.TownIdleExp = operationsconfig.TownIdleExp{
			Enabled: true, IntervalSeconds: 60, ExpPerTick: 24560, MapIDs: []int32{10004},
		}
	})
	// 窗口号按 Unix 秒 / 节拍秒数推进：同一窗口内不结算，跨窗口才结算。
	// base 取 60 的整数倍，保证测试时间戳落在窗口起点。
	const base = int64(1_700_000_040)
	window := server.townIdleExpWindow(time.Unix(base, 0))
	if got := server.townIdleExpStep(time.Unix(base+59, 0), window); got != window {
		t.Fatalf("window advanced inside one interval: %d -> %d", window, got)
	}
	if got := totalExpOf(ss); got != 0 {
		t.Fatalf("same-window step granted exp: %d", got)
	}
	window = server.townIdleExpStep(time.Unix(base+60, 0), window)
	if got := totalExpOf(ss); got != 24560*expStoragePerDisplay {
		t.Fatalf("next-window step exp = %d, want %d", got, 24560*expStoragePerDisplay)
	}
	if server.townIdleExpStep(time.Unix(base+119, 0), window) != window {
		t.Fatal("window advanced inside one interval")
	}
	if got := totalExpOf(ss); got != 24560*expStoragePerDisplay {
		t.Fatalf("same-window step after a grant: %d", got)
	}
}
