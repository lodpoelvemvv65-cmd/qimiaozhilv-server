package main

import (
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"mhqserver/internal/operationsconfig"
	"mhqserver/protocol"
)

func TestFamilyBossProgressSurvivesDatabaseReopen(t *testing.T) {
	loadOnlineTablesForTest(t)
	path := filepath.Join(t.TempDir(), "family-boss.db")
	dsn := mysqlTestDSN(t, path)
	store, err := OpenStore(dsn)
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

	store, err = OpenStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	globalServer.store = store
	familyBossStates.mu.Lock()
	delete(familyBossStates.m, familyID)
	familyBossStates.mu.Unlock()
	reloaded := familyBossState(familyID, 1)
	if reloaded.Hp != 12345678 || reloaded.HasReward || reloaded.MaxHp != familyBossMaxHP(1) || reloaded.DeadAt != 0 {
		t.Fatalf("reloaded family boss = %+v", reloaded)
	}
}

func TestFamilyBossDamageSurvivesDatabaseReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "family-boss-damage.db")
	dsn := mysqlTestDSN(t, path)
	store, err := OpenStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	oldServer := globalServer
	globalServer = &Server{store: store}
	t.Cleanup(func() { globalServer = oldServer })

	const familyID int64 = 7654322
	const bossID int32 = 1
	clearFamilyBossDamage(familyID, bossID)
	recordFamilyBossDamage(familyID, bossID, 41, 90)
	recordFamilyBossDamage(familyID, bossID, 42, 30)
	recordFamilyBossTreatment(familyID, bossID, 41, 140)
	if got := familyBossDamageSnapshot(familyID, bossID); got[41] != 90 || got[42] != 30 {
		t.Fatalf("in-memory family boss damage = %v", got)
	}
	if got := familyBossTreatmentSnapshot(familyID, bossID); got[41] != 140 {
		t.Fatalf("in-memory family boss treatment = %v", got)
	}
	store.Close()

	store, err = OpenStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	globalServer.store = store
	familyBossDamage.mu.Lock()
	key := familyBossDamageKey{familyID: familyID, bossID: bossID}
	delete(familyBossDamage.m, key)
	delete(familyBossDamage.treat, key)
	delete(familyBossDamage.participants, key)
	delete(familyBossDamage.loaded, key)
	familyBossDamage.mu.Unlock()

	got := familyBossDamageSnapshot(familyID, bossID)
	if got[41] != 90 || got[42] != 30 || len(got) != 2 {
		t.Fatalf("reloaded family boss damage = %v, want map[41:90 42:30]", got)
	}
	if gotTreat := familyBossTreatmentSnapshot(familyID, bossID); gotTreat[41] != 140 || len(gotTreat) != 1 {
		t.Fatalf("reloaded family boss treatment = %v, want map[41:140]", gotTreat)
	}
	if gotParticipants := familyBossParticipantSnapshot(familyID, bossID); !gotParticipants[41] || !gotParticipants[42] || len(gotParticipants) != 2 {
		t.Fatalf("reloaded family boss participants = %v, want players 41 and 42", gotParticipants)
	}
	clearFamilyBossDamage(familyID, bossID)
}

func TestFamilyBossRewardClaimSurvivesDatabaseReopen(t *testing.T) {
	dsn := mysqlTestDSN(t, "family-boss-reward-claim")
	store, err := OpenStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{store: store}
	const familyID int64 = 7654323
	const playerID int64 = 43
	if !server.grantFamilyBossReward(familyID, 2, playerID) ||
		!server.canClaimFamilyBossReward(familyID, 2, playerID) {
		t.Fatal("new family boss reward claim was not available")
	}
	store.Close()

	store, err = OpenStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server.store = store
	if !server.canClaimFamilyBossReward(familyID, 2, playerID) {
		t.Fatal("family boss reward claim was lost after database reopen")
	}
	if !server.consumeFamilyBossReward(familyID, 2, playerID) ||
		server.canClaimFamilyBossReward(familyID, 2, playerID) {
		t.Fatal("persisted family boss reward claim was not consumed exactly once")
	}
}

func TestFamilyBossConsumesDailyKeyButNoBagTicketOrEnergy(t *testing.T) {
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
	// 一级角色没有自然主属性，需给出基础体力/精神，否则最大血量为 0 会被入场血量守卫拦下。
	ss.phyAdd, ss.spiAdd = 1, 1
	setDungeonQuotaForTest(ss, 50, 10, 2)
	ss.bag = map[int32]*bagItem{0: {ItemId: 20217, ItemType: int32(protocol.ItemType_MaterialsItem), Count: 1}}
	before := cloneBagMap(ss.bag)
	ch := &channel{id: 77, conn: &recordingConn{}, session: ss}
	server := &Server{conns: map[int64]*channel{77: ch}}

	result := server.onStartFamilyBossFight(ch, &protocol.C2M_StartFamilyBossFight{RpcId: 1, BossId: 1}).(*protocol.M2C_StartFamilyBossFight)
	if result.Message != "" || ss.battle == nil {
		t.Fatalf("family boss did not start with a daily key: result=%+v battle=%p", result, ss.battle)
	}
	if ss.energy != 0 || !reflect.DeepEqual(ss.bag, before) {
		t.Fatalf("family boss consumed an undeclared cost: energy=%d bag=%v want=%v", ss.energy, ss.bag, before)
	}
	if got := ss.signin.FamilyBossKeys; got != 1 {
		t.Fatalf("family boss keys=%d, want 1", got)
	}
}

func TestFamilyBossSettlementAwardsEachOutcomeOnce(t *testing.T) {
	oldTables := tables
	tables = &datatables{
		familyBossConfig: map[int64]map[string]interface{}{1: {
			"MonsterId": 50001, "Contribute": 100, "PersonalContribute": 150,
			"BonusDropChance": 0,
		}},
		monsterBase: map[int64]map[string]interface{}{50001: {"Hp": 1000}},
	}
	t.Cleanup(func() { tables = oldTables })
	const familyID int64 = 123456789
	familyBossStates.mu.Lock()
	delete(familyBossStates.m, familyID)
	familyBossStates.mu.Unlock()
	t.Cleanup(func() {
		familyBossStates.mu.Lock()
		delete(familyBossStates.m, familyID)
		familyBossStates.mu.Unlock()
	})

	ss := newSession()
	ss.playerID, ss.familyID = 7, familyID
	ss.bag = make(map[int32]*bagItem)
	ch := &channel{id: 7, conn: &recordingConn{}, session: ss}
	server := &Server{}

	defeat := &battleState{mapID: -1, monsters: []*monsterUnit{{hp: 321, maxHP: 1000, alive: true}}}
	reward := server.settleFamilyBossBattle(ch, defeat, false)
	if reward.FamilyContribution != 100 || reward.PersonalContribution != 150 {
		t.Fatalf("defeat reward contributions = %+v", reward)
	}
	state := familyBossState(familyID, 1)
	if state.Hp != 321 || state.HasReward {
		t.Fatalf("defeat state = %+v, want hp 321 without shared claim", state)
	}
	duplicate := server.settleFamilyBossBattle(ch, defeat, false)
	if duplicate.FamilyContribution != 0 || ss.familyContribute != 100 || ss.personalContribute != 150 {
		t.Fatalf("duplicate settlement changed contributions: duplicate=%+v family=%d personal=%d", duplicate, ss.familyContribute, ss.personalContribute)
	}

	victory := &battleState{mapID: -1, monsters: []*monsterUnit{{hp: 0, maxHP: 1000, alive: false}}}
	server.settleFamilyBossBattle(ch, victory, true)
	state = familyBossState(familyID, 1)
	if state.Hp != 0 || !state.HasReward || ss.familyContribute != 200 || ss.personalContribute != 300 {
		t.Fatalf("victory settlement state=%+v family=%d personal=%d", state, ss.familyContribute, ss.personalContribute)
	}
}

func TestFamilyBossSettlementShowsContributionTip(t *testing.T) {
	oldTables := tables
	tables = &datatables{
		familyBossConfig: map[int64]map[string]interface{}{1: {
			"MonsterId": 50001, "Contribute": 100, "PersonalContribute": 150,
		}},
		monsterBase: map[int64]map[string]interface{}{50001: {"Hp": 1000}},
	}
	t.Cleanup(func() { tables = oldTables })
	const familyID int64 = 123456793
	ss := newSession()
	ss.playerID, ss.familyID = 7, familyID
	ss.bag = make(map[int32]*bagItem)
	conn := &recordingConn{}
	ch := &channel{id: 7, conn: conn, session: ss}
	battle := &battleState{mapID: -1, monsters: []*monsterUnit{{hp: 0, maxHP: 1000, alive: false}}}
	(&Server{}).settleFamilyBossBattle(ch, battle, true)

	found := false
	for _, frame := range decodeRecordedFrames(t, conn.Bytes()) {
		if frame.opcode != protocol.OpM2C_SendTip {
			continue
		}
		var tip protocol.M2C_SendTip
		if err := proto.Unmarshal(frame.body, &tip); err != nil {
			t.Fatal(err)
		}
		if tip.Message == "家族BOSS结算：家族贡献 +100，个人贡献 +150" && tip.ActorId == ss.playerID {
			found = true
		}
	}
	if !found {
		t.Fatalf("contribution tip missing from settlement frames: %v", recordedOpcodes(t, conn.Bytes()))
	}
}

func familyBossRewardTablesForTest() *datatables {
	return &datatables{
		familyBossConfig: map[int64]map[string]interface{}{1: {
			"MonsterId": 50001, "Contribute": 100, "PersonalContribute": 150,
			"Dropasubset": 10051, "BonusDropChance": 0,
		}},
		monsterBase: map[int64]map[string]interface{}{50001: {"Hp": 1000}},
		parentset: map[int64]map[string]interface{}{10051: {
			"SubsetArr": []interface{}{
				map[string]interface{}{"_Id": 30091},
				map[string]interface{}{"_Id": 30092},
			},
		}},
		sonSet: map[int64]map[string]interface{}{
			30091: {"DropArr": []interface{}{map[string]interface{}{
				"_Id": 110331, "Weight": 1, "MinCount": 2, "MaxCount": 2,
			}}},
			30092: {"DropArr": []interface{}{map[string]interface{}{
				"_Id": 20046, "Weight": 1, "MinCount": 1, "MaxCount": 1,
			}}},
		},
	}
}

func prepareFamilyBossClaimTest(t *testing.T, familyID int64, playerIDs ...int64) {
	t.Helper()
	clearFamilyBossRewardClaims(familyID)
	familyBossStates.mu.Lock()
	delete(familyBossStates.m, familyID)
	familyBossStates.mu.Unlock()
	state := familyBossState(familyID, 1)
	state.Hp = 0
	state.HasReward = true
	server := &Server{}
	for _, playerID := range playerIDs {
		if !server.grantFamilyBossReward(familyID, 1, playerID) {
			t.Fatalf("grant family boss reward to player %d", playerID)
		}
	}
	t.Cleanup(func() {
		clearFamilyBossRewardClaims(familyID)
		familyBossStates.mu.Lock()
		delete(familyBossStates.m, familyID)
		familyBossStates.mu.Unlock()
	})
}

func TestFamilyBossClaimUsesOnlineDropChainAndNativeResponse(t *testing.T) {
	oldTables := tables
	tables = familyBossRewardTablesForTest()
	t.Cleanup(func() { tables = oldTables })
	const familyID int64 = 123456790
	prepareFamilyBossClaimTest(t, familyID, 8)

	ss := newSession()
	ss.playerID, ss.familyID = 8, familyID
	ss.familyContribute, ss.personalContribute = 100, 150
	ss.bag = make(map[int32]*bagItem)
	conn := &recordingConn{}
	ch := &channel{id: 8, conn: conn, session: ss}
	server := &Server{}

	if result := server.onGetFamilyBossReward(ch, &protocol.C2M_GetFamilyBossReward{RpcId: 9, BossId: 1}); result != nil {
		t.Fatalf("successful claim returned fallback response: %+v", result)
	}
	if bagItemCount(ss, 110331) != 2 || bagItemCount(ss, 20046) != 1 {
		t.Fatalf("online family boss drops missing: bag=%+v", ss.bag)
	}
	if ss.familyContribute != 100 || ss.personalContribute != 150 {
		t.Fatalf("claim repeated contributions: family=%d personal=%d", ss.familyContribute, ss.personalContribute)
	}
	state := familyBossState(familyID, 1)
	if state.Hp != 0 || !state.HasReward {
		t.Fatalf("claimed boss state=%+v, want defeated state retained", state)
	}

	var itemInfos []*protocol.ItemInfo
	for _, frame := range decodeRecordedFrames(t, conn.Bytes()) {
		if frame.opcode != protocol.OpM2C_GetFamilyBossReward {
			continue
		}
		var response protocol.M2C_GetFamilyBossReward
		if err := proto.Unmarshal(frame.body, &response); err != nil {
			t.Fatal(err)
		}
		if response.RpcId != 9 || response.Message != "" {
			t.Fatalf("claim response=%+v", &response)
		}
		for _, raw := range bytesFields(t, frame.body, 1) {
			var item protocol.ItemInfo
			if err := proto.Unmarshal(raw, &item); err != nil {
				t.Fatal(err)
			}
			itemInfos = append(itemInfos, &item)
		}
	}
	wantItems := []*protocol.ItemInfo{{Id: 20046, Count: 1}, {Id: 110331, Count: 2}}
	if len(itemInfos) != len(wantItems) ||
		itemInfos[0].Id != wantItems[0].Id || itemInfos[0].Count != wantItems[0].Count ||
		itemInfos[1].Id != wantItems[1].Id || itemInfos[1].Count != wantItems[1].Count {
		t.Fatalf("native ItemList=%v, want %v", itemInfos, wantItems)
	}
	if result := server.onGetFamilyBossReward(ch, &protocol.C2M_GetFamilyBossReward{BossId: 1}); result == nil {
		t.Fatal("duplicate family boss claim succeeded")
	}
}

func TestFamilyBossEachEligibleMemberClaimsOnce(t *testing.T) {
	oldTables := tables
	tables = familyBossRewardTablesForTest()
	t.Cleanup(func() { tables = oldTables })
	const familyID int64 = 123456791
	prepareFamilyBossClaimTest(t, familyID, 20, 21)
	server := &Server{}
	channels := make([]*channel, 2)
	for i := range channels {
		ss := newSession()
		ss.playerID, ss.familyID = int64(20+i), familyID
		ss.bag = make(map[int32]*bagItem)
		channels[i] = &channel{id: int64(20 + i), conn: &recordingConn{}, session: ss}
	}

	start := make(chan struct{})
	results := make([]proto.Message, len(channels))
	var wait sync.WaitGroup
	for i, ch := range channels {
		wait.Add(1)
		go func(index int, contender *channel) {
			defer wait.Done()
			<-start
			results[index] = server.onGetFamilyBossReward(contender, &protocol.C2M_GetFamilyBossReward{BossId: 1})
		}(i, ch)
	}
	close(start)
	wait.Wait()

	successes := 0
	paidPlayers := 0
	for i, result := range results {
		if result == nil {
			successes++
		}
		if bagItemCount(channels[i].session, 110331) == 2 && bagItemCount(channels[i].session, 20046) == 1 {
			paidPlayers++
		}
	}
	if successes != 2 || paidPlayers != 2 {
		t.Fatalf("member claims successes=%d paidPlayers=%d results=%v", successes, paidPlayers, results)
	}
	for _, ch := range channels {
		if result := server.onGetFamilyBossReward(ch, &protocol.C2M_GetFamilyBossReward{BossId: 1}); result == nil {
			t.Fatalf("player %d claimed the same family boss reward twice", ch.session.playerID)
		}
	}
}

func TestFamilyBossConcurrentDuplicateClaimPaysOneSession(t *testing.T) {
	oldTables := tables
	tables = familyBossRewardTablesForTest()
	t.Cleanup(func() { tables = oldTables })
	const familyID int64 = 123456793
	const playerID int64 = 22
	prepareFamilyBossClaimTest(t, familyID, playerID)
	server := &Server{}
	channels := make([]*channel, 2)
	for i := range channels {
		ss := newSession()
		ss.playerID, ss.familyID = playerID, familyID
		ss.bag = make(map[int32]*bagItem)
		channels[i] = &channel{id: int64(40 + i), conn: &recordingConn{}, session: ss}
	}

	start := make(chan struct{})
	results := make([]proto.Message, len(channels))
	var wait sync.WaitGroup
	for i, ch := range channels {
		wait.Add(1)
		go func(index int, contender *channel) {
			defer wait.Done()
			<-start
			results[index] = server.onGetFamilyBossReward(contender, &protocol.C2M_GetFamilyBossReward{BossId: 1})
		}(i, ch)
	}
	close(start)
	wait.Wait()

	successes := 0
	for _, result := range results {
		if result == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("duplicate concurrent claim successes=%d, want 1", successes)
	}
}

func TestFamilyBossInfoReturnsRewardAvailabilityPerCharacter(t *testing.T) {
	const familyID int64 = 123456794
	prepareFamilyBossClaimTest(t, familyID, 50, 51)
	server := &Server{}
	if !server.consumeFamilyBossReward(familyID, 1, 50) {
		t.Fatal("failed to consume first member claim")
	}

	hasReward := func(playerID int64) bool {
		ss := newSession()
		ss.playerID, ss.familyID = playerID, familyID
		conn := &recordingConn{}
		ch := &channel{id: playerID, conn: conn, session: ss}
		if result := server.onGetFamilyBossInfo(ch, &protocol.C2M_GetFamilyBossInfo{}); result != nil {
			t.Fatalf("get family boss info returned fallback: %+v", result)
		}
		frame := findRecordedFrame(t, conn.Bytes(), protocol.OpM2C_GetFamilyBossInfo)
		for _, raw := range bytesFields(t, frame.body, 1) {
			var info protocol.BossInfo
			if err := proto.Unmarshal(raw, &info); err != nil {
				t.Fatal(err)
			}
			if info.Index == 1 {
				return info.HasReward
			}
		}
		t.Fatal("family boss info did not contain boss 1")
		return false
	}
	if hasReward(50) {
		t.Fatal("claimed member still sees an available family boss reward")
	}
	if !hasReward(51) {
		t.Fatal("unclaimed participating member lost the family boss reward")
	}
}

func TestFamilyBossPartySettlementCannotReopenClaim(t *testing.T) {
	oldTables := tables
	tables = familyBossRewardTablesForTest()
	t.Cleanup(func() { tables = oldTables })
	const familyID int64 = 123456792
	prepareFamilyBossClaimTest(t, familyID)
	state := familyBossState(familyID, 1)
	state.Hp, state.HasReward = state.MaxHp, false

	server := &Server{}
	party := &partyBattle{}
	channels := make([]*channel, 2)
	battles := make([]*battleState, 2)
	for i := range channels {
		ss := newSession()
		ss.playerID, ss.familyID = int64(30+i), familyID
		ss.bag = make(map[int32]*bagItem)
		channels[i] = &channel{id: int64(30 + i), conn: &recordingConn{}, session: ss}
		battles[i] = &battleState{
			mapID: -1, party: party,
			monsters: []*monsterUnit{{hp: 0, maxHP: 1000, alive: false}},
		}
	}

	server.settleFamilyBossBattle(channels[0], battles[0], true)
	if result := server.onGetFamilyBossReward(channels[0], &protocol.C2M_GetFamilyBossReward{BossId: 1}); result != nil {
		t.Fatalf("first party claim failed: %+v", result)
	}
	server.settleFamilyBossBattle(channels[1], battles[1], true)
	state = familyBossState(familyID, 1)
	if state.Hp != 0 || !state.HasReward {
		t.Fatalf("party claim reset defeated boss state: %+v", state)
	}
	if result := server.onGetFamilyBossReward(channels[1], &protocol.C2M_GetFamilyBossReward{BossId: 1}); result != nil {
		t.Fatalf("second party member claim failed: %+v", result)
	}
	for _, ch := range channels {
		if ch.session.familyContribute != 100 || ch.session.personalContribute != 150 {
			t.Fatalf("member %d contributions family=%d personal=%d", ch.session.playerID,
				ch.session.familyContribute, ch.session.personalContribute)
		}
	}
}

func TestFamilyBossBonusPoolExcludesOrdinaryEquipment(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.FamilyBoss.BonusDropChancePercent = 100
		config.FamilyBoss.SkinSharePercent = 100
		config.FamilyBoss.GemItemIDs = []int32{20046, 20047}
		config.FamilyBoss.SkinItemIDs = []int32{120594, 120595}
	})
	oldTables := tables
	tables = &datatables{
		materialBase: map[int64]map[string]interface{}{
			20046: {"MaterialType": 2},
			20047: {"MaterialType": 1},
		},
		skinBase:  map[int64]map[string]interface{}{120594: {}, 120595: {}},
		equipBase: map[int64]map[string]interface{}{120594: {}, 120001: {}},
	}
	t.Cleanup(func() { tables = oldTables })
	gems, skins := familyBossBonusPools()
	if !reflect.DeepEqual(gems, []int32{20046}) || !reflect.DeepEqual(skins, []int32{120594}) {
		t.Fatalf("bonus pools gems=%v skins=%v", gems, skins)
	}
	row := map[string]interface{}{}
	rolls := []float64{0, 0, 0}
	got := rollFamilyBossBonusItem(row, func() float64 {
		value := rolls[0]
		rolls = rolls[1:]
		return value
	})
	if got != 120594 {
		t.Fatalf("skin bonus item=%d, want 120594", got)
	}
	row["BonusSkinShare"] = 0
	rolls = []float64{0, 0, 0}
	got = rollFamilyBossBonusItem(row, func() float64 {
		value := rolls[0]
		rolls = rolls[1:]
		return value
	})
	if got != 20046 {
		t.Fatalf("gem bonus item=%d, want 20046", got)
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

func TestBossDamageRankUsesRecordedMemberDamage(t *testing.T) {
	members := []*familyMember{{ID: 7, Name: "leader"}, {ID: 8, Name: "member"}}
	rank := encodeBossDamageRankWithDamageAndTreatment(120, members,
		map[int64]int32{7: 90, 8: 30}, map[int64]int32{7: 140})
	entries := bytesFields(t, rank, 2)
	var first, second protocol.BossDamagePerMemberMap
	if err := proto.Unmarshal(entries[0], &first); err != nil {
		t.Fatal(err)
	}
	if err := proto.Unmarshal(entries[1], &second); err != nil {
		t.Fatal(err)
	}
	if first.Id != 7 || first.Damage != 90 || first.Treat != 140 || second.Id != 8 || second.Damage != 30 || second.Treat != 0 {
		t.Fatalf("recorded rank = %+v, %+v", &first, &second)
	}
}

func TestBossDamageRankOnlyIncludesParticipatingMembers(t *testing.T) {
	members := []*familyMember{
		{ID: 7, Name: "spectator"},
		{ID: 8, Name: "attacker"},
		{ID: 9, Name: "support"},
	}
	rank := encodeBossDamageRankWithParticipation(120, members,
		map[int64]int32{8: 120}, map[int64]int32{9: 40}, map[int64]bool{8: true, 9: true})
	entries := bytesFields(t, rank, 2)
	if len(entries) != 2 {
		t.Fatalf("participation rank entries=%d, want 2", len(entries))
	}
	for index, wantID := range []int64{8, 9} {
		var entry protocol.BossDamagePerMemberMap
		if err := proto.Unmarshal(entries[index], &entry); err != nil {
			t.Fatal(err)
		}
		if entry.Id != wantID {
			t.Fatalf("participation rank entry %d player=%d, want %d", index, entry.Id, wantID)
		}
	}
}

func TestBossDamageRankTreatmentOnlyDoesNotInventDamage(t *testing.T) {
	members := []*familyMember{{ID: 7, Name: "attacker"}, {ID: 8, Name: "healer"}}
	rank := encodeBossDamageRankWithDamageAndTreatment(0, members, nil, map[int64]int32{8: 140})
	entries := bytesFields(t, rank, 2)
	if len(entries) != len(members) {
		t.Fatalf("treatment-only rank entries=%d, want %d", len(entries), len(members))
	}
	var attacker, healer protocol.BossDamagePerMemberMap
	if err := proto.Unmarshal(entries[0], &attacker); err != nil {
		t.Fatal(err)
	}
	if err := proto.Unmarshal(entries[1], &healer); err != nil {
		t.Fatal(err)
	}
	if attacker.Id != 7 || attacker.Damage != 0 || attacker.Treat != 0 ||
		healer.Id != 8 || healer.Damage != 0 || healer.Treat != 140 {
		t.Fatalf("treatment-only rank invented damage: attacker=%+v healer=%+v", &attacker, &healer)
	}
}

// 家族BOSS「治疗量」只认「玩家治疗队友」。自我回血（数值属性 1044 生命回复，
// 走 ApplyHealthRecovery → applyHealInternal(ref, ref, …)）与自己吸血都是自己→自己，
// 必须排除，否则只打 303 点伤害的角色也会显示几千万治疗量（文档/34 §4、§15.8）。
func TestFamilyBossTreatmentCreditExcludesSelfHeal(t *testing.T) {
	healer := PlayerCombatUnit(7)
	teammate := PlayerCombatUnit(8)
	monster := MonsterCombatUnit(1001)

	cases := []struct {
		name  string
		event CombatEvent
		want  int32
	}{
		{"heals a teammate", CombatEvent{Type: CombatEventHeal, Source: healer, Target: teammate, Amount: 140}, 140},
		{"self passive regen", CombatEvent{Type: CombatEventHeal, Source: healer, Target: healer, Amount: 448647}, 0},
		{"self lifesteal", CombatEvent{Type: CombatEventLifesteal, Source: healer, Target: healer, Amount: 900}, 0},
		{"heals a monster", CombatEvent{Type: CombatEventHeal, Source: healer, Target: monster, Amount: 140}, 0},
		{"monster heals a player", CombatEvent{Type: CombatEventHeal, Source: monster, Target: teammate, Amount: 140}, 0},
		{"damage is not treatment", CombatEvent{Type: CombatEventDamage, Source: healer, Target: monster, Amount: 140}, 0},
		{"zero amount", CombatEvent{Type: CombatEventHeal, Source: healer, Target: teammate, Amount: 0}, 0},
	}
	for _, test := range cases {
		if got := familyBossTreatmentCredit(test.event); got != test.want {
			t.Errorf("%s: credit = %d, want %d", test.name, got, test.want)
		}
	}
}
