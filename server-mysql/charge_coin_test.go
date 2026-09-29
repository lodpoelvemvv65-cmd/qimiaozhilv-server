package main

import (
	"testing"

	"mhqserver/protocol"
)

// 兑换方向：花星币换铜币。客户端弹窗原文
// “请输入您要兑换的星币数量，星币：铜币=1：20”，所以输入的数量是星币，
// 扣 1 个星币进账 20 铜币（旧实现反了：扣铜币给星币）。
func TestChargeCoinExchangesStarCoinsForCopper(t *testing.T) {
	ss := newSession()
	ss.playerID = 71
	ss.coin = 0
	ss.bag = map[int32]*bagItem{
		starCoinBagSlot: {
			ItemId:   consignmentCurrencyItemID,
			ItemType: int32(protocol.ItemType_GoodsItem),
			Count:    100,
		},
	}
	conn := &recordingConn{}
	ch := &channel{id: 1, conn: conn, session: ss}
	server := &Server{}

	response := server.onChargeCoin(ch, &protocol.C2M_ChargeCoin{RpcId: 1, Gem: 101}).(*protocol.M2C_ChargeCoin)
	if response.Message != "星币不足" || response.Error != 0 || ss.coin != 0 || starCoinBalance(ss) != 100 {
		t.Fatalf("insufficient exchange response=%+v coin=%d star=%d", response, ss.coin, starCoinBalance(ss))
	}

	response = server.onChargeCoin(ch, &protocol.C2M_ChargeCoin{RpcId: 2, Gem: 100}).(*protocol.M2C_ChargeCoin)
	if response.Error != 0 || response.Message != "" {
		t.Fatalf("exchange response = %+v", response)
	}
	if ss.coin != 2000 {
		t.Fatalf("copper after exchange = %d, want 2000", ss.coin)
	}
	if got := starCoinBalance(ss); got != 0 {
		t.Fatalf("star coins after exchange = %d, want 0", got)
	}
	if len(ss.bag) != 0 {
		t.Fatalf("bag after exchange = %+v, want empty", ss.bag)
	}
	opcodes := recordedOpcodes(t, conn.Bytes())
	if !containsOpcode(opcodes, protocol.OpM2C_SendBag) {
		t.Fatalf("exchange pushes = %v, missing bag snapshot", opcodes)
	}
	if !containsOpcode(opcodes, protocol.OpM2C_SyncUnitAttribute) {
		t.Fatalf("exchange pushes = %v, missing money update", opcodes)
	}
}

func containsOpcode(opcodes []uint16, target uint16) bool {
	for _, opcode := range opcodes {
		if opcode == target {
			return true
		}
	}
	return false
}
