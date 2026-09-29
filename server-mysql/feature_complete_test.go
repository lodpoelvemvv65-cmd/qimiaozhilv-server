package main

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/internal/operationsconfig"
	"mhqserver/protocol"
)

func featureTestSession(playerID int64) *session {
	ss := newSession()
	ss.playerID = playerID
	ss.jobID = 1
	ss.skinID = 1
	ss.level = 20
	ss.phyAdd, ss.spiAdd = 1, 1
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

func withGameplayConfigForTest(t *testing.T, mutate func(*operationsconfig.Config)) {
	t.Helper()
	previous := gameplayConfigSnapshot()
	config := operationsconfig.Defaults()
	mutate(&config)
	if err := operationsconfig.Validate(config); err != nil {
		t.Fatalf("invalid test Gameplay config: %v", err)
	}
	storeGameplayConfig(config)
	t.Cleanup(func() { storeGameplayConfig(previous) })
}

func TestGameplayDefaultsMatchClientExpectations(t *testing.T) {
	config := operationsconfig.Defaults()
	if config.Quiz.QuestionsPerRound != 10 || config.Pet.QuickEndVoucher != 10 ||
		config.Market.MinimumPercent != 85 || config.Market.MaximumPercent != 125 ||
		config.Dungeon.SpaceTravelDailyAttempts != 50 || config.Dungeon.DeathTowerDailyAttempts != 10 ||
		config.Dungeon.FamilyBossDailyKeys != 2 || config.Dungeon.TimezoneOffsetHours != 8 ||
		config.Progression.ExperienceGainPercent != 200 ||
		config.Progression.SkillPointLevels != 200 ||
		config.Combat.SkillMPCostPercent != 50 || config.Combat.EffectMaxStacks != 10 ||
		config.Combat.EffectTickIntervalMS != 4000 ||
		config.Combat.PeriodicTickOverridesMS["50001033"] != 3500 ||
		config.FamilyBoss.AttributeBaseMultiplier != 1.20 ||
		config.FamilyBoss.AttributeStackIntervalMS != 4710 ||
		config.HardMainStory.NormalRewardMultiplier != 3 ||
		config.PersonalPVP.VictoryScoreDelta != 10 || config.PersonalPVP.DefeatScoreDelta != -5 ||
		config.PersonalPVP.TimezoneOffsetHours != 8 {
		t.Fatalf("unexpected Gameplay defaults: %+v", config)
	}
}

func TestAttributePointsCanBeAllocatedAndResetRepeatedly(t *testing.T) {
	ss := featureTestSession(1)
	// 加点断言以零点为基准，先清掉会话默认的体力/精神加点。
	ss.phyAdd, ss.spiAdd = 0, 0
	ss.charPoint = 21
	server := &Server{}
	ch := &channel{id: 1, session: ss, conn: &recordingConn{}}
	added := server.onAddPoint(ch, &protocol.C2M_AddPoint{PointList: []int32{1, 2, 3, 4, 5, 6}}).(*protocol.M2C_AddPoint)
	if added.Error != 0 || added.Message != "" || ss.charPoint != 0 ||
		ss.strAdd != 1 || ss.wimAdd != 2 || ss.phyAdd != 3 ||
		ss.staAdd != 4 || ss.qukAdd != 5 || ss.spiAdd != 6 {
		t.Fatalf("add point result=%+v point=%d attrs=%d/%d/%d/%d/%d/%d",
			added, ss.charPoint, ss.strAdd, ss.wimAdd, ss.phyAdd, ss.staAdd, ss.qukAdd, ss.spiAdd)
	}
	for attempt := 0; attempt < 2; attempt++ {
		reset := server.onResetPoint(ch, &protocol.C2M_ResetPoint{}).(*protocol.M2C_ResetPoint)
		if reset.Error != 0 || reset.Message != "" || ss.charPoint != 21 ||
			ss.strAdd != 0 || ss.wimAdd != 0 || ss.phyAdd != 0 ||
			ss.staAdd != 0 || ss.qukAdd != 0 || ss.spiAdd != 0 {
			t.Fatalf("reset attempt %d result=%+v point=%d attrs=%d/%d/%d/%d/%d/%d",
				attempt+1, reset, ss.charPoint, ss.strAdd, ss.wimAdd, ss.phyAdd, ss.staAdd, ss.qukAdd, ss.spiAdd)
		}
	}
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
	store, err := OpenStore(mysqlTestDSN(t, filepath.Join(t.TempDir(), "ranking.db")))
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

func TestLargeRankingSnapshotMatchesFullRelations(t *testing.T) {
	withFeatureTables(t, &datatables{
		roleGrowth:      map[int64]map[string]interface{}{1: {}},
		characterGrowth: map[int64]map[string]interface{}{},
		equipBase: map[int64]map[string]interface{}{
			120001: {"PhyAtk": float64(100), "SpecialKey": float64(7), "SpecialValue": float64(10)},
		},
		strengthen: map[int64]map[string]interface{}{
			2: {"AttribteAdd": 0.5},
		},
		manulEquipAttribute: map[int64]map[string]interface{}{
			7001: {"Key": float64(7), "Value": float64(30)},
		},
		equipAffix: map[int64]map[string]interface{}{
			8001: {"AffixArr": []interface{}{map[string]interface{}{"Key": float64(7), "Value": float64(40)}}},
		},
		materialBase: map[int64]map[string]interface{}{
			9001: {"GemKey": float64(7), "GemValue": float64(50)},
		},
		starSoulAttribute: map[int64]map[string]interface{}{
			1101: {"Key": float64(7), "Value": float64(60)},
			1102: {"Key": float64(7), "Value": float64(70)},
		},
		skinBase:          map[int64]map[string]interface{}{1: {}},
		transmigrationAdd: map[int64]map[string]interface{}{},
	})
	store, err := OpenStore(mysqlTestDSN(t, filepath.Join(t.TempDir(), "large-ranking.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)

	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO players (account_id, name, job_id, skin_id, level) VALUES (?, ?, 1, 1, 20)`)
	if err != nil {
		t.Fatal(err)
	}
	var equippedID int64
	for i := 0; i < 501; i++ {
		name := "plain"
		if i == 500 {
			name = "equipped"
		}
		result, insertErr := stmt.Exec(i+1, name)
		if insertErr != nil {
			t.Fatal(insertErr)
		}
		if i == 500 {
			equippedID, err = result.LastInsertId()
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := stmt.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO player_items
		(player_id, location, slot_index, item_id, item_type, server_id, item_count, is_locked,
		 quality, star, strength_level, special_key, special_id, get_source)
		VALUES (?, ?, 0, 120001, 1, 1001, 1, 0, 4, 5, 2, 7, 0, 'ranking-test')`,
		equippedID, itemLocationWorn); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO player_item_main_attributes (player_id, location, slot_index, attribute_type, value) VALUES (?, ?, 0, 7, 0.2)`,
		`INSERT INTO player_item_random_attributes (player_id, location, slot_index, position, attribute_id) VALUES (?, ?, 0, 0, 7001)`,
		`INSERT INTO player_item_affixes (player_id, location, slot_index, position, affix_id) VALUES (?, ?, 0, 0, 8001)`,
		`INSERT INTO player_item_gems (player_id, location, slot_index, position, gem_item_id) VALUES (?, ?, 0, 0, 9001)`,
	} {
		if _, err := tx.Exec(statement, equippedID, itemLocationWorn); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO player_star_souls
		(player_id, star_soul_id, type_id, level, exp, pos_type, quality, main_attribute, is_locked)
		VALUES (?, 91001, 1, 0, 0, 0, 1, 1101, 0)`, equippedID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO player_star_soul_vice_attributes
		(player_id, star_soul_id, position, attribute_type, attribute_add) VALUES (?, 91001, 0, 1102, 0)`, equippedID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO player_star_soul_slots (player_id, slot_index, star_soul_id) VALUES (?, 0, 91001)`, equippedID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	full, err := store.FirstPlayer(501)
	if err != nil {
		t.Fatal(err)
	}
	players, err := store.ListPlayersForRanking(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(players) != 501 {
		t.Fatalf("ranking snapshot players = %d, want 501", len(players))
	}
	var snapshot *Player
	for _, player := range players {
		if player.ID == equippedID {
			snapshot = player
			break
		}
	}
	if snapshot == nil {
		t.Fatal("equipped player missing from ranking snapshot")
	}
	fullSession, snapshotSession := newSession(), newSession()
	fullSession.loadData(full)
	snapshotSession.loadData(snapshot)
	for index := int32(0); index <= 15; index++ {
		want, _ := rankingValue(fullSession, index)
		got, _ := rankingValue(snapshotSession, index)
		if got != want {
			t.Fatalf("ranking index %d snapshot value = %v, full value = %v", index, got, want)
		}
	}

	server := &Server{store: store, conns: make(map[int64]*channel)}
	response := server.onGetRanking(&channel{session: featureTestSession(9999)},
		&protocol.C2M_GetRanking{Index: 6}).(*protocol.M2C_GetRanking)
	if response.Error != 0 {
		t.Fatalf("large ranking error = %d, message = %q", response.Error, response.Message)
	}
	if len(response.RankingInfoList) != 100 || response.RankingInfoList[0].Name != "equipped" {
		t.Fatalf("large ranking did not preserve equipment/star-soul value: first=%+v count=%d",
			response.RankingInfoList[0], len(response.RankingInfoList))
	}
}

func TestQuizEnforcesQuestionStateAndRecordsTaskProgress(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.Quiz.QuestionsPerRound = 3
	})
	questionCount := gameplayQuizQuestionCount()
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
	for answerIndex := int32(1); answerIndex <= questionCount; answerIndex++ {
		answer := int32(2)
		if ss.quizQuestionID == 2 {
			answer = 1
		}
		answered := server.onAnswerQuest(ch, &protocol.C2M_AnswerQuest{Answer: answer}).(*protocol.M2C_AnswerQuest)
		if answered.Error != 0 || answered.Scord != 10 || answered.ContinueCorrectCount != answerIndex || answered.Time < 1 {
			t.Fatalf("answer %d response = %+v", answerIndex, answered)
		}
		completed := ss.signin.TaskQuizProgress[8001] || server.taskCompleted(ss, tables.taskBase[8001])
		if answerIndex < questionCount && completed {
			t.Fatalf("quiz task completed after only %d answers", answerIndex)
		}
		if answerIndex == questionCount {
			if !ss.signin.QuizCompleted || !ss.signin.TaskQuizProgress[8001] || !server.taskCompleted(ss, tables.taskBase[8001]) {
				t.Fatal("quiz task did not complete after configured answer count")
			}
			break
		}
		duplicate := server.onAnswerQuest(ch, &protocol.C2M_AnswerQuest{Answer: answer}).(*protocol.M2C_AnswerQuest)
		if duplicate.Error == 0 {
			t.Fatalf("duplicate answer %d succeeded", answerIndex)
		}
		next := server.onNextAnswerQuest(ch, &protocol.C2M_NextAnswerQuest{}).(*protocol.M2C_NextAnswerQuest)
		wantQuestion := int32(1)
		if answerIndex%2 == 1 {
			wantQuestion = 2
		}
		if next.Error != 0 || next.ConfigId != wantQuestion {
			t.Fatalf("next response after answer %d = %+v", answerIndex, next)
		}
	}
	if ss.quizAnswerCount != questionCount {
		t.Fatalf("quiz answer count = %d, want %d", ss.quizAnswerCount, questionCount)
	}
	ss.quizRunScore = 30
	ss.quizRunTimeMS = 123
	// 答满配置题数后必须结束本轮：客户端只认「Message 非空」，且 Error 必须为 0，
	// 否则 Session.Call 抛异常、界面卡在最后一题。
	ended := server.onNextAnswerQuest(ch, &protocol.C2M_NextAnswerQuest{}).(*protocol.M2C_NextAnswerQuest)
	if ended.Error != 0 || ended.Message == "" || ended.ConfigId != 0 {
		t.Fatalf("next question after a finished round = %+v", ended)
	}
	if ss.quizAnswerCount != questionCount || !ss.quizAnswered {
		t.Fatalf("finished round mutated transient state: count=%d answered=%v",
			ss.quizAnswerCount, ss.quizAnswered)
	}
	stray := server.onAnswerQuest(ch, &protocol.C2M_AnswerQuest{Answer: 1}).(*protocol.M2C_AnswerQuest)
	if stray.Error != 0 || stray.Message != ended.Message {
		t.Fatalf("answer after a finished round = %+v, want the same end message", stray)
	}
	if ss.quizAnswerCount != questionCount || ss.quizRunScore != 30 {
		t.Fatalf("answer after a finished round was scored: count=%d score=%d", ss.quizAnswerCount, ss.quizRunScore)
	}
	// 重新开始一局仍然正常：resetQuizRound 只由 20304 触发。
	restarted := server.onStartAnswerQuest(ch, &protocol.C2M_StartAnswerQuest{}).(*protocol.M2C_StartAnswerQuest)
	if restarted.Error != 0 || restarted.ConfigId != 1 {
		t.Fatalf("restart response = %+v", restarted)
	}
	if ss.quizAnswerCount != 0 || ss.quizRunScore != 0 || ss.quizRunTimeMS != 0 || ss.quizStreak != 0 || ss.quizAnswered {
		t.Fatalf("new quiz round did not reset transient state: count=%d score=%d time=%d streak=%d answered=%v",
			ss.quizAnswerCount, ss.quizRunScore, ss.quizRunTimeMS, ss.quizStreak, ss.quizAnswered)
	}
	firstNextRound := server.onAnswerQuest(ch, &protocol.C2M_AnswerQuest{Answer: 1}).(*protocol.M2C_AnswerQuest)
	if firstNextRound.Error != 0 || ss.quizAnswerCount != 1 {
		t.Fatalf("first answer in next round failed: response=%+v count=%d", firstNextRound, ss.quizAnswerCount)
	}
}

func TestPetActionsUseConfiguredDurationAndReturnMilliseconds(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.Pet.DurationTiers = nil
		config.Pet.ExploreDurationMS = 123_000
		config.Pet.PlayDurationMS = 124_000
		config.Pet.ExperienceDurationMS = 125_000
		config.Pet.QuickEndVoucher = 7
	})
	withFeatureTables(t, &datatables{
		petExploreConfig: map[int64]map[string]interface{}{
			10001: {"Type": int64(petExploreTypeExplore), "Time": int64(600_000)},
			10003: {"Type": int64(petExploreTypeExplore), "Time": int64(3_600_000)},
			10006: {"Type": int64(petExploreTypePlay), "Time": int64(600_000)},
			10008: {"Type": int64(petExploreTypePlay), "Time": int64(3_600_000)},
			10011: {"Type": int64(petExploreTypeExperience), "Time": int64(600_000)},
			10013: {"Type": int64(petExploreTypeExperience), "Time": int64(3_600_000)},
		},
	})
	for actionType, want := range map[int64]int64{
		petExploreTypeExplore:    123,
		petExploreTypePlay:       124,
		petExploreTypeExperience: 125,
	} {
		if got := petActionDurationSeconds(actionType, 1); got != want {
			t.Fatalf("action type %d duration = %d seconds, want %d", actionType, got, want)
		}
	}

	ss := featureTestSession(1)
	ss.pet = newPet()
	server := &Server{}
	ch := &channel{id: 1, session: ss, conn: &recordingConn{}}
	startedAt := time.Now().Unix()
	response := server.onStartPetExplore(ch, &protocol.C2M_StartPetExplore{}).(*protocol.M2C_StartPetExplore)
	if response.Error != 0 || response.Message != "" {
		t.Fatalf("start pet explore response = %+v", response)
	}
	if ss.pet.ActionEnd-startedAt < 122 || ss.pet.ActionEnd-startedAt > 124 {
		t.Fatalf("pet action duration = %d seconds, want about 123", ss.pet.ActionEnd-startedAt)
	}

	info := server.onGetPetInfo(ch, &protocol.C2M_GetPetInfo{}).(*protocol.M2C_GetPetInfo)
	if info.RemainTime < 122_000 || info.RemainTime > 124_000 {
		t.Fatalf("pet remainTime = %d, want milliseconds near 123000", info.RemainTime)
	}
	price := server.onGetPetQuickEndPrice(ch, &protocol.C2M_GetPetQuickEndPrice{}).(*protocol.M2C_GetPetQuickEndPrice)
	if price.Voucher != 7 {
		t.Fatalf("pet quick-end price = %d, want 7", price.Voucher)
	}
	ss.pet.ActionEnd = time.Now().Unix() + 2
	info = server.onGetPetInfo(ch, &protocol.C2M_GetPetInfo{}).(*protocol.M2C_GetPetInfo)
	if info.RemainTime != 2_000 {
		t.Fatalf("two remaining seconds encoded as %d, want 2000 milliseconds", info.RemainTime)
	}
}

func TestPetActionsUseDistinctOnlineRewards(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.Pet.DurationTiers = []operationsconfig.PetDurationTier{{MinimumLevel: 1, DurationMS: 600_000}}
	})
	withFeatureTables(t, &datatables{
		petExploreConfig: map[int64]map[string]interface{}{
			10006: {"Type": int64(petExploreTypePlay), "Time": int64(600_000), "EndRewordArr": []interface{}{
				map[string]interface{}{"PetId": int64(2101), "IncressValue": int64(16)},
			}},
			10011: {"Type": int64(petExploreTypeExperience), "Time": int64(600_000), "EndRewordArr": []interface{}{
				map[string]interface{}{"PetId": int64(2101), "IncressValue": int64(160)},
			}},
		},
		petLevelConfig: map[int64]map[string]interface{}{
			2101: {"Exp": int64(100), "ExpByLevel": int64(20), "MaxLevel": int64(100)},
		},
	})
	server := &Server{}
	ss := featureTestSession(1)
	ss.pet = newPet()
	ch := &channel{id: 1, session: ss, conn: &recordingConn{}}

	ss.pet.PetState = petStatePlay
	server.applyPetActionReward(ch)
	if ss.pet.Intimacy != 16 || ss.pet.Exp != 0 || ss.pet.Active != 0 {
		t.Fatalf("play reward = exp %d intimacy %d active %d, want 0/16/0", ss.pet.Exp, ss.pet.Intimacy, ss.pet.Active)
	}
	ss.pet.PetState = petStateExperience
	server.applyPetActionReward(ch)
	if ss.pet.Level != 2 || ss.pet.Exp != 60 || ss.pet.Intimacy != 16 || ss.pet.Active != 0 {
		t.Fatalf("experience reward = level %d exp %d intimacy %d active %d, want 2/60/16/0",
			ss.pet.Level, ss.pet.Exp, ss.pet.Intimacy, ss.pet.Active)
	}
}

func TestPetDurationTiersIncreaseWithLevel(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {})
	tests := []struct {
		level int32
		want  int64
	}{
		{1, 10 * 60},
		{20, 10 * 60},
		{21, 20 * 60},
		{41, 30 * 60},
		{61, 40 * 60},
		{81, 50 * 60},
		{101, 60 * 60},
		{200, 60 * 60},
	}
	for _, test := range tests {
		for _, actionType := range []int64{petExploreTypeExplore, petExploreTypePlay, petExploreTypeExperience} {
			if got := petActionDurationSeconds(actionType, test.level); got != test.want {
				t.Fatalf("level %d action %d duration=%d, want %d", test.level, actionType, got, test.want)
			}
		}
	}
}

func TestMarketDailyPriceIsStableAndChargedAsDisplayed(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.Market.MinimumPercent = 93
		config.Market.MaximumPercent = 93
		config.Market.TimezoneOffsetHours = 9
	})
	_, _, marketLocation := gameplayMarketSettings()
	day := time.Date(2026, 8, 20, 10, 0, 0, 0, marketLocation)
	first := marketDiscount(71, 1, day)
	if first != 0.93 {
		t.Fatalf("market discount = %.2f, want configured 0.93", first)
	}
	if same := marketDiscount(71, 2, day.Add(12*time.Hour)); same != first {
		t.Fatalf("same account/day discounts differ: %.2f and %.2f", first, same)
	}

	withFeatureTables(t, &datatables{
		marketBase: map[int64]map[string]interface{}{
			10001: {"_id": int64(10001), "Page": int64(0), "ItemId": int64(110305), "Price_YuanBao": int64(101)},
		},
		goodsBase: map[int64]map[string]interface{}{
			110305: {"_id": int64(110305)},
		},
	})
	ss := featureTestSession(1)
	ss.accountID = 71
	ss.yuanBao = 1_000
	conn := &recordingConn{}
	server := &Server{}
	ch := &channel{id: 1, session: ss, conn: conn}
	if response := server.onGetMarket(ch, &protocol.C2M_GetMarket{}); response != nil {
		t.Fatalf("get market returned direct response = %+v", response)
	}
	frames := decodeRecordedFrames(t, conn.Bytes())
	if len(frames) != 1 || frames[0].opcode != protocol.OpM2C_GetMarket {
		t.Fatalf("get market frames = %+v", frames)
	}
	market := &protocol.M2C_GetMarket{}
	if err := proto.Unmarshal(frames[0].body, market); err != nil {
		t.Fatal(err)
	}
	if market.Discount != 0.93 {
		t.Fatalf("wire market discount = %.2f, want configured 0.93", market.Discount)
	}
	wantPrice := marketDiscountedPrice(101, market.Discount)
	before := ss.yuanBao
	server.onBuyInMarket(ch, &protocol.C2M_BuyInMarket{
		PageIndex: 0,
		SlotIndex: 0,
		Count:     1,
		Type:      protocol.MarketType_YuanBaoMarket,
	})
	if charged := before - ss.yuanBao; charged != wantPrice {
		t.Fatalf("market charged %d, displayed rounded price is %d", charged, wantPrice)
	}
}

func TestPVPRequestAndBoardState(t *testing.T) {
	store, err := OpenStore(mysqlTestDSN(t, filepath.Join(t.TempDir(), "pvp.db")))
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
		p, loadErr := store.FirstPlayer(accountID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if updateErr := store.SavePlayerState(playerID, p.JobID, p.Level, p.Exp, p.Energy, p.MapID, p.PosX, p.PosY,
			p.CharPoint, p.SkillPoint, p.StrAdd, p.QukAdd, p.SpiAdd, p.WimAdd, p.PhyAdd, p.StaAdd, p.AutoBattle > 0,
			p.Coin, p.YuanBao, p.Voucher, p.StoreCoin, p.Trans, p.SkinID, p.TitleID, p.FamilyID,
			p.FamilyContribute, p.PersonalContribute, playerRelations{signin: state}, p.Relations.starSoul); updateErr != nil {
			t.Fatal(updateErr)
		}
	}

	first := &channel{id: 1, session: featureTestSession(1)}
	second := &channel{id: 2, session: featureTestSession(2)}
	first.session.state, second.session.state = sessInGame, sessInGame
	// featureTestSession 不初始化血量（hp 留在 newSession 的 -1），而 battleHP()
	// 对未初始化的 hp 走 playerMaxHp()；合成会话没有加载数据表，算出来是 0，
	// 会被个人竞技的健康值校验拦成 battleEntryHealthMessage（pvp.go）。
	// 这里显式给满血，让请求走到真正的匹配逻辑。
	first.session.hp, second.session.hp = 1_000_000, 1_000_000
	server := &Server{store: store, conns: map[int64]*channel{1: first, 2: second}}
	requested := server.onRequestPersonalPvp(first, &protocol.C2M_RequestPersonalPvp{}).(*protocol.M2C_RequestPersonalPvp)
	if requested.Error != 0 || !first.session.signin.PVPIsMatching {
		t.Fatalf("request response = %+v, matching = %v", requested, first.session.signin.PVPIsMatching)
	}
	board := server.onGetPvpBoardInfo(first, &protocol.C2M_GetPvpBoardInfo{}).(*protocol.M2C_GetPvpBoardInfo)
	if board.Error != 0 || !board.IsMatch || board.MatchCount != 1 {
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

	store, err := OpenStore(mysqlTestDSN(t, "task-progress"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	accountID, err := store.CreateAccount("task-progress", "password")
	if err != nil {
		t.Fatal(err)
	}
	playerID, err := store.CreatePlayer(accountID, "task-progress", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	player, err := store.FirstPlayer(accountID)
	if err != nil {
		t.Fatal(err)
	}
	player.Relations.signin = ss.signin
	if err := store.SavePlayerState(playerID, player.JobID, player.Level, player.Exp, player.Energy,
		player.MapID, player.PosX, player.PosY, player.CharPoint, player.SkillPoint,
		player.StrAdd, player.QukAdd, player.SpiAdd, player.WimAdd, player.PhyAdd, player.StaAdd, player.AutoBattle > 0,
		player.Coin, player.YuanBao, player.Voucher, player.StoreCoin, player.Trans, player.SkinID, player.TitleID,
		player.FamilyID, player.FamilyContribute, player.PersonalContribute,
		player.Relations, player.Relations.starSoul); err != nil {
		t.Fatal(err)
	}
	player, err = store.FirstPlayer(accountID)
	if err != nil {
		t.Fatal(err)
	}
	restored := player.Relations.signin
	if restored == nil || restored.TaskKillProgress[1001][501] != 1 || !restored.TaskDialogProgress[1004] ||
		restored.TrialHighestID != 2 {
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
