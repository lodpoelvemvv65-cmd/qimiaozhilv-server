package main

import (
	"encoding/json"
	"testing"

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

	if got := ch.session.tasks[10011]; got != taskStateRunning {
		t.Fatalf("task 10011 state = %d, want Running", got)
	}
	if _, ok := ch.session.tasks[10086]; ok {
		t.Fatal("legacy auto-accepted task 10086 was not removed")
	}
	if _, ok := ch.session.tasks[10087]; ok {
		t.Fatal("invalid legacy task 10087 was not removed")
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
	if got := ch.session.tasks[10011]; got != taskStateRunning {
		t.Fatalf("task 10011 state = %d, want Running", got)
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
