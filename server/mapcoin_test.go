package main

import (
	"strings"
	"testing"
	"time"

	"mhqserver/protocol"
)

func TestMapCoinDailyLimitAndPurchasedCount(t *testing.T) {
	ss := newSession()
	ss.playerID = 7
	ss.voucher = 40
	ch := &channel{id: 1, session: ss}
	server := &Server{}

	first := server.onGetMapCoin(ch, &protocol.C2M_GetMapCoin{RpcId: 1}).(*protocol.M2C_GetMapCoin)
	if first.Error != 0 || first.Message != "" || ss.coin != 500 || ss.signin.MapCoinClaimed != 1 {
		t.Fatalf("first map coin = %+v, state=%+v coin=%d", first, ss.signin, ss.coin)
	}
	exhausted := server.onGetMapCoin(ch, &protocol.C2M_GetMapCoin{RpcId: 2}).(*protocol.M2C_GetMapCoin)
	if exhausted.Error != 0 || !strings.Contains(exhausted.Message, "次数") || ss.coin != 500 {
		t.Fatalf("exhausted map coin = %+v, coin=%d", exhausted, ss.coin)
	}
	added := server.onAddMapCoinCount(ch, &protocol.C2M_AddMapCoinCount{RpcId: 3}).(*protocol.M2C_AddMapCoinCount)
	if added.Error != 0 || added.Message != "" || ss.voucher != 20 || ss.signin.MapCoinExtra != 1 {
		t.Fatalf("add count = %+v, state=%+v voucher=%d", added, ss.signin, ss.voucher)
	}
	second := server.onGetMapCoin(ch, &protocol.C2M_GetMapCoin{RpcId: 4}).(*protocol.M2C_GetMapCoin)
	if second.Error != 0 || second.Message != "" || ss.coin != 1000 || ss.signin.MapCoinClaimed != 2 {
		t.Fatalf("purchased map coin = %+v, state=%+v coin=%d", second, ss.signin, ss.coin)
	}
}

func TestMapCoinPurchaseRejectsWithoutRPCErrorAndResetsNextDay(t *testing.T) {
	ss := newSession()
	ss.playerID = 7
	ss.voucher = 19
	ss.signin = &signinState{MapCoinDay: time.Now().Add(-24 * time.Hour).Format("20060102"), MapCoinExtra: 9, MapCoinClaimed: 9}
	ch := &channel{id: 1, session: ss}
	server := &Server{}

	rejected := server.onAddMapCoinCount(ch, &protocol.C2M_AddMapCoinCount{RpcId: 5}).(*protocol.M2C_AddMapCoinCount)
	if rejected.Error != 0 || !strings.Contains(rejected.Message, "20") || ss.voucher != 19 {
		t.Fatalf("insufficient voucher response = %+v, voucher=%d", rejected, ss.voucher)
	}
	if ss.signin.MapCoinExtra != 0 || ss.signin.MapCoinClaimed != 0 || ss.signin.MapCoinDay != time.Now().Format("20060102") {
		t.Fatalf("daily reset state = %+v", ss.signin)
	}
}
