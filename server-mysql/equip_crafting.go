package main

import (
	"log"
	"math/rand"
	"sort"

	"google.golang.org/protobuf/proto"

	"mhqserver/internal/operationsconfig"
	"mhqserver/protocol"
)

// itemCost is an aggregated material cost. Both ManulEquip and Strengthentable
// use arrays of item/count records, but their id field names differ.
type itemCost struct {
	id    int32
	count int32
}

func itemCosts(raw interface{}, idField string) []itemCost {
	totals := make(map[int32]int32)
	for _, entry := range arrOf(raw) {
		row, _ := entry.(map[string]interface{})
		if row == nil {
			continue
		}
		id := int32(num(row[idField]))
		count := int32(num(row["Count"]))
		if id > 0 && count > 0 {
			totals[id] += count
		}
	}
	ids := make([]int, 0, len(totals))
	for id := range totals {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	out := make([]itemCost, 0, len(ids))
	for _, id := range ids {
		out = append(out, itemCost{id: int32(id), count: totals[int32(id)]})
	}
	return out
}

func mergeItemCost(costs []itemCost, id, count int32) []itemCost {
	for i := range costs {
		if costs[i].id == id {
			costs[i].count += count
			return costs
		}
	}
	return append(costs, itemCost{id: id, count: count})
}

func craftingBagItemCount(ss *session, itemID int32) int32 {
	var total int32
	if ss == nil {
		return 0
	}
	for _, it := range ss.bag {
		if it != nil && it.ItemId == itemID {
			total += it.Count
		}
	}
	return total
}

func missingItemCost(ss *session, costs []itemCost) (itemCost, bool) {
	for _, cost := range costs {
		if craftingBagItemCount(ss, cost.id) < cost.count {
			return cost, true
		}
	}
	return itemCost{}, false
}

func consumeItemCosts(ss *session, costs []itemCost) {
	for _, cost := range costs {
		ss.removeBagCountByItem(cost.id, cost.count)
	}
}

func craftBagWillHaveSpace(ss *session, costs []itemCost) bool {
	if nextBagIndex(ss) >= 0 {
		return true
	}
	// A full bag gains a slot when one required material is consumed completely.
	for _, cost := range costs {
		if craftingBagItemCount(ss, cost.id) == cost.count {
			return true
		}
	}
	return false
}

func craftingItemName(itemID int32) string {
	if tables != nil {
		for _, table := range []map[int64]map[string]interface{}{tables.materialBase, tables.goodsBase, tables.equipBase} {
			if row := table[int64(itemID)]; row != nil {
				if name, ok := row["Name"].(string); ok && name != "" {
					return name
				}
			}
		}
	}
	return "物品"
}

// isManualEquipProduct 判断某个 EquipBase 物品是不是手工打造（ManulEquip）的产物。
// 手工产物在 EquipBase 里没有属性列，属性值全在 SpecialKey/SpecialValue 上。
func isManualEquipProduct(itemID int32) bool {
	if tables == nil || itemID <= 0 {
		return false
	}
	for _, row := range tables.manulEquip {
		if row != nil && int32(num(row["EquipId"])) == itemID {
			return true
		}
	}
	return false
}

func manualAttributeIDs(quality int32, count int) []int32 {
	if tables == nil || count <= 0 || quality < 1 || quality > 6 {
		return nil
	}
	// ManulEquipAttribute uses 101..126 through 601..626, one complete value
	// tier per equipment quality color.
	var ids []int32
	for id, row := range tables.manulEquipAttribute {
		if int32(id/100) == quality && num(row["Key"]) > 0 {
			ids = append(ids, int32(id))
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	rand.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
	if count > len(ids) {
		count = len(ids)
	}
	return append([]int32(nil), ids[:count]...)
}

func manualEquipConfigID(rawType int32) (int64, bool) {
	// ManulEquipUI sends its combo-box index, not ManulEquip._id.
	// The mapping is defined by ManulEquipUI.OnChangeEquiptype.
	clientTypeToConfig := [...]int64{7, 3, 6, 5}
	if rawType >= 0 && int(rawType) < len(clientTypeToConfig) {
		return clientTypeToConfig[rawType], true
	}
	// Keep compatibility with older protocol probes that sent unambiguous
	// table IDs directly. Config ID 3 is intentionally excluded because the
	// real client uses raw type 3 for config ID 5.
	switch rawType {
	case 5, 6, 7:
		return int64(rawType), true
	default:
		return 0, false
	}
}

func (s *Server) onMakeMunalEquip(ch *channel, req *protocol.C2M_MakeMunalEquip) proto.Message {
	ss := ch.session
	resp := &protocol.M2C_MakeMunalEquip{RpcId: req.RpcId}
	reject := func(message string) proto.Message {
		resp.Message = message
		log.Printf("[S=%d] manual equip rejected: %s", ch.id, message)
		return resp
	}
	if ss == nil || ss.playerID == 0 {
		return reject("请先登录")
	}
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	if tables == nil || tables.manulEquip == nil {
		return reject("手工装备配置未加载")
	}
	configID, ok := manualEquipConfigID(req.Type)
	if !ok {
		return reject("手工装备类型无效")
	}
	row := tables.manulEquip[configID]
	if row == nil {
		return reject("手工装备类型无效")
	}
	rarity := int32(req.IsRare)
	arrayField := ""
	switch rarity {
	case 0:
		arrayField = "MaterialArr"
	case 1:
		arrayField = "RareMaterialArr"
	case 2:
		arrayField = "EpicMaterialArr"
	case 3:
		arrayField = "UpgradeRareMaterialArr"
	default:
		return reject("手工装备品质无效")
	}
	costs := itemCosts(row[arrayField], "MaterialId")
	if len(costs) == 0 {
		return reject("手工装备材料配置无效")
	}
	if missing, ok := missingItemCost(ss, costs); ok {
		return reject("材料不足：" + craftingItemName(missing.id))
	}
	if !craftBagWillHaveSpace(ss, costs) {
		return reject("背包已满")
	}
	equipID := int32(num(row["EquipId"]))
	if equipID <= 0 || !itemIsEquip(equipID) {
		return reject("手工装备产物配置无效")
	}
	consumeItemCosts(ss, costs)
	idx := nextBagIndex(ss)
	if idx < 0 {
		return reject("背包已满")
	}
	it := newBagItem(equipID)
	it.GetSource = ss.name
	applyManualCraftOutcome(ss, rarity, it)
	ss.bag[idx] = it
	s.saveData(ch)
	s.pushBagSnapshot(ch)
	log.Printf("[S=%d] manual equip made rawType=%d configID=%d rarity=%d item=%d slot=%d quality=%d star=%d bound=%v attrs=%v", ch.id, req.Type, configID, rarity, equipID, idx, it.Quality, it.Star, it.IsLock, it.RandomAttrs)
	return resp
}

func (s *Server) onStrengthEquip(ch *channel, req *protocol.C2M_StrengthEquip) proto.Message {
	ss := ch.session
	resp := &protocol.M2C_StrengthEquip{RpcId: req.RpcId}
	reject := func(message string) proto.Message {
		resp.Message = message
		log.Printf("[S=%d] strength equip rejected: %s", ch.id, message)
		return resp
	}
	if ss == nil || ss.playerID == 0 {
		return reject("请先登录")
	}
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	it := ss.bag[req.BagIndex]
	if it == nil || !itemIsEquip(it.ItemId) {
		return reject("只能对装备进行强化")
	}
	repairBagItem(it)
	if it.Level >= 20 {
		return reject("装备已强化至最高等级")
	}
	// Strengthentable is keyed by the attempt's target level: row 1 is the
	// first +0 -> +1 attempt, row 2 is +1 -> +2, and so on.
	attemptLevel := it.Level
	if attemptLevel < 1 {
		attemptLevel = 1
	}
	row := tables.strengthen[int64(attemptLevel)]
	if row == nil {
		return reject("该装备当前无法强化")
	}
	costs := itemCosts(row["NeedMaterialArr"], "_Id")
	plusProbability := 0.0
	if req.PlusItemIndex >= 0 {
		plusItem := ss.bag[req.PlusItemIndex]
		if plusItem == nil {
			return reject("强化辅助材料不存在")
		}
		plusRow := tables.strengthPlus[int64(plusItem.ItemId)]
		if plusRow == nil {
			return reject("强化辅助材料无效")
		}
		plusProbability = numf(plusRow["AddProb"])
		costs = mergeItemCost(costs, plusItem.ItemId, 1)
	}
	if missing, ok := missingItemCost(ss, costs); ok {
		return reject("材料不足：" + craftingItemName(missing.id))
	}
	needCoin := int64(num(row["NeedCoin"]))
	if needCoin < 0 || ss.coin < needCoin {
		return reject("金币不足")
	}
	consumeItemCosts(ss, costs)
	ss.coin -= needCoin
	probability := numf(row["Probability"]) + plusProbability
	if probability > 1 {
		probability = 1
	}
	oldLevel := it.Level
	if rand.Float64() < probability {
		it.Level++
		if it.Level > 20 {
			it.Level = 20
		}
		resp.IsSuccess = true
	} else {
		// A failed attempt loses at most one level.  The online FailLevel
		// column can jump straight to an earlier floor (for example 11 -> 8),
		// which makes the configured safety levels behave like random drops.
		failedLevel := oldLevel - 1
		for _, safetyLevel := range gameplayStrengthSafetyLevels() {
			if oldLevel >= safetyLevel && failedLevel < safetyLevel {
				failedLevel = safetyLevel
			}
		}
		it.Level = failedLevel
		if it.Level < 0 {
			it.Level = 0
		}
	}
	s.saveData(ch)
	s.pushMoney(ch)
	s.pushBagSnapshot(ch)
	s.pushPlayerAttrs(ch)
	s.pushUnitCharacter(ch)
	log.Printf("[S=%d] strength equip item=%d level=%d->%d success=%v coin=%d", ch.id, it.ItemId, oldLevel, it.Level, resp.IsSuccess, needCoin)
	return resp
}

func rollMainAttributes(it *bagItem) map[int32]float32 {
	result := make(map[int32]float32)
	if tables == nil || it == nil {
		return result
	}
	minimumDelta, maximumDelta := gameplayEquipmentDeltaRange()
	rollDelta := func() float32 {
		return float32(minimumDelta + rand.Float64()*(maximumDelta-minimumDelta))
	}
	row := tables.equipBase[int64(it.ItemId)]
	for _, attribute := range equipFieldToAttributeType {
		base := numf(row[attribute.field])
		if base != 0 {
			// EquipTrans.mainAttribute is a percentage delta. The client renders
			// base * (1 + delta), so sending an absolute attribute value here
			// applies the template value twice and produces wildly inflated stats.
			result[attribute.key] = rollDelta()
		}
	}
	// 手工产物只有 SpecialKey/SpecialValue 一条固定属性，没有任何属性列。
	// 客户端洗练面板显示的是 GetValueFromConfig(EquipBase, key) 乘 (1 + delta)
	// （TabHelper.GetAttributeString → RoundToInt(base * factor)），而 GetValueFromConfig
	// 只读那 26 个属性列、没有 SpecialKey 回退，基础值恒为 0 就恒显示
	// 「最大生命:0 (19.64%)」，服务端下发任何 delta 都改不了这个显示。
	// 所以手工产物不提供可洗练主属性，客户端按空列表显示"无法洗练"。
	if len(result) == 0 && !isManualEquipProduct(it.ItemId) {
		key := int32(num(row["SpecialKey"]))
		base := numf(row["SpecialValue"])
		if key > 0 && base != 0 {
			result[key] = rollDelta()
		}
	}
	return result
}

func (s *Server) onRefreshEquipMainAttribute(ch *channel, req *protocol.C2M_RefreshEquipMainAttribute) proto.Message {
	ss := ch.session
	resp := &protocol.M2C_RefreshEquipMainAttribute{RpcId: req.RpcId}
	if ss == nil || ss.playerID == 0 {
		resp.Message = "请先登录"
		return resp
	}
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	it := ss.bag[req.BagIndex]
	if it == nil || !itemIsEquip(it.ItemId) {
		resp.Message = "只能对装备进行洗练！"
		return resp
	}
	rolled := rollMainAttributes(it)
	if len(rolled) == 0 {
		resp.Message = "该装备没有可洗练的主属性"
		return resp
	}
	it.MainAttr = rolled
	s.saveData(ch)
	s.pushBagSnapshot(ch)
	s.pushPlayerAttrs(ch)
	s.pushUnitCharacter(ch)
	log.Printf("[S=%d] refresh equip main item=%d slot=%d attrs=%v", ch.id, it.ItemId, req.BagIndex, it.MainAttr)
	return resp
}

type affixCandidate struct {
	id     int32
	family int32
	// attributeCount 是该词条一条覆盖几个属性（1 单属性、2 双属性、3 三属性）。
	attributeCount int
	// sixDimension 标记该词条是否属于“六维”。六维不参与普通抽取，只由洗词缀的
	// 独立概率（six_dimension_percent / six_dimension_full_percent）产出。
	sixDimension bool
}

// affixSixDimensionKeys 是“六维”在 AttributeType 里的取值（见 文档/14 属性键空间）：
// 3 力量、4 敏捷、5 精神、6 智慧、20 体质、21 耐力。
// 注意 1 最大生命 / 2 最大精力不属于六维。
var affixSixDimensionKeys = map[int32]bool{3: true, 4: true, 5: true, 6: true, 20: true, 21: true}

// affixSixDimensionMaxTier 是“满六维”使用的档位序号：线上数据的最高档，
// 即三属性六维的 510/255/255。
const affixSixDimensionMaxTier = 5

// affixFullSixDimensionAttributeCount 是“满六维”要求的属性条数：只有三属性六维
// 才有 510/255/255 这种满值形态。
const affixFullSixDimensionAttributeCount = 3

// affixIsSixDimension 判断某个 EquipAffixConfig id 是否属于六维词条。
// AffixArr 里只要出现任意一个六维属性键就算（单、双、三属性六维都算）。
func affixIsSixDimension(id int32) bool {
	if tables == nil || id <= 0 {
		return false
	}
	row := tables.equipAffix[int64(id)]
	if row == nil {
		return false
	}
	for _, raw := range arrOf(row["AffixArr"]) {
		affix, _ := raw.(map[string]interface{})
		if affix == nil {
			continue
		}
		if affixSixDimensionKeys[int32(num(affix["Key"]))] {
			return true
		}
	}
	return false
}

// affixIDsContainSixDimension 判断一组词缀 id 里是否含六维词条，供登录修复判断使用。
func affixIDsContainSixDimension(ids []int32) bool {
	for _, id := range ids {
		if affixIsSixDimension(id) {
			return true
		}
	}
	return false
}

// affixCandidatesByFamily 把 EquipAffixConfig 按词条编号（id/100000）分组，
// 每个词条保留按 id 升序的全部档位，档位下标 = 档位序号-1。
func affixCandidatesByFamily() map[int32][]affixCandidate {
	if tables == nil {
		return nil
	}
	byFamily := make(map[int32][]affixCandidate)
	for id, row := range tables.equipAffix {
		arr := arrOf(row["AffixArr"])
		if len(arr) == 0 {
			continue
		}
		valid := true
		sixDimension := false
		for _, raw := range arr {
			affix, _ := raw.(map[string]interface{})
			if affix == nil || int32(num(affix["Key"])) <= 0 {
				valid = false
				break
			}
			if affixSixDimensionKeys[int32(num(affix["Key"]))] {
				sixDimension = true
			}
		}
		family := int32(id / 100000)
		if !valid || family <= 0 {
			continue
		}
		byFamily[family] = append(byFamily[family], affixCandidate{
			id: int32(id), family: family, attributeCount: len(arr), sixDimension: sixDimension,
		})
	}
	for family, candidates := range byFamily {
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].id < candidates[j].id })
		// 同一词条各档位的属性构成一致，按“任一档含六维”统一整条词条的归类。
		sixDimension := false
		for _, candidate := range candidates {
			if candidate.sixDimension {
				sixDimension = true
			}
		}
		for index := range candidates {
			candidates[index].sixDimension = sixDimension
		}
		byFamily[family] = candidates
	}
	return byFamily
}

// affixPoolForTier 从每个词条分组里各取一档组成候选池。tier 是档位序号（1..5），
// 超出范围时收敛到最后一档。sixDimension 为 nil 时不过滤，否则只保留匹配的分组。
func affixPoolForTier(byFamily map[int32][]affixCandidate, tier int32, sixDimension *bool) []affixCandidate {
	pick := int(tier) - 1
	if pick < 0 {
		pick = 0
	}
	var pool []affixCandidate
	for _, candidates := range byFamily {
		if len(candidates) == 0 {
			continue
		}
		if sixDimension != nil && candidates[0].sixDimension != *sixDimension {
			continue
		}
		index := pick
		if index >= len(candidates) {
			index = len(candidates) - 1
		}
		pool = append(pool, candidates[index])
	}
	sort.Slice(pool, func(i, j int) bool { return pool[i].family < pool[j].family })
	return pool
}

// affixPoolForQuality 是普通词条池：按装备品质色取档（原版规则 tier = quality-1），
// 并且只含非六维词条。六维由 affixSixDimensionPool 单独产出，不再混进普通池，
// 否则“六维只占 2%”会被普通抽取代数放大。
func affixPoolForQuality(quality int32) []affixCandidate {
	normal := false
	return affixPoolForTier(affixCandidatesByFamily(), quality, &normal)
}

// affixSixDimensionPool 是六维词条池：按配置的品质→档位映射取档。
func affixSixDimensionPool(quality int32, config operationsconfig.AffixWashConfig) []affixCandidate {
	sixDimension := true
	return affixPoolForTier(affixCandidatesByFamily(), config.SixDimensionTierForQuality(quality), &sixDimension)
}

// affixFullSixDimensionPool 是满六维池：三属性六维词条的最高档（510/255/255）。
func affixFullSixDimensionPool() []affixCandidate {
	sixDimension := true
	byFamily := affixCandidatesByFamily()
	pool := affixPoolForTier(byFamily, affixSixDimensionMaxTier, &sixDimension)
	var full []affixCandidate
	for _, candidate := range pool {
		if candidate.attributeCount == affixFullSixDimensionAttributeCount {
			full = append(full, candidate)
		}
	}
	return full
}

// equipmentBonusCount is the 洗练词缀 (EquipAffixConfig) slot count for an
// equipment quality color: white/green have 1 slot, blue/purple have 2, and
// orange/red have 3. It does NOT apply to the 手工随机词条 count, which is
// driven by the item's Star (see applyManualCraftOutcome).
func equipmentBonusCount(quality int32) int {
	count := 1
	if quality >= 3 {
		count = 2
	}
	if quality >= 5 {
		count = 3
	}
	return count
}

func rollAffixIDsWithShuffle(quality int32, shuffle func(int, func(int, int))) []int32 {
	pool := affixPoolForQuality(quality)
	shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	count := equipmentBonusCount(quality)
	if count > len(pool) {
		count = len(pool)
	}
	ids := make([]int32, count)
	for i := range ids {
		ids[i] = pool[i].id
	}
	return ids
}

func rollAffixIDs(quality int32) []int32 {
	return rollAffixIDsWithShuffle(quality, rand.Shuffle)
}

// rollAffixIDsFromPool 从指定候选池里随机取 1 条词缀。六维与满六维都是一次洗练的
// 完整产出，只给一条，不再按品质槽位补齐普通词条。
func rollAffixIDsFromPool(pool []affixCandidate, shuffle func(int, func(int, int))) []int32 {
	if len(pool) == 0 {
		return nil
	}
	shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	return []int32{pool[0].id}
}

// affixPercentRolled 判定一个百分数概率是否命中。percent 取 0..100（0 永不命中、
// 100 必定命中）；roll 返回 [0,1) 的均匀随机数，便于测试注入。
func affixPercentRolled(percent float64, roll func() float64) bool {
	if percent <= 0 {
		return false
	}
	if percent >= 100 {
		return true
	}
	return roll()*100 < percent
}

// rollAffixWashIDs 按配置顺序判定本次洗词缀的结果，返回词缀 id 列表。
// 判定顺序是 满六维 -> 六维 -> 无词缀 -> 普通词条，命中即决定本次结果，因此三者互斥，
// 每个百分比都是“单次洗练的总体概率”。返回空列表代表落空（客户端显示“无词缀”）。
// roll 与 shuffle 都可注入，便于测试构造确定的随机序列。
func rollAffixWashIDs(quality int32, config operationsconfig.AffixWashConfig, roll func() float64, shuffle func(int, func(int, int))) []int32 {
	// 池为空说明数据表或配置缺失，此时跳过对应分支，避免把故障伪装成“落空”。
	fullPool := affixFullSixDimensionPool()
	if len(fullPool) > 0 && affixPercentRolled(config.Affix.SixDimensionFullPercent, roll) {
		return rollAffixIDsFromPool(fullPool, shuffle)
	}
	sixPool := affixSixDimensionPool(quality, config)
	if len(sixPool) > 0 && affixPercentRolled(config.Affix.SixDimensionPercent, roll) {
		return rollAffixIDsFromPool(sixPool, shuffle)
	}
	if affixPercentRolled(config.Affix.EmptyPercent, roll) {
		return nil
	}
	return rollAffixIDsWithShuffle(quality, shuffle)
}

// affixWashCostCoin 是洗词缀（20347）的固定收费：1000 金币。
//
// 这个价格来自客户端面板本身：FGUI 组件 ui://Strength/RefreshEquipUI 的 m_txtPrice
// 设计期文案就是「花费：1000金」，而 RefreshEquipUI 的全部 IL 只 set 过 m_txtBefore /
// m_txtAfter，从没给 m_txtPrice 赋过值，所以玩家看到的一直是这行字。
// 它不是 DLL 字符串，只存在于 FGUI 包里，读法见 文档/14 §7.3。
//
// 客户端 10000 个 coin 单位显示为 1 金币
// （HotfixView TabHelper.GetCoinFormat：金 = value/10000，银 = (value-金*10000)/100，
// 铜 = value%100），所以 1000 金币 = 10,000,000。该价格不写入配置文件。
const affixWashCostCoin int64 = 1000 * 10000

func (s *Server) onRefreshEquipAffix(ch *channel, req *protocol.C2M_RefreshEquipAffix) proto.Message {
	ss := ch.session
	resp := &protocol.M2C_RefreshEquipAffix{RpcId: req.RpcId}
	if ss == nil || ss.playerID == 0 {
		resp.Message = "请先登录"
		return resp
	}
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	it := ss.bag[req.BagIndex]
	if it == nil || !itemIsEquip(it.ItemId) {
		resp.Message = "只能对装备进行洗练！"
		return resp
	}
	// 词缀池为空才是配置故障；落空是合法的洗练结果，两者必须分开判断。
	if len(affixPoolForQuality(it.Quality)) == 0 {
		resp.Message = "装备词缀配置不可用"
		return resp
	}
	if ss.coin < affixWashCostCoin {
		resp.Message = "金币不足"
		return resp
	}
	// 落空与六维结果都要照常扣费：洗练本身不返还材料，也不因为结果不理想退款。
	it.AddAttrs = rollAffixWashIDs(it.Quality, affixWashConfigSnapshot(), rand.Float64, rand.Shuffle)
	ss.coin -= affixWashCostCoin
	s.saveData(ch)
	s.pushMoney(ch)
	s.pushBagSnapshot(ch)
	s.pushPlayerAttrs(ch)
	s.pushUnitCharacter(ch)
	log.Printf("[S=%d] refresh equip affix item=%d slot=%d cost=%d coin=%d affixes=%v",
		ch.id, it.ItemId, req.BagIndex, affixWashCostCoin, ss.coin, it.AddAttrs)
	return resp
}
