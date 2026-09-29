package main

import (
	"math"
	"reflect"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"mhqserver/protocol"
)

func TestLegacyEquipmentAttributeMigration(t *testing.T) {
	for _, test := range []struct {
		name          string
		row           map[string]interface{}
		before, after map[int32]float32
	}{
		{"stamina", map[string]interface{}{"Sta": 500}, map[int32]float32{20: 0.08361881}, map[int32]float32{21: 0.08361881}},
		{"constitution", map[string]interface{}{"Phy": 500}, map[int32]float32{21: -0.2}, map[int32]float32{20: -0.2}},
		{"damage", map[string]interface{}{"PhyDA": 0.1, "MicDA": 0.2}, map[int32]float32{22: 0.1, 23: -0.2}, map[int32]float32{30: 0.1, 31: -0.2}},
		{"reduction", map[string]interface{}{"Nphyi": 0.1, "Nmeni": 0.2}, map[int32]float32{30: 0.1, 31: -0.2}, map[int32]float32{22: 0.1, 23: -0.2}},
		{"correct", map[string]interface{}{"Sta": 500}, map[int32]float32{21: 0.08}, map[int32]float32{21: 0.08}},
		{"both template fields", map[string]interface{}{"Phy": 400, "Sta": 500}, map[int32]float32{20: 0.1, 21: 0.2}, map[int32]float32{20: 0.1, 21: 0.2}},
		{"existing target", map[string]interface{}{"Sta": 500}, map[int32]float32{20: 0.1, 21: 0}, map[int32]float32{20: 0.1, 21: 0}},
		{"special attribute", map[string]interface{}{"Sta": 500, "SpecialKey": 20, "SpecialValue": 100}, map[int32]float32{20: 0.1}, map[int32]float32{20: 0.1}},
		{"unrelated", map[string]interface{}{"Hp": 500}, map[int32]float32{1: 0.1, 20: 0.2}, map[int32]float32{1: 0.1, 20: 0.2}},
	} {
		t.Run(test.name, func(t *testing.T) {
			oldTables := tables
			tables = &datatables{equipBase: map[int64]map[string]interface{}{121155: test.row}}
			t.Cleanup(func() { tables = oldTables })
			item := &bagItem{ItemId: 121155, ItemType: 1, MainAttr: test.before}
			wantChange := !reflect.DeepEqual(test.before, test.after)
			if changed := repairLegacyEquipmentAttributes(item); changed != wantChange {
				t.Fatalf("changed=%v want=%v", changed, wantChange)
			}
			if !reflect.DeepEqual(item.MainAttr, test.after) || repairLegacyEquipmentAttributes(item) {
				t.Fatalf("attributes=%v want=%v; repair must be idempotent", item.MainAttr, test.after)
			}
		})
	}
}

func TestLegacyShoeMigrationAlignsWireAndStamina(t *testing.T) {
	oldTables := tables
	tables = &datatables{equipBase: map[int64]map[string]interface{}{121155: {"Sta": 500, "Rpcrir": 0.3, "Rmcrir": 0.3}}}
	t.Cleanup(func() { tables = oldTables })
	item := &bagItem{ItemId: 121155, ItemType: 1, MainAttr: map[int32]float32{15: 0.058963835, 16: -0.15928213, 20: 0.083618812}}
	if !ensureEquipmentVariations(map[int32]*bagItem{11: item}) {
		t.Fatal("login did not report migration")
	}
	bonus := equipBonus(&session{worn: map[int32]*bagItem{11: item}})
	if math.Abs(float64(bonus[1035])-541.809406) > 0.001 || bonus[1034] != 0 {
		t.Fatalf("stamina=%v constitution=%v", bonus[1035], bonus[1034])
	}
	raw := mainAttributeOf(item)
	attributes := make(map[int32]float32)
	for len(raw) > 0 {
		_, _, tagLen := protowire.ConsumeTag(raw)
		if tagLen < 0 {
			t.Fatal("invalid attribute tag")
		}
		payload, size := protowire.ConsumeBytes(raw[tagLen:])
		if size < 0 {
			t.Fatal("invalid attribute payload")
		}
		var attribute protocol.AttributeMap
		if err := proto.Unmarshal(payload, &attribute); err != nil {
			t.Fatal(err)
		}
		attributes[attribute.Key] = attribute.Value
		raw = raw[tagLen+size:]
	}
	if _, exists := attributes[20]; exists || attributes[21] != item.MainAttr[21] {
		t.Fatalf("wire attributes=%v", attributes)
	}
}

func TestLegacyEquipmentAttributeMigrationMySQL(t *testing.T) {
	store, err := OpenStore(mysqlTestDSN(t, "legacy-equipment-attributes"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	accountID, err := store.CreateAccount("legacyattrs", "secret")
	if err != nil {
		t.Fatal(err)
	}
	playerID, err := store.CreatePlayer(accountID, "legacyattrs", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	const delta = 0.08361881226301193
	for _, location := range []int{1, 2, 3} {
		_, err := store.db.Exec(`INSERT INTO player_items (player_id, location, slot_index, item_id, item_type, get_source) VALUES (?, ?, 11, 121155, 1, '')`, playerID, location)
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.db.Exec(`INSERT INTO player_item_main_attributes VALUES (?, ?, 11, 20, ?)`, playerID, location, delta)
		if err != nil {
			t.Fatal(err)
		}
	}
	catalog := &datatables{equipBase: map[int64]map[string]interface{}{121155: {"Sta": 500}}}
	if count, err := migrateLegacyEquipmentAttributes(store.db, catalog); err != nil || count != 3 {
		t.Fatalf("migration count=%d err=%v", count, err)
	}
	for _, location := range []int{1, 2, 3} {
		var key int32
		var value float64
		if err := store.db.QueryRow(`SELECT attribute_type, value FROM player_item_main_attributes WHERE player_id=? AND location=? AND slot_index=11`, playerID, location).Scan(&key, &value); err != nil {
			t.Fatal(err)
		}
		if key != 21 || value != delta {
			t.Fatalf("location=%d key=%d delta=%v", location, key, value)
		}
	}
	if count, err := migrateLegacyEquipmentAttributes(store.db, catalog); err != nil || count != 0 {
		t.Fatalf("repeated migration count=%d err=%v", count, err)
	}
}
