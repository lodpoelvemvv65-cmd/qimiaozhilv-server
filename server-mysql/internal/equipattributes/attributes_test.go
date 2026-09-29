package equipattributes

import (
	"reflect"
	"testing"
)

func TestNormalizeCrossProfessionEquipment(t *testing.T) {
	original := map[int32]float64{7: 0.15, 11: 0.05, 13: 0.24, 19: 0.2, 20: 0.18, 21: 0.14}
	result := Normalize(original, Template{Base: map[int32]float64{8: 82000, 12: 0.35, 14: 0.25, 19: 3.8, 5: 500, 6: 300}})
	want := map[int32]float64{8: 0.15, 12: 0.05, 14: 0.24, 19: 0.2, 5: 0, 6: 0}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result=%v want=%v", result, want)
	}
	if original[7] != 0.15 || len(original) != 6 {
		t.Fatal("source mutated")
	}
	if again := Normalize(result, Template{Base: map[int32]float64{8: 82000, 12: 0.35, 14: 0.25, 19: 3.8, 5: 500, 6: 300}}); !reflect.DeepEqual(again, result) {
		t.Fatal("normalization not idempotent")
	}
}

func TestNormalizePreservesExistingTargetAndSharedFields(t *testing.T) {
	result := Normalize(map[int32]float64{7: 0.1, 8: 0, 20: -0.2, 21: 0.2}, Template{Base: map[int32]float64{8: 10, 20: 20, 21: 30}})
	if !reflect.DeepEqual(result, map[int32]float64{8: 0, 20: -0.2, 21: 0.2}) {
		t.Fatalf("result=%v", result)
	}
}
