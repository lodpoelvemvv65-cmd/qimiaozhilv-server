package main

import (
	"log"
	"math"
	"sort"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// 任务状态（protocol.TaskState 枚举值，session.tasks 以 int32 存储）
const (
	taskStateWaiting   = int32(protocol.TaskState_TaskWaiting)   // 1 可接
	taskStateRunning   = int32(protocol.TaskState_TaskRunning)   // 2 进行中
	taskStateCompleted = int32(protocol.TaskState_TaskCompleted) // 3 已完成
)

const (
	npcInteractionRadius               = 2.5
	taskRewardSelectionRequiredMessage = "请选择数量的奖励！"
)

// task.go：任务系统（20206-20225 段）。
//
// 任务数据模型：
//   session.tasks    taskID → TaskState（0 无/1 可接/2 进行中/3 已完成）
//   session.killCount 怪物ID → 已击杀数（KillSpecial 任务计数，战斗胜利时累加）
//
// 流程：新角色 EnterGame 时初始化无前置的初始任务链（10011 开始旅程！）为
// Waiting，并在首次场景启动完成后主动推送 M2C_OpenTaskUI(20216)；玩家点击
// 接受后进入 Running，达成条件后再由 CompleteTask 提交结算奖励。

// 任务目标类型（TaskBase.TargetType 字段）
const (
	taskTargetKillSpecial = 1 // 击杀指定怪物（CountTargetArr: CountTarget_Id/CountTarget_Count）
	taskTargetKillAny     = 2 // 击杀任意怪物
	taskTargetCollect     = 3 // 收集道具
	taskTargetDialog      = 4 // 对话（点击对应 NPC 即完成）
	taskTargetLevel       = 5 // 升到指定等级（TargetLevel 字段）
	taskTargetSubmit      = 6 // 提交
	taskTargetChangeMap   = 7 // 进入指定地图
	taskTargetQuiz        = 8 // 答题
)

// taskByID 按任务 ID 查 TaskBase 配置。
func taskByID(id int32) (map[string]interface{}, bool) {
	if tables == nil {
		return nil, false
	}
	tb, ok := tables.taskBase[int64(id)]
	return tb, ok
}

// taskPrerequisiteID applies the server-side compatibility rule for the city
// task officers. The online table ties their first available tasks to the
// beach tutorial (10016/10017), but those officers are intended to be usable
// from the main city. Only the first task in each officer chain is detached;
// later leveling and officer tasks still follow their own chain.
func taskPrerequisiteID(taskID int32, tb map[string]interface{}) int32 {
	if tb == nil {
		return 0
	}
	pre := int32(num(tb["PreTaskId"]))
	if pre == 0 {
		return 0
	}
	giveNPC := int32(num(tb["GiveNPCId"]))
	if giveNPC == 1007 && pre == 10016 {
		// 10017 is the first rush-level task. Its own successors must still
		// wait for 10017 to be completed.
		return 0
	}
	if giveNPC == 1005 || giveNPC == 1006 {
		// 10035-10041 and 10046 are the first entries of the daily/leveling
		// officer chains. The online table uses 10016/10017 as a global
		// tutorial gate for these city tasks; remove that gate only.
		if pre == 10016 || pre == 10017 {
			return 0
		}
	}
	return pre
}

func mapSceneAndLayer(mapID int32) (int32, int32) {
	if mapID >= 100000 {
		return mapID / 100, mapID % 100
	}
	return mapID, 1
}

// validateNPCInteraction checks the same scene/layer and 2.5-unit interaction
// radius used by the client. The position is interpolated server-side so an
// in-flight path cannot interact from its destination before arriving there.
func validateNPCInteraction(ss *session, npcID int32) string {
	if ss == nil || tables == nil {
		return "NPC配置未加载"
	}
	npc := tables.npcBase[int64(npcID)]
	if npc == nil {
		return "NPC不存在"
	}
	sceneID := int32(num(npc["SceneId"]))
	layer := int32(num(npc["MapLayer"]))
	currentScene, currentLayer := mapSceneAndLayer(ss.mapID)
	if sceneID <= 0 || currentScene != sceneID || (layer > 0 && currentLayer != layer) {
		return "不在该NPC所在地图"
	}
	x, y := ss.currentPosition(time.Now(), moveSpeed)
	dx := float64(x) - numf(npc["PosX"])
	dy := float64(y) - numf(npc["PosY"])
	if math.Hypot(dx, dy) > npcInteractionRadius {
		return "距离NPC太远"
	}
	return ""
}

func validateTaskNPC(ss *session, npcID int32) string {
	if npcID <= 0 {
		return "任务未配置NPC"
	}
	if ss == nil || ss.lastNPCID != npcID {
		return "请先与指定NPC对话"
	}
	return validateNPCInteraction(ss, npcID)
}

// onClickNPC：20206 → 20207。点击 NPC，响应 + 推送 M2C_OpenTaskUI(20216) 打开任务面板。
// NPC 相关任务状态同步在 TaskList 中，客户端据此渲染接取/提交按钮。
func (s *Server) onClickNPC(ch *channel, req *protocol.C2M_ClickNPC) proto.Message {
	resp := &protocol.M2C_ClickNPC{RpcId: req.RpcId}
	if ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	npcID := req.NPCId
	// A previous layer's NPC can survive the client's asynchronous scene
	// cleanup and still send its old id. Resolve the beach story NPC from the
	// authoritative map so the task chain remains clickable during that race.
	if ch.session.mapID/100 == 10006 && npcID >= 1012 && npcID <= 1015 {
		layer := ch.session.mapID % 100
		if layer >= 1 && layer <= 4 {
			expected := int32(1011 + layer)
			if npcID != expected {
				log.Printf("[S=%d] remap stale beach npc %d -> %d for map %d", ch.id, npcID, expected, ch.session.mapID)
				npcID = expected
			}
		}
	}
	if tables == nil {
		resp.Error, resp.Message = errBadParam, "配置未加载"
		return resp
	}
	if message := validateNPCInteraction(ch.session, npcID); message != "" {
		resp.Error, resp.Message = errBadParam, message
		return resp
	}
	ch.session.lastNPCID = npcID
	if s.markDialogTaskProgress(ch.session, npcID) {
		s.saveData(ch)
	}

	var list []*protocol.NPCTask
	seen := make(map[int32]bool)
	// 1) 进行中的任务（交任务 NPC 匹配 → 可提交）
	for id, st := range ch.session.tasks {
		if st != taskStateRunning && st != taskStateWaiting {
			continue
		}
		tb, ok := taskByID(id)
		if !ok {
			continue
		}
		if int32(num(tb["SubmitNPCId"])) != npcID {
			continue
		}
		displayState := protocol.TaskState(st)
		if st == taskStateRunning && s.taskCompleted(ch.session, tb) {
			displayState = protocol.TaskState_TaskCompleted
		}
		list = append(list, &protocol.NPCTask{Id: id, TaskState: displayState})
		seen[id] = true
	}
	// 2) 可接取的任务（接任务 NPC 匹配 + 前置已完成）。根任务没有
	// GiveNPCId，但原版会在苹果 JJ 处展示它，因此也按 SubmitNPCId 匹配。
	for id, tb := range tables.taskBase {
		tid := int32(id)
		if seen[tid] {
			continue
		}
		giveNPC := int32(num(tb["GiveNPCId"]))
		if giveNPC != npcID && !(giveNPC == 0 && int32(num(tb["SubmitNPCId"])) == npcID) {
			continue
		}
		if state, accepted := ch.session.tasks[tid]; accepted && state != taskStateWaiting {
			continue
		}
		if pre := taskPrerequisiteID(tid, tb); pre != 0 &&
			ch.session.tasks[pre] != taskStateCompleted {
			continue
		}
		if minLevel := int32(num(tb["Level"])); minLevel > 0 && ch.session.level < minLevel {
			continue
		}
		list = append(list, &protocol.NPCTask{Id: tid, TaskState: protocol.TaskState_TaskWaiting})
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Id < list[j].Id
	})

	if len(list) > 0 {
		s.sendPush(ch, protocol.OpM2C_OpenTaskUI, &protocol.M2C_OpenTaskUI{
			TaskList: list,
			ActorId:  ch.session.playerID,
		})
	} else if isTaskOfficerNPC(npcID) {
		// 任务官 NPC 点击即使无可用任务也推空任务面板（客户端打开面板有反馈，
		// 否则"点击无反应"像 NPC 不能点；练级任务官 1005 原版表无任务 → 空面板）。
		s.sendPush(ch, protocol.OpM2C_OpenTaskUI, &protocol.M2C_OpenTaskUI{
			TaskList: []*protocol.NPCTask{},
			ActorId:  ch.session.playerID,
		})
	}
	// 非任务 NPC：按 NPCBase 打开对应功能 UI（shop.go / portal.go / spacetravel.go）。
	// 客户端 M2C_OpenShopUI/OpenStoreUI/OpenConsignmentUI/OpenForgeUI/OpenMeltEquipUI/
	// OpenSpaceTravelNpcUI 各 Handler 负责打开对应面板。
	switch npcID {
	case 1004: // 万能管家 → 仓库
		s.pushOpenStoreUI(ch)
	case 1008: // 普通商店
		s.pushOpenShopUI(ch)
	case 1009: // 寄售商人
		s.pushOpenConsignmentUI(ch)
	case 1010: // 装备合成大师 → 锻造
		s.sendPush(ch, protocol.OpM2C_OpenForgeUI, &protocol.M2C_OpenForgeUI{
			ActorId: ch.session.playerID,
		})
	case 1011: // 炼化大师 → 宝石镶嵌（熔炼）
		s.sendPush(ch, protocol.OpM2C_OpenMeltEquipUI, &protocol.M2C_OpenMeltEquipUI{
			ActorId: ch.session.playerID,
		})
	case 1018: // 时空旅人（奇妙广场）→ 星空旅行
		s.pushOpenSpaceTravelNpcUI(ch)
	case 1003: // 奇妙大博士 → 答题
		s.sendPush(ch, protocol.OpM2C_OpenQuestUI, &protocol.M2C_OpenQuestUI{
			Card: gameplayQuizQuestionCount(), ActorId: ch.session.playerID,
		})
	}
	log.Printf("[S=%d] click npc %d tasks=%d", ch.id, npcID, len(list))
	return resp
}

// isTaskOfficerNPC：任务官 NPC（原版表 1005 练级任务官/1006 每日任务官/1007 冲级任务官）。
// 点击这些 NPC 必须推任务面板（即使无任务），否则客户端点击无反馈像"不能点"。
func isTaskOfficerNPC(npcID int32) bool {
	switch npcID {
	case 1005, 1006, 1007:
		return true
	}
	return false
}

// onAcceptTask：20217 → 20218。接取任务，状态置 Running。
func (s *Server) onAcceptTask(ch *channel, req *protocol.C2M_AcceptTask) proto.Message {
	resp := &protocol.M2C_AcceptTask{RpcId: req.RpcId}
	tb, ok := taskByID(req.TaskId)
	if !ok {
		resp.Error, resp.Message = errBadParam, "任务不存在"
		return resp
	}
	if state := ch.session.tasks[req.TaskId]; state != 0 && state != taskStateWaiting {
		log.Printf("[S=%d] duplicate accept task %d ignored", ch.id, req.TaskId)
		return resp
	}
	if pre := taskPrerequisiteID(req.TaskId, tb); pre != 0 &&
		ch.session.tasks[pre] != taskStateCompleted {
		resp.Error, resp.Message = errBadParam, "前置任务未完成"
		return resp
	}
	if minLevel := int32(num(tb["Level"])); minLevel > 0 && ch.session.level < minLevel {
		resp.Error, resp.Message = errBadParam, "等级不足"
		return resp
	}
	// The introductory root task has no GiveNPCId and is accepted directly
	// from the auto-opened task window. Other tasks still require the player to
	// have interacted with their configured NPC in the current scene.
	if giveNPC := int32(num(tb["GiveNPCId"])); giveNPC != 0 {
		if message := validateTaskNPC(ch.session, giveNPC); message != "" {
			resp.Error, resp.Message = errBadParam, message
			return resp
		}
	}
	ch.session.tasks[req.TaskId] = taskStateRunning
	if ch.session.signin == nil {
		ch.session.signin = &signinState{}
	}
	ch.session.signin.ensureTaskProgress()
	switch int32(num(tb["TargetType"])) {
	case taskTargetKillSpecial:
		ch.session.signin.TaskKillProgress[req.TaskId] = make(map[int32]int32)
	case taskTargetKillAny:
		ch.session.signin.TaskAnyKillProgress[req.TaskId] = 0
	case taskTargetDialog:
		target := int32(num(tb["DialogTargetId"]))
		ch.session.signin.TaskDialogProgress[req.TaskId] = target != 0 && target == ch.session.lastNPCID
	case taskTargetQuiz:
		ch.session.signin.TaskQuizProgress[req.TaskId] = false
	}
	s.saveData(ch)
	s.pushCurrentTaskStates(ch)
	log.Printf("[S=%d] accept task %d", ch.id, req.TaskId)
	return resp
}

// onCompleteTask：20219 → 20220。提交任务：校验条件达成 → 置 Completed + 发奖励。
func (s *Server) onCompleteTask(ch *channel, req *protocol.C2M_CompleteTask) proto.Message {
	resp := &protocol.M2C_CompleteTask{RpcId: req.RpcId}
	tb, ok := taskByID(req.TaskId)
	if !ok {
		resp.Error, resp.Message = errBadParam, "任务不存在"
		return resp
	}
	state := ch.session.tasks[req.TaskId]
	if state == taskStateCompleted {
		log.Printf("[S=%d] duplicate complete task %d ignored", ch.id, req.TaskId)
		return resp
	}
	if state != taskStateRunning {
		resp.Error, resp.Message = errBadParam, "任务未接取或已完成"
		return resp
	}
	if !s.taskCompleted(ch.session, tb) {
		resp.Error, resp.Message = errBadParam, "任务条件未达成"
		return resp
	}
	if message := validateTaskNPC(ch.session, int32(num(tb["SubmitNPCId"]))); message != "" {
		resp.Error, resp.Message = errBadParam, message
		return resp
	}
	// Reward selection is a user-correctable validation failure.  The native
	// task window only handles a normal RPC response here; a non-zero Error
	// makes Session.Call throw and leaves its async button handler stuck.  Keep
	// the task running and return its user-facing correction tip.
	if message := taskRewardSelectionMessage(tb, req.IndexList); message != "" {
		resp.Message = message
		return resp
	}
	staged, grants, message := stageTaskCompletionWithSelection(ch.session, tb, req.IndexList)
	if message != "" {
		resp.Error, resp.Message = errBadParam, message
		return resp
	}
	starBefore := starCoinBalance(ch.session)
	ch.session.bag = staged
	for _, grant := range grants {
		log.Printf("[S=%d] task reward item=%d count=%d", ch.id, grant.itemID, grant.count)
	}
	ch.session.tasks[req.TaskId] = taskStateCompleted
	s.saveData(ch)
	s.pushBagSnapshot(ch)
	if starCoinBalance(ch.session) != starBefore {
		s.pushMoney(ch)
	}
	s.pushCurrentTaskStates(ch)
	log.Printf("[S=%d] complete task %d", ch.id, req.TaskId)
	return resp
}

// taskCompleted 校验任务完成条件（按 TargetType 分派）。
func (s *Server) taskCompleted(ss *session, tb map[string]interface{}) bool {
	if ss == nil || tb == nil {
		return false
	}
	taskID := int32(num(tb["_id"]))
	switch int32(num(tb["TargetType"])) {
	case taskTargetKillSpecial:
		// 击杀指定怪物，每个 CountTarget 都须达标
		for _, e := range arrOf(tb["CountTargetArr"]) {
			eo, _ := e.(map[string]interface{})
			mid := int32(num(eo["CountTarget_Id"]))
			cnt := int32(num(eo["CountTarget_Count"]))
			if taskKillCount(ss, taskID, mid) < cnt {
				return false
			}
		}
		return true
	case taskTargetKillAny:
		need := int32(num(tb["AnyTargetCount"]))
		return need > 0 && taskAnyKillCount(ss, taskID) >= need
	case taskTargetCollect, taskTargetSubmit:
		return hasTaskMaterials(ss, tb)
	case taskTargetDialog:
		return ss.signin != nil && ss.signin.TaskDialogProgress[taskID]
	case taskTargetLevel:
		targetLevel := int32(num(tb["TargetLevel"]))
		if targetLevel <= 0 {
			targetLevel = int32(num(tb["Level"]))
		}
		return ss.level >= targetLevel
	case taskTargetChangeMap:
		if ss.signin == nil || tables == nil {
			return false
		}
		trial := tables.trialCopy[int64(ss.signin.TrialHighestID)]
		if trial == nil {
			return false
		}
		mapID := int32(num(trial["MapId"]))
		targetScene := int32(num(tb["TargetSceneId"]))
		targetLayer := int32(num(tb["TargetMaplayer"]))
		return mapID/100 == targetScene && mapID%100 >= targetLayer
	case taskTargetQuiz:
		return ss.signin != nil && ss.signin.TaskQuizProgress[taskID]
	default:
		return false
	}
}

func taskKillCount(ss *session, taskID, monsterID int32) int32 {
	if ss != nil && ss.signin != nil {
		if progress, ok := ss.signin.TaskKillProgress[taskID]; ok {
			return progress[monsterID]
		}
	}
	// Existing saves used global kill counters. Preserve in-progress legacy tasks.
	if ss != nil {
		return ss.killCount[monsterID]
	}
	return 0
}

func taskAnyKillCount(ss *session, taskID int32) int32 {
	if ss == nil || ss.signin == nil {
		return 0
	}
	return ss.signin.TaskAnyKillProgress[taskID]
}

func taskMaterialCosts(tb map[string]interface{}) []bagGrant {
	var costs []bagGrant
	for _, entry := range arrOf(tb["CountTargetArr"]) {
		row, _ := entry.(map[string]interface{})
		itemID := int32(num(row["CountTarget_Id"]))
		count := int32(num(row["CountTarget_Count"]))
		if itemID > 0 && count > 0 {
			costs = append(costs, bagGrant{itemID: itemID, count: count})
		}
	}
	return costs
}

func hasTaskMaterials(ss *session, tb map[string]interface{}) bool {
	costs := taskMaterialCosts(tb)
	if len(costs) == 0 {
		return false
	}
	for _, cost := range costs {
		if bagItemCount(ss, cost.itemID) < cost.count {
			return false
		}
	}
	return true
}

// taskRewardGrants returns every configured item reward. It is retained for
// reward paths without a selectable reward list (RwardCount == 0).
func taskRewardGrants(tb map[string]interface{}) []bagGrant {
	if tables == nil {
		return nil
	}
	rewards := arrOf(tb["RewardArr"])
	var grants []bagGrant
	for _, entry := range rewards {
		row, _ := entry.(map[string]interface{})
		if row == nil {
			continue
		}
		itemID := int32(num(row["Reward_Id"]))
		count := int32(num(row["Reward_Count"]))
		if itemID <= 0 || count <= 0 {
			continue
		}
		_, equipOK := tables.equipBase[int64(itemID)]
		_, goodsOK := tables.goodsBase[int64(itemID)]
		_, materialOK := tables.materialBase[int64(itemID)]
		if !equipOK && !goodsOK && !materialOK {
			continue
		}
		grants = append(grants, bagGrant{itemID: itemID, count: count, source: "任务奖励"})
	}
	return grants
}

func taskRewardGrantsSelected(tb map[string]interface{}, indexList []int32) ([]bagGrant, string) {
	if tb == nil {
		return nil, "任务奖励配置无效"
	}
	rewardCount := int32(num(tb["RwardCount"]))
	if rewardCount <= 0 {
		return taskRewardGrants(tb), ""
	}
	if message := taskRewardSelectionMessage(tb, indexList); message != "" {
		return nil, message
	}
	rewards := arrOf(tb["RewardArr"])
	grants := make([]bagGrant, 0, len(indexList))
	for _, index := range indexList {
		row, _ := rewards[index].(map[string]interface{})
		if row == nil {
			return nil, "奖励配置无效"
		}
		itemID := int32(num(row["Reward_Id"]))
		count := int32(num(row["Reward_Count"]))
		if itemID <= 0 || count <= 0 {
			return nil, "奖励配置无效"
		}
		if tables == nil {
			return nil, "配置未加载"
		}
		_, equipOK := tables.equipBase[int64(itemID)]
		_, goodsOK := tables.goodsBase[int64(itemID)]
		_, materialOK := tables.materialBase[int64(itemID)]
		if !equipOK && !goodsOK && !materialOK {
			return nil, "奖励配置无效"
		}
		grants = append(grants, bagGrant{itemID: itemID, count: count, source: "任务奖励"})
	}
	return grants, ""
}

// taskRewardSelectionMessage validates only the player-provided selection.
// Configuration and inventory errors remain normal RPC errors, while an
// incorrect count/index/duplicate can be corrected directly in the task UI.
func taskRewardSelectionMessage(tb map[string]interface{}, indexList []int32) string {
	if tb == nil {
		return ""
	}
	rewardCount := int32(num(tb["RwardCount"]))
	if rewardCount <= 0 {
		return ""
	}
	if len(indexList) != int(rewardCount) {
		return taskRewardSelectionRequiredMessage
	}
	rewards := arrOf(tb["RewardArr"])
	seen := make(map[int32]struct{}, len(indexList))
	for _, index := range indexList {
		if index < 0 || int(index) >= len(rewards) {
			return "奖励选择无效"
		}
		if _, exists := seen[index]; exists {
			return "奖励不能重复选择"
		}
		seen[index] = struct{}{}
	}
	return ""
}

func stageTaskCompletion(ss *session, tb map[string]interface{}) (map[int32]*bagItem, []bagGrant, string) {
	return stageTaskCompletionWithSelection(ss, tb, nil)
}

func stageTaskCompletionWithSelection(ss *session, tb map[string]interface{}, indexList []int32) (map[int32]*bagItem, []bagGrant, string) {
	if ss == nil || ss.bag == nil {
		return nil, nil, "背包状态无效"
	}
	shadow := &session{bag: cloneBagMap(ss.bag)}
	canonicalizeStarCoins(shadow)
	targetType := int32(num(tb["TargetType"]))
	if targetType == taskTargetCollect || targetType == taskTargetSubmit {
		for _, cost := range taskMaterialCosts(tb) {
			if bagItemCount(shadow, cost.itemID) < cost.count {
				return nil, nil, "任务材料不足"
			}
			shadow.removeBagCountByItem(cost.itemID, cost.count)
		}
	}
	grants, message := taskRewardGrantsSelected(tb, indexList)
	if message != "" {
		return nil, nil, message
	}
	for _, grant := range grants {
		if _, ok := addItemToBagInPlace(shadow, grant.itemID, grant.count); !ok {
			return nil, nil, "背包已满"
		}
	}
	return shadow.bag, grants, ""
}

// grantTaskRewards 发放任务奖励入背包（RewardArr 中可入包物品），并推送 M2C_SendBag。
// RewardArr: [{_t, Reward_Type(0/1/2/3), Reward_Id, Reward_Count}]。
// 物品判定：EquipBase→装备(1)、GoodsBase→物品(2)、其他→材料(3)；非物品（货币等）跳过。
func (s *Server) grantTaskRewards(ch *channel, tb map[string]interface{}) bool {
	ss := ch.session
	if ss == nil || tables == nil {
		return false
	}
	grants := taskRewardGrants(tb)
	if len(grants) == 0 {
		return true
	}
	starBefore := starCoinBalance(ss)
	staged, indices, ok := stageBagGrants(ss, grants)
	if !ok {
		log.Printf("[S=%d] task reward rejected: bag full grants=%d", ch.id, len(grants))
		return false
	}
	ss.bag = staged
	if starCoinBalance(ss) != starBefore {
		s.pushMoney(ch)
	}
	for index, grant := range grants {
		log.Printf("[S=%d] task reward item=%d count=%d -> bag[%d]", ch.id, grant.itemID, grant.count, indices[index])
	}
	return true
}

func (s *Server) markDialogTaskProgress(ss *session, npcID int32) bool {
	if ss == nil || npcID == 0 {
		return false
	}
	if ss.signin == nil {
		ss.signin = &signinState{}
	}
	ss.signin.ensureTaskProgress()
	changed := false
	for taskID, state := range ss.tasks {
		row, ok := taskByID(taskID)
		if state != taskStateRunning || !ok || int32(num(row["TargetType"])) != taskTargetDialog {
			continue
		}
		if int32(num(row["DialogTargetId"])) == npcID && !ss.signin.TaskDialogProgress[taskID] {
			ss.signin.TaskDialogProgress[taskID] = true
			changed = true
		}
	}
	return changed
}

func (s *Server) recordTaskMonsterKills(ss *session, monsters []*monsterUnit) {
	if ss == nil {
		return
	}
	if ss.signin == nil {
		ss.signin = &signinState{}
	}
	ss.signin.ensureTaskProgress()
	for _, monster := range monsters {
		if monster == nil || monster.alive {
			continue
		}
		monsterLevel := int32(0)
		if tables != nil {
			if row := tables.monsterBase[int64(monster.monsterID)]; row != nil {
				monsterLevel = int32(num(row["Level"]))
			}
		}
		for taskID, state := range ss.tasks {
			row, ok := taskByID(taskID)
			if state != taskStateRunning || !ok {
				continue
			}
			switch int32(num(row["TargetType"])) {
			case taskTargetKillSpecial:
				progress, exists := ss.signin.TaskKillProgress[taskID]
				if !exists {
					progress = make(map[int32]int32)
					for _, target := range arrOf(row["CountTargetArr"]) {
						entry, _ := target.(map[string]interface{})
						targetID := int32(num(entry["CountTarget_Id"]))
						if targetID > 0 {
							progress[targetID] = ss.killCount[targetID]
						}
					}
					ss.signin.TaskKillProgress[taskID] = progress
				}
				for _, target := range arrOf(row["CountTargetArr"]) {
					entry, _ := target.(map[string]interface{})
					if int32(num(entry["CountTarget_Id"])) == monster.monsterID {
						progress[monster.monsterID]++
					}
				}
			case taskTargetKillAny:
				levelWindow := int32(num(row["AnyTargetLevel"]))
				if levelWindow <= 0 || monsterLevel+levelWindow >= ss.level {
					ss.signin.TaskAnyKillProgress[taskID]++
				}
			}
		}
		ss.killCount[monster.monsterID]++
	}
}

func taskProgressValues(ss *session, taskID int32, tb map[string]interface{}) []int32 {
	switch int32(num(tb["TargetType"])) {
	case taskTargetKillSpecial:
		var values []int32
		for _, target := range arrOf(tb["CountTargetArr"]) {
			entry, _ := target.(map[string]interface{})
			monsterID := int32(num(entry["CountTarget_Id"]))
			need := int32(num(entry["CountTarget_Count"]))
			current := taskKillCount(ss, taskID, monsterID)
			if current > need {
				current = need
			}
			values = append(values, current)
		}
		return values
	case taskTargetKillAny:
		current := taskAnyKillCount(ss, taskID)
		if need := int32(num(tb["AnyTargetCount"])); need > 0 && current > need {
			current = need
		}
		return []int32{current}
	case taskTargetCollect, taskTargetSubmit:
		values := make([]int32, 0)
		for _, cost := range taskMaterialCosts(tb) {
			current := bagItemCount(ss, cost.itemID)
			if current > cost.count {
				current = cost.count
			}
			values = append(values, current)
		}
		return values
	case taskTargetDialog, taskTargetLevel, taskTargetChangeMap, taskTargetQuiz:
		if (&Server{}).taskCompleted(ss, tb) {
			return []int32{1}
		}
		return []int32{0}
	default:
		return nil
	}
}

// onGetTask：20221 → 20222。任务日志查询。
// 响应 M2C_GetTask.TaskList(tag1, 字段级 List<TansferTask{Id, CurrCompleteList}>)：
// 每个进行中任务一条；KillSpecial 任务 CurrCompleteList = 各 CountTarget 当前击杀数
// （客户端据此显示"1/3、2/3"进度）。
func (s *Server) onGetTask(ch *channel, req *protocol.C2M_GetTask) proto.Message {
	ss := ch.session
	if ch == nil || ss == nil || ss.playerID == 0 {
		resp := &protocol.M2C_GetTask{RpcId: req.RpcId}
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	base, err := proto.Marshal(&protocol.M2C_GetTask{RpcId: req.RpcId})
	if err != nil {
		return &protocol.M2C_GetTask{RpcId: req.RpcId}
	}
	count := 0
	for id, state := range ss.tasks {
		if state != taskStateRunning {
			continue
		}
		tt := &protocol.TansferTask{Id: id}
		if tb, ok := taskByID(id); ok {
			tt.CurrCompleteList = append(tt.CurrCompleteList, taskProgressValues(ss, id, tb)...)
		}
		raw, err2 := proto.Marshal(tt)
		if err2 == nil {
			base = pbAppendBytes(base, 1, raw)
			count++
		}
	}
	s.sendRawPush(ch, protocol.OpM2C_GetTask, base)
	log.Printf("[S=%d] get task list count=%d", ch.id, count)
	return nil
}

// onGetTaskState：20223 → 20224。客户端创建完当前场景 NPC 后，用响应中的
// NPCStateList 一次性刷新每个 NPC 的头顶任务图标。
func (s *Server) onGetTaskState(ch *channel, req *protocol.C2M_GetTaskState) proto.Message {
	states := s.currentNPCTaskStateList(ch.session)
	log.Printf("[S=%d] get npc task state count=%d", ch.id, len(states))
	return &protocol.M2C_GetTaskState{RpcId: req.RpcId, NPCStateList: states}
}

func (s *Server) taskDisplayState(ss *session, taskID int32) protocol.TaskState {
	state := ss.tasks[taskID]
	if state != taskStateRunning {
		return protocol.TaskState(state)
	}
	tb, ok := taskByID(taskID)
	if ok && s.taskCompleted(ss, tb) {
		return protocol.TaskState_TaskCompleted
	}
	return protocol.TaskState_TaskRunning
}

func mergeNPCTaskState(states map[int32]protocol.TaskState, npcID int32, state protocol.TaskState) {
	if current, exists := states[npcID]; !exists || state > current {
		states[npcID] = state
	}
}

// currentNPCTaskStates converts task-centric persistence into the NPC-centric
// state expected by NPCComponent.ChangeNPCTaskState. One NPC has one controller,
// so a completable task wins over a running task, which wins over a waiting task.
//
// 进行中的任务同时挂在接取 NPC 和提交 NPC 上：线上抓包里两个 NPC 都返回
// TaskRunning(2)（灰色感叹号），达成条件后只有提交 NPC 变成 TaskCompleted。
func (s *Server) currentNPCTaskStates(ss *session) map[int32]protocol.TaskState {
	states := make(map[int32]protocol.TaskState)
	if ss == nil || tables == nil {
		return states
	}
	for _, tb := range tables.taskBase {
		mergeNPCTaskState(states, int32(num(tb["GiveNPCId"])), protocol.TaskState_TaskNoneState)
		mergeNPCTaskState(states, int32(num(tb["SubmitNPCId"])), protocol.TaskState_TaskNoneState)
	}
	for id, state := range ss.tasks {
		if state != taskStateRunning && state != taskStateWaiting {
			continue
		}
		tb, ok := taskByID(id)
		if !ok {
			continue
		}
		giveNPC := int32(num(tb["GiveNPCId"]))
		if state == taskStateWaiting {
			// NPC id 0 is intentional for the introductory root task: the
			// online GetTaskState response reports it as Waiting before the
			// player accepts it from the auto-opened window.
			mergeNPCTaskState(states, giveNPC, protocol.TaskState_TaskWaiting)
			continue
		}
		mergeNPCTaskState(states, giveNPC, protocol.TaskState_TaskRunning)
		mergeNPCTaskState(states, int32(num(tb["SubmitNPCId"])), s.taskDisplayState(ss, id))
	}
	for id, tb := range tables.taskBase {
		taskID := int32(id)
		if _, exists := ss.tasks[taskID]; exists {
			continue
		}
		if pre := taskPrerequisiteID(taskID, tb); pre != 0 && ss.tasks[pre] != taskStateCompleted {
			continue
		}
		if minLevel := int32(num(tb["Level"])); minLevel > 0 && ss.level < minLevel {
			continue
		}
		mergeNPCTaskState(states, int32(num(tb["GiveNPCId"])), protocol.TaskState_TaskWaiting)
	}
	return states
}

func (s *Server) currentNPCTaskStateList(ss *session) []*protocol.NPCTask {
	states := s.currentNPCTaskStates(ss)
	ids := make([]int, 0, len(states))
	for id := range states {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	result := make([]*protocol.NPCTask, 0, len(ids))
	for _, id := range ids {
		npcID := int32(id)
		result = append(result, &protocol.NPCTask{Id: npcID, TaskState: states[npcID]})
	}
	return result
}

func (s *Server) pushCurrentTaskStates(ch *channel) int {
	states := s.currentNPCTaskStateList(ch.session)
	for _, state := range states {
		s.sendPush(ch, protocol.OpM2C_SendTaskState, &protocol.M2C_SendTaskState{
			NPCState: state,
			ActorId:  ch.session.playerID,
		})
	}
	return len(states)
}

// pushInitialTaskUI reproduces the online first-entry flow for a newly
// created role. It is deliberately one-shot: reconnecting an existing role
// must not open the NPC task window automatically.
func (s *Server) pushInitialTaskUI(ch *channel) bool {
	if ch == nil || ch.session == nil || !ch.session.initialTaskUIPending {
		return false
	}
	var list []*protocol.NPCTask
	for id, state := range ch.session.tasks {
		if state != taskStateWaiting {
			continue
		}
		tb, ok := taskByID(id)
		if !ok || int32(num(tb["PreTaskId"])) != 0 || int32(num(tb["GiveNPCId"])) != 0 ||
			int32(num(tb["SubmitNPCId"])) == 0 || int32(num(tb["TargetType"])) == 0 {
			continue
		}
		list = append(list, &protocol.NPCTask{Id: id, TaskState: protocol.TaskState_TaskWaiting})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Id < list[j].Id })
	ch.session.initialTaskUIPending = false
	if len(list) == 0 {
		return false
	}
	// Online 20216 pushes contain only TaskList for this unsolicited message.
	s.sendPush(ch, protocol.OpM2C_OpenTaskUI, &protocol.M2C_OpenTaskUI{TaskList: list})
	log.Printf("[S=%d] push initial task ui tasks=%d", ch.id, len(list))
	return true
}

// pushTaskProgressAfterKill 击杀后刷新所有进行中任务的显示状态（KillSpecial 达标 →
// 推 TaskCompleted，客户端面板出现"可提交"）。胜利结算后调用。
func (s *Server) pushTaskProgressAfterKill(ch *channel) {
	if ch == nil || ch.session == nil {
		return
	}
	s.pushCurrentTaskStates(ch)
}

// autoAcceptInitialTasks 保留历史函数名，但按原版把有效根任务初始化为
// Waiting（可接受），而不是在 EnterGame 时直接接取。只有 onCreateRole
// 设置了 newRolePending 的新角色，才会安排首次任务面板主动推送；已有角色
// 即使缺少这条任务记录，也只补数据，不主动弹窗。
func (s *Server) autoAcceptInitialTasks(ch *channel) {
	ss := ch.session
	if ss == nil || tables == nil {
		return
	}
	newRole := ss.newRolePending
	ss.newRolePending = false
	repaired := false
	legacyAutoAccept := false
	for id, state := range ss.tasks {
		tb, ok := taskByID(id)
		if state == taskStateRunning && (!ok || int32(num(tb["TargetType"])) == 0 || int32(num(tb["SubmitNPCId"])) == 0) {
			delete(ss.tasks, id)
			legacyAutoAccept = true
			repaired = true
		}
	}
	if legacyAutoAccept {
		for id, state := range ss.tasks {
			tb, ok := taskByID(id)
			if !ok || state != taskStateRunning || int32(num(tb["PreTaskId"])) != 0 {
				continue
			}
			if int32(num(tb["GiveNPCId"])) != 0 {
				delete(ss.tasks, id)
				repaired = true
				continue
			}
			// Older server versions auto-accepted the valid root task too. If
			// the same save contains an invalid legacy task, treat that root
			// Running state as the old auto-accept artifact and let the player
			// accept it from the native introductory window.
			if int32(num(tb["SubmitNPCId"])) != 0 && int32(num(tb["TargetType"])) != 0 {
				ss.tasks[id] = taskStateWaiting
				repaired = true
			}
		}
	}
	var picked int
	for id, tb := range tables.taskBase {
		if int32(num(tb["PreTaskId"])) != 0 ||
			int32(num(tb["GiveNPCId"])) != 0 ||
			int32(num(tb["SubmitNPCId"])) == 0 ||
			int32(num(tb["TargetType"])) == 0 {
			continue
		}
		tid := int32(id)
		if _, ok := ss.tasks[tid]; ok {
			continue
		}
		ss.tasks[tid] = taskStateWaiting
		picked++
		log.Printf("[S=%d] initialize initial task %d as waiting", ch.id, tid)
	}
	if newRole {
		ss.initialTaskUIPending = true
	}
	if picked > 0 || repaired {
		s.saveData(ch)
	}
}
