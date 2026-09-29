package main

import (
	"path/filepath"
	"reflect"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func TestFamilyBossProgressSurvivesDatabaseReopen(t *testing.T) {
	loadOnlineTablesForTest(t)
	path := filepath.Join(t.TempDir(), "family-boss.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	oldServer := globalServer
	globalServer = &Server{store: store}
	t.Cleanup(func() { globalServer = oldServer })

	const familyID int64 = 7654321
	familyBossStates.mu.Lock()
	delete(familyBossStates.m, familyID)
	familyBossStates.mu.Unlock()
	state := familyBossState(familyID, 1)
	state.Hp = 12345678
	state.HasReward = false
	saveFamilyBossStateDB(familyID, familyBossAllStates(familyID))
	store.Close()

	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	globalServer.store = store
	familyBossStates.mu.Lock()
	delete(familyBossStates.m, familyID)
	familyBossStates.mu.Unlock()
	reloaded := familyBossState(familyID, 1)
	if reloaded.Hp != 12345678 || reloaded.HasReward || reloaded.MaxHp != familyBossMaxHP(1) {
		t.Fatalf("reloaded family boss = %+v", reloaded)
	}
}

func TestFamilyBossNeedsNeitherTicketNorEnergy(t *testing.T) {
	loadOnlineTablesForTest(t)
	const familyID int64 = 987654321
	familyBossStates.mu.Lock()
	delete(familyBossStates.m, familyID)
	familyBossStates.mu.Unlock()
	t.Cleanup(func() {
		familyBossStates.mu.Lock()
		delete(familyBossStates.m, familyID)
		familyBossStates.mu.Unlock()
	})

	ss := newSession()
	ss.playerID, ss.familyID, ss.jobID, ss.level, ss.energy = 77, familyID, 1, 1, 0
	ss.bag = map[int32]*bagItem{0: {ItemId: 20217, ItemType: int32(protocol.ItemType_MaterialsItem), Count: 1}}
	before := cloneBagMap(ss.bag)
	ch := &channel{id: 77, conn: &recordingConn{}, session: ss}
	server := &Server{conns: map[int64]*channel{77: ch}}

	result := server.onStartFamilyBossFight(ch, &protocol.C2M_StartFamilyBossFight{RpcId: 1, BossId: 1}).(*protocol.M2C_StartFamilyBossFight)
	if result.Message != "" || ss.battle == nil {
		t.Fatalf("family boss did not start without ticket/energy: result=%+v battle=%p", result, ss.battle)
	}
	if ss.energy != 0 || !reflect.DeepEqual(ss.bag, before) {
		t.Fatalf("family boss consumed an undeclared cost: energy=%d bag=%v want=%v", ss.energy, ss.bag, before)
	}
}

func bytesFields(t *testing.T, raw []byte, target protowire.Number) [][]byte {
	t.Helper()
	var out [][]byte
	for len(raw) > 0 {
		number, wireType, tagLen := protowire.ConsumeTag(raw)
		if tagLen < 0 {
			t.Fatal(protowire.ParseError(tagLen))
		}
		raw = raw[tagLen:]
		if wireType == protowire.BytesType {
			payload, fieldLen := protowire.ConsumeBytes(raw)
			if fieldLen < 0 {
				t.Fatal(protowire.ParseError(fieldLen))
			}
			if number == target {
				out = append(out, append([]byte(nil), payload...))
			}
			raw = raw[fieldLen:]
			continue
		}
		fieldLen := protowire.ConsumeFieldValue(number, wireType, raw)
		if fieldLen < 0 {
			t.Fatal(protowire.ParseError(fieldLen))
		}
		raw = raw[fieldLen:]
	}
	return out
}

func TestBossDamageRankUsesRequiredNestedWireShape(t *testing.T) {
	members := []*familyMember{
		{ID: 7, Name: "leader"},
		{ID: 8, Name: "member"},
	}
	rank := encodeBossDamageRank(120, members)

	var summary protocol.BossDamageMap
	if err := proto.Unmarshal(rank, &summary); err != nil {
		t.Fatalf("decode BossDamageMap: %v", err)
	}
	if summary.TotalDamage != 120 {
		t.Fatalf("total damage = %d, want 120", summary.TotalDamage)
	}
	entries := bytesFields(t, rank, 2)
	if len(entries) != 2 {
		t.Fatalf("nested DamageList count = %d, want 2", len(entries))
	}
	var first protocol.BossDamagePerMemberMap
	if err := proto.Unmarshal(entries[0], &first); err != nil {
		t.Fatalf("decode first member: %v", err)
	}
	if first.Id != 7 || first.Name != "leader" || first.Damage != 60 {
		t.Fatalf("first member = id %d name %q damage %d", first.Id, first.Name, first.Damage)
	}

	base, err := proto.Marshal(&protocol.M2C_GetBossDamageMap{RpcId: 11})
	if err != nil {
		t.Fatal(err)
	}
	response := pbAppendBytes(base, 1, rank)
	rankList := bytesFields(t, response, 1)
	if len(rankList) != 1 {
		t.Fatalf("top-level RankList count = %d, want 1", len(rankList))
	}
	if got := bytesFields(t, rankList[0], 2); len(got) != 2 {
		t.Fatalf("top-level rank lost nested members: %d", len(got))
	}
}
