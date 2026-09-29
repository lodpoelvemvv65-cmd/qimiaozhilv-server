package main

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"reflect"
	"testing"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func TestStarSoulSingleCandidatePositionsUseTheirConfiguredMainAttribute(t *testing.T) {
	loadOnlineTablesForTest(t)
	for _, test := range []struct {
		position    int32
		attributeID int32
	}{
		{position: 9, attributeID: 1105},
		{position: 11, attributeID: 1106},
	} {
		item := generateStarSoul(1001, test.position, 6)
		if item.Main != test.attributeID {
			t.Fatalf("position %d main attribute = %d, want %d", test.position, item.Main, test.attributeID)
		}
		if len(item.Vice) != 3 || len(item.ViceAdd) != 3 {
			t.Fatalf("position %d initial random attributes/tiers = %v/%v, want three of each", test.position, item.Vice, item.ViceAdd)
		}
		for _, attributeID := range item.Vice {
			if got := int32(num(tables.starSoulAttribute[int64(attributeID)]["Type"])); got != 2 {
				t.Fatalf("position %d random attribute %d type = %d, want high-tier type 2", test.position, attributeID, got)
			}
		}
	}
}

func TestStarSoulMainUsesPositionProbabilityAndViceUsesGlobalWeights(t *testing.T) {
	loadOnlineTablesForTest(t)
	item := generateStarSoulWithRand(1001, 0, 6, func(total int) int { return total - 1 })
	if item.Main != 1115 {
		t.Fatalf("position-0 last weighted main = %d, want auxiliary attribute 1115", item.Main)
	}
	if !reflect.DeepEqual(item.Vice, []int32{1150, 1149, 1148}) {
		t.Fatalf("last weighted high-tier initial attributes = %v, want [1150 1149 1148]", item.Vice)
	}

	candidates := starSoulViceCandidates()
	if len(candidates) != 30 {
		t.Fatalf("high-tier random pool size = %d, want 30", len(candidates))
	}
	wantWeights := map[int32]int{1146: 1, 1147: 1, 1148: 1, 1149: 2, 1150: 2}
	roll := 0
	for _, candidate := range candidates {
		if want, ok := wantWeights[candidate.attributeID]; ok && candidate.weight != want {
			t.Errorf("high-tier attribute %d weight = %d, want %d", candidate.attributeID, candidate.weight, want)
		}
		if candidate.attributeID == 1146 {
			break
		}
		roll += candidate.weight
	}
	if got := pickStarSoulVice(candidates, map[int32]bool{}, func(int) int { return roll }); got != 1146 {
		t.Fatalf("lifesteal-rate weighted roll = %d, want 1146", got)
	}
}

func TestHistoricalLowTierViceAttributesMigrateByAttributeKey(t *testing.T) {
	loadOnlineTablesForTest(t)
	item := &starSoulItem{Vice: []int32{1103, 1109}, ViceAdd: []float32{2, 1}}
	if !normalizeStarSoulVice(item) {
		t.Fatal("historical low-tier random attributes were not migrated")
	}
	if !reflect.DeepEqual(item.Vice, []int32{1127, 1133}) || !reflect.DeepEqual(item.ViceAdd, []float32{2, 1}) {
		t.Fatalf("migrated random attributes/tiers = %v/%v, want [1127 1133]/[2 1]", item.Vice, item.ViceAdd)
	}
}

func TestStarSoulExchangeAlwaysProducesTiangongQuality(t *testing.T) {
	loadOnlineTablesForTest(t)
	if got := starSoulExchangeQuality(); got != 6 {
		t.Fatalf("star-soul exchange quality = %d, want 天工 quality 6", got)
	}
	tables.materialBase[int64(starSoulFragment)]["Quality"] = json.Number("1")
	if got := starSoulExchangeQuality(); got != 6 {
		t.Fatalf("star-soul exchange quality changed with fragment quality: %d", got)
	}
}

func TestHistoricalLevel20StarSoulRandomGrowthIsIdempotent(t *testing.T) {
	loadOnlineTablesForTest(t)
	item := &starSoulItem{ID: 1786865535675133900, TypeID: 1001, PosType: 0, Quality: 6, Level: 20, Main: 1103}
	bag := newStarSoulBag()
	bag.Items[item.ID] = item

	if !ensureStarSoulViceGrowth(bag) {
		t.Fatal("historical level-20 star soul was not repaired")
	}
	if item.ViceLevel != 20 || len(item.Vice) != 4 || len(item.ViceAdd) != 4 {
		t.Fatalf("repaired star soul growth=%d vice=%v tiers=%v, want growth 20 and four attributes",
			item.ViceLevel, item.Vice, item.ViceAdd)
	}
	for index, tier := range item.ViceAdd {
		if tier < 0 || tier > 2 {
			t.Fatalf("random attribute %d tier=%v, want 0..2", index, tier)
		}
	}
	wantVice := append([]int32(nil), item.Vice...)
	wantTiers := append([]float32(nil), item.ViceAdd...)
	if ensureStarSoulViceGrowth(bag) {
		t.Fatal("second historical repair changed the star soul again")
	}
	if !reflect.DeepEqual(item.Vice, wantVice) || !reflect.DeepEqual(item.ViceAdd, wantTiers) {
		t.Fatalf("idempotent repair rerolled vice=%v/%v, want %v/%v", item.Vice, item.ViceAdd, wantVice, wantTiers)
	}
}

func TestStarSoulRepairSeedSeparatesSequentialInstances(t *testing.T) {
	loadOnlineTablesForTest(t)
	base := int64(1786865535675133900)
	first := &starSoulItem{ID: base, TypeID: 1001, PosType: 0, Quality: 6, Level: 20}
	second := &starSoulItem{ID: base + 1, TypeID: 1001, PosType: 1, Quality: 6, Level: 20}
	legacyVice0, legacyAdd0 := starSoulViceForSeedWithMax(first, legacyStarSoulRepairSeed(first), 3)
	legacyVice1, legacyAdd1 := starSoulViceForSeedWithMax(second, legacyStarSoulRepairSeed(second), 3)
	if !sameInt32Values(legacyVice0, legacyVice1) || !sameFloat32Values(legacyAdd0, legacyAdd1) {
		t.Fatalf("test fixtures no longer reproduce the old correlated roll: %v/%v vs %v/%v", legacyVice0, legacyAdd0, legacyVice1, legacyAdd1)
	}
	newVice0, newAdd0 := starSoulViceForSeed(first, starSoulRepairSeed(first))
	newVice1, newAdd1 := starSoulViceForSeed(second, starSoulRepairSeed(second))
	if sameInt32Values(newVice0, newVice1) && sameFloat32Values(newAdd0, newAdd1) {
		t.Fatalf("sequential star souls still share one repair roll: %v/%v", newVice0, newAdd0)
	}
}

func TestStarSoulViceRepairMigratesPersistedLegacyRoll(t *testing.T) {
	loadOnlineTablesForTest(t)
	item := &starSoulItem{ID: 1786865535675133900, TypeID: 1001, PosType: 0, Quality: 6, Level: 20, ViceLevel: 20}
	legacyVice, legacyAdd := starSoulViceForSeedWithMax(item, legacyStarSoulRepairSeed(item), 3)
	item.Vice = append([]int32(nil), legacyVice...)
	item.ViceAdd = append([]float32(nil), legacyAdd...)
	bag := newStarSoulBag()
	bag.Items[item.ID] = item
	if !ensureStarSoulViceGrowth(bag) {
		t.Fatal("legacy correlated star soul was not migrated")
	}
	wantVice, wantAdd := starSoulViceForSeed(item, starSoulRepairSeed(item))
	if !sameInt32Values(item.Vice, wantVice) || !sameFloat32Values(item.ViceAdd, wantAdd) {
		t.Fatalf("migrated vice=%v/%v, want %v/%v", item.Vice, item.ViceAdd, wantVice, wantAdd)
	}
	if ensureStarSoulViceGrowth(bag) {
		t.Fatal("second star soul migration changed the repaired roll")
	}
}

func TestStarSoulRandomGrowthTriggersAtEveryFourthLevel(t *testing.T) {
	loadOnlineTablesForTest(t)
	rng := rand.New(rand.NewSource(42))
	item := &starSoulItem{ID: 99, TypeID: 1001, PosType: 1, Quality: 1, Main: 1005}
	initializeStarSoulVice(item, rng.Intn)
	if len(item.Vice) != 1 {
		t.Fatalf("initial random attributes=%v, want one for quality 1", item.Vice)
	}

	item.Level = 3
	if applyStarSoulViceGrowth(item, rng.Intn) || item.ViceLevel != 0 || len(item.Vice) != 1 {
		t.Fatalf("level 3 unexpectedly grew random attributes: growth=%d vice=%v", item.ViceLevel, item.Vice)
	}
	item.Level = 4
	if !applyStarSoulViceGrowth(item, rng.Intn) || item.ViceLevel != 4 || len(item.Vice) != 2 {
		t.Fatalf("level 4 growth=%d vice=%v, want two random attributes", item.ViceLevel, item.Vice)
	}
	item.Level = 8
	if !applyStarSoulViceGrowth(item, rng.Intn) || item.ViceLevel != 8 || len(item.Vice) != 3 {
		t.Fatalf("level 8 growth=%d vice=%v, want three random attributes", item.ViceLevel, item.Vice)
	}
	item.Level = 12
	if !applyStarSoulViceGrowth(item, rng.Intn) || item.ViceLevel != 12 || len(item.Vice) != 4 {
		t.Fatalf("level 12 growth=%d vice=%v, want four random attributes", item.ViceLevel, item.Vice)
	}
	wantVice := append([]int32(nil), item.Vice...)
	wantTiers := append([]float32(nil), item.ViceAdd...)
	if applyStarSoulViceGrowth(item, rng.Intn) || !reflect.DeepEqual(item.Vice, wantVice) || !reflect.DeepEqual(item.ViceAdd, wantTiers) {
		t.Fatal("same-level growth was applied more than once")
	}
}

func TestGetStarSoulBagInitializesEveryEquipmentSlot(t *testing.T) {
	resp := (&Server{}).onGetStarSoulBag(
		&channel{id: 1, session: newSession()},
		&protocol.C2M_GetStarSoulBag{RpcId: 17},
	).(*protocol.M2C_GetStarSoulBag)

	if len(resp.UsedIdMap) != 12 {
		t.Fatalf("usedIdMap length = %d, want 12 equipment slots", len(resp.UsedIdMap))
	}
	for slot, itemID := range resp.UsedIdMap {
		if itemID != 0 {
			t.Fatalf("empty slot %d contains item %d", slot, itemID)
		}
	}

	wire, err := proto.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	// repeated int64 tag 2 is encoded as a packed length-delimited field. The
	// payload must be present even though every value is the proto3 zero value.
	if !bytes.Contains(wire, append([]byte{0x12, 0x0c}, make([]byte, 12)...)) {
		t.Fatalf("wire response does not contain 12 encoded empty slots: %x", wire)
	}

	var decoded protocol.M2C_GetStarSoulBag
	if err := proto.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(decoded.UsedIdMap) != 12 {
		t.Fatalf("decoded usedIdMap length = %d, want 12", len(decoded.UsedIdMap))
	}
}

func TestPutonStarSoulItemTogglesSameItemOff(t *testing.T) {
	ss := newSession()
	ss.playerID = 7001
	ss.bag = make(map[int32]*bagItem)
	item := &starSoulItem{ID: 101, TypeID: 1001, PosType: 2, Main: 1}
	ss.starSoul = newStarSoulBag()
	ss.starSoul.Items[item.ID] = item
	ch := &channel{id: 7001, conn: &recordingConn{}, session: ss}
	server := &Server{}

	if response := server.onPutonStarSoulItem(ch, &protocol.C2M_PutonStarSoulItem{RpcId: 1, ItemId: item.ID}); response.(*protocol.M2C_PutonStarSoulItem).Message != "" {
		t.Fatalf("first equip response = %+v", response)
	}
	if ss.starSoul.Used[item.PosType] != item.ID || !item.IsUsed {
		t.Fatalf("first equip state used=%v itemUsed=%v", ss.starSoul.Used, item.IsUsed)
	}
	if response := server.onPutonStarSoulItem(ch, &protocol.C2M_PutonStarSoulItem{RpcId: 2, ItemId: item.ID}); response.(*protocol.M2C_PutonStarSoulItem).Message != "" {
		t.Fatalf("second equip response = %+v", response)
	}
	if ss.starSoul.Used[item.PosType] != 0 || item.IsUsed {
		t.Fatalf("same-item toggle state used=%v itemUsed=%v, want empty slot", ss.starSoul.Used, item.IsUsed)
	}
}

func TestPutonStarSoulItemReplacesOtherItemAndKeepsLockedGuard(t *testing.T) {
	ss := newSession()
	ss.playerID = 7002
	ss.starSoul = newStarSoulBag()
	old := &starSoulItem{ID: 201, TypeID: 1001, PosType: 1, Main: 1}
	newItem := &starSoulItem{ID: 202, TypeID: 1002, PosType: 1, Main: 1}
	locked := &starSoulItem{ID: 203, TypeID: 1003, PosType: 1, Main: 1, IsLocked: true}
	ss.starSoul.Items[old.ID], ss.starSoul.Items[newItem.ID], ss.starSoul.Items[locked.ID] = old, newItem, locked
	ss.starSoul.Used[1], old.IsUsed = old.ID, true
	ch := &channel{id: 7002, conn: &recordingConn{}, session: ss}
	server := &Server{}

	if response := server.onPutonStarSoulItem(ch, &protocol.C2M_PutonStarSoulItem{ItemId: newItem.ID}); response.(*protocol.M2C_PutonStarSoulItem).Message != "" {
		t.Fatalf("replacement response = %+v", response)
	}
	if ss.starSoul.Used[1] != newItem.ID || !newItem.IsUsed || old.IsUsed {
		t.Fatalf("replacement state used=%v oldUsed=%v newUsed=%v", ss.starSoul.Used, old.IsUsed, newItem.IsUsed)
	}
	response := server.onPutonStarSoulItem(ch, &protocol.C2M_PutonStarSoulItem{ItemId: locked.ID}).(*protocol.M2C_PutonStarSoulItem)
	if response.Message == "" || ss.starSoul.Used[1] != newItem.ID {
		t.Fatalf("locked item changed slot: response=%+v used=%v", response, ss.starSoul.Used)
	}
}

func TestStarSoulExchangeRequiresFragment(t *testing.T) {
	oldTables := tables
	defer func() { tables = oldTables }()
	if err := loadDatatables("../datatable_json"); err != nil {
		t.Fatal(err)
	}
	ss := newSession()
	ss.playerID = 7
	ss.bag = map[int32]*bagItem{1: {ItemId: 20284, ItemType: 3, Count: 1}}
	ch := &channel{id: 1, session: ss}
	resp := (&Server{}).onGetStarSoulItem(ch, &protocol.C2M_GetStarSoulItem{RpcId: 9, ConfigId: 100100})
	got := resp.(*protocol.M2C_GetStarSoulItem)
	if got.Message != "缺少星魂碎片 x30" {
		t.Fatalf("exchange without fragment message = %q", got.Message)
	}
	if len(ss.ensureStarSoulBag().Items) != 0 {
		t.Fatal("exchange without fragment created a star soul")
	}
}

func TestStarSoulExchangeRequiresAllThirtyFragmentsAtomically(t *testing.T) {
	loadOnlineTablesForTest(t)
	ss := newSession()
	ss.playerID = 7
	ss.bag = map[int32]*bagItem{1: {ItemId: starSoulFragment, ItemType: 3, Count: starSoulExchangeCost - 1}}
	ch := &channel{id: 1, session: ss}

	resp := (&Server{}).onGetStarSoulItem(ch, &protocol.C2M_GetStarSoulItem{RpcId: 10, ConfigId: 100903})
	got := resp.(*protocol.M2C_GetStarSoulItem)
	if got.Message != "缺少星魂碎片 x30" {
		t.Fatalf("exchange with 29 fragments message = %q", got.Message)
	}
	if count := bagItemCount(ss, starSoulFragment); count != starSoulExchangeCost-1 {
		t.Fatalf("exchange with insufficient fragments changed count to %d", count)
	}
	if len(ss.ensureStarSoulBag().Items) != 0 {
		t.Fatal("exchange with insufficient fragments created a star soul")
	}
}

func TestStarSoulExchangeConsumesThirtyAndKeepsSelectedTypeAndPosition(t *testing.T) {
	loadOnlineTablesForTest(t)
	ss := newSession()
	ss.playerID = 7
	ss.bag = map[int32]*bagItem{1: {ItemId: starSoulFragment, ItemType: 3, Count: starSoulExchangeCost + 1}}
	ch := &channel{id: 1, session: ss}

	// The client encodes the selected StarSoulTypeConfig id and equipment slot
	// as typeId*100+posType. Attributes remain random by design (the online
	// confirmation text says "selected type's random star soul").
	resp := (&Server{}).onGetStarSoulItem(ch, &protocol.C2M_GetStarSoulItem{RpcId: 11, ConfigId: 100903})
	got := resp.(*protocol.M2C_GetStarSoulItem)
	if got.Message != "" {
		t.Fatalf("successful exchange returned message %q", got.Message)
	}
	if count := bagItemCount(ss, starSoulFragment); count != 1 {
		t.Fatalf("exchange consumed wrong fragment count: remaining=%d, want 1", count)
	}
	bag := ss.ensureStarSoulBag()
	if len(bag.Items) != 1 {
		t.Fatalf("exchange created %d star souls, want 1", len(bag.Items))
	}
	for _, item := range bag.Items {
		if item == nil {
			t.Fatal("exchange created nil star soul")
		}
		if item.TypeID != 1009 || item.PosType != 3 {
			t.Fatalf("exchange ignored selected type/position: type=%d pos=%d", item.TypeID, item.PosType)
		}
		if item.Quality != starSoulExchangeQuality() {
			t.Fatalf("exchange quality=%d, want configured quality %d", item.Quality, starSoulExchangeQuality())
		}
		if item.Main == 0 || len(item.Vice) != 3 || len(item.ViceAdd) != 3 {
			t.Fatalf("exchange attributes=%d/%v/%v, want random quality-6 attributes", item.Main, item.Vice, item.ViceAdd)
		}
	}
}
