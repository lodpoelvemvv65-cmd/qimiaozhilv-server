package main

import (
	"encoding/json"
	"testing"
)

func TestMonsterSkillSelectionUsesEveryWeightedGroupSlot(t *testing.T) {
	oldTables := tables
	t.Cleanup(func() { tables = oldTables })
	tables = &datatables{
		monsterBase: map[int64]map[string]interface{}{
			81001: {"SkillGroupId": json.Number("10006")},
		},
		skillGroup: map[int64]map[string]interface{}{
			10006: {"SkillsArr": []interface{}{
				map[string]interface{}{"Skills_Id": json.Number("500002")},
				map[string]interface{}{"Skills_Id": json.Number("500002")},
				map[string]interface{}{"Skills_Id": json.Number("500002")},
				map[string]interface{}{"Skills_Id": json.Number("500002")},
				map[string]interface{}{"Skills_Id": json.Number("500030")},
			}},
		},
	}

	if got := selectMonsterSkillID(81001, func(size int) int {
		if size != 5 {
			t.Fatalf("weighted skill slot count = %d, want 5", size)
		}
		return 0
	}); got != 500002 {
		t.Fatalf("first weighted skill = %d, want 500002", got)
	}
	if got := selectMonsterSkillID(81001, func(int) int { return 4 }); got != 500030 {
		t.Fatalf("last weighted skill = %d, want 500030", got)
	}
	if got := selectMonsterSkillID(99999, func(int) int { return 0 }); got != 500001 {
		t.Fatalf("missing monster fallback skill = %d, want 500001", got)
	}
}

// 怪物技能的**真实**运行时路径是 selectMonsterSkillID() 从 SkillGroupBase 取 id、
// 再 skillLogicCatalog.Plan(id, 1) 构计划（battle_monster_attack.go）。
// 这条断言保证所有技能组里引用到的怪物技能都能建出计划 —— 一旦某个组引用了
// 配置里不存在的技能，Plan 会返回错误，而调用方是 `if err != nil { continue }`，
// 表现为"该怪物这一轮白站"，不会有任何报错。
//
// 这条测试取代了已删除的 TestAllKnownMonsterSkillsHaveAuditedSpecs：那条断言的是
// 一张零调用方的近似表 monsterSkillSpecs，与运行路径无关。
func TestEveryMonsterSkillReferencedByAGroupBuildsAPlan(t *testing.T) {
	loadOnlineTablesForTest(t)
	catalog := loadTestSkillLogic(t)
	if tables == nil || len(tables.skillGroup) == 0 {
		t.Fatal("SkillGroupBase was not loaded")
	}

	seen := map[int32]bool{}
	for groupID, group := range tables.skillGroup {
		for _, raw := range arrOf(group["SkillsArr"]) {
			entry, _ := raw.(map[string]interface{})
			if entry == nil {
				continue
			}
			skillID := int32(num(entry["Skills_Id"]))
			if skillID <= 0 || seen[skillID] {
				continue
			}
			seen[skillID] = true
			if _, err := catalog.Plan(skillID, 1); err != nil {
				t.Errorf("group %d references monster skill %d, which cannot build a plan: %v", groupID, skillID, err)
			}
		}
	}
	if len(seen) < 30 {
		t.Fatalf("only %d monster skills are referenced by any group; the walk looks broken", len(seen))
	}
}
