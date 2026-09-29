package main

import (
	"fmt"
	"log"
	"math/rand"
	"sort"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// onDeleteItem handles the client's discard protocol. M2C_DeleteItem has no
// BagMapList field, so the updated snapshot is delivered through M2C_SendBag.
func (s *Server) onDeleteItem(ch *channel, req *protocol.C2M_DeleteItem) proto.Message {
	resp := &protocol.M2C_DeleteItem{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Message = "请先登录"
		return resp
	}
	ss := ch.session
	it := ss.bag[req.Index]
	if it == nil {
		resp.Message = "物品不存在"
		return resp
	}
	if it.IsLock {
		resp.Message = "锁定物品不能丢弃"
		return resp
	}
	if req.Count <= 0 || req.Count > it.Count {
		resp.Message = "数量无效"
		return resp
	}
	itemID := it.ItemId
	if !ss.removeBagCount(req.Index, req.Count) {
		resp.Message = "数量无效"
		return resp
	}
	base, err := proto.Marshal(resp)
	if err != nil {
		resp.Message = "背包刷新失败"
		return resp
	}
	s.sendRawPush(ch, protocol.OpM2C_DeleteItem, base)
	s.pushBagSnapshot(ch)
	s.saveData(ch)
	log.Printf("[S=%d] discard bag index=%d item=%d count=%d", ch.id, req.Index, itemID, req.Count)
	return nil
}

// bagtools.go：背包整理/拆分/装备升级/锻造。
//
// 这些 opcode 之前服务器未处理 → 客户端 await 无响应 → 挂起刷屏 → 掉线
// （与 20071 SelectEnermy 同机制）。全部补完整 RPC 响应（RpcId + BagMapList
// 全量，客户端 UpdateBagUI 刷新背包）。

// ===================== 拆分 20267 → 20268 =====================
// C2M_SplitItem{Index=1, Count=2} → M2C_SplitItem{BagMapList=1}。
// 把背包 Index 格的消耗品拆出 Count 个到新格子（装备不可拆；至少留 1）。
func (s *Server) onSplitItem(ch *channel, req *protocol.C2M_SplitItem) proto.Message {
	ss := ch.session
	resp := &protocol.M2C_SplitItem{RpcId: req.RpcId}
	if ch == nil || ss == nil || ss.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	it, ok := ss.bag[req.Index]
	if !ok || it == nil {
		resp.Error, resp.Message = errBadParam, "物品不存在"
		return resp
	}
	if it.ItemType == 1 {
		resp.Error, resp.Message = errBadParam, "装备不可拆分"
		return resp
	}
	if req.Count <= 0 || req.Count >= it.Count {
		resp.Error, resp.Message = errBadParam, "拆分数量无效"
		return resp
	}
	idx := nextBagIndex(ss)
	if idx < 0 {
		resp.Error, resp.Message = errBadParam, "背包已满"
		return resp
	}
	it.Count -= req.Count
	nw := cloneBagItem(it)
	nw.Count = req.Count
	ss.bag[idx] = nw

	base, err := proto.Marshal(resp)
	if err != nil {
		resp.Error, resp.Message = errBadParam, "序列化失败"
		return resp
	}
	base = encodeBagMapList(base, 1, bagPairs(ss.bag))
	s.sendRawPush(ch, protocol.OpM2C_SplitItem, base)
	s.saveData(ch)
	log.Printf("[S=%d] split item index=%d count=%d -> new index=%d", ch.id, req.Index, req.Count, idx)
	return nil
}

// ===================== 整理背包 20269 → 20270 =====================
// C2M_SortBag{} → M2C_SortBag{BagMapList=1}。
// 按 (ItemType, ItemId) 排序重排到格子 1..n；同 Id 消耗品堆叠（MaxAmount 上限）。
func (s *Server) onSortBag(ch *channel, req *protocol.C2M_SortBag) proto.Message {
	ss := ch.session
	resp := &protocol.M2C_SortBag{RpcId: req.RpcId}
	if ch == nil || ss == nil || ss.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	type entry struct {
		it *bagItem
	}
	var list []entry
	canonicalizeStarCoins(ss)
	for _, it := range ss.bag {
		if it != nil && !isStarCoinItem(it.ItemId) {
			list = append(list, entry{it})
		}
	}
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i].it, list[j].it
		if a.ItemType != b.ItemType {
			return a.ItemType < b.ItemType
		}
		return a.ItemId < b.ItemId
	})
	// 重排：同 ItemId 且可堆叠的合并
	nb := make(map[int32]*bagItem)
	var next int32
	for _, e := range list {
		it := e.it
		if it.ItemType != 1 {
			// 尝试堆到已有同 Id 格子
			for i := int32(0); i < next; i++ {
				old, ok := nb[i]
				if !ok || old == nil || old.ItemId != it.ItemId || old.ItemType != it.ItemType || !samePurchaseOrigin(old, it) {
					continue
				}
				max := int32(rewardStackLimit(it.ItemId))
				if max <= 0 {
					max = 999
				}
				if old.Count < max {
					add := it.Count
					if old.Count+add > max {
						add = max - old.Count
					}
					old.Count += add
					it.Count -= add
					if it.Count <= 0 {
						break
					}
				}
			}
			if it.Count <= 0 {
				continue
			}
		}
		nb[next] = it
		next++
	}
	for index, it := range ss.bag {
		if it != nil && isStarCoinItem(it.ItemId) {
			nb[index] = it
		}
	}
	ss.bag = nb
	base, err := proto.Marshal(resp)
	if err != nil {
		resp.Error, resp.Message = errBadParam, "序列化失败"
		return resp
	}
	base = encodeBagMapList(base, 1, bagPairs(ss.bag))
	s.sendRawPush(ch, protocol.OpM2C_SortBag, base)
	s.saveData(ch)
	log.Printf("[S=%d] sort bag %d -> %d items", ch.id, len(list), len(ss.bag))
	return nil
}

// ===================== 物品进化 20277 → 20278 =====================
// C2M_Upgrade{Index=1, isLock=2} → M2C_Upgrade{BagMapList=1}。
// Index 中的一件物品是进化主体，UpgradeNeedMaterialArr 是额外投入；
// 失败后按 FailCastArr 返还。isLock 决定成功产物是否锁定。
func (s *Server) onUpgradeEquip(ch *channel, req *protocol.C2M_Upgrade) proto.Message {
	resp := &protocol.M2C_Upgrade{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	ss := ch.session
	it := ss.bag[req.Index]
	if it == nil {
		resp.Message = "物品不存在"
		return resp
	}
	if tables == nil || tables.itemUpgrade == nil {
		resp.Message = "进化配置未加载"
		return resp
	}
	row := tables.itemUpgrade[int64(it.ItemId)]
	if row == nil {
		resp.Message = "此物品不能进化"
		return resp
	}

	staged, success, message := stageItemUpgrade(ss, req.Index, row, req.IsLock, rand.Float64)
	if message != "" {
		resp.Message = message
		return resp
	}
	ss.bag = staged
	if !success {
		// UpgradeUI displays a non-empty Message before its BagMapList branch.
		// Push the changed snapshot independently so failed attempts cannot leave
		// the local inventory stale.
		s.pushBagSnapshot(ch)
		resp.Message = "进化失败"
	}

	base, err := proto.Marshal(resp)
	if err != nil {
		resp.Message = "背包刷新失败"
		return resp
	}
	base = encodeBagMapList(base, 1, bagPairs(ss.bag))
	s.sendRawPush(ch, protocol.OpM2C_Upgrade, base)
	s.saveData(ch)
	log.Printf("[S=%d] upgrade item index=%d item=%d target=%d success=%v lock=%v",
		ch.id, req.Index, it.ItemId, int32(num(row["UpgradeItemId"])), success, req.IsLock)
	return nil
}

type itemUpgradeCost struct {
	itemID int32
	count  int32
}

func itemUpgradeCosts(row map[string]interface{}, field, idField, countField string) ([]itemUpgradeCost, bool) {
	values := arrOf(row[field])
	costs := make([]itemUpgradeCost, 0, len(values))
	for _, value := range values {
		entry, _ := value.(map[string]interface{})
		if entry == nil {
			return nil, false
		}
		itemID := int32(num(entry[idField]))
		count := int32(num(entry[countField]))
		if itemID <= 0 || count < 0 {
			return nil, false
		}
		if count > 0 {
			costs = append(costs, itemUpgradeCost{itemID: itemID, count: count})
		}
	}
	return costs, true
}

func stageItemUpgrade(ss *session, index int32, row map[string]interface{}, isLock bool, roll func() float64) (map[int32]*bagItem, bool, string) {
	if ss == nil || row == nil || roll == nil {
		return nil, false, "进化状态无效"
	}
	target := ss.bag[index]
	if target == nil || target.Count <= 0 || target.ItemId != int32(num(row["_id"])) {
		return nil, false, "物品不存在或已变化"
	}
	upgradeItemID := int32(num(row["UpgradeItemId"]))
	if upgradeItemID <= 0 {
		return nil, false, "进化目标配置无效"
	}
	materials, ok := itemUpgradeCosts(row, "UpgradeNeedMaterialArr", "UpgradeNeedMaterial_Id", "UpgradeNeedMaterial_Count")
	if !ok || len(materials) == 0 {
		return nil, false, "进化材料配置无效"
	}

	shadow := &session{bag: cloneBagMap(ss.bag)}
	if !shadow.removeBagCount(index, 1) {
		return nil, false, "进化物品数量不足"
	}
	for _, material := range materials {
		if bagItemCount(shadow, material.itemID) < material.count {
			return nil, false, "进化材料不足"
		}
		shadow.removeBagCountByItem(material.itemID, material.count)
	}

	rate := numf(row["SucceefulRate"])
	success := rate >= 1 || (rate > 0 && roll() < rate)
	if success {
		result := newBagItem(upgradeItemID)
		if result.ItemType == int32(protocol.ItemType_EquipItem) {
			result.GetSource = "装备升级"
		}
		result.IsLock = target.IsLock || isLock
		if shadow.bag[index] == nil {
			shadow.bag[index] = result
		} else if _, added := addItemToBagInPlace(shadow, upgradeItemID, 1); !added {
			return nil, false, "背包空间不足"
		}
		return shadow.bag, true, ""
	}

	refunds, ok := itemUpgradeCosts(row, "FailCastArr", "_Id", "Count")
	if !ok {
		return nil, false, "进化失败返还配置无效"
	}
	for _, refund := range refunds {
		if _, added := addItemToBagInPlace(shadow, refund.itemID, refund.count); !added {
			return nil, false, "背包空间不足"
		}
	}
	return shadow.bag, false, ""
}

// ===================== 锻造/打造 20204 → 20205 =====================
// C2M_ForgeEquip{Id=2} → M2C_ForgeEquip{BagMapList=1}。
// Id = EquipForge._id（配方：NeedMaterialArr 材料 + NeedEquipmentId 前置装备 +
// NeedCoin 铜币 + JobType 职业；产出 MadeEquipId）。原版表 437 条。
func (s *Server) onForgeEquip(ch *channel, req *protocol.C2M_ForgeEquip) proto.Message {
	ss := ch.session
	resp := &protocol.M2C_ForgeEquip{RpcId: req.RpcId}
	if ch == nil || ss == nil || ss.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	reject := func(msg string) proto.Message {
		log.Printf("[S=%d] forge equip rejected: %s", ch.id, msg)
		resp.Message = msg
		return resp
	}
	if tables == nil {
		return reject("配置未加载")
	}
	row, ok := tables.equipForge[int64(req.Id)]
	if !ok {
		return reject("打造配方不存在")
	}
	job := int32(num(row["JobType"]))
	if job != 0 && job != jobTypeOf(ss.jobID) {
		return reject("职业不符，无法打造")
	}
	// Stage every input and output on a cloned bag. This makes malformed
	// recipes, duplicate material IDs and full-bag failures fully atomic.
	shadow := &session{bag: cloneBagMap(ss.bag)}
	needEquipment := int32(num(row["NeedEquipmentId"]))
	if needEquipment > 0 {
		if !consumeBagItem(shadow, needEquipment, 1) {
			return reject("缺少前置装备")
		}
	}
	// 材料消耗（NeedMaterialArr；任一不足时 shadow 直接丢弃）
	materials := arrOf(row["NeedMaterialArr"])
	type mat struct{ id, cnt int32 }
	var mats []mat
	for _, e := range materials {
		eo, _ := e.(map[string]interface{})
		if eo == nil {
			continue
		}
		mats = append(mats, mat{int32(num(eo["NeedMaterial_Id"])), int32(num(eo["NeedMaterial_Count"]))})
	}
	for _, m := range mats {
		if m.id <= 0 || m.cnt <= 0 || !consumeBagItem(shadow, m.id, m.cnt) {
			return reject("材料不足")
		}
	}
	// 铜币
	coin := int64(num(row["NeedCoin"]))
	if coin < 0 || ss.coin < coin {
		return reject("铜币不足")
	}
	// 产出入包
	made := int32(num(row["MadeEquipId"]))
	if made <= 0 {
		return reject("配方异常")
	}
	if idx := shadow.addItemToBag(made, 1); idx < 0 {
		return reject("背包已满")
	}
	ss.bag = shadow.bag
	ss.coin -= coin
	// 推送 1028 铜币
	s.pushMoney(ch)
	base, err := proto.Marshal(resp)
	if err != nil {
		return reject("序列化失败")
	}
	base = encodeBagMapList(base, 1, bagPairs(ss.bag))
	s.sendRawPush(ch, protocol.OpM2C_ForgeEquip, base)
	s.saveData(ch)
	log.Printf("[S=%d] forge equip recipe=%d made=%d coin=%d", ch.id, req.Id, made, coin)
	return nil
}

// wornSlotOfItem：返回穿戴中与物品同 Id 的槽位（未穿戴返回 false）。
func (ss *session) wornSlotOfItem(itemID int32) (int32, bool) {
	for slot, it := range ss.worn {
		if it != nil && it.ItemId == itemID {
			return slot, true
		}
	}
	return 0, false
}

// ===================== 交换背包格子 20265 → 20266 =====================
// C2M_ChangeItemPos{Pos1=1, Pos2=2} → M2C_ChangeItemPos{BagMapList=1}。
func (s *Server) onChangeItemPos(ch *channel, req *protocol.C2M_ChangeItemPos) proto.Message {
	ss := ch.session
	resp := &protocol.M2C_ChangeItemPos{RpcId: req.RpcId}
	if ch == nil || ss == nil || ss.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	if req.Pos1 < 0 || req.Pos1 >= bagSlotCount || req.Pos2 < 0 || req.Pos2 >= bagSlotCount {
		resp.Message = "背包格子无效"
		return resp
	}
	if req.Pos1 == req.Pos2 {
		// 同格：直接成功
	} else {
		ss.bag[req.Pos1], ss.bag[req.Pos2] = ss.bag[req.Pos2], ss.bag[req.Pos1]
		if ss.bag[req.Pos1] == nil {
			delete(ss.bag, req.Pos1)
		}
		if ss.bag[req.Pos2] == nil {
			delete(ss.bag, req.Pos2)
		}
	}
	base, err := proto.Marshal(resp)
	if err != nil {
		resp.Error, resp.Message = errBadParam, "序列化失败"
		return resp
	}
	base = encodeBagMapList(base, 1, bagPairs(ss.bag))
	s.sendRawPush(ch, protocol.OpM2C_ChangeItemPos, base)
	s.saveData(ch)
	log.Printf("[S=%d] change item pos %d <-> %d", ch.id, req.Pos1, req.Pos2)
	return nil
}

// ===================== 元宝兑换代金券 20312 → 20313 =====================
// C2M_ChargeVoucher{YuanBao=1} → M2C_ChargeVoucher。
// 自建服兑换率 1:1（1 元宝 → 1 代金券）；不足返回错误。
func (s *Server) onChargeVoucher(ch *channel, req *protocol.C2M_ChargeVoucher) proto.Message {
	ss := ch.session
	resp := &protocol.M2C_ChargeVoucher{RpcId: req.RpcId}
	if ch == nil || ss == nil || ss.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	if req.YuanBao <= 0 {
		resp.Error, resp.Message = errBadParam, "数量无效"
		return resp
	}
	amount := int64(req.YuanBao)
	if !ss.canApplyCurrencyDelta(currencyDelta{voucher: amount}) {
		resp.Error, resp.Message = errBadParam, "代金券已达上限"
		return resp
	}
	if !s.spendYuanBao(ch, amount) {
		resp.Error, resp.Message = errBadParam, "元宝不足"
		return resp
	}
	if !s.addVoucher(ch, amount) {
		ss.yuanBao += amount
		s.pushMoney(ch)
		resp.Error, resp.Message = errBadParam, "代金券已达上限"
		return resp
	}
	s.saveData(ch)
	log.Printf("[S=%d] charge voucher %d yuanbao -> %d voucher", ch.id, req.YuanBao, ss.voucher)
	return resp
}

// ===================== 兑换星币 20314 → 20315 =====================
// C2M_ChargeCoin.Gem 是玩家在弹窗里输入的数量。客户端弹窗原文
// “请输入您要兑换的星币数量，星币：铜币=1：20”给出比例 1 星币 = 20 铜币。
// 兑换方向是【花星币换铜币】：扣 Gem 个星币，进账 Gem×20 铜币。
// 星币是背包物品 110205（隐藏在 bagSlot -1000000），不是 NumericType 货币。
func (s *Server) onChargeCoin(ch *channel, req *protocol.C2M_ChargeCoin) proto.Message {
	resp := &protocol.M2C_ChargeCoin{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	ss := ch.session
	if req.Gem <= 0 {
		resp.Message = "数量无效"
		return resp
	}
	spend := int64(req.Gem)
	if starCoinBalance(ss) < spend {
		resp.Message = "星币不足"
		return resp
	}
	gain := spend * 20
	if !ss.canApplyCurrencyDelta(currencyDelta{coin: gain}) {
		resp.Message = "铜币已达上限"
		return resp
	}
	// spendStarCoin 已推送余额与背包快照（星币条目会消失），这里只补铜币入账的推送。
	if !s.spendStarCoin(ch, spend) {
		resp.Message = "星币不足"
		return resp
	}
	ss.coin += gain
	s.pushMoney(ch)
	s.saveData(ch)
	log.Printf("[S=%d] exchange %d star coins for %d copper", ch.id, spend, gain)
	return resp
}

func resetMapCoinDaily(ss *session, now time.Time) {
	if ss.signin == nil {
		ss.signin = &signinState{}
	}
	day := now.Format("20060102")
	if ss.signin.MapCoinDay != day {
		ss.signin.MapCoinDay = day
		ss.signin.MapCoinExtra = 0
		ss.signin.MapCoinClaimed = 0
	}
}

// ===================== 金币怪 20351 → 20354 =====================
// 只有当前场景真实生成过金币怪时才能领取；次数、奖励和购买价格来自 Gameplay 热更。
func (s *Server) onGetMapCoin(ch *channel, req *protocol.C2M_GetMapCoin) proto.Message {
	resp := &protocol.M2C_GetMapCoin{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	ss := ch.session
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	resetMapCoinDaily(ss, time.Now())
	if ss.mapCoinUnitID == 0 || ss.fieldMonsterConfigIDs[ss.mapCoinUnitID] != mapCoinMonsterConfigID {
		resp.Message = "地图金币怪已失效"
		return resp
	}
	_, minimumReward, maximumReward, dailyBase, _ := gameplayMapCoinSettings()
	if ss.signin.MapCoinClaimed >= dailyBase+ss.signin.MapCoinExtra {
		resp.Message = "今日拾取地图金币次数已用完"
		s.saveData(ch)
		return resp
	}
	reward := randomRangeAmount(minimumReward, maximumReward)
	if !ss.canApplyCurrencyDelta(currencyDelta{coin: reward}) {
		resp.Message = "铜币已达上限"
		return resp
	}
	unitID := consumeMapCoinMonster(ss)
	ss.signin.MapCoinClaimed++
	ss.coin += reward
	s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
		UnitId: ss.playerID, NumericType: ntCoin, Value: float32(ss.coin), ActorId: ss.playerID,
	})
	s.sendPush(ch, protocol.OpM2C_SendTip, &protocol.M2C_SendTip{
		Message: fmt.Sprintf("恭喜你获得%d铜币", reward), ActorId: ss.playerID,
	})
	s.sendPush(ch, protocol.OpM2C_DisposeMapMonster, &protocol.M2C_DisposeMapMonster{
		Id: unitID, ActorId: ss.playerID,
	})
	s.saveData(ch)
	log.Printf("[S=%d] get map coin +%d total=%d count=%d/%d", ch.id, reward, ss.coin,
		ss.signin.MapCoinClaimed, dailyBase+ss.signin.MapCoinExtra)
	return resp
}

func (s *Server) onAddMapCoinCount(ch *channel, req *protocol.C2M_AddMapCoinCount) proto.Message {
	resp := &protocol.M2C_AddMapCoinCount{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	ss := ch.session
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	resetMapCoinDaily(ss, time.Now())
	_, _, _, _, voucherCost := gameplayMapCoinSettings()
	if !s.spendVoucher(ch, voucherCost) {
		// Butler callbacks treat a non-zero RPC error as a transport failure.
		resp.Message = fmt.Sprintf("代金券不足，需要%d代金券", voucherCost)
		return resp
	}
	ss.signin.MapCoinExtra++
	s.saveData(ch)
	log.Printf("[S=%d] add map coin count voucher=%d extra=%d", ch.id, voucherCost, ss.signin.MapCoinExtra)
	return resp
}
