package main

import (
	"fmt"
	"log"
	"math"
	"strconv"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// ===================== datatable 配置 =====================
// 运行时由 config_store.go 从 MySQL 配置树重建这些表。

type datatables struct {
	characterGrowth     map[int64]map[string]interface{} // CharacterGrowth.json
	mainStory           map[int64]map[string]interface{} // MainStory.json
	mainStoryExp        map[int64]map[string]interface{} // MainStoryExp.json
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
	itemUpgrade         map[int64]map[string]interface{} // ItemUpgrade.json（背包物品进化）
	equipAffix          map[int64]map[string]interface{} // EquipAffixConfig.json（装备词缀）
	manulEquip          map[int64]map[string]interface{} // ManulEquip.json（手工装备配方）
	manulEquipAttribute map[int64]map[string]interface{} // ManulEquipAttribute.json（手工随机属性）
	suitConfig          map[int64]map[string]interface{} // SuitConfig.json
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

// 配置数字辅助。MySQL 配置树返回 int64/float64；单测原表读取器的
// number 类型通过 fmt.Stringer 兼容，不进入生产二进制。
func num(v interface{}) int64 {
	switch n := v.(type) {
	case fmt.Stringer:
		if i, err := strconv.ParseInt(n.String(), 10, 64); err == nil {
			return i
		}
		if f, err := strconv.ParseFloat(n.String(), 64); err == nil {
			return int64(f)
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
	case fmt.Stringer:
		if f, err := strconv.ParseFloat(n.String(), 64); err == nil {
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
	copyID         int64 // authoritative CopyConfig row used to start this fight
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
	scenePending   bool
	// 客户端自行按特效自身的 Time 销毁 EffectType=4 预制体（线上不发同场景
	// ChangeMap），服务端不记录、也不补发清理用的切图。
	// A party auto-run exits the final layer through the leader's portal path.
	// Keep every settled member in that scene until the leader moves the team.
	partyFinalReturnPending bool
	familyBossSettled       bool
	runtime                 *CombatRuntime
	effectMeta              map[string]battleEffectMetadata
	owner                   *session
	modifierHooks           map[string]*activeSkillModifier
	monsterReadyAt          time.Time
	assemblingWave          *monsterAttackWave
	party                   *partyBattle
	pvp                     *pvpBattle
	activity                *activityBattle
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

// copyVictoryAwardsMonsterDrops mirrors the original CopyRewordConfig rows
// whose IsKillMonsterGiveItem flag is true. Daily activities have dedicated
// settlement rules and are handled through battle.activity instead.
func copyVictoryAwardsMonsterDrops(copyID int64) bool {
	if tables == nil || tables.copyConfig == nil {
		return false
	}
	row := tables.copyConfig[copyID]
	switch fmt.Sprint(row["VictoryCopyReword"]) {
	case "MainStoryBattleVictory", "TrialCopyBattleVictory", "BossBattleVictory",
		"ManulEquipBattleVictory", "MainStoryIdleBattleVictory", "SpaceTravelBattleVictory":
		return true
	default:
		return false
	}
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

// 玩家战斗属性由最终六维经 CharacterGrowth 换算，并叠加直接属性加成。

// sendPush：打包并推送给客户端（opcode 为 M2C 推送消息）。
func (s *Server) sendPush(ch *channel, opcode uint16, msg proto.Message) {
	if ch == nil || ch.session == nil ||
		(ch.session.superseded.Load() && opcode != protocol.OpG2C_ForceOffLine) {
		return
	}
	if attribute, ok := msg.(*protocol.M2C_SyncUnitAttribute); ok &&
		attribute.NumericType == 1001 && attribute.UnitId == ch.session.playerID &&
		attribute.ActorId == ch.session.playerID {
		hp := int32(0)
		switch value := float64(attribute.Value); {
		case value >= float64(maxInt32Value):
			hp = int32(maxInt32Value)
		case value > 0:
			hp = int32(value)
		}
		ch.session.updateTeamHeadHP(hp)
	}
	out, err := proto.Marshal(msg)
	if err != nil {
		log.Printf("[S=%d] marshal push opcode=%d err=%v", ch.id, opcode, err)
		return
	}
	s.SendToChannel(ch, packOuter(opcode, out))
	log.Printf("[S=%d] push opcode=%d len=%d", ch.id, opcode, len(out))
}

// ===================== 角色成长 / 升级 =====================
// 角色等级仍以客户端累计值存储；属性成长使用当前转生段内等级。
// 升级经验：客户端经验条上限是 (int)(0.01*Lv³)。服务端使用 100 倍
// 定点整数，NumericType.Exp(1027) 推送时再除以 100 转回客户端单位。
//
//	expNeed(Lv) = 100 * floor(Lv³/100)
//
// 公式与 RoleGrowth.ExpArr{Exp_BaseNumber=0.01, Exp_Power=3} 一致，且不设下限：
// 线上 1-4 级门槛就是 0（见下方 expNeed 的抓包说明）。
func roleRow(jobID int32) map[string]interface{} {
	if tables == nil {
		return nil
	}
	if v, ok := tables.roleGrowth[int64(jobTypeOf(jobID))]; ok {
		return v
	}
	return tables.roleGrowth[1]
}

// expNeed：升到下一级所需经验（服务端 100 倍定点存储单位）。
//
// 客户端 CharacterUIHelper.GetMaxExpByLevel 把 0.01*Lv³ 截断成 int32 作为经验条上限，
// 服务端保留两位小数（*100）但用同一条公式截断。
//
// 不设最小值：线上 1~4 级门槛是 0。依据 2026-09-13 抓包
// （参考数据/抓包归档/online-base-attr-idle-20260913.pcapng、online-lv1-idle-20260913.pcapng）
// 的四个整分钟节拍点——每跳 24560 显示经验，1 级新角色第 1/2/3/4 跳后分别是
// 56/67/74/79 级、余 868/264/757/3348。用 Σ_{L=1}^{升级前等级-1} expNeed(L) 反推：
//
//	Lv56 23692、Lv67 48856、Lv74 72923、Lv79 94892 全部 = 跳数*24560 - 余数
//
// 只有 floor(Lv³/100) 且不设下限才同时满足这 4 点：round() 每点都偏大（Lv56 偏 24），
// max(1, …) 每点都偏大 4（即 1~4 级门槛被抬成 1）。RoleGrowth.ExpArr 的
// Exp_BaseNumber=0.01/Exp_Power=3 也印证这条 0.01·Lv³ 公式。
func expNeed(level int32) int64 {
	if level < 1 {
		level = 1
	}
	l := int64(level)
	return l * l * l / 100 * 100
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
		// NumericWatcher_Level reads the current transmigration value when 1026 arrives.
		// Send 1029 first so cumulative levels such as 17004 render as 2·4004.
		{1029, float32(ss.trans)}, // 转生等级（19 文档 §3.4：参与等级文案）
		{1026, float32(ss.level)},
		{1027, float32(ss.exp) / 100},
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
	currentHP, currentMP := clampPlayerCurrentResources(ss)
	s.pushPlayerAttrsTo(ch, ss, currentHP, currentMP)
}

func clampPlayerCurrentResources(ss *session) (currentHP, currentMP int32) {
	if ss == nil {
		return 0, 0
	}
	currentHP, currentMP = ss.battleHP(), ss.battleMP()
	maxHP, maxMP := ss.playerMaxHp(), ss.playerMaxMp()
	if ss.battle != nil {
		ss.battle.playerMaxHP = maxHP
		ss.battle.playerMaxMP = maxMP
	}
	ss.setBattleHP(currentHP)
	ss.setBattleMP(currentMP)
	return ss.battleHP(), ss.battleMP()
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
	visitPlayerNumericSnapshot(ss, currentHP, currentMP, func(numericType int32, value float32) {
		s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: ss.playerID, NumericType: numericType, Value: value, ActorId: ch.session.playerID,
		})
	})
}

// pushBattlePlayerAttrsListTo sends the narrow player snapshot observed during
// the captured family-boss fight. This is deliberately separate from the full
// login snapshot: the capture did not include a login flow.
func (s *Server) pushBattlePlayerAttrsListTo(ch *channel, ss *session, currentHP, currentMP int32) {
	if ch == nil || ch.session == nil || ss == nil || ss.playerID == 0 {
		return
	}
	attributes := collectBattlePlayerNumericSnapshot(ss, currentHP, currentMP)
	base, err := proto.Marshal(&protocol.M2C_SyncUnitAttributeList{
		UnitId: ss.playerID, ActorId: ch.session.playerID,
	})
	if err != nil {
		log.Printf("[S=%d] marshal player numeric snapshot player=%d err=%v", ch.id, ss.playerID, err)
		return
	}
	body := appendAttributeMapList(base, 2, attributes)
	s.SendToChannel(ch, packOuter(protocol.OpM2C_SyncUnitAttributeList, body))
}

func collectBattlePlayerNumericSnapshot(ss *session, currentHP, currentMP int32) []unitNumericAttribute {
	if ss == nil {
		return nil
	}
	maxHP, maxMP := ss.playerMaxHp(), ss.playerMaxMp()
	if currentHP < 0 || currentHP > maxHP {
		currentHP = maxHP
	}
	if currentMP < 0 || currentMP > maxMP {
		currentMP = maxMP
	}
	ss.storeTeamHeadHP(currentHP, maxHP)
	return []unitNumericAttribute{
		{numericType: 1001, value: float32(currentHP)},
		{numericType: 1003, value: float32(currentMP)},
		{numericType: 1009, value: float32(ss.playerPhyAtk())},
		{numericType: 1010, value: float32(ss.playerSpiAtk())},
		{numericType: 1011, value: float32(ss.playerPhyDef())},
		{numericType: 1012, value: float32(ss.playerSpiDef())},
		{numericType: 1013, value: float32(ss.playerExtraNumeric(1013))},
		{numericType: 1015, value: float32(ss.playerExtraNumeric(1015))},
		{numericType: 1016, value: float32(ss.playerExtraNumeric(1016))},
	}
}

func visitPlayerNumericSnapshot(ss *session, currentHP, currentMP int32, visit func(int32, float32)) {
	mhp := ss.playerMaxHp()
	mp := ss.playerMaxMp()
	if currentHP < 0 || currentHP > mhp {
		currentHP = mhp
	}
	if currentMP < 0 || currentMP > mp {
		currentMP = mp
	}
	ss.storeTeamHeadHP(currentHP, mhp)
	push := visit
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
	// 面板完整属性（CharacterUI.ShowNumText 读取）：暴击/辅助值/减伤/速度/命中/抵抗等
	// （NumericType 1013-1023、1031-1035、1042-1046）。equipBonus 已算出 →
	// 非零项逐条推送（否则面板这些行恒 0，穿戴带这些属性的装备不显示）。
	//
	// ⚠ 1017 在 ET.NumericType 里叫 `Dvo`，容易被读成「闪避」，但它**不是闪避**：
	// Cal.AttributeType 把同一个槽位叫「辅助值」，装备 tooltip 也显示「辅助值」，
	// 本服务端正是把它当治疗加成读（battle_treatment.go 的 CombatAttributeAuxiliary）。
	// 装备表里它的取值是 0.005–3.8，当闪避意味着最高 380%，明显不合理。
	// 客户端枚举里没有闪避成员，服务端也没有任何闪避判定。
	// Push zeroes as well so taking equipment off clears a previously non-zero
	// rate in NumericComponent instead of leaving stale panel/combat values.
	for _, t := range []int32{
		1013, 1014, 1015, 1016, 1017, 1018, 1019, 1020, 1021, 1022, 1023,
		1031, 1032, 1033, 1034, 1035, 1042, 1043, 1044, 1045, 1046,
	} {
		push(t, float32(ss.playerExtraNumeric(t)))
	}
	// Level is the only watched field; its handler reads Transmigration to format
	// the cumulative level, so 1029 must already be present in NumericComponent.
	push(1029, float32(ss.trans))
	push(1026, float32(ss.level))
	push(1027, float32(ss.exp)/100)
	push(1038, float32(ss.titleID))
	push(1039, float32(ss.energy))
}

type unitNumericAttribute struct {
	numericType int32
	value       float32
}

func collectPlayerNumericSnapshot(ss *session, currentHP, currentMP int32) []unitNumericAttribute {
	attributes := make([]unitNumericAttribute, 0, 40)
	visitPlayerNumericSnapshot(ss, currentHP, currentMP, func(numericType int32, value float32) {
		attributes = append(attributes, unitNumericAttribute{numericType: numericType, value: value})
	})
	return attributes
}

// pushInitialPlayerAttrs matches the online login snapshot: one 20170 packet
// after map creation, including progress and currencies. Transmigration is
// placed before Level because NumericWatcher_Level reads 1029 while handling
// the 1026 entry.
func (s *Server) pushInitialPlayerAttrs(ch *channel) {
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		return
	}
	ss := ch.session
	attributes := collectPlayerNumericSnapshot(ss, ss.battleHP(), ss.battleMP())
	attributes = append(attributes,
		unitNumericAttribute{numericType: ntCoin, value: float32(ss.coin)},
		unitNumericAttribute{numericType: ntYuanBao, value: float32(ss.yuanBao)},
		unitNumericAttribute{numericType: ntVoucher, value: float32(ss.voucher)},
		unitNumericAttribute{numericType: ntStarCoin, value: float32(starCoinBalance(ss))},
		unitNumericAttribute{numericType: ntHonor, value: float32(ss.honor)},
		unitNumericAttribute{numericType: ntPVPCurrency, value: float32(ss.pvpCurrency)},
		unitNumericAttribute{numericType: ntFamilyContribution, value: float32(ss.familyContribute)},
	)
	base, err := proto.Marshal(&protocol.M2C_SyncUnitAttributeList{
		UnitId: ss.playerID, ActorId: ss.playerID,
	})
	if err != nil {
		log.Printf("[S=%d] marshal initial numeric snapshot err=%v", ch.id, err)
		return
	}
	body := appendAttributeMapList(base, 2, attributes)
	s.SendToChannel(ch, packOuter(protocol.OpM2C_SyncUnitAttributeList, body))
	log.Printf("[S=%d] push initial numeric snapshot count=%d", ch.id, len(attributes))
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
// Gameplay 基础倍率先结算，经验卡 buff（expMult>1 且未到期）再叠乘。
func experienceAfterMultiplier(ss *session, exp int64) int64 {
	if ss == nil || exp <= 0 {
		return exp
	}
	exp = scaleExperience(exp, gameplayExperienceGainMultiplier())
	now := time.Now().UnixMilli()
	category := itemBuffBattleExp
	if ss.idleBattle {
		category = itemBuffIdleExp
	}
	ss.itemBuffMu.Lock()
	defer ss.itemBuffMu.Unlock()
	if buff := ss.itemBuffs[category]; buff != nil && buff.ExpiresAt > now && buff.Multiplier > 1 {
		return scaleExperience(exp, buff.Multiplier)
	}
	// Compatibility for focused tests and sessions created before itemBuffs is loaded.
	if !ss.idleBattle && ss.expMult > 1 && ss.expMultUntil > now {
		return scaleExperience(exp, ss.expMult)
	}
	return exp
}

func scaleExperience(exp int64, multiplier float64) int64 {
	if exp <= 0 || multiplier <= 0 {
		return 0
	}
	scaled := float64(exp) * multiplier
	if scaled >= float64(math.MaxInt64) {
		return math.MaxInt64
	}
	result := int64(scaled)
	if result < 1 {
		return 1
	}
	return result
}

func progressionPointsForLevels(fromLevel, toLevel, trans, interval int32) (charPoints, skillPoints int32) {
	if toLevel <= fromLevel {
		return 0, 0
	}
	fromStage := stageLevel(fromLevel, trans)
	toStage := stageLevel(toLevel, trans)
	charPoints = toStage/4 - fromStage/4
	if interval > 0 {
		skillPoints = toLevel/interval - fromLevel/interval
	}
	return
}

func grantProgressionPoints(ss *session, fromLevel, toLevel int32) {
	if ss == nil {
		return
	}
	charPoints, skillPoints := progressionPointsForLevels(fromLevel, toLevel, ss.trans, gameplaySkillPointLevels())
	if charPoints != 0 {
		ss.charPoint = saturatingAddInt32(ss.charPoint, charPoints)
	}
	if skillPoints != 0 {
		ss.skillPoint = saturatingAddInt32(ss.skillPoint, skillPoints)
	}
}

func (s *Server) gainExp(ch *channel, exp int64) int64 {
	var ss *session
	if ch != nil {
		ss = ch.session
	}
	return s.gainExpAmount(ch, experienceAfterMultiplier(ss, exp))
}

// gainExpAmount：按已经确定的数值结算经验，不再叠乘任何倍率。
// 主城挂机经验走这个入口：线上那一跳下发的就是最终值（24560），
// 不能再乘 progression.experience_gain_percent。
func (s *Server) gainExpAmount(ch *channel, exp int64) int64 {
	if ch == nil {
		return 0
	}
	ss := ch.session
	if ss == nil || exp <= 0 {
		return 0
	}
	if ss.exp > math.MaxInt64-exp {
		ss.exp = math.MaxInt64
	} else {
		ss.exp += exp
	}
	upgraded := false
	levelCap := transLevelCap(ss.trans)
	if levelCap > maxPlayerLevel {
		levelCap = maxPlayerLevel
	}
	fromLevel := ss.level
	for ss.exp >= expNeed(ss.level) && ss.level < levelCap {
		ss.exp -= expNeed(ss.level)
		ss.level++
		upgraded = true
	}
	if upgraded {
		grantProgressionPoints(ss, fromLevel, ss.level)
		s.pushPlayerAttrs(ch)
		s.pushUnitCharacter(ch)
		log.Printf("[S=%d] level up -> %d exp=%d charPoint=%d skillPoint=%d", ch.id, ss.level, ss.exp, ss.charPoint, ss.skillPoint)
	} else {
		s.pushPlayerProgress(ch)
	}
	s.saveData(ch)
	log.Printf("[S=%d] gain exp=%d total=%d level=%d", ch.id, exp, ss.exp, ss.level)
	return exp
}

// onAddPoint：20253 → 20254。属性点分配。
// 客户端（CharacterUI b__16_0，HotfixView 1060 反汇编）发送 C2M_AddPoint.PointList。
// 六行固定顺序为力量/智慧/体质/耐力/敏捷/精神，投入值原样扣除并 1:1 加到基础属性；
// 客户端不设 Trans。响应必须带 Character
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
	if len(req.PointList) != 6 {
		resp.Message = "属性点参数错误"
		resp.Character = buildUnitCharacter(ss)
		return resp
	}
	var sum int64
	for _, n := range req.PointList {
		if n < 0 {
			resp.Message = "点数无效"
			resp.Character = buildUnitCharacter(ss)
			return resp
		}
		sum += int64(n)
	}
	if sum <= 0 || sum > int64(ss.charPoint) {
		resp.Message = "没有可用属性点"
		resp.Character = buildUnitCharacter(ss)
		return resp
	}
	ss.strAdd += req.PointList[0]
	ss.wimAdd += req.PointList[1]
	ss.phyAdd += req.PointList[2]
	ss.staAdd += req.PointList[3]
	ss.qukAdd += req.PointList[4]
	ss.spiAdd += req.PointList[5]
	ss.charPoint -= int32(sum)
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
	ss.charPoint += ss.strAdd + ss.wimAdd + ss.phyAdd + ss.staAdd + ss.qukAdd + ss.spiAdd
	ss.strAdd, ss.wimAdd, ss.phyAdd, ss.staAdd, ss.qukAdd, ss.spiAdd = 0, 0, 0, 0, 0, 0
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

// startMainStoryFightByRegionWithReason keeps the normal two-value entry point
// compatible with interactive callers while exposing the actionable failure
// reason needed by the unattended main-story worker.
func (s *Server) startMainStoryFightByRegionWithReason(ch *channel, region int32) (bool, int, string) {
	ok, count := s.startMainStoryFightByRegion(ch, region)
	if ok {
		return true, count, ""
	}
	if ch == nil || ch.session == nil {
		return false, count, ""
	}
	presentation := battlePresentation{kind: presentationMainStory}
	copyID := battleCopyConfig(region, 0, presentation)
	participants := s.battleParticipantsForPresentation(ch, copyID, presentation)
	if message := battleEntryHealthFailure(ch, participants); message != "" {
		return false, count, message
	}
	_, message := s.mainStoryAIEnergyFailure(ch, region)
	return false, count, message
}

// startMainStoryEncounter starts one of the two native field encounters. The
// client sends MonsterInfo.Region (0 or 1), while the current map determines
// the MainStory row. Both field actors are built from Monster_1Arr[0]; Region
// is a click slot and must never be interpreted as a MainStory table id.
func (s *Server) startMainStoryEncounter(ch *channel, region, slot int32) (bool, int) {
	if ch == nil || ch.session == nil || tables == nil || slot < 0 || slot > 1 {
		return false, 0
	}
	partyStartMu.Lock()
	defer partyStartMu.Unlock()
	ch.session.battleMu.Lock()
	defer ch.session.battleMu.Unlock()

	if ch.session.battle != nil {
		return false, 0
	}
	row := tables.mainStory[int64(region)]
	if row == nil {
		return false, 0
	}
	entries := arrOf(row["Monster_1Arr"])
	if len(entries) == 0 {
		return false, 0
	}
	entry, _ := entries[0].(map[string]interface{})
	monsterID := int32(num(entry["Monster_1_Id"]))
	count := int(num(entry["Monster_1_Count"]))
	if monsterID <= 0 || count <= 0 {
		return false, 0
	}
	units := ch.session.buildMonsterUnitsFromRoster([]int32{monsterID}, []int{count})
	if len(units) == 0 {
		return false, 0
	}
	return s.finishStartBattleWithPresentation(ch, region, units, 0, battlePresentation{kind: presentationMainStory})
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
	kind                       battlePresentationKind
	familyBossID               int32
	deferFamilyBossForPlayerID int64
	manualConfigIDs            []int32
	copyID                     int64
	activity                   *activityBattle
	skipEnergy                 bool
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
func (s *Server) battleParticipantsForPresentation(ch *channel, copyID int64, presentation battlePresentation) []*channel {
	// TrialCopyBattle is explicitly CanTeam=false in the online CopyConfig.
	// Each player owns a private encounter even when party members happen to be
	// on the same trial layer.
	if presentation.kind == presentationTrial {
		return []*channel{ch}
	}
	participants := s.configuredBattleParticipants(ch, copyID, false)
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
	return participants
}

func (s *Server) finishStartBattleWithPresentation(ch *channel, region int32, units []*monsterUnit, mapID int32, presentation battlePresentation) (bool, int) {
	if ch == nil || ch.session == nil || len(units) == 0 {
		return false, 0
	}
	copyID := battleCopyConfig(region, mapID, presentation)
	participants := s.battleParticipantsForPresentation(ch, copyID, presentation)
	for _, member := range participants {
		if member != nil && member.session != nil && s.hasPendingActivityStart(member.session.playerID) {
			log.Printf("[S=%d] reject battle copy=%d: activity scene is still loading", ch.id, copyID)
			return false, 0
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
	quota := dailyDungeonQuotaForBattle(copyID, presentation)
	quotaNow := time.Now()
	if message := s.dailyDungeonQuotaFailure(ch, participants, quota, quotaNow); message != "" {
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
		presentation.kind == presentationWorldBoss) {
		scaleMonsterHPForTeam(units, len(participants))
	}
	applyFamilyBossOpeningMultiplier(units, presentation)

	// Native client battle events synchronously enumerate TeamComponent and
	// NumericComponent. Refresh both before any participant receives the event.
	s.preparePartyBattleClients(participants, ch.session.playerID)

	autoEnabled := make(map[int64]bool, len(participants))
	for _, member := range participants {
		ss := member.session
		autoEnabled[ss.playerID] = ss.autoBattleEnabled
		ss.autoBattle = false
	}

	battleStartedAt := time.Now()
	for _, member := range participants {
		started, _ := s.finishStartSingleBattleWithPresentationAt(
			member, region, units, mapID, presentation, battleStartedAt,
		)
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
	if !s.consumeDailyDungeonQuotas(participants, quota, quotaNow) {
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
		log.Printf("[S=%d] reject battle copy=%d: daily quota changed during start", ch.id, copyID)
		return false, 0
	}
	if quota != dailyDungeonQuotaNone {
		for _, member := range participants {
			s.saveData(member)
		}
	}

	if len(participants) > 1 {
		party := &partyBattle{
			members:        make(map[int64]*battleState, len(participants)),
			memberIDs:      make([]int64, 0, len(participants)),
			monsterReadyAt: participants[0].session.battle.monsterReadyAt,
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
		ss.autoBattleNextCastAt = ss.battleActionReadyAt
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
	message := ""
	for _, member := range participants {
		if member == nil || member.session == nil || member.session.battleHP() > 0 {
			continue
		}
		if origin != nil && origin.session != nil && member.session.playerID != origin.session.playerID {
			name := member.session.name
			if name == "" {
				name = fmt.Sprintf("玩家%d", member.session.playerID)
			}
			message = fmt.Sprintf("队员%s生命值不足，请先恢复", name)
			break
		}
		message = battleEntryHealthMessage
		break
	}
	if message != "" {
		for _, member := range participants {
			if member == nil || member.session == nil {
				continue
			}
			sid := int64(0)
			if origin != nil {
				sid = origin.id
			}
			logCombatHP(member.session, "reject", fmt.Sprintf("S=%d memberS=%d message=%q", sid, member.id, message))
		}
	}
	return message
}

func (s *Server) finishStartSingleBattleWithPresentation(ch *channel, region int32, units []*monsterUnit, mapID int32, presentation battlePresentation) (bool, int) {
	return s.finishStartSingleBattleWithPresentationAt(ch, region, units, mapID, presentation, time.Now())
}

func (s *Server) finishStartSingleBattleWithPresentationAt(ch *channel, region int32, units []*monsterUnit, mapID int32, presentation battlePresentation, battleStartedAt time.Time) (bool, int) {
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
		copyID:      copyID,
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
	initializeBattleOpeningCadence(ss, battle, battleStartedAt)
	battle.runtime = NewCombatRuntime(battle, pid, nil)
	battle.runtime.initializeFamilyBossCombatScaling(presentation, battleStartedAt)
	ch.session.battle = battle
	if presentation.kind == presentationFamilyBoss {
		recordFamilyBossParticipation(ss.familyID, presentation.familyBossID, pid)
	}
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
		if presentation.deferFamilyBossForPlayerID != pid {
			s.pushFamilyBossPresentation(ch, presentation.familyBossID, boss, presentation.deferFamilyBossForPlayerID != 0)
		}
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
		s.pushMonsterOpeningHP(ch, units)
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
		s.pushMonsterOpeningHP(ch, units)
	}
	// 玩家单位在所有战斗类型里都是已有单位。开战时把权威当前/最大资源重发一次，
	// 避免客户端显示满蓝、服务端却因上一场剩余 MP 判定自动技能不可释放。
	s.pushBattleResources(ch, battle)
	s.emitCombatEvents(ch, battle, passiveEvents)

	// 自动战斗常驻：用户开着自动战斗开关 → 新战斗立即重新拉起 worker
	// （autoBattleStep/settleCombatLocked 胜利后只失效 worker 不关开关；
	// 这里在锁内递增 epoch 并启动新 worker，旧 worker 观察 epoch 不符退出）。
	logCombatHP(ss, "start", fmt.Sprintf("S=%d region=%d map=%d copy=%d snapshot=%d/%d atk=%d/%d def=%d/%d %s",
		ch.id, region, mapID, battle.copyID, battle.playerHP, battle.playerMaxHP,
		battle.phyAtk, battle.spiAtk, battle.phyDef, battle.spiDef, combatMonsterSummary(units)))
	if ss.autoBattle {
		ss.autoBattleEpoch++
		ss.autoBattleNextCastAt = ss.battleActionReadyAt
		ss.autoBattleSkillCursor = 0
		go s.runAutoBattle(ch, ss.autoBattleEpoch)
		log.Printf("[S=%d] auto battle resumed for new fight region=%d", ch.id, region)
	}
	return true, len(units)
}

// pushFamilyBossPresentation starts the client's native family-boss scene.
// Both 20144 and 20145 rebuild the Boss unit and HUD; they are not per-hit HP
// messages. The initiating client receives 20144 after its RPC response, while
// team members enrolled by that request receive the one-time 20145 resend.
func (s *Server) pushFamilyBossPresentation(ch *channel, bossID int32, boss *monsterUnit, resend bool) {
	if ch == nil || ch.session == nil || boss == nil || bossID <= 0 {
		return
	}
	if resend {
		s.sendPush(ch, protocol.OpM2C_ReSendFamilyBossInfo, &protocol.M2C_ReSendFamilyBossInfo{
			BossId: bossID, UnitId: boss.id, Hp: maxInt32(0, boss.hp), ActorId: ch.session.playerID,
		})
	} else {
		s.sendPush(ch, protocol.OpM2C_SendFamilyBossInfo, &protocol.M2C_SendFamilyBossInfo{
			BossId: bossID, UnitId: boss.id, Hp: maxInt32(0, boss.hp), ActorId: ch.session.playerID,
		})
	}
	s.pushFamilyBossOpeningAttributes(ch, ch.session.battle, boss)
}

// pushStartedFamilyBossPresentation runs only after M2C_StartFamilyBossFight
// has been written, so the request UI continuation cannot race the async scene
// builder that consumes 20144.
func (s *Server) pushStartedFamilyBossPresentation(ch *channel) {
	if ch == nil || ch.session == nil {
		return
	}
	ss := ch.session
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	battle := ss.battle
	if battle == nil || battle.ended || battle.mapID >= 0 || len(battle.monsters) != 1 {
		return
	}
	s.pushFamilyBossPresentation(ch, -battle.mapID, battle.monsters[0], false)
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
