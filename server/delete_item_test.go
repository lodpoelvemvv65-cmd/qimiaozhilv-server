package main

import (
	"testing"

	"mhqserver/protocol"
)

func TestDeleteItemReturnsBagSnapshotAndHonorsLock(t *testing.T) {
	ss := newSession()
	ss.playerID = 9
	ss.bag = map[int32]*bagItem{
		2: {ItemId: 110305, ItemType: int32(protocol.ItemType_GoodsItem), Count: 3},
		3: {ItemId: 120877, ItemType: int32(protocol.ItemType_EquipItem), Count: 1, IsLock: true},
	}
	conn := &recordingConn{}
	ch := &channel{id: 1, conn: conn, session: ss}
	server := &Server{}

	if got := server.onDeleteItem(ch, &protocol.C2M_DeleteItem{RpcId: 1, Index: 2, Count: 2}); got != nil {
		t.Fatalf("successful discard returned %+v, expected raw response", got)
	}
	if ss.bag[2] == nil || ss.bag[2].Count != 1 {
		t.Fatalf("remaining stack = %#v", ss.bag[2])
	}
	response := findRecordedFrame(t, conn.Bytes(), protocol.OpM2C_DeleteItem)
	if len(response.body) == 0 {
		t.Fatal("discard response is empty")
	}
	bagPush := findRecordedFrame(t, conn.Bytes(), protocol.OpM2C_SendBag)
	if len(bagPush.body) == 0 {
		t.Fatal("discard did not push the updated bag snapshot")
	}

	locked := server.onDeleteItem(ch, &protocol.C2M_DeleteItem{RpcId: 2, Index: 3, Count: 1}).(*protocol.M2C_DeleteItem)
	if locked.Error != 0 || locked.Message == "" || ss.bag[3] == nil {
		t.Fatalf("locked discard response=%+v item=%#v", locked, ss.bag[3])
	}
}
