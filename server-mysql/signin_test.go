package main

import (
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	"mhqserver/protocol"
)

func signinTestCurrentRow(t *testing.T) (map[string]interface{}, int32) {
	t.Helper()
	row, id, ok := signinDailyRewardRow(time.Now().UTC())
	if !ok {
		t.Fatal("current daily sign-in template is missing")
	}
	return row, id
}

func signinBagCount(ss *session, itemID int32) int32 {
	var count int32
	for _, item := range ss.bag {
		if item != nil && item.ItemId == itemID {
			count += item.Count
		}
	}
	return count
}

func frameVarints(t *testing.T, body []byte, field protowire.Number) []uint64 {
	t.Helper()
	var values []uint64
	for len(body) > 0 {
		number, wireType, n := protowire.ConsumeTag(body)
		if n < 0 {
			t.Fatalf("invalid protobuf tag: %v", protowire.ParseError(n))
		}
		body = body[n:]
		if wireType == protowire.VarintType {
			value, valueLen := protowire.ConsumeVarint(body)
			if valueLen < 0 {
				t.Fatalf("invalid protobuf varint: %v", protowire.ParseError(valueLen))
			}
			if number == field {
				values = append(values, value)
			}
			body = body[valueLen:]
			continue
		}
		valueLen := protowire.ConsumeFieldValue(number, wireType, body)
		if valueLen < 0 {
			t.Fatalf("invalid protobuf field: %v", protowire.ParseError(valueLen))
		}
		body = body[valueLen:]
	}
	return values
}

func lastActiveInfoFrame(t *testing.T, conn *recordingConn) recordedFrame {
	t.Helper()
	frames := decodeRecordedFrames(t, conn.Bytes())
	for index := len(frames) - 1; index >= 0; index-- {
		if frames[index].opcode == protocol.OpM2C_SendActiveInfo {
			return frames[index]
		}
	}
	t.Fatal("M2C_SendActiveInfo was not emitted")
	return recordedFrame{}
}

func TestSigninDailyTemplateUsesNewestOnlineSameDay(t *testing.T) {
	loadOnlineTablesForTest(t)
	row, id, ok := signinDailyRewardRow(time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC))
	if !ok || id != 2566 {
		t.Fatalf("2026-08-18 template id=%d ok=%v, want online 2025 id 2566", id, ok)
	}
	grants := signinGrants(row)
	if len(grants) != 1 || grants[0].itemID != 110204 || grants[0].count != 30 {
		t.Fatalf("mapped online grants=%+v, want 110204 x30", grants)
	}
}

func TestSigninDailyRewardValidatesIDAndIsAtomic(t *testing.T) {
	loadOnlineTablesForTest(t)
	server, ss, ch := activityTestChannel(t, 7201)
	row, currentID := signinTestCurrentRow(t)
	grants := signinGrants(row)
	if len(grants) == 0 {
		t.Fatal("current online sign-in row has no rewards")
	}

	invalid := server.onGetSigninReward(ch, &protocol.C2M_GetSigninReward{
		RpcId: 1, ConfigId: currentID + 1,
	}).(*protocol.M2C_GetSigninReward)
	if invalid.Message == "" || ss.signin.LastDay != "" || len(ss.bag) != 0 {
		t.Fatalf("invalid id response=%+v state=%+v bag=%v", invalid, ss.signin, ss.bag)
	}

	response := server.onGetSigninReward(ch, &protocol.C2M_GetSigninReward{
		RpcId: 2, ConfigId: currentID,
	}).(*protocol.M2C_GetSigninReward)
	if response.Message != "" || response.Error != 0 {
		t.Fatalf("valid daily sign-in response=%+v", response)
	}
	for _, grant := range grants {
		if got := signinBagCount(ss, grant.itemID); got != grant.count {
			t.Fatalf("reward item %d count=%d, want %d", grant.itemID, got, grant.count)
		}
	}
	if ss.signin.LastDay != time.Now().UTC().Format("20060102") || ss.signin.MonthCount != 1 {
		t.Fatalf("daily state=%+v", ss.signin)
	}

	repeated := server.onGetSigninReward(ch, &protocol.C2M_GetSigninReward{
		RpcId: 3, ConfigId: currentID,
	}).(*protocol.M2C_GetSigninReward)
	if repeated.Message == "" || ss.signin.MonthCount != 1 {
		t.Fatalf("duplicate response=%+v state=%+v", repeated, ss.signin)
	}

	_, full, fullChannel := activityTestChannel(t, 7202)
	for index := int32(0); index < bagSlotCount; index++ {
		full.bag[index] = &bagItem{ItemId: 900000 + index, ItemType: int32(protocol.ItemType_EquipItem), Count: 1}
	}
	fullResponse := server.onGetSigninReward(fullChannel, &protocol.C2M_GetSigninReward{
		RpcId: 4, ConfigId: currentID,
	}).(*protocol.M2C_GetSigninReward)
	if fullResponse.Message != "背包已满" || full.signin.LastDay != "" || full.signin.MonthCount != 0 {
		t.Fatalf("full-bag response=%+v state=%+v", fullResponse, full.signin)
	}
}

func TestSigninMonthStateUsesTimesAndResetsAcrossMonth(t *testing.T) {
	loadOnlineTablesForTest(t)
	server, ss, ch := activityTestChannel(t, 7203)
	now := time.Now().UTC()
	var configID int32
	var rewardRow map[string]interface{}
	for _, row := range tables.signInRewardMonth {
		candidate := int32(num(row["_id"]))
		if mapped, ok := signinMonthRewardRow(now, candidate); ok {
			configID, rewardRow = candidate, mapped
			break
		}
	}
	if configID == 0 {
		t.Fatal("current month has no online cumulative sign-in template")
	}
	ss.signin = &signinState{
		LastMonth:     now.Format("200601"),
		MonthCount:    31,
		MonthGotMonth: now.Format("200601"),
	}
	response := server.onGetSigninInMonthReward(ch, &protocol.C2M_GetSigninInMonthReward{
		RpcId: 5, ConfigId: configID,
	}).(*protocol.M2C_GetSigninInMonthReward)
	if response.Message != "" || response.Error != 0 {
		t.Fatalf("cumulative sign-in response=%+v", response)
	}
	wantTimes := int32(num(rewardRow["Times"]))
	active := lastActiveInfoFrame(t, ch.conn.(*recordingConn))
	gotTimes := frameVarints(t, active.body, 3)
	if len(gotTimes) != 1 || gotTimes[0] != uint64(wantTimes) {
		t.Fatalf("active tag 3=%v, want Times=%d (not config id %d)", gotTimes, wantTimes, configID)
	}

	resetServer, reset, resetChannel := activityTestChannel(t, 7204)
	reset.signin = &signinState{
		LastMonth: "199901", MonthCount: 21,
		MonthGotMonth: "199901", MonthGotIDs: []int32{configID},
	}
	resetServer.pushActiveInfo(resetChannel)
	if reset.signin.LastMonth != now.Format("200601") || reset.signin.MonthCount != 0 ||
		reset.signin.MonthGotMonth != now.Format("200601") || len(reset.signin.MonthGotIDs) != 0 {
		t.Fatalf("cross-month state was not reset: %+v", reset.signin)
	}
}

func TestSigninPersistsThroughMySQLReload(t *testing.T) {
	loadOnlineTablesForTest(t)
	store, err := OpenStore(mysqlTestDSN(t, filepath.Join(t.TempDir(), "signin.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	accountID, err := store.CreateAccount("signin_persist", "123456")
	if err != nil {
		t.Fatal(err)
	}
	playerID, err := store.CreatePlayer(accountID, "signin", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	server, ss, ch := activityTestChannel(t, playerID)
	server.store = store
	_, currentID := signinTestCurrentRow(t)
	response := server.onGetSigninReward(ch, &protocol.C2M_GetSigninReward{
		RpcId: 6, ConfigId: currentID,
	}).(*protocol.M2C_GetSigninReward)
	if response.Message != "" {
		t.Fatalf("sign-in response=%+v", response)
	}
	player, err := store.FirstPlayer(accountID)
	if err != nil {
		t.Fatal(err)
	}
	restored := player.Relations.signin
	if restored == nil || restored.LastDay != ss.signin.LastDay || restored.MonthCount != 1 {
		t.Fatalf("restored sign-in=%+v, live=%+v", restored, ss.signin)
	}
}
