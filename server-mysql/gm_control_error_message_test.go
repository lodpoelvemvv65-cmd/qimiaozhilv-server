package main

import (
	"fmt"
	"strings"
	"testing"
)

// GM 后台的报错必须写出**具体上限/下限**，运营才知道该改成多少：
// 「宠物等级不能超过 100」而不是旧的「宠物等级必须在 1 到 100 之间」/「宠物等级无效」。
// 这些用例锁住文案格式，避免以后被改回模糊说法。
func TestParseGMOptionalInt32ReportsConcreteBound(t *testing.T) {
	raw := func(value string) *string { return &value }
	over := "99999999999"               // 超过 int32，但仍能按 64 位解析
	notANumber := "9223372036854775808" // 超过 int64，只能报「必须是数字」
	for _, tc := range []struct {
		name  string
		input *string
		want  string
	}{
		{"默认值", nil, ""},
		{"空字符串", raw("  "), "宠物等级不能为空"},
		{"超 int64", raw(notANumber), "宠物等级必须是数字"},
		{"非数字", raw("abc"), "宠物等级必须是数字"},
		{"低于下限", raw("0"), "宠物等级不能小于 1"},
		{"高于上限", raw("101"), "宠物等级不能超过 100"},
		{"超 int32 但仍在 int64 内", raw(over), "宠物等级不能超过 100"},
		{"负数", raw("-5"), "宠物等级不能小于 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, set, err := parseGMOptionalInt32(tc.input, "宠物等级", 1, 100)
			if tc.want == "" {
				if err != nil || set {
					t.Fatalf("err=%v set=%v, want no-op", err, set)
				}
				return
			}
			if err == nil {
				t.Fatal("want an error carrying the concrete bound")
			}
			if err.Error() != tc.want {
				t.Fatalf("message = %q, want %q", err.Error(), tc.want)
			}
			if !set == (tc.name == "默认值") {
				t.Fatalf("set=%v on a rejected value", set)
			}
		})
	}
}

func TestParseGMOptionalInt32AcceptsBoundaries(t *testing.T) {
	low, high := "1", "100"
	if got, set, err := parseGMOptionalInt32(&low, "宠物等级", 1, 100); err != nil || !set || got != 1 {
		t.Fatalf("min: got=%d set=%v err=%v", got, set, err)
	}
	if got, set, err := parseGMOptionalInt32(&high, "宠物等级", 1, 100); err != nil || !set || got != 100 {
		t.Fatalf("max: got=%d set=%v err=%v", got, set, err)
	}
}

func TestParseGMOptionalInt64ReportsConcreteBound(t *testing.T) {
	over := "99999999999999999999"
	_, _, err := parseGMOptionalInt64(&over, "装备实例", 1, 1000)
	if err == nil || err.Error() != "装备实例必须是数字" {
		t.Fatalf("err=%v", err)
	}
	tooBig := "5000"
	_, _, err = parseGMOptionalInt64(&tooBig, "装备实例", 1, 1000)
	if err == nil || err.Error() != "装备实例不能超过 1000" {
		t.Fatalf("err=%v", err)
	}
}

// 端到端走一遍真实 action：报错必须点名具体数值，不能只给状态码或「超出范围」。
func TestGMPetLevelErrorNamesTheCap(t *testing.T) {
	level := "9999"
	err := applyGMPetAdjust(&petState{}, gmPetPayload{Level: &level})
	if err == nil {
		t.Fatal("out-of-range pet level accepted")
	}
	want := fmt.Sprintf("宠物等级不能超过 %d", gmPetMaxLevel(0))
	if err.Error() != want {
		t.Fatalf("message = %q, want %q", err.Error(), want)
	}
	if !strings.Contains(err.Error(), "100") {
		t.Fatalf("message %q must spell out the numeric cap", err.Error())
	}
}

func TestGMEquipAdjustStarAndStrengthErrorsNameTheCap(t *testing.T) {
	ss := &session{bag: map[int32]*bagItem{}, worn: map[int32]*bagItem{}}
	ss.worn[2] = &bagItem{ItemId: 120590, ItemType: 1, ServerId: 88, Level: 1, Star: 1}
	id := fmt.Sprint(88)

	star := fmt.Sprint(gmEquipStarMax + 1)
	_, _, err := applyGMEquipAdjust(ss, gmEquipPayload{ServerID: &id, Star: &star})
	if err == nil || err.Error() != fmt.Sprintf("星级不能超过 %d", gmEquipStarMax) {
		t.Fatalf("err=%v", err)
	}

	strength := fmt.Sprint(gmEquipStrengthMax + 1)
	_, _, err = applyGMEquipAdjust(ss, gmEquipPayload{ServerID: &id, Strength: &strength})
	if err == nil || err.Error() != fmt.Sprintf("强化等级不能超过 %d", gmEquipStrengthMax) {
		t.Fatalf("err=%v", err)
	}

	// 线上上限本身必须放行：星级 20 / 强化 20。
	max := fmt.Sprint(gmEquipStarMax)
	if item, _, err := applyGMEquipAdjust(ss, gmEquipPayload{ServerID: &id, Star: &max}); err != nil {
		t.Fatalf("star=%d rejected: %v", gmEquipStarMax, err)
	} else if item == nil {
		t.Fatal("nil result")
	}
}

// 旧文案「X必须在 A 到 B 之间」在同一个数值上会被两端都说一遍，运营得自己判断
// 是超上限还是低于下限；新文案只报越界的那一侧。
func TestGMNumericErrorsDoNotUseTheOldRangeWording(t *testing.T) {
	for _, field := range []struct {
		raw *string
		min int64
		max int64
	}{
		{func() *string { v := "0"; return &v }(), 1, 20},
		{func() *string { v := "21"; return &v }(), 1, 20},
	} {
		_, _, err := parseGMOptionalInt32(field.raw, "强化等级", field.min, field.max)
		if err == nil {
			t.Fatal("want an error")
		}
		if strings.Contains(err.Error(), "之间") {
			t.Fatalf("message %q still uses the ambiguous range wording", err.Error())
		}
	}
}
