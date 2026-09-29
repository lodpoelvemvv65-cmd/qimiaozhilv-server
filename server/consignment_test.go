package main

import (
	"testing"
	"time"

	"mhqserver/protocol"
)

func TestConsignmentFirstPageShowsNewestListings(t *testing.T) {
	consignMu.Lock()
	previous := consignItems
	now := time.Now()
	consignItems = make([]*consignmentItem, 0, 15)
	for id := int64(1); id <= 15; id++ {
		consignItems = append(consignItems, &consignmentItem{
			ID: id, ItemId: 110305, ItemType: int32(protocol.ItemType_GoodsItem),
			Count: 1, Price: 100, putAt: now.Add(time.Duration(id) * time.Second),
		})
	}
	consignMu.Unlock()
	t.Cleanup(func() {
		consignMu.Lock()
		consignItems = previous
		consignMu.Unlock()
	})

	items := (&Server{}).consignmentSnapshot(protocol.JobType_UnKnown, protocol.ItemType_NoneItem)
	if len(items) != 15 {
		t.Fatalf("snapshot count = %d, want 15", len(items))
	}
	for index, item := range items {
		want := int64(15 - index)
		if item.ID != want {
			t.Fatalf("snapshot item %d ID = %d, want newest-first %d", index, item.ID, want)
		}
	}
	if items[consignPageSize-1].ID != 4 {
		t.Fatalf("page zero ended with ID %d, want 4", items[consignPageSize-1].ID)
	}
}
