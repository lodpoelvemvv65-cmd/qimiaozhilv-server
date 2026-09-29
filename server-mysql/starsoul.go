package main

import (
	"log"
	"math"
	"math/bits"
	"math/rand"
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
	// A fully-grown star soul exposes four distinct random (vice) attributes
	// in the client detail panel.  Lower quality souls unlock these slots as
	// they reach each fourth level; the first slots are still determined by
	// quality in starSoulInitialViceCount.
	starSoulMaxViceCount = 4
	// The online GetStarSoulItem confirmation asks for 30 star-soul
	// fragments per exchange. Keep this server-side so the client cannot
	// reduce the cost by forging the request.
	starSoulExchangeCost int32 = 30
)

type starSoulItem struct {
	ID        int64     `json:"id"`
	TypeID    int32     `json:"type"`
	Level     int32     `json:"level"`
	Exp       int32     `json:"exp"`
	PosType   int32     `json:"pos"`
	Quality   int32     `json:"quality"`
	IsUsed    bool      `json:"used"`
	Main      int32     `json:"main"`
	Vice      []int32   `json:"vice,omitempty"`
	ViceAdd   []float32 `json:"vice_add,omitempty"`
	ViceLevel int32     `json:"vice_level,omitempty"`
	IsLocked  bool      `json:"locked"`
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

func starSoulMainAttributeID(quality, key int32) int32 {
	if tables == nil {
		return 0
	}
	for id, row := range tables.starSoulAttribute {
		if int32(num(row["Type"])) != 1 || int32(num(row["Quality"])) != quality {
			continue
		}
		if int32(num(row["Key"])) == key {
			return int32(id)
		}
	}
	return 0
}

type starSoulAttributeCandidate struct {
	attributeID int32
	weight      int
}

func starSoulMainCandidates(posType, quality int32) []starSoulAttributeCandidate {
	if tables == nil {
		return nil
	}
	row := tables.starSoulEquipAttr[int64(posType)]
	var candidates []starSoulAttributeCandidate
	for _, value := range arrOf(row["AttributeTypeArr"]) {
		entry, _ := value.(map[string]interface{})
		attributeID := starSoulMainAttributeID(quality, int32(num(entry["Key"])))
		if attributeID <= 0 {
			continue
		}
		weight := int(num(entry["Prob"]))
		if weight <= 0 {
			weight = 1
		}
		candidates = append(candidates, starSoulAttributeCandidate{attributeID: attributeID, weight: weight})
	}
	return candidates
}

func pickStarSoulMain(candidates []starSoulAttributeCandidate, intn func(int) int) int32 {
	total := 0
	for _, candidate := range candidates {
		total += candidate.weight
	}
	if total <= 0 {
		return 0
	}
	roll := intn(total)
	for _, candidate := range candidates {
		if roll < candidate.weight {
			return candidate.attributeID
		}
		roll -= candidate.weight
	}
	return 0
}

func starSoulViceAttributeID(key int32) int32 {
	if tables == nil {
		return 0
	}
	for id, row := range tables.starSoulAttribute {
		if int32(num(row["Type"])) == 2 && int32(num(row["Key"])) == key {
			return int32(id)
		}
	}
	return 0
}

func starSoulViceCandidates() []starSoulAttributeCandidate {
	if tables == nil {
		return nil
	}
	candidates := make([]starSoulAttributeCandidate, 0)
	for id, row := range tables.starSoulAttribute {
		if int32(num(row["Type"])) != 2 || int32(num(row["Key"])) <= 0 {
			continue
		}
		weight := int(num(row["ViceWeight"]))
		if weight <= 0 {
			weight = 1
		}
		candidates = append(candidates, starSoulAttributeCandidate{attributeID: int32(id), weight: weight})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].attributeID < candidates[j].attributeID })
	return candidates
}

func pickStarSoulVice(candidates []starSoulAttributeCandidate, used map[int32]bool, intn func(int) int) int32 {
	total := 0
	for _, candidate := range candidates {
		if !used[candidate.attributeID] {
			total += candidate.weight
		}
	}
	if total <= 0 {
		return 0
	}
	roll := intn(total)
	for _, candidate := range candidates {
		if used[candidate.attributeID] {
			continue
		}
		if roll < candidate.weight {
			return candidate.attributeID
		}
		roll -= candidate.weight
	}
	return 0
}

func starSoulInitialViceCount(quality int32) int {
	count := int(quality+1) / 2
	if count < 1 {
		count = 1
	}
	if count > 3 {
		count = 3
	}
	return count
}

func normalizeStarSoulVice(it *starSoulItem) bool {
	if it == nil {
		return false
	}
	changed := false
	if tables != nil {
		for index, attributeID := range it.Vice {
			row := tables.starSoulAttribute[int64(attributeID)]
			if row == nil || int32(num(row["Type"])) == 2 {
				continue
			}
			if replacement := starSoulViceAttributeID(int32(num(row["Key"]))); replacement > 0 && replacement != attributeID {
				it.Vice[index] = replacement
				changed = true
			}
		}
	}
	if len(it.ViceAdd) > len(it.Vice) {
		it.ViceAdd = append([]float32(nil), it.ViceAdd[:len(it.Vice)]...)
		changed = true
	}
	for len(it.ViceAdd) < len(it.Vice) {
		it.ViceAdd = append(it.ViceAdd, 2)
		changed = true
	}
	for index, value := range it.ViceAdd {
		clamped := float32(math.Round(float64(value)))
		if clamped < 0 {
			clamped = 0
		}
		if clamped > 2 {
			clamped = 2
		}
		if clamped != value {
			it.ViceAdd[index] = clamped
			changed = true
		}
	}
	return changed
}

func initializeStarSoulVice(it *starSoulItem, intn func(int) int) bool {
	if it == nil {
		return false
	}
	candidates := starSoulViceCandidates()
	target := starSoulInitialViceCount(it.Quality)
	if target > len(candidates) {
		target = len(candidates)
	}
	used := make(map[int32]bool, len(it.Vice))
	for _, attributeID := range it.Vice {
		used[attributeID] = true
	}
	changed := false
	for len(used) < target {
		attributeID := pickStarSoulVice(candidates, used, intn)
		if attributeID <= 0 {
			break
		}
		used[attributeID] = true
		it.Vice = append(it.Vice, attributeID)
		it.ViceAdd = append(it.ViceAdd, float32(intn(3)))
		changed = true
	}
	return changed
}

func growStarSoulViceWithMax(it *starSoulItem, intn func(int) int, maxViceCount int) bool {
	if it == nil {
		return false
	}
	if maxViceCount < 1 {
		maxViceCount = 1
	}
	candidates := starSoulViceCandidates()
	used := make(map[int32]bool, len(it.Vice))
	for _, attributeID := range it.Vice {
		used[attributeID] = true
	}
	// A fourth slot is unlocked by level growth.  Keep direct callers (and
	// level-zero items) on the tier-improvement path until the item has
	// actually reached level 4; applyStarSoulViceGrowth fills a missed slot
	// for legacy saves whose ViceLevel was already advanced.
	if len(it.Vice) < maxViceCount && it.Level >= 4 {
		if attributeID := pickStarSoulVice(candidates, used, intn); attributeID > 0 {
			it.Vice = append(it.Vice, attributeID)
			it.ViceAdd = append(it.ViceAdd, 0)
			return true
		}
	}
	var improvable []int
	for index, value := range it.ViceAdd {
		if value < 2 {
			improvable = append(improvable, index)
		}
	}
	if len(improvable) == 0 {
		return false
	}
	index := improvable[intn(len(improvable))]
	it.ViceAdd[index]++
	return true
}

func growStarSoulVice(it *starSoulItem, intn func(int) int) bool {
	return growStarSoulViceWithMax(it, intn, starSoulMaxViceCount)
}

func starSoulViceTargetCount(quality, level int32) int {
	target := starSoulInitialViceCount(quality)
	if level > 0 {
		target += int(level / 4)
	}
	if target > starSoulMaxViceCount {
		target = starSoulMaxViceCount
	}
	return target
}

func applyStarSoulViceGrowthWithMax(it *starSoulItem, intn func(int) int, maxViceCount int) bool {
	if it == nil {
		return false
	}
	if maxViceCount < 1 {
		maxViceCount = 1
	}
	changed := normalizeStarSoulVice(it)
	if initializeStarSoulVice(it, intn) {
		changed = true
	}
	processedLevel := it.ViceLevel / 4 * 4
	if processedLevel < 0 {
		processedLevel = 0
	}
	targetLevel := it.Level / 4 * 4
	if targetLevel > 20 {
		targetLevel = 20
	}
	for level := processedLevel + 4; level <= targetLevel; level += 4 {
		growStarSoulViceWithMax(it, intn, maxViceCount)
	}
	// Saves produced before the four-attribute rule may already record a
	// processed level of 20 while carrying only three vice attributes.  Fill
	// the slots implied by the current level without replaying old tier rolls.
	targetCount := starSoulInitialViceCount(it.Quality)
	if targetLevel > 0 {
		targetCount += int(targetLevel / 4)
	}
	if targetCount > maxViceCount {
		targetCount = maxViceCount
	}
	for len(it.Vice) < targetCount {
		if !growStarSoulViceWithMax(it, intn, maxViceCount) {
			break
		}
		changed = true
	}
	if it.ViceLevel != targetLevel {
		it.ViceLevel = targetLevel
		changed = true
	}
	return changed
}

func applyStarSoulViceGrowth(it *starSoulItem, intn func(int) int) bool {
	return applyStarSoulViceGrowthWithMax(it, intn, starSoulMaxViceCount)
}

// legacyStarSoulRepairSeed is the pre-2026-08-29 migration seed.  It is kept
// solely to recognize data already persisted with the old correlated seed.
// The old ID^position mix cancels the low bits when consecutive IDs are
// assigned to consecutive positions, so several slots received identical
// vice attributes.
func legacyStarSoulRepairSeed(it *starSoulItem) int64 {
	if it == nil {
		return 1
	}
	seed := it.ID ^ int64(uint64(uint32(it.TypeID))<<32|uint64(uint32(it.PosType)))
	seed ^= int64(it.Quality) * -7046029254386353131
	if seed == 0 {
		return 1
	}
	return seed
}

// starSoulRepairSeed returns a stable, per-instance seed used only when
// repairing old saves.  Every identity component is mixed independently
// before the final avalanche; in particular, position no longer cancels the
// low bits of a sequential instance ID.
func starSoulRepairSeed(it *starSoulItem) int64 {
	if it == nil {
		return 1
	}
	seed := uint64(it.ID) + 0x9e3779b97f4a7c15
	seed ^= uint64(uint32(it.TypeID)) * 0xbf58476d1ce4e5b9
	seed = bits.RotateLeft64(seed, 23)
	seed ^= uint64(uint32(it.PosType)) * 0x94d049bb133111eb
	seed = bits.RotateLeft64(seed, 29)
	seed ^= uint64(uint32(it.Quality)) * 0x369dea0f31a53f85
	seed ^= seed >> 30
	seed *= 0xbf58476d1ce4e5b9
	seed ^= seed >> 27
	seed *= 0x94d049bb133111eb
	seed ^= seed >> 31
	if seed == 0 {
		return 1
	}
	return int64(seed)
}

func sameInt32Values(left, right []int32) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sameFloat32Values(left, right []float32) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// starSoulViceForSeed reproduces the deterministic repair result from a
// clean vice list.  It is used to identify saves produced by the old broken
// seed without rerolling otherwise valid player-generated attributes.
func starSoulViceForSeed(it *starSoulItem, seed int64) ([]int32, []float32) {
	return starSoulViceForSeedWithMax(it, seed, starSoulMaxViceCount)
}

func starSoulViceForSeedWithMax(it *starSoulItem, seed int64, maxViceCount int) ([]int32, []float32) {
	if it == nil {
		return nil, nil
	}
	copyItem := *it
	copyItem.Vice = nil
	copyItem.ViceAdd = nil
	copyItem.ViceLevel = 0
	rng := rand.New(rand.NewSource(seed))
	applyStarSoulViceGrowthWithMax(&copyItem, rng.Intn, maxViceCount)
	return copyItem.Vice, copyItem.ViceAdd
}

func hasLegacyStarSoulVice(it *starSoulItem) bool {
	if it == nil || len(it.Vice) == 0 {
		return false
	}
	// The legacy migration generated at most three vice attributes.  Recreate
	// that exact output so only those deterministic saves are rerolled after
	// enabling the fourth slot.
	vice, viceAdd := starSoulViceForSeedWithMax(it, legacyStarSoulRepairSeed(it), 3)
	return sameInt32Values(it.Vice, vice) && sameFloat32Values(it.ViceAdd, viceAdd)
}

func ensureStarSoulViceGrowth(bag *starSoulBag) bool {
	if bag == nil {
		return false
	}
	changed := false
	for _, it := range bag.Items {
		if it == nil {
			continue
		}
		if hasLegacyStarSoulVice(it) {
			// Clear only the exact deterministic output of the old migration. A
			// normal random roll that happens to use a similar attribute set is
			// left untouched unless it matches both IDs and tier values.
			it.Vice = nil
			it.ViceAdd = nil
			it.ViceLevel = 0
			changed = true
		}
		rng := rand.New(rand.NewSource(starSoulRepairSeed(it)))
		if applyStarSoulViceGrowth(it, rng.Intn) {
			changed = true
		}
	}
	return changed
}

func generateStarSoulWithRand(typeID, posType, quality int32, intn func(int) int) *starSoulItem {
	it := &starSoulItem{
		ID:      nextStarSoulID(),
		TypeID:  typeID,
		PosType: posType,
		Quality: quality,
		Main:    pickStarSoulMain(starSoulMainCandidates(posType, quality), intn),
	}
	initializeStarSoulVice(it, intn)
	return it
}

func generateStarSoul(typeID, posType, quality int32) *starSoulItem {
	return generateStarSoulWithRand(typeID, posType, quality, rand.Intn)
}

func starSoulExchangeQuality() int32 {
	// The fragment's MaterialBase quality describes the material itself.  It
	// is not the quality of the item produced by the 30-fragment exchange.
	// The client confirmation calls the result a 天工 star soul, which is
	// quality 6 in StarSoulAttribute/StarSoulLevel tables.
	return 6
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
	fragmentCount := bagItemCount(ch.session, starSoulFragment)
	if fragmentCount < starSoulExchangeCost {
		resp.Message = "缺少星魂碎片 x30"
		log.Printf("[S=%d] exchange star soul rejected config=%d: fragments=%d need=%d", ch.id, req.ConfigId, fragmentCount, starSoulExchangeCost)
		return resp
	}
	quality := starSoulExchangeQuality()
	if quality == 0 {
		resp.Message = "星魂兑换品质配置不存在"
		return resp
	}
	if !consumeBagItem(ch.session, starSoulFragment, starSoulExchangeCost) {
		// bagItemCount above makes this unreachable in normal execution; keep
		// the guard so a concurrent mutation can never mint an item for free.
		resp.Message = "缺少星魂碎片 x30"
		return resp
	}
	item := generateStarSoul(typeID, posType, quality)
	if item.Main == 0 {
		ch.session.addItemToBag(starSoulFragment, starSoulExchangeCost)
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
	if b.Used[slot] == it.ID {
		// 20406 is used by the native UI for both equip and unequip. Clicking
		// the equipped item again clears its slot.
		b.Used[slot] = 0
		it.IsUsed = false
		s.pushStarSoulItem(ch, it.ID, it)
		s.pushStarSoulUsed(ch, slot)
		s.pushPlayerAttrs(ch)
		s.saveData(ch)
		return resp
	}
	if old := b.Items[b.Used[slot]]; old != nil {
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
	applyStarSoulViceGrowth(target, rand.Intn)
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

func starSoulBonus(ss *session) map[int32]float64 {
	out := make(map[int32]float64)
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
		out[nt] += numf(row["Value"]) * scale
	}
	for _, id := range b.Used {
		it := b.Items[id]
		if it == nil {
			continue
		}
		add(it.Main, math.Pow(1.1, float64(it.Level)))
		for i, attrID := range it.Vice {
			viceAdd := float64(0)
			if i < len(it.ViceAdd) {
				viceAdd = float64(it.ViceAdd[i])
			}
			add(attrID, (1+viceAdd)*starSoulQualityCoefficient(it.Quality))
		}
	}
	for numeric, value := range starSoulSuitBonus(ss) {
		if numeric == 1042 || numeric == 1043 || numeric == 1044 {
			out[numeric] += value
		}
	}
	return out
}

func starSoulQualityCoefficient(quality int32) float64 {
	switch {
	case quality <= 2:
		return 0.48
	case quality <= 4:
		return 0.8
	default:
		return 1
	}
}

var starSoulSuitPercentNumerics = map[int32][]int32{
	32: {1009, 1010},
	33: {1011, 1012},
	34: {1002},
	35: {1004},
	36: {1013, 1014},
	37: {1015, 1016},
	38: {1018, 1020},
	39: {1019, 1021},
}

func starSoulSuitBonus(ss *session) map[int32]float64 {
	out := make(map[int32]float64)
	if ss == nil || tables == nil {
		return out
	}
	bag := ss.ensureStarSoulBag()
	counts := make(map[int32]int)
	for _, id := range bag.Used {
		if item := bag.Items[id]; item != nil {
			counts[item.TypeID]++
		}
	}
	add := func(key int32, value float64) {
		if numeric, ok := gemKeyToNumeric[key]; ok {
			out[numeric] += value
		}
		for _, numeric := range starSoulSuitPercentNumerics[key] {
			out[numeric] += value
		}
	}
	for typeID, count := range counts {
		row := tables.starSoulType[int64(typeID)]
		if row == nil {
			continue
		}
		if count >= 4 {
			add(int32(num(row["Suit4Key"])), numf(row["Suit4Value"]))
		}
		if count >= 8 {
			add(int32(num(row["Suit8Key"])), numf(row["Suit8Value"]))
		}
	}
	return out
}

func starSoulSuitPercent(ss *session, numeric int32) float64 {
	for _, numerics := range starSoulSuitPercentNumerics {
		for _, candidate := range numerics {
			if candidate == numeric {
				return starSoulSuitBonus(ss)[numeric]
			}
		}
	}
	return 0
}

var _ proto.Message = (*protocol.M2C_GetStarSoulBag)(nil)
