package main

import (
	"path/filepath"
	"reflect"
	"testing"

	"mhqserver/protocol"
)

func featureTestSession(playerID int64) *session {
	ss := newSession()
	ss.playerID = playerID
	ss.jobID = 1
	ss.skinID = 1
	ss.level = 20
	ss.energy = 2000
	ss.tasks = make(map[int32]int32)
	ss.killCount = make(map[int32]int32)
	ss.bag = make(map[int32]*bagItem)
	ss.worn = make(map[int32]*bagItem)
	ss.signin = &signinState{}
	ss.signin.ensureTaskProgress()
	return ss
}

func withFeatureTables(t *testing.T, value *datatables) {
	t.Helper()
	previous := tables
	tables = value
	t.Cleanup(func() { tables = previous })
}

func TestRankingValueSupportsEveryClientIndex(t *testing.T) {
	withFeatureTables(t, &datatables{
		roleGrowth: map[int64]map[string]interface{}{},
		equipBase:  map[int64]map[string]interface{}{},
	})
	ss := featureTestSession(1)
	ss.coin = 1234
	ss.pet = &petState{Level: 9}
	ss.transBonus = map[int32]float32{
		1013: 13, 1014: 14, 1015: 15, 1016: 16,
		1017: 17, 1022: 22, 1023: 23,
	}

	for index := int32(0); index <= 15; index++ {
		if _, ok := rankingValue(ss, index); !ok {
			t.Fatalf("ranking index %d is not supported", index)
		}
	}
	for index, want := range map[int32]float32{
		1: 1234, 4: 9, 5: 17, 8: 13, 9: 14, 10: 15,
		11: 16, 12: 22, 13: 23,
	} {
		if got, _ := rankingValue(ss, index); got != want {
			t.Fatalf("ranking index %d value = %v, want %v", index, got, want)
		}
	}
	if _, ok := rankingValue(ss, 16); ok {
		t.Fatal("unsupported ranking index 16 was accepted")
	}
}

func TestRankingOrdersDescendingAndBreaksTiesByPlayerID(t *testing.T) {
	withFeatureTables(t, &datatables{
		roleGrowth:   map[int64]map[string]interface{}{},
		equipBase:    map[int64]map[string]interface{}{},
		goodsBase:    map[int64]map[string]interface{}{},
		materialBase: map[int64]map[string]interface{}{},
		skinBase:     map[int64]map[string]interface{}{1: {}},
	})
	store, err := OpenStore(filepath.Join(t.TempDir(), "ranking.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)

	for i, entry := range []struct {
		name  string
		level int32
	}{{"low", 10}, {"first-tie", 20}, {"second-tie", 20}} {
		accountID, createErr := store.CreateAccount(entry.name, "password")
		if createErr != nil {
			t.Fatal(createErr)
		}
		playerID, createErr := store.CreatePlayer(accountID, entry.name, 1, 1)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, updateErr := store.db.Exec(`UPDATE players SET level = ? WHERE id = ?`, entry.level, playerID); updateErr != nil {
			t.Fatal(updateErr)
		}
		if playerID != int64(i+1) {
			t.Fatalf("player id = %d, want %d", playerID, i+1)
		}
	}

	server := &Server{store: store, conns: make(map[int64]*channel)}
	response := server.onGetRanking(
		&channel{session: featureTestSession(99)},
		&protocol.C2M_GetRanking{Index: 0},
	).(*protocol.M2C_GetRanking)
	if response.Error != 0 {
		t.Fatalf("ranking error = %d, message = %q", response.Error, response.Message)
	}
	want := []string{"first-tie", "second-tie", "low"}
	if len(response.RankingInfoList) != len(want) {
		t.Fatalf("ranking count = %d, want %d", len(response.RankingInfoList), len(want))
	}
	for i, name := range want {
		if response.RankingInfoList[i].Name != name {
			t.Fatalf("ranking[%d] = %q, want %q", i, response.RankingInfoList[i].Name, name)
		}
	}
}

func TestQuizEnforcesQuestionStateAndRecordsTaskProgress(t *testing.T) {
	withFeatureTables(t, &datatables{
		questConfig: map[int64]map[string]interface{}{
			1: {"Answer": int64(2)},
			2: {"Answer": int64(1)},
		},
		taskBase: map[int64]map[string]interface{}{
			8001: {"_id": int64(8001), "TargetType": int64(taskTargetQuiz)},
		},
	})
	ss := featureTestSession(1)
	ss.tasks[8001] = taskStateRunning
	server := &Server{}
	ch := &channel{session: ss, conn: &recordingConn{}}

	started := server.onStartAnswerQuest(ch, &protocol.C2M_StartAnswerQuest{}).(*protocol.M2C_StartAnswerQuest)
	if started.Error != 0 || started.ConfigId != 1 {
		t.Fatalf("start response = %+v", started)
	}
	tooEarly := server.onNextAnswerQuest(ch, &protocol.C2M_NextAnswerQuest{}).(*protocol.M2C_NextAnswerQuest)
	if tooEarly.Error == 0 {
		t.Fatal("next question succeeded before answering")
	}
	answered := server.onAnswerQuest(ch, &protocol.C2M_AnswerQuest{Answer: 2}).(*protocol.M2C_AnswerQuest)
	if answered.Error != 0 || answered.Scord != 10 || answered.ContinueCorrectCount != 1 || answered.Time < 1 {
		t.Fatalf("answer response = %+v", answered)
	}
	if !ss.signin.TaskQuizProgress[8001] || !server.taskCompleted(ss, tables.taskBase[8001]) {
		t.Fatal("quiz task progress was not completed")
	}
	duplicate := server.onAnswerQuest(ch, &protocol.C2M_AnswerQuest{Answer: 2}).(*protocol.M2C_AnswerQuest)
	if duplicate.Error == 0 {
		t.Fatal("duplicate answer succeeded")
	}
	next := server.onNextAnswerQuest(ch, &protocol.C2M_NextAnswerQuest{}).(*protocol.M2C_NextAnswerQuest)
	if next.Error != 0 || next.ConfigId != 2 {
		t.Fatalf("next response = %+v", next)
	}
}

func TestPVPRequestAndBoardState(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "pvp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	for _, entry := range []struct {
		name  string
		score int32
	}{{"lower", 30}, {"higher", 80}} {
		accountID, createErr := store.CreateAccount(entry.name, "password")
		if createErr != nil {
			t.Fatal(createErr)
		}
		playerID, createErr := store.CreatePlayer(accountID, entry.name, 1, 1)
		if createErr != nil {
			t.Fatal(createErr)
		}
		state := &signinState{PVPScore: entry.score}
		if _, updateErr := store.db.Exec(`UPDATE players SET signin_json = ? WHERE id = ?`, signinToJSON(state), playerID); updateErr != nil {
			t.Fatal(updateErr)
		}
	}

	first := &channel{id: 1, session: featureTestSession(1)}
	second := &channel{id: 2, session: featureTestSession(2)}
	second.session.signin.PVPIsMatching = true
	server := &Server{store: store, conns: map[int64]*channel{1: first, 2: second}}
	requested := server.onRequestPersonalPvp(first, &protocol.C2M_RequestPersonalPvp{}).(*protocol.M2C_RequestPersonalPvp)
	if requested.Error != 0 || !first.session.signin.PVPIsMatching {
		t.Fatalf("request response = %+v, matching = %v", requested, first.session.signin.PVPIsMatching)
	}
	board := server.onGetPvpBoardInfo(first, &protocol.C2M_GetPvpBoardInfo{}).(*protocol.M2C_GetPvpBoardInfo)
	if board.Error != 0 || !board.IsMatch || board.MatchCount != 2 {
		t.Fatalf("board response = %+v", board)
	}
	if len(board.RankList) != 2 || board.RankList[0].Name != "higher" || board.RankList[1].Name != "lower" {
		t.Fatalf("unexpected PVP ranking: %+v", board.RankList)
	}
}

func TestTaskProgressTypesAndPersistence(t *testing.T) {
	withFeatureTables(t, &datatables{
		taskBase: map[int64]map[string]interface{}{
			1001: {"_id": int64(1001), "TargetType": int64(taskTargetKillSpecial), "CountTargetArr": []interface{}{map[string]interface{}{"CountTarget_Id": int64(501), "CountTarget_Count": int64(2)}}},
			1002: {"_id": int64(1002), "TargetType": int64(taskTargetKillSpecial), "CountTargetArr": []interface{}{map[string]interface{}{"CountTarget_Id": int64(501), "CountTarget_Count": int64(1)}}},
			1003: {"_id": int64(1003), "TargetType": int64(taskTargetKillAny), "AnyTargetCount": int64(2), "AnyTargetLevel": int64(5)},
			1004: {"_id": int64(1004), "TargetType": int64(taskTargetDialog), "DialogTargetId": int64(700)},
			1005: {"_id": int64(1005), "TargetType": int64(taskTargetChangeMap), "TargetSceneId": int64(10009), "TargetMaplayer": int64(2)},
		},
		monsterBase: map[int64]map[string]interface{}{
			501: {"Level": int64(16)},
			502: {"Level": int64(14)},
		},
		trialCopy: map[int64]map[string]interface{}{
			2: {"MapId": int64(1000902)},
		},
	})
	ss := featureTestSession(1)
	for id := int32(1001); id <= 1005; id++ {
		ss.tasks[id] = taskStateRunning
	}
	server := &Server{}

	server.recordTaskMonsterKills(ss, []*monsterUnit{{monsterID: 501, alive: false}})
	if got := taskKillCount(ss, 1001, 501); got != 1 {
		t.Fatalf("task 1001 kill count = %d, want 1", got)
	}
	if got := taskKillCount(ss, 1002, 501); got != 1 {
		t.Fatalf("task 1002 kill count = %d, want 1", got)
	}
	if server.taskCompleted(ss, tables.taskBase[1001]) || !server.taskCompleted(ss, tables.taskBase[1002]) {
		t.Fatal("special-kill task completion is not isolated by task id")
	}
	if got := taskAnyKillCount(ss, 1003); got != 1 {
		t.Fatalf("eligible monster any-kill count = %d, want 1", got)
	}
	server.recordTaskMonsterKills(ss, []*monsterUnit{{monsterID: 502, alive: false}})
	if got := taskAnyKillCount(ss, 1003); got != 1 {
		t.Fatalf("low-level monster counted for any-kill task: %d", got)
	}
	if server.taskCompleted(ss, tables.taskBase[1004]) {
		t.Fatal("dialog task completed before the target NPC was clicked")
	}
	if !server.markDialogTaskProgress(ss, 700) || !server.taskCompleted(ss, tables.taskBase[1004]) {
		t.Fatal("dialog task did not complete after the target NPC was clicked")
	}
	ss.signin.TrialHighestID = 2
	if !server.taskCompleted(ss, tables.taskBase[1005]) {
		t.Fatal("trial-layer task did not complete at its configured layer")
	}

	restored := signinFromJSON(signinToJSON(ss.signin))
	if restored == nil || restored.TaskKillProgress[1001][501] != 1 || !restored.TaskDialogProgress[1004] || restored.TrialHighestID != 2 {
		t.Fatalf("task progress persistence mismatch: %+v", restored)
	}
}

func TestTaskMaterialCompletionIsAtomicWhenBagIsFull(t *testing.T) {
	withFeatureTables(t, &datatables{
		taskBase: map[int64]map[string]interface{}{},
		equipBase: map[int64]map[string]interface{}{
			9001: {"Type": int64(0)},
			9002: {"Type": int64(1)},
		},
		goodsBase: map[int64]map[string]interface{}{},
		materialBase: map[int64]map[string]interface{}{
			8001: {},
		},
	})
	ss := featureTestSession(1)
	ss.bag[0] = &bagItem{ItemId: 8001, ItemType: int32(protocol.ItemType_MaterialsItem), Count: 1}
	for slot := int32(1); slot < bagSlotCount; slot++ {
		ss.bag[slot] = &bagItem{ItemId: 9001, ItemType: int32(protocol.ItemType_EquipItem), Count: 1}
	}
	submit := map[string]interface{}{
		"TargetType":     int64(taskTargetSubmit),
		"CountTargetArr": []interface{}{map[string]interface{}{"CountTarget_Id": int64(8001), "CountTarget_Count": int64(1)}},
		"RewardArr":      []interface{}{map[string]interface{}{"Reward_Id": int64(9002), "Reward_Count": int64(1)}},
	}
	staged, _, message := stageTaskCompletion(ss, submit)
	if message != "" {
		t.Fatalf("completion that frees one slot failed: %q", message)
	}
	if got := bagItemCount(&session{bag: staged}, 8001); got != 0 {
		t.Fatalf("submitted material count = %d, want 0", got)
	}
	if got := bagItemCount(&session{bag: staged}, 9002); got != 1 {
		t.Fatalf("reward count = %d, want 1", got)
	}

	full := featureTestSession(2)
	for slot := int32(0); slot < bagSlotCount; slot++ {
		full.bag[slot] = &bagItem{ItemId: 9001, ItemType: int32(protocol.ItemType_EquipItem), Count: 1}
	}
	before := cloneBagMap(full.bag)
	nonMaterial := map[string]interface{}{
		"TargetType": int64(taskTargetLevel),
		"RewardArr":  []interface{}{map[string]interface{}{"Reward_Id": int64(9002), "Reward_Count": int64(1)}},
	}
	if _, _, message = stageTaskCompletion(full, nonMaterial); message == "" {
		t.Fatal("reward unexpectedly fit in a full bag")
	}
	if !reflect.DeepEqual(full.bag, before) {
		t.Fatal("failed task reward mutated the original bag")
	}
}
