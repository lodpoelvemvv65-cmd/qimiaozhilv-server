package main

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

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
	leftSession.playerID, leftSession.name = 101, "left"
	leftSession.jobID, leftSession.skinID, leftSession.level, leftSession.mapID = 1, 1, 20, 10004
	rightSession := newSession()
	rightSession.playerID, rightSession.name = 202, "right"
	rightSession.jobID, rightSession.skinID, rightSession.level, rightSession.mapID = 2, 2, 20, 10004
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

func TestPKRejectsEitherDefeatedPlayerBeforeInstallingBattle(t *testing.T) {
	loadOnlineTablesForTest(t)
	leftSession := newSession()
	leftSession.playerID, leftSession.name = 301, "left"
	leftSession.jobID, leftSession.level, leftSession.mapID = 1, 20, 10004
	rightSession := newSession()
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
