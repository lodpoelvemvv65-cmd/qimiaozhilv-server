package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// ===================== datatable 加载 =====================
// datatable 配置表格式（见 _decompress_datatable.py 解包结果）：
//   顶层 JSON 数组，每项为 [key, {字段...}] 二元组。
//   服务器只需加载战斗相关几张表。

type datatables struct {
	characterGrowth     map[int64]map[string]interface{} // CharacterGrowth.json
	mainStory           map[int64]map[string]interface{} // MainStory.json
	monsterBase         map[int64]map[string]interface{} // MonsterBase.json
	mapMonsterConfig    map[int64]map[string]interface{} // MapMonsterConfig.json（场景展示怪）
	skillConfig         map[int64]map[string]interface{} // SkillConfig.json
	skillGroup          map[int64]map[string]interface{} // SkillGroupBase.json
	taskBase            map[int64]map[string]interface{} // TaskBase.json
	npcBase             map[int64]map[string]interface{} // NPCBase.json
	skillLearn          map[int64]map[string]interface{} // SkillLearn.json
	roleGrowth          map[int64]map[string]interface{} // RoleGrowth.json（升级经验 + 血蓝成长）
	equipBase           map[int64]map[string]interface{} // EquipBase.json（装备属性/穿戴校验）
	strengthen          map[int64]map[string]interface{} // Strengthentable.json（强化等级 1..20）
	strengthPlus        map[int64]map[string]interface{} // StrengthPlusConfig.json（强化辅助材料）
	equipAffix          map[int64]map[string]interface{} // EquipAffixConfig.json（装备词缀）
	manulEquip          map[int64]map[string]interface{} // ManulEquip.json（手工装备配方）
	manulEquipAttribute map[int64]map[string]interface{} // ManulEquipAttribute.json（手工随机属性）
	goodsBase           map[int64]map[string]interface{} // GoodsBase.json（物品）
	copyConfig          map[int64]map[string]interface{} // CopyConfig.json（战斗行动值消耗）

	// 商店 / 市场 / 多商店（13 文档）
	shopBase   map[int64]map[string]interface{} // ShopBase.json（普通商店）
	marketBase map[int64]map[string]interface{} // MarketBase.json（元宝市场）
	multiShop  map[int64]map[string]interface{} // MultiShop.json（多商店）
	// 宠物（17 文档）
	petConfig        map[int64]map[string]interface{} // PetConfig.json
	petLevelConfig   map[int64]map[string]interface{} // PetLevelConfig.json
	petExploreConfig map[int64]map[string]interface{} // PetExploreConfig.json
	// 星空旅行（20393/20394 + 选图 10039+ 场景）
	spaceTravelConfig map[int64]map[string]interface{} // SpaceTravelConfig.json
	// 每日活动（20409）：入口表及三类专用战斗编排。
	activePerDay         map[int64]map[string]interface{} // ActivePerDayConfig.json
	starSoulCopy         map[int64]map[string]interface{} // StarSoulCopyConfig.json
	worldBossConfig      map[int64]map[string]interface{} // WorldBossConfig.json
	journeyOfDeathConfig map[int64]map[string]interface{} // JourneyOfDeathCopyConfig.json
	// 挑战副本（传送门活动列表 10009/10036/10037/10038）
	trialCopy map[int64]map[string]interface{} // TrialCopy.json（MapId → MonsterId）
	// Boss 场景（传送门 Boss 列表 10010 层 1..25）
	bossBase map[int64]map[string]interface{} // BossBase.json（_id=1000+layer → MonsterId）
	// 手动装备场景（活动列表 10033/10034/10035）
	manulEquipMonsterConfig map[int64]map[string]interface{} // ManulEquipMonsterConfig.json
	// 转生加成（19 文档 §4.2：_id = 职业族×10 + 转生等级 11..43）
	transmigrationAdd map[int64]map[string]interface{} // TransmigrationAddConfig.json
	// 签到（20419-20423）
	signInReward      map[int64]map[string]interface{} // SignInRewardConfig.json
	signInRewardMonth map[int64]map[string]interface{} // SignInRewardMonth.json
	// 皮肤（穿皮肤装备 120594+ → 模型变化；SkinBase._id → PrfabId → Sys_Prefab）
	skinBase map[int64]map[string]interface{} // SkinBase.json
	// 场景传送配置（选图/传送点）
	sceneTrans map[int64]map[string]interface{} // SceneTransConfig.json
	// 材料（宝石等；MaterialBase.MaterialType==2=宝石，GemType/GemLevel 字段）
	materialBase map[int64]map[string]interface{} // MaterialBase.json
	// 宝石费用（GemPriceConfig：按宝石等级 1..7 的镶嵌/拆卸费）
	gemPrice map[int64]map[string]interface{} // GemPriceConfig.json
	// 宝石可镶嵌部位（GemInlayConfig：部位 Type 0..11 → 允许的宝石属性类型）
	gemInlay map[int64]map[string]interface{} // GemInlayConfig.json
	// 家族（family.go）：家族 BOSS 配置（FamilyBossConfig _id=1..5）
	familyBossConfig map[int64]map[string]interface{} // FamilyBossConfig.json
	// 家族掉落链（family.go）：Parentset（Dropasubset → SubsetArr）→ SonSet（DropArr）
	parentset map[int64]map[string]interface{} // Parentset.json
	sonSet    map[int64]map[string]interface{} // SonSet.json
	// 技能特效（20077 PlaySkillEffect；EffectConfig._id → EffectType：1=弹道 2/3=通用 4=循环）
	effectConfig map[int64]map[string]interface{} // EffectConfig.json
	// 打造（bagtools.go onForgeEquip：EquipForge._id → 配方）
	equipForge map[int64]map[string]interface{} // EquipForge.json
	// 星魂（starsoul.go）
	starSoulType      map[int64]map[string]interface{} // StarSoulTypeConfig.json
	starSoulAttribute map[int64]map[string]interface{} // StarSoulAttributeConfig.json
	starSoulEquipAttr map[int64]map[string]interface{} // StarSoulEquipAttributeTypeConfig.json
	starSoulLevel     map[int64]map[string]interface{} // StarSoulLevelConfig.json
	questConfig       map[int64]map[string]interface{} // QuestConfig.json（答题）
}

var tables *datatables

// loadKVTable：读取 [key, {...}] 形式的 JSON 表。
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
		k, err := key.Int64()
		if err != nil {
			continue
		}
		dec2 := json.NewDecoder(bytes.NewReader(pair[1]))
		dec2.UseNumber()
		var v map[string]interface{}
		if err := dec2.Decode(&v); err != nil {
			continue
		}
		out[k] = v
	}
	return out, nil
}

// loadDatatables：加载战斗所需配置表（main 启动时调用）。
func loadDatatables(dir string) error {
	t := &datatables{}
	for _, p := range []struct {
		name string
		dst  *map[int64]map[string]interface{}
	}{
		{"MainStory", &t.mainStory},
		{"MonsterBase", &t.monsterBase},
		{"MapMonsterConfig", &t.mapMonsterConfig},
		{"SkillConfig", &t.skillConfig},
		{"SkillGroupBase", &t.skillGroup},
		{"TaskBase", &t.taskBase},
		{"NPCBase", &t.npcBase},
		{"SkillLearn", &t.skillLearn},
		{"RoleGrowth", &t.roleGrowth},
		{"CharacterGrowth", &t.characterGrowth},
		{"EquipBase", &t.equipBase},
		{"Strengthentable", &t.strengthen},
		{"StrengthPlusConfig", &t.strengthPlus},
		{"EquipAffixConfig", &t.equipAffix},
		{"ManulEquip", &t.manulEquip},
		{"ManulEquipAttribute", &t.manulEquipAttribute},
		{"GoodsBase", &t.goodsBase},
		{"CopyConfig", &t.copyConfig},
		{"ShopBase", &t.shopBase},
		{"MarketBase", &t.marketBase},
		{"MultiShop", &t.multiShop},
		{"PetConfig", &t.petConfig},
		{"PetLevelConfig", &t.petLevelConfig},
		{"PetExploreConfig", &t.petExploreConfig},
		{"SpaceTravelConfig", &t.spaceTravelConfig},
		{"ActivePerDayConfig", &t.activePerDay},
		{"StarSoulCopyConfig", &t.starSoulCopy},
		{"WorldBossConfig", &t.worldBossConfig},
		{"JourneyOfDeathCopyConfig", &t.journeyOfDeathConfig},
		{"TrialCopy", &t.trialCopy},
		{"BossBase", &t.bossBase},
		{"ManulEquipMonsterConfig", &t.manulEquipMonsterConfig},
		{"TransmigrationAddConfig", &t.transmigrationAdd},
		{"SignInRewardConfig", &t.signInReward},
		{"SignInRewardMonth", &t.signInRewardMonth},
		{"SkinBase", &t.skinBase},
		{"SceneTransConfig", &t.sceneTrans},
		{"MaterialBase", &t.materialBase},
		{"GemPriceConfig", &t.gemPrice},
		{"GemInlayConfig", &t.gemInlay},
		{"FamilyBossConfig", &t.familyBossConfig},
		{"Parentset", &t.parentset},
		{"SonSet", &t.sonSet},
		{"EffectConfig", &t.effectConfig},
		{"EquipForge", &t.equipForge},
		{"StarSoulTypeConfig", &t.starSoulType},
		{"StarSoulAttributeConfig", &t.starSoulAttribute},
		{"StarSoulEquipAttributeTypeConfig", &t.starSoulEquipAttr},
		{"StarSoulLevelConfig", &t.starSoulLevel},
		{"QuestConfig", &t.questConfig},
	} {
		m, err := loadKVTable(filepath.Join(dir, p.name+".json"))
		if err != nil {
			return fmt.Errorf("load %s: %w", p.name, err)
		}
		*p.dst = m
		log.Printf("datatable %s loaded: %d rows", p.name, len(m))
	}
	tables = t
	return nil
}

// —— JSON 数字辅助（decoder.UseNumber 后为 json.Number）——
func num(v interface{}) int64 {
	switch n := v.(type) {
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i
		}
	case int:
		return int64(n)
	case int32:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	case float32:
		return int64(n)
	}
	return 0
}

func numf(v interface{}) float64 {
	switch n := v.(type) {
	case json.Number:
		if f, err := n.Float64(); err == nil {
			return f
		}
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int32:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}

func arrOf(v interface{}) []interface{} {
	a, _ := v.([]interface{})
	return a
}

// ===================== 技能栏 =====================
// 技能栏槽位由玩家已学技能（session.skillOrder）生成，见 skill.go mainUISlotList。
// 槽 Id = 技能 ID；客户端点槽发 C2M_UseMainUISkill{SlotId=槽序号}。

// ===================== 战斗状态 =====================
type monsterUnit struct {
	id           int64
	monsterID    int32 // MonsterBase._id
	hp           int32
	maxHP        int32
	phyAtk       int32
	spiAtk       int32
	phyDef       int32
	spiDef       int32
	extraNumeric map[int32]float64
	exp          int64
	alive        bool
}

type battleState struct {
	region         int32
	mapID          int32 // 发起战斗时的场景 mapId（Boss 击杀判定用；0=主线）
	battleType     int32 // 客户端 BattleType：1=主线/普通场景，2=试炼
	trialCopyID    int32 // 试炼战斗对应 TrialCopy._id；非试炼为 0
	monsters       []*monsterUnit
	selectedID     int64
	selectedAllyID int64
	playerHP       int32
	playerMaxHP    int32
	playerMP       int32
	playerMaxMP    int32
	phyAtk         int32
	spiAtk         int32
	phyDef         int32
	spiDef         int32
	playerDefeated bool
	ended          bool
	runtime        *CombatRuntime
	effectMeta     map[string]battleEffectMetadata
	owner          *session
	modifierHooks  map[string]*activeSkillModifier
	party          *partyBattle
	pvp            *pvpBattle
	activity       *activityBattle
}

func (battle *battleState) alliedPlayerRefs() []CombatUnitRef {
	if battle == nil || battle.runtime == nil {
		return nil
	}
	if battle.party == nil {
		return []CombatUnitRef{battle.runtime.Player()}
	}
	refs := make([]CombatUnitRef, 0, len(battle.party.memberIDs))
	for _, playerID := range battle.party.memberIDs {
		refs = append(refs, PlayerCombatUnit(playerID))
	}
	return refs
}

func battleTypeForClient(battle *battleState) int32 {
	if battle == nil || battle.battleType <= 0 {
		return 1
	}
	return battle.battleType
}

// battleCopyConfig resolves the authoritative CopyConfig row for every
// battle entry point. The wire BattleVictory/BattleDefeat value is the
// configured BattleType, not a generic "normal" value.
func battleCopyConfig(region, mapID int32, presentation battlePresentation) int64 {
	if presentation.copyID > 0 {
		return presentation.copyID
	}
	if presentation.kind == presentationWorldBoss {
		// The 25 selectable BossBase layers use CopyConfig.BossBattle (10005,
		// NeedBossEnergy=1). WorldBossBattle (10015) is a separate timed mode.
		return 10005
	}
	if presentation.kind == presentationFamilyBoss || mapID < 0 {
		return 10006
	}
	if presentation.kind == presentationManualEquip {
		return 10007
	}
	if _, _, ok := trialCopyForMap(mapID); ok {
		return 10004
	}
	sceneID := mapID / 100
	if sceneID >= 10039 && sceneID <= 10044 {
		return 10013
	}
	if sceneID == 10010 {
		return 10005
	}
	if tables != nil {
		if row := tables.mainStory[int64(region)]; row != nil {
			if int32(num(row["Layer"])) == 10 {
				return 10003
			}
		}
	}
	return 10001
}

func mainStoryRegionForSession(ss *session, requested int32) int32 {
	// MainStory rows are authoritative for every scene, not just the beach.
	// Some client click paths send Region=0 (for example the sky-city maps),
	// while the map itself unambiguously identifies the configured row.
	if ss != nil {
		if region, _, _, _, ok := mainStoryRosterForMap(ss.mapID); ok {
			return region
		}
	}
	// Beach maps are encoded as sceneId*100+layer (1000601..1000610).
	// The client can send a local region number such as 1 after changing layers,
	// so the authoritative stage must come from the current map.
	if ss != nil && ss.mapID/100 == 10006 {
		layer := ss.mapID % 100
		if layer >= 1 && layer <= 10 {
			return 1000 + layer
		}
	}
	if requested > 0 {
		return requested
	}
	return 0
}

func sessionHasBattle(ch *channel) bool {
	if ch == nil || ch.session == nil {
		return false
	}
	ch.session.battleMu.Lock()
	defer ch.session.battleMu.Unlock()
	return ch.session.battle != nil
}

// 玩家战斗属性 = 线上成长公式 + 加点 + 装备加成（growth.go / session.playerXxx()）。

// sendPush：打包并推送给客户端（opcode 为 M2C 推送消息）。
func (s *Server) sendPush(ch *channel, opcode uint16, msg proto.Message) {
	out, err := proto.Marshal(msg)
	if err != nil {
		log.Printf("[S=%d] marshal push opcode=%d err=%v", ch.id, opcode, err)
		return
	}
	s.SendToChannel(ch, packOuter(opcode, out))
	log.Printf("[S=%d] push opcode=%d len=%d", ch.id, opcode, len(out))
}

// ===================== 角色成长 / 升级 =====================
// 线上成长公式（RoleGrowth.json，见 11 文档 §公式）：
//
//	value = X1 + X2*Lv + X3*Lv²/100（float 计算，int 截断）
//
// 最终属性 = 职业成长 + 手动加点（strAdd 等）+ 装备加成（equipBonus）。
// 实现在 growth.go：baseMaxHp/baseMaxMp/baseStr/... 与 session.playerXxx()。
// 升级经验（线上 GetMaxExpByLevel = 0.01*Lv³，放大 10000 倍）：
//
//	expNeed(Lv) = 100*Lv³
func roleRow(jobID int32) map[string]interface{} {
	if tables == nil {
		return nil
	}
	if v, ok := tables.roleGrowth[int64(jobTypeOf(jobID))]; ok {
		return v
	}
	return tables.roleGrowth[1]
}

func numf2(v interface{}) float64 { return numf(v) }

// maxHpAt：职业等级基础 MaxHp（不含加点/装备）。线上公式 Hp1+Hp2*Lv+Hp3*Lv²/100。
func maxHpAt(jobID, level int32, add int32) int32 {
	return baseMaxHp(jobID, level) + add
}

func maxMpAt(jobID, level int32) int32 {
	return baseMaxMp(jobID, level)
}

func attrAt(key string, base float64, jobID, level int32) int32 {
	return int32(base + numf2(roleRow(jobID)[key])*float64(level))
}

func expNeed(level int32) int64 {
	if level < 1 {
		level = 1
	}
	l := int64(level)
	return 100 * l * l * l
}

func copyEnergyCost(configID int64, field string, fallback int32) int32 {
	if tables != nil {
		if row := tables.copyConfig[configID]; row != nil {
			if value, exists := row[field]; exists {
				cost := int32(num(value))
				if cost >= 0 {
					return cost
				}
			}
		}
	}
	return fallback
}

// battleEnergyCost is the single authority for entry cost. CopyConfig uses
// NeedEnergy for normal copies and NeedBossEnergy for BossBattle; both consume
// the player's NumericType 1039 Energy in this client.
func battleEnergyCost(configID int64) int32 {
	if tables == nil {
		return 0
	}
	row := tables.copyConfig[configID]
	if row == nil {
		return 0
	}
	for _, field := range []string{"NeedEnergy", "NeedBossEnergy"} {
		if value, exists := row[field]; exists {
			cost := int32(num(value))
			if cost > 0 {
				return cost
			}
		}
	}
	return 0
}

func mainStoryEnergyCost(region int32) int32 {
	// Every MainStory scene has ten layers. Layer 10 is the online boss copy
	// (CopyConfig 10003, NeedEnergy=1); normal/elite layers cost 3.
	if tables != nil {
		if row := tables.mainStory[int64(region)]; row != nil && int32(num(row["Layer"])) == 10 {
			return copyEnergyCost(10003, "NeedEnergy", 1)
		}
	}
	return copyEnergyCost(10001, "NeedEnergy", 3)
}

func bossEnergyCost() int32 {
	return copyEnergyCost(10005, "NeedBossEnergy", 1)
}

func (s *Server) pushPlayerProgress(ch *channel) {
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		return
	}
	ss := ch.session
	for _, value := range []struct {
		typeID int32
		value  float32
	}{
		{1026, float32(ss.level)},
		// 经验条单位：客户端 CharacterUIHelper.GetMaxExpByLevel = (int)(0.01×Lv³)（万单位，
		// 仅显示用；判定曲线仍为 100·Lv³）。经验条 max = 0.01·Lv³，故 1027 必须推
		// exp/10000（万单位），否则 bar = exp/(0.01·Lv³) = 需求×10000 → 经验条恒满。
		{1027, float32(ss.exp) / 10000},
		{1029, float32(ss.trans)}, // 转生等级（19 文档 §3.4：参与等级文案）
		{1039, float32(ss.energy)},
	} {
		s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: ss.playerID, NumericType: value.typeID, Value: value.value, ActorId: ss.playerID,
		})
	}
}

// pushPlayerAttrs：推送玩家当前数值属性（进游戏 / 升级 / 加点 / 穿脱装备后共用）。
// NumericType：Hp=1001 MaxHp=1002 Mp=1003 MaxMp=1004 Str=1005 Quk=1006 Spi=1007
// Wim=1008 PhyAtk=1009 SpiAtk=1010 PhyDef=1011 SpiDef=1012，另同步
// Level=1026 Exp=1027 Energy=1039。
// 值 = 线上职业成长 + 手动加点 + 装备加成（growth.go）。
func (s *Server) pushPlayerAttrs(ch *channel) {
	ss := ch.session
	mhp := ss.playerMaxHp()
	mp := ss.playerMaxMp()
	s.pushPlayerAttrsTo(ch, ss, mhp, mp)
}

// pushPlayerAttrsTo sends one player's complete NumericComponent snapshot to
// a specific client. UnitCharacter.FillNum only initializes HP/MP/level; the
// character panel, team heads and combat targeting also read attack, defence,
// rates and current resources directly from the scene Unit's NumericComponent.
// This helper is therefore used for inspecting another player and before a
// multi-player fight starts, not only for the local player.
func (s *Server) pushPlayerAttrsTo(ch *channel, ss *session, currentHP, currentMP int32) {
	if ch == nil || ch.session == nil || ss == nil || ss.playerID == 0 {
		return
	}
	pid := ss.playerID
	mhp := ss.playerMaxHp()
	mp := ss.playerMaxMp()
	if currentHP < 0 || currentHP > mhp {
		currentHP = mhp
	}
	if currentMP < 0 || currentMP > mp {
		currentMP = mp
	}
	push := func(t int32, v float32) {
		s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: pid, NumericType: t, Value: v, ActorId: ch.session.playerID,
		})
	}
	push(1002, float32(mhp))               // MaxHp
	push(1001, float32(currentHP))         // Hp
	push(1004, float32(mp))                // MaxMp
	push(1003, float32(currentMP))         // Mp
	push(1005, float32(ss.playerStr()))    // Str
	push(1006, float32(ss.playerQuk()))    // Quk
	push(1007, float32(ss.playerSpi()))    // Spi
	push(1008, float32(ss.playerWim()))    // Wim
	push(1009, float32(ss.playerPhyAtk())) // PhyAtk
	push(1010, float32(ss.playerSpiAtk())) // SpiAtk
	push(1011, float32(ss.playerPhyDef())) // PhyDef
	push(1012, float32(ss.playerSpiDef())) // SpiDef
	// SkinId(1036)：客户端 NumericWatcher_SkinId（HotfixView 497）→
	// UnitViewSystem.ChangeSkin(SkinBase[value].PrfabId) → 世界内模型立即重建。
	// 穿/脱皮肤装备必须推（否则世界模型不变，只有重开面板才更新）。
	skin := ss.skinID
	if skin <= 0 || (skin > 4 && !isSkinIDValid(skin)) {
		skin = ss.jobID
	}
	push(1036, float32(skin))
	// 面板完整属性（CharacterUI.ShowNumText 读取）：暴击/闪避/穿透/减伤/Dvo 等
	// （NumericType 1013-1023、1031-1035、1042-1046）。equipBonus 已算出 →
	// 非零项逐条推送（否则面板这些行恒 0，穿戴带暴击/闪避的装备不显示）。
	// Push zeroes as well so taking equipment off clears a previously non-zero
	// rate in NumericComponent instead of leaving stale panel/combat values.
	for _, t := range []int32{
		1013, 1014, 1015, 1016, 1017, 1018, 1019, 1020, 1021, 1022, 1023,
		1031, 1032, 1033, 1034, 1035, 1042, 1043, 1044, 1045, 1046,
	} {
		push(t, float32(ss.playerExtraNumeric(t)))
	}
	push(1026, float32(ss.level))
	push(1027, float32(ss.exp)/10000)
	push(1029, float32(ss.trans))
	push(1039, float32(ss.energy))
}

// pushUnitCharacter：重推 M2C_SendUnitInfo（升级/加点/穿脱装备后 Level、属性点、
// 角色名同步；血蓝由 pushPlayerAttrs 的 20169 推送）。
func (s *Server) pushUnitCharacter(ch *channel) {
	ss := ch.session
	su := &protocol.M2C_SendUnitInfo{
		UnitCharacter: buildUnitCharacter(ss),
		ActorId:       ss.playerID,
	}
	s.sendPush(ch, protocol.OpM2C_SendUnitInfo, su)
}

// 等级上限（客户端无硬编码上限：RoleGrowth 公式无上限、NumericWatcher_Level 无上限判断；
// 服务器原保守 200，按用户要求提到 4000。int32 属性安全：4000²/100×系数≈4e7）
const maxPlayerLevel = 31500

// gainExp：获得经验 → 升级循环 → 升级推送（全属性 + 属性点 + 角色信息）。
// 经验卡 buff（expMult>1 且未到期）按倍率放大经验。
func experienceAfterMultiplier(ss *session, exp int64) int64 {
	if ss == nil || exp <= 0 {
		return exp
	}
	now := time.Now().UnixMilli()
	category := itemBuffBattleExp
	if ss.idleBattle {
		category = itemBuffIdleExp
	}
	ss.itemBuffMu.Lock()
	defer ss.itemBuffMu.Unlock()
	if buff := ss.itemBuffs[category]; buff != nil && buff.ExpiresAt > now && buff.Multiplier > 1 {
		return int64(float64(exp) * buff.Multiplier)
	}
	// Compatibility for focused tests and sessions created before itemBuffs is loaded.
	if !ss.idleBattle && ss.expMult > 1 && ss.expMultUntil > now {
		return int64(float64(exp) * ss.expMult)
	}
	return exp
}

func (s *Server) gainExp(ch *channel, exp int64) {
	ss := ch.session
	if ss == nil || exp <= 0 {
		return
	}
	// 经验卡倍率（4倍经验卡 ExpRange=4 × ContinuedSeconds；生效期内 ×4）
	exp = experienceAfterMultiplier(ss, exp)
	ss.exp += exp
	upgraded := false
	levelCap := transLevelCap(ss.trans)
	if levelCap > maxPlayerLevel {
		levelCap = maxPlayerLevel
	}
	for ss.exp >= expNeed(ss.level) && ss.level < levelCap {
		ss.exp -= expNeed(ss.level)
		ss.level++
		ss.charPoint++  // 每级 1 属性点
		ss.skillPoint++ // 每级 1 技能点（学习技能消耗）
		upgraded = true
	}
	if upgraded {
		s.pushPlayerAttrs(ch)
		s.pushUnitCharacter(ch)
		log.Printf("[S=%d] level up -> %d exp=%d charPoint=%d skillPoint=%d", ch.id, ss.level, ss.exp, ss.charPoint, ss.skillPoint)
	} else {
		s.pushPlayerProgress(ch)
	}
	s.saveData(ch)
	log.Printf("[S=%d] gain exp=%d total=%d level=%d", ch.id, exp, ss.exp, ss.level)
}

// onAddPoint：20253 → 20254。属性点分配。
// 客户端（CharacterUI b__16_0，HotfixView 1060 反汇编）发送 C2M_AddPoint.PointList
// （repeated int32，按行序 = Str/Quk/Spi/Wim），**不设 Trans**；响应必须带 Character
// （客户端 ClientUnitCharacterComponent.Update(msg.Character) 首步 get_Id()，nil 必 NRE）。
// ⚠ 客户端健壮性（Player.log 实证）：Session.Call 对 Error!=0 响应直接抛 RpcException →
// 会话销毁 → 掉线。因此【任何校验失败必须返回成功（Error=0）+ 当前 Character】，
// Message 仅作提示（客户端 IsNullOrEmpty(Message) 判空显示，不崩）。
func (s *Server) onAddPoint(ch *channel, req *protocol.C2M_AddPoint) proto.Message {
	resp := &protocol.M2C_AddPoint{RpcId: req.RpcId}
	ss := ch.session
	if ss.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	if len(req.PointList) > 0 {
		var sum int32
		for _, n := range req.PointList {
			if n < 0 {
				resp.Message = "点数无效"
				resp.Character = buildUnitCharacter(ss)
				return resp
			}
			sum += n
		}
		if sum <= 0 || sum > ss.charPoint {
			resp.Message = "没有可用属性点"
			resp.Character = buildUnitCharacter(ss)
			return resp
		}
		if len(req.PointList) >= 1 {
			ss.strAdd += req.PointList[0]
		}
		if len(req.PointList) >= 2 {
			ss.qukAdd += req.PointList[1]
		}
		if len(req.PointList) >= 3 {
			ss.spiAdd += req.PointList[2]
		}
		if len(req.PointList) >= 4 {
			ss.wimAdd += req.PointList[3]
		}
		ss.charPoint -= sum
	} else {
		if ss.charPoint <= 0 {
			resp.Message = "没有可用属性点"
			resp.Character = buildUnitCharacter(ss)
			return resp
		}
		switch req.Trans {
		case 1:
			ss.strAdd++
		case 2:
			ss.qukAdd++
		case 3:
			ss.spiAdd++
		case 4:
			ss.wimAdd++
		default:
			resp.Message = "无效属性"
			resp.Character = buildUnitCharacter(ss)
			return resp
		}
		ss.charPoint--
	}
	resp.Character = buildUnitCharacter(ss)
	s.pushPlayerAttrs(ch)
	s.pushUnitCharacter(ch)
	s.saveData(ch)
	log.Printf("[S=%d] add point list=%v trans=%d charPoint=%d", ch.id, req.PointList, req.Trans, ss.charPoint)
	return resp
}

// onResetPoint：20255 → 20256。洗点：返还属性点。响应带 Character（客户端同 NRE 防护）。
// 同上：任何失败返回成功（Error=0）+ 当前 Character，不触发客户端 RpcException。
func (s *Server) onResetPoint(ch *channel, req *protocol.C2M_ResetPoint) proto.Message {
	resp := &protocol.M2C_ResetPoint{RpcId: req.RpcId}
	ss := ch.session
	if ss.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	ss.charPoint += ss.strAdd + ss.qukAdd + ss.spiAdd + ss.wimAdd
	ss.strAdd, ss.qukAdd, ss.spiAdd, ss.wimAdd = 0, 0, 0, 0
	resp.Character = buildUnitCharacter(ss)
	s.pushPlayerAttrs(ch)
	s.pushUnitCharacter(ch)
	s.saveData(ch)
	log.Printf("[S=%d] reset point charPoint=%d", ch.id, ss.charPoint)
	return resp
}

// ===================== 战斗消息处理 =====================

// startMainStoryFightByRegion：按 MainStory region 建立服务器战斗状态并推送战斗初始化
// （20047 InitMainStoryMap + 20050 MainStoryMonsterInfo）。onStartMainStoryFight（20048）
// 与 onClickMapUnit（20391 点击字段怪）共用。返回 (成功, 怪物数)。
func (s *Server) startMainStoryFightByRegion(ch *channel, region int32) (bool, int) {
	return s.startMainStoryFightByRegionWithPresentation(ch, region, battlePresentation{kind: presentationMainStory})
}

func (s *Server) startMainStoryFightByRegionWithPresentation(ch *channel, region int32, presentation battlePresentation) (bool, int) {
	if ch == nil || ch.session == nil {
		return false, 0
	}
	partyStartMu.Lock()
	defer partyStartMu.Unlock()
	ch.session.battleMu.Lock()
	defer ch.session.battleMu.Unlock()

	if tables == nil {
		return false, 0
	}
	if ch.session.battle != nil {
		log.Printf("[S=%d] reject start region=%d: battle already active", ch.id, region)
		return false, 0
	}
	ms, ok := tables.mainStory[int64(region)]
	if !ok {
		return false, 0
	}

	// 构建怪物单位（单位 id 从 10 亿起，避免与玩家 id 冲突）
	var ids []int32
	var counts []int
	for gi := 1; gi <= 6; gi++ {
		arrKey := fmt.Sprintf("Monster_%dArr", gi)
		idKey := fmt.Sprintf("Monster_%d_Id", gi)
		cntKey := fmt.Sprintf("Monster_%d_Count", gi)
		for _, e := range arrOf(ms[arrKey]) {
			eo, _ := e.(map[string]interface{})
			mid := int32(num(eo[idKey]))
			cnt := int(num(eo[cntKey]))
			if mid <= 0 || cnt <= 0 {
				continue
			}
			ids = append(ids, mid)
			counts = append(counts, cnt)
		}
	}
	units := ch.session.buildMonsterUnitsFromRoster(ids, counts)
	if len(units) == 0 {
		return false, 0
	}

	return s.finishStartBattleWithPresentation(ch, region, units, 0, presentation)
}

// buildMonsterUnitsFromRoster：按 (monsterID, count) 列表构建战斗怪物单位。
// 单位 id 从 10 亿起递增（nextBattleUnitBase），避免与玩家 id 冲突。
func (ss *session) buildMonsterUnitsFromRoster(ids []int32, counts []int) []*monsterUnit {
	if ss == nil || tables == nil {
		return nil
	}
	var units []*monsterUnit
	var seq = ss.nextBattleUnitBase()
	for gi, mid := range ids {
		if mid <= 0 {
			continue
		}
		cnt := 1
		if gi < len(counts) && counts[gi] > 0 {
			cnt = counts[gi]
		}
		mb, ok := tables.monsterBase[int64(mid)]
		if !ok {
			log.Printf("[S] monster %d not in MonsterBase", mid)
			continue
		}
		hp := int32(num(mb["Hp"]))
		for i := 0; i < cnt; i++ {
			seq++
			extra := make(map[int32]float64)
			for field, numericType := range equipFieldToNumeric {
				if value := numf(mb[field]); value != 0 {
					extra[numericType] = value
				}
			}
			units = append(units, &monsterUnit{
				id:           seq,
				monsterID:    mid,
				hp:           hp,
				maxHP:        hp,
				phyAtk:       int32(num(mb["PhyAtk"])),
				spiAtk:       int32(num(mb["SpiAtk"])),
				phyDef:       int32(num(mb["PhyDef"])),
				spiDef:       int32(num(mb["SpiDef"])),
				extraNumeric: extra,
				exp:          num(mb["Exp"]),
				alive:        true,
			})
		}
	}
	return units
}

// startSceneBattleByMap：非主线场景（挑战副本/Boss/手动装备/星空旅行）字段怪战斗。
// 普通副本复用 20047/20050；世界 BOSS 必须只走 20061 专用开战事件，否则客户端会
// 同时创建 BOSS 单位和主线怪物单位，产生重影，后续血量也会更新到错误的单位。
// 精力：Boss 场景（10010）按 CopyConfig.BossBattle.NeedBossEnergy 扣除；
// 其余副本场景不扣（线上 TrialCopyBattle 等没有 NeedEnergy）。
func (s *Server) startSceneBattleByMap(ch *channel, mapID int32) (bool, int) {
	ss := ch.session
	if ss == nil || tables == nil {
		return false, 0
	}
	partyStartMu.Lock()
	defer partyStartMu.Unlock()
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	if ss.battle != nil {
		log.Printf("[S=%d] reject scene battle map=%d: battle already active", ch.id, mapID)
		return false, 0
	}
	ids, counts, _, ok := sceneRosterForMap(mapID)
	if !ok || len(ids) == 0 {
		return false, 0
	}
	units := ss.buildMonsterUnitsFromRoster(ids, counts)
	if len(units) == 0 {
		return false, 0
	}
	// Boss 场景（10010）：BOSS 死亡期间拒绝开战（刷新时间倒计时提示）。
	if layer := bossLayerForMapID(mapID); layer > 0 {
		if remain := bossDeadRemaining(layer); remain > 0 {
			log.Printf("[S=%d] reject boss fight map=%d: dead, respawn in %s", ch.id, mapID, remain)
			return false, 0
		}
	}
	// 客户端 M2C_InitMainStoryMapHandler 用 MainStoryId 查 MainStoryCategory 建战斗队伍：
	// 表 _id 从 1001 开始（10006 海滩第 1 层）。传 mapID%100（如 Boss 层 1）会查不到行
	// → 战斗场景不建 → 客户端看不到战斗界面（用户实测：点怪开战但界面不显示，只有
	// 自动战斗在后台打，胜利后自动回城）。非主线场景统一映射到客户端必有行的 1001。
	region := int32(1000 + (mapID % 100))
	if _, ok := tables.mainStory[int64(region)]; !ok {
		region = 1001
	}
	if layer := bossLayerForMapID(mapID); layer > 0 {
		if len(units) != 1 {
			log.Printf("[S=%d] reject boss fight map=%d: roster has %d units", ch.id, mapID, len(units))
			return false, 0
		}
		return s.finishStartDedicatedBossBattle(ch, region, units, mapID, 0)
	}
	return s.finishStartBattle(ch, region, units, mapID)
}

// finishStartBattle：建立服务器端战斗状态并推送 20047/20050（调用方须持 battleMu）。
// 玩家属性 = 线上成长 + 加点 + 装备（见 growth.go）；血蓝继承会话当前值（吃药可恢复）。
func (s *Server) finishStartBattle(ch *channel, region int32, units []*monsterUnit, mapID int32) (bool, int) {
	return s.finishStartBattleWithPresentation(ch, region, units, mapID, battlePresentation{kind: presentationMainStory})
}

type battlePresentationKind uint8

const (
	presentationMainStory battlePresentationKind = iota
	presentationTrial
	presentationManualEquip
	presentationWorldBoss
	presentationFamilyBoss
)

type battlePresentation struct {
	kind            battlePresentationKind
	familyBossID    int32
	manualConfigIDs []int32
	copyID          int64
	activity        *activityBattle
	skipEnergy      bool
}

// finishStartTrialBattle keeps the native 20091/20093 trial RPC flow, then
// emits the client's existing 20050 battle-presentation event with the exact
// same monster ids/unit ids. In this client build StartTrialCopyFightEvent is
// implemented but has no publisher: the 20093 await continuation only removes
// TrialCopyMonsterTeamComponent's display monster. Without this presentation
// event attacks play directly in the field and neither side moves to the
// battle formation circles.
func (s *Server) finishStartTrialBattle(ch *channel, region int32, units []*monsterUnit, mapID int32) (bool, int) {
	return s.finishStartBattleWithPresentation(ch, region, units, mapID, battlePresentation{kind: presentationTrial})
}

func (s *Server) finishStartManualEquipBattle(ch *channel, region int32, units []*monsterUnit, mapID int32, configIDs []int32) (bool, int) {
	return s.finishStartBattleWithPresentation(ch, region, units, mapID, battlePresentation{
		kind:            presentationManualEquip,
		manualConfigIDs: append([]int32(nil), configIDs...),
	})
}

// finishStartDedicatedBossBattle 建立专用 BOSS 战斗。familyBossID=0 表示世界 BOSS，
// 非 0 表示 FamilyBossConfig._id。专用事件本身会创建战斗队伍、BOSS 单位和 HUD，
// 不能再叠加 20047/20050 主线创建链路。
func (s *Server) finishStartDedicatedBossBattle(ch *channel, region int32, units []*monsterUnit, mapID, familyBossID int32) (bool, int) {
	if len(units) != 1 {
		return false, 0
	}
	presentation := battlePresentation{kind: presentationWorldBoss}
	if familyBossID != 0 {
		presentation.kind = presentationFamilyBoss
		presentation.familyBossID = familyBossID
	}
	return s.finishStartBattleWithPresentation(ch, region, units, mapID, presentation)
}

// finishStartBattleWithPresentation enrolls every eligible, online team member
// in one authoritative fight. Callers hold partyStartMu and the origin
// session's battleMu; this function locks the other participants while their
// battle states are installed.
func (s *Server) finishStartBattleWithPresentation(ch *channel, region int32, units []*monsterUnit, mapID int32, presentation battlePresentation) (bool, int) {
	if ch == nil || ch.session == nil || len(units) == 0 {
		return false, 0
	}
	copyID := battleCopyConfig(region, mapID, presentation)
	participants := s.configuredBattleParticipants(ch, copyID, presentation.kind == presentationTrial)
	if len(participants) == 0 {
		participants = []*channel{ch}
	}
	if presentation.kind == presentationFamilyBoss {
		familyID := ch.session.familyID
		eligible := participants[:0]
		for _, member := range participants {
			if member != nil && member.session != nil && member.session.familyID == familyID {
				eligible = append(eligible, member)
			}
		}
		participants = eligible
		if len(participants) == 0 {
			participants = []*channel{ch}
		}
	}
	if row := tables.copyConfig[copyID]; row != nil {
		minimum := int(num(row["MinTeamMember"]))
		if minimum < 1 {
			minimum = 1
		}
		if len(participants) < minimum {
			log.Printf("[S=%d] reject copy=%d: team members=%d need=%d", ch.id, copyID, len(participants), minimum)
			return false, 0
		}
	}

	locked := make([]*session, 0, len(participants)-1)
	for _, member := range participants {
		if member == nil || member.session == nil || member == ch {
			continue
		}
		member.session.battleMu.Lock()
		locked = append(locked, member.session)
	}
	defer func() {
		for i := len(locked) - 1; i >= 0; i-- {
			locked[i].battleMu.Unlock()
		}
	}()

	for _, member := range participants {
		if member == nil || member.session == nil || member.session.battle != nil {
			return false, 0
		}
	}
	if message := battleEntryHealthFailure(ch, participants); message != "" {
		log.Printf("[S=%d] reject battle copy=%d: %s", ch.id, copyID, message)
		return false, 0
	}
	energyCost := battleEnergyCost(copyID)
	if presentation.skipEnergy {
		energyCost = 0
	}
	if energyCost > 0 {
		for _, member := range participants {
			if member.session.energy < energyCost {
				log.Printf("[S=%d] reject team battle: player=%d energy=%d need=%d", ch.id,
					member.session.playerID, member.session.energy, energyCost)
				return false, 0
			}
		}
		for _, member := range participants {
			member.session.energy -= energyCost
			s.pushPlayerProgress(member)
			s.saveData(member)
			log.Printf("[S=%d] battle energy -%d -> %d", member.id, energyCost, member.session.energy)
		}
	}
	if len(participants) > 1 && (presentation.kind == presentationMainStory ||
		presentation.kind == presentationTrial || presentation.kind == presentationWorldBoss) {
		scaleMonsterHPForTeam(units, len(participants))
	}

	// Native client battle events synchronously enumerate TeamComponent and
	// NumericComponent. Refresh both before any participant receives the event.
	s.preparePartyBattleClients(participants, ch.session.playerID)

	autoEnabled := make(map[int64]bool, len(participants))
	for _, member := range participants {
		ss := member.session
		autoEnabled[ss.playerID] = ss.autoBattleEnabled
		ss.autoBattle = false
	}

	for _, member := range participants {
		started, _ := s.finishStartSingleBattleWithPresentation(member, region, units, mapID, presentation)
		if !started {
			for _, installed := range participants {
				if installed != nil && installed.session != nil && installed.session.battle != nil {
					installed.session.battle.ended = true
					installed.session.battle = nil
				}
			}
			for _, restore := range participants {
				if restore != nil && restore.session != nil {
					restore.session.autoBattle = autoEnabled[restore.session.playerID]
				}
			}
			return false, 0
		}
	}

	if len(participants) > 1 {
		party := &partyBattle{
			members:   make(map[int64]*battleState, len(participants)),
			memberIDs: make([]int64, 0, len(participants)),
		}
		for _, member := range participants {
			battle := member.session.battle
			battle.party = party
			battle.monsters = units
			party.members[member.session.playerID] = battle
			party.memberIDs = append(party.memberIDs, member.session.playerID)
		}
		party.shareCombatState()
		s.pushPartyBattleResources(participants)
	}

	for _, member := range participants {
		ss := member.session
		ss.autoBattle = autoEnabled[ss.playerID]
		if !ss.autoBattle {
			continue
		}
		ss.autoBattleEpoch++
		ss.autoBattleNextCastAt = time.Time{}
		ss.autoBattleSkillCursor = 0
		go s.runAutoBattle(member, ss.autoBattleEpoch)
		log.Printf("[S=%d] auto battle resumed for team fight region=%d", member.id, region)
	}
	return true, len(units)
}

const battleEntryHealthMessage = "生命值不足，请先恢复"

// battleEntryHealthFailure checks inherited HP before energy is consumed or
// combat state is installed. All battle types converge on this guard.
func battleEntryHealthFailure(origin *channel, participants []*channel) string {
	for _, member := range participants {
		if member == nil || member.session == nil || member.session.battleHP() > 0 {
			continue
		}
		if origin != nil && origin.session != nil && member.session.playerID != origin.session.playerID {
			name := member.session.name
			if name == "" {
				name = fmt.Sprintf("玩家%d", member.session.playerID)
			}
			return fmt.Sprintf("队员%s生命值不足，请先恢复", name)
		}
		return battleEntryHealthMessage
	}
	return ""
}

func (s *Server) finishStartSingleBattleWithPresentation(ch *channel, region int32, units []*monsterUnit, mapID int32, presentation battlePresentation) (bool, int) {
	ss := ch.session
	pid := ss.playerID
	battleType := int32(1)
	trialCopyID := int32(0)
	copyID := battleCopyConfig(region, mapID, presentation)
	if row := tables.copyConfig[copyID]; row != nil {
		if configured := int32(num(row["BattleType"])); configured > 0 {
			battleType = configured
		}
	}
	if id, _, ok := trialCopyForMap(mapID); ok {
		trialCopyID = id
	}

	// 建立服务器端战斗状态（玩家属性 = 线上成长 + 加点 + 装备，见 growth.go）。
	// 进战斗时血蓝继承会话当前值（非战斗血蓝持久在 session.hp/mp，吃药可恢复）。
	battle := &battleState{
		region:      region,
		mapID:       mapID, // Boss 击杀判定用（0=主线战斗）
		battleType:  battleType,
		trialCopyID: trialCopyID,
		monsters:    units,
		playerHP:    ss.battleHP(),
		playerMaxHP: ss.playerMaxHp(),
		playerMP:    ss.battleMP(),
		playerMaxMP: ss.playerMaxMp(),
		phyAtk:      ss.playerPhyAtk(),
		spiAtk:      ss.playerSpiAtk(),
		phyDef:      ss.playerPhyDef(),
		spiDef:      ss.playerSpiDef(),
		owner:       ss,
		activity:    presentation.activity.clone(),
	}
	battle.runtime = NewCombatRuntime(battle, pid, nil)
	ch.session.battle = battle
	passiveEvents, passiveErr := battle.initializePassiveSkills(ss, nil)
	if passiveErr != nil {
		log.Printf("[S=%d] initialize battle passives: %v", ch.id, passiveErr)
	}

	switch presentation.kind {
	case presentationWorldBoss:
		boss := units[0]
		s.sendPush(ch, protocol.OpM2C_SendBossInfo, &protocol.M2C_SendBossInfo{
			BossId: boss.monsterID, UnitId: boss.id, ActorId: pid,
		})
		log.Printf("[S=%d] world boss fight start bossId=%d unitId=%d hp=%d/%d",
			ch.id, boss.monsterID, boss.id, boss.hp, boss.maxHP)
	case presentationFamilyBoss:
		boss := units[0]
		s.sendPush(ch, protocol.OpM2C_SendFamilyBossInfo, &protocol.M2C_SendFamilyBossInfo{
			BossId: presentation.familyBossID, UnitId: boss.id, Hp: boss.hp, ActorId: pid,
		})
		log.Printf("[S=%d] family boss fight start bossId=%d monsterId=%d unitId=%d hp=%d/%d",
			ch.id, presentation.familyBossID, boss.monsterID, boss.id, boss.hp, boss.maxHP)
	case presentationManualEquip:
		infoList := make([]*protocol.MonsterUnitInfo, 0, len(units))
		for i, u := range units {
			configID := u.monsterID
			if i < len(presentation.manualConfigIDs) {
				configID = presentation.manualConfigIDs[i]
			}
			infoList = append(infoList, &protocol.MonsterUnitInfo{Id: u.id, MonsterId: configID})
		}
		s.sendPush(ch, protocol.OpM2C_SendManulEquipMonsterInfo, &protocol.M2C_SendManulEquipMonsterInfo{
			UnitIdList: infoList,
			ActorId:    pid,
		})
	case presentationMainStory, presentationTrial:
		// 20047 was already sent when the map was initialised. Starting a fight
		// only sends 20050, otherwise the display team is created a second time.
		infoList := make([]*protocol.MonsterUnitInfo, 0, len(units))
		for _, u := range units {
			infoList = append(infoList, &protocol.MonsterUnitInfo{Id: u.id, MonsterId: u.monsterID})
		}
		s.sendPush(ch, protocol.OpM2C_MainStoryMonsterInfo, &protocol.M2C_MainStoryMonsterInfo{
			MonsterUnitInfoList: infoList,
			BattleType:          2, // ET.UnitType.Monster, not the battle result type
			ActorId:             pid,
		})
	}
	// 玩家单位在所有战斗类型里都是已有单位。开战时把权威当前/最大资源重发一次，
	// 避免客户端显示满蓝、服务端却因上一场剩余 MP 判定自动技能不可释放。
	s.pushBattleResources(ch, battle)
	s.emitCombatEvents(ch, battle, passiveEvents)

	// 自动战斗常驻：用户开着自动战斗开关 → 新战斗立即重新拉起 worker
	// （autoBattleStep/settleCombatLocked 胜利后只失效 worker 不关开关；
	// 这里在锁内递增 epoch 并启动新 worker，旧 worker 观察 epoch 不符退出）。
	if ss.autoBattle {
		ss.autoBattleEpoch++
		ss.autoBattleNextCastAt = time.Time{}
		ss.autoBattleSkillCursor = 0
		go s.runAutoBattle(ch, ss.autoBattleEpoch)
		log.Printf("[S=%d] auto battle resumed for new fight region=%d", ch.id, region)
	}
	return true, len(units)
}

func (s *Server) pushBattleResources(ch *channel, battle *battleState) {
	if ch == nil || ch.session == nil || battle == nil {
		return
	}
	for _, attr := range []struct {
		numericType int32
		value       int32
	}{
		{numericType: 1002, value: battle.playerMaxHP},
		{numericType: 1001, value: battle.playerHP},
		{numericType: 1004, value: battle.playerMaxMP},
		{numericType: 1003, value: battle.playerMP},
	} {
		s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: ch.session.playerID, NumericType: attr.numericType,
			Value: float32(attr.value), ActorId: ch.session.playerID,
		})
	}
}

// 20048 → 20049：开始主线战斗。Region = MainStory._id。
// 响应 20049 Message 必须留空（客户端判空才发起战斗场景）。
// 随后推送 M2C_InitMainStoryMap(20047) + M2C_MainStoryMonsterInfo(20050)。
func (s *Server) onStartMainStoryFight(ch *channel, req *protocol.C2M_StartMainStoryFight) proto.Message {
	resp := &protocol.M2C_StartMainStoryFight{RpcId: req.RpcId}
	if ch.session.battleHP() <= 0 {
		resp.Message = battleEntryHealthMessage
		return resp
	}
	region := mainStoryRegionForSession(ch.session, req.Region)
	if ch.session.energy < mainStoryEnergyCost(region) {
		resp.Message = "体力不足"
		return resp
	}
	ok, n := s.startMainStoryFightByRegion(ch, region)
	if !ok {
		// Field-monster and main-story click paths can overlap. Treat a duplicate
		// request as an idempotent success so Session.Call does not disconnect.
		if sessionHasBattle(ch) {
			log.Printf("[S=%d] duplicate start main story fight region=%d ignored", ch.id, region)
			return resp
		}
		log.Printf("[S=%d] start main story fight failed requested=%d resolved=%d", ch.id, req.Region, region)
		resp.Message = "章节不存在或没有怪物"
		return resp
	}
	log.Printf("[S=%d] start main story fight region=%d monsters=%d", ch.id, region, n)
	return resp
}

type skillDamageMode uint8

const (
	skillDamageNone skillDamageMode = iota
	skillDamageAttackMultiplier
	skillDamageTargetCurrentHP
	skillDamagePlayerCurrentHP
	skillDamagePlayerMaxHP
)

type skillCastSpec struct {
	cd            int32
	castType      int32
	castValue     float64
	effectID      int32
	delay         int32
	friendly      bool
	damageMode    skillDamageMode
	targetCount   int
	damageValue   float64
	spiritualHit  bool
	ignoreDefense bool
}

// Team 来自已逆向校验的 SkillLogicConfig。SkillConfig 表本身没有 Team 字段。
// 友方和自身技能在当前单人战斗中都以玩家自身为目标，绝不能按敌方结算。
var friendlySkillIDs = map[int32]struct{}{
	110301: {}, 110303: {}, 110401: {}, 110404: {}, 110501: {}, 110502: {},
	110601: {}, 110602: {}, 110604: {},
	210404: {}, 210504: {},
	310301: {}, 310302: {}, 310401: {}, 310402: {}, 310404: {}, 310501: {},
	310304: {}, 310503: {}, 310504: {}, 310601: {},
	410304: {}, 410402: {}, 410404: {}, 410504: {}, 410602: {}, 410604: {},
}

// 这些技能只有持续状态、条件引爆或增减益。状态机尚未实现前安全跳过即时伤害。
var noImmediateDamageSkillIDs = map[int32]struct{}{
	210404: {}, 210504: {}, 310202: {}, 310303: {}, 310304: {}, 310403: {},
	310503: {}, 310603: {}, 310604: {}, 410403: {}, 410404: {}, 410602: {},
}

// 这两个技能的等级行 Args 是状态参数，伤害百分比由技能逻辑定义为生命百分比，
// 不能套用普通的攻击倍率解析。
var officerCurrentHPDamagePercent = map[int32]float64{1: 1, 2: 2, 3: 4, 4: 7}
var officerMaxHPDamagePercent = map[int32]float64{1: 2, 2: 4, 3: 7}

// 以下表逐项来自 SkillLogicConfig。目标数和伤害倍率使用明确字段或逻辑常量，
// 不再从 Args 最大值、EffectId 等表现字段推断。
var targetCountArgIndexBySkillID = map[int32]int{
	110101: 0,
	310201: 0, 310502: 0, 310602: 0,
	410101: 1, 410201: 1, 410202: 0, 410301: 1, 410302: 0,
	410303: 0, 410401: 1, 410501: 1, 410503: 0,
	420101: 1,
}

var fixedTargetCountBySkillID = map[int32]int{
	210201: 2,
	210501: 3,
	410502: 10,
	410601: 10,
	410603: 5,
}

var damageArgIndexBySkillID = map[int32]int{
	100001: 1,
	110101: 1, 110201: 0, 110202: 0, 110302: 0, 110304: 0,
	110402: 0, 110403: 0, 110504: 0, 120101: 0,
	200001: 1,
	210101: 0, 210201: 0, 210202: 0, 210301: 0, 210302: 0,
	210303: 0, 210304: 0, 210401: 0, 210402: 0, 210403: 0,
	210501: 1, 210502: 0, 210503: 0, 210601: 1, 210602: 0,
	210603: 3, 210604: 4, 220101: 0,
	300001: 1,
	310101: 0, 310201: 1, 310502: 1, 310602: 1, 320101: 0,
	410101: 2, 410201: 2, 410202: 1, 410301: 2, 410302: 1,
	410303: 1, 410401: 2, 410501: 2, 410503: 1, 410601: 1,
}

var fixedDamageBySkillIDAndLevel = map[int32]map[int32]float64{
	400001: {1: 193},
	410502: {1: 100, 2: 120, 3: 150, 4: 180},
	410603: {1: 50, 2: 50, 3: 50},
	420101: {
		1: 140, 2: 145, 3: 150, 4: 155, 5: 160,
		6: 165, 7: 170, 8: 175, 9: 180, 10: 185,
	},
}

var ignoreDefenseSkillIDs = map[int32]struct{}{
	110504: {},
}

func hasID(set map[int32]struct{}, id int32) bool {
	_, ok := set[id]
	return ok
}

func skillArg(row map[string]interface{}, index int) (float64, bool) {
	v, ok := row[fmt.Sprintf("Args%d", index)]
	if !ok {
		return 0, false
	}
	return numf(v), true
}

func skillDamageValue(skillID, level int32, levelCfg map[string]interface{}) (float64, bool) {
	if byLevel, ok := fixedDamageBySkillIDAndLevel[skillID]; ok {
		value, exists := byLevel[level]
		return value, exists
	}
	argIndex, ok := damageArgIndexBySkillID[skillID]
	if !ok {
		return 0, false
	}
	return skillArg(levelCfg, argIndex)
}

func skillTargetCount(skillID int32, levelCfg map[string]interface{}) int {
	if count, ok := fixedTargetCountBySkillID[skillID]; ok {
		return count
	}
	if argIndex, ok := targetCountArgIndexBySkillID[skillID]; ok {
		if v, ok := skillArg(levelCfg, argIndex); ok && v >= 1 && v <= 20 {
			return int(v)
		}
	}
	return 1
}

func parseSkillCastSpec(skillID, level int32, baseCfg, levelCfg map[string]interface{}) (skillCastSpec, error) {
	var spec skillCastSpec
	if baseCfg == nil || levelCfg == nil || level <= 0 {
		return spec, fmt.Errorf("skill config incomplete")
	}
	if int32(num(baseCfg["SkillType"])) == 1 {
		return spec, fmt.Errorf("passive skill cannot be cast")
	}

	spec.cd = int32(num(baseCfg["CD"]))
	spec.castType = int32(num(baseCfg["CastType"]))
	spec.castValue = numf(baseCfg["CastValue"])
	spec.effectID = int32(num(baseCfg["EffectId"]))
	spec.delay = int32(num(baseCfg["DelayTime"]))
	spec.friendly = hasID(friendlySkillIDs, skillID)
	job := skillID / 100000
	spec.spiritualHit = job == 3 || job == 4

	if spec.cd < 0 || spec.castType < 0 || spec.castType > 2 ||
		math.IsNaN(spec.castValue) || math.IsInf(spec.castValue, 0) || spec.castValue < 0 {
		return skillCastSpec{}, fmt.Errorf("invalid cast metadata")
	}
	if spec.friendly || hasID(noImmediateDamageSkillIDs, skillID) {
		return spec, nil
	}
	if skillID == 110503 {
		if value, ok := officerCurrentHPDamagePercent[level]; !ok {
			return skillCastSpec{}, fmt.Errorf("invalid skill level")
		} else {
			spec.damageMode = skillDamagePlayerCurrentHP
			spec.targetCount = 1
			spec.damageValue = value
			return spec, nil
		}
	}
	if skillID == 110603 {
		if value, ok := officerMaxHPDamagePercent[level]; !ok {
			return skillCastSpec{}, fmt.Errorf("invalid skill level")
		} else {
			spec.damageMode = skillDamagePlayerMaxHP
			spec.targetCount = 2
			if level >= 3 {
				spec.targetCount = 3
			}
			spec.damageValue = value
			return spec, nil
		}
	}
	damageValue, hasImmediateDamage := skillDamageValue(skillID, level, levelCfg)
	if !hasImmediateDamage {
		return spec, nil
	}

	spec.targetCount = skillTargetCount(skillID, levelCfg)
	if skillID == 110403 {
		spec.damageMode = skillDamageTargetCurrentHP
		spec.damageValue, _ = skillArg(levelCfg, 0)
	} else {
		spec.damageMode = skillDamageAttackMultiplier
		spec.damageValue = damageValue
		spec.ignoreDefense = hasID(ignoreDefenseSkillIDs, skillID)
	}
	if spec.targetCount <= 0 || spec.damageValue <= 0 || math.IsNaN(spec.damageValue) || math.IsInf(spec.damageValue, 0) {
		return skillCastSpec{}, fmt.Errorf("invalid damage arguments")
	}
	return spec, nil
}

func (b *battleState) skillResourceCost(castType int32, castValue float64) (mpCost, hpCost int32, err error) {
	if b == nil || castValue < 0 || math.IsNaN(castValue) || math.IsInf(castValue, 0) {
		return 0, 0, fmt.Errorf("invalid resource cost")
	}
	switch castType {
	case 0:
		return 0, 0, nil
	case 1:
		if castValue == 0 {
			return 0, 0, nil
		}
		mpCost = int32(math.Ceil(float64(b.playerMaxMP) * castValue))
		if mpCost <= 0 || mpCost > b.playerMP {
			return 0, 0, fmt.Errorf("not enough MP")
		}
		return mpCost, 0, nil
	case 2:
		if castValue == 0 {
			return 0, 0, nil
		}
		if b.playerHP <= 1 {
			return 0, 0, fmt.Errorf("not enough HP")
		}
		// 血量施法按当前 HP 比例扣除；100% 消耗保留 1 HP，避免施法请求
		// 在结算前直接令施法者死亡。
		hpCost = int32(math.Ceil(float64(b.playerHP) * castValue))
		if hpCost >= b.playerHP {
			hpCost = b.playerHP - 1
		}
		if hpCost <= 0 {
			return 0, 0, fmt.Errorf("not enough HP")
		}
		return 0, hpCost, nil
	default:
		return 0, 0, fmt.Errorf("unsupported cast type")
	}
}

func (b *battleState) selectSkillTargets(count int) []*monsterUnit {
	alive := b.aliveMonsters()
	if len(alive) == 0 || count <= 0 {
		return nil
	}
	if count > len(alive) {
		count = len(alive)
	}
	out := make([]*monsterUnit, 0, count)
	if b.selectedID != 0 {
		for _, m := range alive {
			if m.id == b.selectedID {
				out = append(out, m)
				break
			}
		}
	}
	for _, m := range alive {
		if len(out) >= count {
			break
		}
		if len(out) > 0 && out[0] == m {
			continue
		}
		out = append(out, m)
	}
	return out
}

// 20233 → 20234：点击技能槽使用技能。SlotId = 槽序号。
// 响应 20234 的 Message 必须留空，否则客户端误发 C2M_UseMainUIGoods。
func (s *Server) onUseMainUISkill(ch *channel, req *protocol.C2M_UseMainUISkill) proto.Message {
	resp := &protocol.M2C_UseMainUISkill{RpcId: req.RpcId}
	if ch == nil || ch.session == nil {
		resp.Error, resp.Message = errNotLogged, "login required"
		return resp
	}
	ch.session.battleMu.Lock()
	defer ch.session.battleMu.Unlock()
	return s.useMainUISkillLocked(ch, req, resp)
}

// useMainUISkillLocked contains the authoritative cast path. Callers must hold
// session.battleMu so manual input and the automatic battle worker cannot settle
// two actions against the same battle state concurrently.
func (s *Server) useMainUISkillLocked(ch *channel, req *protocol.C2M_UseMainUISkill, resp *protocol.M2C_UseMainUISkill) proto.Message {
	return s.useSkillPlanLocked(ch, req, resp)
}

func (s *Server) useMainUISkillLegacyLocked(ch *channel, req *protocol.C2M_UseMainUISkill, resp *protocol.M2C_UseMainUISkill) proto.Message {
	b := ch.session.battle
	if tables == nil {
		resp.Error, resp.Message = errBadParam, "配置未加载"
		return resp
	}
	if b == nil || b.ended {
		resp.Error, resp.Message = errBadParam, "未在战斗中"
		return resp
	}
	if len(ch.session.skillOrder) == 0 {
		resp.Error, resp.Message = errBadParam, "技能栏为空"
		return resp
	}
	if int(req.SlotId) < 0 || int(req.SlotId) >= len(ch.session.skillOrder) {
		resp.Error, resp.Message = errBadParam, "技能槽无效"
		return resp
	}

	skillID := ch.session.skillOrder[req.SlotId]
	level, learned := ch.session.skills[skillID]
	if !learned || level <= 0 {
		resp.Error, resp.Message = errBadParam, "skill not learned"
		return resp
	}
	// xx00 stores combat metadata. xx01..xxNN are level argument rows and
	// intentionally omit CD, cast and effect fields.
	cfg, ok := tables.skillConfig[int64(skillID)*100]
	if !ok {
		resp.Error, resp.Message = errBadParam, "技能配置不存在"
		return resp
	}

	levelCfg, ok := tables.skillConfig[int64(skillID)*100+int64(level)]
	if !ok {
		resp.Error, resp.Message = errBadParam, "skill level config missing"
		return resp
	}
	spec, err := parseSkillCastSpec(skillID, level, cfg, levelCfg)
	if err != nil {
		resp.Error, resp.Message = errBadParam, err.Error()
		return resp
	}
	if spec.damageMode == skillDamageNone {
		// 纯状态、治疗和护盾尚无服务端状态机。必须在扣资源、登记 CD、
		// 推送表现和怪物反击前拒绝，避免玩家消耗资源后得到一次空施放。
		resp.Error, resp.Message = errBadParam, "skill effect not implemented"
		return resp
	}
	pid := ch.session.playerID

	// 先确认战场仍有目标；实际受击列表再按技能目标数和当前选中目标生成。
	targets := b.aliveMonsters()
	if len(targets) == 0 {
		resp.Error, resp.Message = errBadParam, "没有可攻击目标"
		return resp
	}
	var hit []*monsterUnit
	if spec.damageMode != skillDamageNone {
		hit = b.selectSkillTargets(spec.targetCount)
		if len(hit) == 0 {
			resp.Error, resp.Message = errBadParam, "no attack target"
			return resp
		}
	}
	mpCost, hpCost, err := b.skillResourceCost(spec.castType, spec.castValue)
	if err != nil {
		resp.Error, resp.Message = errBadParam, err.Error()
		return resp
	}
	if remaining, ready := ch.session.tryStartSkillCooldown(skillID, time.Now(), time.Duration(spec.cd)*time.Millisecond); !ready {
		resp.Error = errBadParam
		resp.Message = fmt.Sprintf("skill cooldown: %dms", maxInt64(1, remaining.Milliseconds()))
		return resp
	}
	b.playerMP -= mpCost
	b.playerHP -= hpCost
	targetID := pid
	if !spec.friendly {
		targetID = targets[0].id
		if len(hit) > 0 {
			targetID = hit[0].id
		}
	}

	// 1) 播放技能（20075）
	s.sendPush(ch, protocol.OpM2C_PlaySkill, &protocol.M2C_PlaySkill{
		UnitId:   pid,
		SkillId:  skillID,
		CoolTime: spec.cd,
		TargetId: targetID,
		MpCost:   mpCost,
	})
	if mpCost > 0 {
		// 当前客户端版本没有注册 20079 handler，使用通用 Numeric 同步更新蓝条。
		s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: pid, NumericType: 1003, Value: float32(b.playerMP), ActorId: pid,
		})
	}
	if hpCost > 0 {
		s.sendPush(ch, protocol.OpM2C_BattleSkillRet, &protocol.M2C_BattleSkillRet{
			UnitId: pid, ChangeHpValue: -hpCost,
		})
	}
	if spec.effectID > 0 {
		s.sendPush(ch, protocol.OpM2C_PlaySkillEffect, &protocol.M2C_PlaySkillEffect{
			UnitId: pid, TargetId: targetID, Time: spec.delay, EffectId: spec.effectID,
		})
	}

	// 3) 逐个目标结算伤害（20078）
	for _, m := range hit {
		var dmg int32
		switch spec.damageMode {
		case skillDamageAttackMultiplier:
			dmg = b.attackMultiplierDamage(spec, m)
		case skillDamageTargetCurrentHP:
			dmg = int32(math.Ceil(float64(m.hp) * spec.damageValue / 100))
			if dmg < 1 {
				dmg = 1
			}
		case skillDamagePlayerCurrentHP:
			dmg = int32(math.Ceil(float64(b.playerHP) * spec.damageValue / 100))
			if dmg < 1 {
				dmg = 1
			}
		case skillDamagePlayerMaxHP:
			dmg = int32(math.Ceil(float64(b.playerMaxHP) * spec.damageValue / 100))
			if dmg < 1 {
				dmg = 1
			}
		}
		if dmg > m.hp {
			dmg = m.hp
		}
		m.hp -= dmg
		s.sendPush(ch, protocol.OpM2C_BattleSkillRet, &protocol.M2C_BattleSkillRet{
			UnitId:        m.id,
			ChangeHpValue: -dmg,
		})
		// 20078 负责受击/飘字；20169 向现有怪物单位同步血量。
		s.pushMonsterHP(ch, m)
		log.Printf("[S=%d] player skill %d hits monster %d dmg=%d hp=%d/%d",
			ch.id, skillID, m.id, dmg, m.hp, m.maxHP)
		if m.hp <= 0 {
			m.hp = 0
			m.alive = false
			if b.selectedID == m.id {
				b.selectedID = 0
			}
			s.sendPush(ch, protocol.OpM2C_MainstoryMonsterDead, &protocol.M2C_MainstoryMonsterDead{UnitId: m.id})
		}
	}
	// 4) CD 转圈（20237）
	s.sendPush(ch, protocol.OpM2C_StartCD, &protocol.M2C_StartCD{
		Id:      skillID,
		SkillCD: spec.cd,
		Type:    protocol.MainUIType_SkillSlot,
	})

	// 5) 全灭 → 胜利
	if len(b.aliveMonsters()) == 0 {
		b.ended = true
		s.emitVictory(ch, b)
		return resp
	}

	// 6) 怪物反击（存活怪物各打玩家一次）
	s.monstersAttack(ch, b)
	return resp
}

// playerDamage：按等级行的百分比倍率结算玩家技能伤害。
// 公式：max(1, 玩家攻击 - 目标防御) × multiplier / 100。
func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func (b *battleState) playerDamage(multiplier float64, def int32, spiritual bool) int32 {
	atk := b.phyAtk
	if spiritual {
		atk = b.spiAtk
	}
	base := atk - def
	if base < 1 {
		base = 1
	}
	d := float64(base) * multiplier / 100
	if d < 1 {
		d = 1
	}
	return int32(d)
}

func (b *battleState) attackMultiplierDamage(spec skillCastSpec, target *monsterUnit) int32 {
	def := target.phyDef
	if spec.spiritualHit {
		def = target.spiDef
	}
	if spec.ignoreDefense {
		def = 0
	}
	return b.playerDamage(spec.damageValue, def, spec.spiritualHit)
}

func (b *battleState) aliveMonsters() []*monsterUnit {
	var out []*monsterUnit
	for _, m := range b.monsters {
		if m.alive {
			out = append(out, m)
		}
	}
	return out
}

// emitVictory：胜利结算 —— 20054 BattleVictory + 20084 SendReward。
// 同时累加击杀计数（KillSpecial 任务用），并持久化任务状态。
func (s *Server) emitVictory(ch *channel, b *battleState) {
	pid := ch.session.playerID
	syncBattleHealthToSession(ch.session) // 胜利残血写回 → 非战斗吃药可恢复
	// 魔法球（战斗结束自动回满）：胜利结算触发（活跃 buff 一次性生效）。
	s.applyMagicBallRecover(ch)
	var exp int64
	for _, m := range b.monsters {
		if !m.alive {
			exp += m.exp
		}
	}
	s.recordTaskMonsterKills(ch.session, b.monsters)
	// 击杀计数变化 → 刷新涉及任务的显示状态（Running → Completed 可提交），
	// 客户端任务面板/追踪随 M2C_SendTaskState 更新（"1/3、2/3"进度由 GetTask 推送）。
	s.pushTaskProgressAfterKill(ch)
	coin := exp / 10
	var rewardItems []*protocol.RewardItem
	var trialDrops trialReward
	if b.battleType == trialBattleType {
		trialDrops = trialBattleDrops(b)
		exp += trialDrops.exp
		coin = trialDrops.coin
		rewardItems = s.applyTrialReward(ch, trialDrops, false)
	} else if b.activity != nil {
		// Activity copy rows use the same authoritative MonsterBase drop chain.
		// Keep bag/money changes transactional through the existing trial reward
		// staging helper; StarSoulCopy has its own instance-bag reward below.
		activityDrops := trialBattleDrops(b)
		exp += activityDrops.exp
		coin += activityDrops.coin
		rewardItems = s.applyTrialReward(ch, activityDrops, false)
		s.awardStarSoulActivity(ch, b)
	}
	s.saveData(ch)
	displayExp := experienceAfterMultiplier(ch.session, exp)
	s.gainExp(ch, exp)
	ch.session.idleBattle = false
	if b.battleType == trialBattleType {
		s.recordTrialVictory(ch, b.trialCopyID)
	}
	// 家族 BOSS 击杀：battle.mapID = -BossId（family.go onStartFamilyBossFight 标记）。
	// 胜利 → BOSS 血归 0、HasReward=true、击杀者加贡献；败北/退出 → 血写回家族状态。
	if b.mapID < 0 {
		bossID := -b.mapID
		ss := ch.session
		if ss.familyID != 0 {
			st := familyBossState(ss.familyID, bossID)
			if len(b.aliveMonsters()) == 0 {
				// 胜利：BOSS 被击杀
				st.Hp = 0
				st.HasReward = true
				if row, ok := tables.familyBossConfig[int64(bossID)]; ok {
					ss.personalContribute += int32(num(row["PersonalContribute"]))
					ss.familyContribute += int32(num(row["Contribute"]))
				}
				saveFamilyBossStateDB(ss.familyID, familyBossAllStates(ss.familyID))
				log.Printf("[S=%d] family boss %d defeated, reward ready", ch.id, bossID)
			} else {
				// 败北/退出：写回剩余血
				alive := b.aliveMonsters()
				if len(alive) > 0 {
					st.Hp = alive[0].hp
				}
				saveFamilyBossStateDB(ss.familyID, familyBossAllStates(ss.familyID))
				log.Printf("[S=%d] family boss %d fight ended, hp=%d", ch.id, bossID, st.Hp)
			}
		}
	}
	// 世界 BOSS 击杀：10010 场景战斗胜利且击杀了该层 BOSS 单位 →
	// 标记死亡 + 全服广播（BossDead 移除单位 / BossBeDefeat / 世界聊天"谁击败了 BOSS"）。
	if layer := bossLayerForMapID(b.mapID); layer > 0 {
		markAndBroadcast := func() {
			monsterID := int32(0)
			if row, ok := tables.bossBase[int64(1000+layer)]; ok {
				monsterID = int32(num(row["MonsterId"]))
			}
			for _, m := range b.monsters {
				if !m.alive && (monsterID == 0 || m.monsterID == monsterID) {
					markBossDead(layer, ch.session.name)
					s.broadcastBossKill(ch.session.name, layer)
					break
				}
			}
		}
		if b.party != nil {
			b.party.bossOnce.Do(markAndBroadcast)
		} else {
			markAndBroadcast()
		}
	}
	log.Printf("[S=%d] battle victory region=%d exp=%d coin=%d", ch.id, b.region, exp, coin)
	s.sendBattleVictory(ch, b)
	s.sendPush(ch, protocol.OpM2C_SendReward, &protocol.M2C_SendReward{
		ItemList: rewardItems, Exp: displayExp, Coin: coin, ActorId: pid,
	})
	ch.session.battle = nil
	if b.battleType == trialBattleType {
		// Party settlement emits every member's rewards first. Moving anyone here
		// would rebuild TeamComponent while another member is still in combat.
		if b.party != nil {
			return
		}
		s.advanceAfterTrialVictory(ch, b, exp, trialDrops, coin)
		return
	}
	if b.activity != nil {
		// A party is advanced once after every member has received the current
		// stage reward; solo activity stages are chained after this battle has
		// been detached so the next presentation cannot observe the old state.
		if b.party != nil {
			return
		}
		s.continueSoloActivity(ch, b)
		return
	}
	// 注意：胜利后【不】自动进入下一层——线上逻辑是玩家手动走到本层末尾的
	// 光圈（传送点）点击后经 C2M_RequestEnterMap(20031) 换层。自动切层会导致
	// 任务链（第 2 层荔枝JJ 交任务）之外的行为偏差（用户 2026-08-14 确认）。
}

// pushMonsterHP：用 20169 更新已经存在的怪物单位数值。
// 20053 是断线恢复时的整场重建消息，普通伤害发送它会重复创建同 ID 怪物。
func (s *Server) pushMonsterHP(ch *channel, m *monsterUnit) {
	if ch == nil || ch.session == nil || m == nil {
		return
	}
	hp := m.hp
	if hp < 0 {
		hp = 0
	}
	for _, attr := range []struct {
		numericType int32
		value       int32
	}{
		{numericType: 1002, value: m.maxHP},
		{numericType: 1001, value: hp},
	} {
		s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: m.id, NumericType: attr.numericType, Value: float32(attr.value), ActorId: ch.session.playerID,
		})
	}
}

// pushMonsterHPByID：按战斗怪 id 推 Numeric 血量（runtime 伤害事件用）。
func (s *Server) pushMonsterHPByID(ch *channel, battle *battleState, monsterID int64) {
	if battle == nil {
		return
	}
	for _, m := range battle.monsters {
		if m.id == monsterID {
			s.pushMonsterHP(ch, m)
			if ch != nil && ch.session != nil {
				persistFamilyBossProgress(ch.session.familyID, battle)
			}
			return
		}
	}
}

// monstersAttack：所有存活怪物各攻击玩家一次。
// 20076 MonsterPlaySkill + 20077 特效 + 20078 扣血；玩家死亡 → 20083 UnitDead + 20055 BattleDefeat。
func (s *Server) monstersAttackLegacy(ch *channel, b *battleState) {
	pid := ch.session.playerID
	for _, m := range b.monsters {
		if !m.alive {
			continue
		}
		// 怪物技能组（SkillGroupBase → 第一个技能；怪物放技能用基础技能 id）
		skillID := int32(500001)
		if mb, ok := tables.monsterBase[int64(m.monsterID)]; ok {
			if sgID := num(mb["SkillGroupId"]); sgID > 0 {
				if sg, ok := tables.skillGroup[sgID]; ok {
					if skills := arrOf(sg["SkillsArr"]); len(skills) > 0 {
						if e0, ok := skills[0].(map[string]interface{}); ok {
							if sid := num(e0["Skills_Id"]); sid > 0 {
								skillID = int32(sid)
							}
						}
					}
				}
			}
		}
		dmg := b.monsterDamage(m, skillID)
		if dmg > b.playerHP {
			dmg = b.playerHP
		}
		b.playerHP -= dmg
		s.sendPush(ch, protocol.OpM2C_MonsterPlaySkill, &protocol.M2C_MonsterPlaySkill{
			UnitId:   m.id,
			SkillId:  skillID,
			TargetId: pid,
		})
		s.sendPush(ch, protocol.OpM2C_BattleSkillRet, &protocol.M2C_BattleSkillRet{
			UnitId:        pid,
			ChangeHpValue: -dmg,
		})
		log.Printf("[S=%d] monster %d hits player dmg=%d playerHP=%d", ch.id, m.id, dmg, b.playerHP)
		if b.playerHP <= 0 {
			break
		}
	}
	if b.playerHP <= 0 {
		b.ended = true
		b.playerHP = 0
		s.sendPush(ch, protocol.OpM2C_UnitDead, &protocol.M2C_UnitDead{UnitId: pid})
		s.sendPush(ch, protocol.OpM2C_BattleDefeat, &protocol.M2C_BattleDefeat{
			BattleType: battleTypeForClient(b),
			ActorId:    pid,
		})
		ch.session.battle = nil
		ch.session.idleBattle = false
		if b.battleType == trialBattleType {
			x, y := mainCityReturnSpawn()
			s.changeMap(ch, 10004, x, y)
		}
		log.Printf("[S=%d] battle defeat region=%d", ch.id, b.region)
	}
}

// 20235 → 20236：使用主界面物品槽。实现在 mainui.go（物品槽消耗/喂宠物/刷新槽位与背包）。

// 20069 → 20070：自动战斗开关。技能选择与执行见 auto_battle.go。
func (s *Server) onAutoBattle(ch *channel, req *protocol.C2M_AutoBattle) proto.Message {
	resp := &protocol.M2C_AutoBattle{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	ch.session.battleMu.Lock()
	ch.session.autoBattleEnabled = req.IsAuto
	active := ch.session.battle != nil && !ch.session.battle.ended
	ch.session.battleMu.Unlock()
	epoch := ch.session.setAutoBattle(req.IsAuto && active)
	s.saveData(ch)
	if req.IsAuto && active {
		ch.session.battleMu.Lock()
		b := ch.session.battle
		candidates := append([]int32(nil), ch.session.autoSkills...)
		var mp, maxMP int32
		if b != nil {
			mp, maxMP = b.playerMP, b.playerMaxMP
		}
		ch.session.battleMu.Unlock()
		log.Printf("[S=%d] auto battle start savedSkills=%v mp=%d/%d epoch=%d",
			ch.id, candidates, mp, maxMP, epoch)
		go s.runAutoBattle(ch, epoch)
	}
	log.Printf("[S=%d] auto battle isAuto=%v active=%v epoch=%d", ch.id, req.IsAuto, active, epoch)
	return resp
}

// 20071 → 20072：选择敌人。后续单体技能优先命中所选目标。
// ⚠ 幂等成功：客户端 ClickBattleTargetEvent 用 Session.Call await 此请求，Error≠0 会
// 抛 RpcException 刷屏并可能拖垮客户端（Player.log 实证 "Rpc error ... C2M_SelectEnermy"）。
// 非战斗/目标不存在时静默忽略，始终返回空 Message 成功。
func (s *Server) onSelectEnermy(ch *channel, req *protocol.C2M_SelectEnermy) proto.Message {
	resp := &protocol.M2C_SelectEnermy{RpcId: req.RpcId}
	if ch == nil || ch.session == nil {
		return resp
	}
	ch.session.battleMu.Lock()
	defer ch.session.battleMu.Unlock()
	b := ch.session.battle
	if b == nil || b.ended {
		return resp // 非战斗：忽略选择
	}
	for _, m := range b.monsters {
		if m.id == req.Id && m.alive {
			b.selectedID = req.Id
			log.Printf("[S=%d] select enemy id=%d", ch.id, req.Id)
			return resp
		}
	}
	// 目标不存在/已死亡：静默成功（不打断客户端点击）
	return resp
}
