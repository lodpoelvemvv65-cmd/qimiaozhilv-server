package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func manualEquipFieldUnitID(t *testing.T, ch *channel) int64 {
	t.Helper()
	if ch == nil || ch.session == nil || len(ch.session.fieldMonsterConfigIDs) != 1 {
		t.Fatalf("manual field monsters = %v, want exactly one", ch.session.fieldMonsterConfigIDs)
	}
	for unitID := range ch.session.fieldMonsterConfigIDs {
		return unitID
	}
	return 0
}

func manualEquipPartyForTest(t *testing.T, mapID int32, count int) (*Server, []*channel) {
	t.Helper()
	teamMu.Lock()
	oldTeams, oldSeq := teams, teamSeq
	teams, teamSeq = make(map[int64]*teamState), 1
	teamMu.Unlock()
	t.Cleanup(func() {
		teamMu.Lock()
		teams, teamSeq = oldTeams, oldSeq
		teamMu.Unlock()
	})

	server := &Server{conns: make(map[int64]*channel)}
	members := make([]*channel, 0, count)
	ids := make([]int64, 0, count)
	for index := 0; index < count; index++ {
		ch, _ := consistencyChannel(int64(7100+index), mapID)
		ss := ch.session
		ss.name = "manual-member"
		ss.teamID = 1
		ss.signin = &signinState{}
		ss.bag = make(map[int32]*bagItem)
		ss.tasks = make(map[int32]int32)
		ss.killCount = make(map[int32]int32)
		server.conns[ch.id] = ch
		server.pushFieldMonsters(ch)
		members = append(members, ch)
		ids = append(ids, ss.playerID)
	}
	teamMu.Lock()
	teams[1] = &teamState{LeaderId: ids[0], Members: ids}
	teamMu.Unlock()
	return server, members
}

func TestManualEquipProgressCyclesAndResumesFixedClientEntry(t *testing.T) {
	ss := newSession()
	const firstMap = int32(1003301)
	if got := manualEquipEntryResumeMap(ss, firstMap); got != firstMap {
		t.Fatalf("fresh entry = %d, want %d", got, firstMap)
	}
	for layer := int32(1); layer <= manualEquipLayerCount; layer++ {
		mapID := int32(1003300) + layer
		if !recordManualEquipVictory(ss, mapID) {
			t.Fatalf("layer %d victory did not update progress", layer)
		}
		if got := manualEquipProgress(ss, 1); got != layer {
			t.Fatalf("layer %d progress = %d", layer, got)
		}
		wantNext := layer + 1
		if wantNext > manualEquipLayerCount {
			wantNext = 1
		}
		if got := manualEquipEntryResumeMap(ss, firstMap); got != 1003300+wantNext {
			t.Fatalf("after layer %d entry = %d, want layer %d", layer, got, wantNext)
		}
	}
	if !recordManualEquipVictory(ss, firstMap) || manualEquipProgress(ss, 1) != 1 {
		t.Fatalf("new cycle layer one did not reset progress: %+v", ss.signin)
	}
}

func TestManualEquipExitPortalAdvancesOnlyAfterVictory(t *testing.T) {
	ss := newSession()
	ss.mapID = 1003301
	if got := manualEquipPortalTargetMap(ss, 10004); got != 10004 {
		t.Fatalf("unfinished layer portal target = %d, want city", got)
	}
	if !setManualEquipProgress(ss, 1, 1) {
		t.Fatal("recording layer-one progress failed")
	}
	if got := manualEquipPortalTargetMap(ss, 10004); got != 1003302 {
		t.Fatalf("layer-one portal target = %d, want 1003302", got)
	}
	if got := manualEquipPortalTargetMap(ss, 1000401); got != 1003302 {
		t.Fatalf("legacy city portal target = %d, want 1003302", got)
	}

	ss.mapID = 1003304
	if !setManualEquipProgress(ss, 1, 4) {
		t.Fatal("recording layer-four progress failed")
	}
	if got := manualEquipPortalTargetMap(ss, 10004); got != 10004 {
		t.Fatalf("layer-four portal target = %d, want city", got)
	}
	ss.mapID = 1003301
	if got := manualEquipPortalTargetMap(ss, 10004); got != 10004 {
		t.Fatalf("new-cycle layer-one portal target = %d, want city", got)
	}
}

func TestManualEquipExitPortalMovesPartyToNextLayer(t *testing.T) {
	loadOnlineTablesForTest(t)
	server, members := manualEquipPartyForTest(t, 1003301, 3)
	for _, member := range members {
		cancelReturnTestMapStartup(t, member)
		if !setManualEquipProgress(member.session, 1, 1) {
			t.Fatalf("member %d progress was not recorded", member.session.playerID)
		}
	}
	leader := members[0]
	response, transition := server.onRequestEnterMap(leader, &protocol.C2M_RequestEnterMap{MapId: 10004})
	if response.Message != "" || transition == nil {
		t.Fatalf("next-layer portal response=%+v transition=%v", response, transition != nil)
	}
	transition()
	for _, member := range members {
		if member.session.mapID != 1003302 {
			t.Fatalf("member %d map=%d, want 1003302", member.session.playerID, member.session.mapID)
		}
	}

	for _, member := range members {
		member.session.mapID = 1003304
		if !setManualEquipProgress(member.session, 1, 4) {
			// The first call above may already have set the same value; keep the
			// test focused on the final-layer portal behavior in that case.
			if manualEquipProgress(member.session, 1) != 4 {
				t.Fatalf("member %d final progress=%d", member.session.playerID, manualEquipProgress(member.session, 1))
			}
		}
	}
	response, transition = server.onRequestEnterMap(leader, &protocol.C2M_RequestEnterMap{MapId: 10004})
	if response.Message != "" || transition == nil {
		t.Fatalf("final-layer portal response=%+v transition=%v", response, transition != nil)
	}
	transition()
	for _, member := range members {
		if member.session.mapID != 10004 {
			t.Fatalf("member %d final map=%d, want city", member.session.playerID, member.session.mapID)
		}
	}
}

func TestManualEquipRejectsForgedDirectLayerEntry(t *testing.T) {
	loadOnlineTablesForTest(t)
	ch, _ := consistencyChannel(7201, 10004)
	response, transition := (&Server{}).onRequestEnterMap(ch, &protocol.C2M_RequestEnterMap{
		MapId: 1003302,
		RpcId: 7,
	})
	if transition != nil {
		t.Fatal("forged layer produced a map transition")
	}
	if response.RpcId != 7 || response.Message == "" || ch.session.mapID != 10004 {
		t.Fatalf("forged layer response=%+v map=%d", response, ch.session.mapID)
	}
}

func TestManualEquipSoloClickExplainsOnlineThreePlayerRequirement(t *testing.T) {
	loadOnlineTablesForTest(t)
	server, members := manualEquipPartyForTest(t, 1003301, 1)
	leader := members[0]
	response := server.onClickMapUnit(leader, &protocol.C2M_ClickMapUnit{
		Id: manualEquipFieldUnitID(t, leader), Type: mapMonsterTypeManualEquip,
	}).(*protocol.M2C_ClickMapUnit)
	if !strings.Contains(response.Message, "3") || !strings.Contains(response.Message, "同一层") {
		t.Fatalf("solo failure message = %q", response.Message)
	}
	if leader.session.battle != nil {
		t.Fatal("solo player entered a three-player manual dungeon")
	}
}

func TestManualEquipThreeSameLayerMembersShareOnlineBattleAndVictory(t *testing.T) {
	loadOnlineTablesForTest(t)
	const mapID = int32(1003403)
	server, members := manualEquipPartyForTest(t, mapID, 3)
	leader := members[0]
	started, count, firstConfigID := server.startManualEquipBattle(leader, manualEquipFieldUnitID(t, leader))
	if !started || count != 3 || firstConfigID != 2301 {
		t.Fatalf("manual start=(%v,%d,%d), want (true,3,2301)", started, count, firstConfigID)
	}
	party := leader.session.battle.party
	if party == nil || len(party.memberIDs) != 3 {
		t.Fatalf("manual party = %+v", party)
	}
	wantConfigIDs := []int32{2301, 2302, 2303}
	wantMonsterIDs := []int32{40007, 40019, 40031}
	for _, member := range members {
		battle := member.session.battle
		if battle == nil || battle.party != party || battle.copyID != manualEquipCopyID || battle.mapID != mapID {
			t.Fatalf("member %d battle = %+v", member.session.playerID, battle)
		}
		if len(member.session.fieldMonsterConfigIDs) != 0 {
			t.Fatalf("member %d field monster was not removed", member.session.playerID)
		}
		assertOnlineMonsterUnits(t, battle.monsters, wantMonsterIDs, []int{1, 1, 1})
		frame := findRecordedFrame(t, member.conn.(*recordingConn).Bytes(), protocol.OpM2C_SendManulEquipMonsterInfo)
		var message protocol.M2C_SendManulEquipMonsterInfo
		if err := proto.Unmarshal(frame.body, &message); err != nil {
			t.Fatal(err)
		}
		gotConfigIDs := make([]int32, 0, len(message.UnitIdList))
		for _, unit := range message.UnitIdList {
			gotConfigIDs = append(gotConfigIDs, unit.MonsterId)
		}
		if !reflect.DeepEqual(gotConfigIDs, wantConfigIDs) {
			t.Fatalf("member %d manual slots = %v, want %v", member.session.playerID, gotConfigIDs, wantConfigIDs)
		}
	}
	for _, monster := range party.members[members[0].session.playerID].monsters {
		monster.hp, monster.alive = 0, false
	}
	for _, battle := range party.members {
		battle.ended = true
	}
	server.finishPartyVictory(party)
	for _, member := range members {
		if member.session.battle != nil || manualEquipProgress(member.session, 2) != 3 {
			t.Fatalf("member %d victory state battle=%p progress=%d", member.session.playerID,
				member.session.battle, manualEquipProgress(member.session, 2))
		}
	}
}

func TestManualEquipClientDisplayAndOnlineDropChain(t *testing.T) {
	loadOnlineTablesForTest(t)
	const (
		tier  = int32(3)
		layer = int32(4)
	)
	mapID := (manualEquipFirstScene+tier-1)*100 + layer
	field := sceneFieldMonsters(mapID)
	if len(field) != 1 {
		t.Fatalf("manual field monsters = %d, want one", len(field))
	}
	fieldConfig := tables.mapMonsterConfig[int64(field[0].configID)]
	leaderConfigID := tier*1000 + layer*100 + 1
	leaderConfig := tables.manulEquipMonsterConfig[int64(leaderConfigID)]
	leaderMonsterID := int32(num(leaderConfig["MonsterId"]))
	leaderMonster := tables.monsterBase[int64(leaderMonsterID)]
	if got, want := fmt.Sprint(fieldConfig["Desc"]), fmt.Sprint(leaderConfig["Name"]); got != want {
		t.Fatalf("field name = %q, want client manual name %q", got, want)
	}
	if got, want := int32(num(fieldConfig["PrefabId"])), int32(num(leaderMonster["PrefabId"])); got != want {
		t.Fatalf("field prefab = %d, want leader prefab %d", got, want)
	}

	ch, _ := consistencyChannel(7400, mapID)
	configIDs := []int32{leaderConfigID, leaderConfigID + 1, leaderConfigID + 2}
	monsterIDs := make([]int32, 0, len(configIDs))
	for _, configID := range configIDs {
		monsterIDs = append(monsterIDs, int32(num(tables.manulEquipMonsterConfig[int64(configID)]["MonsterId"])))
	}
	units := ch.session.buildMonsterUnitsFromRoster(monsterIDs, []int{1, 1, 1})
	for index, unit := range units {
		row := tables.monsterBase[int64(monsterIDs[index])]
		if unit.hp != int32(num(row["Hp"])) || unit.maxHP != int32(num(row["Hp"])) {
			t.Fatalf("slot %d HP=%d/%d, want client MonsterBase HP=%d", index, unit.hp, unit.maxHP, num(row["Hp"]))
		}
	}

	// Make one online Parentset/SonSet result deterministic, then verify the
	// manual battle uses the MonsterBase Dropasubset rather than map placeholder
	// metadata or a hard-coded item.
	previousParentset, previousSonSet := tables.parentset, tables.sonSet
	tables.parentset = map[int64]map[string]interface{}{
		10067: {"SubsetArr": []interface{}{map[string]interface{}{"_Id": int64(30117)}}},
	}
	tables.sonSet = map[int64]map[string]interface{}{
		30117: {"DropArr": []interface{}{map[string]interface{}{
			"_Id": int64(20245), "Weight": int64(1), "MinCount": int64(2), "MaxCount": int64(2),
		}}},
	}
	t.Cleanup(func() {
		tables.parentset, tables.sonSet = previousParentset, previousSonSet
	})
	dead := make([]*monsterUnit, len(units))
	for index, unit := range units {
		copyUnit := *unit
		copyUnit.alive = false
		copyUnit.hp = 0
		dead[index] = &copyUnit
	}
	reward := battleVictoryDrops(&battleState{copyID: manualEquipCopyID, region: 0, monsters: dead})
	if got := reward.items[20245]; got != 2 {
		t.Fatalf("manual online drop = %d, want 2 from Parentset 10067", got)
	}
}

func TestManualEquipPartyDefeatAndQuitReturnToCity(t *testing.T) {
	loadOnlineTablesForTest(t)
	server, members := manualEquipPartyForTest(t, 1003504, 3)
	leader := members[0]
	started, _, _ := server.startManualEquipBattle(leader, manualEquipFieldUnitID(t, leader))
	if !started {
		t.Fatal("manual party did not start")
	}
	party := leader.session.battle.party
	for _, battle := range party.members {
		battle.ended = true
	}
	server.finishPartyDefeat(party)
	for _, member := range members {
		if member.session.battle != nil || member.session.mapID != 10004 {
			t.Fatalf("defeated member %d battle=%p map=%d", member.session.playerID,
				member.session.battle, member.session.mapID)
		}
	}

	solo, _ := consistencyChannel(7300, 1003303)
	soloBattle := &battleState{
		copyID: manualEquipCopyID, mapID: 1003303, battleType: 6,
		playerHP: 0, playerMaxHP: 100, playerMP: 100, playerMaxMP: 100,
		monsters: []*monsterUnit{{id: 9001, monsterID: 40003, hp: 1, maxHP: 1, alive: true}},
		owner:    solo.session,
	}
	solo.session.battle = soloBattle
	solo.session.battleMu.Lock()
	settled := server.settleCombatLocked(solo, soloBattle)
	solo.session.battleMu.Unlock()
	if !settled || solo.session.battle != nil || solo.session.mapID != 10004 {
		t.Fatalf("solo defeat settled=%v battle=%p map=%d", settled, solo.session.battle, solo.session.mapID)
	}

	quit, _ := consistencyChannel(7301, 1003302)
	quit.session.battle = &battleState{
		copyID: manualEquipCopyID, mapID: 1003302, battleType: 6,
		playerHP: 100, playerMaxHP: 100, playerMP: 100, playerMaxMP: 100,
	}
	server.onQuitBattle(quit, &protocol.C2M_QuitBattle{})
	if quit.session.battle != nil || quit.session.mapID != 10004 {
		t.Fatalf("quit manual battle=%p map=%d", quit.session.battle, quit.session.mapID)
	}
}
