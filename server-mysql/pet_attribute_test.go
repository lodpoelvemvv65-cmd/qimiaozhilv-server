package main

import (
	"math"
	"testing"
)

func TestOnlineScreenshotPetAttributes(t *testing.T) {
	loadOnlineTablesForTest(t)
	tests := []struct {
		name  string
		petID int32
		level int32
		want  map[int32]float64
	}{
		{
			name: "Pet04 level 200", petID: 2104, level: 200,
			want: map[int32]float64{
				1005: 5500, 1006: 5500, 1007: 5500, 1034: 5500, 1035: 5500,
				1018: 0.36, 1013: 0.40, 1015: 2.00,
			},
		},
		{
			name: "Pet04 level 102", petID: 2104, level: 102,
			want: map[int32]float64{
				1005: 3540, 1006: 3540, 1007: 3540, 1034: 3540, 1035: 3540,
				1018: 0.1836, 1013: 0.204, 1015: 1.02,
			},
		},
		{
			name: "Pet05 level 200", petID: 2105, level: 200,
			want: map[int32]float64{
				1006: 5500, 1007: 5500, 1008: 5500, 1034: 5500, 1035: 5500,
				1020: 0.36, 1014: 0.40, 1016: 2.00,
			},
		},
		{
			name: "Pet01 level 72", petID: 2101, level: 72,
			want: map[int32]float64{1007: 864, 1034: 864, 1035: 864},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ss := &session{pet: &petState{PetId: test.petID, Level: test.level}}
			got := petAttributeBonus(ss)
			if len(got) != len(test.want) {
				t.Fatalf("attribute count = %d, want %d: %v", len(got), len(test.want), got)
			}
			for numeric, want := range test.want {
				if math.Abs(got[numeric]-want) > 0.000001 {
					t.Errorf("NumericType %d = %v, want %v", numeric, got[numeric], want)
				}
			}
		})
	}
}

func TestPetAttributesParticipateInPlayerDirectBonus(t *testing.T) {
	loadOnlineTablesForTest(t)
	ss := &session{
		pet:        &petState{PetId: 2104, Level: 200},
		transBonus: map[int32]float32{1005: 25},
	}
	if got := directNumericBonus(ss, 1005); got != 5525 {
		t.Fatalf("strength direct bonus = %v, want 5525", got)
	}
}
