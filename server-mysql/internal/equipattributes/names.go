package equipattributes

import "strconv"

// Names 是 Cal.AttributeType（文档/14 §4.9）的中文名，键空间与上面的 Fields 完全一致。
//
// GM 后台一律展示中文名，不露出裸 id：随机属性、洗练词缀、宝石属性三处共用这一张表。
// 属性 key 出现在 EquipBase 的主属性/特殊属性、ManulEquipAttribute.Key、
// EquipAffixConfig.AffixArr[].Key、MaterialBase.GemKey 四个地方，所以这里只放一份，
// 由 server 与 cmd/gm-api 共同引用。
//
// 客户端 TabHelper.GetAttributeString 用 switch(key-1) 取名，1..10 与 20..21 按整数显示、
// 11..19 与 22..31 按百分比显示；文案与客户端 Constant 表一致，不要自行改写。
var Names = map[int32]string{
	0: "无",
	1: "最大生命", 2: "最大精力", 3: "力量", 4: "敏捷", 5: "精神", 6: "智慧",
	7: "物理攻击", 8: "精神攻击", 9: "物理防御", 10: "精神防御",
	11: "物理暴击系数", 12: "精神暴击系数", 13: "物理暴击效果", 14: "精神暴击效果",
	15: "抗物理暴击系数", 16: "抗精神暴击系数", 17: "抗物理暴击效果", 18: "抗精神暴击效果",
	19: "辅助值", 20: "体质", 21: "耐力", 22: "物理免伤", 23: "精神免伤",
	24: "速度", 25: "命中", 26: "抵抗", 27: "吸血率", 28: "吸血量",
	29: "生命回复", 30: "物理增伤", 31: "精神增伤",
}

// AttributeName 返回属性中文名；未登记的 key 回退成 "属性N"，保证界面不留空白。
func AttributeName(key int32) string {
	if name, ok := Names[key]; ok {
		return name
	}
	return "属性" + strconv.FormatInt(int64(key), 10)
}

// NamesByKey 把 Names 转成 map[string]string，供 JSON 响应直接使用。
// Go 的 map[int32]string 序列化后键虽然是数字字符串，但显式转一次能让
// 前端类型固定为 Record<string, string>，不用做数字/字符串键的兼容。
func NamesByKey() map[string]string {
	out := make(map[string]string, len(Names))
	for key, name := range Names {
		out[strconv.FormatInt(int64(key), 10)] = name
	}
	return out
}
