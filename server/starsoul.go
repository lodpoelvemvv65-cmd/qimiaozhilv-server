package main

import (
	"encoding/json"
	"log"
	"math"
	"sort"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

const (
	starSoulSlotCount = 12
	starSoulCapacity  = 1000
	starSoulFragment  = 20283
)

type starSoulItem struct {
	ID       int64     `json:"id"`
	TypeID   int32     `json:"type"`
	Level    int32     `json:"level"`
	Exp      int32     `json:"exp"`
	PosType  int32     `json:"pos"`
	Quality  int32     `json:"quality"`
	IsUsed   bool      `json:"used"`
	Main     int32     `json:"main"`
	Vice     []int32   `json:"vice,omitempty"`
	ViceAdd  []float32 `json:"vice_add,omitempty"`
	IsLocked bool      `json:"locked"`
}

type starSoulBag struct {
	Items map[int64]*starSoulItem `json:"items"`
	Used  []int64                 `json:"used"`
}

func newStarSoulBag() *starSoulBag {
	return &starSoulBag{Items: make(map[int64]*starSoulItem), Used: make([]int64, starSoulSlotCount)}
}

func (b *starSoulBag) normalize() *starSoulBag {
	if b == nil {
		return newStarSoulBag()
	}
	if b.Items == nil {
		b.Items = make(map[int64]*starSoulItem)
	}
	used := make([]int64, starSoulSlotCount)
	copy(used, b.Used)
	b.Used = used
	for _, it := range b.Items {
		if it != nil {
			it.IsUsed = false
		}
	}
	for slot, id := range b.Used {
		it := b.Items[id]
		if id == 0 || it == nil || it.PosType != int32(slot) {
			b.Used[slot] = 0
			continue
		}
		it.IsUsed = true
	}
	return b
}

func starSoulBagFromJSON(value string) *starSoulBag {
	b := newStarSoulBag()
	if value != "" {
		if err := json.Unmarshal([]byte(value), b); err != nil {
			log.Printf("invalid starsoul_json: %v", err)
			return newStarSoulBag()
		}
	}
	return b.normalize()
}

func starSoulBagToJSON(b *starSoulBag) string {
	b = b.normalize()
	if len(b.Items) == 0 {
		return ""
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return ""
	}
	return string(raw)
}

func (ss *session) ensureStarSoulBag() *starSoulBag {
	if ss.starSoul == nil {
		ss.starSoul = newStarSoulBag()
	}
	return ss.starSoul.normalize()
}

func (it *starSoulItem) netItem() *protocol.StarSoulNetItem {
	if it == nil {
		return &protocol.StarSoulNetItem{}
	}
	return &protocol.StarSoulNetItem{
		Id:       it.ID,
		TypeId:   it.TypeID,
		Level:    it.Level,
		Exp:      it.Exp,
		PosType:  it.PosType,
		Quality:  it.Quality,
		IsUsed:   it.IsUsed,
		Main:     it.Main,
		Vice:     append([]int32(nil), it.Vice...),
		ViceAdd:  append([]float32(nil), it.ViceAdd...),
		IsLocked: it.IsLocked,
	}
}

func (b *starSoulBag) netItems() []*protocol.StarSoulNetItem {
	ids := make([]int64, 0, len(b.Items))
	for id, it := range b.Items {
		if it != nil {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]*protocol.StarSoulNetItem, 0, len(ids))
	for _, id := range ids {
		out = append(out, b.Items[id].netItem())
	}
	return out
}

func (b *starSoulBag) suitKVs() []*protocol.KVInt32Int32 {
	counts := make(map[int32]int32)
	for _, id := range b.Used {
		if it := b.Items[id]; it != nil {
			counts[it.TypeID]++
		}
	}
	types := make([]int32, 0, len(counts))
	for typeID := range counts {
		types = append(types, typeID)
	}
	sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })
	out := make([]*protocol.KVInt32Int32, 0, len(types))
	for _, typeID := range types {
		count := counts[typeID]
		var suit int32
		if count >= 4 {
			suit = 1
		}
		if count >= 8 && tables != nil {
			if row := tables.starSoulType[int64(typeID)]; row != nil && num(row["Suit8Key"]) != 0 {
				suit = 3
			}
		}
		if suit != 0 {
			out = append(out, &protocol.KVInt32Int32{Key: typeID, Value: suit})
		}
	}
	return out
}

var starSoulIDSeq atomic.Int64

func nextStarSoulID() int64 {
	for {
		old := starSoulIDSeq.Load()
		next := time.Now().UnixNano()
		if next <= old {
			next = old + 1
		}
		if starSoulIDSeq.CompareAndSwap(old, next) {
			return next
		}
	}
}

func starSoulAttributeID(quality, key int32) int32 {
	if tables == nil {
		return 0
	}
	var fallback int32
	for id, row := range tables.starSoulAttribute {
		if int32(num(row["Type"])) != 1 || int32(num(row["Quality"])) != quality {
			continue
		}
		if fallback == 0 || int32(id) < fallback {
			fallback = int32(id)
		}
		if int32(num(row["Key"])) == key {
			return int32(id)
		}
	}
	return fallback
}

func starSoulPositionKeys(posType int32) []int32 {
	if tables == nil {
		return []int32{1}
	}
	row := tables.starSoulEquipAttr[int64(posType)]
	var keys []int32
	for _, value := range arrOf(row["AttributeTypeArr"]) {
		entry, _ := value.(map[string]interface{})
		if key := int32(num(entry["Key"])); key > 0 {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		keys = []int32{1}
	}
	return keys
}

func generateStarSoul(typeID, posType, quality int32) *starSoulItem {
	keys := starSoulPositionKeys(posType)
	it := &starSoulItem{
		ID:      nextStarSoulID(),
		TypeID:  typeID,
		PosType: posType,
		Quality: quality,
		Main:    starSoulAttributeID(quality, keys[0]),
	}
	viceCount := int(quality+1) / 2
	if viceCount > 3 {
		viceCount = 3
	}
	for i := 0; i < viceCount && i < len(keys); i++ {
		id := starSoulAttributeID(quality, keys[(i+1)%len(keys)])
		if id > 0 && id != it.Main {
			it.Vice = append(it.Vice, id)
			it.ViceAdd = append(it.ViceAdd, float32(i%3))
		}
	}
	return it
}

func bagItemCount(ss *session, itemID int32) int32 {
	var count int32
	if ss == nil {
		return 0
	}
	for _, it := range ss.bag {
		if it != nil && it.ItemId == itemID {
			count += it.Count
		}
	}
	return count
}

func (s *Server) pushStarSoulItem(ch *channel, id int64, it *starSoulItem) {
	s.sendPush(ch, protocol.OpM2C_SyncStarSoulBag, &protocol.M2C_SyncStarSoulBag{
		Id: id, Item: it.netItem(), ActorId: ch.session.playerID,
	})
}

func (s *Server) pushStarSoulUsed(ch *channel, slot int32) {
	b := ch.session.ensureStarSoulBag()
	s.sendPush(ch, protocol.OpM2C_SyncStarSoulBagItemUsed, &protocol.M2C_SyncStarSoulBagItemUsed{
		Key: slot, Value: b.Used[slot], SuitKVs: b.suitKVs(), ActorId: ch.session.playerID,
	})
}

func (s *Server) onGetStarSoulBag(ch *channel, req *protocol.C2M_GetStarSoulBag) proto.Message {
	b := ch.session.ensureStarSoulBag()
	log.Printf("[S=%d] get star soul bag actorId=%d items=%d usedSlots=%d", ch.id, req.ActorId, len(b.Items), len(b.Used))
	return &protocol.M2C_GetStarSoulBag{
		RpcId: req.RpcId, ItemList: b.netItems(), UsedIdMap: append([]int64(nil), b.Used...), SuitKVs: b.suitKVs(),
	}
}

func (s *Server) onGetStarSoulItem(ch *channel, req *protocol.C2M_GetStarSoulItem) proto.Message {
	resp := &protocol.M2C_GetStarSoulItem{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Message = "请先登录"
		return resp
	}
	typeID, posType := req.ConfigId/100, req.ConfigId%100
	if tables == nil || tables.starSoulType[int64(typeID)] == nil || posType < 0 || posType >= starSoulSlotCount {
		resp.Message = "星魂兑换配置不存在"
		return resp
	}
	b := ch.session.ensureStarSoulBag()
	if len(b.Items) >= starSoulCapacity {
		resp.Message = "星魂背包已满"
		return resp
	}
	if bagItemCount(ch.session, starSoulFragment) < 1 {
		resp.Message = "缺少星魂碎片 x1"
		log.Printf("[S=%d] exchange star soul rejected config=%d: no fragment", ch.id, req.ConfigId)
		return resp
	}
	consumeBagItem(ch.session, starSoulFragment, 1)
	item := generateStarSoul(typeID, posType, 4)
	if item.Main == 0 {
		ch.session.addItemToBag(starSoulFragment, 1)
		resp.Message = "星魂属性配置不存在"
		return resp
	}
	b.Items[item.ID] = item
	s.pushBagSnapshot(ch)
	s.pushStarSoulItem(ch, item.ID, item)
	s.saveData(ch)
	log.Printf("[S=%d] exchange star soul ok config=%d item=%d type=%d pos=%d quality=%d", ch.id, req.ConfigId, item.ID, typeID, posType, item.Quality)
	return resp
}

func (s *Server) onPutonStarSoulItem(ch *channel, req *protocol.C2M_PutonStarSoulItem) proto.Message {
	resp := &protocol.M2C_PutonStarSoulItem{RpcId: req.RpcId}
	b := ch.session.ensureStarSoulBag()
	it := b.Items[req.ItemId]
	if it == nil {
		resp.Message = "星魂不存在"
		return resp
	}
	if it.IsLocked {
		resp.Message = "锁定的星魂不能穿戴"
		return resp
	}
	if it.PosType < 0 || it.PosType >= starSoulSlotCount {
		resp.Message = "星魂部位无效"
		return resp
	}
	slot := it.PosType
	if old := b.Items[b.Used[slot]]; old != nil && old.ID != it.ID {
		old.IsUsed = false
		s.pushStarSoulItem(ch, old.ID, old)
	}
	b.Used[slot] = it.ID
	it.IsUsed = true
	s.pushStarSoulItem(ch, it.ID, it)
	s.pushStarSoulUsed(ch, slot)
	s.pushPlayerAttrs(ch)
	s.saveData(ch)
	return resp
}

func (s *Server) onLockStarSoulItem(ch *channel, req *protocol.C2M_LockStarSoulItem) {
	b := ch.session.ensureStarSoulBag()
	if it := b.Items[req.ItemId]; it != nil {
		it.IsLocked = req.IsLock
		s.pushStarSoulItem(ch, it.ID, it)
		s.saveData(ch)
	}
}

func starSoulLevelRow(quality, level int32) map[string]interface{} {
	if tables == nil || quality < 1 || quality > 6 || level < 0 || level > 20 {
		return nil
	}
	id := int64(10001 + (quality-1)*21 + level)
	return tables.starSoulLevel[id]
}

func (s *Server) onUpgradeStarSoulItem(ch *channel, req *protocol.C2M_UpgradeStarSoulItem) proto.Message {
	resp := &protocol.M2C_UpgradeStarSoulItem{RpcId: req.RpcId}
	b := ch.session.ensureStarSoulBag()
	target := b.Items[req.ItemId]
	if target == nil {
		resp.Message = "目标星魂不存在"
		return resp
	}
	if target.IsLocked || target.IsUsed {
		resp.Message = "锁定或已穿戴的星魂不能升级"
		return resp
	}
	if len(req.IdList) == 0 {
		resp.Message = "请选择升级材料"
		return resp
	}
	seen := make(map[int64]bool)
	var addExp int32
	var needCoin int64
	for _, id := range req.IdList {
		mat := b.Items[id]
		if id == target.ID || seen[id] || mat == nil || mat.IsLocked || mat.IsUsed {
			resp.Message = "升级材料无效"
			return resp
		}
		seen[id] = true
		row := starSoulLevelRow(mat.Quality, mat.Level)
		if row == nil {
			resp.Message = "星魂等级配置不存在"
			return resp
		}
		addExp += int32(num(row["Exp"]))
		needCoin += num(row["NeedCoin"])
	}
	if ch.session.coin < needCoin {
		resp.Message = "铜币不足"
		return resp
	}
	ch.session.coin -= needCoin
	for id := range seen {
		delete(b.Items, id)
		s.pushStarSoulItem(ch, id, nil)
	}
	target.Exp += addExp
	for target.Level < 20 {
		row := starSoulLevelRow(target.Quality, target.Level+1)
		if row == nil {
			break
		}
		need := int32(num(row["NeedExp"]))
		if need <= 0 || target.Exp < need {
			break
		}
		target.Exp -= need
		target.Level++
	}
	s.pushMoney(ch)
	s.pushStarSoulItem(ch, target.ID, target)
	s.saveData(ch)
	return resp
}

func (s *Server) resolveStarSouls(ch *channel, ids []int64) ([]*protocol.RewardItem, string) {
	b := ch.session.ensureStarSoulBag()
	seen := make(map[int64]bool)
	var fragments int32
	for _, id := range ids {
		it := b.Items[id]
		if seen[id] || it == nil || it.IsLocked || it.IsUsed {
			return nil, "包含不可分解的星魂"
		}
		seen[id] = true
		fragments += it.Quality
	}
	if len(seen) == 0 {
		return nil, "请选择要分解的星魂"
	}
	if ch.session.addItemToBag(starSoulFragment, fragments) < 0 {
		return nil, "背包已满"
	}
	for id := range seen {
		delete(b.Items, id)
		s.pushStarSoulItem(ch, id, nil)
	}
	s.pushBagSnapshot(ch)
	s.saveData(ch)
	return []*protocol.RewardItem{{Id: starSoulFragment, ItemType: protocol.ItemType_MaterialsItem, Count: fragments}}, ""
}

func (s *Server) onResolveStarSoul(ch *channel, req *protocol.C2M_ResolveStarSoul) proto.Message {
	resp := &protocol.M2C_ResolveStarSoul{RpcId: req.RpcId}
	resp.ItenList, resp.Message = s.resolveStarSouls(ch, req.Ids)
	return resp
}

func (s *Server) onResolveAllStarSoul(ch *channel, req *protocol.C2M_ResolveAllStarSoul) proto.Message {
	resp := &protocol.M2C_ResolveAllStarSoul{RpcId: req.RpcId}
	b := ch.session.ensureStarSoulBag()
	ids := make([]int64, 0, len(b.Items))
	for id, it := range b.Items {
		if it != nil && !it.IsLocked && !it.IsUsed {
			ids = append(ids, id)
		}
	}
	_, resp.Message = s.resolveStarSouls(ch, ids)
	return resp
}

func starSoulBonus(ss *session) map[int32]int32 {
	out := make(map[int32]int32)
	if ss == nil || tables == nil {
		return out
	}
	b := ss.ensureStarSoulBag()
	add := func(attributeID int32, scale float64) {
		row := tables.starSoulAttribute[int64(attributeID)]
		if row == nil {
			return
		}
		key := int32(num(row["Key"]))
		nt, ok := gemKeyToNumeric[key]
		if !ok {
			return
		}
		out[nt] += int32(math.Round(numf(row["Value"]) * scale))
	}
	for _, id := range b.Used {
		it := b.Items[id]
		if it == nil {
			continue
		}
		add(it.Main, math.Pow(1.1, float64(it.Level)))
		for i, attrID := range it.Vice {
			scale := 1.0
			if i < len(it.ViceAdd) {
				switch {
				case it.ViceAdd[i] < 1:
					scale = 1
				case it.ViceAdd[i] < 2:
					scale = 0.8
				default:
					scale = 0.48
				}
			}
			add(attrID, scale)
		}
	}
	return out
}

var _ proto.Message = (*protocol.M2C_GetStarSoulBag)(nil)
