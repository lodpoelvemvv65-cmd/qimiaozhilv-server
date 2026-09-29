package main

import (
	"fmt"
	"reflect"
	"strings"
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

func TestBossRevivalNotifiesAllPlayersWithoutSpawningOffLayer(t *testing.T) {
	loadOnlineTablesForTest(t)
	onLayer, onLayerConn := consistencyChannel(1, 1001001)
	otherLayer, otherLayerConn := consistencyChannel(2, 1001002)
	mainCity, mainCityConn := consistencyChannel(3, 1000401)
	server := &Server{conns: map[int64]*channel{
		onLayer.id:    onLayer,
		otherLayer.id: otherLayer,
		mainCity.id:   mainCity,
	}}

	refreshRecipients, noticeRecipients := server.broadcastBossRevival(1)
	if refreshRecipients != 1 || noticeRecipients != 3 {
		t.Fatalf("revival recipients refresh=%d notice=%d, want 1/3",
			refreshRecipients, noticeRecipients)
	}

	assertNotice := func(ch *channel, conn *recordingConn, wantRefresh bool) {
		t.Helper()
		frames := decodeRecordedFrames(t, conn.Bytes())
		wantFrames := 1
		if wantRefresh {
			wantFrames = 2
		}
		if len(frames) != wantFrames {
			t.Fatalf("player %d opcodes=%v, want %d frames", ch.id, recordedOpcodes(t, conn.Bytes()), wantFrames)
		}
		if wantRefresh && frames[0].opcode != protocol.OpM2C_BossRefresh {
			t.Fatalf("player %d first opcode=%d, want BossRefresh", ch.id, frames[0].opcode)
		}
		noticeFrame := frames[len(frames)-1]
		if noticeFrame.opcode != protocol.OpM2C_SendSystemChat {
			t.Fatalf("player %d notice opcode=%d, want SendSystemChat", ch.id, noticeFrame.opcode)
		}
		var notice protocol.M2C_SendSystemChat
		if err := proto.Unmarshal(noticeFrame.body, &notice); err != nil {
			t.Fatal(err)
		}
		content := bytesFields(t, noticeFrame.body, 1)
		if notice.Type != protocol.ChatType_System || notice.Name != systemNoticeSender ||
			!notice.IsSystemBrocast || notice.ActorId != ch.session.playerID || len(content) != 1 ||
			string(content[0]) != bossRefreshNotice(bossName(1)) {
			t.Fatalf("player %d refresh notice=%+v content=%q", ch.id, &notice, content)
		}
	}

	assertNotice(onLayer, onLayerConn, true)
	assertNotice(otherLayer, otherLayerConn, false)
	assertNotice(mainCity, mainCityConn, false)
}

func TestBossKillUsesSystemChannelAndOnlyRemovesBossOnLayer(t *testing.T) {
	loadOnlineTablesForTest(t)
	onLayer, onLayerConn := consistencyChannel(1, 1001001)
	onLayer.session.fieldBossUnitID = 80001
	mainCity, mainCityConn := consistencyChannel(2, 1000401)
	server := &Server{conns: map[int64]*channel{
		onLayer.id:  onLayer,
		mainCity.id: mainCity,
	}}

	participants := []bossKillParticipant{
		{name: "队长", playerID: 10001},
		{name: "队员二", playerID: 10002},
	}
	server.broadcastBossKill(participants, 1)
	wantContent := fmt.Sprintf("[color=#FF3333]伴随一声巨响，在[/color]"+
		"[color=#FFE600]【队长(10001)】[/color]"+
		"[color=#FFE600]【队员二(10002)】[/color]"+
		"[color=#FF3333]队伍强力攻击和完美防御下，[/color]"+
		"[color=#28E828]【%s】[/color]"+
		"[color=#FF3333]应声倒下！[/color]", bossName(1))

	assertKillNotice := func(ch *channel, conn *recordingConn, wantDead bool) {
		t.Helper()
		frames := decodeRecordedFrames(t, conn.Bytes())
		wantOpcodes := []uint16{protocol.OpM2C_BossBeDefeat, protocol.OpM2C_SendSystemChat}
		if wantDead {
			wantOpcodes = append([]uint16{protocol.OpM2C_BossDead}, wantOpcodes...)
		}
		if got := recordedOpcodes(t, conn.Bytes()); !reflect.DeepEqual(got, wantOpcodes) {
			t.Fatalf("player %d kill opcodes=%v, want %v", ch.id, got, wantOpcodes)
		}
		noticeFrame := frames[len(frames)-1]
		var notice protocol.M2C_SendSystemChat
		if err := proto.Unmarshal(noticeFrame.body, &notice); err != nil {
			t.Fatal(err)
		}
		content := bytesFields(t, noticeFrame.body, 1)
		if notice.Type != protocol.ChatType_System || notice.Name != systemNoticeSender ||
			!notice.IsSystemBrocast || notice.ActorId != ch.session.playerID || len(content) != 1 ||
			string(content[0]) != wantContent {
			t.Fatalf("player %d kill notice=%+v content=%q", ch.id, &notice, content)
		}
	}

	assertKillNotice(onLayer, onLayerConn, true)
	assertKillNotice(mainCity, mainCityConn, false)
}

func TestBossKillParticipantsUseSharedBattleMemberOrder(t *testing.T) {
	leader, _ := consistencyChannel(20001, 1001001)
	leader.session.name = "队长"
	member, _ := consistencyChannel(20002, 1001001)
	member.session.name = "队员"
	party := &partyBattle{
		memberIDs: []int64{leader.session.playerID, member.session.playerID},
		members: map[int64]*battleState{
			leader.session.playerID: {owner: leader.session},
			member.session.playerID: {owner: member.session},
		},
	}
	battle := &battleState{owner: leader.session, party: party}

	got := bossKillParticipants(leader, battle)
	want := []bossKillParticipant{
		{name: "队长", playerID: 20001},
		{name: "队员", playerID: 20002},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("boss participants=%+v, want %+v", got, want)
	}
}

func TestBossKillNoticeEscapesParticipantUBB(t *testing.T) {
	notice := bossKillNotice([]bossKillParticipant{{name: "[color=red]队员[/color]", playerID: 7}}, "怪物")
	if strings.Contains(notice, "[color=red]") || !strings.Contains(notice, "［color=red］队员［/color］(7)") {
		t.Fatalf("boss notice did not escape player UBB: %q", notice)
	}
}

func TestBossRefreshNoticeEscapesBossUBB(t *testing.T) {
	notice := bossRefreshNotice("[color=red]怪物[/color]")
	if strings.Contains(notice, "[color=red]") || !strings.Contains(notice, "［color=red］怪物［/color］") {
		t.Fatalf("refresh notice did not escape boss UBB: %q", notice)
	}
	if !strings.Contains(notice, "[color=#FF3333]") || !strings.Contains(notice, "[color=#28E828]") || !strings.Contains(notice, "已刷新") {
		t.Fatalf("refresh notice missing kill-style colors: %q", notice)
	}
}

func TestExpiredBossQueryAnnouncesRefreshLikeKill(t *testing.T) {
	loadOnlineTablesForTest(t)
	old := globalServer
	defer func() { globalServer = old }()
	worldBossMu.Lock()
	oldStates := worldBossStates
	worldBossStates = map[int32]*worldBossState{}
	worldBossMu.Unlock()
	defer func() {
		worldBossMu.Lock()
		worldBossStates = oldStates
		worldBossMu.Unlock()
	}()

	onLayer, onLayerConn := consistencyChannel(1, 1001001)
	mainCity, mainCityConn := consistencyChannel(2, 1000401)
	server := &Server{conns: map[int64]*channel{
		onLayer.id:  onLayer,
		mainCity.id: mainCity,
	}}
	globalServer = server

	markBossDead(1, "测试玩家")
	worldBossMu.Lock()
	worldBossStates[1].deadAt = time.Now().Add(-2 * time.Hour)
	worldBossMu.Unlock()

	if remaining := bossDeadRemaining(1); remaining != 0 {
		t.Fatalf("expired remaining=%v, want 0", remaining)
	}
	if remaining := bossDeadRemaining(1); remaining != 0 {
		t.Fatalf("second remaining=%v, want 0", remaining)
	}

	assertExpiredNotice := func(ch *channel, conn *recordingConn, wantRefresh bool) {
		t.Helper()
		frames := decodeRecordedFrames(t, conn.Bytes())
		wantOpcodes := []uint16{protocol.OpM2C_SendSystemChat}
		if wantRefresh {
			wantOpcodes = []uint16{protocol.OpM2C_BossRefresh, protocol.OpM2C_SendSystemChat}
		}
		if got := recordedOpcodes(t, conn.Bytes()); !reflect.DeepEqual(got, wantOpcodes) {
			t.Fatalf("player %d opcodes=%v, want %v", ch.id, got, wantOpcodes)
		}
		noticeFrame := frames[len(frames)-1]
		var notice protocol.M2C_SendSystemChat
		if err := proto.Unmarshal(noticeFrame.body, &notice); err != nil {
			t.Fatal(err)
		}
		content := bytesFields(t, noticeFrame.body, 1)
		if notice.Type != protocol.ChatType_System || notice.Name != systemNoticeSender ||
			!notice.IsSystemBrocast || notice.ActorId != ch.session.playerID || len(content) != 1 ||
			string(content[0]) != bossRefreshNotice(bossName(1)) {
			t.Fatalf("player %d expired notice=%+v content=%q", ch.id, &notice, content)
		}
	}

	assertExpiredNotice(onLayer, onLayerConn, true)
	assertExpiredNotice(mainCity, mainCityConn, false)
}
