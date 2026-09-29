package main

import "testing"

func TestVerifiedSwapPreservesValuesAndRejectsChangedEquipment(t *testing.T) {
	before := map[int32]float64{20: 0.1814524084329605, 21: 0.14883722364902496, 7: 0.15}
	after, err := swapVerified(before, before[20], before[21])
	if err != nil || after[20] != before[21] || after[21] != before[20] || after[7] != before[7] {
		t.Fatalf("after=%v err=%v", after, err)
	}
	if _, err := swapVerified(after, before[20], before[21]); err != nil {
		t.Fatal(err)
	}
	if _, err := swapVerified(map[int32]float64{20: 0, 21: 0}, before[20], before[21]); err == nil {
		t.Fatal("accepted changed equipment")
	}
}
