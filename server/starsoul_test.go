package main

import (
	"bytes"
	"testing"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

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
	if got.Message != "缺少星魂碎片 x1" {
		t.Fatalf("exchange without fragment message = %q", got.Message)
	}
	if len(ss.ensureStarSoulBag().Items) != 0 {
		t.Fatal("exchange without fragment created a star soul")
	}
}
