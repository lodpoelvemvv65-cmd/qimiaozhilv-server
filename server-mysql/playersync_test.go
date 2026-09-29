package main

import (
	"math"
	"reflect"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func TestLaterPlayerUsesNativeEnterMapForExistingPlayers(t *testing.T) {
	firstSession := newSession()
	firstSession.state = sessInGame
	firstSession.playerID = 2
	firstSession.name = "first"
	firstSession.jobID = 1
	firstSession.skinID = 1
	firstSession.level = 10
	firstSession.mapID = 10004
	firstSession.x, firstSession.y = -12, -1
	firstConn := &recordingConn{}
	first := &channel{id: 1, conn: firstConn, session: firstSession}

	laterSession := newSession()
	laterSession.state = sessInGame
	laterSession.playerID = 408
	laterSession.name = "later"
	laterSession.jobID = 2
	laterSession.skinID = 2
	laterSession.titleID = 120888
	laterSession.level = 20
	laterSession.hp, laterSession.mp = 31, 17
	laterSession.pet = &petState{PetId: 2102, Level: 12, Name: "later-pet", IsShow: true}
	laterSession.mapID = 10004
	laterSession.x, laterSession.y = -8, -3
	laterSession.yAngle = 180
	later := &channel{id: 2, conn: &recordingConn{}, session: laterSession}

	server := &Server{conns: map[int64]*channel{first.id: first, later.id: later}}
	server.broadcastUnitsInMapExcept(later)

	frames := decodeRecordedFrames(t, firstConn.Bytes())
	if len(frames) == 0 || frames[0].opcode != protocol.OpM2C_EnterMap {
		t.Fatalf("existing player frames = %+v, want EnterMap first", recordedOpcodes(t, firstConn.Bytes()))
	}
	message := &protocol.M2C_EnterMap{}
	if err := proto.Unmarshal(frames[0].body, message); err != nil {
		t.Fatal(err)
	}
	if message.PvpUnitCharacter == nil || message.PvpUnitCharacter.Id != laterSession.playerID ||
		message.PvpUnitCharacter.Title != laterSession.titleID ||
		message.PvpUnitCharacter.PetId != laterSession.pet.PetId ||
		message.PvpUnitCharacter.PetName != laterSession.pet.Name ||
		message.PvpUnitCharacter.IsShowPet != laterSession.pet.IsShow ||
		message.PvpUnitCharacter.PetLevel != laterSession.pet.Level ||
		message.PvpUnitCharacter.Hp != 31 || message.PvpUnitCharacter.Mp != 17 ||
		message.PvpUnitCharacter.MaxHp != laterSession.playerMaxHp() ||
		message.PvpUnitCharacter.MaxMp != laterSession.playerMaxMp() ||
		message.UnitInfo == nil || message.UnitInfo.X != laterSession.x || message.UnitInfo.Y != laterSession.y ||
		message.UnitInfo.YAngle != laterSession.yAngle ||
		message.ActorId != firstSession.playerID {
		t.Fatalf("EnterMap = %+v", message)
	}
	values := map[int32]float32{}
	for _, frame := range frames[1:] {
		if frame.opcode != protocol.OpM2C_SyncUnitAttribute {
			t.Fatalf("follow-up opcode=%d, want resource sync", frame.opcode)
		}
		attr := &protocol.M2C_SyncUnitAttribute{}
		if err := proto.Unmarshal(frame.body, attr); err != nil {
			t.Fatal(err)
		}
		if attr.UnitId != laterSession.playerID || attr.ActorId != firstSession.playerID {
			t.Fatalf("resource attr=%+v", attr)
		}
		values[attr.NumericType] = attr.Value
	}
	if values[1001] != 31 || values[1003] != 17 {
		t.Fatalf("live resources=%v, want hp 31 mp 17", values)
	}
}

func TestMapChangeDefersScenePlayersUntilReadyAndDeduplicatesThem(t *testing.T) {
	loadOnlineTablesForTest(t)
	residentSession := newSession()
	residentSession.state = sessInGame
	residentSession.playerID, residentSession.mapID = 2, 10004
	residentSession.name, residentSession.jobID, residentSession.skinID = "resident", 1, 1
	resident := &channel{id: 1, conn: &recordingConn{}, session: residentSession}

	movingSession := newSession()
	movingSession.state = sessInGame
	movingSession.playerID, movingSession.mapID = 408, 10005
	movingSession.name, movingSession.jobID, movingSession.skinID = "moving", 2, 2
	moving := &channel{id: 2, conn: &recordingConn{}, session: movingSession}
	server := &Server{conns: map[int64]*channel{resident.id: resident, moving.id: moving}}

	server.changeMap(moving, 10004, -12, -1)
	for _, frame := range decodeRecordedFrames(t, moving.conn.(*recordingConn).Bytes()) {
		if frame.opcode == protocol.OpM2C_EnterMap || frame.opcode == protocol.OpM2C_CreateMapMonster {
			t.Fatalf("scene entity opcode %d was sent before map startup", frame.opcode)
		}
	}
	if frames := decodeRecordedFrames(t, resident.conn.(*recordingConn).Bytes()); len(frames) != 0 {
		t.Fatalf("resident received moving player before startup: %+v", frames)
	}

	seq, ready := movingSession.currentMapSceneVersion()
	if ready || !server.finishMapStartup(moving, 10004, seq, false) {
		t.Fatalf("map startup state ready=%t seq=%d", ready, seq)
	}
	if server.finishMapStartup(moving, 10004, seq, false) {
		t.Fatal("same map startup completed twice")
	}
	for _, recipient := range []*channel{resident, moving} {
		count := 0
		for _, frame := range decodeRecordedFrames(t, recipient.conn.(*recordingConn).Bytes()) {
			if frame.opcode == protocol.OpM2C_EnterMap {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("recipient %d EnterMap count=%d, want 1", recipient.session.playerID, count)
		}
	}
}

func TestMapChangeClearsOldSceneBeforeCreatingNewPlayers(t *testing.T) {
	withFeatureTables(t, &datatables{})
	makePlayer := func(channelID, playerID int64, mapID int32) *channel {
		ss := newSession()
		ss.state, ss.playerID, ss.mapID = sessInGame, playerID, mapID
		ss.name, ss.jobID, ss.skinID = "player", 1, 1
		seq := ss.markMapChange()
		if !ss.markMapReady(seq) {
			t.Fatalf("player %d scene was not marked ready", playerID)
		}
		return &channel{id: channelID, conn: &recordingConn{}, session: ss}
	}

	moving := makePlayer(1, 101, 10005)
	oldPeerA := makePlayer(2, 202, 10005)
	oldPeerB := makePlayer(3, 303, 10005)
	targetPeer := makePlayer(4, 404, 10004)
	moving.session.level, moving.session.hp, moving.session.mp = 30, 111, 22
	targetPeer.session.level, targetPeer.session.hp, targetPeer.session.mp = 40, 333, 44
	server := &Server{conns: map[int64]*channel{
		moving.id: moving, oldPeerA.id: oldPeerA, oldPeerB.id: oldPeerB, targetPeer.id: targetPeer,
	}}
	for _, peer := range []*channel{oldPeerA, oldPeerB} {
		if !server.markScenePlayerVisible(moving, peer) {
			t.Fatalf("moving player could not see old peer %d", peer.session.playerID)
		}
	}
	moving.conn = &recordingConn{}
	targetPeer.conn = &recordingConn{}

	server.changeMap(moving, 10004, -12, -1)

	clearCount, clearIndex, changeIndex := 0, -1, -1
	for index, frame := range decodeRecordedFrames(t, moving.conn.(*recordingConn).Bytes()) {
		switch frame.opcode {
		case protocol.OpM2C_OffLine:
			t.Fatal("map change guessed an individual old-scene Unit ID")
		case protocol.OpM2C_UnitsInMap:
			clearCount++
			clearIndex = index
			var message protocol.M2C_UnitsInMap
			if err := proto.Unmarshal(frame.body, &message); err != nil {
				t.Fatal(err)
			}
			if message.ActorId != moving.session.playerID ||
				len(bytesFields(t, frame.body, 1)) != 0 || len(bytesFields(t, frame.body, 2)) != 0 {
				t.Fatalf("scene clear=%+v", &message)
			}
		case protocol.OpM2C_EnterMap:
			t.Fatal("map change created a player before scene startup")
		case protocol.OpM2C_ChangeMap:
			changeIndex = index
		}
	}
	if clearCount != 1 || clearIndex < 0 || changeIndex <= clearIndex {
		t.Fatalf("scene cleanup order invalid: count=%d clear=%d change=%d", clearCount, clearIndex, changeIndex)
	}
	if frames := decodeRecordedFrames(t, targetPeer.conn.(*recordingConn).Bytes()); len(frames) != 0 {
		t.Fatalf("target peer received moving player before startup: %+v", frames)
	}

	seq, ready := moving.session.currentMapSceneVersion()
	if ready || !server.finishMapStartup(moving, 10004, seq, false) {
		t.Fatalf("map startup state ready=%t seq=%d", ready, seq)
	}
	for _, recipient := range []*channel{moving, targetPeer} {
		enterCount := 0
		for _, frame := range decodeRecordedFrames(t, recipient.conn.(*recordingConn).Bytes()) {
			if frame.opcode == protocol.OpM2C_EnterMap {
				enterCount++
				var message protocol.M2C_EnterMap
				if err := proto.Unmarshal(frame.body, &message); err != nil {
					t.Fatal(err)
				}
				source := moving.session
				if recipient == moving {
					source = targetPeer.session
				}
				if message.PvpUnitCharacter == nil || message.PvpUnitCharacter.Id != source.playerID ||
					message.PvpUnitCharacter.Level != source.level ||
					message.PvpUnitCharacter.Hp != source.battleHP() ||
					message.PvpUnitCharacter.Mp != source.battleMP() {
					t.Fatalf("recipient %d EnterMap character=%+v", recipient.session.playerID, message.PvpUnitCharacter)
				}
			}
		}
		if enterCount != 1 {
			t.Fatalf("recipient %d EnterMap count=%d, want 1", recipient.session.playerID, enterCount)
		}
	}
}

func TestStaleMapStartupCannotCreatePlayers(t *testing.T) {
	loadOnlineTablesForTest(t)
	ss := newSession()
	ss.state, ss.playerID, ss.mapID = sessInGame, 408, 10005
	ss.name, ss.jobID, ss.skinID = "moving", 2, 2
	ch := &channel{id: 2, conn: &recordingConn{}, session: ss}
	otherSession := newSession()
	otherSession.state, otherSession.playerID, otherSession.mapID = sessInGame, 2, 10004
	other := &channel{id: 1, conn: &recordingConn{}, session: otherSession}
	server := &Server{conns: map[int64]*channel{ch.id: ch, other.id: other}}

	server.changeMap(ch, 10004, -12, -1)
	staleSeq, _ := ss.currentMapSceneVersion()
	server.changeMap(ch, 10006, 1, 2)
	ch.conn = &recordingConn{}
	other.conn = &recordingConn{}
	if server.finishMapStartup(ch, 10004, staleSeq, false) {
		t.Fatal("stale map startup unexpectedly completed")
	}
	if got := recordedOpcodes(t, ch.conn.(*recordingConn).Bytes()); len(got) != 0 {
		t.Fatalf("stale startup opcodes=%v", got)
	}
}

func TestTeamMapStartupCreatesPlayersBeforeFullSnapshotAtSharedCoordinates(t *testing.T) {
	loadOnlineTablesForTest(t)
	teamMu.Lock()
	oldTeams, oldSeq := teams, teamSeq
	teams, teamSeq = make(map[int64]*teamState), 1
	teamMu.Unlock()
	t.Cleanup(func() {
		teamMu.Lock()
		teams, teamSeq = oldTeams, oldSeq
		teamMu.Unlock()
	})

	makeMember := func(channelID, playerID int64) *channel {
		ss := newSession()
		ss.state, ss.playerID, ss.mapID, ss.teamID = sessInGame, playerID, 10005, 1
		ss.name, ss.jobID, ss.skinID = "member", 1, 1
		ss.level, ss.hp, ss.mp = int32(40+channelID), 300+int32(channelID), 80+int32(channelID)
		return &channel{id: channelID, conn: &recordingConn{}, session: ss}
	}
	leader := makeMember(1, 101)
	member := makeMember(2, 202)
	server := &Server{conns: map[int64]*channel{leader.id: leader, member.id: member}}
	teamMu.Lock()
	teams[1] = &teamState{LeaderId: 101, Members: []int64{101, 202}}
	teamMu.Unlock()

	server.changeMapForTeamLeader(leader, 10004, -12.00656, -1.474469)
	wantPositions := map[int64][2]float32{
		leader.session.playerID: {-12.00656, -1.474469},
		member.session.playerID: {-12.00656, -1.474469},
	}
	for _, ch := range []*channel{leader, member} {
		seq, ready := ch.session.currentMapSceneVersion()
		if ready || !server.finishMapStartup(ch, 10004, seq, false) {
			t.Fatalf("player %d did not finish map startup", ch.session.playerID)
		}
	}
	time.Sleep(mapTeamSnapshotDelay + 50*time.Millisecond)

	for _, recipient := range []*channel{leader, member} {
		frames := decodeRecordedFrames(t, recipient.conn.(*recordingConn).Bytes())
		enterIndex, fullTeamIndex := -1, -1
		for index, frame := range frames {
			switch frame.opcode {
			case protocol.OpM2C_EnterMap:
				enterIndex = index
				var message protocol.M2C_EnterMap
				if err := proto.Unmarshal(frame.body, &message); err != nil {
					t.Fatal(err)
				}
				source := leader.session
				if recipient == leader {
					source = member.session
				}
				wantSource := wantPositions[source.playerID]
				if message.UnitInfo == nil || message.UnitInfo.X != wantSource[0] || message.UnitInfo.Y != wantSource[1] {
					t.Fatalf("recipient %d source %d EnterMap position=%+v, want=(%f,%f)",
						recipient.session.playerID, source.playerID, message.UnitInfo, wantSource[0], wantSource[1])
				}
				if message.PvpUnitCharacter == nil || message.PvpUnitCharacter.Id != source.playerID ||
					message.PvpUnitCharacter.Level != source.level ||
					message.PvpUnitCharacter.Hp != source.battleHP() ||
					message.PvpUnitCharacter.Mp != source.battleMP() {
					t.Fatalf("recipient %d EnterMap character=%+v", recipient.session.playerID, message.PvpUnitCharacter)
				}
			case protocol.OpM2C_TeamMember:
				var message protocol.M2C_TeamMember
				if err := proto.Unmarshal(frame.body, &message); err != nil {
					t.Fatal(err)
				}
				if reflect.DeepEqual(message.UnitIds, []int64{101, 202}) {
					fullTeamIndex = index
				}
			}
		}
		if enterIndex < 0 || fullTeamIndex <= enterIndex {
			t.Fatalf("recipient %d frame order=%v, EnterMap=%d fullTeam=%d",
				recipient.session.playerID, recordedOpcodes(t, recipient.conn.(*recordingConn).Bytes()), enterIndex, fullTeamIndex)
		}
		wantRecipient := wantPositions[recipient.session.playerID]
		if recipient.session.x != wantRecipient[0] || recipient.session.y != wantRecipient[1] {
			t.Fatalf("recipient %d authoritative position=(%f,%f), want=(%f,%f)", recipient.session.playerID,
				recipient.session.x, recipient.session.y, wantRecipient[0], wantRecipient[1])
		}
	}
}

func TestMapChangeDoesNotGuessClientGeneratedNPCUnitIDs(t *testing.T) {
	withFeatureTables(t, &datatables{npcBase: map[int64]map[string]interface{}{
		1001: {"SceneId": int64(10005), "MapLayer": int64(1)},
		1002: {"SceneId": int64(10005), "MapLayer": int64(1)},
		1004: {"SceneId": int64(10004), "MapLayer": int64(1)},
	}})

	departingSession := newSession()
	departingSession.state, departingSession.playerID, departingSession.mapID = sessInGame, 101, 1000501
	departing := &channel{id: 1, conn: &recordingConn{}, session: departingSession}
	server := &Server{conns: map[int64]*channel{departing.id: departing}}

	server.changeMap(departing, 10004, -12, -1)
	departing.session.markMapChange()

	clearCount, clearIndex, changeIndex := 0, -1, -1
	for index, frame := range decodeRecordedFrames(t, departing.conn.(*recordingConn).Bytes()) {
		switch frame.opcode {
		case protocol.OpM2C_OffLine:
			t.Fatal("NPC cleanup guessed a config ID as a client-generated Unit ID")
		case protocol.OpM2C_LeaveMap:
			t.Fatal("NPC cleanup emitted player-oriented LeaveMap")
		case protocol.OpM2C_DisposeMapMonster:
			t.Fatal("NPC cleanup emitted a targeted monster disposal")
		case protocol.OpM2C_UnitsInMap:
			clearCount++
			clearIndex = index
			var clear protocol.M2C_UnitsInMap
			if err := proto.Unmarshal(frame.body, &clear); err != nil {
				t.Fatal(err)
			}
			if clear.ActorId != departing.session.playerID ||
				len(bytesFields(t, frame.body, 1)) != 0 || len(bytesFields(t, frame.body, 2)) != 0 {
				t.Fatalf("scene clear=%+v", &clear)
			}
		case protocol.OpM2C_ChangeMap:
			changeIndex = index
		}
	}
	if clearCount != 1 || clearIndex < 0 || changeIndex <= clearIndex {
		t.Fatalf("NPC scene cleanup order invalid: count=%d clear=%d change=%d", clearCount, clearIndex, changeIndex)
	}
}

func TestReturningMemberRefreshesReadyPartyViewsAfterSceneStartup(t *testing.T) {
	loadOnlineTablesForTest(t)
	teamMu.Lock()
	oldTeams, oldSeq := teams, teamSeq
	teams, teamSeq = make(map[int64]*teamState), 1
	teamMu.Unlock()
	t.Cleanup(func() {
		teamMu.Lock()
		teams, teamSeq = oldTeams, oldSeq
		teamMu.Unlock()
	})

	makeMember := func(channelID, playerID int64, mapID int32) *channel {
		ss := newSession()
		ss.state, ss.playerID, ss.mapID, ss.teamID = sessInGame, playerID, mapID, 1
		ss.name, ss.jobID, ss.skinID = "member", 1, 1
		return &channel{id: channelID, conn: &recordingConn{}, session: ss}
	}
	leader := makeMember(1, 101, 10004)
	returning := makeMember(2, 202, 10005)
	server := &Server{conns: map[int64]*channel{leader.id: leader, returning.id: returning}}
	teamMu.Lock()
	teams[1] = &teamState{LeaderId: 101, Members: []int64{101, 202}}
	teamMu.Unlock()

	leaderSeq := leader.session.markMapChange()
	if !leader.session.markMapReady(leaderSeq) {
		t.Fatal("leader scene was not marked ready")
	}
	server.changeMap(returning, 10004, -12.00656, -1.474469)
	returning.conn = &recordingConn{}
	leader.conn = &recordingConn{}
	returningSeq, ready := returning.session.currentMapSceneVersion()
	if ready || !server.finishMapStartup(returning, 10004, returningSeq, false) {
		t.Fatalf("returning member startup ready=%t seq=%d", ready, returningSeq)
	}
	time.Sleep(mapTeamSnapshotDelay + 50*time.Millisecond)

	for _, recipient := range []*channel{leader, returning} {
		frames := decodeRecordedFrames(t, recipient.conn.(*recordingConn).Bytes())
		enterIndex, teamIndex := -1, -1
		for index, frame := range frames {
			switch frame.opcode {
			case protocol.OpM2C_EnterMap:
				enterIndex = index
			case protocol.OpM2C_TeamMember:
				var snapshot protocol.M2C_TeamMember
				if err := proto.Unmarshal(frame.body, &snapshot); err != nil {
					t.Fatal(err)
				}
				if snapshot.LeaderId == 101 && reflect.DeepEqual(snapshot.UnitIds, []int64{101, 202}) {
					teamIndex = index
				}
			}
		}
		if enterIndex < 0 || teamIndex <= enterIndex {
			t.Fatalf("recipient %d frame order=%v, EnterMap=%d fullTeam=%d",
				recipient.session.playerID, recordedOpcodes(t, recipient.conn.(*recordingConn).Bytes()), enterIndex, teamIndex)
		}
	}
}

func TestTeamSnapshotRefreshesExactRemoteHPAfterHeadListRebuild(t *testing.T) {
	loadOnlineTablesForTest(t)
	makeMember := func(channelID, playerID int64, hp int32) *channel {
		ss := newSession()
		ss.state, ss.playerID, ss.mapID = sessInGame, playerID, 10004
		ss.name, ss.jobID, ss.skinID, ss.level, ss.hp = "member", 1, 1, 20, hp
		return &channel{id: channelID, conn: &recordingConn{}, session: ss}
	}
	recipient := makeMember(1, 101, -1)
	zero := makeMember(2, 202, 0)
	partial := makeMember(3, 303, 31)
	full := makeMember(4, 404, -1)
	full.session.level = 31500
	full.session.transBonus[1002] = 100000001
	for _, member := range []*channel{zero, partial, full} {
		buildUnitCharacter(member.session)
	}
	_, fullMaxHP, cached := full.session.loadTeamHeadHP()
	if !cached || float32(fullMaxHP-1) != float32(fullMaxHP) {
		t.Fatalf("high-level HP cache=%d ready=%t does not exercise float32 precision", fullMaxHP, cached)
	}
	server := &Server{conns: map[int64]*channel{
		recipient.id: recipient, zero.id: zero, partial.id: partial, full.id: full,
	}}

	server.sendTeamMember(recipient, 101, []int64{101, 202, 303, 404})
	frames := decodeRecordedFrames(t, recipient.conn.(*recordingConn).Bytes())
	// 重建前每个队友两条抖动（1001 血量、1003 蓝量），重建后每人四条精确值。
	if len(frames) != 19 {
		t.Fatalf("team HP refresh frame count=%d opcodes=%v, want 19",
			len(frames), recordedOpcodes(t, recipient.conn.(*recordingConn).Bytes()))
	}
	if frames[6].opcode != protocol.OpM2C_TeamMember {
		t.Fatalf("team snapshot index opcode=%d, want TeamMember", frames[6].opcode)
	}
	wantBefore := map[int64]float32{
		202: 1, 303: 32,
		404: math.Nextafter32(float32(fullMaxHP), 0),
	}
	wantAfter := map[int64]float32{202: 0, 303: 31, 404: float32(fullMaxHP)}
	seenBefore := make(map[int64]float32, 3)
	// conns 以连接序号为键，这里按玩家 id 反查，才能核对蓝量抖动。
	memberByPlayer := make(map[int64]*channel, len(server.conns))
	for _, member := range server.conns {
		memberByPlayer[member.session.playerID] = member
	}
	for _, frame := range frames[0:6] {
		if frame.opcode != protocol.OpM2C_SyncUnitAttribute {
			t.Fatalf("HP refresh opcode=%d, want SyncUnitAttribute", frame.opcode)
		}
		var message protocol.M2C_SyncUnitAttribute
		if err := proto.Unmarshal(frame.body, &message); err != nil {
			t.Fatal(err)
		}
		if message.ActorId != recipient.session.playerID {
			t.Fatalf("HP refresh message=%+v", &message)
		}
		if message.NumericType == 1003 {
			// 蓝量和血量走同一条客户端刷新链路，重建前也必须抖动一次。
			if exact := float32(memberByPlayer[message.UnitId].session.battleMP()); message.Value == exact {
				t.Fatalf("MP nudge for %d equals exact %v", message.UnitId, exact)
			}
			continue
		}
		if message.NumericType != 1001 {
			t.Fatalf("HP refresh message=%+v", &message)
		}
		seenBefore[message.UnitId] = message.Value
	}
	if !reflect.DeepEqual(seenBefore, wantBefore) {
		t.Fatalf("HP refresh before=%v, want %v", seenBefore, wantBefore)
	}
	seenAfter := make(map[int64]float32, 3)
	for _, frame := range frames[7:] {
		var message protocol.M2C_SyncUnitAttribute
		if err := proto.Unmarshal(frame.body, &message); err != nil {
			t.Fatal(err)
		}
		if message.NumericType == 1001 {
			seenAfter[message.UnitId] = message.Value
		}
	}
	if !reflect.DeepEqual(seenAfter, wantAfter) {
		t.Fatalf("HP refresh after=%v, want %v", seenAfter, wantAfter)
	}
	maxSeen := make(map[int64]float32, 3)
	for _, frame := range frames[7:] {
		var message protocol.M2C_SyncUnitAttribute
		if err := proto.Unmarshal(frame.body, &message); err != nil {
			t.Fatal(err)
		}
		if message.NumericType != 1002 {
			continue
		}
		if message.ActorId != recipient.session.playerID {
			t.Fatalf("MaxHP refresh message=%+v", &message)
		}
		maxSeen[message.UnitId] = message.Value
	}
	if !reflect.DeepEqual(maxSeen, map[int64]float32{202: float32(zero.session.playerMaxHp()), 303: float32(partial.session.playerMaxHp()), 404: float32(fullMaxHP)}) {
		t.Fatalf("MaxHP refresh=%v", maxSeen)
	}
	if zero.session.hp != 0 || partial.session.hp != 31 || full.session.hp != -1 {
		t.Fatalf("authoritative HP mutated: zero=%d partial=%d full=%d",
			zero.session.hp, partial.session.hp, full.session.hp)
	}
}

func TestExistingPlayerUpdateDoesNotCreateDuplicateSceneUnit(t *testing.T) {
	firstSession := newSession()
	firstSession.state = sessInGame
	firstSession.playerID, firstSession.mapID = 2, 10004
	first := &channel{id: 1, conn: &recordingConn{}, session: firstSession}
	updatedSession := newSession()
	updatedSession.state = sessInGame
	updatedSession.playerID, updatedSession.mapID = 408, 10004
	updatedSession.name, updatedSession.jobID, updatedSession.skinID, updatedSession.level = "updated", 1, 1, 10
	updatedSession.titleID = 120888
	updated := &channel{id: 2, conn: &recordingConn{}, session: updatedSession}
	server := &Server{conns: map[int64]*channel{first.id: first, updated.id: updated}}

	server.broadcastPlayerUpdate(updated)
	frames := decodeRecordedFrames(t, first.conn.(*recordingConn).Bytes())
	if len(frames) == 0 || frames[0].opcode != protocol.OpM2C_SendUnitInfo {
		t.Fatalf("first update frame = %+v, want SendUnitInfo", frames)
	}
	seenTitleCharacter := false
	seenTitleNumeric := false
	for _, frame := range frames {
		if frame.opcode == protocol.OpM2C_EnterMap || frame.opcode == protocol.OpM2C_UnitsInMap {
			t.Fatalf("existing player update recreated scene unit with opcode %d", frame.opcode)
		}
		switch frame.opcode {
		case protocol.OpM2C_SendUnitInfo:
			message := &protocol.M2C_SendUnitInfo{}
			if err := proto.Unmarshal(frame.body, message); err != nil {
				t.Fatal(err)
			}
			seenTitleCharacter = message.UnitCharacter != nil && message.UnitCharacter.Title == updatedSession.titleID
		case protocol.OpM2C_SyncUnitAttribute:
			message := &protocol.M2C_SyncUnitAttribute{}
			if err := proto.Unmarshal(frame.body, message); err != nil {
				t.Fatal(err)
			}
			if message.NumericType == 1038 && message.UnitId == updatedSession.playerID &&
				message.Value == float32(updatedSession.titleID) {
				seenTitleNumeric = true
			}
		}
	}
	if !seenTitleCharacter || !seenTitleNumeric {
		t.Fatalf("other-player title update: character=%t numeric=%t", seenTitleCharacter, seenTitleNumeric)
	}
}

func TestMapStartupResyncsOwnCurrentResources(t *testing.T) {
	loadOnlineTablesForTest(t)
	ss := newSession()
	ss.state, ss.playerID, ss.mapID = sessInGame, 3903, 10005
	ss.jobID, ss.skinID, ss.level = 3, 3, 20
	ss.hp, ss.mp = 58, 17
	ch := &channel{id: 13, conn: &recordingConn{}, session: ss}
	server := &Server{conns: map[int64]*channel{ch.id: ch}}
	server.changeMap(ch, 10004, -12, -1)
	ch.conn = &recordingConn{}
	seq, ready := ss.currentMapSceneVersion()
	if ready || !server.finishMapStartup(ch, 10004, seq, false) {
		t.Fatalf("startup ready=%t seq=%d", ready, seq)
	}
	values := map[int32]float32{}
	for _, frame := range decodeRecordedFrames(t, ch.conn.(*recordingConn).Bytes()) {
		if frame.opcode != protocol.OpM2C_SyncUnitAttribute {
			continue
		}
		var message protocol.M2C_SyncUnitAttribute
		if err := proto.Unmarshal(frame.body, &message); err != nil {
			t.Fatal(err)
		}
		if message.UnitId == ss.playerID {
			values[message.NumericType] = message.Value
		}
	}
	if values[1001] != 58 || values[1003] != 17 {
		t.Fatalf("own resources=%v, want hp 58 mp 17", values)
	}
	if values[1002] != float32(ss.playerMaxHp()) || values[1004] != float32(ss.playerMaxMp()) {
		t.Fatalf("own maxima=%v, want %d/%d", values, ss.playerMaxHp(), ss.playerMaxMp())
	}
}
