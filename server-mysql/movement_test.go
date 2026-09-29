package main

import (
	"encoding/binary"
	"math"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func TestFrameClickMapSendsConfiguredMovementSpeed(t *testing.T) {
	ss := newSession()
	ss.playerID = 42
	ss.resetMovement(1, 2)
	conn := &recordingConn{}
	ch := &channel{conn: conn, session: ss}
	(&Server{}).onFrameClickMap(ch, &protocol.Frame_ClickMap{
		UnitInfo: &protocol.UnitPosInfo{X: 9, Y: 6},
	})

	frame := conn.Bytes()
	if len(frame) < 4 {
		t.Fatalf("pathfinding frame too short: %x", frame)
	}
	length := int(binary.LittleEndian.Uint16(frame[:2]))
	if length < 2 || len(frame) != length+2 {
		t.Fatalf("pathfinding frame length=%d bytes=%d", length, len(frame))
	}
	if opcode := binary.LittleEndian.Uint16(frame[2:4]); opcode != protocol.OpM2C_PathfindingResult {
		t.Fatalf("pathfinding opcode=%d", opcode)
	}
	message := &protocol.M2C_PathfindingResult{}
	if err := proto.Unmarshal(frame[4:], message); err != nil {
		t.Fatalf("decode pathfinding result: %v", err)
	}
	if message.MoveSpeed != 3.5 {
		t.Fatalf("move speed=%v, want 3.5", message.MoveSpeed)
	}
	if message.X != 1 || message.Y != 2 || message.TX != 9 || message.TY != 6 {
		t.Fatalf("path=(%v,%v)->(%v,%v)", message.X, message.Y, message.TX, message.TY)
	}
}

func TestMovementUsesInterpolatedPositionForRepeatedClicks(t *testing.T) {
	ss := newSession()
	ss.resetMovement(0, 0)
	started := time.Unix(100, 0)
	ss.beginMovement(started, 10, 0, 5)

	x, y := ss.beginMovement(started.Add(time.Second), 5, 5, 5)
	if math.Abs(float64(x-5)) > 0.001 || math.Abs(float64(y)) > 0.001 {
		t.Fatalf("second path started at (%.3f, %.3f), want (5, 0)", x, y)
	}
	if ss.moveStartX != x || ss.moveStartY != y || ss.moveTargetX != 5 || ss.moveTargetY != 5 {
		t.Fatalf("new movement state = start(%.3f, %.3f) target(%.3f, %.3f)",
			ss.moveStartX, ss.moveStartY, ss.moveTargetX, ss.moveTargetY)
	}
}

func TestMovementStopsAtDestination(t *testing.T) {
	ss := newSession()
	ss.resetMovement(-2, 1)
	started := time.Unix(200, 0)
	ss.beginMovement(started, 3, 1, 5)

	x, y := ss.currentPosition(started.Add(2*time.Second), 5)
	if x != 3 || y != 1 || ss.moving {
		t.Fatalf("completed movement = (%.3f, %.3f), moving=%v", x, y, ss.moving)
	}
}

func TestStopMoveSettlesInterpolatedPosition(t *testing.T) {
	oldTables := tables
	t.Cleanup(func() { tables = oldTables })
	tables = &datatables{npcBase: map[int64]map[string]interface{}{
		1004: {"SceneId": int64(10004), "MapLayer": int64(1), "PosX": float64(3.5), "PosY": float64(0)},
	}}
	ss := newSession()
	ss.playerID, ss.mapID = 42, 10004
	ss.resetMovement(0, 0)
	ss.beginMovement(time.Now().Add(-time.Second), 10, 0, moveSpeed)

	(&Server{}).onStopMove(&channel{session: ss}, &protocol.C2M_StopMove{})
	if ss.moving {
		t.Fatal("stop move left the session moving")
	}
	if ss.x < 3.4 || ss.x > 3.6 || ss.y != 0 {
		t.Fatalf("stopped position = (%.3f, %.3f), want approximately (3.5, 0)", ss.x, ss.y)
	}
	if ss.moveStartX != ss.x || ss.moveTargetX != ss.x || !ss.moveStartedAt.IsZero() {
		t.Fatalf("stopped movement state was not normalized: start=%.3f target=%.3f at=%v",
			ss.moveStartX, ss.moveTargetX, ss.moveStartedAt)
	}
	if message := validateNPCInteraction(ss, 1004); message != "" {
		t.Fatalf("NPC interaction used a stale pre-stop position: %q", message)
	}
}

func TestMapTransitionRejectsQueuedOldSceneClicks(t *testing.T) {
	ss := newSession()
	ss.playerID = 42
	ss.mapID = 1000601
	ss.resetMovement(8, 4)
	conn := &recordingConn{}
	ch := &channel{id: 1, conn: conn, session: ss}
	server := &Server{conns: map[int64]*channel{ch.id: ch}}

	spawnX, spawnY := mainCityReturnSpawn()
	server.changeMap(ch, 10004, spawnX, spawnY)
	framesBeforeClick := len(conn.Bytes())
	server.onFrameClickMap(ch, &protocol.Frame_ClickMap{
		UnitInfo: &protocol.UnitPosInfo{X: 25, Y: 20},
	})
	if ss.x != spawnX || ss.y != spawnY || ss.moveTargetX != spawnX || ss.moveTargetY != spawnY || ss.moving {
		t.Fatalf("queued old-scene click changed city spawn: pos=(%.3f,%.3f) target=(%.3f,%.3f) moving=%v",
			ss.x, ss.y, ss.moveTargetX, ss.moveTargetY, ss.moving)
	}
	if got := len(conn.Bytes()); got != framesBeforeClick {
		t.Fatalf("queued old-scene click emitted %d bytes, want none", got-framesBeforeClick)
	}

	ss.mapChangeMu.Lock()
	ss.mapChangeAt = time.Now().Add(-mapStartupDelay - time.Millisecond)
	ss.mapChangeMu.Unlock()
	server.onFrameClickMap(ch, &protocol.Frame_ClickMap{
		UnitInfo: &protocol.UnitPosInfo{X: -5.724, Y: -0.94},
	})
	if !ss.moving || ss.moveTargetX != -5.724 || ss.moveTargetY != -0.94 {
		t.Fatalf("ready city movement was not accepted: target=(%.3f,%.3f) moving=%v",
			ss.moveTargetX, ss.moveTargetY, ss.moving)
	}
	if got := len(conn.Bytes()); got <= framesBeforeClick {
		t.Fatal("ready city movement emitted no path packet")
	}
}

func TestMapTransitionStopsClientMovementBeforeChangingScene(t *testing.T) {
	ss := newSession()
	ss.playerID = 42
	ss.mapID = 1000601
	ss.resetMovement(8, 4)
	ss.beginMovement(time.Now().Add(time.Second), 20, 4, moveSpeed)
	conn := &recordingConn{}
	ch := &channel{id: 1, conn: conn, session: ss}
	server := &Server{conns: map[int64]*channel{ch.id: ch}}

	spawnX, spawnY := mainCityReturnSpawn()
	server.changeMap(ch, 10004, spawnX, spawnY)

	frames := decodeRecordedFrames(t, conn.Bytes())
	stopIndex, changeIndex := -1, -1
	for index, frame := range frames {
		switch frame.opcode {
		case protocol.OpM2C_Stop:
			if stopIndex >= 0 {
				t.Fatal("map transition emitted more than one movement stop")
			}
			stopIndex = index
			var stop protocol.M2C_Stop
			if err := proto.Unmarshal(frame.body, &stop); err != nil {
				t.Fatalf("decode movement stop: %v", err)
			}
			if stop.Id != ss.playerID || stop.ActorId != ss.playerID || stop.X != 8 || stop.Y != 4 {
				t.Fatalf("movement stop = %+v, want player=%d old position=(8,4)", &stop, ss.playerID)
			}
		case protocol.OpM2C_ChangeMap:
			changeIndex = index
		}
	}
	if stopIndex < 0 || changeIndex < 0 || stopIndex >= changeIndex {
		t.Fatalf("map packet order invalid: stop=%d change=%d", stopIndex, changeIndex)
	}
	if ss.mapID != 10004 || ss.x != spawnX || ss.y != spawnY || ss.moving {
		t.Fatalf("post-map movement state = map %d pos=(%.3f,%.3f) moving=%v, want city spawn (%.3f,%.3f)",
			ss.mapID, ss.x, ss.y, ss.moving, spawnX, spawnY)
	}
}

func TestTeamLeaderMovementDrivesSameSceneFollowers(t *testing.T) {
	teamMu.Lock()
	oldTeams, oldSeq := teams, teamSeq
	teams, teamSeq = make(map[int64]*teamState), 1
	teamMu.Unlock()
	t.Cleanup(func() {
		teamMu.Lock()
		teams, teamSeq = oldTeams, oldSeq
		teamMu.Unlock()
	})

	makePlayer := func(id int64, x float32) *channel {
		ss := newSession()
		ss.state = sessInGame
		ss.playerID, ss.mapID, ss.teamID = id, 10004, 1
		ss.resetMovement(x, 0)
		return &channel{id: id, conn: &recordingConn{}, session: ss}
	}
	leader := makePlayer(101, 0)
	follower := makePlayer(202, -1)
	server := &Server{conns: map[int64]*channel{101: leader, 202: follower}}
	teamMu.Lock()
	teams[1] = &teamState{LeaderId: 101, Members: []int64{101, 202}}
	teamMu.Unlock()

	server.onFrameClickMap(leader, &protocol.Frame_ClickMap{
		UnitInfo: &protocol.UnitPosInfo{X: 10, Y: 0},
	})
	wantX, wantY := teamFollowerTarget(10, 0, 1, 0, 1)
	initialX, initialY, _ := follower.session.movementTarget()
	if initialX != -1 || initialY != 0 {
		t.Fatalf("follower moved before follow delay: target=(%.2f,%.2f)",
			initialX, initialY)
	}
	for _, frame := range decodeRecordedFrames(t, follower.conn.(*recordingConn).Bytes()) {
		if frame.opcode != protocol.OpM2C_PathfindingResult {
			continue
		}
		var earlyPath protocol.M2C_PathfindingResult
		if err := proto.Unmarshal(frame.body, &earlyPath); err != nil {
			t.Fatal(err)
		}
		if earlyPath.Id == follower.session.playerID {
			t.Fatal("follower path was sent before follow delay")
		}
	}
	time.Sleep(teamFollowDelay + 100*time.Millisecond)
	actualX, actualY, _ := follower.session.movementTarget()
	if actualX != wantX || actualY != wantY {
		t.Fatalf("follower target=(%.2f,%.2f), want (%.2f,%.2f)",
			actualX, actualY, wantX, wantY)
	}
	for _, recipient := range []*channel{leader, follower} {
		seenFollowerPath := false
		for _, frame := range decodeRecordedFrames(t, recipient.conn.(*recordingConn).Bytes()) {
			if frame.opcode != protocol.OpM2C_PathfindingResult {
				continue
			}
			var path protocol.M2C_PathfindingResult
			if err := proto.Unmarshal(frame.body, &path); err != nil {
				t.Fatal(err)
			}
			if path.Id == follower.session.playerID && path.TX == wantX && path.TY == wantY {
				seenFollowerPath = true
			}
		}
		if !seenFollowerPath {
			t.Fatalf("recipient %d did not receive follower path", recipient.session.playerID)
		}
	}
}

func TestTeamLeaderContinuousClicksDoNotCancelFollower(t *testing.T) {
	teamMu.Lock()
	oldTeams, oldSeq := teams, teamSeq
	teams, teamSeq = make(map[int64]*teamState), 1
	teamMu.Unlock()
	t.Cleanup(func() {
		teamMu.Lock()
		teams, teamSeq = oldTeams, oldSeq
		teamMu.Unlock()
	})

	makePlayer := func(id int64) *channel {
		ss := newSession()
		ss.state = sessInGame
		ss.playerID, ss.mapID, ss.teamID = id, 10004, 1
		ss.resetMovement(0, 0)
		return &channel{id: id, conn: &recordingConn{}, session: ss}
	}
	leader := makePlayer(101)
	follower := makePlayer(202)
	server := &Server{conns: map[int64]*channel{101: leader, 202: follower}}
	teamMu.Lock()
	teams[1] = &teamState{LeaderId: 101, Members: []int64{101, 202}}
	teamMu.Unlock()

	server.onFrameClickMap(leader, &protocol.Frame_ClickMap{UnitInfo: &protocol.UnitPosInfo{X: 10, Y: 0}})
	time.Sleep(teamFollowDelay / 2)
	server.onFrameClickMap(leader, &protocol.Frame_ClickMap{UnitInfo: &protocol.UnitPosInfo{X: 20, Y: 0}})
	// The first timer must still release the follower before the second click's
	// timer is due. This catches the old cancel-on-every-click behavior.
	time.Sleep(teamFollowDelay/2 + 40*time.Millisecond)
	targetX, targetY, moving := follower.session.movementTarget()
	if !moving || targetX == 0 && targetY == 0 {
		t.Fatalf("continuous leader clicks cancelled follower: target=(%.2f,%.2f) moving=%v", targetX, targetY, moving)
	}
}

func TestTeamLeaderMovementFormsFiveMemberLineWithFollowDelay(t *testing.T) {
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
	memberIDs := make([]int64, 0, maxTeamMembers)
	members := make([]*channel, 0, maxTeamMembers)
	for slot := 0; slot < maxTeamMembers; slot++ {
		id := int64(101 + slot)
		ss := newSession()
		ss.state, ss.playerID, ss.mapID, ss.teamID = sessInGame, id, 10004, 1
		ss.resetMovement(0, 0)
		member := &channel{id: id, conn: &recordingConn{}, session: ss}
		server.conns[id] = member
		memberIDs = append(memberIDs, id)
		members = append(members, member)
	}
	teamMu.Lock()
	teams[1] = &teamState{LeaderId: memberIDs[0], Members: append([]int64(nil), memberIDs...)}
	teamMu.Unlock()

	server.onFrameClickMap(members[0], &protocol.Frame_ClickMap{
		UnitInfo: &protocol.UnitPosInfo{X: 0.6, Y: 0.8},
	})
	_, _, firstMoving := members[1].session.movementTarget()
	_, _, secondMoving := members[2].session.movementTarget()
	if firstMoving || secondMoving {
		t.Fatal("followers started moving with the leader")
	}
	time.Sleep(teamFollowDelay*4 + 150*time.Millisecond)
	spacing := teamFollowSpacing
	if total := spacing * float32(len(members)-1); total > 1 {
		spacing = 1 / float32(len(members)-1)
	}
	for slot, member := range members[1:] {
		gap := spacing * float32(slot+1)
		wantX, wantY := float32(0.6)-0.6*gap, float32(0.8)-0.8*gap
		actualX, actualY, _ := member.session.movementTarget()
		if math.Abs(float64(actualX-wantX)) > 0.0001 ||
			math.Abs(float64(actualY-wantY)) > 0.0001 {
			t.Fatalf("slot %d target=(%.3f,%.3f), want line (%.3f,%.3f)",
				slot+1, actualX, actualY, wantX, wantY)
		}
		seenOwnPath := false
		for _, frame := range decodeRecordedFrames(t, member.conn.(*recordingConn).Bytes()) {
			if frame.opcode != protocol.OpM2C_PathfindingResult {
				continue
			}
			var path protocol.M2C_PathfindingResult
			if err := proto.Unmarshal(frame.body, &path); err != nil {
				t.Fatal(err)
			}
			if path.Id == member.session.playerID {
				seenOwnPath = true
				break
			}
		}
		if !seenOwnPath {
			t.Fatalf("slot %d did not receive a delayed path", slot+1)
		}
	}
}

func TestTeamLeaderMovementTargetsStayInOneLine(t *testing.T) {
	unitX, unitY := float32(1), float32(0)
	firstX, firstY := teamFollowerTarget(10, 0, unitX, unitY, 1)
	secondX, secondY := teamFollowerTarget(10, 0, unitX, unitY, 2)
	if firstY != secondY {
		t.Fatalf("follower targets are not in one line: first=(%.2f,%.2f) second=(%.2f,%.2f)",
			firstX, firstY, secondX, secondY)
	}
	if math.Abs(float64(firstX-secondX)-float64(teamFollowSpacing)) > 0.0001 {
		t.Fatalf("follower target spacing=%.3f, want %.3f", firstX-secondX, teamFollowSpacing)
	}
}

func TestSameSceneTeamFollowerCannotBreakFormation(t *testing.T) {
	teamMu.Lock()
	oldTeams, oldSeq := teams, teamSeq
	teams, teamSeq = make(map[int64]*teamState), 1
	teamMu.Unlock()
	t.Cleanup(func() {
		teamMu.Lock()
		teams, teamSeq = oldTeams, oldSeq
		teamMu.Unlock()
	})

	leaderSession, followerSession := newSession(), newSession()
	leaderSession.state, followerSession.state = sessInGame, sessInGame
	leaderSession.playerID, leaderSession.mapID, leaderSession.teamID = 101, 10004, 1
	followerSession.playerID, followerSession.mapID, followerSession.teamID = 202, 10004, 1
	followerSession.resetMovement(3, 4)
	leader := &channel{id: 101, conn: &recordingConn{}, session: leaderSession}
	follower := &channel{id: 202, conn: &recordingConn{}, session: followerSession}
	server := &Server{conns: map[int64]*channel{101: leader, 202: follower}}
	teamMu.Lock()
	teams[1] = &teamState{LeaderId: 101, Members: []int64{101, 202}}
	teamMu.Unlock()

	server.onFrameClickMap(follower, &protocol.Frame_ClickMap{
		UnitInfo: &protocol.UnitPosInfo{X: 20, Y: 20},
	})
	if followerSession.x != 3 || followerSession.y != 4 || followerSession.moving {
		t.Fatalf("controlled follower moved independently: pos=(%.2f,%.2f) moving=%v",
			followerSession.x, followerSession.y, followerSession.moving)
	}
	if len(follower.conn.(*recordingConn).Bytes()) != 0 || len(leader.conn.(*recordingConn).Bytes()) != 0 {
		t.Fatal("controlled follower click emitted a path packet")
	}
}
