package main

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func TestReplacePlayerSessionForcesOldConnectionOutOfOnlineRouting(t *testing.T) {
	server := &Server{conns: make(map[int64]*channel)}
	old, oldConn := tradeTestChannel(1, 7001, "old")
	current, _ := tradeTestChannel(2, 7001, "current")
	old.session.state = sessInGame
	current.session.state = sessLogged
	server.conns[old.id] = old
	server.conns[current.id] = current

	server.replacePlayerSession(current, 7001)
	if !old.session.superseded.Load() || !old.session.offlineRun.Load() {
		t.Fatal("old session was not invalidated and cleaned")
	}
	if opcodes := recordedOpcodes(t, oldConn.Bytes()); len(opcodes) != 1 || opcodes[0] != protocol.OpG2C_ForceOffLine {
		t.Fatalf("old session opcodes=%v", opcodes)
	}
	if got := server.findChannelByPlayerID(7001); got != nil {
		t.Fatalf("LoginGate-only session entered gameplay routing: %#v", got)
	}
	channels := server.onlineChannels()
	if len(channels) != 0 {
		t.Fatalf("online channels=%v", channels)
	}
	current.session.state = sessInGame
	if got := server.findChannelByPlayerID(7001); got != current {
		t.Fatalf("entered session routing selected %#v, want current", got)
	}
}

func TestSupersededSessionOnlyAnswersKeepalive(t *testing.T) {
	server := &Server{conns: make(map[int64]*channel)}
	old, conn := tradeTestChannel(1, 7002, "old")
	old.session.state = sessInGame
	old.session.superseded.Store(true)
	server.conns[old.id] = old
	server.sendPush(old, protocol.OpM2C_BattleChangeState, &protocol.M2C_BattleChangeState{})
	server.sendRawPush(old, protocol.OpM2C_DeleteItem, nil)
	if len(conn.Bytes()) != 0 {
		t.Fatal("superseded session received a queued gameplay push")
	}

	request, err := proto.Marshal(&protocol.C2M_GetTask{RpcId: 10})
	if err != nil {
		t.Fatal(err)
	}
	server.handleOpcode(old, protocol.OpC2M_GetTask, request)
	if len(conn.Bytes()) != 0 {
		t.Fatal("superseded gameplay request received a response")
	}

	ping, err := proto.Marshal(&protocol.C2G_Ping{RpcId: 11})
	if err != nil {
		t.Fatal(err)
	}
	server.handleOpcode(old, protocol.OpC2G_Ping, ping)
	opcodes := recordedOpcodes(t, conn.Bytes())
	if len(opcodes) != 1 || opcodes[0] != protocol.OpG2C_Ping {
		t.Fatalf("superseded keepalive opcodes=%v", opcodes)
	}
}

func TestReplaceDuringPartyActivityCleansClientsBeforeRemovingUnit(t *testing.T) {
	teamMu.Lock()
	previousTeams, previousSequence := teams, teamSeq
	teams, teamSeq = make(map[int64]*teamState), 1
	teamMu.Unlock()
	t.Cleanup(func() {
		teamMu.Lock()
		teams, teamSeq = previousTeams, previousSequence
		teamMu.Unlock()
	})

	server := &Server{conns: make(map[int64]*channel)}
	old, oldConn := tradeTestChannel(1, 7100, "old")
	current, currentConn := tradeTestChannel(2, 7100, "current")
	old.session.state = sessInGame
	old.session.mapID = 1005001
	old.session.teamID = 1
	current.session.state = sessLogged
	server.conns[old.id] = old
	server.conns[current.id] = current

	memberIDs := []int64{7100, 7101, 7102, 7103, 7104}
	party := &partyBattle{
		members:   make(map[int64]*battleState, len(memberIDs)),
		memberIDs: append([]int64(nil), memberIDs...),
	}
	remainingConnections := make([]*recordingConn, 0, 4)
	for index, playerID := range memberIDs {
		var member *channel
		if index == 0 {
			member = old
		} else {
			var conn *recordingConn
			member, conn = tradeTestChannel(int64(index+2), playerID, "member")
			member.session.state = sessInGame
			member.session.mapID = 1005001
			member.session.teamID = 1
			server.conns[member.id] = member
			remainingConnections = append(remainingConnections, conn)
		}
		battle := &battleState{
			mapID: 1005001, playerHP: 100, playerMaxHP: 100, playerMP: 50, playerMaxMP: 50,
			owner: member.session, party: party,
			activity: &activityBattle{
				ActiveID: 10022, Method: activeWorldBoss, Stage: 1,
				ReturnMap: 10004, ReturnX: -3.5, ReturnY: 1.25,
			},
		}
		member.session.battle = battle
		party.members[playerID] = battle
	}
	teams[1] = &teamState{LeaderId: memberIDs[0], Members: append([]int64(nil), memberIDs...)}

	server.replacePlayerSession(current, old.session.playerID)

	if old.session.battle != nil || !old.session.superseded.Load() {
		t.Fatal("replaced session retained its party battle")
	}
	if old.session.mapID != 10004 || old.session.x != -3.5 || old.session.y != 1.25 {
		t.Fatalf("activity return position = %d/(%.2f,%.2f)", old.session.mapID, old.session.x, old.session.y)
	}
	teamMu.Lock()
	_, teamExists := teams[1]
	teamMu.Unlock()
	if teamExists {
		t.Fatal("leader replacement retained the old team")
	}
	partyIDs, _ := party.settlementSnapshot()
	if teamContains(partyIDs, old.session.playerID) || len(partyIDs) != 4 {
		t.Fatalf("party battle after replacement = %v", partyIDs)
	}
	if got := server.findChannelByPlayerID(old.session.playerID); got != nil {
		t.Fatalf("LoginGate-only replacement entered routing: %#v", got)
	}
	if len(currentConn.Bytes()) != 0 {
		t.Fatal("LoginGate-only replacement received gameplay data")
	}
	if opcodes := recordedOpcodes(t, oldConn.Bytes()); len(opcodes) != 1 || opcodes[0] != protocol.OpG2C_ForceOffLine {
		t.Fatalf("replaced session opcodes=%v", opcodes)
	}
	for index, conn := range remainingConnections {
		opcodes := recordedOpcodes(t, conn.Bytes())
		teamIndex := opcodeIndex(opcodes, protocol.OpM2C_TeamMember)
		tipIndex := opcodeIndex(opcodes, protocol.OpM2C_SendTip)
		offlineIndex := opcodeIndex(opcodes, protocol.OpM2C_OffLine)
		if teamIndex < 0 || tipIndex < 0 || offlineIndex < 0 || teamIndex >= tipIndex || tipIndex >= offlineIndex {
			t.Fatalf("member %d ordering=%v, want TeamMember, SendTip, then OffLine", index+1, opcodes)
		}
	}
}

func TestDisconnectReturnsPrivateBattlesToMainCity(t *testing.T) {
	for name, battle := range map[string]*battleState{
		"trial":       {battleType: trialBattleType, mapID: 1000901},
		"manual":      {copyID: 10007, mapID: 1003301},
		"family-boss": {mapID: -1},
	} {
		t.Run(name, func(t *testing.T) {
			ss := newSession()
			ss.mapID = battle.mapID
			ss.resetMovement(10, 20)
			restoreBattleReturnOnDisconnect(ss, battle)
			wantX, wantY := mainCityReturnSpawn()
			if ss.mapID != 10004 || ss.x != wantX || ss.y != wantY {
				t.Fatalf("disconnect return = %d/(%.2f,%.2f), want city/(%.2f,%.2f)", ss.mapID, ss.x, ss.y, wantX, wantY)
			}
		})
	}
}

func TestDisconnectKeepsOrdinaryBattleMap(t *testing.T) {
	ss := newSession()
	ss.mapID = 1000601
	ss.resetMovement(10, 20)
	restoreBattleReturnOnDisconnect(ss, &battleState{mapID: 1000601})
	if ss.mapID != 1000601 || ss.x != 10 || ss.y != 20 {
		t.Fatalf("ordinary disconnect return = %d/(%.2f,%.2f)", ss.mapID, ss.x, ss.y)
	}
}

func opcodeIndex(opcodes []uint16, target uint16) int {
	for index, opcode := range opcodes {
		if opcode == target {
			return index
		}
	}
	return -1
}
