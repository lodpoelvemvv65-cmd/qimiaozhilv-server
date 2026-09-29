package main

import (
	"fmt"
	"log"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

const (
	activeStarSoul       = "ActiveStarSoulCopy"
	activeIdleBattle     = "ActiveIdleBattle"
	activeWorldBoss      = "ActiveWorldBossCopy"
	activeJourneyOfDeath = "ActiveJourneyOfDeathCopy"
)

// activityBattle is the immutable stage descriptor copied into every party
// member's battle state. ActivePerDayConfig chooses the mode; the dedicated
// copy table chooses the exact monster roster for this stage.
type activityBattle struct {
	ActiveID   int32
	Method     string
	CopyType   int32
	Difficulty int32
	Stage      int32
	ReturnMap  int32
	ReturnX    float32
	ReturnY    float32
}

func (a *activityBattle) clone() *activityBattle {
	if a == nil {
		return nil
	}
	copy := *a
	return &copy
}

func (a *activityBattle) copyID() int64 {
	if a == nil {
		return 0
	}
	switch a.Method {
	case activeStarSoul:
		return 10014
	case activeWorldBoss:
		return 10015
	case activeJourneyOfDeath:
		return 10016
	default:
		return 0
	}
}

func (a *activityBattle) next() *activityBattle {
	if a == nil {
		return nil
	}
	maxStage := int32(0)
	switch a.Method {
	case activeStarSoul:
		maxStage = 3
	case activeJourneyOfDeath:
		maxStage = 2
	}
	if a.Stage >= maxStage {
		return nil
	}
	next := a.clone()
	next.Stage++
	return next
}

func activityStageScene(a *activityBattle) (mapID int32, x, y float32, ok bool) {
	if a == nil || a.Stage <= 0 {
		return 0, 0, 0, false
	}
	switch a.Method {
	case activeStarSoul:
		if a.CopyType < 1 || a.CopyType > 5 || a.Stage > 3 {
			return 0, 0, 0, false
		}
		// Sys_Scene 10045..10049 are the five online star-soul scenes.
		return (10044+a.CopyType)*100 + a.Stage, -14, -1.259455, true
	case activeWorldBoss:
		return 1005001, -12, -1.259455, true
	case activeJourneyOfDeath:
		if a.Stage > 2 {
			return 0, 0, 0, false
		}
		return 1005100 + a.Stage, -6.52, -1.92, true
	default:
		return 0, 0, 0, false
	}
}

func isActivityStageMap(mapID int32) bool {
	sceneID, layer := mapID/100, mapID%100
	if sceneID >= 10045 && sceneID <= 10049 {
		return layer >= 1 && layer <= 3
	}
	return mapID == 1005001 || mapID == 1005101 || mapID == 1005102
}

// Activity return coordinates live only in the in-memory battle descriptor.
// If a process restart leaves a persisted dedicated map behind, return to the
// city instead of initializing a regular SceneTransConfig for that copy scene.
func normalizeActivityLoginMap(ss *session) bool {
	if ss == nil || !isActivityStageMap(ss.mapID) {
		return false
	}
	x, y := mainCityReturnSpawn()
	ss.mapID = 10004
	ss.resetMovement(x, y)
	return true
}

func activeParams(row map[string]interface{}) []string {
	values := arrOf(row["Params"])
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, fmt.Sprint(value))
	}
	return out
}

func parseActiveParam(values []string, index int) (int32, bool) {
	if index < 0 || index >= len(values) {
		return 0, false
	}
	value, err := strconv.ParseInt(values[index], 10, 32)
	return int32(value), err == nil
}

func activeAvailableToday(row map[string]interface{}, now time.Time) bool {
	weekday := int64(now.Weekday())
	for _, value := range arrOf(row["Day"]) {
		if num(value) == weekday {
			return true
		}
	}
	return false
}

func activityUnavailableMessage(row map[string]interface{}, now time.Time) string {
	if activeAvailableToday(row, now) {
		return ""
	}
	if fmt.Sprint(row["BattleMethod"]) != activeStarSoul {
		return "活动今日未开放"
	}
	copyType, ok := parseActiveParam(activeParams(row), 0)
	if !ok || copyType < 1 || copyType > 5 {
		return "活动今日未开放"
	}
	openDays := make([]string, 0, len(arrOf(row["Day"])))
	for _, value := range arrOf(row["Day"]) {
		if name := chineseWeekday(time.Weekday(num(value))); name != "" {
			openDays = append(openDays, name)
		}
	}
	copyName := string(rune('A' + copyType - 1))
	todayType := int32(now.Weekday())
	if todayType >= 1 && todayType <= 5 {
		todayName := string(rune('A' + todayType - 1))
		return fmt.Sprintf("今天是%s，当前开放%s星魂副本；%s星魂副本开放时间为%s，请点击%s层",
			chineseWeekday(now.Weekday()), todayName, copyName, strings.Join(openDays, "、"), todayName)
	}
	return fmt.Sprintf("今天是%s；%s星魂副本开放时间为%s",
		chineseWeekday(now.Weekday()), copyName, strings.Join(openDays, "、"))
}

func chineseWeekday(day time.Weekday) string {
	switch day {
	case time.Sunday:
		return "星期日"
	case time.Monday:
		return "星期一"
	case time.Tuesday:
		return "星期二"
	case time.Wednesday:
		return "星期三"
	case time.Thursday:
		return "星期四"
	case time.Friday:
		return "星期五"
	case time.Saturday:
		return "星期六"
	default:
		return ""
	}
}

func starSoulCopyRow(copyType, difficulty, stage int32) map[string]interface{} {
	if tables == nil {
		return nil
	}
	for _, row := range tables.starSoulCopy {
		if int32(num(row["Type"])) == copyType && int32(num(row["Difficulty"])) == difficulty &&
			int32(num(row["BossIndex"])) == stage {
			return row
		}
	}
	return nil
}

func journeyOfDeathRow(difficulty, stage int32) map[string]interface{} {
	if tables == nil {
		return nil
	}
	for _, row := range tables.journeyOfDeathConfig {
		if int32(num(row["Difficulty"])) == difficulty && int32(num(row["BossIndex"])) == stage {
			return row
		}
	}
	return nil
}

func worldBossActivityRow() map[string]interface{} {
	if tables == nil {
		return nil
	}
	ids := make([]int, 0, len(tables.worldBossConfig))
	for id := range tables.worldBossConfig {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	if len(ids) == 0 {
		return nil
	}
	return tables.worldBossConfig[int64(ids[0])]
}

func activityRoster(a *activityBattle) ([]int32, []int, bool) {
	if a == nil {
		return nil, nil, false
	}
	var row map[string]interface{}
	switch a.Method {
	case activeStarSoul:
		row = starSoulCopyRow(a.CopyType, a.Difficulty, a.Stage)
	case activeWorldBoss:
		row = worldBossActivityRow()
	case activeJourneyOfDeath:
		row = journeyOfDeathRow(a.Difficulty, a.Stage)
	}
	if row == nil {
		return nil, nil, false
	}
	var ids []int32
	var counts []int
	monsterValues := arrOf(row["MonsterIdArr"])
	if len(monsterValues) == 0 {
		monsterValues = arrOf(row["MonsterId"])
	}
	for _, value := range monsterValues {
		if id := int32(num(value)); id > 0 {
			ids = append(ids, id)
			counts = append(counts, 1)
		}
	}
	return ids, counts, len(ids) > 0
}

func activityBattleDrops(battle *battleState) trialReward {
	reward := trialBattleDrops(battle)
	if battle != nil && battle.activity != nil && battle.activity.Method == activeWorldBoss {
		rollParentset(gameplayWorldBossRewardParentsetID(), &reward)
	}
	return reward
}

func journeyDifficulty(activeID int32) int32 {
	if tables == nil {
		return 0
	}
	ids := make([]int, 0, 8)
	for id, row := range tables.activePerDay {
		if fmt.Sprint(row["BattleMethod"]) == activeJourneyOfDeath {
			ids = append(ids, int(id))
		}
	}
	sort.Ints(ids)
	for index, id := range ids {
		if int32(id) == activeID {
			return int32(index + 1)
		}
	}
	return 0
}

// idleRegionForSession uses the current main-story map when available. The
// online server also remembers campaign progress outside those maps; this
// build has no separate campaign-record field, so the closest monster level
// not above the player's cumulative level is the table-derived equivalent.
func idleRegionForSession(ss *session) int32 {
	if ss == nil || tables == nil {
		return 0
	}
	if region, _, _, _, ok := mainStoryRosterForMap(ss.mapID); ok {
		return region
	}
	bestRegion, bestLevel := int32(0), int32(-1)
	for id, row := range tables.mainStory {
		ids, _ := rosterFromMainStoryConfig(row)
		if len(ids) == 0 {
			continue
		}
		level := int32(0)
		for _, monsterID := range ids {
			if monster := tables.monsterBase[int64(monsterID)]; monster != nil {
				if candidate := int32(num(monster["Level"])); candidate > level {
					level = candidate
				}
			}
		}
		if level <= ss.level && (level > bestLevel || (level == bestLevel && int32(id) > bestRegion)) {
			bestRegion, bestLevel = int32(id), level
		}
	}
	if bestRegion == 0 {
		bestRegion = 1001
	}
	return bestRegion
}

func rosterFromMainStoryConfig(row map[string]interface{}) ([]int32, []int) {
	var ids []int32
	var counts []int
	for group := 1; group <= 6; group++ {
		arrKey := fmt.Sprintf("Monster_%dArr", group)
		idKey := fmt.Sprintf("Monster_%d_Id", group)
		countKey := fmt.Sprintf("Monster_%d_Count", group)
		for _, value := range arrOf(row[arrKey]) {
			entry, _ := value.(map[string]interface{})
			id, count := int32(num(entry[idKey])), int(num(entry[countKey]))
			if id > 0 && count > 0 {
				ids, counts = append(ids, id), append(counts, count)
			}
		}
	}
	return ids, counts
}

func (s *Server) startActivityStage(ch *channel, activity *activityBattle, skipEnergy bool) (bool, string) {
	if ch == nil || ch.session == nil || activity == nil {
		return false, "活动参数无效"
	}
	_, _, ok := activityRoster(activity)
	if !ok {
		return false, "活动怪物配置不存在"
	}
	participants := s.configuredBattleParticipants(ch, activity.copyID(), false)
	if len(participants) == 0 {
		participants = []*channel{ch}
	}
	if message := battleEntryHealthFailure(ch, participants); message != "" {
		return false, message
	}
	presentation := battlePresentation{
		kind:       presentationMainStory,
		copyID:     activity.copyID(),
		activity:   activity,
		skipEnergy: skipEnergy,
	}
	if message := s.battleDailyDungeonQuotaFailure(ch, 1001, ch.session.mapID, presentation, time.Now()); message != "" {
		return false, message
	}
	if activity.ReturnMap == 0 {
		activity.ReturnMap = ch.session.mapID
		activity.ReturnX = ch.session.x
		activity.ReturnY = ch.session.y
	}
	mapID, x, y, hasScene := activityStageScene(activity)
	if !hasScene {
		return false, "活动场景配置不存在"
	}
	if !s.queueActivityStage(ch, participants, mapID, x, y, activity, skipEnergy) {
		return false, "活动正在加载或队员状态已变化"
	}
	log.Printf("[S=%d] queue active %d %s difficulty=%d stage=%d participants=%d map=%d",
		ch.id, activity.ActiveID, activity.Method, activity.Difficulty, activity.Stage, len(participants), mapID)
	return true, ""
}

func (s *Server) onStartActive(ch *channel, req *protocol.C2M_StartActive) proto.Message {
	return s.onStartActiveAt(ch, req, time.Now())
}

func (s *Server) onStartActiveAt(ch *channel, req *protocol.C2M_StartActive, now time.Time) proto.Message {
	resp := &protocol.M2C_StartActive{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Message = "请先登录"
		return resp
	}
	if tables == nil {
		resp.Message = "活动数据未加载"
		return resp
	}
	row := tables.activePerDay[int64(req.ItemId)]
	if row == nil {
		resp.Message = "活动不存在"
		return resp
	}
	if message := activityUnavailableMessage(row, now); message != "" {
		if fmt.Sprint(row["BattleMethod"]) == activeStarSoul {
			// The activity list callback should display response.Message, but the
			// stock UI can already be closing while the RPC returns. SendTip owns a
			// native independent TipUI and makes the schedule refusal unambiguous.
			s.sendPush(ch, protocol.OpM2C_SendTip, &protocol.M2C_SendTip{
				Message: message, ActorId: ch.session.playerID,
			})
			log.Printf("[S=%d] reject star soul active=%d: %s", ch.id, req.ItemId, message)
		} else {
			resp.Message = message
		}
		return resp
	}
	if sessionHasBattle(ch) {
		resp.Message = "战斗正在进行中"
		return resp
	}

	method := fmt.Sprint(row["BattleMethod"])
	params := activeParams(row)
	copyID := (&activityBattle{Method: method}).copyID()
	if copyID == 0 && method == activeIdleBattle {
		copyID = 10010
	}
	if copyID != 0 && !copyRequiresSoloTeamExit(copyID) {
		if message := s.teamLeaderDungeonEntryFailure(ch); message != "" {
			resp.Message = message
			return resp
		}
	}
	switch method {
	case activeIdleBattle:
		region := idleRegionForSession(ch.session)
		result := s.onStartBattleIdleFight(ch, &protocol.C2M_StartBattleIdleFight{SceneId: region})
		if message := result.(*protocol.M2C_StartBattleIdleFight).Message; message != "" {
			resp.Message = message
		}
		return resp
	case activeStarSoul:
		copyType, okType := parseActiveParam(params, 0)
		difficulty, okDifficulty := parseActiveParam(params, 1)
		if !okType || !okDifficulty || copyType < 1 || copyType > 5 || difficulty < 1 || difficulty > 4 {
			resp.Message = "星魂副本参数无效"
			return resp
		}
		_, resp.Message = s.startActivityStage(ch, &activityBattle{
			ActiveID: req.ItemId, Method: method, CopyType: copyType, Difficulty: difficulty, Stage: 1,
		}, false)
	case activeWorldBoss:
		_, resp.Message = s.startActivityStage(ch, &activityBattle{
			ActiveID: req.ItemId, Method: method, Stage: 1,
		}, false)
	case activeJourneyOfDeath:
		minimum, okMinimum := parseActiveParam(params, 0)
		difficulty := journeyDifficulty(req.ItemId)
		if !okMinimum || difficulty == 0 {
			resp.Message = "死亡之路参数无效"
			return resp
		}
		if ch.session.level < minimum {
			resp.Message = fmt.Sprintf("需要达到%d级", minimum)
			return resp
		}
		_, resp.Message = s.startActivityStage(ch, &activityBattle{
			ActiveID: req.ItemId, Method: method, Difficulty: difficulty, Stage: 1,
		}, false)
	default:
		resp.Message = "活动功能未配置"
	}
	return resp
}

func starSoulActivityTypeIDs(copyType int32) []int32 {
	if tables == nil {
		return nil
	}
	var ids []int32
	for id, row := range tables.starSoulType {
		if int32(num(row["CopyType"])) == copyType {
			ids = append(ids, int32(id))
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func activityRandomCount(minimum, maximum int32) int32 {
	if minimum < 0 {
		minimum = 0
	}
	if maximum < minimum {
		maximum = minimum
	}
	if maximum == minimum {
		return minimum
	}
	return minimum + int32(rand.Intn(int(maximum-minimum+1)))
}

// awardStarSoulActivity uses the online copy row's type and count range.
// The client tables expose four difficulties and six qualities but no quality
// probability table, so difficulty is the only authoritative quality tier
// available for this source.
func (s *Server) awardStarSoulActivity(ch *channel, battle *battleState) int {
	if ch == nil || ch.session == nil || battle == nil || battle.activity == nil ||
		battle.activity.Method != activeStarSoul {
		return 0
	}
	activity := battle.activity
	row := starSoulCopyRow(activity.CopyType, activity.Difficulty, activity.Stage)
	typeIDs := starSoulActivityTypeIDs(activity.CopyType)
	if row == nil || len(typeIDs) == 0 {
		return 0
	}
	count := activityRandomCount(int32(num(row["StarSoulCountMin"])), int32(num(row["StarSoulCountMax"])))
	bag := ch.session.ensureStarSoulBag()
	free := starSoulCapacity - len(bag.Items)
	if int(count) > free {
		count = int32(free)
	}
	quality := activity.Difficulty
	if quality < 1 {
		quality = 1
	}
	if quality > 6 {
		quality = 6
	}
	for index := int32(0); index < count; index++ {
		typeID := typeIDs[rand.Intn(len(typeIDs))]
		item := generateStarSoul(typeID, int32(rand.Intn(starSoulSlotCount)), quality)
		if item.Main == 0 {
			continue
		}
		bag.Items[item.ID] = item
		s.pushStarSoulItem(ch, item.ID, item)
	}
	return int(count)
}

func (s *Server) continueSoloActivity(ch *channel, battle *battleState) {
	if battle == nil || battle.party != nil || battle.activity == nil {
		return
	}
	next := battle.activity.next()
	if next == nil {
		s.scheduleActivityReturn(ch, battle.activity)
		return
	}
	time.AfterFunc(50*time.Millisecond, func() {
		configStateMu.RLock()
		defer configStateMu.RUnlock()
		if started, message := s.startActivityStage(ch, next, true); !started {
			log.Printf("[S=%d] continue active %d stage=%d failed: %s", ch.id, next.ActiveID, next.Stage, message)
		}
	})
}

func (s *Server) continuePartyActivity(memberIDs []int64, battles map[int64]*battleState) {
	if len(memberIDs) < 2 {
		return
	}
	var next *activityBattle
	var origin *channel
	for _, playerID := range memberIDs {
		battle := battles[playerID]
		if battle != nil && battle.activity != nil {
			next = battle.activity.next()
		}
		candidate := s.findChannelByPlayerID(playerID)
		if candidate != nil && (origin == nil || s.teamLeaderDungeonEntryFailure(candidate) == "") {
			origin = candidate
		}
	}
	if next == nil || origin == nil {
		if next == nil {
			for _, playerID := range memberIDs {
				if ch := s.findChannelByPlayerID(playerID); ch != nil {
					if battle := battles[playerID]; battle != nil {
						s.scheduleActivityReturn(ch, battle.activity)
					}
				}
			}
		}
		return
	}
	if started, message := s.startActivityStage(origin, next, true); !started {
		log.Printf("continue party active %d stage=%d failed: %s", next.ActiveID, next.Stage, message)
	}
}

func (s *Server) scheduleActivityReturn(ch *channel, activity *activityBattle) {
	if ch == nil || ch.session == nil || activity == nil || activity.ReturnMap == 0 {
		return
	}
	returnState := activity.clone()
	mapSeq, _ := ch.session.currentMapSceneVersion()
	time.AfterFunc(50*time.Millisecond, func() {
		configStateMu.RLock()
		defer configStateMu.RUnlock()
		partyStartMu.Lock()
		defer partyStartMu.Unlock()
		ss := ch.session
		if ss == nil || !ss.isCurrentMapChange(mapSeq) {
			return
		}
		ss.battleMu.Lock()
		busy := ss.battle != nil
		ss.battleMu.Unlock()
		if busy {
			return
		}
		s.changeMap(ch, returnState.ReturnMap, returnState.ReturnX, returnState.ReturnY)
	})
}

func (s *Server) returnFromActivity(ch *channel, activity *activityBattle) bool {
	if ch == nil || ch.session == nil || activity == nil || activity.ReturnMap == 0 {
		return false
	}
	s.changeMap(ch, activity.ReturnMap, activity.ReturnX, activity.ReturnY)
	return true
}
