package main

import (
	"math"

	"mhqserver/protocol"
)

// RoleGrowth's quadratic fields are retained as offline reverse-engineering
// helpers. Online character captures show that runtime combat attributes use
// CharacterGrowth conversions instead of adding these fields to final stats.
// 升级经验：客户端 GetMaxExpByLevel = (int)(0.01*Lv³)；服务端以 100 倍
// 定点整数判定，即 expNeed(Lv) = 100*floor(Lv³/100)（线上 1~4 级门槛为 0，
// 见 battle.go 的抓包说明），推送 NumericType.Exp 时再除以 100。

// growAt：X1 + X2*Lv + X3*Lv²/100 → int 截断。
func growAt(r map[string]interface{}, k1, k2, k3 string, lv int32) int32 {
	v := growFloatAt(r, k1, k2, k3, lv)
	if v >= float64(maxInt32Value) {
		return int32(maxInt32Value)
	}
	if v <= float64(minInt32Value) {
		return int32(minInt32Value)
	}
	return int32(v)
}

func growFloatAt(r map[string]interface{}, k1, k2, k3 string, lv int32) float64 {
	l := float64(lv)
	return numf(r[k1]) + numf(r[k2])*l + numf(r[k3])*l*l/100
}

const (
	maxInt32Value = int64(1<<31 - 1)
	minInt32Value = int64(-1 << 31)
)

// clampInt64ToInt32 and clampFloat64ToInt32 are the single conversion
// boundary for gameplay values that eventually live in the protocol/session
// int32 resource fields.  A direct Go conversion from an out-of-range value
// is implementation-dependent and, on the potion path, can turn a full
// health restore into a negative/zero value before the normal max clamp runs.
func clampInt64ToInt32(value int64) int32 {
	if value >= maxInt32Value {
		return int32(maxInt32Value)
	}
	if value <= minInt32Value {
		return int32(minInt32Value)
	}
	return int32(value)
}

func clampFloat64ToInt32(value float64) int32 {
	if math.IsNaN(value) {
		return 0
	}
	if value >= float64(maxInt32Value) {
		return int32(maxInt32Value)
	}
	if value <= float64(minInt32Value) {
		return int32(minInt32Value)
	}
	return int32(value)
}

func saturatingAddInt32(left, right int32) int32 {
	return clampInt64ToInt32(int64(left) + int64(right))
}

func statSum(values ...int32) int32 {
	var total int64
	for _, value := range values {
		total += int64(value)
	}
	if total > maxInt32Value {
		return int32(maxInt32Value)
	}
	if total < minInt32Value {
		return int32(minInt32Value)
	}
	return int32(total)
}

// ===================== 装备属性（EquipBase.json → NumericType） =====================
// EquipBase 字段 → NumericType（Unity.Model.dll NumericType 枚举，见 11 文档）：
//
//	Hp→1002 MaxHp, Mp→1004 MaxMp, Str→1005, Quk→1006, Spi→1007, Wim→1008,
//	PhyAtk→1009, SpiAtk→1010, PhyDef→1011, SpiDef→1012,
//	Pcrir→1013, Mcrir→1014, Pcri→1015, Mcri→1016, Dvo→1017,
//	Rpcrir→1018, Rpcri→1019, Rmcrir→1020, Rmcri→1021, Nphyi→1022, Nmeni→1023,
//	Spd→1031, Hit→1032, Res→1033, Phy→1034, Sta→1035,
//	SuckR→1042, SuckV→1043, HpRecover→1044, PhyDA→1045, MicDA→1046。
var equipFieldToNumeric = map[string]int32{
	"Hp": 1002, "Mp": 1004,
	"Str": 1005, "Quk": 1006, "Spi": 1007, "Wim": 1008,
	"PhyAtk": 1009, "SpiAtk": 1010, "PhyDef": 1011, "SpiDef": 1012,
	"Pcrir": 1013, "Mcrir": 1014, "Pcri": 1015, "Mcri": 1016, "Dvo": 1017,
	"Rpcrir": 1018, "Rpcri": 1019, "Rmcrir": 1020, "Rmcri": 1021,
	"Nphyi": 1022, "Nmeni": 1023,
	"Spd": 1031, "Hit": 1032, "Res": 1033, "Phy": 1034, "Sta": 1035,
	"SuckR": 1042, "SuckV": 1043, "HpRecover": 1044, "PhyDA": 1045, "MicDA": 1046,
}

// equipBonus：汇总已穿戴装备的属性加成（NumericType → 总值）。
// 含装备基础属性（EquipBase）与已镶嵌宝石属性（MaterialBase.GemKey/GemValue）。
func equipBonus(ss *session) map[int32]float32 {
	out := make(map[int32]float32)
	if tables == nil || ss == nil {
		return out
	}
	for _, it := range ss.worn {
		if it == nil || it.ItemType != 1 {
			continue
		}
		eb, ok := tables.equipBase[int64(it.ItemId)]
		if !ok {
			continue
		}
		for f, nt := range equipFieldToNumeric {
			out[nt] += float32(numf(eb[f]))
		}
		// The client displays the complete strengthened special attribute as:
		// SpecialValue * (1 + Strengthentable[level].AttribteAdd).
		if numeric, ok := gemKeyToNumeric[int32(num(eb["SpecialKey"]))]; ok {
			factor := 1.0
			if row := tables.strengthen[int64(it.Level)]; row != nil {
				factor += numf(row["AttribteAdd"])
			}
			out[numeric] += float32(numf(eb["SpecialValue"]) * factor)
		}
		// The client treats mainAttribute.Value as a percentage delta and shows
		// templateValue * (1 + delta). The template value was added above, so
		// only templateValue * delta belongs in the additional server bonus.
		for key, value := range it.MainAttr {
			if nt, ok := gemKeyToNumeric[key]; ok {
				out[nt] += float32(equipTemplateValueForAttribute(eb, key)) * value
			}
		}
		// 手工装备随机属性（ManulEquipAttribute id → Key/Value）。
		for _, id := range it.RandomAttrs {
			row := tables.manulEquipAttribute[int64(id)]
			if row == nil {
				continue
			}
			if nt, ok := gemKeyToNumeric[int32(num(row["Key"]))]; ok {
				out[nt] += float32(numf(row["Value"]))
			}
		}
		// 洗练词缀（每行可能包含一条或组合属性）。
		for _, id := range it.AddAttrs {
			row := tables.equipAffix[int64(id)]
			if row == nil {
				continue
			}
			for _, raw := range arrOf(row["AffixArr"]) {
				affix, _ := raw.(map[string]interface{})
				if affix == nil {
					continue
				}
				if nt, ok := gemKeyToNumeric[int32(num(affix["Key"]))]; ok {
					out[nt] += float32(numf(affix["Value"]))
				}
			}
		}
		// 宝石属性：GemKey=AttributeType（1-31）→ NumericType（gemKeyToNumeric），
		// GemValue=数值。宝石显示/战斗统一走同一映射（与 14 文档 §4.9 一致）。
		for _, gemID := range it.GemList {
			if gemID <= 0 {
				continue
			}
			mrow, ok := tables.materialBase[int64(gemID)]
			if !ok {
				continue
			}
			key := int32(num(mrow["GemKey"]))
			val := float32(numf(mrow["GemValue"]))
			nt, ok := gemKeyToNumeric[key]
			if ok {
				out[nt] += val
			}
		}
	}
	addEquipSuitBonus(out, ss)
	return out
}

func addEquipSuitBonus(out map[int32]float32, ss *session) {
	if ss == nil || tables == nil || len(tables.suitConfig) == 0 {
		return
	}
	wornIDs := make(map[int32]struct{}, len(ss.worn))
	for _, item := range ss.worn {
		if item != nil && item.ItemType == 1 {
			wornIDs[item.ItemId] = struct{}{}
		}
	}
	profession := jobTypeOf(ss.jobID)
	for _, suit := range tables.suitConfig {
		for _, rawEquip := range arrOf(suit["EquipArr"]) {
			equip, _ := rawEquip.(map[string]interface{})
			if equip == nil || int32(num(equip["Job"])) != profession {
				continue
			}
			required := arrOf(equip["EquipIdArr"])
			complete := len(required) > 0
			for _, rawID := range required {
				if _, ok := wornIDs[int32(num(rawID))]; !ok {
					complete = false
					break
				}
			}
			if complete {
				for _, rawAttribute := range arrOf(suit["SuitArr"]) {
					attribute, _ := rawAttribute.(map[string]interface{})
					if attribute == nil {
						continue
					}
					if numeric, ok := gemKeyToNumeric[int32(num(attribute["Key"]))]; ok {
						out[numeric] += float32(numf(attribute["Value"]))
					}
				}
			}
			break
		}
	}
}

func equipTemplateValueForAttribute(equipBase map[string]interface{}, key int32) float64 {
	for _, attribute := range equipFieldToAttributeType {
		if attribute.key == key {
			if value := numf(equipBase[attribute.field]); value != 0 {
				return value
			}
			break
		}
	}
	if int32(num(equipBase["SpecialKey"])) == key {
		return numf(equipBase["SpecialValue"])
	}
	return 0
}

func roundedEquipBonus(ss *session, numeric int32) int32 {
	value := math.Round(float64(equipBonus(ss)[numeric]))
	if value >= float64(maxInt32Value) {
		return int32(maxInt32Value)
	}
	if value <= float64(minInt32Value) {
		return int32(minInt32Value)
	}
	return int32(value)
}

func petAttributeBonus(ss *session) map[int32]float64 {
	out := make(map[int32]float64)
	if ss == nil || ss.pet == nil || tables == nil || tables.petConfig == nil {
		return out
	}
	row := tables.petConfig[int64(ss.pet.PetId)]
	for _, raw := range arrOf(row["AddAttibuteMapArr"]) {
		attribute, _ := raw.(map[string]interface{})
		if attribute == nil {
			continue
		}
		numeric, ok := gemKeyToNumeric[int32(num(attribute["Key"]))]
		if !ok {
			continue
		}
		out[numeric] += numf(attribute["BaseValue"]) + numf(attribute["Value"])*float64(ss.pet.Level)
	}
	return out
}

// directNumericBonus is the flat value supplied by equipment, star souls,
// transmigration and the current pet. CharacterGrowth conversions are excluded
// so primary attributes remain non-recursive formula inputs.
func directNumericBonus(ss *session, numeric int32) float64 {
	if ss == nil {
		return 0
	}
	return float64(equipBonus(ss)[numeric]) +
		starSoulBonus(ss)[numeric] +
		float64(ss.transBonus[numeric]) +
		petAttributeBonus(ss)[numeric]
}

func roundedDirectNumericBonus(ss *session, numeric int32) int32 {
	value := math.Round(directNumericBonus(ss, numeric))
	if value >= float64(maxInt32Value) {
		return int32(maxInt32Value)
	}
	if value <= float64(minInt32Value) {
		return int32(minInt32Value)
	}
	return int32(value)
}

// CharacterGrowth rows are keyed by AttributeType*10+profession. They convert
// final primary attributes into the derived values sent by the online
// server, for example officer strength into physical attack.
func characterGrowthValue(ss *session, attributeType int32) float64 {
	if ss == nil || tables == nil || tables.characterGrowth == nil {
		return 0
	}
	row := tables.characterGrowth[int64(attributeType*10+jobTypeOf(ss.jobID))]
	if row == nil {
		return 0
	}
	inputs := map[string]float64{
		"Str": float64(ss.playerStr()),
		"Quk": float64(ss.playerQuk()),
		"Spi": float64(ss.playerSpi()),
		"Wim": float64(ss.playerWim()),
		"Phy": float64(ss.playerPhy()),
		"Sta": float64(ss.playerSta()),
	}
	var value float64
	for field, input := range inputs {
		value += numf(row[field]) * input
	}
	return value
}

func roundedCharacterGrowthValue(ss *session, attributeType int32) int32 {
	value := math.Round(characterGrowthValue(ss, attributeType))
	if value >= float64(maxInt32Value) {
		return int32(maxInt32Value)
	}
	if value <= float64(minInt32Value) {
		return int32(minInt32Value)
	}
	return int32(value)
}

// gemKeyToNumeric：客户端 Cal.AttributeType（1-31）→ NumericType（20169 推送键）。
var gemKeyToNumeric = map[int32]int32{
	1: 1002, 2: 1004, 3: 1005, 4: 1006, 5: 1007, 6: 1008,
	7: 1009, 8: 1010, 9: 1011, 10: 1012,
	11: 1013, 12: 1014, 13: 1015, 14: 1016,
	15: 1018, 16: 1020, 17: 1019, 18: 1021, 19: 1017,
	20: 1034, 21: 1035, 22: 1022, 23: 1023, 24: 1031, 25: 1032, 26: 1033,
	27: 1042, 28: 1043, 29: 1044, 30: 1045, 31: 1046,
}

// ===================== 玩家最终属性 =====================

// naturalPrimaryStat：六维基值 = ⌈当前转生阶段等级 / 10⌉（1 级就是 1 点）。
// 依据 2026-09-13 线上抓包（参考数据/抓包归档/online-base-attr-idle-20260913.pcapng、
// online-lv1-idle-20260913.pcapng，20170 推送的 1005/1006/1007/1008 原值）：
// Lv1→1、Lv56→6、Lv67→7、Lv74→8，精神/体质/耐力 再 +12（本地由转生/装备加成给出）。
// 线上 Lv1 面板 力量=1 而 round(Lv/10) 给 0、Lv74 线上 8 而 round 给 7，故不能取整；
// ⌊Lv/10⌋+1 在整十级（8500→851）与线上转生角色截图（8500→850）冲突，同样排除。
func (ss *session) naturalPrimaryStat() int32 {
	if ss == nil {
		return 0
	}
	return clampFloat64ToInt32(math.Ceil(float64(stageLevel(ss.level, ss.trans)) / 10))
}

func (ss *session) playerMaxHp() int32 {
	return scaleStat(
		statSum(roundedDirectNumericBonus(ss, 1002), roundedCharacterGrowthValue(ss, 1)),
		starSoulSuitPercent(ss, 1002),
	)
}
func (ss *session) playerMaxMp() int32 {
	return scaleStat(
		statSum(roundedDirectNumericBonus(ss, 1004), roundedCharacterGrowthValue(ss, 2)),
		starSoulSuitPercent(ss, 1004),
	)
}
func (ss *session) playerStr() int32 {
	return statSum(ss.naturalPrimaryStat(), ss.strAdd, roundedDirectNumericBonus(ss, 1005))
}
func (ss *session) playerQuk() int32 {
	return statSum(ss.naturalPrimaryStat(), ss.qukAdd, roundedDirectNumericBonus(ss, 1006))
}
func (ss *session) playerSpi() int32 {
	return statSum(ss.naturalPrimaryStat(), ss.spiAdd, roundedDirectNumericBonus(ss, 1007))
}
func (ss *session) playerWim() int32 {
	return statSum(ss.naturalPrimaryStat(), ss.wimAdd, roundedDirectNumericBonus(ss, 1008))
}
func (ss *session) playerPhy() int32 {
	return statSum(ss.naturalPrimaryStat(), ss.phyAdd, roundedDirectNumericBonus(ss, 1034))
}
func (ss *session) playerSta() int32 {
	return statSum(ss.naturalPrimaryStat(), ss.staAdd, roundedDirectNumericBonus(ss, 1035))
}
func (ss *session) playerPhyAtk() int32 {
	return scaleStat(
		statSum(roundedDirectNumericBonus(ss, 1009), roundedCharacterGrowthValue(ss, 7)),
		starSoulSuitPercent(ss, 1009),
	)
}
func (ss *session) playerSpiAtk() int32 {
	return scaleStat(
		statSum(roundedDirectNumericBonus(ss, 1010), roundedCharacterGrowthValue(ss, 8)),
		starSoulSuitPercent(ss, 1010),
	)
}
func (ss *session) playerPhyDef() int32 {
	return scaleStat(
		statSum(roundedDirectNumericBonus(ss, 1011), roundedCharacterGrowthValue(ss, 9)),
		starSoulSuitPercent(ss, 1011),
	)
}
func (ss *session) playerSpiDef() int32 {
	return scaleStat(
		statSum(roundedDirectNumericBonus(ss, 1012), roundedCharacterGrowthValue(ss, 10)),
		starSoulSuitPercent(ss, 1012),
	)
}

func scaleStat(value int32, percent float64) int32 {
	scaled := math.Round(float64(value) * (1 + percent))
	if scaled >= float64(maxInt32Value) {
		return int32(maxInt32Value)
	}
	if scaled <= float64(minInt32Value) {
		return int32(minInt32Value)
	}
	return int32(scaled)
}

// playerExtraNumeric returns complete panel/combat values for attributes not
// represented by UnitCharacter's integer fields.
func (ss *session) playerExtraNumeric(numeric int32) float64 {
	value := directNumericBonus(ss, numeric)
	switch numeric {
	case 1017:
		value += characterGrowthValue(ss, 19)
	case 1031:
		value += characterGrowthValue(ss, 24)
	case 1032:
		value += characterGrowthValue(ss, 25)
	case 1033:
		value += characterGrowthValue(ss, 26)
	case 1034:
		value = float64(ss.playerPhy())
	case 1035:
		value = float64(ss.playerSta())
	}
	value *= 1 + starSoulSuitPercent(ss, numeric)
	return value
}

// isSkinIDValid：SkinId 是否在客户端 SkinBase 表（1-8 职业皮肤 + 120594+ 皮肤装备）。
func isSkinIDValid(skin int32) bool {
	if tables == nil {
		return skin >= 1 && skin <= 8
	}
	_, ok := tables.skinBase[int64(skin)]
	return ok
}

// buildUnitCharacter：构造 M2C_SendCharacter / M2C_GetCharacter 用的 UnitCharacter。
// 字段与 Hotfix.dll UnitCharacter 类一致（ProtoTag 见 11 文档）。
func buildUnitCharacter(ss *session) *protocol.UnitCharacter {
	mhp := ss.playerMaxHp()
	mp := ss.playerMaxMp()
	hp := ss.battleHP()
	currentMP := ss.battleMP()
	ss.storeTeamHeadHP(hp, mhp)
	skin := ss.skinID
	if skin <= 0 || (skin > 4 && !isSkinIDValid(skin)) {
		skin = ss.jobID // 兜底：SkinBase.Get(0) 会 NRE（12 文档 §2.3）
	}
	familyName := ""
	if ss.familyID != 0 && globalServer != nil && globalServer.store != nil {
		_ = globalServer.store.db.QueryRow(`SELECT name FROM families WHERE id = ?`, ss.familyID).Scan(&familyName)
	}
	character := &protocol.UnitCharacter{
		Id:             ss.playerID,
		NickName:       ss.name,
		LeaderId:       unitCharacterLeaderID(ss.playerID),
		JobId:          ss.jobID,
		Title:          ss.titleID,
		CampType:       protocol.CampType_Pioneer,
		Family:         familyName,
		SkinId:         skin,
		CharacterPoint: ss.charPoint,
		SkillPoint:     ss.skillPoint, // 技能点（每 Gameplay.skill_point_levels 级 +1，仅首次学习技能消耗）
		Level:          ss.level,
		Trans:          ss.trans,
		Hp:             hp,
		MaxHp:          mhp,
		Mp:             currentMP,
		MaxMp:          mp,
	}
	if ss.pet != nil {
		character.PetId = ss.pet.PetId
		character.PetName = ss.pet.Name
		character.IsShowPet = ss.pet.IsShow
		character.PetLevel = ss.pet.Level
	}
	return character
}
