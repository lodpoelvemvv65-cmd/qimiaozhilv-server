package skillcatalog

import "testing"

func TestClassifyUsesOnlineJobFamilyAndChineseName(t *testing.T) {
	skill := classify(110101, " 爆破飞刀 ")
	if skill.ID != 110101 || skill.Name != "爆破飞刀" || skill.JobType != 1 || skill.JobName != "军官" {
		t.Fatalf("officer skill = %+v", skill)
	}
	skill = classify(200001, "")
	if skill.Name != "未命名技能" || skill.JobType != 2 || skill.JobName != "运动员" {
		t.Fatalf("sportsman skill = %+v", skill)
	}
}

func TestDisplayNamePrefersCatalogChinese(t *testing.T) {
	names := NamesByID([]Skill{{ID: 110101, Name: "爆破飞刀"}})
	if got := DisplayName(110101, names); got != "爆破飞刀" {
		t.Fatalf("known name = %q", got)
	}
	if got := DisplayName(999, names); got != "未命名技能" {
		t.Fatalf("unknown name = %q", got)
	}
}
