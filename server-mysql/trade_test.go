package main

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func tradeTestChannel(id, playerID int64, name string) (*channel, *recordingConn) {
	ss := newSession()
	ss.state = sessInGame
	ss.playerID = playerID
	ss.name = name
	ss.mapID = 10004
	ss.x, ss.y = 0, 0
	ss.coin = 100
	ss.bag = make(map[int32]*bagItem)
	conn := &recordingConn{}
	return &channel{id: id, session: ss, conn: conn}, conn
}

func TestTradeCompatibilityUsesNegativeTeamRequestMarker(t *testing.T) {
	tradeMu.Lock()
	trades = make(map[int64]*tradeState)
	tradeSeq.Store(0)
	tradeMu.Unlock()
	server := &Server{conns: make(map[int64]*channel)}
	first, _ := tradeTestChannel(1, 101, "first")
	second, secondConn := tradeTestChannel(2, 202, "second")
	server.conns[first.id] = first
	server.conns[second.id] = second

	response := server.onRequestTeam(first, &protocol.C2M_RequestTeam{TargetId: -second.session.playerID}).(*protocol.M2C_RequestTeam)
	if response.Message != "" {
		t.Fatalf("compat request message=%q", response.Message)
	}
	frames := decodeRecordedFrames(t, secondConn.Bytes())
	if len(frames) != 1 || frames[0].opcode != protocol.OpM2C_RequestList {
		t.Fatalf("compat invite opcodes=%v", recordedOpcodes(t, secondConn.Bytes()))
	}
	var invite protocol.M2C_RequestList
	if err := proto.Unmarshal(frames[0].body, &invite); err != nil {
		t.Fatal(err)
	}
	if invite.UnitId != first.session.playerID || invite.ActorId != -first.session.playerID {
		t.Fatalf("compat invite=%+v", &invite)
	}

	handled := server.onHandleTeam(second, &protocol.C2M_HandleTeam{
		HandleInfo: &protocol.HandleInfo{Id: -first.session.playerID, Bool: false},
		IsRequest:  true,
	})
	if handled == nil || first.session.tradeID != 0 || second.session.tradeID != 0 {
		t.Fatal("compat reject did not close trade")
	}
}

func TestTradeCompatibilityWorkspaceOpensAndCompletesWithoutUnknownOpcodes(t *testing.T) {
	tradeMu.Lock()
	trades = make(map[int64]*tradeState)
	tradeSeq.Store(0)
	tradeMu.Unlock()
	server := &Server{conns: make(map[int64]*channel)}
	first, firstConn := tradeTestChannel(1, 101, "first")
	second, secondConn := tradeTestChannel(2, 202, "second")
	first.session.bag[0] = &bagItem{ItemId: 110305, ItemType: 3, ServerId: 7001, Count: 3}
	second.session.bag[0] = &bagItem{ItemId: 110306, ItemType: 3, ServerId: 8001, Count: 2}
	server.conns[first.id] = first
	server.conns[second.id] = second

	server.onRequestTeam(first, &protocol.C2M_RequestTeam{TargetId: -second.session.playerID})
	handle := server.onHandleTeam(second, &protocol.C2M_HandleTeam{
		HandleInfo: &protocol.HandleInfo{Id: -first.session.playerID, Bool: true},
		IsRequest:  true,
		RpcId:      77,
	})
	if response, ok := handle.(*protocol.M2C_HandleTrade); !ok || response.RpcId != 77 {
		t.Fatalf("compat handle response lost rpc id: %#v", handle)
	}
	for name, conn := range map[string]*recordingConn{"first": firstConn, "second": secondConn} {
		opcodes := recordedOpcodes(t, conn.Bytes())
		if !containsOpcode(opcodes, protocol.OpM2C_OpenStoreUI) {
			t.Fatalf("%s did not receive trade workspace open: %v", name, opcodes)
		}
		if containsOpcode(opcodes, protocol.OpM2C_TradeState) {
			t.Fatalf("%s received unsupported trade-state opcode: %v", name, opcodes)
		}
	}
	frames := decodeRecordedFrames(t, firstConn.Bytes())
	foundOpen := false
	for _, frame := range frames {
		if frame.opcode != protocol.OpM2C_OpenStoreUI {
			continue
		}
		var open protocol.M2C_OpenStoreUI
		if err := proto.Unmarshal(frame.body, &open); err != nil {
			t.Fatal(err)
		}
		foundOpen = open.ActorId == -first.session.playerID
	}
	if !foundOpen {
		t.Fatal("trade workspace did not carry the negative open marker")
	}

	tradeID := first.session.tradeID
	if got := server.onPutInStore(first, &protocol.C2M_PutInStore{BagIndex: 0, Count: 2, Page: 0}); got != nil {
		t.Fatalf("compat put item returned %+v", got)
	}
	if first.session.bag[0].Count != 3 {
		t.Fatal("offering an item removed it before confirmation")
	}
	if got := server.onPutInStore(second, &protocol.C2M_PutInStore{BagIndex: 0, Count: 1, Page: 0}); got != nil {
		t.Fatalf("compat second put item returned %+v", got)
	}
	coinResp := server.onPutCoinInStore(first, &protocol.C2M_PutCoinInStore{Coin: 25}).(*protocol.M2C_PutCoinInStore)
	if coinResp.Message != "" || coinResp.Coin != 25 || first.session.coin != 100 {
		t.Fatalf("compat coin offer=%+v wallet=%d", coinResp, first.session.coin)
	}
	coinResp2 := server.onPutCoinInStore(second, &protocol.C2M_PutCoinInStore{Coin: 10}).(*protocol.M2C_PutCoinInStore)
	if coinResp2.Message != "" || coinResp2.Coin != 10 || second.session.coin != 100 {
		t.Fatalf("compat second coin offer=%+v wallet=%d", coinResp2, second.session.coin)
	}

	if got := server.onSortStore(first, &protocol.C2M_SortStore{Page: 0}); got != nil {
		t.Fatalf("compat first lock returned %+v", got)
	}
	if first.session.tradeID != tradeID || second.session.tradeID != tradeID {
		t.Fatal("compat trade completed before the second participant locked")
	}
	if got := server.onSortStore(second, &protocol.C2M_SortStore{Page: 0}); got == nil ||
		got.(*protocol.M2C_SortStore).Message != "交易完成" {
		t.Fatalf("compat second lock completion response=%+v", got)
	}
	if first.session.tradeID != 0 || second.session.tradeID != 0 || tradeID == 0 {
		t.Fatalf("compat trade did not close: id=%d sessions=%d/%d", tradeID, first.session.tradeID, second.session.tradeID)
	}
	if first.session.coin != 85 || second.session.coin != 115 {
		t.Fatalf("compat coins after trade=%d/%d", first.session.coin, second.session.coin)
	}
	if bagItemCount(first.session, 110305) != 1 || bagItemCount(first.session, 110306) != 1 ||
		bagItemCount(second.session, 110305) != 2 || bagItemCount(second.session, 110306) != 1 {
		t.Fatal("compat trade item exchange mismatch")
	}
}

func TestTradeCompatibilitySnapshotShowsBothSidesAndResetsForNextTrade(t *testing.T) {
	tradeMu.Lock()
	trades = make(map[int64]*tradeState)
	tradeSeq.Store(0)
	tradeMu.Unlock()
	server := &Server{conns: make(map[int64]*channel)}
	first, _ := tradeTestChannel(1, 101, "first")
	second, _ := tradeTestChannel(2, 202, "second")
	first.session.coin = 20000
	second.session.coin = 20000
	first.session.bag[0] = &bagItem{ItemId: 110305, ItemType: 3, ServerId: 7001, Count: 3}
	second.session.bag[0] = &bagItem{ItemId: 110306, ItemType: 3, ServerId: 8001, Count: 2}
	server.conns[first.id] = first
	server.conns[second.id] = second

	server.onRequestTeam(first, &protocol.C2M_RequestTeam{TargetId: -second.session.playerID})
	server.onHandleTeam(second, &protocol.C2M_HandleTeam{
		HandleInfo: &protocol.HandleInfo{Id: -first.session.playerID, Bool: true}, IsRequest: true,
	})
	server.onPutInStore(first, &protocol.C2M_PutInStore{BagIndex: 0, Count: 2, Page: 0})
	server.onPutInStore(second, &protocol.C2M_PutInStore{BagIndex: 0, Count: 1, Page: 0})
	server.onPutCoinInStore(first, &protocol.C2M_PutCoinInStore{Coin: 12345})
	server.onPutCoinInStore(second, &protocol.C2M_PutCoinInStore{Coin: 6789})

	handled, firstView := compatTradeSnapshotView(first)
	if !handled || firstView.selfCoin != 12345 || firstView.otherCoin != 6789 {
		t.Fatalf("first trade view=%+v handled=%v", firstView, handled)
	}
	if firstView.items[0] == nil || firstView.items[0].ItemId != 110305 ||
		firstView.items[5] == nil || firstView.items[5].ItemId != 110306 {
		t.Fatalf("first left/right items=%+v", firstView.items)
	}
	wire := appendCompatTradeView(nil, compatTradeView{
		selfCoin: 12345, otherCoin: 6789, selfLocked: true, otherConfirmed: true,
	})
	var decoded protocol.M2C_GetStore
	if err := proto.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SelfCoin != 12345 || decoded.OtherCoin != 6789 || !decoded.SelfLocked || decoded.OtherLocked ||
		decoded.SelfConfirmed || !decoded.OtherConfirmed {
		t.Fatalf("decoded compatibility fields=%+v", &decoded)
	}
	_, secondView := compatTradeSnapshotView(second)
	if secondView.selfCoin != 6789 || secondView.otherCoin != 12345 ||
		secondView.items[0] == nil || secondView.items[0].ItemId != 110306 ||
		secondView.items[5] == nil || secondView.items[5].ItemId != 110305 {
		t.Fatalf("second trade view=%+v", secondView)
	}
	if _, response := server.compatTradeTakeItem(first, &protocol.C2M_TakeOffStore{
		StoreIndex: 5, Count: 1, Page: 0,
	}); response == nil || response.(*protocol.M2C_TakeOffStore).Message == "" {
		t.Fatal("remote-side trade item was removable")
	}

	server.onExtandStore(first, &protocol.C2M_ExtandStore{})
	if first.session.tradeCompatOpen || first.session.tradeCompatClosed || first.session.tradeID != 0 {
		t.Fatalf("first close state=%+v", first.session)
	}
	if second.session.tradeID != 0 {
		t.Fatalf("second trade id remained=%d", second.session.tradeID)
	}
	if second.session.tradeCompatOpen || !second.session.tradeCompatClosed {
		t.Fatalf("remote close marker was lost: %+v", second.session)
	}
	if handled, remoteClosed := compatTradeSnapshotView(second); !handled || remoteClosed.message == "" {
		t.Fatalf("remote close snapshot=%+v handled=%v", remoteClosed, handled)
	}
	server.onRequestTeam(first, &protocol.C2M_RequestTeam{TargetId: -second.session.playerID})
	server.onHandleTeam(second, &protocol.C2M_HandleTeam{
		HandleInfo: &protocol.HandleInfo{Id: -first.session.playerID, Bool: true}, IsRequest: true,
	})
	if !first.session.tradeCompatOpen || !second.session.tradeCompatOpen ||
		first.session.tradeID == 0 || second.session.tradeID != first.session.tradeID {
		t.Fatalf("second trade did not reinitialize: first=%+v second=%+v", first.session, second.session)
	}
}

func TestNormalStoreOpenClearsCompletedCompatibilityTradeMarker(t *testing.T) {
	tradeMu.Lock()
	trades = make(map[int64]*tradeState)
	tradeSeq.Store(0)
	tradeMu.Unlock()
	server := &Server{conns: make(map[int64]*channel)}
	ch, conn := tradeTestChannel(1, 101, "warehouse")
	ch.session.store[0] = &bagItem{ItemId: 110305, ItemType: 3, ServerId: 7001, Count: 2}
	ch.session.tradeCompatClosed = true
	server.conns[ch.id] = ch

	server.pushOpenStoreUI(ch)
	if ch.session.tradeCompatOpen || ch.session.tradeCompatClosed || ch.session.tradeID != 0 {
		t.Fatalf("normal store open retained trade state: open=%v closed=%v id=%d",
			ch.session.tradeCompatOpen, ch.session.tradeCompatClosed, ch.session.tradeID)
	}

	if got := server.onGetStore(ch, &protocol.C2M_GetStore{Page: 0, RpcId: 12}); got != nil {
		t.Fatalf("normal store get returned unexpected response: %+v", got)
	}
	frames := decodeRecordedFrames(t, conn.Bytes())
	var storeFrame *recordedFrame
	for index := range frames {
		if frames[index].opcode == protocol.OpM2C_GetStore {
			storeFrame = &frames[index]
		}
	}
	if storeFrame == nil {
		t.Fatalf("normal store open did not return %d", protocol.OpM2C_GetStore)
	}
	var response protocol.M2C_GetStore
	if err := proto.Unmarshal(storeFrame.body, &response); err != nil {
		t.Fatal(err)
	}
	if response.Message != "" || response.Total < 1 {
		t.Fatalf("normal store response retained trade message: %+v", &response)
	}
	if entries := bytesFields(t, storeFrame.body, 1); len(entries) != 1 {
		t.Fatalf("normal store response omitted StoreList: entries=%d", len(entries))
	}

	if got := server.onTakeOffStore(ch, &protocol.C2M_TakeOffStore{StoreIndex: 0, Count: 1, Page: 0, RpcId: 13}); got != nil {
		t.Fatalf("normal store take-out returned unexpected response: %+v", got)
	}
	if bagItemCount(ch.session, 110305) != 1 || ch.session.store[0] == nil || ch.session.store[0].Count != 1 {
		t.Fatalf("normal store take-out did not move one item: bag=%d store=%+v",
			bagItemCount(ch.session, 110305), ch.session.store[0])
	}
}

func TestTradeCompatibilitySnapshotKeepsRemoteEquipmentDetails(t *testing.T) {
	equipment := &bagItem{
		ItemId: 121214, ItemType: 1, ServerId: 9001, Count: 1,
		SpecialKey: 3, Star: 9, Quality: 5, Level: 12, SpecialId: 77,
		GemList: []int32{20101, 0},
	}
	wire := appendCompatTradeView(nil, compatTradeView{
		items: map[int32]*bagItem{5: equipment},
	})
	entries := bytesFields(t, wire, 1)
	if len(entries) != 1 {
		t.Fatalf("remote equipment entries=%d", len(entries))
	}
	var decoded protocol.BagMap
	if err := proto.Unmarshal(entries[0], &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Index != 5 {
		t.Fatalf("remote equipment slot=%d", decoded.Index)
	}
	detail := decoded.EquipTransMessage
	if detail == nil || detail.EquipId != equipment.ItemId || detail.Star != equipment.Star ||
		detail.Quality != equipment.Quality || detail.Level != equipment.Level ||
		detail.SpecialId != equipment.SpecialId {
		t.Fatalf("remote equipment tooltip detail=%+v", detail)
	}
}

func TestTradeExchangesBagInstancesAndCoinsAfterBothLock(t *testing.T) {
	tradeMu.Lock()
	trades = make(map[int64]*tradeState)
	tradeSeq.Store(0)
	tradeMu.Unlock()
	server := &Server{conns: make(map[int64]*channel)}
	first, firstConn := tradeTestChannel(1, 101, "甲")
	second, secondConn := tradeTestChannel(2, 202, "乙")
	first.session.bag[0] = &bagItem{ItemId: 110305, ItemType: 3, ServerId: 7001, Count: 3}
	second.session.bag[0] = &bagItem{ItemId: 110306, ItemType: 3, ServerId: 8001, Count: 2}

	server.conns[first.id] = first
	server.conns[second.id] = second
	if got := server.onRequestTrade(first, &protocol.C2M_RequestTrade{TargetId: second.session.playerID}); got == nil {
		t.Fatal("request trade returned nil response")
	}
	tradeID := first.session.tradeID
	if tradeID == 0 || second.session.tradeID != tradeID {
		t.Fatalf("trade ids not assigned: first=%d second=%d", tradeID, second.session.tradeID)
	}
	if opcodes := recordedOpcodes(t, secondConn.Bytes()); len(opcodes) == 0 || opcodes[0] != protocol.OpM2C_RequestList {
		t.Fatalf("invite opcodes=%v", opcodes)
	}
	if got := server.onHandleTrade(second, &protocol.C2M_HandleTrade{TradeId: tradeID, Accept: true}); got == nil {
		t.Fatal("accept trade returned nil response")
	}
	if got := server.onTradeOffer(first, &protocol.C2M_TradeOffer{
		TradeId: tradeID, Coin: 25,
		Items: []*protocol.TradeItem{{Index: 0, Count: 2}},
	}); got == nil {
		t.Fatal("first offer returned nil response")
	}
	if got := server.onTradeOffer(second, &protocol.C2M_TradeOffer{
		TradeId: tradeID, Coin: 10,
		Items: []*protocol.TradeItem{{Index: 0, Count: 1}},
	}); got == nil {
		t.Fatal("second offer returned nil response")
	}
	if got := server.onTradeLock(first, &protocol.C2M_TradeLock{TradeId: tradeID, Locked: true}); got == nil {
		t.Fatal("first lock returned nil response")
	}
	if first.session.tradeID != tradeID || second.session.tradeID != tradeID {
		t.Fatal("trade completed before the second participant locked")
	}
	if got := server.onTradeLock(second, &protocol.C2M_TradeLock{TradeId: tradeID, Locked: true}); got == nil {
		t.Fatal("second lock returned nil response")
	}

	if first.session.tradeID != 0 || second.session.tradeID != 0 {
		t.Fatal("trade state was not cleared")
	}
	if first.session.coin != 85 || second.session.coin != 115 {
		t.Fatalf("coins after trade=%d/%d, want 85/115", first.session.coin, second.session.coin)
	}
	if got := bagItemCount(first.session, 110305); got != 1 {
		t.Fatalf("first remaining item count=%d, want 1", got)
	}
	if got := bagItemCount(first.session, 110306); got != 1 {
		t.Fatalf("first received item count=%d, want 1", got)
	}
	if got := bagItemCount(second.session, 110305); got != 2 {
		t.Fatalf("second received item count=%d, want 2", got)
	}
	if got := bagItemCount(second.session, 110306); got != 1 {
		t.Fatalf("second remaining item count=%d, want 1", got)
	}
	if !containsOpcode(recordedOpcodes(t, firstConn.Bytes()), protocol.OpM2C_TradeState) ||
		!containsOpcode(recordedOpcodes(t, secondConn.Bytes()), protocol.OpM2C_TradeState) {
		t.Fatal("trade state was not pushed to both participants")
	}
}

func TestTradeOfferRejectsLockedOrChangedItems(t *testing.T) {
	tradeMu.Lock()
	trades = make(map[int64]*tradeState)
	tradeSeq.Store(100)
	tradeMu.Unlock()
	server := &Server{conns: make(map[int64]*channel)}
	first, _ := tradeTestChannel(11, 111, "甲")
	second, _ := tradeTestChannel(12, 222, "乙")
	first.session.bag[0] = &bagItem{ItemId: 110305, ItemType: 3, ServerId: 9001, Count: 1}
	server.conns[first.id] = first
	server.conns[second.id] = second
	if got := server.onRequestTrade(first, &protocol.C2M_RequestTrade{TargetId: second.session.playerID}); got == nil {
		t.Fatal("request trade returned nil response")
	}
	id := first.session.tradeID
	if got := server.onHandleTrade(second, &protocol.C2M_HandleTrade{TradeId: id, Accept: true}); got == nil {
		t.Fatal("accept trade returned nil response")
	}
	if got := server.onTradeOffer(first, &protocol.C2M_TradeOffer{
		TradeId: id, Items: []*protocol.TradeItem{{Index: 0, Count: 1}},
	}); got == nil {
		t.Fatal("initial offer returned nil response")
	}
	first.session.bag[0].ServerId = 9002
	resp := server.onTradeLock(first, &protocol.C2M_TradeLock{TradeId: id, Locked: true}).(*protocol.M2C_TradeLock)
	if resp.Message == "" {
		t.Fatal("changed item was accepted into trade")
	}
	server.cancelTradeForPlayer(first.session.playerID, "test cleanup")
}
