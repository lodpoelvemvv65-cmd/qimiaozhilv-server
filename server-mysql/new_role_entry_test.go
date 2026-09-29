package main

import (
	"database/sql"
	"errors"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func TestNewRoleKeepsReservedIdentityThroughFirstEntry(t *testing.T) {
	loadOnlineTablesForTest(t)
	store, err := OpenStore(mysqlTestDSN(t, "new-role-first-entry"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	accountID, err := store.CreateAccount("new-role-entry", "password")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{store: store}
	ss := newSession()
	conn := &recordingConn{}
	ch := &channel{id: 1, conn: conn, session: ss}

	gate := server.onLoginGate(ch, &protocol.C2G_LoginGate{
		Key: store.IssueKey(accountID), RpcId: 1,
	}).(*protocol.G2C_LoginGate)
	if gate.Error != 0 || gate.HasRole || gate.PlayerId <= 0 ||
		ss.playerID != gate.PlayerId || ss.reservedPlayerID != gate.PlayerId {
		t.Fatalf("new-role LoginGate = %+v session ids=%d/%d", gate, ss.playerID, ss.reservedPlayerID)
	}
	mismatch := server.onCreateRole(ch, &protocol.C2R_CreateRole{
		PlayerId: gate.PlayerId + 1, JobId: 5, Name: "错误角色标识", RpcId: 2,
	}).(*protocol.R2C_CreateRole)
	if mismatch.Error == 0 {
		t.Fatalf("mismatched PlayerId was accepted: %+v", mismatch)
	}
	if _, err := store.FirstPlayer(accountID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("mismatched create persisted a player: %v", err)
	}
	created := server.onCreateRole(ch, &protocol.C2R_CreateRole{
		PlayerId: gate.PlayerId, JobId: 5, Name: "首次进入角色", RpcId: 3,
	}).(*protocol.R2C_CreateRole)
	if created.Error != 0 {
		t.Fatalf("reserved create failed: %+v", created)
	}
	player, err := store.FirstPlayer(accountID)
	if err != nil || player.ID != gate.PlayerId {
		t.Fatalf("stored player id/error = %d/%v, want %d", player.ID, err, gate.PlayerId)
	}

	if response := server.onEnterGame(ch, &protocol.C2G_EnterGame{RpcId: 4}); response != nil {
		t.Fatalf("onEnterGame returned an extra response: %+v", response)
	}
	// Prevent delayed callbacks from outliving this focused immediate-order test.
	ss.superseded.Store(true)
	frames := decodeRecordedFrames(t, conn.Bytes())
	want := []uint16{
		protocol.OpG2C_EnterGame,
		protocol.OpM2C_SendUnitInfo,
		protocol.OpM2C_UnitsInMap,
		protocol.OpM2C_ChangeMap,
		protocol.OpM2C_SyncUnitAttributeList,
		protocol.OpM2C_InitMainStoryMap,
	}
	if len(frames) != len(want) {
		t.Fatalf("immediate first-entry frames = %+v, want %v", frames, want)
	}
	for index, opcode := range want {
		if frames[index].opcode != opcode {
			t.Fatalf("first-entry frame %d opcode=%d, want %d", index, frames[index].opcode, opcode)
		}
	}
	var enter protocol.G2C_EnterGame
	if err := proto.Unmarshal(frames[0].body, &enter); err != nil {
		t.Fatal(err)
	}
	if enter.Id != gate.PlayerId || enter.JobId != 5 || enter.SkinId != 0 || enter.IsOnline {
		t.Fatalf("first-entry response = %+v", &enter)
	}
	if wireHasField(t, frames[0].body, 2) || wireHasField(t, frames[0].body, 4) {
		t.Fatal("first-entry response encoded SkinId or IsOnline")
	}
	for _, frame := range frames[1:] {
		if frame.opcode == protocol.OpM2C_SyncPet || frame.opcode == protocol.OpM2C_SendActiveInfo {
			t.Fatalf("component-dependent opcode %d was sent immediately", frame.opcode)
		}
	}
}

func wireHasField(t *testing.T, raw []byte, target protowire.Number) bool {
	t.Helper()
	for len(raw) > 0 {
		number, wireType, tagLen := protowire.ConsumeTag(raw)
		if tagLen < 0 {
			t.Fatal(protowire.ParseError(tagLen))
		}
		raw = raw[tagLen:]
		valueLen := protowire.ConsumeFieldValue(number, wireType, raw)
		if valueLen < 0 {
			t.Fatal(protowire.ParseError(valueLen))
		}
		if number == target {
			return true
		}
		raw = raw[valueLen:]
	}
	return false
}
