package main

import (
	"fmt"
	"log"
	"math/rand"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// ===================== 背包数据结构 =====================
// 协议（Hotfix.dll 反汇编，见 11 文档）：
//   BagMap{Index=1, NetItem=2, EquipTransMessage=3}
//   NetItem{ItemId=1, ItemType=2, ServerId=3, Count=4, IsLock=5, GetSource=6, Description=7}
//   EquipTransMessage{EquipId=1, specialKey=3, IsLocked=4, mainAttribute=5,
//                     randomAttributes=6, Star=7, Quality=8, Level=9, specialId=10}
// ItemType 枚举：0=NoneItem 1=EquipItem 2=GoodsItem 3=MaterialsItem。
// 客户端 BagUI 固定创建 48 个格子，并直接用 BagMap.Index 调 GetChildAt；
// 因此背包格子只能是 0..47。穿戴槽位 Index = EquipBase.Type。

const (
	bagSlotCount   int32 = 48
	invalidBagSlot int32 = -1
)

func visibleBagItemCount(ss *session) int {
	if ss == nil {
		return 0
	}
	count := 0
	for index, item := range ss.bag {
		if index >= 0 && item != nil && !isStarCoinItem(item.ItemId) {
			count++
		}
	}
	return count
}

type bagItem struct {
	ItemId            int32             `json:"i"` // EquipBase/GoodsBase/MaterialBase 模板 id
	ItemType          int32             `json:"t"` // 1=装备 2=物品 3=材料（宝石也是材料）
	ServerId          int64             `json:"s"` // 实例 id（唯一）
	Count             int32             `json:"c"`
	IsLock            bool              `json:"l"`
	Quality           int32             `json:"q"`           // 装备品质
	Star              int32             `json:"r"`           // 装备星级
	Level             int32             `json:"v"`           // 强化等级
	SpecialKey        int32             `json:"k,omitempty"` // EquipBase.SpecialKey（AttributeType）
	SpecialId         int32             `json:"p,omitempty"` // 特殊实例 id（宝石/附魔预留）
	MainAttr          map[int32]float32 `json:"m,omitempty"` // 洗练主属性增量（AttributeType → Value）
	RandomAttrs       []int32           `json:"x,omitempty"` // ManulEquipAttribute id 列表
	AddAttrs          []int32           `json:"a,omitempty"` // EquipAffixConfig id 列表
	GemList           []int32           `json:"g,omitempty"` // 已镶嵌宝石 ItemId（槽位 0..MaxHole-1）
	GetSource         string            `json:"o,omitempty"` // 客户端详情“来源”；手工装备记录制作者角色名
	PurchaseSource    int32             `json:"-"`
	PurchaseCurrency  int32             `json:"-"`
	PurchaseUnitPrice int64             `json:"-"`
}

// defaultEquipmentSource keeps the client equipment detail panel useful for
// legacy and generic reward paths that do not carry a more specific source.
// Context-aware grant paths overwrite this value (for example, "普通商店"
// or "任务奖励").
const defaultEquipmentSource = "系统获得"

// 初始背包：线上不给初始装备，新手装备来自主线任务 10011 奖励
// （马桶塞 120590~120593 按职业 + 药水），grantTaskRewards 真实入包（task.go）。
var starterEquip = map[int32][]int32{} // 预留：如需兜底初始装备按职业填

const titleEquipSlot int32 = 4

// newBagItem：从 EquipBase/GoodsBase/MaterialBase 构造背包物品。
func newBagItem(itemID int32) *bagItem {
	it := &bagItem{ItemId: itemID, Count: 1}
	if tables != nil {
		if eb, ok := tables.equipBase[int64(itemID)]; ok {
			it.ItemType = 1
			it.GetSource = defaultEquipmentSource
			it.Quality = int32(num(eb["Quality"]))
			it.Star = int32(num(eb["Star"]))
			// Equipment enters the bag at the online +0 baseline.  The
			// Strengthentable row for level 1 is the first +0 -> +1 attempt;
			// writing 1 here made every shop/reward item appear as +1 and also
			// applied the level-1 special-value multiplier before any strengthening.
			it.Level = 0
			it.SpecialKey = int32(num(eb["SpecialKey"]))
		} else if gb, ok := tables.goodsBase[int64(itemID)]; ok {
			_ = gb
			it.ItemType = 2
		} else {
			it.ItemType = 3 // 材料（MaterialBase）
		}
	}
	// Keep the wire representation aligned with EquipBase.MaxHole even for
	// newly created instances. The client uses GemList length to create the
	// visible sockets, including empty sockets represented by zero.
	repairBagItem(it)
	ensureEquipmentVariation(it)
	// Empty affixes are intentional for newly granted equipment.  The native
	// client only adds these rows through the explicit affix-washing action;
	// login repair must preserve an empty list instead of manufacturing rows.
	if it.ItemType == 1 && len(it.AddAttrs) > 0 {
		ensureEquipmentAffixCount(it)
	}
	return it
}

func ensureEquipmentVariation(it *bagItem) bool {
	if it == nil || it.ItemType != 1 || len(it.MainAttr) > 0 {
		return false
	}
	rolled := rollMainAttributes(it)
	if len(rolled) == 0 {
		return false
	}
	it.MainAttr = rolled
	return true
}

// ensureEquipmentAffixCount repairs equipment instances created before the
// quality-color affix rule was enforced. Existing valid affixes are retained;
// missing entries are filled from the quality-tier pool in stable order so a
// login migration never rerolls a player's attributes. Excess or invalid
// entries are removed down to the 1/2/3 slot count driven by the quality color
// (the client renders however many attribute ids the server sends; it has no
// slot rule of its own, and shows 无词缀 for an empty list).
func ensureEquipmentAffixCount(it *bagItem) bool {
	if it == nil || it.ItemType != 1 || tables == nil {
		return false
	}
	// 洗词缀的六维结果（含满六维）是一次洗练的完整产出，只有 1 条，本来就少于
	// 品质槽位；六维词条也不在普通品质池里。任何“补齐到 1/2/3 条”的修复都会覆盖
	// 玩家的洗练结果，所以命中六维就整件跳过登录修复。
	if affixIDsContainSixDimension(it.AddAttrs) {
		return false
	}
	pool := affixPoolForQuality(it.Quality)
	if len(pool) == 0 {
		return false
	}
	target := equipmentBonusCount(it.Quality)
	if target > len(pool) {
		target = len(pool)
	}
	valid := make(map[int32]bool, len(pool))
	for _, candidate := range pool {
		valid[candidate.id] = true
	}
	ids := make([]int32, 0, target)
	seen := make(map[int32]bool, target)
	for _, id := range it.AddAttrs {
		if len(ids) >= target || seen[id] || !valid[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	for _, candidate := range pool {
		if len(ids) >= target {
			break
		}
		if !seen[candidate.id] {
			seen[candidate.id] = true
			ids = append(ids, candidate.id)
		}
	}
	if len(ids) == len(it.AddAttrs) {
		unchanged := true
		for i := range ids {
			if ids[i] != it.AddAttrs[i] {
				unchanged = false
				break
			}
		}
		if unchanged {
			return false
		}
	}
	it.AddAttrs = ids
	return true
}

func ensureEquipmentVariations(itemMaps ...map[int32]*bagItem) bool {
	changed := false
	for _, items := range itemMaps {
		for _, item := range items {
			if repairBagItem(item) {
				changed = true
			}
			if repairLegacyEquipmentAttributes(item) {
				changed = true
			}
			if ensureEquipmentVariation(item) {
				changed = true
			}
			// Do not fill an empty affix list during login.  Existing affixes are
			// still normalized when present, which keeps old malformed entries
			// from leaking onto the wire without changing online blank gear.
			if len(item.AddAttrs) > 0 && ensureEquipmentAffixCount(item) {
				changed = true
			}
		}
	}
	return changed
}

// repairBagItem aligns legacy item types with the current templates and fills
// missing equipment fields. Returning true makes login persist each repair.
func repairBagItem(it *bagItem) bool {
	if it == nil || tables == nil {
		return false
	}
	changed := false
	if eb, ok := tables.equipBase[int64(it.ItemId)]; ok {
		if it.GetSource == "" {
			it.GetSource = defaultEquipmentSource
			changed = true
		}
		if it.ItemType != 1 {
			it.ItemType = 1 // 旧存档兜底：EquipBase 物品一律按装备
			changed = true
		}
		if it.SpecialKey == 0 {
			it.SpecialKey = int32(num(eb["SpecialKey"]))
			changed = it.SpecialKey != 0 || changed
		}
		if it.Quality == 0 {
			it.Quality = int32(num(eb["Quality"]))
			changed = it.Quality != 0 || changed
		}
		if it.Star == 0 {
			it.Star = int32(num(eb["Star"]))
			changed = it.Star != 0 || changed
		}
		// Empty sockets are added while encoding the wire message. Keeping them
		// out of the in-memory item makes rejected requests side-effect free.
		if maxHole := int(num(eb["MaxHole"])); maxHole > 0 && len(it.GemList) > maxHole {
			it.GemList = append([]int32(nil), it.GemList[:maxHole]...)
			changed = true
		}
		return changed
	}
	if _, ok := tables.goodsBase[int64(it.ItemId)]; ok {
		if it.ItemType != int32(protocol.ItemType_GoodsItem) {
			it.ItemType = int32(protocol.ItemType_GoodsItem)
			return true
		}
		return false
	}
	if _, ok := tables.materialBase[int64(it.ItemId)]; ok && it.ItemType != int32(protocol.ItemType_MaterialsItem) {
		it.ItemType = int32(protocol.ItemType_MaterialsItem)
		return true
	}
	return false
}

// equipSlotForItem returns the canonical character-panel slot from EquipBase.Type.
// Weapons omit Type in the JSON table, so the enum's default value 0 is their slot.
func equipSlotForItem(it *bagItem) int32 {
	if it == nil || tables == nil {
		return -1
	}
	repairBagItem(it)
	eb, ok := tables.equipBase[int64(it.ItemId)]
	if !ok {
		return -1
	}
	slot := int32(num(eb["Type"]))
	if slot < 0 || slot > 11 {
		return -1
	}
	return slot
}

// normalizeWornSlots repairs old/manual grants whose map key did not match
// EquipBase.Type. The client renders WornBagMapList by this key, so keeping
// the canonical slot prevents armor from appearing in the wrong UI position.
func normalizeWornSlots(ss *session) bool {
	if ss == nil || len(ss.worn) == 0 || tables == nil {
		return false
	}
	type move struct {
		from int32
		to   int32
		item *bagItem
	}
	var moves []move
	reserved := make(map[int32]bool)
	for from, it := range ss.worn {
		to := equipSlotForItem(it)
		if to < 0 || to == from {
			continue
		}
		if _, occupied := ss.worn[to]; occupied || reserved[to] {
			continue
		}
		reserved[to] = true
		moves = append(moves, move{from: from, to: to, item: it})
	}
	for _, m := range moves {
		delete(ss.worn, m.from)
		ss.worn[m.to] = m.item
	}
	return len(moves) > 0
}

// initBag：初始化背包/穿戴容器（新号空背包，物品经任务奖励/掉落入包）。
func initBag(ss *session) {
	ss.bag = make(map[int32]*bagItem)
	ss.worn = make(map[int32]*bagItem)
}

func cloneBag(m map[int32]*bagItem) map[int32]*bagItem {
	out := make(map[int32]*bagItem, len(m))
	for index, item := range m {
		if item == nil {
			continue
		}
		copyItem := *item
		if item.MainAttr != nil {
			copyItem.MainAttr = make(map[int32]float32, len(item.MainAttr))
			for key, value := range item.MainAttr {
				copyItem.MainAttr[key] = value
			}
		}
		copyItem.RandomAttrs = append([]int32(nil), item.RandomAttrs...)
		copyItem.AddAttrs = append([]int32(nil), item.AddAttrs...)
		copyItem.GemList = append([]int32(nil), item.GemList...)
		out[index] = &copyItem
	}
	return out
}

// normalizeBagSlots 把旧服务端产生的 1..200 背包下标压紧到客户端合法的
// 0..47。返回 true 表示存档需要回写。正常存档不会超过 48 项；超过容量的
// 数据保留原样并记录日志，避免迁移时静默丢失物品。
func normalizeBagSlots(ss *session) bool {
	if ss == nil || len(ss.bag) == 0 {
		return false
	}
	changed := canonicalizeStarCoins(ss)
	keys := make([]int, 0, len(ss.bag))
	for index, item := range ss.bag {
		if item != nil && !isStarCoinItem(item.ItemId) {
			keys = append(keys, int(index))
		}
	}
	if len(keys) > int(bagSlotCount) {
		log.Printf("[P=%d] cannot normalize bag with %d visible entries into %d client slots", ss.playerID, len(keys), bagSlotCount)
		return changed
	}
	sort.Ints(keys)
	normalized := make(map[int32]*bagItem, len(keys))
	changed = changed || len(keys) != len(ss.bag)
	for index, oldIndex := range keys {
		newIndex := int32(index)
		normalized[newIndex] = ss.bag[int32(oldIndex)]
		if newIndex != int32(oldIndex) {
			changed = true
		}
	}
	// Keep the canonical non-rendered currency stacks alongside the visible
	// slots when rebuilding the map.
	for index, item := range ss.bag {
		if item != nil && isStarCoinItem(item.ItemId) && index < 0 {
			normalized[index] = item
		}
	}
	if changed {
		ss.bag = normalized
	}
	return changed
}

// pruneInvalidBagItems 清掉背包/穿戴里客户端无法识别的物品：
// 消耗品必须在 GoodsBase 表，装备必须在 EquipBase 表，材料必须在 MaterialBase 表；
// 三表都不在 → 客户端背包渲染找不到配置直接 NRE 崩溃（Player.log 实证
// `GoodsBase == null where Id = 20001` → UpdateBagUIEvent.cs:80）。
// 注：MaterialBase 里非宝石的普通材料（20001 素材等）也允许在背包——客户端背包
// 渲染走 MaterialBase（ItemType=3 材料）；但客户端只显示图标，功能（镶嵌/打造）
// 需要对应 MaterialBase.MaterialType==2（宝石）等。
func pruneInvalidBagItems(ss *session) {
	if ss == nil || tables == nil {
		return
	}
	valid := func(itemID int32) bool {
		if _, ok := tables.goodsBase[int64(itemID)]; ok {
			return true
		}
		if _, ok := tables.equipBase[int64(itemID)]; ok {
			return true
		}
		if _, ok := tables.materialBase[int64(itemID)]; ok {
			return true
		}
		return false
	}
	removed := 0
	for idx, it := range ss.bag {
		if it == nil {
			continue
		}
		if !valid(it.ItemId) {
			delete(ss.bag, idx)
			removed++
		}
	}
	for idx, it := range ss.worn {
		if it == nil {
			continue
		}
		if !valid(it.ItemId) {
			delete(ss.worn, idx)
			removed++
		}
	}
	if removed > 0 {
		log.Printf("[P=%d] pruned %d invalid bag items (not in GoodsBase/EquipBase/MaterialBase)", ss.playerID, removed)
	}
}

// nextBagIndex：背包下一个空位。
func nextBagIndex(ss *session) int32 {
	if ss == nil {
		return invalidBagSlot
	}
	for i := int32(0); i < bagSlotCount; i++ {
		if _, ok := ss.bag[i]; !ok {
			return i
		}
	}
	return invalidBagSlot
}

// sendRawPush：推送预编码 protobuf 字节（手工 List 字段用）。
func (s *Server) sendRawPush(ch *channel, opcode uint16, body []byte) {
	if ch == nil || ch.session == nil || ch.session.superseded.Load() {
		return
	}
	s.SendToChannel(ch, packOuter(opcode, body))
	log.Printf("[S=%d] push opcode=%d len=%d", ch.id, opcode, len(body))
}

// ===================== 角色信息 20251 → 20252 =====================
// 客户端 CharacterUI.AwakeAsync 发 C2M_GetCharacter{Id} → await M2C_GetCharacter
// （UnitCharacter + WornBagMapList），随后 ClientUnitCharacterComponent.Update
// + ShowNumText 刷新角色面板。点击其他玩家菜单"角色"也走这里（Id=对方）。
func (s *Server) onGetCharacter(ch *channel, req *protocol.C2M_GetCharacter) proto.Message {
	ss := ch.session
	pid := req.Id
	if pid == 0 {
		pid = ss.playerID
	}
	resp := &protocol.M2C_GetCharacter{RpcId: req.RpcId}
	target := ss
	if pid != ss.playerID {
		// 查看其他玩家：在线才可查看（用对方 session 构造角色信息）
		tc := s.findChannelByPlayerID(pid)
		if tc == nil || tc.session == nil {
			resp.Error, resp.Message = errBadParam, "玩家不在线"
			return resp
		}
		target = tc.session
	}
	base, err := proto.Marshal(resp)
	if err != nil {
		resp.Error, resp.Message = errBadParam, "序列化失败"
		return resp
	}
	// UnitCharacter 字段（proto 已含 tag=1）
	uc, err := proto.Marshal(buildUnitCharacter(target))
	if err != nil {
		resp.Error, resp.Message = errBadParam, "序列化失败"
		return resp
	}
	base = pbAppendBytes(base, 1, uc)
	// The client keeps WornEquipDic between CharacterUI opens and applies this
	// list incrementally. Clear every online slot before adding the selected
	// character's actual equipment, otherwise viewing an unequipped player can
	// display equipment left over from the previously viewed player.
	for slot := int32(0); slot <= 11; slot++ {
		base = appendEmptyWornSlot(base, 3, slot)
	}
	// WornBagMapList 字段（tag=3，客户端字段级 ProtoMember）
	base = encodeBagMapList(base, 3, bagPairs(target.worn))
	// CharacterUI.ShowNumText reads NumericComponent from the selected scene
	// unit. Send the complete target snapshot before completing the RPC.
	s.pushPlayerAttrsTo(ch, target, target.battleHP(), target.battleMP())
	s.sendRawPush(ch, protocol.OpM2C_GetCharacter, base)
	// CharacterUI.ShowEffect resolves star souls from the currently viewed
	// UnitId. The regular character response has no star-soul fields, so send
	// the existing full-sync message immediately after it with the target's
	// UnitId. This also keeps viewing another player isolated from the viewer's
	// own star-soul bag.
	starSoul := target.ensureStarSoulBag()
	s.sendPush(ch, protocol.OpM2C_SyncStarSoulBagInfo, &protocol.M2C_SyncStarSoulBagInfo{
		UnitId:    target.playerID,
		ItemList:  starSoul.netItems(),
		UsedIdMap: append([]int64(nil), starSoul.Used...),
		SuitKVs:   starSoul.suitKVs(),
		ActorId:   target.playerID,
	})
	log.Printf("[S=%d] get character id=%d level=%d hp=%d mp=%d worn=%d",
		ch.id, pid, target.level, target.playerMaxHp(), target.playerMaxMp(), len(target.worn))
	return nil
}

// ===================== 背包 20258 → 20259 + 20260 =====================
// 客户端 BagUI.AwakeAsync 发 C2M_GetBag → await M2C_GetBag（BagMapList），
// 随后 UpdateBagUI 事件刷新格子；M2C_SendBag 为服务器主动推送（Handler 同样发 UpdateBagUI）。
func (s *Server) onGetBag(ch *channel, req *protocol.C2M_GetBag) proto.Message {
	ss := ch.session
	resp := &protocol.M2C_GetBag{RpcId: req.RpcId}
	base, err := proto.Marshal(resp)
	if err != nil {
		resp.Error, resp.Message = errBadParam, "序列化失败"
		return resp
	}
	base = encodeBagMapList(base, 1, bagPairs(ss.bag))
	s.sendRawPush(ch, protocol.OpM2C_GetBag, base)
	// 主动推送 M2C_SendBag（BagMapList）
	sb, _ := proto.Marshal(&protocol.M2C_SendBag{RpcId: 0, ActorId: ss.playerID})
	sb = encodeBagMapList(sb, 1, bagPairs(ss.bag))
	s.sendRawPush(ch, protocol.OpM2C_SendBag, sb)
	log.Printf("[S=%d] get bag items=%d", ch.id, len(ss.bag))
	return nil
}

// ===================== 穿戴 20271 → 20272 =====================
// 客户端双击装备格子发 C2M_PutOn{Index=背包格子} → M2C_PutOn{BagMapList, WornBagMapList}，
// 随后 UpdateBagUI + UpdateWornEquipUI 刷新背包与穿戴面板。
func encodePutOnResponse(ss *session, rpcID int32, message string) ([]byte, error) {
	resp := &protocol.M2C_PutOn{RpcId: rpcID, Message: message}
	base, err := proto.Marshal(resp)
	if err != nil {
		return nil, err
	}
	if ss != nil {
		base = encodeBagMapList(base, 1, bagPairs(ss.bag))
		base = encodeBagMapList(base, 2, bagPairs(ss.worn))
	}
	return base, nil
}

// finishPutOn 对成功和业务拒绝统一发送 raw 20272。客户端会对 Error=1
// 直接抛 RPC 异常，因此可预期的业务拒绝必须保持 Error=0，用 Message 显示原因。
func (s *Server) finishPutOn(ch *channel, req *protocol.C2M_PutOn, it *bagItem, equipJob, needLevel int32, message string) proto.Message {
	ss := ch.session
	body, err := encodePutOnResponse(ss, req.RpcId, message)
	if err != nil {
		log.Printf("[S=%d] put on encode failed rpc=%d err=%v", ch.id, req.RpcId, err)
		return &protocol.M2C_PutOn{RpcId: req.RpcId, Error: errBadParam, Message: "序列化失败"}
	}
	s.sendRawPush(ch, protocol.OpM2C_PutOn, body)
	itemID := int32(0)
	if it != nil {
		itemID = it.ItemId
	}
	result := message
	if result == "" {
		result = "ok"
	}
	log.Printf("[S=%d] put on index=%d item=%d playerJob=%d equipJob=%d playerLevel=%d needLevel=%d result=%q bag=%d worn=%d rpc=%d",
		ch.id, req.Index, itemID, ss.jobID, equipJob, ss.level, needLevel, result, len(ss.bag), len(ss.worn), req.RpcId)
	return nil
}

func (s *Server) onPutOn(ch *channel, req *protocol.C2M_PutOn) proto.Message {
	if ch == nil || ch.session == nil {
		return &protocol.M2C_PutOn{RpcId: req.RpcId, Error: errNotLogged, Message: "请先登录"}
	}
	ss := ch.session
	it, ok := ss.bag[req.Index]
	if !ok || it == nil {
		return s.finishPutOn(ch, req, nil, 0, 0, "物品不存在")
	}
	if it.ItemType != 1 {
		return s.finishPutOn(ch, req, it, 0, 0, "该物品不可穿戴")
	}
	if tables == nil {
		return s.finishPutOn(ch, req, it, 0, 0, "装备配置未加载")
	}
	eb, ok := tables.equipBase[int64(it.ItemId)]
	if !ok {
		return s.finishPutOn(ch, req, it, 0, 0, "装备配置不存在")
	}
	equipJob := int32(num(eb["JobId"]))
	needLevel := int32(num(eb["UseLevel"]))
	if equipJob != 0 && equipJob != jobTypeOf(ss.jobID) {
		return s.finishPutOn(ch, req, it, equipJob, needLevel, "职业不符")
	}
	if needLevel > ss.level {
		return s.finishPutOn(ch, req, it, equipJob, needLevel, "等级不足")
	}
	if ss.worn == nil {
		ss.worn = make(map[int32]*bagItem)
	}
	// 穿戴槽位严格使用 EquipBase.Type；武器省略 Type，枚举默认值 0 即左上武器槽。
	slot := equipSlotForItem(it)
	if slot < 0 {
		return s.finishPutOn(ch, req, it, equipJob, needLevel, "装备部位无效")
	}
	// 交换时复用新装备原来的格子，满背包也能正常换装。
	if old, ok := ss.worn[slot]; ok && old != nil {
		ss.bag[req.Index] = old
	} else {
		delete(ss.bag, req.Index)
	}
	ss.worn[slot] = it
	// 皮肤装备（EquipBase Type=2 且 ∈ SkinBase：120594 篮球宝贝等）→ 更新 SkinId。
	// 客户端穿皮肤装备后人物模型按 SkinBase[SkinId].PrfabId 重建（用户实测"立马生效"）；
	// 服务器同步 SkinId 使面板立绘/重登/推给他人一致。脱皮肤装备恢复职业皮肤。
	applySkinEquip(ss)
	applyWornTitle(ss)
	s.saveData(ch)
	// 先发完整的背包/穿戴快照。客户端先移除背包实例，再处理属性和外观推送，
	// 可避免同一件装备在背包与个人信息中同时残留。
	if response := s.finishPutOn(ch, req, it, equipJob, needLevel, ""); response != nil {
		return response
	}
	// 属性变化：重推数值属性（含 1036 SkinId → 世界模型立即换肤）+ 角色信息 + 角色面板。
	s.pushPlayerAttrs(ch)
	s.pushUnitCharacter(ch)
	s.sendPush(ch, protocol.OpM2C_SendCharacter, &protocol.M2C_SendCharacter{
		UnitCharacter: buildUnitCharacter(ss),
		Id:            ss.playerID,
		ActorId:       ss.playerID,
	})
	// 同场景其他玩家更新现有 Unit，不能重复发送 EnterMap 创建同一个 Id。
	s.broadcastPlayerUpdate(ch)
	return nil
}

// applySkinEquip：按当前穿戴更新皮肤。穿上皮肤装备（EquipBase Type=2 且 ∈ SkinBase）→
// SkinId = 装备 id（如 120594 → SkinBase.PrfabId=10 → Skin1.prefab）；
// 脱下皮肤装备 → 恢复职业皮肤（jobID 1-4）。
func applySkinEquip(ss *session) {
	if tables == nil {
		return
	}
	for _, it := range ss.worn {
		if it == nil {
			continue
		}
		eb, ok := tables.equipBase[int64(it.ItemId)]
		if !ok || int32(num(eb["Type"])) != 2 {
			continue
		}
		if _, isSkin := tables.skinBase[int64(it.ItemId)]; isSkin {
			ss.skinID = it.ItemId
			return
		}
	}
	// 未穿皮肤装备：职业皮肤
	ss.skinID = ss.jobID
}

// applySkinEquipOnLoad keeps a valid direct SkinId when no skin equipment is
// worn. This allows an administrator or a test fixture to grant a custom
// appearance by writing players.skin_id, without adding an item to a shop.
// Once a skin item is worn it remains authoritative, matching applySkinEquip.
func applySkinEquipOnLoad(ss *session) {
	if tables == nil {
		return
	}
	for _, it := range ss.worn {
		if it == nil {
			continue
		}
		eb, ok := tables.equipBase[int64(it.ItemId)]
		if !ok || int32(num(eb["Type"])) != 2 {
			continue
		}
		if _, isSkin := tables.skinBase[int64(it.ItemId)]; isSkin {
			ss.skinID = it.ItemId
			return
		}
	}
	if !isSkinIDValid(ss.skinID) {
		ss.skinID = ss.jobID
	}
}

// applyWornTitle restores the displayed title from the title equipment slot.
// HudCharacter.SetTitle resolves this value directly through EquipBase, so the
// title must be the worn item's template id rather than a separate TitleConfig id.
func applyWornTitle(ss *session) bool {
	if ss == nil {
		return false
	}
	wanted := int32(0)
	if it := ss.worn[titleEquipSlot]; it != nil && it.ItemId > 0 {
		wanted = it.ItemId
	}
	if ss.titleID == wanted {
		return false
	}
	ss.titleID = wanted
	return true
}

// ===================== 脱下 20273 → 20274 =====================
// 客户端角色面板槽位按钮发 C2M_Takeoff{Index=槽位} → M2C_Takeoff{BagMapList, WornBagMapList}。
func (s *Server) onTakeoff(ch *channel, req *protocol.C2M_Takeoff) proto.Message {
	ss := ch.session
	resp := &protocol.M2C_Takeoff{RpcId: req.RpcId}
	it, ok := ss.worn[req.Index]
	if !ok || it == nil {
		// CharacterUI may emit the same double-click callback twice when an old UI
		// instance is still alive. Treat an already-empty slot as an idempotent
		// business rejection; non-zero Error makes Session.Call disconnect.
		resp.Message = "该部位未穿戴装备"
		return resp
	}
	idx := nextBagIndex(ss)
	if idx < 0 {
		resp.Message = "背包已满"
		return resp
	}
	ss.bag[idx] = it
	delete(ss.worn, req.Index)
	// 脱下皮肤装备 → 皮肤恢复职业（applySkinEquip 遍历剩余穿戴）。
	applySkinEquip(ss)
	if req.Index == titleEquipSlot {
		ss.titleID = 0
	}

	base, err := proto.Marshal(resp)
	if err != nil {
		resp.Error, resp.Message = errBadParam, "序列化失败"
		return resp
	}
	base = encodeBagMapList(base, 1, bagPairs(ss.bag))
	base = encodeBagMapList(base, 2, bagPairs(ss.worn))
	base = appendEmptyWornSlot(base, 2, req.Index)
	s.sendRawPush(ch, protocol.OpM2C_Takeoff, base)
	s.pushPlayerAttrs(ch) // 含 1036 SkinId → 世界模型换肤
	s.pushUnitCharacter(ch)
	s.sendPush(ch, protocol.OpM2C_SendCharacter, &protocol.M2C_SendCharacter{
		UnitCharacter: buildUnitCharacter(ss),
		Id:            ss.playerID,
		ActorId:       ss.playerID,
	})
	s.broadcastPlayerUpdate(ch)
	s.saveData(ch)
	log.Printf("[S=%d] take off slot=%d item=%d", ch.id, req.Index, it.ItemId)
	return nil
}

// ===================== 使用物品 20275 → 20276 =====================
// 客户端双击药品格子发 C2M_UseGoods{Index} → M2C_UseGoods{BagMapList, MainUISlotList}。
// 客户端（BagUI.AwakeAsync b__7，HotfixView 1301 反汇编）：
//
//	Message 非空 → OpenUI 提示；Message 空 → UpdateBagUI(BagMapList 全量) +
//	UpdateMainUISlot(MainUISlotList)——MainUISlotList 必须非空（null 会 NRE），
//	且含消耗涉及物品槽的条目（Id/MainUIType=2/ItemCount/Index）数量才会刷新。
//
// 药品效果（GoodsBase.EffectType：11=FixedHp 12=FixedMp 2=PercentHp 3=PercentMp
//
//	1=FixedHp+FixedMp 13/24=领主宝箱 15/16=宠物粮食 19=精力 20/21=跑图卡）：
//
// 按线上字段恢复血/蓝并推送属性；其余类型进 useGoodsEffect 分支（开箱/货币/精力等）。
const strengthCouponItemID int32 = 110836

// useEquipStrengthCoupon consumes the equipment-strengthening coupon only when
// the first visible bag slot contains an equipment item below the level cap.
// Keep validation, level update, and coupon consumption under battleMu so an
// automatic battle tick cannot race a double-click and consume two coupons.
func (s *Server) useEquipStrengthCoupon(ch *channel, couponIndex int32) (bool, string) {
	if ch == nil || ch.session == nil {
		return false, "请先登录"
	}
	ss := ch.session
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	coupon := ss.bag[couponIndex]
	if coupon == nil || coupon.ItemId != strengthCouponItemID || coupon.Count <= 0 {
		return false, "强化券不存在"
	}
	target := ss.bag[0]
	if target == nil || !itemIsEquip(target.ItemId) {
		return false, "请先将装备放在背包第一格"
	}
	repairBagItem(target)
	if target.Level >= 20 {
		return false, "装备已强化至20级"
	}
	if target.Level < 1 {
		target.Level = 1
	}
	target.Level = 20
	consumeGoods(ss, couponIndex)
	return true, ""
}

func (s *Server) onUseGoods(ch *channel, req *protocol.C2M_UseGoods) proto.Message {
	ss := ch.session
	resp := &protocol.M2C_UseGoods{RpcId: req.RpcId}
	if coupon := ss.bag[req.Index]; coupon != nil && coupon.ItemType == int32(protocol.ItemType_GoodsItem) && coupon.ItemId == strengthCouponItemID {
		if gb, exists := tables.goodsBase[int64(coupon.ItemId)]; exists && int32(num(gb["EffectType"])) == 23 {
			if applied, rejection := s.useEquipStrengthCoupon(ch, req.Index); !applied {
				resp.Message = rejection
				return resp
			}
			base, err := proto.Marshal(resp)
			if err != nil {
				resp.Error, resp.Message = errBadParam, "marshal failed"
				return resp
			}
			base = encodeBagMapList(base, 1, bagPairs(ss.bag))
			base = appendMainUISlotList(base, 2, ss.mainUISlotList())
			s.sendRawPush(ch, protocol.OpM2C_UseGoods, base)
			if slotID := ss.mainUISlotOfItem(coupon.ItemId); slotID >= 0 {
				s.sendPush(ch, protocol.OpM2C_StartCD, &protocol.M2C_StartCD{
					Id: coupon.ItemId, SkillCD: goodsItemCD(gb), Type: protocol.MainUIType_ItemSlot,
				})
			}
			s.pushPlayerAttrs(ch)
			s.pushUnitCharacter(ch)
			s.saveData(ch)
			log.Printf("[S=%d] use strength coupon item=%d target=0 level=20", ch.id, coupon.ItemId)
			return nil
		}
	}
	it, ok := ss.bag[req.Index]
	if !ok || it == nil {
		resp.Error, resp.Message = errBadParam, "物品不存在"
		return resp
	}
	if it.ItemType != 2 {
		resp.Error, resp.Message = errBadParam, "该物品不可使用"
		return resp
	}
	gb, ok := tables.goodsBase[int64(it.ItemId)]
	if !ok {
		resp.Error, resp.Message = errBadParam, "物品配置不存在"
		return resp
	}
	// 宠物粮食 / 经验卡：交给宠物系统（pet.go），消耗物品并喂食。
	if s.petFeed(ch, gb) {
		consumeGoods(ss, req.Index)
		base, err := proto.Marshal(resp)
		if err != nil {
			resp.Error, resp.Message = errBadParam, "序列化失败"
			return resp
		}
		base = encodeBagMapList(base, 1, bagPairs(ss.bag))
		base = appendMainUISlotList(base, 2, ss.mainUISlotList())
		s.sendRawPush(ch, protocol.OpM2C_UseGoods, base)
		s.saveData(ch)
		log.Printf("[S=%d] use pet food item=%d", ch.id, it.ItemId)
		return nil
	}
	// 基础效果：恢复 Hp/Mp（FixedHp/FixedMp 或 PercentHp/PercentMp 线上字段）
	var healHP, healMP int32
	effectType := int32(num(gb["EffectType"]))
	if effectType == 9 || effectType == 13 || effectType == 24 {
		// 宝箱必须把“消耗箱子”和“发放全部随机奖励”作为一个事务。
		// 箱子正好占一格时，预演会正确利用它消耗后释放的格子。
		if !s.openGoodsBox(ch, req.Index, gb) {
			resp.Message = "背包已满"
			return resp
		}
		base, err := proto.Marshal(resp)
		if err != nil {
			resp.Error, resp.Message = errBadParam, "序列化失败"
			return resp
		}
		base = encodeBagMapList(base, 1, bagPairs(ss.bag))
		base = appendMainUISlotList(base, 2, ss.mainUISlotList())
		s.sendRawPush(ch, protocol.OpM2C_UseGoods, base)
		s.saveData(ch)
		log.Printf("[S=%d] use goods box item=%d", ch.id, it.ItemId)
		return nil
	}
	// 非血蓝效果：开箱/货币/精力/跑图等（宝箱 EffectType 13/24 内容表为全 0 → 不发奖励）
	if handled, rejection := s.useGoodsEffect(ch, gb, it.Count); handled {
		if rejection != "" {
			resp.Message = rejection
			return resp
		}
		// 货币类物品整格兑换；其余特殊物品消耗 1 个。
		if isCurrencyGoods(it.ItemId) {
			delete(ss.bag, req.Index)
		} else {
			consumeGoods(ss, req.Index)
		}
		base, err := proto.Marshal(resp)
		if err != nil {
			resp.Error, resp.Message = errBadParam, "序列化失败"
			return resp
		}
		base = encodeBagMapList(base, 1, bagPairs(ss.bag))
		base = appendMainUISlotList(base, 2, ss.mainUISlotList())
		s.sendRawPush(ch, protocol.OpM2C_UseGoods, base)
		s.saveData(ch)
		log.Printf("[S=%d] use special goods item=%d effect=%d", ch.id, it.ItemId, effectType)
		return nil
	}
	// Keep the authoritative resource update and its wire snapshot together.
	// Automatic battle phases use the same lock; releasing it before sending
	// allowed a hit to be emitted between the heal and the potion snapshot,
	// making the client briefly apply an older HP value.
	ss.battleMu.Lock()
	if rejection := goodsRestoreRejection(ss, gb); rejection != "" {
		ss.battleMu.Unlock()
		resp.Message = rejection
		return resp
	}
	healHP, healMP = goodsHeal(ss, gb)
	currentHP, currentMP := ss.battleHP(), ss.battleMP()
	consumeGoods(ss, req.Index)
	if healHP != 0 || healMP != 0 {
		s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: ss.playerID, NumericType: 1001, Value: float32(currentHP), ActorId: ss.playerID,
		})
		s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: ss.playerID, NumericType: 1003, Value: float32(currentMP), ActorId: ss.playerID,
		})
	}
	ss.battleMu.Unlock()
	// 队友的组队头像也要跟着回满，否则自己看到满血、队友看到半血（见 team_vital_sync.go）。
	// 必须在 battleMu 之外调用：本函数内部会取 teamMu，而队伍换图已有 teamMu → battleMu 的加锁顺序。
	if healHP != 0 || healMP != 0 {
		s.pushTeamVitals(ch, currentHP, currentMP)
	}
	// 物品若在快捷物品槽中，给该槽推送 CD（20237）——槽位显示遮罩 + 时间。
	if slotID := ss.mainUISlotOfItem(it.ItemId); slotID >= 0 {
		s.sendPush(ch, protocol.OpM2C_StartCD, &protocol.M2C_StartCD{
			Id: it.ItemId, SkillCD: goodsItemCD(gb), Type: protocol.MainUIType_ItemSlot,
		})
	}
	base, err := proto.Marshal(resp)
	if err != nil {
		resp.Error, resp.Message = errBadParam, "序列化失败"
		return resp
	}
	base = encodeBagMapList(base, 1, bagPairs(ss.bag))
	base = appendMainUISlotList(base, 2, ss.mainUISlotList())
	s.sendRawPush(ch, protocol.OpM2C_UseGoods, base)
	s.saveData(ch)
	log.Printf("[S=%d] use goods item=%d healHP=%d healMP=%d", ch.id, it.ItemId, healHP, healMP)
	return nil
}

// useGoodsEffect 处理非血蓝消耗品效果（消耗前调用，返回 true = 已处理）：
//
//	19 = 精力药水（OtherParam 恢复精力）
//	2/3 = 魔法球（战斗结束后自动回满血/蓝；左上角 20080 buff 显示时长）
//	6/7/16 = 经验卡（ExpRange × ContinuedSeconds 设置经验倍率 buff + 20080 左上角显示）
//	8 = 喇叭（占位；广播走聊天）
//	10 = 礼券类（经验药水/金币袋/元宝/代金券礼券，按 Description 语义换算）
//	20/21 = 跑图卡（每张增加一小时储备并显示剩余时间）
//	货币类物品（GoodsBase._id 110201 经验 / 110202 元宝 / 110203 铜币 / 110204 代金券）：
//	使用后按 count 换算对应货币/经验入账（原版表无数值字段，取等价换算）。
//	其余 EffectType=0 物品（材料等）返回 false（由调用方按无效果处理）。
func (s *Server) useGoodsEffect(ch *channel, gb map[string]interface{}, count int32) (bool, string) {
	if ch == nil || ch.session == nil || gb == nil {
		return false, ""
	}
	effectType := int32(num(gb["EffectType"]))
	itemID := int32(num(gb["_id"]))
	ss := ch.session
	switch effectType {
	case 6, 7, 16:
		s.activateItemBuff(ch, gb)
		log.Printf("[S=%d] use exp card item=%d effect=%d mult=%.1f", ch.id, itemID, effectType, numf(gb["ExpRange"]))
		return true, ""
	case 2:
		s.activateItemBuff(ch, gb)
		log.Printf("[S=%d] use hp ball item=%d cap=%d", ch.id, itemID, num(gb["Capacity"]))
		return true, ""
	case 3:
		s.activateItemBuff(ch, gb)
		log.Printf("[S=%d] use mp ball item=%d cap=%d", ch.id, itemID, num(gb["Capacity"]))
		return true, ""
	case 8:
		// 喇叭：实际广播走聊天；此处确认消耗（ContinuedSeconds 为横幅时长）。
		log.Printf("[S=%d] use horn item=%d cs=%dms", ch.id, itemID, int64(num(gb["ContinuedSeconds"])))
		return true, ""
	case 10:
		// 礼券类：按物品 Name/Description 语义换算（经验药水/金币袋/元宝/代金券礼券）。
		applied, rejection := s.applyCouponGoods(ch, gb, count)
		if !applied {
			return true, rejection
		}
		return true, ""
	case 19:
		// 精力药水：恢复精力（NumericType 1039）
		param := ""
		if s, ok := gb["OtherParam"].(string); ok {
			param = s
		}
		val := int32(0)
		for _, part := range strings.Split(param, ",") {
			part = strings.TrimSpace(part)
			if idx := strings.LastIndex(part, ":"); idx >= 0 {
				if v, err := strconv.Atoi(strings.TrimSpace(part[idx+1:])); err == nil {
					val = int32(v)
				}
			}
		}
		if val <= 0 {
			val = 50
		}
		ss.energy += val
		if ss.energy > 10000 {
			ss.energy = 10000
		}
		s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: ss.playerID, NumericType: ntEnergy, Value: float32(ss.energy), ActorId: ss.playerID,
		})
		log.Printf("[S=%d] use energy potion +%d -> %d", ch.id, val, ss.energy)
		return true, ""
	case 20, 21:
		// 每张增加一小时储备时间；同类时间累积并显示在状态栏。
		s.activateItemBuff(ch, gb)
		log.Printf("[S=%d] use run card item=%d effect=%d", ch.id, itemID, effectType)
		return true, ""
	}
	// 货币类物品：使用 → 按整格数量换算货币/经验入账
	if isCurrencyGoods(itemID) {
		switch itemID {
		case 110201: // 经验
			s.gainExp(ch, int64(count)*1000)
			log.Printf("[S=%d] use exp item %d count=%d +%d exp", ch.id, itemID, count, int64(count)*1000)
		case 110202: // 元宝
			if !s.addYuanBao(ch, int64(count)) {
				return true, "元宝已达上限"
			}
		case 110203: // 铜币
			if !s.addCoin(ch, int64(count)) {
				return true, "铜币已达上限"
			}
		case 110204: // 代金券
			if !s.addVoucher(ch, int64(count)) {
				return true, "代金券已达上限"
			}
		}
		return true, ""
	}
	// 回城卷轴：EffectType 5（110344 "使用后返回城镇"）→ 回主城光圈。
	if effectType == 5 {
		allowed, message := s.returnPartyToMainCity(ch, "return-scroll", true, false)
		if !allowed {
			log.Printf("[S=%d] reject return scroll player=%d: %s", ch.id, ch.session.playerID, message)
			return true, message
		}
		log.Printf("[S=%d] use return scroll item=%d -> main city", ch.id, itemID)
		return true, ""
	}
	return false, ""
}

func couponDescriptionRange(desc string) (int64, int64, bool) {
	numbers := regexp.MustCompile(`\d+`).FindAllString(desc, -1)
	if len(numbers) == 0 {
		return 0, 0, false
	}
	minimum, err := strconv.ParseInt(numbers[0], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	maximum := minimum
	if strings.Contains(desc, "随机") && len(numbers) > 1 {
		if value, parseErr := strconv.ParseInt(numbers[1], 10, 64); parseErr == nil {
			maximum = value
		}
	}
	if maximum < minimum {
		minimum, maximum = maximum, minimum
	}
	return minimum, maximum, true
}

func couponDescriptionAmount(desc string) int64 {
	minimum, maximum, ok := couponDescriptionRange(desc)
	if !ok {
		return 0
	}
	if maximum == minimum {
		return minimum
	}
	return minimum + rand.Int63n(maximum-minimum+1)
}

func randomRangeAmount(minimum, maximum int64) int64 {
	if maximum <= minimum {
		return minimum
	}
	return minimum + rand.Int63n(maximum-minimum+1)
}

// applyCouponGoods 礼券类（EffectType 10）效果：
// 按物品 Name/Description 语义换算入账；随机礼券按描述中的闭区间逐次抽取。
func (s *Server) applyCouponGoods(ch *channel, gb map[string]interface{}, count int32) (bool, string) {
	ss := ch.session
	desc := fmt.Sprintf("%v", gb["Description"])
	itemID := int32(num(gb["_id"]))
	var gainExp, gainCoin, gainYB, gainVoucher int64
	if minimum, maximum, configured := gameplayVoucherRange(itemID); configured {
		// 运营配置优先于客户端描述，支持不改包直接热更随机礼券范围。
		gainVoucher = randomRangeAmount(minimum, maximum)
	} else {
		switch {
		case itemID >= 110353 && itemID <= 110358: // 经验药水（100万~100亿）
			amounts := map[int32]int64{110353: 1000000, 110354: 3000000, 110355: 5000000, 110356: 100000000, 110357: 5000000000, 110358: 10000000000}
			gainExp = amounts[itemID]
		case itemID >= 110362 && itemID <= 110372: // 代金券礼券（500~120000）
			amounts := map[int32]int64{110362: 500, 110363: 1000, 110364: 1500, 110365: 2000, 110366: 5000, 110367: 10000, 110368: 15000, 110369: 20000, 110370: 30000, 110371: 40000, 110372: 120000}
			gainVoucher = amounts[itemID]
		case itemID >= 110373 && itemID <= 110377: // 金币袋（50000~2000万铜币）
			amounts := map[int32]int64{110373: 50000, 110374: 500000, 110375: 990000, 110376: 9990000, 110377: 20000000}
			gainCoin = amounts[itemID]
		case itemID == 110378 || itemID == 110379: // 银元宝/金元宝（10/50 元宝）
			amounts := map[int32]int64{110378: 10, 110379: 50}
			gainYB = amounts[itemID]
		case itemID == 110343: // 金条（10 银币 = 1000 铜币）
			gainCoin = 1000
		default:
			// 未配置的物品继续兼容线上 Description 固定值或随机闭区间。
			if val := couponDescriptionAmount(desc); val > 0 {
				switch {
				case strings.Contains(desc, "经验"):
					gainExp = val
				case strings.Contains(desc, "铜币") || strings.Contains(desc, "金币"):
					gainCoin = val
				case strings.Contains(desc, "元宝"):
					gainYB = val
				case strings.Contains(desc, "代金券"):
					gainVoucher = val
				}
			}
		}
	}
	if !ss.canApplyCurrencyDelta(currencyDelta{coin: gainCoin, yuanBao: gainYB, voucher: gainVoucher}) {
		return false, "货币已达上限"
	}
	if gainExp > 0 {
		s.gainExp(ch, gainExp)
	}
	if !s.applyCurrencyDelta(ch, currencyDelta{coin: gainCoin, yuanBao: gainYB, voucher: gainVoucher}) {
		return false, "货币已达上限"
	}
	log.Printf("[S=%d] use coupon goods item=%d exp=%d coin=%d yb=%d voucher=%d", ch.id, itemID, gainExp, gainCoin, gainYB, gainVoucher)
	return true, ""
}

// applyMagicBallRecover 战斗结束后从魔法球的持久化储量补充血/蓝。
// 储量按实际恢复值扣除，耗尽后立即移除左上角状态图标。
func (s *Server) applyMagicBallRecover(ch *channel) {
	if ch == nil || ch.session == nil {
		return
	}
	ss := ch.session
	changed := false
	statusChanged := false
	var removed []activeItemBuff
	ss.itemBuffMu.Lock()
	ss.ensureItemBuffsLocked()
	if buff := ss.itemBuffs[itemBuffHPBall]; buff != nil {
		need := ss.playerMaxHp() - ss.battleHP()
		if need > 0 && buff.Capacity > 0 {
			recover := int64(need)
			if recover > buff.Capacity {
				recover = buff.Capacity
			}
			ss.setBattleHP(saturatingAddInt32(ss.battleHP(), clampInt64ToInt32(recover)))
			buff.Capacity -= recover
			changed = true
		}
		if buff.Capacity <= 0 {
			removed = append(removed, *buff)
			delete(ss.itemBuffs, itemBuffHPBall)
			statusChanged = true
		}
	}
	if buff := ss.itemBuffs[itemBuffMPBall]; buff != nil {
		need := ss.playerMaxMp() - ss.battleMP()
		if need > 0 && buff.Capacity > 0 {
			recover := int64(need)
			if recover > buff.Capacity {
				recover = buff.Capacity
			}
			ss.setBattleMP(saturatingAddInt32(ss.battleMP(), clampInt64ToInt32(recover)))
			buff.Capacity -= recover
			changed = true
		}
		if buff.Capacity <= 0 {
			removed = append(removed, *buff)
			delete(ss.itemBuffs, itemBuffMPBall)
			statusChanged = true
		}
	}
	ss.syncLegacyItemBuffFieldsLocked(time.Now().UnixMilli())
	ss.itemBuffMu.Unlock()
	for i := range removed {
		s.pushItemBuffMessage(ch, &removed[i], protocol.ChangeType_Reduce, 0)
	}
	if changed {
		s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: ss.playerID, NumericType: 1001, Value: float32(ss.battleHP()), ActorId: ss.playerID,
		})
		s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: ss.playerID, NumericType: 1003, Value: float32(ss.battleMP()), ActorId: ss.playerID,
		})
		// 自动回满同样要同步给队友的组队头像（见 team_vital_sync.go）。
		// 这里只能用「排到锁外」的补推：本函数也会被战斗结算路径
		// （emitVictory / recoverPlayerAfterDefeat，调用方持 battleMu）调到，
		// 而 pushTeamVitals 要取 teamMu，与队伍换图的 teamMu → battleMu 反向。
		s.scheduleTeamVitalSync(ch)
		log.Printf("[S=%d] magic ball auto recover hp=%d mp=%d", ch.id, ss.battleHP(), ss.battleMP())
	}
	if changed || statusChanged {
		s.saveData(ch)
	}
}

func rollGoodsBoxGrants(gb map[string]interface{}) []bagGrant {
	type group struct {
		field, idField, probabilityField, countField string
	}
	groups := []group{
		{"EquipArr", "Equip_Id", "Equip_Probability", "Equip_Count"},
		{"GoodsArr", "Goods_Id", "Goods_Probability", "Goods_Count"},
		{"MaterialsArr", "Materials_Id", "Materials_Probability", "Materials_Count"},
	}
	var grants []bagGrant
	for _, group := range groups {
		for _, entry := range arrOf(gb[group.field]) {
			row, _ := entry.(map[string]interface{})
			if row == nil {
				continue
			}
			itemID := int32(num(row[group.idField]))
			probability := numf(row[group.probabilityField])
			count := int32(num(row[group.countField]))
			if itemID <= 0 || probability <= 0 || count <= 0 {
				continue
			}
			if probability < 1 && rand.Float64() > float64(probability) {
				continue
			}
			grants = append(grants, bagGrant{itemID: itemID, count: count})
		}
	}
	return grants
}

// openGoodsBox atomically consumes one box and grants every rolled reward.
func (s *Server) openGoodsBox(ch *channel, sourceIndex int32, gb map[string]interface{}) bool {
	if ch == nil || ch.session == nil || gb == nil {
		return false
	}
	ss := ch.session
	shadow := &session{bag: cloneBagMap(ss.bag)}
	if !shadow.removeBagCount(sourceIndex, 1) {
		return false
	}
	grants := rollGoodsBoxGrants(gb)
	for _, grant := range grants {
		if _, ok := addItemToBagInPlace(shadow, grant.itemID, grant.count); !ok {
			log.Printf("[S=%d] open box item=%d rejected: bag full", ch.id, int32(num(gb["_id"])))
			return false
		}
	}
	ss.bag = shadow.bag
	log.Printf("[S=%d] open box item=%d rewards=%d", ch.id, int32(num(gb["_id"])), len(grants))
	return true
}

// isCurrencyGoods：货币/经验类物品（GoodsBase _id 110201-110204）。
func isCurrencyGoods(itemID int32) bool {
	return itemID >= 110201 && itemID <= 110204
}

// syncBattleHealthToSession 把战斗中的血蓝写回会话（战斗结束/退出时调用），
// 使非战斗吃药能恢复残血（battleHP() 战斗外读 ss.hp）。
func syncBattleHealthToSession(ss *session) {
	if ss == nil {
		return
	}
	if ss.battle != nil {
		ss.hp = ss.battle.playerHP
		ss.mp = ss.battle.playerMP
	} else if ss.hp < 0 {
		ss.hp = ss.playerMaxHp()
	}
	if ss.mp < 0 {
		ss.mp = ss.playerMaxMp()
	}
}

// mainUISlotOfItem 返回包含该物品的主界面物品槽 Index；不在任何槽中返回 -1。
func (ss *session) mainUISlotOfItem(itemID int32) int32 {
	if ss == nil {
		return -1
	}
	for i, sl := range ss.mainUISlots {
		if sl.Type == 2 && sl.Id == itemID {
			return int32(i)
		}
	}
	return -1
}

// consumeGoods 消耗背包格一个物品（数量 >1 减一，否则删格）。
func consumeGoods(ss *session, index int32) {
	if it, ok := ss.bag[index]; ok && it != nil {
		if it.Count > 1 {
			it.Count--
		} else {
			delete(ss.bag, index)
		}
	}
}

const (
	goodsHPFullMessage   = "生命值已满，无需恢复"
	goodsMPFullMessage   = "精力已满，无需恢复"
	goodsHPMPFullMessage = "生命值和精力已满，无需恢复"
)

func goodsRestoresHP(gb map[string]interface{}) bool {
	return gb != nil && (num(gb["FixedHp"]) > 0 || numf(gb["PercentHp"]) > 0)
}

func goodsRestoresMP(gb map[string]interface{}) bool {
	return gb != nil && (num(gb["FixedMp"]) > 0 || numf(gb["PercentMp"]) > 0)
}

func sessionResourceFull(ss *session) (hpFull, mpFull bool) {
	if ss == nil {
		return true, true
	}
	maxHP, maxMP := ss.playerMaxHp(), ss.playerMaxMp()
	return ss.battleHP() >= maxHP, ss.battleMP() >= maxMP
}

// goodsRestoreRejection blocks restore items that cannot change HP/MP.
// Dual-restore items are allowed while either resource is missing; only a
// completely full character is rejected. Detection uses GoodsBase restore
// fields, not a single potion EffectType.
func goodsRestoreRejection(ss *session, gb map[string]interface{}) string {
	restoreHP, restoreMP := goodsRestoresHP(gb), goodsRestoresMP(gb)
	if !restoreHP && !restoreMP {
		return ""
	}
	hpFull, mpFull := sessionResourceFull(ss)
	switch {
	case restoreHP && restoreMP:
		if hpFull && mpFull {
			return goodsHPMPFullMessage
		}
	case restoreHP && hpFull:
		return goodsHPFullMessage
	case restoreMP && mpFull:
		return goodsMPFullMessage
	}
	return ""
}

// goodsHeal 按 GoodsBase 恢复字段计算血/蓝回复量并应用到会话。
// 线上字段：FixedHp/FixedMp（固定值）、PercentHp/PercentMp（百分比，0~1 小数）。
func goodsHeal(ss *session, gb map[string]interface{}) (healHP, healMP int32) {
	if ss == nil || gb == nil {
		return 0, 0
	}
	maxHP := ss.playerMaxHp()
	maxMP := ss.playerMaxMp()
	curHP := ss.battleHP()
	curMP := ss.battleMP()
	// Equipment/skin changes can raise or lower the effective maxima between
	// battle snapshots.  Refresh an active battle's maxima before applying a
	// potion, and normalize inherited current values against the new limits.
	// This keeps a potion from being constrained by a stale pre-skin battle max
	// and prevents a negative "healed amount" when a maximum was reduced.
	if ss.battle != nil {
		ss.battle.playerMaxHP = maxHP
		ss.battle.playerMaxMP = maxMP
	}
	if curHP < 0 {
		curHP = 0
	} else if curHP > maxHP {
		curHP = maxHP
	}
	if curMP < 0 {
		curMP = 0
	} else if curMP > maxMP {
		curMP = maxMP
	}
	if v := num(gb["FixedHp"]); v > 0 {
		healHP = clampInt64ToInt32(v)
	}
	if v := num(gb["FixedMp"]); v > 0 {
		healMP = clampInt64ToInt32(v)
	}
	if p := numf(gb["PercentHp"]); p > 0 {
		healHP = maxInt32(healHP, clampFloat64ToInt32(float64(maxHP)*p))
	}
	if p := numf(gb["PercentMp"]); p > 0 {
		healMP = maxInt32(healMP, clampFloat64ToInt32(float64(maxMP)*p))
	}
	// Use a widened/saturating sum.  At the int32 ceiling a 100% potion has
	// healHP=MaxInt32 and curHP may already be MaxInt32; a plain int32 add
	// wraps to -2 and the subsequent max clamp incorrectly reports 0 HP.
	newHP := saturatingAddInt32(curHP, healHP)
	if newHP > maxHP {
		newHP = maxHP
	}
	newMP := saturatingAddInt32(curMP, healMP)
	if newMP > maxMP {
		newMP = maxMP
	}
	ss.setBattleHP(newHP)
	ss.setBattleMP(newMP)
	return int32(int64(newHP) - int64(curHP)), int32(int64(newMP) - int64(curMP))
}

func maxInt32(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}

// battleHP / battleMP / setBattleHP / setBattleMP：读写会话当前血蓝。
// 战斗中血蓝以 battleState 为准；非战斗持久在 session.hp/mp（初始 = 满值，
// 战斗后写回残血 → 吃药可恢复，主界面血条随 20169 刷新）。
func (ss *session) battleHP() int32 {
	if ss.battle != nil {
		return ss.battle.playerHP
	}
	if ss.hp < 0 {
		return ss.playerMaxHp() // 未初始化 = 满血
	}
	return ss.hp
}

func (ss *session) battleMP() int32 {
	if ss.battle != nil {
		return ss.battle.playerMP
	}
	if ss.mp < 0 {
		return ss.playerMaxMp()
	}
	return ss.mp
}

func (ss *session) setBattleHP(v int32) {
	if ss == nil {
		return
	}
	before := ss.battleHP()
	if ss.battle != nil {
		ss.battle.playerHP = v
		if ss.battle.playerHP > ss.battle.playerMaxHP {
			ss.battle.playerHP = ss.battle.playerMaxHP
		}
		if ss.battle.playerHP < 0 {
			ss.battle.playerHP = 0
		}
		ss.hp = ss.battle.playerHP // 战斗内同步到会话（战斗结束写回用）
	} else {
		ss.hp = v
		if ss.hp > ss.playerMaxHp() {
			ss.hp = ss.playerMaxHp()
		}
		if ss.hp < 0 {
			ss.hp = 0
		}
	}
	logCombatHPWrite(ss, v, before, ss.battleHP())
}

func (ss *session) setBattleMP(v int32) {
	if ss.battle != nil {
		ss.battle.playerMP = v
		if ss.battle.playerMP > ss.battle.playerMaxMP {
			ss.battle.playerMP = ss.battle.playerMaxMP
		}
		if ss.battle.playerMP < 0 {
			ss.battle.playerMP = 0
		}
		ss.mp = ss.battle.playerMP
	} else {
		ss.mp = v
		if ss.mp > ss.playerMaxMp() {
			ss.mp = ss.playerMaxMp()
		}
		if ss.mp < 0 {
			ss.mp = 0
		}
	}
}
