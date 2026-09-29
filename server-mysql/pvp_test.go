package main

import (
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"mhqserver/internal/operationsconfig"
	"mhqserver/protocol"
)

func varintsForField(t *testing.T, raw []byte, wanted protowire.Number) []uint64 {
	t.Helper()
	var values []uint64
	for len(raw) > 0 {
		number, wireType, n := protowire.ConsumeTag(raw)
		if n < 0 {
			t.Fatalf("invalid protobuf tag: %v", protowire.ParseError(n))
		}
		raw = raw[n:]
		if wireType == protowire.VarintType {
			value, consumed := protowire.ConsumeVarint(raw)
			if consumed < 0 {
				t.Fatalf("invalid protobuf varint: %v", protowire.ParseError(consumed))
			}
			if number == wanted {
				values = append(values, value)
			}
			raw = raw[consumed:]
			continue
		}
		consumed := protowire.ConsumeFieldValue(number, wireType, raw)
		if consumed < 0 {
			t.Fatalf("invalid protobuf field: %v", protowire.ParseError(consumed))
		}
		raw = raw[consumed:]
	}
	return values
}

func TestPKCreatesBothEnemyTeamsAndNativeExit(t *testing.T) {
	loadOnlineTablesForTest(t)
	leftSession := newSession()
	leftSession.state = sessInGame
	leftSession.playerID, leftSession.name = 101, "left"
	leftSession.jobID, leftSession.skinID, leftSession.level, leftSession.mapID = 1, 1, 20, 10004
	leftSession.signin = &signinState{PVPScore: 30}
	rightSession := newSession()
	rightSession.state = sessInGame
	rightSession.playerID, rightSession.name = 202, "right"
	rightSession.jobID, rightSession.skinID, rightSession.level, rightSession.mapID = 2, 2, 20, 10004
	rightSession.signin = &signinState{PVPScore: 40}
	left := &channel{id: 1, conn: &recordingConn{}, session: leftSession}
	right := &channel{id: 2, conn: &recordingConn{}, session: rightSession}
	server := &Server{conns: map[int64]*channel{left.id: left, right.id: right}}

	response := server.onStartPKFight(left, &protocol.C2M_StartPKFight{RpcId: 9, TargetId: rightSession.playerID}).(*protocol.M2C_StartPKFight)
	if response.Error != 0 || response.Message != "" {
		t.Fatalf("start PK response = %+v", response)
	}
	if leftSession.battle == nil || rightSession.battle == nil || leftSession.battle.pvp == nil ||
		leftSession.battle.pvp != rightSession.battle.pvp {
		t.Fatal("PK did not create one linked authoritative duel")
	}
	if leftSession.battle.copyID != directPKCopyID || leftSession.battle.battleType != directPKBattleType ||
		rightSession.battle.copyID != directPKCopyID || rightSession.battle.battleType != directPKBattleType {
		t.Fatalf("direct PK copy/type left=%d/%d right=%d/%d", leftSession.battle.copyID,
			leftSession.battle.battleType, rightSession.battle.copyID, rightSession.battle.battleType)
	}
	if leftSession.battle.monsters[0].id != rightSession.playerID || rightSession.battle.monsters[0].id != leftSession.playerID {
		t.Fatalf("mirrored PK targets = %d/%d", leftSession.battle.monsters[0].id, rightSession.battle.monsters[0].id)
	}

	for _, test := range []struct {
		channel *channel
		target  int64
	}{{left, 202}, {right, 101}} {
		teamBeforeStart := false
		foundStart := false
		for _, frame := range decodeRecordedFrames(t, test.channel.conn.(*recordingConn).Bytes()) {
			switch frame.opcode {
			case protocol.OpM2C_TeamMember:
				if !foundStart {
					teamBeforeStart = true
				}
			case protocol.OpM2C_SendStartPK:
				foundStart = true
				values := varintsForField(t, frame.body, 1)
				if len(values) != 1 || int64(values[0]) != test.target {
					t.Fatalf("player %d PK TargetIdList = %v, want [%d]", test.channel.session.playerID, values, test.target)
				}
				battleTypes := varintsForField(t, frame.body, 2)
				if len(battleTypes) != 1 || int32(battleTypes[0]) != directPKBattleType {
					t.Fatalf("player %d direct PK battleType = %v", test.channel.session.playerID, battleTypes)
				}
			}
		}
		if !teamBeforeStart || !foundStart {
			t.Fatalf("player %d missing ordered team/PK start packets", test.channel.session.playerID)
		}
	}

	duel := leftSession.battle.pvp
	duel.mu.Lock()
	leftSession.battle.monsters[0].hp -= 25
	syncPVPStateLocked(leftSession.battle)
	duel.mu.Unlock()
	if rightSession.battle.playerHP != rightSession.playerMaxHp()-25 || rightSession.battle.monsters[0].hp != leftSession.battle.playerHP {
		t.Fatalf("PK HP mirror leftTarget=%d rightPlayer=%d rightTarget=%d leftPlayer=%d",
			leftSession.battle.monsters[0].hp, rightSession.battle.playerHP,
			rightSession.battle.monsters[0].hp, leftSession.battle.playerHP)
	}

	server.onQuitBattle(left, &protocol.C2M_QuitBattle{})
	if leftSession.battle != nil || rightSession.battle != nil {
		t.Fatalf("PK quit left stale battles: left=%p right=%p", leftSession.battle, rightSession.battle)
	}
	if leftSession.signin.PVPScore != 30 || rightSession.signin.PVPScore != 40 ||
		leftSession.signin.PVPBattleCount != 0 || rightSession.signin.PVPBattleCount != 0 {
		t.Fatalf("direct PK changed rated state: left=%+v right=%+v", leftSession.signin, rightSession.signin)
	}
	assertResult := func(ch *channel, opcode uint16) {
		t.Helper()
		for _, frame := range decodeRecordedFrames(t, ch.conn.(*recordingConn).Bytes()) {
			if frame.opcode == opcode {
				return
			}
		}
		t.Fatalf("player %d missing PK result opcode %d", ch.session.playerID, opcode)
	}
	assertResult(left, protocol.OpM2C_PKFightDefeat)
	assertResult(right, protocol.OpM2C_PKFightVictory)
	for _, test := range []struct {
		channel *channel
		player  int64
	}{
		{left, leftSession.playerID},
		{right, rightSession.playerID},
	} {
		frames := decodeRecordedFrames(t, test.channel.conn.(*recordingConn).Bytes())
		var snapshots []*protocol.M2C_TeamMember
		for _, frame := range frames {
			if frame.opcode != protocol.OpM2C_TeamMember {
				continue
			}
			message := &protocol.M2C_TeamMember{}
			if err := proto.Unmarshal(frame.body, message); err != nil {
				t.Fatal(err)
			}
			snapshots = append(snapshots, message)
		}
		if len(snapshots) < 2 || snapshots[len(snapshots)-1].LeaderId != test.player ||
			len(snapshots[len(snapshots)-1].UnitIds) != 1 || snapshots[len(snapshots)-1].UnitIds[0] != test.player {
			t.Fatalf("player %d missing singleton team cleanup after PK: %+v", test.player, snapshots)
		}
	}

	// Ensure the result packets remain valid generated messages.
	for _, frame := range decodeRecordedFrames(t, right.conn.(*recordingConn).Bytes()) {
		if frame.opcode == protocol.OpM2C_PKFightVictory {
			var result protocol.M2C_PKFightVictory
			if err := proto.Unmarshal(frame.body, &result); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func newPersonalPVPTestChannel(id int64, mapID int32, x, y float32) *channel {
	ss := newSession()
	ss.state = sessInGame
	ss.playerID, ss.name = id, "arena-player"
	ss.jobID, ss.skinID, ss.level = 1, 1, 20
	ss.mapID = mapID
	ss.resetMovement(x, y)
	ss.signin = &signinState{PVPBattleDay: personalPVPDay(time.Now())}
	return &channel{id: id, conn: &recordingConn{}, session: ss}
}

func TestPersonalPVPMatchesInOnlineArenaAndSettlesRatedResult(t *testing.T) {
	loadOnlineTablesForTest(t)
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.PersonalPVP.VictoryScoreDelta = 10
		config.PersonalPVP.DefeatScoreDelta = -5
	})
	left := newPersonalPVPTestChannel(401, 10004, -2, -1)
	right := newPersonalPVPTestChannel(402, 1000501, 3, 4)
	left.session.signin.PVPScore = 30
	right.session.signin.PVPScore = 3
	server := &Server{conns: map[int64]*channel{left.id: left, right.id: right}}

	firstResponse := server.onRequestPersonalPvp(left, &protocol.C2M_RequestPersonalPvp{RpcId: 1})
	if firstResponse == nil || !left.session.signin.PVPIsMatching {
		t.Fatalf("first queue response=%+v state=%+v", firstResponse, left.session.signin)
	}
	secondResponse := server.onRequestPersonalPvp(right, &protocol.C2M_RequestPersonalPvp{RpcId: 2})
	if secondResponse != nil {
		t.Fatalf("paired request should send its ordered response directly: %+v", secondResponse)
	}
	if left.session.mapID != personalPVPArenaMapID || right.session.mapID != personalPVPArenaMapID {
		t.Fatalf("arena maps left=%d right=%d", left.session.mapID, right.session.mapID)
	}
	if left.session.battle == nil || right.session.battle == nil || left.session.battle.pvp == nil ||
		left.session.battle.pvp != right.session.battle.pvp || !left.session.battle.pvp.rated {
		t.Fatal("personal PVP did not create one rated linked duel")
	}
	for _, battle := range []*battleState{left.session.battle, right.session.battle} {
		if battle.copyID != personalPVPCopyID || battle.battleType != personalPVPBattleType ||
			battle.mapID != personalPVPArenaMapID {
			t.Fatalf("personal PVP copy/type/map=%d/%d/%d", battle.copyID, battle.battleType, battle.mapID)
		}
		if !battle.scenePending {
			t.Fatal("personal PVP battle accepted input before its arena scene was ready")
		}
	}
	for _, participant := range []*channel{left, right} {
		if containsOpcode(recordedOpcodes(t, participant.conn.(*recordingConn).Bytes()), protocol.OpM2C_SendStartPK) {
			t.Fatalf("player %d received StartPK before arena startup", participant.session.playerID)
		}
	}
	time.Sleep(mapStartupDelay + mapTeamSnapshotDelay + 100*time.Millisecond)
	if left.session.battle.scenePending || right.session.battle.scenePending {
		t.Fatal("personal PVP presentation remained pending after arena startup")
	}

	assertStartOrder := func(ch *channel, responseRequired bool) {
		t.Helper()
		frames := decodeRecordedFrames(t, ch.conn.(*recordingConn).Bytes())
		responseIndex, unitIndex, startIndex := -1, -1, -1
		for index, frame := range frames {
			switch frame.opcode {
			case protocol.OpM2C_RequestPersonalPvp:
				responseIndex = index
			case protocol.OpM2C_UnitsInMap, protocol.OpM2C_EnterMap:
				unitIndex = index
			case protocol.OpM2C_SendStartPK:
				startIndex = index
				battleTypes := varintsForField(t, frame.body, 2)
				if len(battleTypes) != 1 || int32(battleTypes[0]) != personalPVPBattleType {
					t.Fatalf("player %d personal PVP battleType=%v", ch.session.playerID, battleTypes)
				}
			}
		}
		if responseRequired && (responseIndex < 0 || responseIndex >= unitIndex) {
			t.Fatalf("player %d response/unit order=%d/%d", ch.session.playerID, responseIndex, unitIndex)
		}
		if unitIndex < 0 || startIndex < 0 || unitIndex >= startIndex {
			t.Fatalf("player %d unit/start order=%d/%d", ch.session.playerID, unitIndex, startIndex)
		}
	}
	assertStartOrder(left, false)
	assertStartOrder(right, true)

	duel := left.session.battle.pvp
	duel.mu.Lock()
	server.finishPVPBattleLocked(duel, left.session.playerID, "test-result")
	duel.mu.Unlock()
	if left.session.signin.PVPScore != 40 || right.session.signin.PVPScore != 0 ||
		left.session.signin.PVPBattleCount != 1 || right.session.signin.PVPBattleCount != 1 {
		t.Fatalf("rated result left=%+v right=%+v", left.session.signin, right.session.signin)
	}
	if left.session.signin.PVPIsMatching || right.session.signin.PVPIsMatching {
		t.Fatal("rated result left a player in the matching pool")
	}
	if left.session.mapID != 10004 || left.session.x != -2 || left.session.y != -1 ||
		right.session.mapID != 1000501 || right.session.x != 3 || right.session.y != 4 {
		t.Fatalf("rated return points left=%d/(%v,%v) right=%d/(%v,%v)",
			left.session.mapID, left.session.x, left.session.y,
			right.session.mapID, right.session.x, right.session.y)
	}
}

func TestPersonalPVPDailyCapResetsOnConfiguredDay(t *testing.T) {
	loadOnlineTablesForTest(t)
	ch := newPersonalPVPTestChannel(501, 10004, 0, 0)
	server := &Server{conns: map[int64]*channel{ch.id: ch}}
	ch.session.signin.PVPBattleCount = personalPVPDailyBattleCap

	blocked := server.onRequestPersonalPvp(ch, &protocol.C2M_RequestPersonalPvp{}).(*protocol.M2C_RequestPersonalPvp)
	if blocked.Message != "今日战斗次数已用完" || ch.session.signin.PVPIsMatching {
		t.Fatalf("daily cap response=%+v state=%+v", blocked, ch.session.signin)
	}
	ch.session.signin.PVPBattleDay = "20200101"
	queued := server.onRequestPersonalPvp(ch, &protocol.C2M_RequestPersonalPvp{}).(*protocol.M2C_RequestPersonalPvp)
	if queued.Error != 0 || !ch.session.signin.PVPIsMatching || ch.session.signin.PVPBattleCount != 0 ||
		ch.session.signin.PVPBattleDay != personalPVPDay(time.Now()) {
		t.Fatalf("next-day queue response=%+v state=%+v", queued, ch.session.signin)
	}
	server.cancelPersonalPVPMatch(ch.session)
}

func TestPersonalPVPDisconnectSettlesAndRestoresOfflinePlayer(t *testing.T) {
	loadOnlineTablesForTest(t)
	left := newPersonalPVPTestChannel(601, 10004, -3, -1)
	right := newPersonalPVPTestChannel(602, 1000501, 4, 2)
	server := &Server{conns: map[int64]*channel{left.id: left, right.id: right}}
	if response := server.onRequestPersonalPvp(left, &protocol.C2M_RequestPersonalPvp{}); response == nil {
		t.Fatal("first player did not enter queue")
	}
	if response := server.onRequestPersonalPvp(right, &protocol.C2M_RequestPersonalPvp{}); response != nil {
		t.Fatalf("second player did not pair: %+v", response)
	}

	server.mu.Lock()
	delete(server.conns, right.id)
	server.mu.Unlock()
	server.cleanupPlayerSession(right)
	if left.session.battle != nil || right.session.battle != nil {
		t.Fatalf("disconnect left stale battles: left=%p right=%p", left.session.battle, right.session.battle)
	}
	if left.session.signin.PVPScore != 10 || right.session.signin.PVPScore != 0 ||
		left.session.signin.PVPBattleCount != 1 || right.session.signin.PVPBattleCount != 1 {
		t.Fatalf("disconnect result left=%+v right=%+v", left.session.signin, right.session.signin)
	}
	if left.session.mapID != 10004 || left.session.x != -3 || left.session.y != -1 ||
		right.session.mapID != 1000501 || right.session.x != 4 || right.session.y != 2 {
		t.Fatalf("disconnect return left=%d/(%v,%v) right=%d/(%v,%v)",
			left.session.mapID, left.session.x, left.session.y,
			right.session.mapID, right.session.x, right.session.y)
	}
}

func TestPersonalPVPDisconnectCancelsWaitingState(t *testing.T) {
	loadOnlineTablesForTest(t)
	ch := newPersonalPVPTestChannel(701, 10004, 0, 0)
	server := &Server{conns: map[int64]*channel{ch.id: ch}}
	if response := server.onRequestPersonalPvp(ch, &protocol.C2M_RequestPersonalPvp{}); response == nil {
		t.Fatal("player did not enter queue")
	}
	server.cleanupPlayerSession(ch)
	if ch.session.signin.PVPIsMatching || ch.session.signin.PVPMatchCount != 0 {
		t.Fatalf("disconnect retained matching state: %+v", ch.session.signin)
	}
}

func TestPKRejectsEitherDefeatedPlayerBeforeInstallingBattle(t *testing.T) {
	loadOnlineTablesForTest(t)
	leftSession := newSession()
	leftSession.state = sessInGame
	leftSession.playerID, leftSession.name = 301, "left"
	leftSession.jobID, leftSession.level, leftSession.mapID = 1, 20, 10004
	rightSession := newSession()
	rightSession.state = sessInGame
	rightSession.playerID, rightSession.name = 302, "right"
	rightSession.jobID, rightSession.level, rightSession.mapID = 2, 20, 10004
	left := &channel{id: 1, conn: &recordingConn{}, session: leftSession}
	right := &channel{id: 2, conn: &recordingConn{}, session: rightSession}
	server := &Server{conns: map[int64]*channel{left.id: left, right.id: right}}

	leftSession.hp = 0
	response := server.onStartPKFight(left, &protocol.C2M_StartPKFight{
		RpcId: 1, TargetId: rightSession.playerID,
	}).(*protocol.M2C_StartPKFight)
	if response.Message != battleEntryHealthMessage {
		t.Fatalf("defeated challenger message=%q, want %q", response.Message, battleEntryHealthMessage)
	}
	if leftSession.battle != nil || rightSession.battle != nil {
		t.Fatal("defeated challenger installed PK battle state")
	}

	leftSession.hp = leftSession.playerMaxHp()
	rightSession.hp = 0
	response = server.onStartPKFight(left, &protocol.C2M_StartPKFight{
		RpcId: 2, TargetId: rightSession.playerID,
	}).(*protocol.M2C_StartPKFight)
	if response.Message != "对方生命值不足，暂时无法挑战" {
		t.Fatalf("defeated target message=%q", response.Message)
	}
	if leftSession.battle != nil || rightSession.battle != nil {
		t.Fatal("defeated target installed PK battle state")
	}
}
