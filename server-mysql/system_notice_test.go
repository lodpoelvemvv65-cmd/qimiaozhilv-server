package main

import (
	"reflect"
	"testing"

	"google.golang.org/protobuf/proto"
	"mhqserver/protocol"
)

func TestSendRewardAlsoWritesNativeSystemChannel(t *testing.T) {
	oldTables := tables
	tables = &datatables{
		equipBase: map[int64]map[string]interface{}{
			120001: {"Name": "测试装备"},
		},
		goodsBase: map[int64]map[string]interface{}{
			110204: {"Name": "代金券"},
		},
		materialBase: map[int64]map[string]interface{}{
			130001: {"Name": "测试材料"},
		},
	}
	t.Cleanup(func() { tables = oldTables })

	ch, conn := consistencyChannel(7001, 1000401)
	reward := &protocol.M2C_SendReward{
		Exp: 2300, Coin: 380, ActorId: ch.session.playerID,
		ItemList: []*protocol.RewardItem{
			{Id: 120001, ItemType: protocol.ItemType_EquipItem, Count: 1},
			{Id: 110204, ItemType: protocol.ItemType_GoodsItem, Count: 8},
			{Id: 130001, ItemType: protocol.ItemType_MaterialsItem, Count: 2},
		},
	}
	(&Server{}).sendReward(ch, reward)

	frames := decodeRecordedFrames(t, conn.Bytes())
	if len(frames) != 2 || frames[0].opcode != protocol.OpM2C_SendReward ||
		frames[1].opcode != protocol.OpM2C_SendSystemChat {
		t.Fatalf("reward opcodes=%v, want SendReward then SendSystemChat", recordedOpcodes(t, conn.Bytes()))
	}
	var notice protocol.M2C_SendSystemChat
	if err := proto.Unmarshal(frames[1].body, &notice); err != nil {
		t.Fatal(err)
	}
	wantLines := []string{
		"获得经验：2300",
		"获得铜币：380",
		"获得测试装备 x1",
		"获得代金券 x8",
		"获得测试材料 x2",
	}
	gotLines := make([]string, 0, len(wantLines))
	for _, raw := range bytesFields(t, frames[1].body, 1) {
		gotLines = append(gotLines, string(raw))
	}
	if notice.Type != protocol.ChatType_System || notice.Name != systemNoticeSender ||
		notice.IsSystemBrocast || notice.ActorId != ch.session.playerID ||
		!reflect.DeepEqual(gotLines, wantLines) {
		t.Fatalf("system reward notice=%+v, want lines=%v", &notice, wantLines)
	}
}

func TestSendRewardSkipsEmptySystemNotice(t *testing.T) {
	ch, conn := consistencyChannel(7002, 1000401)
	(&Server{}).sendReward(ch, &protocol.M2C_SendReward{ActorId: ch.session.playerID})

	if got := recordedOpcodes(t, conn.Bytes()); !reflect.DeepEqual(got, []uint16{protocol.OpM2C_SendReward}) {
		t.Fatalf("empty reward opcodes=%v, want only SendReward", got)
	}
}
