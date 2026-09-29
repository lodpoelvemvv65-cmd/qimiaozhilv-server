package main

import (
	"log"
	"math"
	"math/rand"
	"sort"

	"google.golang.org/protobuf/proto"

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

func manualAttributeIDs(rarity int32, count int) []int32 {
	if tables == nil || count <= 0 {
		return nil
	}
	// The online table contains six complete value tiers (101..126 through
	// 601..626). Material rarity selects increasingly strong tiers.
	tiers := [...]int32{1, 3, 5, 6}
	if rarity < 0 || int(rarity) >= len(tiers) {
		return nil
	}
	tier := tiers[rarity]
	var ids []int32
	for id, row := range tables.manulEquipAttribute {
		if int32(id/100) == tier && num(row["Key"]) > 0 {
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
	attributeCounts := [...]int{1, 2, 3, 3}
	it.RandomAttrs = manualAttributeIDs(rarity, attributeCounts[rarity])
	ss.bag[idx] = it
	s.saveData(ch)
	s.pushBagSnapshot(ch)
	log.Printf("[S=%d] manual equip made rawType=%d configID=%d rarity=%d item=%d slot=%d attrs=%v", ch.id, req.Type, configID, rarity, equipID, idx, it.RandomAttrs)
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
	if it.Level <= 0 {
		it.Level = 1
	}
	if it.Level >= 20 {
		return reject("装备已强化至最高等级")
	}
	row := tables.strengthen[int64(it.Level)]
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
		it.Level = int32(num(row["FailLevel"]))
		if it.Level < 1 {
			it.Level = 1
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
	row := tables.equipBase[int64(it.ItemId)]
	for _, attribute := range equipFieldToAttributeType {
		base := numf(row[attribute.field])
		if base != 0 {
			// EquipTrans.mainAttribute is a percentage delta. The client renders
			// base * (1 + delta), so sending an absolute attribute value here
			// applies the template value twice and produces wildly inflated stats.
			result[attribute.key] = float32(-0.2 + rand.Float64()*0.4)
		}
	}
	if len(result) == 0 {
		key := int32(num(row["SpecialKey"]))
		base := numf(row["SpecialValue"])
		if key > 0 && base != 0 {
			result[key] = float32(-0.2 + rand.Float64()*0.4)
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
	id    int32
	key   int32
	value float64
}

func rollAffixIDs(quality int32) []int32 {
	if tables == nil {
		return nil
	}
	byKey := make(map[int32][]affixCandidate)
	for id, row := range tables.equipAffix {
		arr := arrOf(row["AffixArr"])
		if len(arr) != 1 {
			continue
		}
		affix, _ := arr[0].(map[string]interface{})
		if affix == nil {
			continue
		}
		key := int32(num(affix["Key"]))
		if key <= 0 {
			continue
		}
		byKey[key] = append(byKey[key], affixCandidate{id: int32(id), key: key, value: math.Abs(numf(affix["Value"]))})
	}
	tier := int(quality) - 1
	if tier < 0 {
		tier = 0
	}
	var pool []affixCandidate
	for _, candidates := range byKey {
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].value < candidates[j].value })
		pick := tier
		if pick >= len(candidates) {
			pick = len(candidates) - 1
		}
		if pick >= 0 {
			pool = append(pool, candidates[pick])
		}
	}
	sort.Slice(pool, func(i, j int) bool { return pool[i].key < pool[j].key })
	rand.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	count := 1
	if quality >= 3 {
		count = 2
	}
	if quality >= 5 {
		count = 3
	}
	if count > len(pool) {
		count = len(pool)
	}
	ids := make([]int32, count)
	for i := range ids {
		ids[i] = pool[i].id
	}
	return ids
}

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
	it.AddAttrs = rollAffixIDs(it.Quality)
	if len(it.AddAttrs) == 0 {
		resp.Message = "装备词缀配置不可用"
		return resp
	}
	s.saveData(ch)
	s.pushBagSnapshot(ch)
	s.pushPlayerAttrs(ch)
	s.pushUnitCharacter(ch)
	log.Printf("[S=%d] refresh equip affix item=%d slot=%d affixes=%v", ch.id, it.ItemId, req.BagIndex, it.AddAttrs)
	return resp
}
