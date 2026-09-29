package main

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/internal/operationsconfig"
	"mhqserver/protocol"
)

const testMapCoinReward = int64(3_508_647)

func mapCoinTestContext(t *testing.T) (*Server, *session, *channel, *recordingConn) {
	t.Helper()
	loadOnlineTablesForTest(t)
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.MapCoin.SpawnChancePercent = 100
		config.MapCoin.MinimumReward = testMapCoinReward
		config.MapCoin.MaximumReward = testMapCoinReward
		config.MapCoin.DailyBaseCount = 1
		config.MapCoin.ExtraCountVoucher = 20
	})
	ss := newSession()
	ss.state, ss.playerID, ss.mapID = sessInGame, 7, 10004
	ss.coin, ss.voucher = 100, 40
	conn := &recordingConn{}
	ch := &channel{id: 1, conn: conn, session: ss}
	server := &Server{mapCoinRoll: func() float64 { return 0 }}
	return server, ss, ch, conn
}

func TestMapCoinEncounterUsesNativeCreateClaimAndDisposePackets(t *testing.T) {
	server, ss, ch, conn := mapCoinTestContext(t)
	if !server.pushMapCoinMonster(ch) || ss.mapCoinUnitID == 0 {
		t.Fatal("map entry did not spawn the configured map coin encounter")
	}
	unitID := ss.mapCoinUnitID
	frames := decodeRecordedFrames(t, conn.Bytes())
	if len(frames) != 1 || frames[0].opcode != protocol.OpM2C_CreateMapMonster {
		t.Fatalf("spawn opcodes=%v", recordedOpcodes(t, conn.Bytes()))
	}
	var created protocol.M2C_CreateMapMonster
	if err := proto.Unmarshal(frames[0].body, &created); err != nil {
		t.Fatal(err)
	}
	if created.Id != unitID || created.ConfigId != mapCoinMonsterConfigID ||
		created.MapMonsterType != mapMonsterTypeMapCoin || created.X != 0 || created.Y != -1.2 {
		t.Fatalf("created map coin monster=%+v", &created)
	}

	response := server.onGetMapCoin(ch, &protocol.C2M_GetMapCoin{RpcId: 1}).(*protocol.M2C_GetMapCoin)
	if response.Error != 0 || response.Message != "" || ss.coin != 100+testMapCoinReward ||
		ss.signin.MapCoinClaimed != 1 || ss.mapCoinUnitID != 0 {
		t.Fatalf("map coin response=%+v state=%+v coin=%d unit=%d", response, ss.signin, ss.coin, ss.mapCoinUnitID)
	}
	frames = decodeRecordedFrames(t, conn.Bytes())
	want := []uint16{
		protocol.OpM2C_CreateMapMonster,
		protocol.OpM2C_SyncUnitAttribute,
		protocol.OpM2C_SendTip,
		protocol.OpM2C_DisposeMapMonster,
	}
	got := recordedOpcodes(t, conn.Bytes())
	if len(got) != len(want) {
		t.Fatalf("claim opcodes=%v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("claim opcodes=%v, want %v", got, want)
		}
	}
	var numeric protocol.M2C_SyncUnitAttribute
	if err := proto.Unmarshal(frames[1].body, &numeric); err != nil || numeric.NumericType != ntCoin ||
		numeric.UnitId != ss.playerID || int64(numeric.Value) != ss.coin {
		t.Fatalf("coin numeric=%+v err=%v", &numeric, err)
	}
	var tip protocol.M2C_SendTip
	if err := proto.Unmarshal(frames[2].body, &tip); err != nil || tip.Message != "恭喜你获得3508647铜币" {
		t.Fatalf("map coin tip=%+v err=%v", &tip, err)
	}
	var disposed protocol.M2C_DisposeMapMonster
	if err := proto.Unmarshal(frames[3].body, &disposed); err != nil || disposed.Id != unitID {
		t.Fatalf("disposed map coin=%+v err=%v", &disposed, err)
	}
}

func TestMapCoinDailyLimitAndPurchasedCount(t *testing.T) {
	server, ss, ch, _ := mapCoinTestContext(t)
	if !server.pushMapCoinMonster(ch) {
		t.Fatal("first map coin did not spawn")
	}
	first := server.onGetMapCoin(ch, &protocol.C2M_GetMapCoin{RpcId: 1}).(*protocol.M2C_GetMapCoin)
	if first.Error != 0 || first.Message != "" || ss.coin != 100+testMapCoinReward {
		t.Fatalf("first map coin=%+v coin=%d", first, ss.coin)
	}
	if server.pushMapCoinMonster(ch) {
		t.Fatal("map coin spawned after daily count was exhausted")
	}
	forged := server.onGetMapCoin(ch, &protocol.C2M_GetMapCoin{RpcId: 2}).(*protocol.M2C_GetMapCoin)
	if forged.Error != 0 || !strings.Contains(forged.Message, "失效") || ss.coin != 100+testMapCoinReward {
		t.Fatalf("forged map coin=%+v coin=%d", forged, ss.coin)
	}
	added := server.onAddMapCoinCount(ch, &protocol.C2M_AddMapCoinCount{RpcId: 3}).(*protocol.M2C_AddMapCoinCount)
	if added.Error != 0 || added.Message != "" || ss.voucher != 20 || ss.signin.MapCoinExtra != 1 {
		t.Fatalf("add count=%+v state=%+v voucher=%d", added, ss.signin, ss.voucher)
	}
	if !server.pushMapCoinMonster(ch) {
		t.Fatal("purchased map coin did not spawn")
	}
	second := server.onGetMapCoin(ch, &protocol.C2M_GetMapCoin{RpcId: 4}).(*protocol.M2C_GetMapCoin)
	if second.Error != 0 || second.Message != "" || ss.coin != 100+2*testMapCoinReward || ss.signin.MapCoinClaimed != 2 {
		t.Fatalf("purchased map coin=%+v state=%+v coin=%d", second, ss.signin, ss.coin)
	}
}

func TestMapCoinPurchaseRejectsWithoutRPCErrorAndResetsNextDay(t *testing.T) {
	server, ss, ch, _ := mapCoinTestContext(t)
	ss.voucher = 19
	ss.signin = &signinState{
		MapCoinDay:   time.Now().Add(-24 * time.Hour).Format("20060102"),
		MapCoinExtra: 9, MapCoinClaimed: 9,
	}

	rejected := server.onAddMapCoinCount(ch, &protocol.C2M_AddMapCoinCount{RpcId: 5}).(*protocol.M2C_AddMapCoinCount)
	if rejected.Error != 0 || !strings.Contains(rejected.Message, "20") || ss.voucher != 19 {
		t.Fatalf("insufficient voucher response=%+v voucher=%d", rejected, ss.voucher)
	}
	if ss.signin.MapCoinExtra != 0 || ss.signin.MapCoinClaimed != 0 ||
		ss.signin.MapCoinDay != time.Now().Format("20060102") {
		t.Fatalf("daily reset state=%+v", ss.signin)
	}
}

func TestMapCoinSpawnChanceCanDisableEncounter(t *testing.T) {
	server, ss, ch, _ := mapCoinTestContext(t)
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.MapCoin.SpawnChancePercent = 0
	})
	if server.pushMapCoinMonster(ch) || ss.mapCoinUnitID != 0 {
		t.Fatal("zero spawn chance created a map coin monster")
	}
}

func TestMapStartupDoesNotHoldTransitionLockWhileCreatingMapCoin(t *testing.T) {
	server, _, first, _ := mapCoinTestContext(t)
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.MapCoin.SpawnChancePercent = 50
	})
	enteredRoll := make(chan struct{})
	releaseRoll := make(chan struct{})
	server.mapCoinRoll = func() float64 {
		close(enteredRoll)
		<-releaseRoll
		return 1
	}
	secondSession := featureTestSession(8)
	secondSession.state, secondSession.mapID = sessInGame, 1000601
	second := &channel{id: 2, conn: &recordingConn{}, session: secondSession}
	server.conns = map[int64]*channel{first.id: first, second.id: second}
	cancelReturnTestMapStartup(t, first, second)

	seq := server.changeMap(first, 10004, -12, -1)
	startupDone := make(chan bool, 1)
	go func() { startupDone <- server.finishMapStartup(first, 10004, seq, false) }()
	select {
	case <-enteredRoll:
	case <-time.After(time.Second):
		t.Fatal("map startup did not reach map-coin creation")
	}

	transitionDone := make(chan struct{})
	go func() {
		server.changeMap(second, 10004, -12, -1)
		close(transitionDone)
	}()
	transitionBlocked := false
	select {
	case <-transitionDone:
	case <-time.After(200 * time.Millisecond):
		transitionBlocked = true
	}
	close(releaseRoll)
	select {
	case started := <-startupDone:
		if !started {
			t.Fatal("map startup was rejected")
		}
	case <-time.After(time.Second):
		t.Fatal("map startup did not finish after map-coin roll resumed")
	}
	if transitionBlocked {
		select {
		case <-transitionDone:
		case <-time.After(time.Second):
			t.Fatal("second map transition remained blocked")
		}
		t.Fatal("map startup held mapTransitionMu while waiting in map-coin creation")
	}
}
