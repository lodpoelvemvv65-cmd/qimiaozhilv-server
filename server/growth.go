package main

import (
	"math"

	"mhqserver/protocol"
)

// ===================== 线上属性公式（RoleGrowth.json） =====================
// 线上统一成长公式：value = X1 + X2*Lv + X3*Lv²/100（float 计算后 int 截断，C# 语义）。
// 客户端只显示服务器推送的 NumericComponent 值，公式确认自线上 RoleGrowth 表字段
// （Hp1/Hp2/Hp3、Vig1/Vig2/Vig3、Str1..3、PhyAtk1..3 等，见 08 文档 §8 / 11 文档）。
// 升级经验：线上 GetMaxExpByLevel = 0.01*Lv³（仅客户端经验条显示用），
// 服务器判定需求按同一公式放大 10000 倍：expNeed(Lv) = 100*Lv³（与主线奖励/怪经验量级吻合）。

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

// 职业基础成长（未含加点/装备）
func baseMaxHp(jobID, lv int32) int32 { r := roleRow(jobID); return growAt(r, "Hp1", "Hp2", "Hp3", lv) }
func baseMaxMp(jobID, lv int32) int32 {
	r := roleRow(jobID)
	return growAt(r, "Vig1", "Vig2", "Vig3", lv)
}
func baseStr(jobID, lv int32) int32 {
	r := roleRow(jobID)
	return growAt(r, "Str1", "Str2", "Str3", lv)
}
func baseQuk(jobID, lv int32) int32 {
	r := roleRow(jobID)
	return growAt(r, "Quk1", "Quk2", "Quk3", lv)
}
func baseSpi(jobID, lv int32) int32 {
	r := roleRow(jobID)
	return growAt(r, "Spi1", "Spi2", "Spi3", lv)
}
func baseWim(jobID, lv int32) int32 {
	r := roleRow(jobID)
	return growAt(r, "Wim1", "Wim2", "Wim3", lv)
}
func basePhyAtk(jobID, lv int32) int32 {
	r := roleRow(jobID)
	return growAt(r, "PhyAtk1", "PhyAtk2", "PhyAtk3", lv)
}
func baseSpiAtk(jobID, lv int32) int32 {
	r := roleRow(jobID)
	return growAt(r, "SpiAtk1", "SpiAtk2", "SpiAtk3", lv)
}
func basePhyDef(jobID, lv int32) int32 {
	r := roleRow(jobID)
	return growAt(r, "PhyDef1", "PhyDef2", "PhyDef3", lv)
}
func baseSpiDef(jobID, lv int32) int32 {
	r := roleRow(jobID)
	return growAt(r, "SpiDef1", "SpiDef2", "SpiDef3", lv)
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
	return out
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

// directNumericBonus is the flat value supplied by equipment, star souls and
// transmigration. CharacterGrowth conversions are excluded so primary
// attributes remain non-recursive formula inputs.
func directNumericBonus(ss *session, numeric int32) float64 {
	if ss == nil {
		return 0
	}
	return float64(equipBonus(ss)[numeric]) +
		float64(starSoulBonus(ss)[numeric]) +
		float64(ss.transBonus[numeric])
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
		"Phy": directNumericBonus(ss, 1034),
		"Sta": directNumericBonus(ss, 1035),
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

// gemKeyToNumeric：宝石 AttributeType（GemKey，1-31）→ NumericType（20169 推送键）。
// 线上映射见 14 文档 §4.9（⚠️ 推断/未证实，自建服务端采用）。
var gemKeyToNumeric = map[int32]int32{
	1: 1002, 2: 1004, 3: 1005, 4: 1006, 5: 1007, 6: 1008,
	7: 1009, 8: 1010, 9: 1011, 10: 1012,
	11: 1013, 12: 1014, 13: 1015, 14: 1016,
	15: 1018, 16: 1020, 17: 1019, 18: 1021, 19: 1017,
	20: 1035, 21: 1034, 22: 1045, 23: 1046, 24: 1031, 25: 1032, 26: 1033,
	27: 1042, 28: 1043, 29: 1044, 30: 1022, 31: 1023,
}

// ===================== 玩家最终属性 =====================
// 最终属性 = 职业成长（RoleGrowth）+ 手动加点 + 装备加成 + 转生加成（trans.go）。
func (ss *session) playerMaxHp() int32 {
	return statSum(baseMaxHp(ss.jobID, ss.level), roundedDirectNumericBonus(ss, 1002), roundedCharacterGrowthValue(ss, 1))
}
func (ss *session) playerMaxMp() int32 {
	return statSum(baseMaxMp(ss.jobID, ss.level), roundedDirectNumericBonus(ss, 1004), roundedCharacterGrowthValue(ss, 2))
}
func (ss *session) playerStr() int32 {
	return statSum(baseStr(ss.jobID, ss.level), ss.strAdd, roundedDirectNumericBonus(ss, 1005))
}
func (ss *session) playerQuk() int32 {
	return statSum(baseQuk(ss.jobID, ss.level), ss.qukAdd, roundedDirectNumericBonus(ss, 1006))
}
func (ss *session) playerSpi() int32 {
	return statSum(baseSpi(ss.jobID, ss.level), ss.spiAdd, roundedDirectNumericBonus(ss, 1007))
}
func (ss *session) playerWim() int32 {
	return statSum(baseWim(ss.jobID, ss.level), ss.wimAdd, roundedDirectNumericBonus(ss, 1008))
}
func (ss *session) playerPhyAtk() int32 {
	return statSum(basePhyAtk(ss.jobID, ss.level), roundedDirectNumericBonus(ss, 1009), roundedCharacterGrowthValue(ss, 7))
}
func (ss *session) playerSpiAtk() int32 {
	return statSum(baseSpiAtk(ss.jobID, ss.level), roundedDirectNumericBonus(ss, 1010), roundedCharacterGrowthValue(ss, 8))
}
func (ss *session) playerPhyDef() int32 {
	return statSum(basePhyDef(ss.jobID, ss.level), roundedDirectNumericBonus(ss, 1011), roundedCharacterGrowthValue(ss, 9))
}
func (ss *session) playerSpiDef() int32 {
	return statSum(baseSpiDef(ss.jobID, ss.level), roundedDirectNumericBonus(ss, 1012), roundedCharacterGrowthValue(ss, 10))
}

// playerExtraNumeric returns complete panel/combat values for attributes not
// represented by UnitCharacter's integer fields.
func (ss *session) playerExtraNumeric(numeric int32) float64 {
	value := directNumericBonus(ss, numeric)
	switch numeric {
	case 1017:
		value += growFloatAt(roleRow(ss.jobID), "Dvo1", "Dvo2", "Dvo3", ss.level) +
			characterGrowthValue(ss, 19)
	case 1031:
		value += characterGrowthValue(ss, 24)
	case 1032:
		value += characterGrowthValue(ss, 25)
	case 1033:
		value += characterGrowthValue(ss, 26)
	}
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
	skin := ss.skinID
	if skin <= 0 || (skin > 4 && !isSkinIDValid(skin)) {
		skin = ss.jobID // 兜底：SkinBase.Get(0) 会 NRE（12 文档 §2.3）
	}
	familyName := ""
	if ss.familyID != 0 && globalServer != nil && globalServer.store != nil {
		_ = globalServer.store.db.QueryRow(`SELECT name FROM families WHERE id = ?`, ss.familyID).Scan(&familyName)
	}
	return &protocol.UnitCharacter{
		Id:             ss.playerID,
		NickName:       ss.name,
		LeaderId:       ss.playerID,
		JobId:          ss.jobID,
		CampType:       protocol.CampType_Pioneer,
		Family:         familyName,
		SkinId:         skin,
		CharacterPoint: ss.charPoint,
		SkillPoint:     ss.skillPoint, // 技能点（角色升级每级 +1，仅首次学习技能消耗）
		Level:          ss.level,
		Trans:          ss.trans,
		Hp:             mhp,
		MaxHp:          mhp,
		Mp:             mp,
		MaxMp:          mp,
	}
}
