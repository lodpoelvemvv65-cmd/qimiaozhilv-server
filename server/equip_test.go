package main

import (
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func withEquipTable(t *testing.T, itemID, specialKey int32, equipType ...int32) {
	t.Helper()
	oldTables := tables
	t.Cleanup(func() { tables = oldTables })
	row := map[string]interface{}{
		"SpecialKey": json.Number(strconv.Itoa(int(specialKey))),
		"Quality":    json.Number("4"),
		"Star":       json.Number("5"),
	}
	if len(equipType) > 0 {
		row["Type"] = json.Number(strconv.Itoa(int(equipType[0])))
	}
	tables = &datatables{equipBase: map[int64]map[string]interface{}{
		int64(itemID): {
			"SpecialKey": row["SpecialKey"],
			"Quality":    row["Quality"],
			"Star":       row["Star"],
		},
	}}
	if value, ok := row["Type"]; ok {
		tables.equipBase[int64(itemID)]["Type"] = value
	}
}

func TestNewBagItemUsesEquipSpecialKey(t *testing.T) {
	const itemID int32 = 120592
	withEquipTable(t, itemID, 1)

	it := newBagItem(itemID)
	if it.SpecialKey != 1 {
		t.Fatalf("SpecialKey = %d, want 1", it.SpecialKey)
	}

	it.SpecialId = 77
	var got protocol.EquipTransMessage
	if err := proto.Unmarshal(encodeEquipTrans(it), &got); err != nil {
		t.Fatalf("decode EquipTransMessage: %v", err)
	}
	if got.SpecialKey != 1 {
		t.Fatalf("wire specialKey = %d, want 1", got.SpecialKey)
	}
	if got.SpecialId != 77 {
		t.Fatalf("wire specialId = %d, want 77", got.SpecialId)
	}
}

func TestBagFromJSONRepairsLegacyEquipSpecialKey(t *testing.T) {
	const itemID int32 = 120592
	withEquipTable(t, itemID, 1)

	legacy := `[{"k":5,"v":{"i":120592,"t":1,"c":1,"q":4,"r":5,"v":1,"p":77}}]`
	bag := bagFromJSON(legacy)
	it := bag[5]
	if it == nil {
		t.Fatal("legacy item was not loaded")
	}
	if it.SpecialKey != 1 {
		t.Fatalf("repaired SpecialKey = %d, want 1", it.SpecialKey)
	}
	if it.SpecialId != 77 {
		t.Fatalf("legacy SpecialId = %d, want 77", it.SpecialId)
	}

	roundTrip := bagFromJSON(bagToJSON(bag))[5]
	if roundTrip == nil || roundTrip.SpecialKey != 1 || roundTrip.SpecialId != 77 {
		t.Fatalf("round-trip item = %#v, want SpecialKey/SpecialId 1/77", roundTrip)
	}
}

func TestEncodeEquipTransRepairsInMemoryLegacyItem(t *testing.T) {
	const itemID int32 = 120592
	withEquipTable(t, itemID, 1)

	it := &bagItem{ItemId: itemID, ItemType: 1, Count: 1}
	var got protocol.EquipTransMessage
	if err := proto.Unmarshal(encodeEquipTrans(it), &got); err != nil {
		t.Fatalf("decode EquipTransMessage: %v", err)
	}
	if got.SpecialKey != 1 || it.SpecialKey != 1 {
		t.Fatalf("SpecialKey wire/item = %d/%d, want 1/1", got.SpecialKey, it.SpecialKey)
	}
}

func TestEquipDetailsUseAttributeTypeKeysAndZeroInstanceDelta(t *testing.T) {
	const itemID int32 = 120592
	withEquipTable(t, itemID, 1)
	tables.equipBase[int64(itemID)]["Hp"] = json.Number("100")
	tables.equipBase[int64(itemID)]["PhyAtk"] = json.Number("20")
	tables.equipBase[int64(itemID)]["Pcrir"] = json.Number("0.1")
	tables.equipBase[int64(itemID)]["Mcrir"] = json.Number("0.1")
	tables.equipBase[int64(itemID)]["Dvo"] = json.Number("0.05")

	raw := encodeEquipTrans(newBagItem(itemID))
	var keys []int32
	for len(raw) > 0 {
		number, wireType, tagLen := protowire.ConsumeTag(raw)
		if tagLen < 0 {
			t.Fatal(protowire.ParseError(tagLen))
		}
		raw = raw[tagLen:]
		if number != 5 || wireType != protowire.BytesType {
			fieldLen := protowire.ConsumeFieldValue(number, wireType, raw)
			if fieldLen < 0 {
				t.Fatal(protowire.ParseError(fieldLen))
			}
			raw = raw[fieldLen:]
			continue
		}
		payload, fieldLen := protowire.ConsumeBytes(raw)
		if fieldLen < 0 {
			t.Fatal(protowire.ParseError(fieldLen))
		}
		raw = raw[fieldLen:]
		var attribute protocol.AttributeMap
		if err := proto.Unmarshal(payload, &attribute); err != nil {
			t.Fatal(err)
		}
		if attribute.Key < 1 || attribute.Key > 31 {
			t.Fatalf("attribute key = %d, want AttributeType 1..31", attribute.Key)
		}
		if attribute.Value != 0 {
			t.Fatalf("template instance delta = %v, want 0", attribute.Value)
		}
		keys = append(keys, attribute.Key)
	}
	if want := []int32{1, 7, 11, 12, 19}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("attribute keys = %v, want %v", keys, want)
	}
}

func TestEquipHighAttributeFieldsUseClientAttributeTypeOrder(t *testing.T) {
	want := map[string]int32{
		"Sta": 20, "Phy": 21, "PhyDA": 22, "MicDA": 23,
		"Nphyi": 30, "Nmeni": 31,
	}
	got := make(map[string]int32, len(want))
	for _, attribute := range equipFieldToAttributeType {
		if _, ok := want[attribute.field]; ok {
			got[attribute.field] = attribute.key
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("high attribute mapping = %v, want %v", got, want)
	}
}

func TestEncodeNetItemIncludesSingleCount(t *testing.T) {
	raw := encodeNetItem(&bagItem{ItemId: 120592, ItemType: 1, Count: 1})
	var item protocol.NetItem
	if err := proto.Unmarshal(raw, &item); err != nil {
		t.Fatal(err)
	}
	if item.Count != 1 {
		t.Fatalf("wire Count = %d, want 1", item.Count)
	}
}

func TestPutOnBusinessRejectIsSuccessfulRPCWithCurrentLists(t *testing.T) {
	ss := &session{
		bag: map[int32]*bagItem{
			3: {ItemId: 120590, ItemType: 1, Count: 1},
		},
		worn: map[int32]*bagItem{
			1: {ItemId: 120592, ItemType: 1, Count: 1},
		},
	}
	body, err := encodePutOnResponse(ss, 77, "职业不符")
	if err != nil {
		t.Fatal(err)
	}
	var response protocol.M2C_PutOn
	if err := proto.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	if response.RpcId != 77 || response.Error != 0 || response.Message != "职业不符" {
		t.Fatalf("response = %+v", &response)
	}
	if got := countBytesField(t, body, 1); got != 1 {
		t.Fatalf("BagMapList entries = %d, want 1", got)
	}
	if got := countBytesField(t, body, 2); got != 1 {
		t.Fatalf("WornBagMapList entries = %d, want 1", got)
	}
}

func TestNormalizeWornSlotsUsesOnlineEquipPosition(t *testing.T) {
	const itemID int32 = 121152
	oldTables := tables
	t.Cleanup(func() { tables = oldTables })
	tables = &datatables{equipBase: map[int64]map[string]interface{}{
		int64(itemID): {
			"Type":       json.Number("8"),
			"SpecialKey": json.Number("1"),
			"Quality":    json.Number("4"),
			"Star":       json.Number("10"),
		},
	}}
	ss := &session{worn: map[int32]*bagItem{
		3: {ItemId: itemID, ItemType: 1, Count: 1},
	}}

	if !normalizeWornSlots(ss) {
		t.Fatal("expected misplaced equipment to be normalized")
	}
	if ss.worn[3] != nil || ss.worn[8] == nil || ss.worn[8].ItemId != itemID {
		t.Fatalf("worn slots = %#v, want item %d in slot 8", ss.worn, itemID)
	}
}

func TestWeaponUsesDefaultTypeZeroSlotAndIsEncoded(t *testing.T) {
	const itemID int32 = 121214
	withEquipTable(t, itemID, 7) // SpecialKey=7 is PhyAtk; Type is omitted for weapons.
	it := newBagItem(itemID)
	if got := equipSlotForItem(it); got != 0 {
		t.Fatalf("weapon slot = %d, want 0", got)
	}

	ss := &session{worn: map[int32]*bagItem{7: it}}
	if !normalizeWornSlots(ss) {
		t.Fatal("expected legacy SpecialKey slot to be normalized")
	}
	if ss.worn[7] != nil || ss.worn[0] != it {
		t.Fatalf("worn slots = %#v, want weapon in slot 0", ss.worn)
	}
	if pairs := bagPairs(ss.worn); len(pairs) != 1 || pairs[0][0].(int32) != 0 {
		t.Fatalf("encoded pairs = %#v, want slot 0", pairs)
	}
}

func TestEmptyWornBagMapClearsClientSlot(t *testing.T) {
	raw := encodeEmptyWornBagMap(0)
	var got protocol.BagMap
	if err := proto.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode empty worn BagMap: %v", err)
	}
	if got.Index != 0 {
		t.Fatalf("empty worn slot index = %d, want weapon slot 0", got.Index)
	}
	if got.NetItem == nil {
		t.Fatal("empty worn slot must contain an explicit NetItem tombstone")
	}
	if got.NetItem.ItemId != 0 || got.NetItem.ItemType != 0 {
		t.Fatalf("empty worn NetItem = %#v, want zero item/type", got.NetItem)
	}
}

func TestGetOtherCharacterClearsAllSlotsAndPushesTargetNumerics(t *testing.T) {
	loadOnlineTablesForTest(t)
	viewerSession := newSession()
	viewerSession.playerID, viewerSession.jobID, viewerSession.skinID, viewerSession.level = 10, 1, 1, 10
	viewer := &channel{id: 1, conn: &recordingConn{}, session: viewerSession}
	targetSession := newSession()
	targetSession.playerID, targetSession.jobID, targetSession.skinID, targetSession.level = 20, 1, 1, 25
	targetSession.worn = make(map[int32]*bagItem)
	targetSession.worn[0] = newBagItem(121214)
	target := &channel{id: 2, conn: &recordingConn{}, session: targetSession}
	server := &Server{conns: map[int64]*channel{viewer.id: viewer, target.id: target}}

	if response := server.onGetCharacter(viewer, &protocol.C2M_GetCharacter{RpcId: 7, Id: targetSession.playerID}); response != nil {
		t.Fatalf("onGetCharacter returned unexpected response: %+v", response)
	}
	frames := decodeRecordedFrames(t, viewer.conn.(*recordingConn).Bytes())
	seenTargetAttack := false
	var characterBody []byte
	for _, frame := range frames {
		switch frame.opcode {
		case protocol.OpM2C_SyncUnitAttribute:
			var message protocol.M2C_SyncUnitAttribute
			if err := proto.Unmarshal(frame.body, &message); err != nil {
				t.Fatal(err)
			}
			if message.UnitId == targetSession.playerID && message.NumericType == 1009 &&
				message.Value == float32(targetSession.playerPhyAtk()) {
				seenTargetAttack = true
			}
		case protocol.OpM2C_GetCharacter:
			characterBody = frame.body
		}
	}
	if !seenTargetAttack {
		t.Fatal("viewing another character did not initialize the target's full NumericComponent")
	}
	if got := countBytesField(t, characterBody, 3); got != 13 {
		t.Fatalf("WornBagMapList entries = %d, want 12 empty slots plus target weapon", got)
	}
}

func TestBagSlotsUseClientZeroBasedCapacity(t *testing.T) {
	ss := &session{bag: make(map[int32]*bagItem)}
	if got := nextBagIndex(ss); got != 0 {
		t.Fatalf("first empty bag slot = %d, want 0", got)
	}
	for index := int32(0); index < bagSlotCount; index++ {
		ss.bag[index] = &bagItem{ItemId: 20001, ItemType: 3, Count: 1}
	}
	if got := nextBagIndex(ss); got != invalidBagSlot {
		t.Fatalf("full bag returned slot %d, want %d", got, invalidBagSlot)
	}
}

func TestNormalizeBagSlotsMigratesLegacyOutOfRangeIndexes(t *testing.T) {
	first := &bagItem{ItemId: 20001, ItemType: 3, Count: 1, ServerId: 11}
	second := &bagItem{ItemId: 20002, ItemType: 3, Count: 1, ServerId: 22}
	ss := &session{playerID: 7, bag: map[int32]*bagItem{3: first, 48: second}}
	if !normalizeBagSlots(ss) {
		t.Fatal("legacy bag was not normalized")
	}
	if len(ss.bag) != 2 || ss.bag[0] != first || ss.bag[1] != second {
		t.Fatalf("normalized bag = %#v, want legacy items in slots 0 and 1", ss.bag)
	}
}

func TestPutOnSendsInventorySnapshotBeforeAttributePushes(t *testing.T) {
	const itemID int32 = 120660
	withEquipTable(t, itemID, 9, 2)
	item := newBagItem(itemID)
	item.ServerId = 77
	ss := newSession()
	ss.playerID, ss.jobID, ss.level = 7, 1, 5000
	ss.bag = map[int32]*bagItem{0: item}
	conn := &recordingConn{}
	ch := &channel{id: 1, conn: conn, session: ss}
	if response := (&Server{}).onPutOn(ch, &protocol.C2M_PutOn{RpcId: 9, Index: 0}); response != nil {
		t.Fatalf("put-on returned fallback response: %+v", response)
	}
	if ss.bag[0] != nil || ss.worn[2] != item {
		t.Fatalf("put-on state bag=%#v worn=%#v", ss.bag, ss.worn)
	}
	opcodes := recordedOpcodes(t, conn.Bytes())
	if len(opcodes) == 0 || opcodes[0] != protocol.OpM2C_PutOn {
		t.Fatalf("first put-on frame = %v, want %d", opcodes, protocol.OpM2C_PutOn)
	}
}

func TestEquipBonusUsesStrengthenedSpecialValueAndKeepsRates(t *testing.T) {
	const itemID int32 = 121214
	withEquipTable(t, itemID, 7)
	tables.equipBase[int64(itemID)]["PhyAtk"] = json.Number("80")
	tables.equipBase[int64(itemID)]["Dvo"] = json.Number("0.6")
	tables.equipBase[int64(itemID)]["SpecialValue"] = json.Number("103")
	tables.strengthen = map[int64]map[string]interface{}{
		20: {"AttribteAdd": json.Number("0.981992243576765")},
	}
	ss := &session{worn: map[int32]*bagItem{
		0: {ItemId: itemID, ItemType: 1, Count: 1, Level: 20},
	}}

	bonus := equipBonus(ss)
	wantAttack := 80.0 + 103.0*(1.0+0.981992243576765)
	if math.Abs(float64(bonus[1009])-wantAttack) > 0.001 {
		t.Fatalf("physical attack bonus = %v, want %v", bonus[1009], wantAttack)
	}
	if math.Abs(float64(bonus[1017]-0.6)) > 0.0001 {
		t.Fatalf("dodge rate = %v, want 0.6", bonus[1017])
	}
}

func TestApplySkinEquipRestoresVisualSkinFromWornItem(t *testing.T) {
	const skinID int32 = 120661
	oldTables := tables
	t.Cleanup(func() { tables = oldTables })
	tables = &datatables{
		equipBase: map[int64]map[string]interface{}{
			int64(skinID): {
				"Type":       json.Number("2"),
				"SpecialKey": json.Number("9"),
				"Quality":    json.Number("4"),
				"Star":       json.Number("10"),
			},
		},
		skinBase: map[int64]map[string]interface{}{
			int64(skinID): {"PrfabId": json.Number("45")},
		},
	}
	ss := &session{
		jobID:  1,
		skinID: 1,
		worn: map[int32]*bagItem{
			2: {ItemId: skinID, ItemType: 1, Count: 1},
		},
	}

	applySkinEquip(ss)
	if ss.skinID != skinID {
		t.Fatalf("skinID = %d, want worn skin %d", ss.skinID, skinID)
	}
}

func countBytesField(t *testing.T, raw []byte, target protowire.Number) int {
	t.Helper()
	count := 0
	for len(raw) > 0 {
		number, wireType, tagLen := protowire.ConsumeTag(raw)
		if tagLen < 0 {
			t.Fatal(protowire.ParseError(tagLen))
		}
		raw = raw[tagLen:]
		if wireType == protowire.BytesType {
			_, fieldLen := protowire.ConsumeBytes(raw)
			if fieldLen < 0 {
				t.Fatal(protowire.ParseError(fieldLen))
			}
			if number == target {
				count++
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
	return count
}
