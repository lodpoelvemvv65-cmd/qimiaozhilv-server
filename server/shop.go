package main

import (
	"log"
	"sort"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// shop.go：商店 / 市场 / 多商店 / 寄售行 / 仓库 / 远程商店 处理器（13 文档）。
//
// 协议约定（见 文档/13-商店与货币系统.md）：
//   - BagMapList 必须返回背包全量快照；StoreList 至少覆盖当前仓库页，
//     当前实现返回全仓快照以支持客户端本地跨页搜索（见文档 §3.6）。
//   - 可预期的业务拒绝（货币不足/背包已满/配置不存在等）返回 Error=0 + Message，
//     客户端弹 Tip；只有硬错误（未登录/序列化失败）才用 Error≠0。
//   - 商店/市场按 Page+Slot 定位行（Page 过滤 + _id 升序 + 0 基序号）；
//     多商店按 Type+Index 定位。
//
// 货币：铜币 coin=1028（Shop/MultiShop Type2..4）、元宝=1024（Market 元宝市/多商店 Type1）、
// 兑换券=1025（Market 兑换券市）。扣款走 state_extra.go 的 spend* 助手（自动推 M2C_SyncUnitAttribute）。

// ===================== 公共辅助 =====================

// rowsOfPage 过滤表中 Page==pageIndex 的行，按 _id 升序返回（页内 SlotIndex 以此为序）。
func rowsOfPage(tbl map[int64]map[string]interface{}, page int32) []map[string]interface{} {
	var rows []map[string]interface{}
	for _, row := range tbl {
		if int32(num(row["Page"])) != page {
			continue
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return num(rows[i]["_id"]) < num(rows[j]["_id"]) })
	return rows
}

// rowsOfType 过滤多商店表中 Type==shopType 的行，按 _id 升序返回。
func rowsOfType(tbl map[int64]map[string]interface{}, shopType int32) []map[string]interface{} {
	var rows []map[string]interface{}
	for _, row := range tbl {
		if int32(num(row["Type"])) != shopType {
			continue
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return num(rows[i]["_id"]) < num(rows[j]["_id"]) })
	return rows
}

func safePriceTotal(unit int64, count int32) (int64, bool) {
	if unit < 0 || count <= 0 {
		return 0, false
	}
	if unit > int64(^uint64(0)>>1)/int64(count) {
		return 0, false
	}
	return unit * int64(count), true
}

// ===================== 元宝市场 Market（20172/20173 + 20176/20177） =====================

// onGetMarket：20172 → 20173。一次性下发全部市场条目 _id（M2C_GetMarket.MarketIdList，tag=2
// 为手工追加的 unpacked repeated int32）+ 折扣系数 Discount（自建服 1.0，不折扣）。
func (s *Server) onGetMarket(ch *channel, req *protocol.C2M_GetMarket) proto.Message {
	resp := &protocol.M2C_GetMarket{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	if tables == nil || tables.marketBase == nil {
		resp.Error, resp.Message = errBadParam, "配置未加载"
		return resp
	}
	base, err := proto.Marshal(&protocol.M2C_GetMarket{RpcId: req.RpcId, Discount: 1.0})
	if err != nil {
		resp.Error, resp.Message = errBadParam, "序列化失败"
		return resp
	}
	ids := make([]int, 0, len(tables.marketBase))
	for id := range tables.marketBase {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	for _, id := range ids {
		base = pbAppendVarint(base, 2, uint64(id))
	}
	s.sendRawPush(ch, protocol.OpM2C_GetMarket, base)
	log.Printf("[S=%d] get market rows=%d", ch.id, len(ids))
	return nil
}

// onBuyInMarket：20176 → 20177。按 Page+Slot 定位 MarketBase 行；
// 元宝市场按 Price_YuanBao（Discount=1.0）扣元宝，兑换券市场按 ×2 扣兑换券；
// OnlyYuanBao 商品禁止非元宝市场购买。成功回包携带背包全量 BagMapList。
func (s *Server) onBuyInMarket(ch *channel, req *protocol.C2M_BuyInMarket) proto.Message {
	ss := ch.session
	resp := &protocol.M2C_BuyInMarket{RpcId: req.RpcId}
	if ch == nil || ss == nil || ss.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	if tables == nil || tables.marketBase == nil {
		resp.Message = "配置未加载"
		return resp
	}
	if req.Count <= 0 {
		resp.Message = "数量无效"
		return resp
	}
	rows := rowsOfPage(tables.marketBase, req.PageIndex)
	if req.SlotIndex < 0 || int(req.SlotIndex) >= len(rows) {
		resp.Message = "配置不存在"
		return resp
	}
	row := rows[req.SlotIndex]
	basePrice := num(row["Price_YuanBao"])
	var price int64
	switch req.Type {
	case protocol.MarketType_YuanBaoMarket:
		price = basePrice // 折扣 1.0
	case protocol.MarketType_VoucherMarket:
		price = basePrice * 2
	default:
		price = basePrice
	}
	onlyYB := false
	if b, ok := row["OnlyYuanBao"].(bool); ok {
		onlyYB = b
	}
	if onlyYB && req.Type != protocol.MarketType_YuanBaoMarket {
		resp.Message = "该物品只能用元宝购买"
		return resp
	}
	total, ok := safePriceTotal(price, req.Count)
	if !ok {
		resp.Message = "价格无效"
		return resp
	}
	spent := false
	if req.Type == protocol.MarketType_YuanBaoMarket {
		spent = s.spendYuanBao(ch, total)
	} else {
		spent = s.spendVoucher(ch, total)
	}
	if !spent {
		resp.Message = "货币不足"
		return resp
	}
	idx := ss.addItemToBag(int32(num(row["ItemId"])), req.Count)
	if idx < 0 {
		// 背包已满：退还已扣货币
		if req.Type == protocol.MarketType_YuanBaoMarket {
			s.addYuanBao(ch, total)
		} else {
			ss.voucher += total
			s.pushMoney(ch)
		}
		resp.Message = "背包已满"
		return resp
	}
	base, err := proto.Marshal(resp)
	if err != nil {
		resp.Message = "序列化失败"
		return resp
	}
	base = encodeBagMapList(base, 1, bagPairs(ss.bag))
	s.sendRawPush(ch, protocol.OpM2C_BuyInMarket, base)
	s.saveData(ch)
	log.Printf("[S=%d] buy in market page=%d slot=%d item=%d count=%d total=%d type=%d",
		ch.id, req.PageIndex, req.SlotIndex, int32(num(row["ItemId"])), req.Count, total, req.Type)
	return nil
}

// ===================== 普通商店 Shop（20174/20175） =====================

// onBuyInShop：20174 → 20175。按 Page+Slot 定位 ShopBase 行，Price 为铜币价；
// 扣铜币 → 物品入包 → 回包携带背包全量 BagMapList。
func (s *Server) onBuyInShop(ch *channel, req *protocol.C2M_BuyInShop) proto.Message {
	ss := ch.session
	resp := &protocol.M2C_BuyInShop{RpcId: req.RpcId}
	if ch == nil || ss == nil || ss.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	if tables == nil || tables.shopBase == nil {
		resp.Message = "配置未加载"
		return resp
	}
	if req.Count <= 0 {
		resp.Message = "数量无效"
		return resp
	}
	rows := rowsOfPage(tables.shopBase, req.PageIndex)
	if req.SlotIndex < 0 || int(req.SlotIndex) >= len(rows) {
		resp.Message = "配置不存在"
		return resp
	}
	row := rows[req.SlotIndex]
	total, ok := safePriceTotal(num(row["Price"]), req.Count)
	if !ok {
		resp.Message = "价格无效"
		return resp
	}
	if !s.spendCoin(ch, total) {
		resp.Message = "铜币不足"
		return resp
	}
	idx := ss.addItemToBag(int32(num(row["ItemId"])), req.Count)
	if idx < 0 {
		s.addCoin(ch, total) // 背包已满：退还铜币
		resp.Message = "背包已满"
		return resp
	}
	base, err := proto.Marshal(resp)
	if err != nil {
		resp.Message = "序列化失败"
		return resp
	}
	base = encodeBagMapList(base, 1, bagPairs(ss.bag))
	s.sendRawPush(ch, protocol.OpM2C_BuyInShop, base)
	s.saveData(ch)
	log.Printf("[S=%d] buy in shop page=%d slot=%d item=%d count=%d total=%d",
		ch.id, req.PageIndex, req.SlotIndex, int32(num(row["ItemId"])), req.Count, total)
	return nil
}

// ===================== 卖出（20178/20179） =====================

// onSellItem：20178 → 20179。按 GoodsBase.Price × Count 结算铜币（缺表按 10）；
// 装备（ItemType=1）不可出售；成功回包携带背包全量 BagMapList。
func (s *Server) onSellItem(ch *channel, req *protocol.C2M_SellItem) proto.Message {
	ss := ch.session
	resp := &protocol.M2C_SellItem{RpcId: req.RpcId}
	if ch == nil || ss == nil || ss.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	it, ok := ss.bag[req.SlotIndex]
	if !ok || it == nil {
		resp.Message = "物品不存在"
		return resp
	}
	if it.ItemType == 1 {
		resp.Message = "该物品不能出售"
		return resp
	}
	if req.Count <= 0 || req.Count > it.Count {
		resp.Message = "数量无效"
		return resp
	}
	price := int64(10)
	if tables != nil {
		if gb, ok := tables.goodsBase[int64(it.ItemId)]; ok {
			if p := num(gb["Price"]); p > 0 {
				price = p
			}
		}
	}
	gain, ok := safePriceTotal(price, req.Count)
	if !ok {
		resp.Message = "价格无效"
		return resp
	}
	if !ss.removeBagCount(req.SlotIndex, req.Count) {
		resp.Message = "数量无效"
		return resp
	}
	s.addCoin(ch, gain)
	base, err := proto.Marshal(resp)
	if err != nil {
		resp.Message = "序列化失败"
		return resp
	}
	base = encodeBagMapList(base, 1, bagPairs(ss.bag))
	s.sendRawPush(ch, protocol.OpM2C_SellItem, base)
	s.saveData(ch)
	log.Printf("[S=%d] sell item=%d count=%d gain=%d", ch.id, it.ItemId, req.Count, gain)
	return nil
}

// ===================== 多商店 MultiShop（20417/20418） =====================

// onBuyInMultiShop：20417 → 20418。按 Type+Index 定位 MultiShop 行；
// Type==1 用元宝，其余用铜币；成功回包携带背包全量 BagMapList。
func (s *Server) onBuyInMultiShop(ch *channel, req *protocol.C2M_BuyInMultiShop) proto.Message {
	ss := ch.session
	resp := &protocol.M2C_BuyInMultiShop{RpcId: req.RpcId}
	if ch == nil || ss == nil || ss.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	if tables == nil || tables.multiShop == nil {
		resp.Message = "配置未加载"
		return resp
	}
	if req.Count <= 0 {
		resp.Message = "数量无效"
		return resp
	}
	rows := rowsOfType(tables.multiShop, req.ShopType)
	if req.Index < 0 || int(req.Index) >= len(rows) {
		resp.Message = "配置不存在"
		return resp
	}
	row := rows[req.Index]
	total, ok := safePriceTotal(num(row["Price"]), req.Count)
	if !ok {
		resp.Message = "价格无效"
		return resp
	}
	paid := false
	if req.ShopType == 1 {
		paid = s.spendYuanBao(ch, total)
	} else {
		paid = s.spendCoin(ch, total)
	}
	if !paid {
		resp.Message = "货币不足"
		return resp
	}
	idx := ss.addItemToBag(int32(num(row["ItemId"])), req.Count)
	if idx < 0 {
		// 背包已满：退还已扣货币
		if req.ShopType == 1 {
			s.addYuanBao(ch, total)
		} else {
			s.addCoin(ch, total)
		}
		resp.Message = "背包已满"
		return resp
	}
	base, err := proto.Marshal(resp)
	if err != nil {
		resp.Message = "序列化失败"
		return resp
	}
	base = encodeBagMapList(base, 1, bagPairs(ss.bag))
	s.sendRawPush(ch, protocol.OpM2C_BuyInMultiShop, base)
	s.saveData(ch)
	log.Printf("[S=%d] buy in multishop type=%d index=%d item=%d count=%d total=%d",
		ch.id, req.ShopType, req.Index, int32(num(row["ItemId"])), req.Count, total)
	return nil
}

// ===================== 仓库 Store（20188-20203） =====================
// session.store：仓库格子（Index 0..N-1）；session.storeCoin：仓库铜币。
// 所有 StoreList 均为仓库全量快照（客户端按 Index 覆盖刷新）。

// 客户端 StoreUI.ShowSlot 以 currPage*60 作为每页首格，构造器默认 2 页。
const (
	storeSlotsPerPage int32 = 60
	defaultStorePages int32 = 2
	maxStorePages     int32 = 10
	storeExpandCoin   int64 = 100000
)

func storePageCount(ss *session) int32 {
	if ss == nil || ss.storePages < 1 {
		return defaultStorePages
	}
	return ss.storePages
}

func storeCapacityFor(ss *session) int32 {
	return storePageCount(ss) * storeSlotsPerPage
}

func validStorePage(ss *session, page int32) bool {
	return page >= 0 && page < storePageCount(ss)
}

func storePageRange(page int32) (int32, int32) {
	start := page * storeSlotsPerPage
	return start, start + storeSlotsPerPage
}

func storeIndexOnPage(index, page int32) bool {
	start, end := storePageRange(page)
	return index >= start && index < end
}

func storeStackLimit(it *bagItem) int32 {
	if it == nil || it.ItemType == int32(protocol.ItemType_EquipItem) {
		return 1
	}
	if tables != nil {
		for _, table := range []map[int64]map[string]interface{}{tables.goodsBase, tables.materialBase} {
			if row, ok := table[int64(it.ItemId)]; ok {
				if max := int32(num(row["MaxAmount"])); max > 0 {
					return max
				}
			}
		}
	}
	return 999
}

// storePutItem 把 count 个 it 放入指定仓库页：非装备（可堆叠）先并入同 ItemId 的存量格，
// 剩余部分再找该页首个空位。返回 false 表示当前页已满。
// 注意：失败时可能已部分写入存量格，调用方需用快照回滚保证原子性。
func storePutItem(ss *session, it *bagItem, count, page int32) bool {
	if ss == nil || ss.store == nil || it == nil || count <= 0 {
		return false
	}
	remaining := count
	start, end := storePageRange(page)
	stackLimit := storeStackLimit(it)
	if it.ItemType != 1 {
		for i := start; i < end; i++ {
			old, ok := ss.store[i]
			if !ok || old == nil || old.ItemId != it.ItemId || old.ItemType != it.ItemType {
				continue
			}
			if old.Count >= stackLimit {
				continue
			}
			add := remaining
			if old.Count+add > stackLimit {
				add = stackLimit - old.Count
			}
			old.Count += add
			remaining -= add
			if remaining <= 0 {
				return true
			}
		}
	}
	for i := start; i < end && remaining > 0; i++ {
		if old, ok := ss.store[i]; ok && old != nil {
			continue
		}
		add := remaining
		if add > stackLimit {
			add = stackLimit
		}
		item := cloneBagItem(it)
		item.Count = add
		ss.store[i] = item
		remaining -= add
	}
	return remaining == 0
}

// removeStoreCount 从仓库格扣减数量；扣完删格。成功返回 true。
func removeStoreCount(ss *session, index int32, count int32) bool {
	it, ok := ss.store[index]
	if !ok || it == nil || count <= 0 || it.Count < count {
		return false
	}
	it.Count -= count
	if it.Count <= 0 {
		delete(ss.store, index)
	}
	return true
}

// snapshotStore / snapshotBag：深拷贝容器，供入库/出库失败时回滚（保证扣包与入仓原子）。
func snapshotStore(ss *session) map[int32]*bagItem {
	m := make(map[int32]*bagItem, len(ss.store))
	for k, v := range ss.store {
		m[k] = cloneBagItem(v)
	}
	return m
}

func snapshotBag(ss *session) map[int32]*bagItem {
	m := make(map[int32]*bagItem, len(ss.bag))
	for k, v := range ss.bag {
		m[k] = cloneBagItem(v)
	}
	return m
}

// restoreBagItem 把扣减失败的 count 个物品放回背包原格（格被删则重建）。
func restoreBagItem(ss *session, index int32, it *bagItem, count int32) {
	if old, ok := ss.bag[index]; ok && old != nil {
		old.Count += count
		return
	}
	item := *it
	item.Count = count
	ss.bag[index] = &item
}

// restoreStoreItem 把扣减失败的 count 个物品放回仓库原格。
func restoreStoreItem(ss *session, index int32, it *bagItem, count int32) {
	if old, ok := ss.store[index]; ok && old != nil {
		old.Count += count
		return
	}
	item := *it
	item.Count = count
	ss.store[index] = &item
}

// onGetStore：20188 → 20189。返回仓库 StoreList + 库内铜币 + 总页数。
func sortStore(ss *session, page int32) {
	if ss == nil || len(ss.store) == 0 {
		return
	}
	start, end := storePageRange(page)
	items := make([]*bagItem, 0, storeSlotsPerPage)
	for index, it := range ss.store {
		if index < start || index >= end {
			continue
		}
		if it != nil && it.Count > 0 {
			items = append(items, cloneBagItem(it))
		}
		delete(ss.store, index)
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.ItemType != b.ItemType {
			return a.ItemType < b.ItemType
		}
		if a.ItemId != b.ItemId {
			return a.ItemId < b.ItemId
		}
		if a.Quality != b.Quality {
			return a.Quality > b.Quality
		}
		if a.Level != b.Level {
			return a.Level > b.Level
		}
		if a.Star != b.Star {
			return a.Star > b.Star
		}
		return a.ServerId < b.ServerId
	})
	for i, it := range items {
		ss.store[start+int32(i)] = it
	}
}

func (s *Server) onGetStore(ch *channel, req *protocol.C2M_GetStore) proto.Message {
	ss := ch.session
	resp := &protocol.M2C_GetStore{RpcId: req.RpcId}
	if ch == nil || ss == nil || ss.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	pages := storePageCount(ss)
	if !validStorePage(ss, req.Page) {
		resp.Message = "仓库页不存在"
		return resp
	}
	base, err := proto.Marshal(&protocol.M2C_GetStore{RpcId: req.RpcId, Coin: ss.storeCoin, Total: pages})
	if err != nil {
		resp.Error, resp.Message = errBadParam, "序列化失败"
		return resp
	}
	base = encodeBagMapList(base, 1, bagPairs(ss.store))
	s.sendRawPush(ch, protocol.OpM2C_GetStore, base)
	log.Printf("[S=%d] get store page=%d/%d items=%d coin=%d", ch.id, req.Page, pages, len(ss.store), ss.storeCoin)
	return nil
}

// onSortStore：20190 → 20191。只整理请求页，回包仓库全量 StoreList。
func (s *Server) onSortStore(ch *channel, req *protocol.C2M_SortStore) proto.Message {
	ss := ch.session
	resp := &protocol.M2C_SortStore{RpcId: req.RpcId}
	if ch == nil || ss == nil || ss.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	if !validStorePage(ss, req.Page) {
		resp.Message = "仓库页不存在"
		return resp
	}
	sortStore(ss, req.Page)
	base, err := proto.Marshal(resp)
	if err != nil {
		resp.Error, resp.Message = errBadParam, "序列化失败"
		return resp
	}
	base = encodeBagMapList(base, 1, bagPairs(ss.store))
	s.sendRawPush(ch, protocol.OpM2C_SortStore, base)
	s.saveData(ch)
	log.Printf("[S=%d] sort store items=%d", ch.id, len(ss.store))
	return nil
}

// onPutInStore：20192 → 20193。背包 → 请求页（同 ItemId 堆叠，否则用该页首个空位）；
// 仓库满则回滚并提示。回包顺序：StoreList(tag=1) 后 BagMapList(tag=2)。
func (s *Server) onPutInStore(ch *channel, req *protocol.C2M_PutInStore) proto.Message {
	ss := ch.session
	resp := &protocol.M2C_PutInStore{RpcId: req.RpcId}
	if ch == nil || ss == nil || ss.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	if !validStorePage(ss, req.Page) {
		resp.Message = "仓库页不存在"
		return resp
	}
	it, ok := ss.bag[req.BagIndex]
	if !ok || it == nil {
		resp.Message = "物品不存在"
		return resp
	}
	if req.Count <= 0 || req.Count > it.Count {
		resp.Message = "数量无效"
		return resp
	}
	itemSnapshot := cloneBagItem(it)
	if !ss.removeBagCount(req.BagIndex, req.Count) {
		resp.Message = "数量无效"
		return resp
	}
	backup := snapshotStore(ss)
	if !storePutItem(ss, itemSnapshot, req.Count, req.Page) {
		ss.store = backup // 回滚仓库（含已部分写入的存量格）
		restoreBagItem(ss, req.BagIndex, itemSnapshot, req.Count)
		resp.Message = "当前仓库页已满"
		return resp
	}
	base, err := proto.Marshal(resp)
	if err != nil {
		resp.Message = "序列化失败"
		return resp
	}
	base = encodeBagMapList(base, 1, bagPairs(ss.store)) // StoreList
	base = encodeBagMapList(base, 2, bagPairs(ss.bag))   // BagMapList
	s.sendRawPush(ch, protocol.OpM2C_PutInStore, base)
	s.saveData(ch)
	log.Printf("[S=%d] put in store bag=%d item=%d count=%d", ch.id, req.BagIndex, itemSnapshot.ItemId, req.Count)
	return nil
}

// onTakeOffStore：20194 → 20195。仓库 → 背包（对称于 onPutInStore）；
// 背包满则回滚并提示。回包顺序：BagMapList(tag=1) 后 StoreList(tag=2)。
func (s *Server) onTakeOffStore(ch *channel, req *protocol.C2M_TakeOffStore) proto.Message {
	ss := ch.session
	resp := &protocol.M2C_TakeOffStore{RpcId: req.RpcId}
	if ch == nil || ss == nil || ss.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	if !validStorePage(ss, req.Page) || req.StoreIndex < 0 || req.StoreIndex >= storeCapacityFor(ss) || !storeIndexOnPage(req.StoreIndex, req.Page) {
		resp.Message = "仓库位置无效"
		return resp
	}
	it, ok := ss.store[req.StoreIndex]
	if !ok || it == nil {
		resp.Message = "物品不存在"
		return resp
	}
	if req.Count <= 0 || req.Count > it.Count {
		resp.Message = "数量无效"
		return resp
	}
	itemSnapshot := cloneBagItem(it)
	if !removeStoreCount(ss, req.StoreIndex, req.Count) {
		resp.Message = "数量无效"
		return resp
	}
	bagBackup := snapshotBag(ss)
	idx := invalidBagSlot
	if itemSnapshot.ItemType == int32(protocol.ItemType_EquipItem) {
		idx = nextBagIndex(ss)
		if idx >= 0 {
			item := cloneBagItem(itemSnapshot)
			item.Count = req.Count
			ss.bag[idx] = item
		}
	} else {
		idx = ss.addItemToBag(itemSnapshot.ItemId, req.Count)
	}
	if idx < 0 {
		ss.bag = bagBackup // 回滚背包（addItemToBag 可能已部分堆叠）
		restoreStoreItem(ss, req.StoreIndex, itemSnapshot, req.Count)
		resp.Message = "背包已满"
		return resp
	}
	base, err := proto.Marshal(resp)
	if err != nil {
		resp.Message = "序列化失败"
		return resp
	}
	base = encodeBagMapList(base, 1, bagPairs(ss.bag))   // BagMapList
	base = encodeBagMapList(base, 2, bagPairs(ss.store)) // StoreList
	s.sendRawPush(ch, protocol.OpM2C_TakeOffStore, base)
	s.saveData(ch)
	log.Printf("[S=%d] take off store idx=%d item=%d count=%d -> bag[%d]",
		ch.id, req.StoreIndex, itemSnapshot.ItemId, req.Count, idx)
	return nil
}

// onPutCoinInStore：20196 → 20197。背包铜币存入仓库，回包库内铜币总额。
func (s *Server) onPutCoinInStore(ch *channel, req *protocol.C2M_PutCoinInStore) proto.Message {
	ss := ch.session
	resp := &protocol.M2C_PutCoinInStore{RpcId: req.RpcId}
	if ch == nil || ss == nil || ss.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	amount := req.Coin
	if amount <= 0 {
		resp.Message = "数量无效"
		return resp
	}
	if ss.coin < amount {
		resp.Message = "铜币不足"
		return resp
	}
	ss.coin -= amount
	ss.storeCoin += amount
	s.pushMoney(ch)
	resp.Coin = ss.storeCoin
	s.saveData(ch)
	log.Printf("[S=%d] put coin in store amount=%d storeCoin=%d", ch.id, amount, ss.storeCoin)
	return resp
}

// onTakeCoinOutStore：20198 → 20199。仓库铜币取回背包，回包库内铜币总额。
func (s *Server) onTakeCoinOutStore(ch *channel, req *protocol.C2M_TakeCoinOutStore) proto.Message {
	ss := ch.session
	resp := &protocol.M2C_TakeCoinOutStore{RpcId: req.RpcId}
	if ch == nil || ss == nil || ss.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	amount := req.Coin
	if amount <= 0 {
		resp.Message = "数量无效"
		return resp
	}
	if ss.storeCoin < amount {
		resp.Message = "仓库铜币不足"
		return resp
	}
	ss.storeCoin -= amount
	ss.coin += amount
	s.pushMoney(ch)
	resp.Coin = ss.storeCoin
	s.saveData(ch)
	log.Printf("[S=%d] take coin out store amount=%d storeCoin=%d", ch.id, amount, ss.storeCoin)
	return resp
}

// onQueryExtandPrice：20200 → 20201。仓库扩容费用查询（自建服固定 100000 铜）。
func (s *Server) onQueryExtandPrice(ch *channel, req *protocol.C2M_QueryExtandPrice) proto.Message {
	resp := &protocol.M2C_QueryExtandPrice{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	resp.Coin = storeExpandCoin
	resp.Voucher = 0
	return resp
}

// onExtandStore：20202 → 20203。仓库扩容（自建服暂为空操作，直接成功）。
func (s *Server) onExtandStore(ch *channel, req *protocol.C2M_ExtandStore) proto.Message {
	resp := &protocol.M2C_ExtandStore{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	ss := ch.session
	if ss.storePages < defaultStorePages {
		ss.storePages = defaultStorePages
	}
	if ss.storePages >= maxStorePages {
		resp.Message = "仓库已达最大容量"
		return resp
	}
	if !s.spendCoin(ch, storeExpandCoin) {
		resp.Message = "铜币不足"
		return resp
	}
	ss.storePages++
	s.saveData(ch)
	log.Printf("[S=%d] expand store pages=%d capacity=%d", ch.id, ss.storePages, storeCapacityFor(ss))
	return resp
}

// ===================== 寄售行 Consignment（20181-20186） =====================
// 完整实现见 consignment.go（上架/列表/购买/过期退款）。

// ===================== 远程商店 RemoteStore（20294-20297） =====================

// onGetOpenRemoteStore：20294 → 20295。返回开远程商店价格（自建服固定 1000 铜）。
func (s *Server) onGetOpenRemoteStore(ch *channel, req *protocol.C2M_GetOpenRemoteStore) proto.Message {
	resp := &protocol.M2C_GetOpenRemoteStore{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	resp.Price = 1000
	return resp
}

// onOpenRemoteStore：20296 → 20297。扣 1000 铜开通远程商店（开店推送由 NPC 点击链路触发）。
func (s *Server) onOpenRemoteStore(ch *channel, req *protocol.C2M_OpenRemoteStore) proto.Message {
	resp := &protocol.M2C_OpenRemoteStore{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	if !s.spendCoin(ch, 1000) {
		resp.Message = "铜币不足"
		return resp
	}
	s.saveData(ch)
	log.Printf("[S=%d] open remote store (1000 coin)", ch.id)
	return resp
}

// ===================== UI 打开推送（NPC 点击链路复用） =====================

// pushOpenShopUI 推送 M2C_OpenShopUI(20209) 打开普通商店界面。
func (s *Server) pushOpenShopUI(ch *channel) {
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		return
	}
	s.sendPush(ch, protocol.OpM2C_OpenShopUI, &protocol.M2C_OpenShopUI{ActorId: ch.session.playerID})
}

// pushOpenStoreUI 推送 M2C_OpenStoreUI(20187) 打开仓库（管家）界面。
func (s *Server) pushOpenStoreUI(ch *channel) {
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		return
	}
	s.sendPush(ch, protocol.OpM2C_OpenStoreUI, &protocol.M2C_OpenStoreUI{ActorId: ch.session.playerID})
}

// pushOpenConsignmentUI 推送 M2C_OpenConsignmentUI(20210) 打开寄售行界面。
// 客户端不会在打开后主动请求 20181，因此这里必须携带筛选为“全部”的第 0 页。
func (s *Server) pushOpenConsignmentUI(ch *channel) {
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		return
	}
	items := s.consignmentSnapshot(protocol.JobType_UnKnown, protocol.ItemType_NoneItem)
	pages := int32((len(items) + consignPageSize - 1) / consignPageSize)
	if pages < 1 {
		pages = 1
	}
	base, err := proto.Marshal(&protocol.M2C_OpenConsignmentUI{
		RpcId:     0,
		TotalPage: pages,
		ActorId:   ch.session.playerID,
	})
	if err != nil {
		log.Printf("[S=%d] push open consignment ui marshal err=%v", ch.id, err)
		return
	}
	base, _ = appendConsignmentPage(base, items, 0)
	s.sendRawPush(ch, protocol.OpM2C_OpenConsignmentUI, base)
}
