package main

import "testing"

func TestAttributeTypeMappingsMatchClientEnums(t *testing.T) {
	wantNumeric := map[int32]int32{
		1: 1002, 2: 1004, 3: 1005, 4: 1006, 5: 1007, 6: 1008,
		7: 1009, 8: 1010, 9: 1011, 10: 1012,
		11: 1013, 12: 1014, 13: 1015, 14: 1016,
		15: 1018, 16: 1020, 17: 1019, 18: 1021, 19: 1017,
		20: 1034, 21: 1035, 22: 1022, 23: 1023, 24: 1031, 25: 1032, 26: 1033,
		27: 1042, 28: 1043, 29: 1044, 30: 1045, 31: 1046,
	}
	if len(gemKeyToNumeric) != len(wantNumeric) {
		t.Fatalf("AttributeType mapping count = %d, want %d", len(gemKeyToNumeric), len(wantNumeric))
	}
	for attributeType, want := range wantNumeric {
		if got := gemKeyToNumeric[attributeType]; got != want {
			t.Errorf("AttributeType %d maps to NumericType %d, want %d", attributeType, got, want)
		}
	}

	wantFields := map[string]int32{
		"Hp": 1, "Mp": 2, "Str": 3, "Quk": 4, "Spi": 5, "Wim": 6,
		"PhyAtk": 7, "SpiAtk": 8, "PhyDef": 9, "SpiDef": 10,
		"Pcrir": 11, "Mcrir": 12, "Pcri": 13, "Mcri": 14,
		"Rpcrir": 15, "Rmcrir": 16, "Rpcri": 17, "Rmcri": 18,
		"Dvo": 19, "Phy": 20, "Sta": 21, "Nphyi": 22, "Nmeni": 23,
		"Spd": 24, "Hit": 25, "Res": 26,
		"SuckR": 27, "SuckV": 28, "HpRecover": 29, "PhyDA": 30, "MicDA": 31,
	}
	if len(equipFieldToAttributeType) != len(wantFields) {
		t.Fatalf("equipment field mapping count = %d, want %d", len(equipFieldToAttributeType), len(wantFields))
	}
	for _, mapping := range equipFieldToAttributeType {
		want, ok := wantFields[mapping.field]
		if !ok {
			t.Errorf("unexpected equipment field %q", mapping.field)
			continue
		}
		if mapping.key != want {
			t.Errorf("equipment field %s maps to AttributeType %d, want %d", mapping.field, mapping.key, want)
		}
		delete(wantFields, mapping.field)
	}
	for field := range wantFields {
		t.Errorf("equipment field %s has no AttributeType mapping", field)
	}
}
