package main

import (
	"hash/fnv"
	"log"
	"math"
	"sort"
	"strconv"
	"time"

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
// 货币：金币 Coin=1028（普通 Shop）；元宝=1024、代金券=1025（Market）；
// MultiShop Type1=星币 110205/1041，Type2=荣誉 1030，Type3=竞技币 1040，
// Type4=家族贡献 1037。扣款走 state_extra.go 的 spend* 助手并同步余额。

// ===================== 公共辅助 =====================

// rowsOfPage 过滤表中 Page==pageIndex 的行，按 _id 升序返回（页内 SlotIndex 以此为序）。
func rowsOfPage(tbl map[int64]map[string]interface{}, page int32) []map[string]interface{} {
	var rows []map[string]interface{}
	for _, row := range tbl {
		if !shopRowEnabled(row) {
			continue
		}
		if int32(num(row["Page"])) != page {
			continue
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return num(rows[i]["_id"]) < num(rows[j]["_id"]) })
	return rows
}

func rawRowsOfPage(tbl map[int64]map[string]interface{}, page int32) []map[string]interface{} {
	var rows []map[string]interface{}
	for _, row := range tbl {
		if int32(num(row["Page"])) == page {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return num(rows[i]["_id"]) < num(rows[j]["_id"]) })
	return rows
}

// rowsOfType 过滤多商店表中 Type==shopType 的行，按 _id 升序返回。
func rowsOfType(tbl map[int64]map[string]interface{}, shopType int32) []map[string]interface{} {
	var rows []map[string]interface{}
	for _, row := range tbl {
		if !shopRowEnabled(row) {
			continue
		}
		if int32(num(row["Type"])) != shopType {
			continue
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return num(rows[i]["_id"]) < num(rows[j]["_id"]) })
	return rows
}

func rawRowsOfType(tbl map[int64]map[string]interface{}, shopType int32) []map[string]interface{} {
	var rows []map[string]interface{}
	for _, row := range tbl {
		if int32(num(row["Type"])) == shopType {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return num(rows[i]["_id"]) < num(rows[j]["_id"]) })
	return rows
}

// shopRowEnabled controls whether a row is visible and purchasable.  The
// field is intentionally optional so existing YAML rows remain on sale; set
// Enabled: false to hide a row immediately after publishing the YAML.
func shopRowEnabled(row map[string]interface{}) bool {
	if row == nil {
		return false
	}
	if _, ok := row["Enabled"]; !ok {
		return true
	}
	return boolOf(row["Enabled"])
}

const (
	shopCatalogMarket int32 = 1
	shopCatalogShop   int32 = 2
	shopCatalogMulti  int32 = 3
)

const (
	saleDestinationShop   int32 = 1
	saleDestinationMarket int32 = 2
)

type shopCatalogSlot struct {
	storeType  int32
	pageOrType int32
	slot       int32
}

func (ss *session) rememberShopCatalog(items []*protocol.ShopItemPrice) {
	if ss == nil {
		return
	}
	index := make(map[shopCatalogSlot]int32, len(items))
	for _, item := range items {
		if item == nil || item.ConfigId <= 0 || item.SlotIndex < 0 {
			continue
		}
		index[shopCatalogSlot{item.StoreType, item.PageOrType, item.SlotIndex}] = item.ConfigId
	}
	ss.shopCatalogMu.Lock()
	ss.shopCatalog = index
	ss.shopCatalogMu.Unlock()
}

func (ss *session) mergeShopCatalog(items []*protocol.ShopItemPrice) {
	if ss == nil {
		return
	}
	ss.shopCatalogMu.Lock()
	if ss.shopCatalog == nil {
		ss.shopCatalog = make(map[shopCatalogSlot]int32)
	}
	for _, item := range items {
		if item == nil || item.ConfigId <= 0 || item.SlotIndex < 0 {
			continue
		}
		ss.shopCatalog[shopCatalogSlot{item.StoreType, item.PageOrType, item.SlotIndex}] = item.ConfigId
	}
	ss.shopCatalogMu.Unlock()
}

func (ss *session) catalogConfigID(storeType, pageOrType, slot int32) (int32, bool) {
	if ss == nil {
		return 0, false
	}
	ss.shopCatalogMu.RLock()
	id, ok := ss.shopCatalog[shopCatalogSlot{storeType, pageOrType, slot}]
	ss.shopCatalogMu.RUnlock()
	return id, ok
}

func rowByConfigID(tbl map[int64]map[string]interface{}, configID int32) map[string]interface{} {
	if configID <= 0 {
		return nil
	}
	row := tbl[int64(configID)]
	if !shopRowEnabled(row) {
		return nil
	}
	return row
}

// buildShopCatalog creates the authoritative directory consumed by patched
// clients. SlotIndex follows the active rows after filtering disabled entries,
// matching the legacy ShopUI which skips hidden rows and numbers the remaining
// visible items consecutively.
func buildShopCatalog(ss *session) []*protocol.ShopItemPrice {
	if tables == nil {
		return nil
	}
	out := make([]*protocol.ShopItemPrice, 0)
	discount := marketDiscountForSession(ss, time.Now())
	pages := make(map[int32]struct{})
	for _, row := range tables.marketBase {
		if shopRowEnabled(row) {
			pages[int32(num(row["Page"]))] = struct{}{}
		}
	}
	for _, row := range tables.shopBase {
		if shopRowEnabled(row) {
			pages[int32(num(row["Page"]))] = struct{}{}
		}
	}
	pageList := make([]int32, 0, len(pages))
	for page := range pages {
		pageList = append(pageList, page)
	}
	sort.Slice(pageList, func(i, j int) bool { return pageList[i] < pageList[j] })
	for _, page := range pageList {
		for slot, row := range rowsOfPage(tables.marketBase, page) {
			id := int32(num(row["_id"]))
			itemID := int32(num(row["ItemId"]))
			if id <= 0 || itemID <= 0 {
				continue
			}
			out = append(out, &protocol.ShopItemPrice{
				StoreType: shopCatalogMarket, ConfigId: id, PageOrType: page,
				SlotIndex: int32(slot), ItemId: itemID,
				Price:        marketYuanBaoPrice(row, discount),
				VoucherPrice: func() int64 { p, _ := marketVoucherPrice(row); return p }(),
				Enabled:      true, Description: itemDescription(itemID),
			})
		}
		for slot, row := range rowsOfPage(tables.shopBase, page) {
			id := int32(num(row["_id"]))
			itemID := int32(num(row["ItemId"]))
			if id <= 0 || itemID <= 0 {
				continue
			}
			out = append(out, &protocol.ShopItemPrice{
				StoreType: shopCatalogShop, ConfigId: id, PageOrType: page,
				SlotIndex: int32(slot), ItemId: itemID, Price: num(row["Price"]), Enabled: true,
				Description: itemDescription(itemID),
			})
		}
	}
	for shopType := int32(1); shopType <= 16; shopType++ {
		for slot, row := range rowsOfType(tables.multiShop, shopType) {
			id := int32(num(row["_id"]))
			itemID := int32(num(row["ItemId"]))
			if id <= 0 || itemID <= 0 {
				continue
			}
			out = append(out, &protocol.ShopItemPrice{
				StoreType: shopCatalogMulti, ConfigId: id, PageOrType: shopType,
				SlotIndex: int32(slot), ItemId: itemID, Price: num(row["Price"]), Enabled: true,
				Description: itemDescription(itemID),
			})
		}
	}
	return out
}

// compactShopPriceData mirrors the seven-field wire contract used by the
// client compatibility patch.  Prices are intentionally narrowed to int32:
// the legacy client stores shop prices in Int32, while the authoritative
// ShopItemPrice message still carries int64 values for newer clients.
func compactShopPriceData(items []*protocol.ShopItemPrice) []int32 {
	if len(items) == 0 {
		return nil
	}
	out := make([]int32, 0, len(items)*7)
	for _, item := range items {
		if item == nil || !item.Enabled {
			continue
		}
		out = append(out,
			item.StoreType,
			item.PageOrType,
			item.SlotIndex,
			item.ConfigId,
			item.ItemId,
			int32(item.Price),
			int32(item.VoucherPrice),
		)
	}
	return out
}

// compactShopDescriptionData mirrors compactShopPriceData.  The patched
// client uses the same catalog order to associate each description with its
// ItemId; disabled rows are skipped in both arrays.
func compactShopDescriptionData(items []*protocol.ShopItemPrice) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if item == nil || !item.Enabled {
			continue
		}
		out = append(out, item.Description)
	}
	return out
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

func marketDiscount(accountID, playerID int64, now time.Time) float32 {
	minimumPercent, maximumPercent, location := gameplayMarketSettings()
	identity := accountID
	if identity <= 0 {
		identity = playerID
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(strconv.FormatInt(identity, 10)))
	_, _ = hash.Write([]byte("|"))
	_, _ = hash.Write([]byte(now.In(location).Format("20060102")))
	percent := minimumPercent + hash.Sum64()%(maximumPercent-minimumPercent+1)
	return float32(percent) / 100
}

func marketDiscountForSession(ss *session, now time.Time) float32 {
	if ss == nil {
		minimumPercent, _, _ := gameplayMarketSettings()
		return float32(minimumPercent) / 100
	}
	return marketDiscount(ss.accountID, ss.playerID, now)
}

func marketDiscountedPrice(basePrice int64, discount float32) int64 {
	if basePrice < 0 || discount < 0 {
		return -1
	}
	scaled := float64(float32(basePrice) * discount)
	if scaled > float64(math.MaxInt64) {
		return -1
	}
	return int64(math.RoundToEven(scaled))
}

// marketYuanBaoPrice resolves the yuanbao price shown and charged for one
// market item. Legacy rows keep the account/day discount; rows that explicitly
// opt out (such as the max-level strengthening coupon) use their fixed price.
func marketYuanBaoPrice(row map[string]interface{}, discount float32) int64 {
	if row == nil {
		return -1
	}
	basePrice := num(row["Price_YuanBao"])
	if boolOf(row["NoDiscount"]) {
		return basePrice
	}
	return marketDiscountedPrice(basePrice, discount)
}

// marketVoucherPrice resolves a voucher price. Price_Voucher is an optional
// extension used by new rows; old rows retain the online two-times-yuanbao
// compatibility rule.
func marketVoucherPrice(row map[string]interface{}) (int64, bool) {
	if row == nil {
		return 0, false
	}
	if explicit := num(row["Price_Voucher"]); explicit > 0 {
		return explicit, true
	}
	return safePriceTotal(num(row["Price_YuanBao"]), 2)
}

// ===================== 元宝市场 Market（20172/20173 + 20176/20177） =====================

// onGetMarket：20172 → 20173。一次性下发全部市场条目 _id（M2C_GetMarket.MarketIdList，tag=2
// 为手工追加的 unpacked repeated int32）+ 当天账号固定的 0.85~1.25 价格系数。
func (s *Server) onGetMarket(ch *channel, req *protocol.C2M_GetMarket) proto.Message {
	return s.sendMarketSnapshot(ch, req, true)
}

func (s *Server) sendMarketSnapshot(ch *channel, req *protocol.C2M_GetMarket, markDestination bool) proto.Message {
	resp := &protocol.M2C_GetMarket{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	if tables == nil || tables.marketBase == nil {
		resp.Error, resp.Message = errBadParam, "配置未加载"
		return resp
	}
	if markDestination {
		ch.session.saleDestination = saleDestinationMarket
	}
	discount := marketDiscountForSession(ch.session, time.Now())
	rows := make([]map[string]interface{}, 0, len(tables.marketBase))
	for _, row := range tables.marketBase {
		if shopRowEnabled(row) {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return num(rows[i]["_id"]) < num(rows[j]["_id"]) })
	prices := make([]*protocol.ShopItemPrice, 0, len(rows))
	priceList := make([]int32, 0, len(rows)*2)
	descriptionList := make([]string, 0, len(rows))
	pageSlots := make(map[int32]int32)
	for _, row := range rows {
		page := int32(num(row["Page"]))
		slot := pageSlots[page]
		pageSlots[page] = slot + 1
		voucher, _ := marketVoucherPrice(row)
		itemID := int32(num(row["ItemId"]))
		description := itemDescription(itemID)
		prices = append(prices, &protocol.ShopItemPrice{
			StoreType: shopCatalogMarket, ConfigId: int32(num(row["_id"])),
			PageOrType: page, SlotIndex: slot, ItemId: itemID,
			Price: marketYuanBaoPrice(row, discount), VoucherPrice: voucher, Enabled: true,
			Description: description,
		})
		descriptionList = append(descriptionList, description)
		// PriceList is the compact field consumed by the patched legacy
		// client.  Keep the exact same _id order as MarketIdList and store
		// yuanbao/voucher prices as alternating int32 values.
		priceList = append(priceList, int32(marketYuanBaoPrice(row, discount)), int32(voucher))
	}
	ch.session.mergeShopCatalog(prices)
	base, err := proto.Marshal(&protocol.M2C_GetMarket{RpcId: req.RpcId, Discount: discount, ItemPrices: prices, PriceList: priceList, DescriptionList: descriptionList})
	if err != nil {
		resp.Error, resp.Message = errBadParam, "序列化失败"
		return resp
	}
	ids := make([]int, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, int(num(row["_id"])))
	}
	sort.Ints(ids)
	for _, id := range ids {
		base = pbAppendVarint(base, 2, uint64(id))
	}
	s.sendRawPush(ch, protocol.OpM2C_GetMarket, base)
	log.Printf("[S=%d] get market rows=%d", ch.id, len(ids))
	return nil
}

// pushMarketSnapshot is used by the configuration reload notifier.  It sends
// the same wire shape as a normal C2M_GetMarket request, with RpcId=0 so the
// client treats it as a push and refreshes an open market immediately.
func (s *Server) pushMarketSnapshot(ch *channel) {
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		return
	}
	_ = s.sendMarketSnapshot(ch, &protocol.C2M_GetMarket{}, false)
}

// onBuyInMarket：20176 → 20177。按 Page+Slot 定位 MarketBase 行；
// 元宝市场按客户端展示的当天价格系数扣元宝，兑换券市场按 ×2 扣兑换券；
// OnlyYuanBao 商品禁止非元宝市场购买。成功回包携带背包全量 BagMapList。
func (s *Server) onBuyInMarket(ch *channel, req *protocol.C2M_BuyInMarket) proto.Message {
	resp := &protocol.M2C_BuyInMarket{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	ss := ch.session
	starBefore := starCoinBalance(ss)
	if tables == nil || tables.marketBase == nil {
		resp.Message = "配置未加载"
		return resp
	}
	if req.Count <= 0 {
		resp.Message = "数量无效"
		return resp
	}
	rows := rowsOfPage(tables.marketBase, req.PageIndex)
	var row map[string]interface{}
	if configID, ok := ss.catalogConfigID(shopCatalogMarket, req.PageIndex, req.SlotIndex); ok {
		row = rowByConfigID(tables.marketBase, configID)
		if row == nil {
			resp.Message = "商品已下架"
			return resp
		}
	} else {
		rawRows := rawRowsOfPage(tables.marketBase, req.PageIndex)
		if req.SlotIndex < 0 || int(req.SlotIndex) >= len(rawRows) {
			resp.Message = "配置不存在"
			return resp
		}
		if !shopRowEnabled(rawRows[req.SlotIndex]) {
			resp.Message = "商品已下架"
			return resp
		}
		if int(req.SlotIndex) >= len(rows) {
			resp.Message = "配置不存在"
			return resp
		}
		row = rows[req.SlotIndex]
	}
	var price int64
	switch req.Type {
	case protocol.MarketType_YuanBaoMarket:
		price = marketYuanBaoPrice(row, marketDiscountForSession(ss, time.Now()))
	case protocol.MarketType_VoucherMarket:
		var priceOK bool
		price, priceOK = marketVoucherPrice(row)
		if !priceOK {
			resp.Message = "浠锋牸鏃犳晥"
			return resp
		}
	default:
		// Never silently treat an unknown market type as a voucher purchase.
		// Doing so would debit the wrong balance when a forged/stale request
		// carries Type=NoneMarket or a future enum value.
		resp.Message = "市场类型无效"
		return resp
	}
	// Both market tabs are valid payment methods.  OnlyYuanBao is a client
	// presentation hint from the original data table, not an authorization
	// rule: voucher purchases use the voucher price while yuanbao purchases use
	// the daily discounted yuanbao price below.
	total, ok := safePriceTotal(price, req.Count)
	if !ok {
		resp.Message = "价格无效"
		return resp
	}
	// Stage the item before debiting any currency. A full-bag rejection is
	// therefore side-effect free and never needs a best-effort refund.
	purchaseCurrency := purchaseCurrencyVoucher
	if req.Type == protocol.MarketType_YuanBaoMarket {
		purchaseCurrency = purchaseCurrencyYuanBao
	}
	stagedBag, _, fits := stageBagGrants(ss, []bagGrant{{
		itemID: int32(num(row["ItemId"])), count: req.Count, source: "商城",
		purchaseSource: purchaseSourceMarket, purchaseCurrency: purchaseCurrency, purchaseUnitPrice: price,
	}})
	if !fits {
		resp.Message = "背包已满"
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
	ss.bag = stagedBag
	base, err := proto.Marshal(resp)
	if err != nil {
		resp.Message = "序列化失败"
		return resp
	}
	base = encodeBagMapList(base, 1, bagPairs(ss.bag))
	s.sendRawPush(ch, protocol.OpM2C_BuyInMarket, base)
	// Keep the response snapshot for the original handler and also send the
	// canonical bag push consumed by the client's UpdateBagUI handler.
	s.pushBagSnapshot(ch)
	if starCoinBalance(ss) != starBefore {
		s.pushMoney(ch)
	}
	s.saveData(ch)
	log.Printf("[S=%d] buy in market page=%d slot=%d item=%d count=%d total=%d type=%d",
		ch.id, req.PageIndex, req.SlotIndex, int32(num(row["ItemId"])), req.Count, total, req.Type)
	return nil
}

// ===================== 普通商店 Shop（20174/20175） =====================

// onBuyInShop：20174 → 20175。按 Page+Slot 定位 ShopBase 行，Price 为金币价；
// 扣金币（NumericType.Coin=1028）→ 物品入包 → 回包携带背包全量 BagMapList。
func (s *Server) onBuyInShop(ch *channel, req *protocol.C2M_BuyInShop) proto.Message {
	resp := &protocol.M2C_BuyInShop{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	ss := ch.session
	starBefore := starCoinBalance(ss)
	if tables == nil || tables.shopBase == nil {
		resp.Message = "配置未加载"
		return resp
	}
	if req.Count <= 0 {
		resp.Message = "数量无效"
		return resp
	}
	rows := rowsOfPage(tables.shopBase, req.PageIndex)
	var row map[string]interface{}
	if configID, ok := ss.catalogConfigID(shopCatalogShop, req.PageIndex, req.SlotIndex); ok {
		row = rowByConfigID(tables.shopBase, configID)
		if row == nil {
			resp.Message = "商品已下架"
			return resp
		}
	} else {
		rawRows := rawRowsOfPage(tables.shopBase, req.PageIndex)
		if req.SlotIndex < 0 || int(req.SlotIndex) >= len(rawRows) {
			resp.Message = "配置不存在"
			return resp
		}
		if !shopRowEnabled(rawRows[req.SlotIndex]) {
			resp.Message = "商品已下架"
			return resp
		}
		if int(req.SlotIndex) >= len(rows) {
			resp.Message = "配置不存在"
			return resp
		}
		row = rows[req.SlotIndex]
	}
	total, ok := safePriceTotal(num(row["Price"]), req.Count)
	if !ok {
		resp.Message = "价格无效"
		return resp
	}
	unitPrice := num(row["Price"])
	stagedBag, _, fits := stageBagGrants(ss, []bagGrant{{
		itemID: int32(num(row["ItemId"])), count: req.Count, source: "普通商店",
		purchaseSource: purchaseSourceShop, purchaseCurrency: purchaseCurrencyCoin, purchaseUnitPrice: unitPrice,
	}})
	if !fits {
		resp.Message = "背包已满"
		return resp
	}
	if !s.spendCoin(ch, total) {
		resp.Message = "铜币不足"
		return resp
	}
	ss.bag = stagedBag
	base, err := proto.Marshal(resp)
	if err != nil {
		resp.Message = "序列化失败"
		return resp
	}
	base = encodeBagMapList(base, 1, bagPairs(ss.bag))
	s.sendRawPush(ch, protocol.OpM2C_BuyInShop, base)
	// M2C_BuyInShop carries a client-only field-level BagMapList that is not
	// represented in the generated Go type. The standard push is required by
	// clients that refresh through M2C_SendBag(20260).
	s.pushBagSnapshot(ch)
	if starCoinBalance(ss) != starBefore {
		s.pushMoney(ch)
	}
	s.saveData(ch)
	log.Printf("[S=%d] buy in shop page=%d slot=%d item=%d count=%d total=%d",
		ch.id, req.PageIndex, req.SlotIndex, int32(num(row["ItemId"])), req.Count, total)
	return nil
}

// ===================== 卖出（20178/20179） =====================

// onSellItem：20178 → 20179。商城元宝/代金券购入批次卖回商城时按实付价
// 折算为代金券；其他来源和出售入口按客户端物品价格结算铜币。
func (s *Server) onSellItem(ch *channel, req *protocol.C2M_SellItem) proto.Message {
	resp := &protocol.M2C_SellItem{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	ss := ch.session
	it, ok := ss.bag[req.SlotIndex]
	if !ok || it == nil {
		resp.Message = "物品不存在"
		return resp
	}
	if isProtectedCurrencyItem(it.ItemId) {
		resp.Message = "该物品不能出售"
		return resp
	}
	if req.Count <= 0 || req.Count > it.Count {
		resp.Message = "数量无效"
		return resp
	}
	delta, gain, premiumRecovery, ok := saleCurrencyDelta(ss, it, req.Count)
	if !ok {
		resp.Message = "价格无效"
		return resp
	}
	if !ss.canApplyCurrencyDelta(delta) {
		resp.Message = "货币已达上限"
		return resp
	}
	originalCount := it.Count
	if !ss.removeBagCount(req.SlotIndex, req.Count) {
		resp.Message = "数量无效"
		return resp
	}
	if !s.applyCurrencyDeltaNoPush(ch, delta) {
		it.Count = originalCount
		ss.bag[req.SlotIndex] = it
		return &protocol.M2C_SellItem{RpcId: req.RpcId, Message: "货币已达上限"}
	}
	base, err := proto.Marshal(resp)
	if err != nil {
		resp.Message = "序列化失败"
		return resp
	}
	base = encodeBagMapList(base, 1, bagPairs(ss.bag))
	s.sendRawPush(ch, protocol.OpM2C_SellItem, base)
	// Some client builds only refresh the visible inventory from the generic
	// M2C_SendBag push (the field-level BagMapList on M2C_SellItem is not
	// present in their generated response type).  Send the same full snapshot
	// after the awaited sell response so every successful sale immediately
	// removes/decrements the sold stack.  Keep this before the currency sync so
	// a second rapid sale cannot make the first UI update appear to be skipped.
	s.pushBagSnapshot(ch)
	// Send the balance after the awaited sell response; otherwise the native
	// client can apply the next sale's update together with this one.
	s.pushMoney(ch)
	s.saveData(ch)
	currencyName := "coin"
	if premiumRecovery {
		currencyName = "voucher"
	}
	log.Printf("[S=%d] sell item=%d count=%d gain=%d currency=%s purchase_source=%d purchase_currency=%d unit_price=%d destination=%d",
		ch.id, it.ItemId, req.Count, gain, currencyName, it.PurchaseSource, it.PurchaseCurrency,
		it.PurchaseUnitPrice, ss.saleDestination)
	return nil
}

func saleCurrencyDelta(ss *session, item *bagItem, count int32) (currencyDelta, int64, bool, bool) {
	if item == nil || count <= 0 {
		return currencyDelta{}, 0, false, false
	}
	_, recoveryPercent := gameplayShopSaleSettings()
	if ss != nil && ss.saleDestination == saleDestinationMarket && item.PurchaseSource == purchaseSourceMarket &&
		(item.PurchaseCurrency == purchaseCurrencyYuanBao || item.PurchaseCurrency == purchaseCurrencyVoucher) &&
		item.PurchaseUnitPrice > 0 {
		total, ok := safePriceTotal(item.PurchaseUnitPrice, count)
		if !ok {
			return currencyDelta{}, 0, true, false
		}
		gain := total/100*recoveryPercent + total%100*recoveryPercent/100
		return currencyDelta{voucher: gain}, gain, true, gain > 0
	}
	price, ok := ordinarySaleUnitPrice(item)
	if !ok {
		return currencyDelta{}, 0, false, false
	}
	gain, ok := safePriceTotal(price, count)
	return currencyDelta{coin: gain}, gain, false, ok
}

func ordinarySaleUnitPrice(item *bagItem) (int64, bool) {
	if item == nil || item.ItemId <= 0 || item.ItemType < int32(protocol.ItemType_EquipItem) ||
		item.ItemType > int32(protocol.ItemType_MaterialsItem) {
		return 0, false
	}
	if enabled, configuredPrice, configured := gameplayShopSaleRule(item.ItemId, isManualEquipmentProduct(item.ItemId)); configured {
		return configuredPrice, enabled && configuredPrice > 0
	}
	fallbackPrice, _ := gameplayShopSaleSettings()
	if tables == nil {
		return fallbackPrice, fallbackPrice > 0
	}
	var row map[string]interface{}
	switch item.ItemType {
	case int32(protocol.ItemType_EquipItem):
		row = tables.equipBase[int64(item.ItemId)]
	default:
		row = tables.goodsBase[int64(item.ItemId)]
		if row == nil {
			row = tables.materialBase[int64(item.ItemId)]
		}
	}
	if price := num(row["Price"]); price > 0 {
		return price, true
	}
	return fallbackPrice, fallbackPrice > 0
}

// isManualEquipmentProduct identifies the product category from the
// authoritative ManulEquip table loaded from MySQL. ItemType cannot do this:
// crafted and ordinary equipment both use ItemType_EquipItem.
func isManualEquipmentProduct(itemID int32) bool {
	if tables == nil {
		return false
	}
	for _, row := range tables.manulEquip {
		if int32(num(row["EquipId"])) == itemID {
			return true
		}
	}
	return false
}

// isProtectedCurrencyItem covers all online currency/experience goods. The
// client marks these as bound or balance-like items; selling one as an
// ordinary GoodsBase item would convert a different currency into copper.
func isProtectedCurrencyItem(itemID int32) bool {
	switch itemID {
	case 110201, 110202, 110203, 110204, 110205, 110206:
		return true
	default:
		return false
	}
}

// ===================== 多商店 MultiShop（20417/20418） =====================

// onBuyInMultiShop：20417 → 20418。按 Type+Index 定位 MultiShop 行；
// Type1=星币，Type2=荣誉，Type3=竞技币，Type4=家族贡献；成功回包携带背包快照。
func (s *Server) onBuyInMultiShop(ch *channel, req *protocol.C2M_BuyInMultiShop) proto.Message {
	resp := &protocol.M2C_BuyInMultiShop{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	ss := ch.session
	if tables == nil || tables.multiShop == nil {
		resp.Message = "配置未加载"
		return resp
	}
	if req.Count <= 0 {
		resp.Message = "数量无效"
		return resp
	}
	rows := rowsOfType(tables.multiShop, req.ShopType)
	var row map[string]interface{}
	if configID, ok := ss.catalogConfigID(shopCatalogMulti, req.ShopType, req.Index); ok {
		row = rowByConfigID(tables.multiShop, configID)
		if row == nil {
			resp.Message = "商品已下架"
			return resp
		}
	} else {
		rawRows := rawRowsOfType(tables.multiShop, req.ShopType)
		if req.Index < 0 || int(req.Index) >= len(rawRows) {
			resp.Message = "配置不存在"
			return resp
		}
		if !shopRowEnabled(rawRows[req.Index]) {
			resp.Message = "商品已下架"
			return resp
		}
		if int(req.Index) >= len(rows) {
			resp.Message = "配置不存在"
			return resp
		}
		row = rows[req.Index]
	}
	total, ok := safePriceTotal(num(row["Price"]), req.Count)
	if !ok {
		resp.Message = "价格无效"
		return resp
	}
	beforeBag := cloneBagMap(ss.bag)
	starBefore := starCoinBalance(ss)
	beforeCoin, beforeYuanBao := ss.coin, ss.yuanBao
	beforeVoucher, beforeHonor := ss.voucher, ss.honor
	beforePVP, beforeFamily := ss.pvpCurrency, ss.familyContribute
	paid := false
	switch req.ShopType {
	case 1:
		paid = s.spendStarCoin(ch, total)
	case 2:
		paid = s.spendHonor(ch, total)
	case 3:
		paid = s.spendPVPCurrency(ch, total)
	case 4:
		paid = s.spendFamilyContribution(ch, total)
	default:
		resp.Message = "商店类型无效"
		return resp
	}
	if !paid {
		resp.Message = "货币不足"
		return resp
	}
	purchaseCurrency := map[int32]int32{
		1: purchaseCurrencyStarCoin, 2: purchaseCurrencyHonor,
		3: purchaseCurrencyPVP, 4: purchaseCurrencyFamilyContribution,
	}[req.ShopType]
	idx := ss.addItemToBagWithGrant(bagGrant{
		itemID: int32(num(row["ItemId"])), count: req.Count, source: "商城",
		purchaseSource: purchaseSourceMultiShop, purchaseCurrency: purchaseCurrency, purchaseUnitPrice: num(row["Price"]),
	})
	if idx < 0 {
		ss.bag = beforeBag
		ss.coin, ss.yuanBao = beforeCoin, beforeYuanBao
		ss.voucher, ss.honor = beforeVoucher, beforeHonor
		ss.pvpCurrency, ss.familyContribute = beforePVP, beforeFamily
		s.pushMoney(ch)
		s.pushBagSnapshot(ch)
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
	if starCoinBalance(ss) != starBefore {
		s.pushMoney(ch)
	}
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

// resolveStoreIndex accepts the canonical 0-based global index used by the
// current client.  A few legacy StoreUI builds sent the slot offset within
// the selected page instead; when the global slot is empty, resolve that
// legacy form to the occupied slot on the requested page.  This keeps normal
// page-one requests unambiguous while allowing old clients to take items out
// of page two instead of reporting "物品不存在".
func resolveStoreIndex(ss *session, index, page int32) (int32, bool) {
	if ss == nil || !validStorePage(ss, page) || index < 0 || index >= storeCapacityFor(ss) {
		return 0, false
	}
	if !storeIndexOnPage(index, page) {
		start, end := storePageRange(page)
		if page <= 0 || index >= storeSlotsPerPage || index+start < start || index+start >= end {
			return 0, false
		}
		if _, exists := ss.store[index+start]; exists {
			return index + start, true
		}
		return 0, false
	}
	if _, exists := ss.store[index]; exists {
		return index, true
	}
	// For page two and above, an occupied page-relative slot is a safe legacy
	// fallback only when the canonical global slot is empty.
	start, end := storePageRange(page)
	if page > 0 && index < storeSlotsPerPage && index+start >= start && index+start < end {
		if _, exists := ss.store[index+start]; exists {
			return index + start, true
		}
	}
	return index, false
}

func storeStackLimit(it *bagItem) int32 {
	if it == nil || it.ItemType == int32(protocol.ItemType_EquipItem) {
		return 1
	}
	if it.ItemId == 110344 {
		return 999
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
			if !ok || old == nil || old.ItemId != it.ItemId || old.ItemType != it.ItemType || !samePurchaseOrigin(old, it) {
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
	if handled, view := compatTradeSnapshotView(ch); handled {
		resp.Coin, resp.Total, resp.Message = view.selfCoin, 1, view.message
		if view.message != "" {
			return resp
		}
		base, err := proto.Marshal(resp)
		if err != nil {
			resp.Message = "交易报价序列化失败"
			return resp
		}
		base = appendCompatTradeView(base, view)
		s.sendRawPush(ch, protocol.OpM2C_GetStore, base)
		return nil
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
	if handled, response := s.compatTradeSort(ch, req); handled {
		return response
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
	if handled, response := s.compatTradePutItem(ch, req); handled {
		return response
	}
	if !validStorePage(ss, req.Page) {
		resp.Message = "仓库页不存在"
		return resp
	}
	// Count is optional on the wire.  Native single-item drag/double-click
	// paths in older StoreUI builds omitted it, which decodes as zero; those
	// paths mean exactly one item rather than an invalid quantity.
	if req.Count == 0 {
		req.Count = 1
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
	if handled, response := s.compatTradeTakeItem(ch, req); handled {
		return response
	}
	if req.Count == 0 {
		req.Count = 1
	}
	storeIndex, validIndex := resolveStoreIndex(ss, req.StoreIndex, req.Page)
	if !validIndex {
		resp.Message = "仓库位置无效"
		return resp
	}
	it, ok := ss.store[storeIndex]
	if !ok || it == nil {
		resp.Message = "物品不存在"
		return resp
	}
	if req.Count <= 0 || req.Count > it.Count {
		resp.Message = "数量无效"
		return resp
	}
	itemSnapshot := cloneBagItem(it)
	if !removeStoreCount(ss, storeIndex, req.Count) {
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
		idx = ss.addItemToBagWithGrant(bagGrant{
			itemID: itemSnapshot.ItemId, count: req.Count,
			purchaseSource: itemSnapshot.PurchaseSource, purchaseCurrency: itemSnapshot.PurchaseCurrency,
			purchaseUnitPrice: itemSnapshot.PurchaseUnitPrice,
		})
	}
	if idx < 0 {
		ss.bag = bagBackup // 回滚背包（addItemToBag 可能已部分堆叠）
		restoreStoreItem(ss, storeIndex, itemSnapshot, req.Count)
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
	log.Printf("[S=%d] take off store idx=%d (requested=%d page=%d) item=%d count=%d -> bag[%d]",
		ch.id, storeIndex, req.StoreIndex, req.Page, itemSnapshot.ItemId, req.Count, idx)
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
	if handled, coin, message := s.compatTradeChangeCoin(ch, req.RpcId, req.Coin, true); handled {
		resp.Coin, resp.Message = coin, message
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
	if !canAddInt64Balance(ss.storeCoin, amount) {
		resp.Message = "浠撳簱閾滃竵宸茶揪涓婇檺"
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
	if handled, coin, message := s.compatTradeChangeCoin(ch, req.RpcId, req.Coin, false); handled {
		resp.Coin, resp.Message = coin, message
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
	if !canAddInt64Balance(ss.coin, amount) {
		resp.Message = "鑳屽寘閾滃竵宸茶揪涓婇檺"
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
	if ch.session.tradeID != 0 || ch.session.tradeCompatClosed {
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
	if handled, response := s.compatTradeCancel(ch, req.RpcId); handled {
		return response
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
	ch.session.saleDestination = saleDestinationShop
	s.sendPush(ch, protocol.OpM2C_OpenShopUI, &protocol.M2C_OpenShopUI{ActorId: ch.session.playerID})
}

// pushOpenStoreUI 推送 M2C_OpenStoreUI(20187) 打开仓库（管家）界面。
func (s *Server) pushOpenStoreUI(ch *channel) {
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		return
	}
	// A completed compatibility trade leaves a short-lived marker so that
	// delayed trade-window refreshes receive the native "trade ended" tip.
	// Opening the warehouse from the NPC is an explicit transition back to
	// the normal store UI, so consume that marker before the client sends
	// C2M_GetStore.  Do not touch an active trade session.
	tradeMu.Lock()
	if ch.session.tradeID == 0 {
		ch.session.tradeCompatOpen = false
		ch.session.tradeCompatClosed = false
	}
	tradeMu.Unlock()
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
