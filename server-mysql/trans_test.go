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

func TestStageLevelUsesClientTransmigrationOffsets(t *testing.T) {
	for _, test := range []struct{ total, trans, want int32 }{
		{8500, 0, 8500},
		{10007, 1, 4007},
		{17007, 2, 4007},
		{13000, 2, 0},
	} {
		if got := stageLevel(test.total, test.trans); got != test.want {
			t.Fatalf("stageLevel(%d, %d) = %d, want %d", test.total, test.trans, got, test.want)
		}
	}
}

func TestLoadDataMigratesLegacyLocalTransLevel(t *testing.T) {
	oldTables := tables
	tables = nil
	t.Cleanup(func() { tables = oldTables })

	playerWithSkills := func(level int32) *Player {
		return &Player{Level: level, Trans: 2, Relations: playerRelations{
			skills: map[int32]int32{100001: 1}, skillOrder: []int32{100001},
		}}
	}

	ss := newSession()
	ss.jobID = 1
	if migrated := ss.loadData(playerWithSkills(4000)); !migrated {
		t.Fatal("legacy trans level was not migrated")
	}
	if ss.level != 17000 {
		t.Fatalf("migrated total level = %d, want 17000 (2-4000)", ss.level)
	}

	ss = newSession()
	ss.jobID = 1
	if migrated := ss.loadData(playerWithSkills(17000)); migrated {
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

func TestTransLevelTwoTargetsSecondTierInsteadOfAddingTwo(t *testing.T) {
	ss := newSession()
	ss.playerID = 8
	ss.trans = 1
	ss.level = 13000
	ss.charPoint = 77
	resp := (&Server{}).onTransLevel(
		&channel{session: ss},
		&protocol.C2M_TransLevel{RpcId: 10, Level: 2},
	).(*protocol.M2C_TransLevel)
	if resp.Error != 0 || resp.Message != "" {
		t.Fatalf("response = %+v, want success", resp)
	}
	if ss.trans != 2 {
		t.Fatalf("trans=%d, want target tier 2", ss.trans)
	}
	if ss.charPoint != 0 {
		t.Fatalf("unspent character points = %d after transmigration, want 0", ss.charPoint)
	}
}

func TestTransLevelDoesNotRepeatCurrentTarget(t *testing.T) {
	ss := newSession()
	ss.playerID = 9
	ss.trans = 1
	ss.level = 13000
	resp := (&Server{}).onTransLevel(
		&channel{session: ss},
		&protocol.C2M_TransLevel{RpcId: 11, Level: 1},
	).(*protocol.M2C_TransLevel)
	if resp.Error != 0 || resp.Message == "" || ss.trans != 1 {
		t.Fatalf("response=%+v trans=%d, want rejected repeat", resp, ss.trans)
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
				map[string]interface{}{"Key": json.Number("21"), "Value": json.Number("1400")},
				map[string]interface{}{"Key": json.Number("22"), "Value": json.Number("0.08")},
				map[string]interface{}{"Key": json.Number("23"), "Value": json.Number("0.06")},
				map[string]interface{}{"Key": json.Number("30"), "Value": json.Number("0.12")},
				map[string]interface{}{"Key": json.Number("31"), "Value": json.Number("0.09")},
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
	if ss.transBonus[1034] != 2000 || ss.transBonus[1035] != 1400 {
		t.Fatalf("constitution/stamina bonuses = %v/%v, want 2000/1400", ss.transBonus[1034], ss.transBonus[1035])
	}
	for numeric, want := range map[int32]float32{1022: 0.08, 1023: 0.06, 1045: 0.12, 1046: 0.09} {
		if math.Abs(float64(ss.transBonus[numeric]-want)) > 0.0001 {
			t.Errorf("NumericType %d bonus = %v, want %v", numeric, ss.transBonus[numeric], want)
		}
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
