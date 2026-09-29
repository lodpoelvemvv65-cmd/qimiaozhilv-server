package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func taskRow(values map[string]string) map[string]interface{} {
	row := make(map[string]interface{}, len(values))
	for key, value := range values {
		row[key] = json.Number(value)
	}
	return row
}

func TestAutoAcceptInitialTasksRepairsLegacyData(t *testing.T) {
	oldTables := tables
	defer func() { tables = oldTables }()
	tables = &datatables{taskBase: map[int64]map[string]interface{}{
		10011: taskRow(map[string]string{"PreTaskId": "0", "GiveNPCId": "0", "SubmitNPCId": "1012", "TargetType": "4"}),
		10086: taskRow(map[string]string{"PreTaskId": "0", "GiveNPCId": "1012", "SubmitNPCId": "1012", "TargetType": "4"}),
		10087: taskRow(map[string]string{"PreTaskId": "0", "GiveNPCId": "0", "SubmitNPCId": "0", "TargetType": "0"}),
	}}

	ch := &channel{session: &session{tasks: map[int32]int32{
		10011: taskStateRunning,
		10086: taskStateRunning,
		10087: taskStateRunning,
	}}}
	(&Server{}).autoAcceptInitialTasks(ch)

	if got := ch.session.tasks[10011]; got != taskStateWaiting {
		t.Fatalf("task 10011 state = %d, want Waiting", got)
	}
	if _, ok := ch.session.tasks[10086]; ok {
		t.Fatal("legacy auto-accepted task 10086 was not removed")
	}
	if _, ok := ch.session.tasks[10087]; ok {
		t.Fatal("invalid legacy task 10087 was not removed")
	}
	if ch.session.initialTaskUIPending {
		t.Fatal("existing role unexpectedly scheduled an unsolicited task window")
	}
}

func TestAutoAcceptInitialTasksSchedulesWindowOnlyForCreatedRole(t *testing.T) {
	oldTables := tables
	defer func() { tables = oldTables }()
	tables = &datatables{taskBase: map[int64]map[string]interface{}{
		10011: taskRow(map[string]string{"PreTaskId": "0", "GiveNPCId": "0", "SubmitNPCId": "1012", "TargetType": "4"}),
	}}

	ch := &channel{session: &session{
		newRolePending: true,
		tasks:          make(map[int32]int32),
	}}
	(&Server{}).autoAcceptInitialTasks(ch)
	if !ch.session.initialTaskUIPending {
		t.Fatal("created role did not schedule its initial task window")
	}
	if ch.session.newRolePending {
		t.Fatal("new-role marker was not consumed by EnterGame task initialization")
	}
}

func TestAutoAcceptInitialTasksPreservesLegitimateAcceptedTask(t *testing.T) {
	oldTables := tables
	defer func() { tables = oldTables }()
	tables = &datatables{taskBase: map[int64]map[string]interface{}{
		10011: taskRow(map[string]string{"PreTaskId": "0", "GiveNPCId": "0", "SubmitNPCId": "1012", "TargetType": "4"}),
		10086: taskRow(map[string]string{"PreTaskId": "0", "GiveNPCId": "1012", "SubmitNPCId": "1012", "TargetType": "4"}),
	}}

	ch := &channel{session: &session{tasks: map[int32]int32{10086: taskStateRunning}}}
	(&Server{}).autoAcceptInitialTasks(ch)

	if got := ch.session.tasks[10086]; got != taskStateRunning {
		t.Fatalf("legitimate task 10086 state = %d, want Running", got)
	}
	if got := ch.session.tasks[10011]; got != taskStateWaiting {
		t.Fatalf("task 10011 state = %d, want Waiting", got)
	}
}

func TestTaskCompletedUsesTargetLevel(t *testing.T) {
	server := &Server{}
	row := taskRow(map[string]string{"TargetType": "5", "Level": "10", "TargetLevel": "100"})
	if server.taskCompleted(&session{level: 99}, row) {
		t.Fatal("level task completed before TargetLevel")
	}
	if !server.taskCompleted(&session{level: 100}, row) {
		t.Fatal("level task did not complete at TargetLevel")
	}
}

func TestCityOfficerFirstTasksDoNotRequireBeachChain(t *testing.T) {
	loadOnlineTablesForTest(t)
	for _, test := range []struct {
		taskID int32
		want   int32
	}{
		{taskID: 10017, want: 0}, // first rush-level task, online pre-task 10016
		{taskID: 10035, want: 0}, // first daily officer task, online pre-task 10017
		{taskID: 10041, want: 0}, // first leveling officer task, online pre-task 10017
		{taskID: 10046, want: 0}, // first leveling-map task, online pre-task 10016
		{taskID: 10018, want: 10017},
		{taskID: 10042, want: 10041},
		{taskID: 10047, want: 10046},
	} {
		t.Run(fmt.Sprintf("task_%d", test.taskID), func(t *testing.T) {
			row, ok := taskByID(test.taskID)
			if !ok {
				t.Fatalf("missing online task %d", test.taskID)
			}
			if got := taskPrerequisiteID(test.taskID, row); got != test.want {
				t.Fatalf("task %d prerequisite=%d, want %d", test.taskID, got, test.want)
			}
		})
	}
}

func TestClickCityOfficerShowsFirstTaskBeforeBeachCompletion(t *testing.T) {
	loadOnlineTablesForTest(t)
	for _, test := range []struct {
		npcID  int32
		taskID int32
		level  int32
		x, y   float32
	}{
		{npcID: 1005, taskID: 10046, level: 100, x: -9.378, y: -0.92},
		{npcID: 1006, taskID: 10035, level: 250, x: -7.632, y: -0.94},
		{npcID: 1007, taskID: 10017, level: 10, x: -5.724, y: -0.94},
	} {
		t.Run(fmt.Sprintf("npc_%d", test.npcID), func(t *testing.T) {
			ss := newSession()
			ss.playerID, ss.level, ss.mapID = 7, test.level, 10004
			ss.resetMovement(test.x, test.y)
			ss.tasks = make(map[int32]int32)
			ch := &channel{id: 1, conn: &recordingConn{}, session: ss}
			response := (&Server{}).onClickNPC(ch, &protocol.C2M_ClickNPC{NPCId: test.npcID}).(*protocol.M2C_ClickNPC)
			if response.Error != 0 {
				t.Fatalf("click npc response=%+v", response)
			}
			frames := decodeRecordedFrames(t, ch.conn.(*recordingConn).Bytes())
			if len(frames) != 1 || frames[0].opcode != protocol.OpM2C_OpenTaskUI {
				t.Fatalf("npc frames=%v, want one OpenTaskUI", recordedOpcodes(t, ch.conn.(*recordingConn).Bytes()))
			}
			var opened protocol.M2C_OpenTaskUI
			if err := proto.Unmarshal(frames[0].body, &opened); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, task := range opened.TaskList {
				if task.Id == test.taskID && task.TaskState == protocol.TaskState_TaskWaiting {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("npc task list=%+v, missing waiting task %d", opened.TaskList, test.taskID)
			}
		})
	}
}

func TestAcceptCityOfficerFirstTaskBeforeBeachCompletion(t *testing.T) {
	loadOnlineTablesForTest(t)
	for _, test := range []struct {
		npcID  int32
		taskID int32
		level  int32
		x, y   float32
	}{
		{npcID: 1005, taskID: 10046, level: 100, x: -9.378, y: -0.92},
		{npcID: 1006, taskID: 10035, level: 250, x: -7.632, y: -0.94},
		{npcID: 1007, taskID: 10017, level: 10, x: -5.724, y: -0.94},
	} {
		t.Run(fmt.Sprintf("npc_%d_task_%d", test.npcID, test.taskID), func(t *testing.T) {
			ss := newSession()
			ss.playerID, ss.level, ss.mapID = 7, test.level, 10004
			ss.resetMovement(test.x, test.y)
			ss.tasks = make(map[int32]int32)
			ch := &channel{id: 1, conn: &recordingConn{}, session: ss}
			if response := (&Server{}).onClickNPC(ch, &protocol.C2M_ClickNPC{NPCId: test.npcID}).(*protocol.M2C_ClickNPC); response.Error != 0 {
				t.Fatalf("click npc response=%+v", response)
			}
			response := (&Server{}).onAcceptTask(ch, &protocol.C2M_AcceptTask{TaskId: test.taskID}).(*protocol.M2C_AcceptTask)
			if response.Error != 0 {
				t.Fatalf("accept task response=%+v", response)
			}
			if got := ss.tasks[test.taskID]; got != taskStateRunning {
				t.Fatalf("task %d state=%d, want Running", test.taskID, got)
			}
		})
	}
}

func TestStaleTaskPanelRequestsAreIdempotent(t *testing.T) {
	oldTables := tables
	defer func() { tables = oldTables }()
	tables = &datatables{taskBase: map[int64]map[string]interface{}{
		10011: taskRow(map[string]string{"TargetType": "4"}),
		10012: taskRow(map[string]string{"TargetType": "1"}),
	}}
	ch := &channel{session: &session{tasks: map[int32]int32{
		10011: taskStateCompleted,
		10012: taskStateRunning,
	}}}

	complete := (&Server{}).onCompleteTask(ch, &protocol.C2M_CompleteTask{TaskId: 10011})
	if complete.(*protocol.M2C_CompleteTask).Error != 0 || complete.(*protocol.M2C_CompleteTask).Message != "" {
		t.Fatalf("duplicate completion returned an RPC error: %+v", complete)
	}
	accept := (&Server{}).onAcceptTask(ch, &protocol.C2M_AcceptTask{TaskId: 10012})
	if accept.(*protocol.M2C_AcceptTask).Error != 0 || accept.(*protocol.M2C_AcceptTask).Message != "" {
		t.Fatalf("duplicate acceptance returned an RPC error: %+v", accept)
	}
}

func TestNPCInteractionRequiresConfiguredMapLayerAndDistance(t *testing.T) {
	loadOnlineTablesForTest(t)
	tests := []struct {
		name  string
		mapID int32
		x, y  float32
		npcID int32
		want  string
	}{
		{name: "valid beach npc", mapID: 1000601, x: -1.8, y: -0.84, npcID: 1012},
		{name: "wrong map", mapID: 10004, x: -1.8, y: -0.84, npcID: 1012, want: "地图"},
		{name: "wrong layer", mapID: 1000602, x: -1.8, y: -0.84, npcID: 1012, want: "地图"},
		{name: "too far", mapID: 1000601, x: 1, y: -0.84, npcID: 1012, want: "太远"},
		{name: "valid city npc", mapID: 10004, x: -5.724, y: -0.94, npcID: 1007},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ss := newSession()
			ss.mapID = test.mapID
			ss.resetMovement(test.x, test.y)
			got := validateNPCInteraction(ss, test.npcID)
			if test.want == "" && got != "" {
				t.Fatalf("valid interaction rejected: %q", got)
			}
			if test.want != "" && !strings.Contains(got, test.want) {
				t.Fatalf("interaction error = %q, want substring %q", got, test.want)
			}
		})
	}
	ss := newSession()
	ss.playerID, ss.mapID = 7, 10004
	ss.resetMovement(-1.8, -0.84)
	ss.tasks = make(map[int32]int32)
	response := (&Server{}).onClickNPC(&channel{session: ss}, &protocol.C2M_ClickNPC{NPCId: 1012}).(*protocol.M2C_ClickNPC)
	if response.Error == 0 || ss.lastNPCID != 0 {
		t.Fatalf("spoofed click was not rejected: response=%+v lastNPC=%d", response, ss.lastNPCID)
	}
}

func TestAcceptTaskRequiresCurrentNPCInteraction(t *testing.T) {
	loadOnlineTablesForTest(t)
	makeSession := func() *session {
		ss := newSession()
		ss.playerID, ss.level, ss.mapID = 7, 1, 1000601
		ss.resetMovement(-1.8, -0.84)
		ss.tasks = map[int32]int32{10011: taskStateCompleted}
		ss.signin = &signinState{}
		ss.signin.ensureTaskProgress()
		return ss
	}

	withoutClick := makeSession()
	rejected := (&Server{}).onAcceptTask(&channel{session: withoutClick}, &protocol.C2M_AcceptTask{TaskId: 10012}).(*protocol.M2C_AcceptTask)
	if rejected.Error == 0 || withoutClick.tasks[10012] != 0 {
		t.Fatalf("accept without NPC click was not rejected: response=%+v state=%d", rejected, withoutClick.tasks[10012])
	}

	tooFar := makeSession()
	tooFar.lastNPCID = 1012
	tooFar.resetMovement(1, -0.84)
	rejected = (&Server{}).onAcceptTask(&channel{session: tooFar}, &protocol.C2M_AcceptTask{TaskId: 10012}).(*protocol.M2C_AcceptTask)
	if rejected.Error == 0 || tooFar.tasks[10012] != 0 {
		t.Fatalf("remote NPC accept was not rejected: response=%+v state=%d", rejected, tooFar.tasks[10012])
	}

	valid := makeSession()
	valid.lastNPCID = 1012
	accepted := (&Server{}).onAcceptTask(&channel{session: valid}, &protocol.C2M_AcceptTask{TaskId: 10012}).(*protocol.M2C_AcceptTask)
	if accepted.Error != 0 || valid.tasks[10012] != taskStateRunning {
		t.Fatalf("valid NPC accept failed: response=%+v state=%d", accepted, valid.tasks[10012])
	}
}

func TestBeachFinalTaskCompletesAfterTownOfficerDialog(t *testing.T) {
	loadOnlineTablesForTest(t)
	ss := newSession()
	ss.playerID, ss.level, ss.mapID = 10, 131, 1000604
	ss.resetMovement(-1.8, -0.84)
	ss.lastNPCID = 1015
	ss.tasks = map[int32]int32{10015: taskStateCompleted}
	ss.bag = make(map[int32]*bagItem)
	ss.worn = make(map[int32]*bagItem)
	ss.signin = &signinState{}
	ss.signin.ensureTaskProgress()
	ch := &channel{id: 1, conn: &recordingConn{}, session: ss}
	server := &Server{}

	accepted := server.onAcceptTask(ch, &protocol.C2M_AcceptTask{TaskId: 10016}).(*protocol.M2C_AcceptTask)
	if accepted.Error != 0 || ss.tasks[10016] != taskStateRunning {
		t.Fatalf("task 10016 was not accepted from beach layer 4 NPC 1015: response=%+v state=%d", accepted, ss.tasks[10016])
	}
	task, ok := taskByID(10016)
	if !ok {
		t.Fatal("missing online task 10016")
	}
	if server.taskCompleted(ss, task) {
		t.Fatal("task 10016 completed before talking to town NPC 1007")
	}

	returned := server.onBackMainCity(ch, &protocol.C2M_BackMainCity{}).(*protocol.M2C_BackMainCity)
	if returned.Error != 0 || ss.mapID != 10004 {
		t.Fatalf("Ctrl+G back-to-town request failed: response=%+v map=%d", returned, ss.mapID)
	}
	ss.resetMovement(-5.724, -0.94)
	clicked := server.onClickNPC(ch, &protocol.C2M_ClickNPC{NPCId: 1007}).(*protocol.M2C_ClickNPC)
	if clicked.Error != 0 || ss.lastNPCID != 1007 || !server.taskCompleted(ss, task) {
		t.Fatalf("town NPC 1007 dialog did not complete task 10016: response=%+v lastNPC=%d", clicked, ss.lastNPCID)
	}

	completed := server.onCompleteTask(ch, &protocol.C2M_CompleteTask{
		TaskId: 10016, IndexList: []int32{0, 1},
	}).(*protocol.M2C_CompleteTask)
	if completed.Error != 0 || ss.tasks[10016] != taskStateCompleted {
		t.Fatalf("task 10016 was not submitted to town NPC 1007: response=%+v state=%d", completed, ss.tasks[10016])
	}
}

func TestCompleteTaskRevalidatesSubmitNPCMapAndDistance(t *testing.T) {
	loadOnlineTablesForTest(t)
	makeSession := func() *session {
		ss := newSession()
		ss.playerID, ss.level, ss.mapID = 8, 1, 1000601
		ss.resetMovement(-1.8, -0.84)
		ss.tasks = map[int32]int32{10011: taskStateRunning}
		ss.killCount = make(map[int32]int32)
		ss.bag = make(map[int32]*bagItem)
		ss.signin = &signinState{TaskDialogProgress: map[int32]bool{10011: true}}
		ss.signin.ensureTaskProgress()
		return ss
	}
	tests := []struct {
		name     string
		mutate   func(*session)
		wantText string
	}{
		{name: "wrong npc", mutate: func(ss *session) { ss.lastNPCID = 1013 }, wantText: "指定NPC"},
		{name: "wrong map", mutate: func(ss *session) { ss.lastNPCID, ss.mapID = 1012, 10004 }, wantText: "地图"},
		{name: "too far", mutate: func(ss *session) { ss.lastNPCID = 1012; ss.resetMovement(1, -0.84) }, wantText: "太远"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ss := makeSession()
			test.mutate(ss)
			response := (&Server{}).onCompleteTask(&channel{session: ss}, &protocol.C2M_CompleteTask{
				TaskId: 10011, IndexList: []int32{0, 1, 2},
			}).(*protocol.M2C_CompleteTask)
			if response.Error == 0 || !strings.Contains(response.Message, test.wantText) {
				t.Fatalf("invalid submit response = %+v, want %q", response, test.wantText)
			}
			if ss.tasks[10011] != taskStateRunning || len(ss.bag) != 0 {
				t.Fatalf("rejected submit mutated state: task=%d bag=%+v", ss.tasks[10011], ss.bag)
			}
		})
	}
}

func TestTaskRewardsSelectionUsesExactlyRwardCount(t *testing.T) {
	loadOnlineTablesForTest(t)
	task, ok := taskByID(10011)
	if !ok {
		t.Fatal("missing online task 10011")
	}
	grants, message := taskRewardGrantsSelected(task, []int32{0, 2, 5})
	if message != "" || len(grants) != 3 {
		t.Fatalf("selected reward grants=%+v message=%q, want 3 grants", grants, message)
	}
	want := []int32{110305, 120590, 120593}
	for index, grant := range grants {
		if grant.itemID != want[index] {
			t.Fatalf("grant[%d].itemID = %d, want %d", index, grant.itemID, want[index])
		}
	}
	for _, indexes := range [][]int32{nil, {}, {0, 1}, {0, 1, 2, 3}, {0, 0, 1}, {0, 1, 9}} {
		if _, message = taskRewardGrantsSelected(task, indexes); message == "" {
			t.Fatalf("selection %v was accepted", indexes)
		}
	}
}

func TestCompleteTaskWithInvalidSelectionReturnsNativeTip(t *testing.T) {
	loadOnlineTablesForTest(t)
	for _, test := range []struct {
		name    string
		indexes []int32
		message string
	}{
		{name: "empty", message: taskRewardSelectionRequiredMessage},
		{name: "too few", indexes: []int32{0}, message: taskRewardSelectionRequiredMessage},
		{name: "too many", indexes: []int32{0, 1, 2, 3}, message: taskRewardSelectionRequiredMessage},
		{name: "duplicate", indexes: []int32{0, 0, 1}, message: "奖励不能重复选择"},
		{name: "out of range", indexes: []int32{0, 1, 9}, message: "奖励选择无效"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ss := newSession()
			ss.playerID, ss.level, ss.mapID = 9, 1, 1000601
			ss.resetMovement(-1.8, -0.84)
			ss.lastNPCID = 1012
			ss.tasks = map[int32]int32{10011: taskStateRunning}
			ss.killCount = make(map[int32]int32)
			ss.bag = make(map[int32]*bagItem)
			ss.worn = make(map[int32]*bagItem)
			ss.signin = &signinState{TaskDialogProgress: map[int32]bool{10011: true}}
			ss.signin.ensureTaskProgress()
			ch := &channel{id: 1, conn: &recordingConn{}, session: ss}

			response := (&Server{}).onCompleteTask(ch, &protocol.C2M_CompleteTask{
				TaskId: 10011, IndexList: test.indexes,
			}).(*protocol.M2C_CompleteTask)
			if response.Error != 0 || response.Message != test.message {
				t.Fatalf("invalid reward selection response=%+v, want normal response with tip %q", response, test.message)
			}
			if ss.tasks[10011] != taskStateRunning || len(ss.bag) != 0 {
				t.Fatalf("rejected selection mutated state: task=%d bag=%+v", ss.tasks[10011], ss.bag)
			}
		})
	}
}

func TestInitialTaskUIPushIsOneShot(t *testing.T) {
	oldTables := tables
	defer func() { tables = oldTables }()
	tables = &datatables{taskBase: map[int64]map[string]interface{}{
		10011: taskRow(map[string]string{"PreTaskId": "0", "GiveNPCId": "0", "SubmitNPCId": "1012", "TargetType": "4"}),
	}}
	conn := &recordingConn{}
	ch := &channel{id: 1, conn: conn, session: &session{
		playerID:             7,
		initialTaskUIPending: true,
		tasks:                map[int32]int32{10011: taskStateWaiting},
	}}
	server := &Server{}
	if !server.pushInitialTaskUI(ch) {
		t.Fatal("initial task UI was not pushed")
	}
	if ch.session.initialTaskUIPending {
		t.Fatal("initial task UI remained pending after push")
	}
	frames := decodeRecordedFrames(t, conn.Bytes())
	if len(frames) != 1 || frames[0].opcode != protocol.OpM2C_OpenTaskUI {
		t.Fatalf("initial task frames=%v, want one OpenTaskUI", recordedOpcodes(t, conn.Bytes()))
	}
	var opened protocol.M2C_OpenTaskUI
	if err := proto.Unmarshal(frames[0].body, &opened); err != nil {
		t.Fatal(err)
	}
	if len(opened.TaskList) != 1 || opened.TaskList[0].Id != 10011 || opened.TaskList[0].TaskState != protocol.TaskState_TaskWaiting {
		t.Fatalf("initial task payload=%+v, want task 10011 Waiting", opened.TaskList)
	}
	if server.pushInitialTaskUI(ch) {
		t.Fatal("initial task UI was pushed more than once")
	}

	legacy := &channel{conn: &recordingConn{}, session: &session{
		playerID: 7,
		tasks:    map[int32]int32{10011: taskStateWaiting},
	}}
	if server.pushInitialTaskUI(legacy) {
		t.Fatal("existing role without pending marker received initial task UI")
	}
}

// 海滩任务 10012/10013 的 GiveNPCId 都是 1012、SubmitNPCId 都是 1013。线上抓包
// 显示两个任务进行时 NPC 1012 与 1013 都返回 TaskRunning(2)，不需要海滩专用逻辑。
func TestRunningTaskMarksGiveAndSubmitNPCs(t *testing.T) {
	loadOnlineTablesForTest(t)
	server := &Server{}

	inProgress := newSession()
	inProgress.playerID, inProgress.level = 31, 1
	inProgress.tasks = map[int32]int32{10012: taskStateRunning}
	inProgress.signin = &signinState{}
	inProgress.signin.ensureTaskProgress()
	states := server.currentNPCTaskStates(inProgress)
	if states[1012] != protocol.TaskState_TaskRunning || states[1013] != protocol.TaskState_TaskRunning {
		t.Fatalf("in-progress task npc1012=%v npc1013=%v, want both TaskRunning",
			states[1012], states[1013])
	}

	completable := newSession()
	completable.playerID, completable.level = 32, 1
	completable.tasks = map[int32]int32{10012: taskStateRunning, 10013: taskStateRunning}
	completable.signin = &signinState{}
	completable.signin.ensureTaskProgress()
	completable.signin.TaskDialogProgress[10013] = true
	states = server.currentNPCTaskStates(completable)
	// 条件达成的 10013 只让提交 NPC 变成 TaskCompleted，接取 NPC 仍是进行中；
	// 10012 尚未完成，所以 1013 取 Completed > Running 的最高优先级。
	if states[1012] != protocol.TaskState_TaskRunning {
		t.Fatalf("give npc state=%v, want TaskRunning", states[1012])
	}
	if states[1013] != protocol.TaskState_TaskCompleted {
		t.Fatalf("submit npc state=%v, want TaskCompleted (Completed > Running > Waiting)",
			states[1013])
	}
}

func TestGetTaskStateReturnsNPCIdsAndOriginalMarkers(t *testing.T) {
	oldTables := tables
	defer func() { tables = oldTables }()
	tables = &datatables{taskBase: map[int64]map[string]interface{}{
		10011: taskRow(map[string]string{
			"GiveNPCId": "0", "SubmitNPCId": "1012", "TargetType": "4", "Level": "1",
		}),
		10012: taskRow(map[string]string{
			"GiveNPCId": "1013", "SubmitNPCId": "1013", "TargetType": "5", "TargetLevel": "1", "Level": "1",
		}),
		10013: taskRow(map[string]string{
			"GiveNPCId": "1014", "SubmitNPCId": "1015", "TargetType": "1", "Level": "1",
		}),
	}}
	ss := newSession()
	ss.playerID, ss.level = 7, 1
	ss.tasks = map[int32]int32{10011: taskStateRunning, 10012: taskStateRunning}
	ss.signin = &signinState{}
	ss.signin.ensureTaskProgress()
	ch := &channel{id: 1, conn: &recordingConn{}, session: ss}

	response := (&Server{}).onGetTaskState(ch, &protocol.C2M_GetTaskState{RpcId: 88}).(*protocol.M2C_GetTaskState)
	if response.RpcId != 88 || len(ch.conn.(*recordingConn).Bytes()) != 0 {
		t.Fatalf("get task state response=%+v pushed=%d bytes", response, len(ch.conn.(*recordingConn).Bytes()))
	}
	// 进行中的任务 10011 同时把接取 NPC 与提交 NPC 标成 TaskRunning；
	// 条件达成的 10012 只让提交 NPC 变成 TaskCompleted。
	want := []*protocol.NPCTask{
		{Id: 0, TaskState: protocol.TaskState_TaskRunning},
		{Id: 1012, TaskState: protocol.TaskState_TaskRunning},
		{Id: 1013, TaskState: protocol.TaskState_TaskCompleted},
		{Id: 1014, TaskState: protocol.TaskState_TaskWaiting},
		{Id: 1015, TaskState: protocol.TaskState_TaskNoneState},
	}
	if !reflect.DeepEqual(response.NPCStateList, want) {
		t.Fatalf("npc task states=%+v, want %+v", response.NPCStateList, want)
	}
}

func TestTaskStatePushUsesNPCIdsInsteadOfTaskIds(t *testing.T) {
	oldTables := tables
	defer func() { tables = oldTables }()
	tables = &datatables{taskBase: map[int64]map[string]interface{}{
		10011: taskRow(map[string]string{
			"GiveNPCId": "0", "SubmitNPCId": "1012", "TargetType": "4", "Level": "1",
		}),
	}}
	ss := newSession()
	ss.playerID, ss.level = 9, 1
	ss.tasks = map[int32]int32{10011: taskStateRunning}
	ss.signin = &signinState{}
	ss.signin.ensureTaskProgress()
	ch := &channel{id: 1, conn: &recordingConn{}, session: ss}

	if count := (&Server{}).pushCurrentTaskStates(ch); count != 2 {
		t.Fatalf("pushed npc task state count=%d, want 2", count)
	}
	got := make(map[int32]protocol.TaskState)
	for _, frame := range decodeRecordedFrames(t, ch.conn.(*recordingConn).Bytes()) {
		if frame.opcode != protocol.OpM2C_SendTaskState {
			t.Fatalf("task state opcode=%d", frame.opcode)
		}
		var message protocol.M2C_SendTaskState
		if err := proto.Unmarshal(frame.body, &message); err != nil {
			t.Fatal(err)
		}
		got[message.NPCState.Id] = message.NPCState.TaskState
	}
	want := map[int32]protocol.TaskState{
		0: protocol.TaskState_TaskRunning, 1012: protocol.TaskState_TaskRunning,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pushed npc task states=%v, want %v", got, want)
	}
}

func TestSelectEnemy(t *testing.T) {
	battle := &battleState{monsters: []*monsterUnit{
		{id: 1, alive: true},
		{id: 2, alive: true},
	}}
	ch := &channel{session: &session{battle: battle}}
	resp := (&Server{}).onSelectEnermy(ch, &protocol.C2M_SelectEnermy{Id: 2})
	if resp.(*protocol.M2C_SelectEnermy).Error != 0 {
		t.Fatal("selecting a live enemy returned an error")
	}
	if battle.selectedID != 2 {
		t.Fatalf("selected enemy = %d, want 2", battle.selectedID)
	}
}
