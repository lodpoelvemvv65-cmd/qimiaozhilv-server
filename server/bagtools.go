package main

import (
	"log"
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
	nw := newBagItem(it.ItemId)
	nw.Count = req.Count
	nw.ItemType = it.ItemType
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
	for _, it := range ss.bag {
		if it != nil {
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
				if !ok || old == nil || old.ItemId != it.ItemId || old.ItemType != it.ItemType {
					continue
				}
				max := int32(999)
				if tables != nil {
					if gb, ok := tables.goodsBase[int64(it.ItemId)]; ok {
						if m := int32(num(gb["MaxAmount"])); m > 0 {
							max = m
						}
					}
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

// ===================== 装备升级/进化 20277 → 20278 =====================
// C2M_Upgrade{Index=1, isLock=2} → M2C_Upgrade{BagMapList=1}。
// 自建服简化：背包格装备 Level+1（不消耗材料），属性重推。
func (s *Server) onUpgradeEquip(ch *channel, req *protocol.C2M_Upgrade) proto.Message {
	ss := ch.session
	resp := &protocol.M2C_Upgrade{RpcId: req.RpcId}
	if ch == nil || ss == nil || ss.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	it, ok := ss.bag[req.Index]
	if !ok || it == nil {
		resp.Error, resp.Message = errBadParam, "物品不存在"
		return resp
	}
	if it.ItemType != 1 {
		resp.Error, resp.Message = errBadParam, "只能升级装备"
		return resp
	}
	it.Level++
	if it.Level > 20 {
		it.Level = 20 // 上限
	}
	// 穿上中的装备：同步穿戴属性 + 角色数值
	if _, worn := ss.wornSlotOfItem(it.ItemId); worn {
		applySkinEquip(ss)
		s.pushPlayerAttrs(ch)
		s.pushUnitCharacter(ch)
	}
	base, err := proto.Marshal(resp)
	if err != nil {
		resp.Error, resp.Message = errBadParam, "序列化失败"
		return resp
	}
	base = encodeBagMapList(base, 1, bagPairs(ss.bag))
	s.sendRawPush(ch, protocol.OpM2C_Upgrade, base)
	s.saveData(ch)
	log.Printf("[S=%d] upgrade equip index=%d item=%d level=%d", ch.id, req.Index, it.ItemId, it.Level)
	return nil
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
	// 前置装备（NeedEquipmentId 非 0 时需消耗 1 件）
	if need := int32(num(row["NeedEquipmentId"])); need > 0 {
		if !consumeBagItem(ss, need, 1) {
			return reject("缺少前置装备")
		}
	}
	// 材料消耗（NeedMaterialArr；任一不足回滚已扣）
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
		if !consumeBagItem(ss, m.id, m.cnt) {
			// 回滚已扣材料
			for _, r := range mats {
				if r.id == m.id {
					break
				}
				ss.addItemToBag(r.id, r.cnt)
			}
			if need := int32(num(row["NeedEquipmentId"])); need > 0 {
				ss.addItemToBag(need, 1)
			}
			return reject("材料不足")
		}
	}
	// 铜币
	coin := int64(num(row["NeedCoin"]))
	if ss.coin < coin {
		for _, m := range mats {
			ss.addItemToBag(m.id, m.cnt)
		}
		if need := int32(num(row["NeedEquipmentId"])); need > 0 {
			ss.addItemToBag(need, 1)
		}
		return reject("铜币不足")
	}
	ss.coin -= coin
	// 产出入包
	made := int32(num(row["MadeEquipId"]))
	if made <= 0 {
		return reject("配方异常")
	}
	if idx := ss.addItemToBag(made, 1); idx < 0 {
		// 背包满回滚
		ss.coin += coin
		for _, m := range mats {
			ss.addItemToBag(m.id, m.cnt)
		}
		if need := int32(num(row["NeedEquipmentId"])); need > 0 {
			ss.addItemToBag(need, 1)
		}
		return reject("背包已满")
	}
	// 推送 1028 铜币
	s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
		UnitId: ss.playerID, NumericType: 1028, Value: float32(ss.coin), ActorId: ss.playerID,
	})
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
	if !s.spendYuanBao(ch, int64(req.YuanBao)) {
		resp.Error, resp.Message = errBadParam, "元宝不足"
		return resp
	}
	ss.voucher += int64(req.YuanBao)
	s.pushMoney(ch)
	s.saveData(ch)
	log.Printf("[S=%d] charge voucher %d yuanbao -> %d voucher", ch.id, req.YuanBao, ss.voucher)
	return resp
}

// ===================== 兑换星币 20314 → 20315 =====================
// C2M_ChargeCoin{Gem=1} → M2C_ChargeCoin。客户端"兑换星币"入口
// （用宝石/元宝兑换星币货币）。自建服无独立星币货币——Gem 按元宝处理，
// 1 元宝 → 1000 铜币，避免客户端 await 无响应卡死。
func (s *Server) onChargeCoin(ch *channel, req *protocol.C2M_ChargeCoin) proto.Message {
	ss := ch.session
	resp := &protocol.M2C_ChargeCoin{RpcId: req.RpcId}
	if ch == nil || ss == nil || ss.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	if req.Gem <= 0 {
		resp.Error, resp.Message = errBadParam, "数量无效"
		return resp
	}
	if !s.spendYuanBao(ch, int64(req.Gem)) {
		resp.Error, resp.Message = errBadParam, "元宝不足"
		return resp
	}
	s.addCoin(ch, int64(req.Gem)*1000)
	s.saveData(ch)
	log.Printf("[S=%d] charge coin %d gem -> %d coin", ch.id, req.Gem, int64(req.Gem)*1000)
	return resp
}

const (
	mapCoinDailyBaseCount = int32(1)
	mapCoinVoucherCost    = int64(20)
)

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
// 每日默认可拾取一次；万能管家可花费 20 代金券购买当天额外次数。
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
	if ss.signin.MapCoinClaimed >= mapCoinDailyBaseCount+ss.signin.MapCoinExtra {
		resp.Message = "今日拾取地图金币次数已用完"
		s.saveData(ch)
		return resp
	}
	ss.signin.MapCoinClaimed++
	reward := int64(500)
	ss.coin += reward
	s.pushMoney(ch)
	s.saveData(ch)
	log.Printf("[S=%d] get map coin +%d total=%d count=%d/%d", ch.id, reward, ss.coin,
		ss.signin.MapCoinClaimed, mapCoinDailyBaseCount+ss.signin.MapCoinExtra)
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
	if !s.spendVoucher(ch, mapCoinVoucherCost) {
		// Butler callbacks treat a non-zero RPC error as a transport failure.
		resp.Message = "代金券不足，需要20代金券"
		return resp
	}
	ss.signin.MapCoinExtra++
	s.saveData(ch)
	log.Printf("[S=%d] add map coin count voucher=%d extra=%d", ch.id, mapCoinVoucherCost, ss.signin.MapCoinExtra)
	return resp
}
