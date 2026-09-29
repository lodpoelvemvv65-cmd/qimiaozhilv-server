package equipattributes

import "testing"

// Names 与 Fields 共用同一个 AttributeType 键空间，必须一一对应：
// Fields 是 EquipBase → 属性 key 的反查表，少一个 key 就会让某条装备属性
// 在 GM 后台显示成「属性N」，多一个 key 则说明表抄错了。
func TestNamesCoverExactlyTheFieldKeySpace(t *testing.T) {
	byKey := map[int32]string{}
	for field, key := range Fields {
		if existing, ok := byKey[key]; ok {
			t.Fatalf("Fields has two names for key %d: %q and %q", key, existing, field)
		}
		byKey[key] = field
	}
	for key := range byKey {
		if _, ok := Names[key]; !ok {
			t.Fatalf("Names is missing AttributeType %d (%s)", key, byKey[key])
		}
	}
	for key := range Names {
		if key == 0 {
			continue // 0=无，不是 EquipBase 字段
		}
		if _, ok := byKey[key]; !ok {
			t.Fatalf("Names has AttributeType %d (%s) but Fields has no matching field", key, Names[key])
		}
	}
	if len(Names) != len(byKey)+1 {
		t.Fatalf("Names has %d entries, want %d (Fields keys + 0=无)", len(Names), len(byKey)+1)
	}
}

func TestAttributeNameIsAlwaysNonEmpty(t *testing.T) {
	for _, tc := range []struct {
		key  int32
		want string
	}{
		{0, "无"},
		{3, "力量"},
		{6, "智慧"},
		{19, "辅助值"},
		{20, "体质"},
		{21, "耐力"},
		{31, "精神增伤"},
		{99, "属性99"}, // 未登记的 key 不能留空白
	} {
		if got := AttributeName(tc.key); got != tc.want {
			t.Fatalf("AttributeName(%d) = %q, want %q", tc.key, got, tc.want)
		}
	}
}

func TestNamesByKeyUsesStringKeys(t *testing.T) {
	byKey := NamesByKey()
	if len(byKey) != len(Names) {
		t.Fatalf("NamesByKey has %d entries, want %d", len(byKey), len(Names))
	}
	if byKey["3"] != "力量" || byKey["0"] != "无" {
		t.Fatalf("NamesByKey = %v", byKey)
	}
}
