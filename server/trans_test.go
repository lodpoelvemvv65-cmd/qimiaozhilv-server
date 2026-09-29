package main

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"mhqserver/protocol"
)

func TestClientTransLevelBoundaries(t *testing.T) {
	wantStart := []int32{0, 6000, 13000, 21500, 31500}
	for trans, want := range wantStart {
		if got := transStart(int32(trans)); got != want {
			t.Fatalf("transStart(%d) = %d, want %d", trans, got, want)
		}
	}
	for trans, want := range []int32{6000, 13000, 21500, 31500} {
		if got := transLevelCap(int32(trans)); got != want {
			t.Fatalf("transLevelCap(%d) = %d, want %d", trans, got, want)
		}
	}
}

func TestLoadDataMigratesLegacyLocalTransLevel(t *testing.T) {
	oldTables := tables
	tables = nil
	t.Cleanup(func() { tables = oldTables })

	ss := newSession()
	ss.jobID = 1
	if migrated := ss.loadData(&Player{Level: 4000, Trans: 2}); !migrated {
		t.Fatal("legacy trans level was not migrated")
	}
	if ss.level != 17000 {
		t.Fatalf("migrated total level = %d, want 17000 (2-4000)", ss.level)
	}

	ss = newSession()
	ss.jobID = 1
	if migrated := ss.loadData(&Player{Level: 17000, Trans: 2}); migrated {
		t.Fatal("valid total level was migrated twice")
	}
}

func TestTransLevelRejectsInsufficientTotalLevelWithoutRPCError(t *testing.T) {
	ss := newSession()
	ss.playerID = 7
	ss.level = 12999
	resp := (&Server{}).onTransLevel(
		&channel{session: ss},
		&protocol.C2M_TransLevel{RpcId: 9, Level: 2},
	).(*protocol.M2C_TransLevel)
	if resp.Error != 0 || !strings.Contains(resp.Message, "等级不足") {
		t.Fatalf("response = %+v, want Error=0 level message", resp)
	}
	if ss.trans != 0 {
		t.Fatalf("trans changed to %d after rejected request", ss.trans)
	}
}

func TestTransBonusUsesCompleteAttributeTypeMapping(t *testing.T) {
	oldTables := tables
	t.Cleanup(func() { tables = oldTables })
	tables = &datatables{transmigrationAdd: map[int64]map[string]interface{}{
		12: {
			"AddAttributeArr": []interface{}{
				map[string]interface{}{"Key": json.Number("3"), "Value": json.Number("200")},
				map[string]interface{}{"Key": json.Number("19"), "Value": json.Number("0.6")},
				map[string]interface{}{"Key": json.Number("20"), "Value": json.Number("2000")},
			},
		},
	}}

	ss := &session{jobID: 1, trans: 2}
	ss.recalcTransBonus()
	if ss.transBonus[1005] != 200 {
		t.Fatalf("strength bonus = %v, want 200", ss.transBonus[1005])
	}
	if math.Abs(float64(ss.transBonus[1017]-0.6)) > 0.0001 {
		t.Fatalf("dodge bonus = %v, want 0.6", ss.transBonus[1017])
	}
	if ss.transBonus[1035] != 2000 {
		t.Fatalf("stamina bonus = %v, want 2000", ss.transBonus[1035])
	}
	if ss.transBonus[1002] != 0 {
		t.Fatalf("AttributeType 20 was incorrectly mapped to MaxHp: %v", ss.transBonus[1002])
	}
}

func TestHighTransLevelStatsDoNotWrapNegative(t *testing.T) {
	row := map[string]interface{}{
		"Hp1": json.Number("1"), "Hp2": json.Number("1"), "Hp3": json.Number("260"),
	}
	if got := growAt(row, "Hp1", "Hp2", "Hp3", 31500); got != int32(maxInt32Value) {
		t.Fatalf("high-level growth = %d, want saturated %d", got, maxInt32Value)
	}
	if got := statSum(int32(maxInt32Value), 1000); got != int32(maxInt32Value) {
		t.Fatalf("stat sum wrapped to %d", got)
	}
}
