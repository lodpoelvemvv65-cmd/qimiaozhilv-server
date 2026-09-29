package main

import (
	"log"
	"math"
	"sort"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// state_extra.go：商店/邮件/聊天/宠物/好友/组队/签到 等新增模块的共享状态类型与辅助函数。
//
// 持久化约定：玩家侧状态由 players 基础列及 player_* 关系表统一加载和保存。
// 组队为全服内存态（Server.teamMu / Server.teams）。

// ===================== 货币（NumericType） =====================
// 线上 NumericType（Unity.Model.dll ET.NumericType，见 13 文档 §2.9）：
//
//	元宝=1024 兑换券=1025 铜币=1028 荣誉=1030 家族贡献=1037 体力=1039
//	竞技币=1040，星币（客户端 NumericType.Gem）=1041。
//
// 玩家基础货币存 session（coin/yuanBao/voucher，DB 列），其余体系暂不启用。
const (
	ntYuanBao            = 1024
	ntVoucher            = 1025
	ntCoin               = 1028
	ntHonor              = 1030
	ntHornor             = ntHonor // compatibility with the historical misspelling
	ntFamilyContribution = 1037
	ntEnergy             = 1039
	ntPVPCurrency        = 1040
	ntStarCoin           = 1041
	ntPVPMoney           = 1047
)

// Star coins (GoodsBase 110205) are persisted as item rows for compatibility
// with existing characters, but they are a currency in the native client.
// Negative slots keep legacy stacks out of the client's visible 0..47 bag.
const starCoinBagSlot int32 = -1000000

func isStarCoinItem(itemID int32) bool { return itemID == consignmentCurrencyItemID }

// canonicalizeStarCoins migrates any legacy visible star-coin stacks to
// negative, non-rendered slots without changing their total balance.
func canonicalizeStarCoins(ss *session) bool {
	if ss == nil || ss.bag == nil {
		return false
	}
	var total int64
	needsRewrite := false
	for index, item := range ss.bag {
		if item == nil || !isStarCoinItem(item.ItemId) {
			continue
		}
		if item.Count > 0 {
			total += int64(item.Count)
		}
		if index != starCoinBagSlot || item.ItemType != int32(protocol.ItemType_GoodsItem) {
			needsRewrite = true
		}
	}
	if !needsRewrite {
		return false
	}
	for index, item := range ss.bag {
		if item != nil && isStarCoinItem(item.ItemId) {
			delete(ss.bag, index)
		}
	}
	for index := starCoinBagSlot; total > 0; index-- {
		chunk := total
		if chunk > int64(math.MaxInt32) {
			chunk = int64(math.MaxInt32)
		}
		ss.bag[index] = &bagItem{ItemId: consignmentCurrencyItemID,
			ItemType: int32(protocol.ItemType_GoodsItem), Count: int32(chunk)}
		total -= chunk
	}
	return true
}

// pushMoney 推送全部可消费货币属性（主界面货币栏刷新）。星币的权威
// 余额仍是背包物品 110205；客户端 NumericType.Gem(1041) 只作为显示同步。
func (s *Server) pushMoney(ch *channel) {
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		return
	}
	pid := ch.session.playerID
	push := func(t int32, v float32) {
		s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: pid, NumericType: t, Value: v, ActorId: pid,
		})
	}
	push(ntCoin, float32(ch.session.coin))
	push(ntYuanBao, float32(ch.session.yuanBao))
	push(ntVoucher, float32(ch.session.voucher))
	push(ntStarCoin, float32(starCoinBalance(ch.session)))
	push(ntHonor, float32(ch.session.honor))
	push(ntPVPCurrency, float32(ch.session.pvpCurrency))
	// The native character panel labels NumericType.PvpMoney (1047) as
	// "竞技币", while the multi-shop consumes PvpCurrency (1040). They are
	// two client-side views of the same authoritative server balance, so keep
	// both NumericComponent keys synchronized.
	push(ntPVPMoney, float32(ch.session.pvpCurrency))
	push(ntFamilyContribution, float32(ch.session.familyContribute))
}

// currencyDelta is the server-side representation of a credit operation. It
// deliberately excludes star coins because those are an item stack (110205),
// not a numeric balance. Keeping the other currencies in one transaction
// prevents a reward from crediting one balance and silently losing another
// when an upper bound is reached.
type currencyDelta struct {
	coin, yuanBao, voucher int64
	honor, pvpCurrency     int64
	familyContribute       int64
}

func canAddInt64Balance(current, delta int64) bool {
	return current >= 0 && delta >= 0 && current <= math.MaxInt64-delta
}

func addInt64Checked(dst *int64, delta int64) bool {
	if dst == nil || !canAddInt64Balance(*dst, delta) {
		return false
	}
	*dst += delta
	return true
}

func (ss *session) canApplyCurrencyDelta(delta currencyDelta) bool {
	if ss == nil {
		return false
	}
	return canAddInt64Balance(ss.coin, delta.coin) &&
		canAddInt64Balance(ss.yuanBao, delta.yuanBao) &&
		canAddInt64Balance(ss.voucher, delta.voucher) &&
		canAddInt64Balance(ss.honor, delta.honor) &&
		canAddInt64Balance(ss.pvpCurrency, delta.pvpCurrency) &&
		ss.familyContribute >= 0 && delta.familyContribute >= 0 &&
		int64(ss.familyContribute) <= int64(math.MaxInt32)-delta.familyContribute
}

// applyCurrencyDelta validates and commits all numeric currency credits as one
// operation. A failed validation leaves every balance untouched.
func (s *Server) applyCurrencyDelta(ch *channel, delta currencyDelta) bool {
	if !s.applyCurrencyDeltaNoPush(ch, delta) {
		return false
	}
	if delta.coin != 0 || delta.yuanBao != 0 || delta.voucher != 0 ||
		delta.honor != 0 || delta.pvpCurrency != 0 || delta.familyContribute != 0 {
		s.pushMoney(ch)
	}
	return true
}

func (s *Server) applyCurrencyDeltaNoPush(ch *channel, delta currencyDelta) bool {
	if ch == nil || ch.session == nil || !ch.session.canApplyCurrencyDelta(delta) {
		return false
	}
	ss := ch.session
	ss.coin += delta.coin
	ss.yuanBao += delta.yuanBao
	ss.voucher += delta.voucher
	ss.honor += delta.honor
	ss.pvpCurrency += delta.pvpCurrency
	ss.familyContribute += int32(delta.familyContribute)
	return true
}

// addCoin 增加铜币并推送余额。返回 false 表示余额上限或参数无效，调用方
// 必须在删除物品之前检查结果，避免“物品已扣、收益未到账”。
func (s *Server) addCoin(ch *channel, v int64) bool {
	if v < 0 {
		return false
	}
	return s.applyCurrencyDelta(ch, currencyDelta{coin: v})
}

// addCoinNoPush applies a validated copper credit without emitting the
// NumericComponent notification. Handlers that must send their RPC response
// before the balance update (for example selling an item) use this to keep
// wire ordering deterministic.
func (s *Server) addCoinNoPush(ch *channel, v int64) bool {
	return v >= 0 && s.applyCurrencyDeltaNoPush(ch, currencyDelta{coin: v})
}

// spendCoin 扣减铜币；不足返回 false（不推送）。
func (s *Server) spendCoin(ch *channel, v int64) bool {
	if ch == nil || ch.session == nil || v < 0 || ch.session.coin < v {
		return false
	}
	ch.session.coin -= v
	s.pushMoney(ch)
	return true
}

// addYuanBao 增加元宝并推送。
func (s *Server) addYuanBao(ch *channel, v int64) bool {
	if v < 0 {
		return false
	}
	return s.applyCurrencyDelta(ch, currencyDelta{yuanBao: v})
}

func (s *Server) addVoucher(ch *channel, v int64) bool {
	if v < 0 {
		return false
	}
	return s.applyCurrencyDelta(ch, currencyDelta{voucher: v})
}

// spendYuanBao 扣减元宝；不足返回 false。
func (s *Server) spendYuanBao(ch *channel, v int64) bool {
	if ch == nil || ch.session == nil || v < 0 || ch.session.yuanBao < v {
		return false
	}
	ch.session.yuanBao -= v
	s.pushMoney(ch)
	return true
}

// spendVoucher 扣减兑换券；不足返回 false。
func (s *Server) spendVoucher(ch *channel, v int64) bool {
	if ch == nil || ch.session == nil || v < 0 || ch.session.voucher < v {
		return false
	}
	ch.session.voucher -= v
	s.pushMoney(ch)
	return true
}

// starCoinBalance returns the single authoritative star-coin balance. Star
// coins are intentionally kept as GoodsBase item 110205 because battle drops,
// consignment and the native bag UI all operate on that item stack.
func starCoinBalance(ss *session) int64 {
	return bagItemCount64(ss, consignmentCurrencyItemID)
}

func (s *Server) addStarCoin(ch *channel, v int64) bool {
	if ch == nil || ch.session == nil || v <= 0 {
		return v == 0
	}
	if v > int64(^uint32(0)>>1) {
		return false
	}
	staged, _, ok := stageBagGrants(ch.session, []bagGrant{{itemID: consignmentCurrencyItemID, count: int32(v)}})
	if !ok {
		return false
	}
	ch.session.bag = staged
	s.pushMoney(ch)
	s.pushBagSnapshot(ch)
	return true
}

func (s *Server) spendStarCoin(ch *channel, v int64) bool {
	if ch == nil || ch.session == nil || v < 0 || starCoinBalance(ch.session) < v {
		return false
	}
	if v == 0 {
		return true
	}
	canonicalizeStarCoins(ch.session)
	if !removeBagItemCount64(ch.session, consignmentCurrencyItemID, v) {
		return false
	}
	s.pushMoney(ch)
	s.pushBagSnapshot(ch)
	return true
}

func (s *Server) addHonor(ch *channel, v int64) bool {
	if v < 0 {
		return false
	}
	return s.applyCurrencyDelta(ch, currencyDelta{honor: v})
}

func (s *Server) spendHonor(ch *channel, v int64) bool {
	if ch == nil || ch.session == nil || v < 0 || ch.session.honor < v {
		return false
	}
	ch.session.honor -= v
	s.pushMoney(ch)
	return true
}

func (s *Server) addPVPCurrency(ch *channel, v int64) bool {
	if v < 0 {
		return false
	}
	return s.applyCurrencyDelta(ch, currencyDelta{pvpCurrency: v})
}

func (s *Server) spendPVPCurrency(ch *channel, v int64) bool {
	if ch == nil || ch.session == nil || v < 0 || ch.session.pvpCurrency < v {
		return false
	}
	ch.session.pvpCurrency -= v
	s.pushMoney(ch)
	return true
}

func (s *Server) addFamilyContribution(ch *channel, v int64) bool {
	if v < 0 {
		return false
	}
	return s.applyCurrencyDelta(ch, currencyDelta{familyContribute: v})
}

func (s *Server) spendFamilyContribution(ch *channel, v int64) bool {
	if ch == nil || ch.session == nil || v < 0 || int64(ch.session.familyContribute) < v {
		return false
	}
	ch.session.familyContribute -= int32(v)
	s.pushMoney(ch)
	return true
}

// ===================== 背包通用辅助 =====================

type bagGrant struct {
	itemID int32
	count  int32
	// source is copied to newly-created equipment instances. Empty means the
	// generic system source from newBagItem is retained.
	source            string
	purchaseSource    int32
	purchaseCurrency  int32
	purchaseUnitPrice int64
}

func cloneBagItem(it *bagItem) *bagItem {
	if it == nil {
		return nil
	}
	cloned := *it
	if it.MainAttr != nil {
		cloned.MainAttr = make(map[int32]float32, len(it.MainAttr))
		for key, value := range it.MainAttr {
			cloned.MainAttr[key] = value
		}
	}
	cloned.RandomAttrs = append([]int32(nil), it.RandomAttrs...)
	cloned.AddAttrs = append([]int32(nil), it.AddAttrs...)
	cloned.GemList = append([]int32(nil), it.GemList...)
	return &cloned
}

func cloneBagMap(src map[int32]*bagItem) map[int32]*bagItem {
	cloned := make(map[int32]*bagItem, len(src))
	for index, item := range src {
		if item != nil {
			cloned[index] = cloneBagItem(item)
		}
	}
	return cloned
}

// addItemToBagInPlace applies one grant to a staging bag. Equipment always uses
// one slot per instance; goods and materials fill existing stacks before using
// new slots and honor the MaxAmount value from the online tables.
func addItemToBagInPlace(ss *session, itemID, count int32) (int32, bool) {
	return addItemToBagInPlaceWithGrant(ss, bagGrant{itemID: itemID, count: count})
}

func addItemToBagInPlaceWithSource(ss *session, itemID, count int32, source string) (int32, bool) {
	return addItemToBagInPlaceWithGrant(ss, bagGrant{itemID: itemID, count: count, source: source})
}

func applyBagGrantOrigin(item *bagItem, grant bagGrant) {
	if item == nil {
		return
	}
	item.PurchaseSource = grant.purchaseSource
	item.PurchaseCurrency = grant.purchaseCurrency
	item.PurchaseUnitPrice = grant.purchaseUnitPrice
}

func addItemToBagInPlaceWithGrant(ss *session, grant bagGrant) (int32, bool) {
	itemID, count := grant.itemID, grant.count
	if ss == nil || ss.bag == nil || itemID <= 0 || count <= 0 {
		return invalidBagSlot, false
	}
	if isStarCoinItem(itemID) {
		// Currency grants do not consume a visible inventory slot.  Aggregate
		// legacy stacks in the staging map and keep the result in a negative slot.
		canonicalizeStarCoins(ss)
		remaining := int64(count)
		for _, item := range ss.bag {
			if item == nil || !isStarCoinItem(item.ItemId) || remaining <= 0 {
				continue
			}
			available := int64(math.MaxInt32) - int64(item.Count)
			if available <= 0 {
				continue
			}
			add := remaining
			if add > available {
				add = available
			}
			item.Count += int32(add)
			remaining -= add
		}
		for index := starCoinBagSlot; remaining > 0; index-- {
			add := remaining
			if add > int64(math.MaxInt32) {
				add = int64(math.MaxInt32)
			}
			ss.bag[index] = &bagItem{ItemId: consignmentCurrencyItemID,
				ItemType: int32(protocol.ItemType_GoodsItem), Count: int32(add)}
			remaining -= add
		}
		return starCoinBagSlot, true
	}
	template := newBagItem(itemID)
	applyBagGrantOrigin(template, grant)
	firstIndex := invalidBagSlot
	if template.ItemType == int32(protocol.ItemType_EquipItem) {
		for count > 0 {
			index := nextBagIndex(ss)
			if index < 0 {
				return invalidBagSlot, false
			}
			item := newBagItem(itemID)
			item.Count = 1
			if grant.source != "" {
				item.GetSource = grant.source
			}
			applyBagGrantOrigin(item, grant)
			ss.bag[index] = item
			if firstIndex < 0 {
				firstIndex = index
			}
			count--
		}
		return firstIndex, true
	}

	limit64 := rewardStackLimit(itemID)
	if limit64 <= 0 || limit64 > int64(^uint32(0)>>1) {
		limit64 = 999
	}
	limit := int32(limit64)
	for index := int32(0); index < bagSlotCount && count > 0; index++ {
		item := ss.bag[index]
		if item == nil || item.ItemId != itemID || item.ItemType != template.ItemType ||
			!samePurchaseOrigin(item, template) || item.Count >= limit {
			continue
		}
		add := limit - item.Count
		if add > count {
			add = count
		}
		item.Count += add
		count -= add
		if firstIndex < 0 {
			firstIndex = index
		}
	}
	for count > 0 {
		index := nextBagIndex(ss)
		if index < 0 {
			return invalidBagSlot, false
		}
		add := limit
		if add > count {
			add = count
		}
		item := newBagItem(itemID)
		item.Count = add
		if grant.source != "" && item.ItemType == int32(protocol.ItemType_EquipItem) {
			item.GetSource = grant.source
		}
		applyBagGrantOrigin(item, grant)
		ss.bag[index] = item
		count -= add
		if firstIndex < 0 {
			firstIndex = index
		}
	}
	return firstIndex, true
}

// stageBagGrants validates a complete reward batch against a cloned bag. The
// caller commits the returned map only when every item fits, so full-bag errors
// never leave partial stacks or partially delivered rewards behind.
func stageBagGrants(ss *session, grants []bagGrant) (map[int32]*bagItem, []int32, bool) {
	if ss == nil || ss.bag == nil {
		return nil, nil, false
	}
	shadow := &session{bag: cloneBagMap(ss.bag)}
	canonicalizeStarCoins(shadow)
	indices := make([]int32, 0, len(grants))
	for _, grant := range grants {
		index, ok := addItemToBagInPlaceWithGrant(shadow, grant)
		if !ok {
			return nil, nil, false
		}
		indices = append(indices, index)
	}
	return shadow.bag, indices, true
}

// addItemToBag atomically adds one item grant and returns the first stack/slot
// touched by it. A full bag returns -1 and leaves the original bag unchanged.
func (ss *session) addItemToBag(itemID int32, count int32) int32 {
	staged, indices, ok := stageBagGrants(ss, []bagGrant{{itemID: itemID, count: count}})
	if !ok {
		log.Printf("[P=%d] add item=%d count=%d rejected: bag full", ss.playerID, itemID, count)
		return invalidBagSlot
	}
	ss.bag = staged
	return indices[0]
}

// addItemToBagWithSource is the source-aware counterpart used by shop and
// reward handlers that grant equipment.  Goods/material stacks keep the same
// behavior as addItemToBag; only newly-created equipment receives source.
func (ss *session) addItemToBagWithSource(itemID, count int32, source string) int32 {
	staged, indices, ok := stageBagGrants(ss, []bagGrant{{itemID: itemID, count: count, source: source}})
	if !ok {
		log.Printf("[P=%d] add item=%d count=%d rejected: bag full", ss.playerID, itemID, count)
		return invalidBagSlot
	}
	ss.bag = staged
	return indices[0]
}

func (ss *session) addItemToBagWithGrant(grant bagGrant) int32 {
	staged, indices, ok := stageBagGrants(ss, []bagGrant{grant})
	if !ok {
		log.Printf("[P=%d] add item=%d count=%d rejected: bag full", ss.playerID, grant.itemID, grant.count)
		return invalidBagSlot
	}
	ss.bag = staged
	return indices[0]
}

// removeBagCount 从背包格扣减数量；扣完删格。成功返回 true。
func (ss *session) removeBagCount(index int32, count int32) bool {
	it, ok := ss.bag[index]
	if !ok || it == nil || count <= 0 || it.Count < count {
		return false
	}
	it.Count -= count
	if it.Count <= 0 {
		delete(ss.bag, index)
	}
	return true
}

// removeBagCountByItem 按物品 id 跨格扣除数量（技能书等堆叠物）；扣完删格。
func (ss *session) removeBagCountByItem(itemID int32, count int32) {
	if ss == nil || count <= 0 {
		return
	}
	for i, it := range ss.bag {
		if count <= 0 {
			break
		}
		if it == nil || it.ItemId != itemID {
			continue
		}
		if it.Count <= count {
			count -= it.Count
			delete(ss.bag, i)
		} else {
			it.Count -= count
			count = 0
		}
	}
}

// pushBagSnapshot 推送背包全量快照（M2C_SendBag）。
func (s *Server) pushBagSnapshot(ch *channel) {
	ss := ch.session
	if ss == nil || ss.playerID == 0 {
		return
	}
	sb, err := proto.Marshal(&protocol.M2C_SendBag{RpcId: 0, ActorId: ss.playerID})
	if err == nil {
		sb = encodeBagMapList(sb, 1, bagPairs(ss.bag))
		s.sendRawPush(ch, protocol.OpM2C_SendBag, sb)
	}
}

// ===================== 宠物 =====================
// petState：单宠物模型（见 17 文档）。宠物状态存会话并随 saveData 持久化。

type petState struct {
	PetId     int32  `json:"id"`   // 宠物配置 id（2101 基础，2102/2103 升级形态）
	Level     int32  `json:"lv"`   // 等级
	Exp       int32  `json:"exp"`  // 经验
	Intimacy  int32  `json:"im"`   // 亲密度
	Name      string `json:"name"` // 宠物名
	IsShow    bool   `json:"show"` // 是否跟随展示
	Active    int32  `json:"act"`  // 活跃度
	EatCount  int32  `json:"eat"`  // 今日已喂食次数
	PetState  int32  `json:"ps"`   // 0等待 1跟随 2嬉戏 3锻炼 4探险
	ActionEnd int64  `json:"ae"`   // 动作结束 unix 秒；0=无动作
	LastDay   string `json:"day"`  // 上次喂食/动作日（yyyyMMdd），跨日重置
	Rewarded  bool   `json:"rw"`   // 当前动作奖励是否已领
}

// newPet 创建默认宠物（PetConfig 2101 起手）。
func newPet() *petState {
	p := &petState{PetId: 2101, Level: 1, Name: "小宠物", IsShow: true}
	if tables != nil {
		if row, ok := tables.petConfig[2101]; ok {
			if n, ok := row["Name"].(string); ok && n != "" {
				p.Name = n
			}
		}
	}
	return p
}

// petDailyReset 跨日重置喂食次数。
func (p *petState) petDailyReset() {
	day := time.Now().Format("20060102")
	if p.LastDay != day {
		p.LastDay = day
		p.EatCount = 0
	}
}

// ===================== 邮件 =====================

type mailItemMsg struct {
	ItemId            int32 `json:"i"`
	Count             int32 `json:"c"`
	IsLock            bool  `json:"l"`
	IsHasItem         bool  `json:"h"`
	PurchaseSource    int32 `json:"-"`
	PurchaseCurrency  int32 `json:"-"`
	PurchaseUnitPrice int64 `json:"-"`
}

type mailMsg struct {
	Id         int64         `json:"id"`
	Title      string        `json:"t"`
	Content    string        `json:"c"`
	SenderName string        `json:"s"`
	State      int32         `json:"st"` // 0 未领取 1 已领取
	RemainTime int64         `json:"rt"` // 剩余秒（0=永久）
	Items      []mailItemMsg `json:"it"` // 附件
}

// ===================== 好友 =====================

type friendInfo struct {
	Id        int64  `json:"id"`
	Name      string `json:"n"`
	Job       int32  `json:"j"`
	Level     int32  `json:"l"`
	LastLogin int64  `json:"ll"`
}

// ===================== 签到 =====================

type signinState struct {
	LastDay              string                    `json:"day"`           // yyyyMMdd 最近签到日
	LastMonth            string                    `json:"m"`             // yyyyMM 本月记录月份
	MonthCount           int32                     `json:"mc"`            // 本月累计签到次数
	MonthGotIDs          []int32                   `json:"mg,omitempty"`  // 本月已领累计奖励的 SignInRewardMonth._id；推客户端时转换成 Times
	MonthGotMonth        string                    `json:"mm,omitempty"`  // 记录月（跨月清空 MonthGotIDs）
	MapCoinDay           string                    `json:"mcd,omitempty"` // yyyyMMdd 地图金币次数所属日期
	MapCoinExtra         int32                     `json:"mce,omitempty"` // 当日购买的额外拾取次数
	MapCoinClaimed       int32                     `json:"mcc,omitempty"` // 当日已经拾取的次数
	TrialHighestID       int32                     `json:"th,omitempty"`  // 已通关的最高 TrialCopy._id
	TrialRewardDay       string                    `json:"trd,omitempty"` // yyyyMMdd 最近一次批量领取试炼奖励日期
	ManualEquipTier1     int32                     `json:"me1,omitempty"` // 初级手工当前周期已通关层
	ManualEquipTier2     int32                     `json:"me2,omitempty"` // 中级手工当前周期已通关层
	ManualEquipTier3     int32                     `json:"me3,omitempty"` // 高级手工当前周期已通关层
	ManualRareCraftCount int32                     `json:"mrc,omitempty"`
	ManualRarePityAt     int32                     `json:"mrp,omitempty"`
	ManualEpicCraftCount int32                     `json:"mec,omitempty"`
	ManualEpicPityAt     int32                     `json:"mep,omitempty"`
	QuizScore            int32                     `json:"qs,omitempty"`  // 答题累计积分
	QuizTimeMS           int64                     `json:"qt,omitempty"`  // 答题累计用时（同分时越短越前）
	QuizCompleted        bool                      `json:"qc,omitempty"`  // 已完成过答题任务
	PVPScore             int32                     `json:"ps,omitempty"`  // 个人竞技积分
	PVPBattleDay         string                    `json:"pbd,omitempty"` // yyyyMMdd 个人竞技场次所属日期
	PVPBattleCount       int32                     `json:"pbc,omitempty"` // 个人竞技场次
	PVPMatchCount        int32                     `json:"pmc,omitempty"` // 当前匹配池人数快照
	PVPIsMatching        bool                      `json:"pim,omitempty"` // 是否正在匹配
	DungeonQuotaDay      string                    `json:"dqd,omitempty"`
	SpaceTravelRemaining int32                     `json:"str,omitempty"`
	DeathTowerRemaining  int32                     `json:"dtr,omitempty"`
	FamilyBossKeys       int32                     `json:"fbk,omitempty"`
	NormalRunDay         string                    `json:"nrd,omitempty"`
	OnlineRewardMS       int64                     `json:"oram,omitempty"`
	TaskKillProgress     map[int32]map[int32]int32 `json:"tk,omitempty"`
	TaskAnyKillProgress  map[int32]int32           `json:"tak,omitempty"`
	TaskDialogProgress   map[int32]bool            `json:"td,omitempty"`
	TaskQuizProgress     map[int32]bool            `json:"tq,omitempty"`
}

func (s *signinState) ensureTaskProgress() {
	if s.TaskKillProgress == nil {
		s.TaskKillProgress = make(map[int32]map[int32]int32)
	}
	if s.TaskAnyKillProgress == nil {
		s.TaskAnyKillProgress = make(map[int32]int32)
	}
	if s.TaskDialogProgress == nil {
		s.TaskDialogProgress = make(map[int32]bool)
	}
	if s.TaskQuizProgress == nil {
		s.TaskQuizProgress = make(map[int32]bool)
	}
}

// ===================== 仓库（物品仓库） =====================
// session.store：仓库格子（0 基全局 Index，每页 60 格）；session.storeCoin：仓库铜币。

// ===================== 在线玩家查找（聊天/组队/好友广播） =====================

// sortedMailIDs 按 id 升序排序（分页稳定）。
func sortedMailIDs(list []*mailMsg) []int64 {
	ids := make([]int64, 0, len(list))
	for _, m := range list {
		if m != nil {
			ids = append(ids, m.Id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
