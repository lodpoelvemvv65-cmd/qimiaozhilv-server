package main

import "testing"

func TestApplyGMStatsAdjustSetsEnergyAndAttrs(t *testing.T) {
	ss := &session{energy: 1, charPoint: 2, strAdd: 3, qukAdd: 4, spiAdd: 5, wimAdd: 6, phyAdd: 7, staAdd: 8}
	energy := "50"
	charPoint := "20"
	str := "11"
	if err := applyGMStatsAdjust(ss, gmStatsPayload{Energy: &energy, CharPoint: &charPoint, Str: &str}); err != nil {
		t.Fatal(err)
	}
	if ss.energy != 50 || ss.charPoint != 20 || ss.strAdd != 11 || ss.qukAdd != 4 {
		t.Fatalf("ss=%+v", ss)
	}
}
