package main

import (
	"log"
	"sort"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// 任务状态（protocol.TaskState 枚举值，session.tasks 以 int32 存储）
const (
	taskStateWaiting   = int32(protocol.TaskState_TaskWaiting)   // 1 可接
	taskStateRunning   = int32(protocol.TaskState_TaskRunning)   // 2 进行中
	taskStateCompleted = int32(protocol.TaskState_TaskCompleted) // 3 已完成
)

// task.go：任务系统（20206-20225 段）。
//
// 任务数据模型：
//   session.tasks    taskID → TaskState（0 无/1 可接/2 进行中/3 已完成）
//   session.killCount 怪物ID → 已击杀数（KillSpecial 任务计数，战斗胜利时累加）
//
// 流程：新号 EnterGame 时 autoAcceptInitialTasks 自动接取无前置的初始任务链（10011 开始旅程！），
// 点击 NPC 1012 苹果JJ → M2C_OpenTaskUI(20216) 弹出任务面板 → AcceptTask 接取 → 达成条件后
// CompleteTask 提交结算奖励。

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
	ch.session.lastNPCID = npcID
	if s.markDialogTaskProgress(ch.session, npcID) {
		s.saveData(ch)
	}

	var list []*protocol.NPCTask
	seen := make(map[int32]bool)
	// 1) 进行中的任务（交任务 NPC 匹配 → 可提交）
	for id, st := range ch.session.tasks {
		if st != taskStateRunning {
			continue
		}
		tb, ok := taskByID(id)
		if !ok {
			continue
		}
		if int32(num(tb["SubmitNPCId"])) != npcID {
			continue
		}
		displayState := protocol.TaskState_TaskRunning
		if s.taskCompleted(ch.session, tb) {
			displayState = protocol.TaskState_TaskCompleted
		}
		list = append(list, &protocol.NPCTask{Id: id, TaskState: displayState})
		seen[id] = true
	}
	// 2) 可接取的任务（接任务 NPC 匹配 + 前置已完成）
	for id, tb := range tables.taskBase {
		tid := int32(id)
		if seen[tid] {
			continue
		}
		if int32(num(tb["GiveNPCId"])) != npcID {
			continue
		}
		if _, accepted := ch.session.tasks[tid]; accepted {
			continue
		}
		if pre := int32(num(tb["PreTaskId"])); pre != 0 &&
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
			Card: quizCardCount, ActorId: ch.session.playerID,
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
	if ch.session.tasks[req.TaskId] != 0 {
		log.Printf("[S=%d] duplicate accept task %d ignored", ch.id, req.TaskId)
		return resp
	}
	if pre := int32(num(tb["PreTaskId"])); pre != 0 &&
		ch.session.tasks[pre] != taskStateCompleted {
		resp.Error, resp.Message = errBadParam, "前置任务未完成"
		return resp
	}
	if minLevel := int32(num(tb["Level"])); minLevel > 0 && ch.session.level < minLevel {
		resp.Error, resp.Message = errBadParam, "等级不足"
		return resp
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
	s.pushTaskState(ch, req.TaskId, s.taskDisplayState(ch.session, req.TaskId))
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
	staged, grants, message := stageTaskCompletion(ch.session, tb)
	if message != "" {
		resp.Error, resp.Message = errBadParam, message
		return resp
	}
	ch.session.bag = staged
	for _, grant := range grants {
		log.Printf("[S=%d] task reward item=%d count=%d", ch.id, grant.itemID, grant.count)
	}
	ch.session.tasks[req.TaskId] = taskStateCompleted
	s.saveData(ch)
	s.pushBagSnapshot(ch)
	s.pushTaskState(ch, req.TaskId, protocol.TaskState_TaskNoneState)
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

func taskRewardGrants(tb map[string]interface{}) []bagGrant {
	if tables == nil {
		return nil
	}
	var grants []bagGrant
	for _, entry := range arrOf(tb["RewardArr"]) {
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
		grants = append(grants, bagGrant{itemID: itemID, count: count})
	}
	return grants
}

func stageTaskCompletion(ss *session, tb map[string]interface{}) (map[int32]*bagItem, []bagGrant, string) {
	if ss == nil || ss.bag == nil {
		return nil, nil, "背包状态无效"
	}
	shadow := &session{bag: cloneBagMap(ss.bag)}
	targetType := int32(num(tb["TargetType"]))
	if targetType == taskTargetCollect || targetType == taskTargetSubmit {
		for _, cost := range taskMaterialCosts(tb) {
			if bagItemCount(shadow, cost.itemID) < cost.count {
				return nil, nil, "任务材料不足"
			}
			shadow.removeBagCountByItem(cost.itemID, cost.count)
		}
	}
	grants := taskRewardGrants(tb)
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
	var grants []bagGrant
	for _, e := range arrOf(tb["RewardArr"]) {
		eo, _ := e.(map[string]interface{})
		if eo == nil {
			continue
		}
		rid := int32(num(eo["Reward_Id"]))
		cnt := int32(num(eo["Reward_Count"]))
		if rid <= 0 || cnt <= 0 {
			continue
		}
		it := newBagItem(rid)
		if it.ItemType == 0 {
			log.Printf("[S=%d] task reward id=%d skip (not item)", ch.id, rid)
			continue
		}
		grants = append(grants, bagGrant{itemID: rid, count: cnt})
	}
	if len(grants) == 0 {
		return true
	}
	staged, indices, ok := stageBagGrants(ss, grants)
	if !ok {
		log.Printf("[S=%d] task reward rejected: bag full grants=%d", ch.id, len(grants))
		return false
	}
	ss.bag = staged
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

// onGetTaskState：20223 → 20224。任务状态查询，并推送当前进行中/可接任务状态。
func (s *Server) onGetTaskState(ch *channel, req *protocol.C2M_GetTaskState) proto.Message {
	count := s.pushCurrentTaskStates(ch)
	log.Printf("[S=%d] get task state count=%d", ch.id, count)
	return &protocol.M2C_GetTaskState{RpcId: req.RpcId}
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

func (s *Server) currentTaskStates(ss *session) map[int32]protocol.TaskState {
	states := make(map[int32]protocol.TaskState)
	for id, state := range ss.tasks {
		if state == taskStateRunning {
			states[id] = s.taskDisplayState(ss, id)
		}
	}
	for id, tb := range tables.taskBase {
		taskID := int32(id)
		if _, exists := ss.tasks[taskID]; exists || int32(num(tb["GiveNPCId"])) == 0 {
			continue
		}
		if pre := int32(num(tb["PreTaskId"])); pre != 0 && ss.tasks[pre] != taskStateCompleted {
			continue
		}
		if minLevel := int32(num(tb["Level"])); minLevel > 0 && ss.level < minLevel {
			continue
		}
		states[taskID] = protocol.TaskState_TaskWaiting
	}
	return states
}

func (s *Server) pushCurrentTaskStates(ch *channel) int {
	states := s.currentTaskStates(ch.session)
	ids := make([]int, 0, len(states))
	for id := range states {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	for _, id := range ids {
		s.pushTaskState(ch, int32(id), states[int32(id)])
	}
	return len(ids)
}

func (s *Server) pushTaskState(ch *channel, taskID int32, state protocol.TaskState) {
	s.sendPush(ch, protocol.OpM2C_SendTaskState, &protocol.M2C_SendTaskState{
		NPCState: &protocol.NPCTask{Id: taskID, TaskState: state},
		ActorId:  ch.session.playerID,
	})
}

// pushTaskProgressAfterKill 击杀后刷新所有进行中任务的显示状态（KillSpecial 达标 →
// 推 TaskCompleted，客户端面板出现"可提交"）。胜利结算后调用。
func (s *Server) pushTaskProgressAfterKill(ch *channel) {
	if ch == nil || ch.session == nil {
		return
	}
	for id, state := range ch.session.tasks {
		if state != taskStateRunning {
			continue
		}
		s.pushTaskState(ch, id, s.taskDisplayState(ch.session, id))
	}
}

// autoAcceptInitialTasks 自动接取无接取 NPC 的有效根任务（如 10011 开始旅程！）。
// EnterGame 加载任务后调用，并修复旧版本误自动接取的无效根任务记录。
func (s *Server) autoAcceptInitialTasks(ch *channel) {
	ss := ch.session
	if ss == nil || tables == nil {
		return
	}
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
			if ok && state == taskStateRunning && int32(num(tb["PreTaskId"])) == 0 && int32(num(tb["GiveNPCId"])) != 0 {
				delete(ss.tasks, id)
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
		ss.tasks[tid] = taskStateRunning
		picked++
		log.Printf("[S=%d] auto accept initial task %d", ch.id, tid)
	}
	if picked > 0 || repaired {
		s.saveData(ch)
	}
}
