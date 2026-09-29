package main

import (
	"math"
	"testing"

	"mhqserver/protocol"
)

func TestGMCurrencyAliases(t *testing.T) {
	ss := &session{coin: 1, yuanBao: 2, voucher: 3, honor: 4, pvpCurrency: 5, familyContribute: 6}
	ss.bag = map[int32]*bagItem{starCoinBagSlot: {
		ItemId: consignmentCurrencyItemID, ItemType: int32(protocol.ItemType_GoodsItem), Count: 7,
	}}
	tests := []struct {
		name  string
		value int64
	}{
		{"coin", 1},
		{"yuanBao", 2},
		{"yuan_bao", 2},
		{"voucher", 3},
		{"honor", 4},
		{"pvpCurrency", 5},
		{"pvp_currency", 5},
		{"familyContribution", 6},
		{"family_contribute", 6},
		{"starCoin", 7},
		{"star_coin", 7},
		{"gem", 7},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := gmCurrencyValue(ss, tc.name)
			if !ok || got != tc.value {
				t.Fatalf("gmCurrencyValue(%q) = (%d, %v), want (%d, true)", tc.name, got, ok, tc.value)
			}
		})
	}
}

// 货币调整的入参单位：GM 后台“金币”填的是金币，余额存的是铜（10000 铜 = 1 金）。
// 其余货币没有辅币单位，必须原样透传，否则元宝/代金券会被放大 10000 倍。
func TestGMCurrencyDeltaToBaseUnits(t *testing.T) {
	tests := []struct {
		name    string
		delta   int64
		want    int64
		wantOK  bool
		balance string
	}{
		{name: "coin", balance: "coin", delta: 1000, want: 10000000, wantOK: true},
		{name: "coin_alias_case", balance: "Coin", delta: -5, want: -50000, wantOK: true},
		{name: "coin_overflow", balance: "coin", delta: math.MaxInt64/10000 + 1, wantOK: false},
		{name: "coin_underflow", balance: "coin", delta: math.MinInt64/10000 - 1, wantOK: false},
		{name: "yuanbao_passthrough", balance: "yuanBao", delta: 1000, want: 1000, wantOK: true},
		{name: "voucher_passthrough", balance: "voucher", delta: 7, want: 7, wantOK: true},
		{name: "starcoin_passthrough", balance: "starCoin", delta: 7, want: 7, wantOK: true},
		{name: "honor_passthrough", balance: "honor", delta: -9, want: -9, wantOK: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := gmCurrencyDeltaToBaseUnits(tc.balance, tc.delta)
			if ok != tc.wantOK {
				t.Fatalf("gmCurrencyDeltaToBaseUnits(%q, %d) ok = %v, want %v", tc.balance, tc.delta, ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if got != tc.want {
				t.Fatalf("gmCurrencyDeltaToBaseUnits(%q, %d) = %d, want %d", tc.balance, tc.delta, got, tc.want)
			}
			// 回显必须与入参同口径：填多少金币，回显就该是多少金币。
			if back := gmCurrencyDisplayValue(tc.balance, got); back != tc.delta && normalizeGMCurrency(tc.balance) == "coin" {
				t.Fatalf("coin round trip: delta %d -> %d base -> %d display", tc.delta, got, back)
			}
		})
	}
}

// 铜币余额不是整金时，回显取下整，但余额本身必须逐铜保留。
func TestGMCurrencyDisplayValueKeepsCoinRemainder(t *testing.T) {
	const balance = int64(11106365357) // 1110636 金 53 银 57 铜
	if got := gmCurrencyDisplayValue("coin", balance); got != 1110636 {
		t.Fatalf("gmCurrencyDisplayValue(coin, %d) = %d, want 1110636", balance, got)
	}
	if got := gmCurrencyDisplayValue("yuanBao", balance); got != balance {
		t.Fatalf("non-coin currency must pass through, got %d", got)
	}
	delta, ok := gmCurrencyDeltaToBaseUnits("coin", 1000)
	if !ok {
		t.Fatal("1000 金币 should convert")
	}
	if got := balance + delta; got != 11116365357 {
		t.Fatalf("applying 1000 金币 gave %d, want 11116365357", got)
	}
}

func TestGMSetStarCoinBalanceUsesHiddenCurrencyStack(t *testing.T) {
	ss := &session{bag: map[int32]*bagItem{
		0: {ItemId: consignmentCurrencyItemID, ItemType: int32(protocol.ItemType_GoodsItem), Count: 10},
		1: {ItemId: 110201, ItemType: int32(protocol.ItemType_GoodsItem), Count: 3},
	}}
	if err := gmSetCurrency(ss, "starCoin", 42); err != nil {
		t.Fatal(err)
	}
	if got := starCoinBalance(ss); got != 42 {
		t.Fatalf("star coin balance = %d, want 42", got)
	}
	if ss.bag[0] != nil || ss.bag[starCoinBagSlot] == nil || ss.bag[1] == nil {
		t.Fatalf("star coin stacks were not canonicalized: %+v", ss.bag)
	}
	if err := gmSetCurrency(ss, "starCoin", 0); err != nil || starCoinBalance(ss) != 0 {
		t.Fatalf("clear star coins: err=%v balance=%d", err, starCoinBalance(ss))
	}
	if err := gmSetCurrency(ss, "starCoin", int64(math.MaxInt32)+1); err == nil {
		t.Fatal("oversized star coin balance was accepted")
	}
}

func TestGMSetCurrencyAliases(t *testing.T) {
	ss := &session{}
	for _, tc := range []struct {
		name string
		want func() int64
	}{
		{"pvpCurrency", func() int64 { return int64(ss.pvpCurrency) }},
		{"familyContribution", func() int64 { return int64(ss.familyContribute) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := gmSetCurrency(ss, tc.name, 42); err != nil {
				t.Fatalf("gmSetCurrency(%q): %v", tc.name, err)
			}
			if got := tc.want(); got != 42 {
				t.Fatalf("gmSetCurrency(%q) set %d, want 42", tc.name, got)
			}
		})
	}
}
