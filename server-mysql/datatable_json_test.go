package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// loadKVTable reads the online unpacked client tables for database-independent
// regression fixtures. Production startup loads the same data only from MySQL.
func loadKVTable(path string) (map[int64]map[string]interface{}, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var arr []json.RawMessage
	if err := dec.Decode(&arr); err != nil {
		return nil, err
	}
	out := make(map[int64]map[string]interface{}, len(arr))
	for _, item := range arr {
		var pair []json.RawMessage
		if err := json.Unmarshal(item, &pair); err != nil || len(pair) < 2 {
			continue
		}
		var key json.Number
		if err := json.Unmarshal(pair[0], &key); err != nil {
			continue
		}
		id, err := key.Int64()
		if err != nil {
			continue
		}
		valueDecoder := json.NewDecoder(bytes.NewReader(pair[1]))
		valueDecoder.UseNumber()
		var value map[string]interface{}
		if err := valueDecoder.Decode(&value); err != nil {
			continue
		}
		out[id] = value
	}
	return out, nil
}

func loadDatatables(dir string) error {
	t := &datatables{}
	for _, table := range []struct {
		name string
		dst  *map[int64]map[string]interface{}
	}{
		{"MainStory", &t.mainStory}, {"MainStoryExp", &t.mainStoryExp}, {"MonsterBase", &t.monsterBase},
		{"MapMonsterConfig", &t.mapMonsterConfig}, {"SkillConfig", &t.skillConfig},
		{"SkillGroupBase", &t.skillGroup}, {"TaskBase", &t.taskBase}, {"NPCBase", &t.npcBase},
		{"SkillLearn", &t.skillLearn}, {"RoleGrowth", &t.roleGrowth}, {"CharacterGrowth", &t.characterGrowth},
		{"EquipBase", &t.equipBase}, {"Strengthentable", &t.strengthen},
		{"StrengthPlusConfig", &t.strengthPlus}, {"EquipAffixConfig", &t.equipAffix},
		{"ManulEquip", &t.manulEquip}, {"ManulEquipAttribute", &t.manulEquipAttribute},
		{"SuitConfig", &t.suitConfig},
		{"GoodsBase", &t.goodsBase}, {"CopyConfig", &t.copyConfig}, {"ShopBase", &t.shopBase},
		{"MarketBase", &t.marketBase}, {"MultiShop", &t.multiShop}, {"PetConfig", &t.petConfig},
		{"PetLevelConfig", &t.petLevelConfig}, {"PetExploreConfig", &t.petExploreConfig},
		{"SpaceTravelConfig", &t.spaceTravelConfig}, {"ActivePerDayConfig", &t.activePerDay},
		{"StarSoulCopyConfig", &t.starSoulCopy}, {"WorldBossConfig", &t.worldBossConfig},
		{"JourneyOfDeathCopyConfig", &t.journeyOfDeathConfig}, {"TrialCopy", &t.trialCopy},
		{"BossBase", &t.bossBase}, {"ManulEquipMonsterConfig", &t.manulEquipMonsterConfig},
		{"TransmigrationAddConfig", &t.transmigrationAdd}, {"SignInRewardConfig", &t.signInReward},
		{"SignInRewardMonth", &t.signInRewardMonth}, {"SkinBase", &t.skinBase},
		{"SceneTransConfig", &t.sceneTrans}, {"MaterialBase", &t.materialBase},
		{"GemPriceConfig", &t.gemPrice}, {"GemInlayConfig", &t.gemInlay},
		{"FamilyBossConfig", &t.familyBossConfig}, {"Parentset", &t.parentset}, {"SonSet", &t.sonSet},
		{"EffectConfig", &t.effectConfig}, {"EquipForge", &t.equipForge},
		{"StarSoulTypeConfig", &t.starSoulType}, {"StarSoulAttributeConfig", &t.starSoulAttribute},
		{"StarSoulEquipAttributeTypeConfig", &t.starSoulEquipAttr}, {"StarSoulLevelConfig", &t.starSoulLevel},
		{"QuestConfig", &t.questConfig},
	} {
		value, err := loadKVTable(filepath.Join(dir, table.name+".json"))
		if err != nil {
			return fmt.Errorf("load %s: %w", table.name, err)
		}
		*table.dst = value
		log.Printf("datatable %s loaded: %d rows", table.name, len(value))
	}
	tables = t
	return nil
}
