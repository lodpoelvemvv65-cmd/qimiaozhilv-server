package main

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"mhqserver/protocol"
)

func TestWorldBossLifecycle(t *testing.T) {
	// 清理状态（默认无 DB，走纯内存路径）
	old := globalServer
	globalServer = nil
	defer func() { globalServer = old }()
	worldBossMu.Lock()
	worldBossStates = map[int32]*worldBossState{}
	worldBossMu.Unlock()

	layer := int32(1)
	if bossAlive(layer) != true {
		t.Fatal("fresh boss should be alive")
	}
	if bossDeadRemaining(layer) != 0 {
		t.Fatal("alive boss should have no respawn timer")
	}
	// 击杀
	markBossDead(layer, "测试玩家")
	if bossAlive(layer) {
		t.Fatal("killed boss should be dead")
	}
	if r := bossDeadRemaining(layer); r <= 0 {
		t.Fatalf("dead boss should have respawn remaining, got %v", r)
	}
	// 模拟刷新时间到：把 deadAt 置为过去 → 自动复活
	worldBossMu.Lock()
	worldBossStates[layer].deadAt = time.Now().Add(-time.Hour)
	worldBossMu.Unlock()
	if bossAlive(layer) != true {
		t.Fatal("boss past respawn interval should revive")
	}
	if bossDeadRemaining(layer) != 0 {
		t.Fatal("revived boss should have no timer")
	}
}

func TestBossLayerForMapID(t *testing.T) {
	old := tables
	defer func() { tables = old }()
	tables = &datatables{
		bossBase: map[int64]map[string]interface{}{
			1001: testRow(map[string]int64{"MonsterId": 10255, "RefreshInterval": 3600000}),
		},
	}
	bossBaseRow := tables.bossBase[1001]
	bossBaseRow["Name"] = "篮球行者（物）"
	if bossLayerForMapID(1000401) != 0 {
		t.Fatal("main city should not be a boss layer")
	}
	if bossLayerForMapID(1001001) != 1 {
		t.Fatal("10010 layer1 should map to boss layer 1")
	}
	if bossLayerForMapID(1001026) != 0 {
		t.Fatal("layer 26 out of range")
	}
	if bossRefreshInterval(1) != time.Hour {
		t.Fatalf("refresh interval = %v, want 1h", bossRefreshInterval(1))
	}
	if bossName(1) != "篮球行者（物）" {
		t.Fatalf("boss name = %q", bossName(1))
	}
}

func TestEveryWorldBossRefreshIntervalMatchesClientTable(t *testing.T) {
	loadOnlineTablesForTest(t)
	wantCounts := map[time.Duration]int{
		5 * time.Minute:  1,
		time.Hour:        5,
		90 * time.Minute: 3,
		3 * time.Hour:    6,
		4 * time.Hour:    4,
		6 * time.Hour:    3,
		8 * time.Hour:    1,
		12 * time.Hour:   2,
	}
	gotCounts := make(map[time.Duration]int)
	for layer := int32(1); layer <= 25; layer++ {
		row := tables.bossBase[int64(1000+layer)]
		if row == nil {
			t.Fatalf("BossBase layer %d is missing", layer)
		}
		want := time.Duration(int64(num(row["RefreshInterval"]))) * time.Millisecond
		if got := bossRefreshInterval(layer); got != want {
			t.Errorf("boss layer %d refresh = %v, want client value %v", layer, got, want)
		}
		gotCounts[want]++
	}
	if len(gotCounts) != len(wantCounts) {
		t.Fatalf("refresh interval groups = %v, want %v", gotCounts, wantCounts)
	}
	for interval, want := range wantCounts {
		if got := gotCounts[interval]; got != want {
			t.Errorf("boss refresh %v count = %d, want %d", interval, got, want)
		}
	}
}

func TestBossRefreshOnlyReachesPlayersOnRevivedLayer(t *testing.T) {
	loadOnlineTablesForTest(t)
	onLayer, onLayerConn := consistencyChannel(1, 1001001)
	otherLayer, otherLayerConn := consistencyChannel(2, 1001002)
	mainCity, mainCityConn := consistencyChannel(3, 1000401)
	server := &Server{conns: map[int64]*channel{
		onLayer.id:    onLayer,
		otherLayer.id: otherLayer,
		mainCity.id:   mainCity,
	}}

	if got := server.broadcastBossRefresh(1); got != 1 {
		t.Fatalf("refresh recipients = %d, want 1", got)
	}
	frames := decodeRecordedFrames(t, onLayerConn.Bytes())
	if len(frames) != 1 || frames[0].opcode != protocol.OpM2C_BossRefresh {
		t.Fatalf("on-layer frames = %v, want BossRefresh", recordedOpcodes(t, onLayerConn.Bytes()))
	}
	message := &protocol.M2C_BossRefresh{}
	if err := proto.Unmarshal(frames[0].body, message); err != nil {
		t.Fatal(err)
	}
	if message.BossId != 1001 || message.ActorId != onLayer.session.playerID {
		t.Fatalf("BossRefresh = %+v, want BossId=1001 ActorId=%d", message, onLayer.session.playerID)
	}
	if len(otherLayerConn.Bytes()) != 0 || len(mainCityConn.Bytes()) != 0 {
		t.Fatalf("off-layer players received refresh: other=%v city=%v",
			recordedOpcodes(t, otherLayerConn.Bytes()), recordedOpcodes(t, mainCityConn.Bytes()))
	}
}
