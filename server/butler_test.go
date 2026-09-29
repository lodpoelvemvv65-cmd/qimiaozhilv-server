package main

import (
	"reflect"
	"strings"
	"testing"

	"mhqserver/protocol"
)

func TestButlerRenameCostsClientConfirmedVoucherAmount(t *testing.T) {
	ss := newSession()
	ss.playerID, ss.name, ss.voucher = 7, "旧名字", 1000
	ch := &channel{id: 1, conn: &recordingConn{}, session: ss}
	server := &Server{}

	resp := server.onChangeNickName(ch, &protocol.C2M_ChangeNickName{RpcId: 1, Name: "新名字"}).(*protocol.M2C_ChangeNickName)
	if resp.Error != 0 || resp.Message != "" || ss.name != "新名字" || ss.voucher != 0 {
		t.Fatalf("rename response=%+v name=%q voucher=%d", resp, ss.name, ss.voucher)
	}

	resp = server.onChangeNickName(ch, &protocol.C2M_ChangeNickName{RpcId: 2, Name: "再改一次"}).(*protocol.M2C_ChangeNickName)
	if resp.Error != 0 || !strings.Contains(resp.Message, "1000") || ss.name != "新名字" || ss.voucher != 0 {
		t.Fatalf("insufficient rename response=%+v name=%q voucher=%d", resp, ss.name, ss.voucher)
	}
}

func TestButlerStoreRoundTripPreservesEquipmentInstance(t *testing.T) {
	ss := newSession()
	ss.playerID = 8
	ss.bag = map[int32]*bagItem{3: {
		ItemId: 120877, ItemType: int32(protocol.ItemType_EquipItem), ServerId: 88,
		Count: 1, IsLock: true, Quality: 4, Star: 10, Level: 13,
		SpecialKey: 7, SpecialId: 99, GetSource: "手工匠人",
		MainAttr: map[int32]float32{7: 0.15}, RandomAttrs: []int32{101, 202},
		AddAttrs: []int32{110001}, GemList: []int32{20046, 0},
	}}
	ss.store = make(map[int32]*bagItem)
	original := cloneBagItem(ss.bag[3])
	ch := &channel{id: 2, conn: &recordingConn{}, session: ss}
	server := &Server{}

	if got := server.onPutInStore(ch, &protocol.C2M_PutInStore{RpcId: 3, BagIndex: 3, Count: 1}); got != nil {
		t.Fatalf("put store returned %+v, raw response expected", got)
	}
	if ss.bag[3] != nil || ss.store[0] == nil {
		t.Fatalf("put store state bag=%v store=%v", ss.bag, ss.store)
	}
	if got := server.onTakeOffStore(ch, &protocol.C2M_TakeOffStore{RpcId: 4, StoreIndex: 0, Count: 1}); got != nil {
		t.Fatalf("take store returned %+v, raw response expected", got)
	}
	if ss.store[0] != nil || ss.bag[0] == nil || !reflect.DeepEqual(ss.bag[0], original) {
		t.Fatalf("equipment changed across store round trip: got=%#v want=%#v", ss.bag[0], original)
	}
}
