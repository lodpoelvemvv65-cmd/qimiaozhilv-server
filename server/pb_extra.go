package main

import (
	"math"
	"sort"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// ===================== protobuf 手工编码（List 字段） =====================
// protocol.pb.go 为 protoc 生成代码，序列化依赖 rawDesc 反射表；其中
// M2C_GetBag.BagMapList、M2C_SendBag.BagMapList、M2C_GetCharacter.WornBagMapList、
// M2C_PutOn/M2C_Takeoff.BagMapList+WornBagMapList、M2C_UseGoods.BagMapList+MainUISlotList
// 在客户端是"字段级 ProtoMember"（List 类型），生成器未包含。
// 这些字段全部是 repeated message（wire type 2），这里手工追加到
// proto.Marshal 已编码的响应体尾部，protobuf 解码端可正常解析。
//
// wire format：key = (field<<3)|wire；varint 小端 7bit 分组。

func pbVarint(v uint64) []byte {
	var out []byte
	for v >= 0x80 {
		out = append(out, byte(v)|0x80)
		v >>= 7
	}
	return append(out, byte(v))
}

func pbKey(field, wire int) []byte {
	return pbVarint(uint64(field)<<3 | uint64(wire))
}

// pbAppendBytes：追加一个 LEN 字段（repeated message 的其中一项）。
func pbAppendBytes(msg []byte, field int, payload []byte) []byte {
	out := append(msg, pbKey(field, 2)...)
	out = append(out, pbVarint(uint64(len(payload)))...)
	return append(out, payload...)
}

// pbAppendVarint：追加一个 varint 字段。
func pbAppendVarint(msg []byte, field int, v uint64) []byte {
	out := append(msg, pbKey(field, 0)...)
	return append(out, pbVarint(v)...)
}

// pbAppendFixed32：追加一个 fixed32 字段（AttributeMap.Value，wire 5）。
func pbAppendFixed32(msg []byte, field int, v float32) []byte {
	bits := math.Float32bits(v)
	out := append(msg, pbKey(field, 5)...)
	return append(out, byte(bits), byte(bits>>8), byte(bits>>16), byte(bits>>24))
}

// ===================== BagMap / NetItem / EquipTransMessage 编码 =====================

// encodeNetItem：NetItem{ItemId=1, ItemType=2, ServerId=3, Count=4, IsLock=5, GetSource=6}。
func encodeNetItem(it *bagItem) []byte {
	var b []byte
	b = pbAppendVarint(b, 1, uint64(it.ItemId))
	b = pbAppendVarint(b, 2, uint64(it.ItemType))
	if it.ServerId != 0 {
		b = pbAppendVarint(b, 3, uint64(it.ServerId))
	}
	if it.Count != 0 {
		b = pbAppendVarint(b, 4, uint64(it.Count))
	}
	if it.IsLock {
		b = pbAppendVarint(b, 5, 1)
	}
	if it.GetSource != "" {
		b = pbAppendBytes(b, 6, []byte(it.GetSource))
	}
	return b
}

// encodeAttributeMap：AttributeMap{Key=1, Value=2(fixed32)}。
func encodeAttributeMap(key int32, val float32) []byte {
	var b []byte
	b = pbAppendVarint(b, 1, uint64(key))
	b = pbAppendFixed32(b, 2, val)
	return b
}

// EquipTransMessage 的属性键使用 Cal.AttributeType，而角色 NumericComponent
// 推送使用 NumericType。两套枚举不能混用。
var equipFieldToAttributeType = []struct {
	field string
	key   int32
}{
	{"Hp", 1}, {"Mp", 2},
	{"Str", 3}, {"Quk", 4}, {"Spi", 5}, {"Wim", 6},
	{"PhyAtk", 7}, {"SpiAtk", 8}, {"PhyDef", 9}, {"SpiDef", 10},
	{"Pcrir", 11}, {"Mcrir", 12}, {"Pcri", 13}, {"Mcri", 14},
	{"Rpcrir", 15}, {"Rmcrir", 16}, {"Rpcri", 17}, {"Rmcri", 18},
	{"Dvo", 19}, {"Sta", 20}, {"Phy", 21}, {"PhyDA", 22}, {"MicDA", 23},
	{"Spd", 24}, {"Hit", 25}, {"Res", 26},
	{"SuckR", 27}, {"SuckV", 28}, {"HpRecover", 29}, {"Nphyi", 30}, {"Nmeni", 31},
}

// mainAttributeOf：模板装备声明基础属性键，Value 下发实例洗练增量；客户端
// 自行读取 EquipBase 基础值并与增量相加。额外洗练键也要下发，否则界面看不到。
func mainAttributeOf(it *bagItem) []byte {
	var b []byte
	if tables == nil || it == nil {
		return b
	}
	eb, ok := tables.equipBase[int64(it.ItemId)]
	if !ok {
		return b
	}
	seen := make(map[int32]bool)
	for _, attribute := range equipFieldToAttributeType {
		// EquipBase contains fractional rates such as Pcrir=0.1 and Dvo=0.05.
		// Integer conversion would truncate them and omit those detail rows.
		if numf(eb[attribute.field]) == 0 {
			continue
		}
		seen[attribute.key] = true
		b = append(b, pbAppendBytes(nil, 5, encodeAttributeMap(attribute.key, it.MainAttr[attribute.key]))...)
	}
	var extra []int
	for key := range it.MainAttr {
		if key > 0 && !seen[key] {
			extra = append(extra, int(key))
		}
	}
	sort.Ints(extra)
	for _, key := range extra {
		b = append(b, pbAppendBytes(nil, 5, encodeAttributeMap(int32(key), it.MainAttr[int32(key)]))...)
	}
	return b
}

// encodeEquipTrans：EquipTransMessage{EquipId=1, specialKey=3, IsLocked=4,
// mainAttribute=5(repeated), randomAttributes=6(repeated), Star=7, Quality=8, Level=9, specialId=10,
// addtionalAttributes=11(repeated), GemList=12(repeated)}。
// GemList（tag12）= 装备已镶嵌宝石 ItemId（槽位 0..MaxHole-1，0=空）——
// 客户端 Equip.gemList 据此显示宝石槽（MeltingUI b__0）。
func encodeEquipTrans(it *bagItem) []byte {
	repairBagItem(it)
	var b []byte
	b = pbAppendVarint(b, 1, uint64(it.ItemId))
	b = pbAppendVarint(b, 3, uint64(it.SpecialKey))
	if it.IsLock {
		b = pbAppendVarint(b, 4, 1)
	}
	ma := mainAttributeOf(it)
	if len(ma) > 0 {
		b = append(b, ma...)
	}
	for _, id := range it.RandomAttrs {
		if id > 0 {
			b = pbAppendVarint(b, 6, uint64(id))
		}
	}
	b = pbAppendVarint(b, 7, uint64(it.Star))
	b = pbAppendVarint(b, 8, uint64(it.Quality))
	b = pbAppendVarint(b, 9, uint64(it.Level))
	b = pbAppendVarint(b, 10, uint64(it.SpecialId))
	for _, id := range it.AddAttrs {
		if id > 0 {
			b = pbAppendVarint(b, 11, uint64(id))
		}
	}
	gemSlots := len(it.GemList)
	if tables != nil {
		if eb, ok := tables.equipBase[int64(it.ItemId)]; ok {
			if maxHole := int(num(eb["MaxHole"])); maxHole > gemSlots {
				gemSlots = maxHole
			}
		}
	}
	for i := 0; i < gemSlots; i++ {
		var gemID int32
		if i < len(it.GemList) {
			gemID = it.GemList[i]
		}
		b = pbAppendVarint(b, 12, uint64(gemID))
	}
	return b
}

// encodeBagMap：BagMap{Index=1, NetItem=2, EquipTransMessage=3}。
// EquipTransMessage 兜底：物品在 EquipBase 表中即按装备编码（即使旧存档 ItemType
// 被错存为 2/3）——客户端 CharacterUI/UpdateWornEquipUIEvent 对缺失的
// EquipTransMessage 无判空（new Equip(msg) 直接 NRE，中断整个穿戴面板刷新，
// 表现 = "个人信息看不到新装备/背景"）。
func encodeBagMap(idx int32, it *bagItem) []byte {
	var b []byte
	b = pbAppendVarint(b, 1, uint64(idx))
	ni := encodeNetItem(it)
	if len(ni) > 0 {
		b = append(b, pbAppendBytes(nil, 2, ni)...)
	}
	if it.ItemType == 1 || itemIsEquip(it.ItemId) { // 装备带 EquipTransMessage
		et := encodeEquipTrans(it)
		if len(et) > 0 {
			b = append(b, pbAppendBytes(nil, 3, et)...)
		}
	}
	return b
}

// encodeEmptyWornBagMap encodes an explicit empty equipment slot. The client
// applies WornBagMapList entries one by one and never clears WornEquipDic, so
// omitting a removed slot leaves its old icon visible in CharacterUI.
func encodeEmptyWornBagMap(idx int32) []byte {
	var b []byte
	b = pbAppendVarint(b, 1, uint64(idx))
	// An empty embedded NetItem still creates the client-side object whose
	// ItemType=0 makes UpdateWornEquipUIEvent clear this slot's icon.
	b = pbAppendBytes(b, 2, nil)
	return b
}

func appendEmptyWornSlot(msg []byte, field int, idx int32) []byte {
	return pbAppendBytes(msg, field, encodeEmptyWornBagMap(idx))
}

// itemIsEquip：物品是否 EquipBase 装备（newBagItem 未命中时为 false）。
func itemIsEquip(itemID int32) bool {
	if tables == nil {
		return false
	}
	_, ok := tables.equipBase[int64(itemID)]
	return ok
}

// encodeBagMapList：整个 BagMapList 字段（field 参数=目标消息中的 tag）。
// items：按 Index 排序的 (Index, item) 对。
func encodeBagMapList(msg []byte, field int, pairs [][2]interface{}) []byte {
	for _, p := range pairs {
		idx := p[0].(int32)
		it := p[1].(*bagItem)
		payload := encodeBagMap(idx, it)
		msg = pbAppendBytes(msg, field, payload)
	}
	return msg
}

// bagPairs：把 bag 或 worn 映射转成排序的 (Index, item) 列表。
func bagPairs(m map[int32]*bagItem) [][2]interface{} {
	keys := make([]int32, 0, len(m))
	for index, it := range m {
		if it != nil {
			keys = append(keys, index)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	out := make([][2]interface{}, 0, len(keys))
	for _, index := range keys {
		out = append(out, [2]interface{}{index, m[index]})
	}
	return out
}

// ===================== 响应组装 =====================

// buildBagResponse：基础响应体 + BagMapList 字段。
// base 用 proto.Marshal 编码（含 RpcId/Error/Message/ActorId），再追加 list 字段。
func appendBagMapList(base []byte, field int, m map[int32]*bagItem) []byte {
	return encodeBagMapList(base, field, bagPairs(m))
}

// ===================== MainUISlotList 复用生成类型 =====================
// M2C_UseGoods.MainUISlotList / M2C_DropItem.MainUISlotList / M2C_UseMainUIGoods.MainUISlotList
// 是字段级 List（repeated MainUISlotInfo），生成代码不含 → 手工编码。
func appendMainUISlotList(msg []byte, field int, infos []*protocol.MainUISlotInfo) []byte {
	for _, info := range infos {
		if info == nil {
			continue
		}
		raw, err := proto.Marshal(info)
		if err != nil {
			continue
		}
		msg = pbAppendBytes(msg, field, raw)
	}
	return msg
}

func encodeMainUISlotInfos(infos []*protocol.MainUISlotInfo) []byte {
	return appendMainUISlotList(nil, 2, infos)
}

// appendFamilyContributionList：向 FamilyMemberInfo 的 raw bytes 追加
// ContributionList(tag6, List<FamilyContributionMap>)（字段级 List，生成代码不含）。
// 返回扩展后的 raw。
func appendFamilyContributionList(msg []byte, field int, mi *protocol.FamilyMemberInfo, memberID int64, personal int32) []byte {
	if mi == nil || memberID <= 0 {
		return msg
	}
	raw, err := proto.Marshal(&protocol.FamilyContributionMap{Id: memberID, Value: personal})
	if err != nil {
		return msg
	}
	return pbAppendBytes(msg, field, raw)
}

// ===================== 同场景玩家同步（20036/20034/20037） =====================
// M2C_UnitsInMap(20036) 的 Units(tag1, List<UnitPosInfo>) 与 PvpUnits(tag2,
// List<UnitCharacter>) 是字段级 ProtoMember（生成代码不含），手工编码。
// 客户端 M2C_UnitsInMapHandler（Hotfix 249）先 UnitComponent.RemoveAll() 再按
// 同一下标 i 取 Units[i]（位置）与 PvpUnits[i]（角色，含 Id）重建单位——
// 两个列表必须等长，且【必须含自己】（否则 RemoveAll 把 MyUnit 也清了）。

// encodeUnitPosInfo：UnitPosInfo{X=2 fixed32, Y=3 fixed32, YAngle=5 varint}。
func encodeUnitPosInfo(x, y float32, yAngle int32) []byte {
	var b []byte
	b = pbAppendFixed32(b, 2, x)
	b = pbAppendFixed32(b, 3, y)
	if yAngle != 0 {
		b = pbAppendVarint(b, 5, uint64(yAngle))
	}
	return b
}

// appendUnitsInMap：把玩家位置列表（tag1 Units）与角色列表（tag2 PvpUnits）追加到 msg。
// unitChars 与 pos 必须等长且一一对应（含自己的位置/角色）。
func appendUnitsInMap(msg []byte, unitChars []*protocol.UnitCharacter, pos [][2]float32) []byte {
	for i, uc := range unitChars {
		if uc == nil {
			continue
		}
		var p [2]float32
		if i < len(pos) {
			p = pos[i]
		}
		msg = pbAppendBytes(msg, 1, encodeUnitPosInfo(p[0], p[1], 0))
		raw, err := proto.Marshal(uc)
		if err != nil {
			continue
		}
		msg = pbAppendBytes(msg, 2, raw)
	}
	return msg
}

// appendLeaveMap：M2C_LeaveMap(20037){UnitId=1}。
func appendLeaveMap(msg []byte, unitID int64) []byte {
	return pbAppendVarint(msg, 1, uint64(unitID))
}
