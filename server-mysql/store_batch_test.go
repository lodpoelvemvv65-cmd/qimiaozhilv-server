package main

import (
	"path/filepath"
	"testing"

	"mhqserver/protocol"
)

func TestStoreBatchPutTakePreservesStackCount(t *testing.T) {
	ss := newSession()
	ss.playerID = 100
	ss.bag = map[int32]*bagItem{
		3: {ItemId: 110305, ItemType: int32(protocol.ItemType_GoodsItem), Count: 50},
	}
	ss.store = make(map[int32]*bagItem)
	ch := &channel{id: 100, conn: &recordingConn{}, session: ss}
	server := &Server{}

	if got := server.onPutInStore(ch, &protocol.C2M_PutInStore{BagIndex: 3, Count: 50, Page: 0}); got != nil {
		t.Fatalf("batch put returned %+v", got)
	}
	if ss.bag[3] != nil || ss.store[0] == nil || ss.store[0].Count != 50 {
		t.Fatalf("batch put state bag=%v store=%v", ss.bag, ss.store)
	}

	if got := server.onTakeOffStore(ch, &protocol.C2M_TakeOffStore{StoreIndex: 0, Count: 17, Page: 0}); got != nil {
		t.Fatalf("batch take returned %+v", got)
	}
	if ss.store[0] == nil || ss.store[0].Count != 33 || ss.bag[0] == nil || ss.bag[0].Count != 17 {
		t.Fatalf("batch take state bag=%v store=%v", ss.bag, ss.store)
	}
}

func TestStoreOperationsStayWithinRequestedPage(t *testing.T) {
	ss := newSession()
	ss.playerID = 200
	ss.storePages = 2
	ss.bag = map[int32]*bagItem{
		3: {ItemId: 110305, ItemType: int32(protocol.ItemType_GoodsItem), Count: 5},
	}
	ss.store = map[int32]*bagItem{
		0:  {ItemId: 90, ItemType: 2, Count: 1},
		70: {ItemId: 120, ItemType: 3, Count: 1},
		65: {ItemId: 110, ItemType: 2, Count: 1},
	}
	ch := &channel{id: 102, conn: &recordingConn{}, session: ss}
	server := &Server{}

	if got := server.onPutInStore(ch, &protocol.C2M_PutInStore{BagIndex: 3, Count: 5, Page: 1}); got != nil {
		t.Fatalf("page-two put returned %+v", got)
	}
	if ss.store[60] == nil || ss.store[60].ItemId != 110305 {
		t.Fatalf("page-two item not placed at index 60: %#v", ss.store)
	}
	if got := server.onSortStore(ch, &protocol.C2M_SortStore{Page: 1}); got != nil {
		t.Fatalf("page-two sort returned %+v", got)
	}
	if ss.store[0] == nil || ss.store[0].ItemId != 90 {
		t.Fatalf("sorting page two changed page one: %#v", ss.store)
	}
	if ss.store[60].ItemId != 110 || ss.store[61].ItemId != 110305 || ss.store[62].ItemId != 120 {
		t.Fatalf("page-two sort order = %#v", ss.store)
	}
	if got := server.onTakeOffStore(ch, &protocol.C2M_TakeOffStore{StoreIndex: 60, Count: 1, Page: 0}); got == nil {
		t.Fatal("cross-page take unexpectedly succeeded")
	}
	if ss.store[60] == nil {
		t.Fatal("cross-page take removed the item")
	}
}

func TestStoreSortCompactsSingleItemWithinPage(t *testing.T) {
	ss := newSession()
	ss.playerID = 201
	ss.storePages = 2
	ss.store = map[int32]*bagItem{
		0:  {ItemId: 90, ItemType: 2, Count: 1},
		75: {ItemId: 120, ItemType: 2, Count: 1},
	}
	ch := &channel{id: 103, conn: &recordingConn{}, session: ss}
	if got := (&Server{}).onSortStore(ch, &protocol.C2M_SortStore{Page: 1}); got != nil {
		t.Fatalf("single-item sort returned %+v", got)
	}
	if ss.store[60] == nil || ss.store[75] != nil || ss.store[0] == nil {
		t.Fatalf("single-item page was not compacted: %#v", ss.store)
	}
}

func TestStoreTakeSupportsSecondPageAndLegacyRelativeIndex(t *testing.T) {
	ss := newSession()
	ss.playerID = 202
	ss.storePages = 2
	ss.bag = make(map[int32]*bagItem)
	ss.store = map[int32]*bagItem{
		61: {ItemId: 110305, ItemType: int32(protocol.ItemType_GoodsItem), Count: 4},
	}
	ch := &channel{id: 104, conn: &recordingConn{}, session: ss}
	server := &Server{}

	// Current clients send the global slot index.
	if got := server.onTakeOffStore(ch, &protocol.C2M_TakeOffStore{StoreIndex: 61, Page: 1, Count: 2}); got != nil {
		t.Fatalf("global second-page take returned %+v", got)
	}
	if ss.store[61] == nil || ss.store[61].Count != 2 || bagItemCount(ss, 110305) != 2 {
		t.Fatalf("global second-page take state bag=%v store=%v", ss.bag, ss.store)
	}

	// Older StoreUI builds used the page-relative slot offset (1) while still
	// sending Page=1.  The server resolves it to global slot 61.
	if got := server.onTakeOffStore(ch, &protocol.C2M_TakeOffStore{StoreIndex: 1, Page: 1}); got != nil {
		t.Fatalf("legacy relative second-page take returned %+v", got)
	}
	if ss.store[61] == nil || ss.store[61].Count != 1 || bagItemCount(ss, 110305) != 3 {
		t.Fatalf("legacy second-page take state bag=%v store=%v", ss.bag, ss.store)
	}
}

func TestStoreTakeZeroCountMeansSingleItem(t *testing.T) {
	ss := newSession()
	ss.playerID = 203
	ss.bag = make(map[int32]*bagItem)
	ss.store = map[int32]*bagItem{
		0: {ItemId: 110305, ItemType: int32(protocol.ItemType_GoodsItem), Count: 2},
	}
	ch := &channel{id: 105, conn: &recordingConn{}, session: ss}
	if got := (&Server{}).onTakeOffStore(ch, &protocol.C2M_TakeOffStore{StoreIndex: 0, Page: 0}); got != nil {
		t.Fatalf("zero-count take returned %+v", got)
	}
	if ss.store[0] == nil || ss.store[0].Count != 1 || bagItemCount(ss, 110305) != 1 {
		t.Fatalf("zero-count take state bag=%v store=%v", ss.bag, ss.store)
	}
}

func TestStoreSortAndExpandPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	store, err := OpenStore(mysqlTestDSN(t, path))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	accountID, err := store.CreateAccount("store-pages-test", "password")
	if err != nil {
		t.Fatal(err)
	}
	playerID, err := store.CreatePlayer(accountID, "store-pages-player", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.FirstPlayer(accountID)
	if err != nil {
		t.Fatal(err)
	}

	ss := newSession()
	ss.playerID, ss.coin, ss.storePages = playerID, 200000, p.StorePages
	ss.signin = &signinState{}
	ss.bag = map[int32]*bagItem{
		0: {ItemId: 130, ItemType: int32(protocol.ItemType_GoodsItem), Count: 7},
	}
	ss.store = map[int32]*bagItem{
		10: {ItemId: 120, ItemType: 3, Count: 1},
		4:  {ItemId: 110, ItemType: 2, Count: 1},
		8:  {ItemId: 100, ItemType: 2, Count: 1},
	}
	ch := &channel{id: 101, conn: &recordingConn{}, session: ss}
	server := &Server{store: store}
	if got := server.onSortStore(ch, &protocol.C2M_SortStore{Page: 0}); got != nil {
		t.Fatalf("sort returned %+v", got)
	}
	if ss.store[0].ItemId != 100 || ss.store[1].ItemId != 110 || ss.store[2].ItemId != 120 {
		t.Fatalf("sort order = %#v", ss.store)
	}
	if got := server.onPutInStore(ch, &protocol.C2M_PutInStore{BagIndex: 0, Count: 3, Page: 0}); got != nil {
		t.Fatalf("put returned %+v", got)
	}
	if ss.store[3] == nil || ss.store[3].ItemId != 130 || ss.store[3].Count != 3 || ss.bag[0].Count != 4 {
		t.Fatalf("put state bag=%v store=%v", ss.bag, ss.store)
	}
	if got := server.onTakeOffStore(ch, &protocol.C2M_TakeOffStore{StoreIndex: 3, Count: 2, Page: 0}); got != nil {
		t.Fatalf("take returned %+v", got)
	}
	if ss.store[3] == nil || ss.store[3].Count != 1 || bagItemCount(ss, 130) != 6 {
		t.Fatalf("take state bag=%v store=%v", ss.bag, ss.store)
	}
	if got := server.onExtandStore(ch, &protocol.C2M_ExtandStore{}); got == nil {
		t.Fatal("expand did not return response")
	}
	if ss.storePages != defaultStorePages+1 || ss.coin != 100000 {
		t.Fatalf("expand state pages=%d coin=%d", ss.storePages, ss.coin)
	}
	p, err = store.FirstPlayer(accountID)
	if err != nil {
		t.Fatal(err)
	}
	if p.StorePages != defaultStorePages+1 {
		t.Fatalf("persisted store pages=%d, want %d", p.StorePages, defaultStorePages+1)
	}
	if p.Relations.store[3] == nil || p.Relations.store[3].ItemId != 130 || p.Relations.store[3].Count != 1 {
		t.Fatalf("persisted store item=%v, want item 130 count 1", p.Relations.store[3])
	}
	if bagItemCount(&session{bag: p.Relations.bag}, 130) != 6 {
		t.Fatalf("persisted bag items=%v, want item 130 count 6", p.Relations.bag)
	}
}
