package main

import (
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// consignment.go：寄售行（20181-20186 段）—— 完整玩家间交易。
//
// 协议（protocol_dump）：
//   20181/82 GetConsignment{JobType=1, ItemType=2, Page=3} →
//           M2C_GetConsignment{TotalPage=1} + ConsignMapList(tag2, 字段级)
//   20183/84 PutInConsignment{Index=1, Price=2, Count=3, Pwd=4} → 上架
//   20185/86 BuyInConsignment{ConsignmentId=1, UnitId=2, Pwd=3} → 购买
//
// ConsignMap{ConsignItemId=1, UnitId=2(卖家), Name=3, RemainTime=4,
//   Item=5(NetItem), EquipTransMessage=6, Price=7, NeedPwd=8}
//
// 数据持久化（走数据库，重启不丢失）：consignment_items 表存全部寄售条目；
// consignSeq 从 server_meta 表恢复（consign_seq 键），起服时加载全部条目进内存热缓存。
// 上架扣背包 → INSERT；购买 DELETE + 物品入买家包、货款给卖家；过期清理并退款。

// consignmentItem 一条寄售。
type consignmentItem struct {
	ID         int64 // ConsignItemId（自增，DB 主键）
	SellerID   int64
	SellerName string
	ItemId     int32
	ItemType   int32
	Count      int32
	Price      int64
	NeedPwd    bool
	Pwd        string
	RemainTime int64 // 剩余秒（上架 72 小时）
	putAt      time.Time
	// 装备详情（客户端 ConsignmentUI.ShowListItem 对 ItemType==1 用
	// ConsignMap.EquipTransMessage 创建 Equip —— 缺失则 NRE → 列表全空，
	// 用户实测"上架了但看不到"）。DB 持久化，重启可重建 EquipTransMessage。
	SpecialKey        int32
	IsLock            bool
	Star              int32
	Quality           int32
	Level             int32
	SpecialId         int32
	GemList           []int32
	PurchaseSource    int32
	PurchaseCurrency  int32
	PurchaseUnitPrice int64
}

var (
	consignMu          sync.Mutex
	consignSeq         int64              // 寄售 ID 序列（DB server_meta 持久化，重启连续）
	consignItems       []*consignmentItem // 内存热缓存（起服从 DB 加载）
	consignPageSize    = 12
	consignTTL         = 72 * time.Hour
	consignmentMailMu  sync.Mutex
	consignmentMailSeq int64
)

const consignmentCurrencyItemID int32 = 110205

// loadConsignmentsFromDB 起服加载全部寄售条目进内存（DB 为准）。
func loadConsignmentsFromDB() {
	if globalServer == nil || globalServer.store == nil {
		return
	}
	consignMu.Lock()
	defer consignMu.Unlock()
	consignItems = consignItems[:0]
	rows, err := globalServer.store.db.Query(`SELECT item.id, item.seller_id, item.seller_name, item.item_id, item.item_type,
		item.count, item.price, item.need_pwd, item.pwd, item.put_at, item.special_key, item.is_lock,
		item.star, item.quality, item.level, item.special_id, COALESCE(origin.purchase_source, 0),
		COALESCE(origin.purchase_currency, 0), COALESCE(origin.purchase_unit_price, 0)
		FROM consignment_items AS item
		LEFT JOIN consignment_item_purchase_origins AS origin ON origin.consignment_id = item.id
		ORDER BY item.id`)
	if err != nil {
		log.Printf("[CONSIGN] load from db err: %v", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		it := &consignmentItem{}
		var putAtMS int64
		var needPwd int
		var itemType, count int32
		var isLock int
		if err := rows.Scan(&it.ID, &it.SellerID, &it.SellerName, &it.ItemId, &itemType, &count,
			&it.Price, &needPwd, &it.Pwd, &putAtMS,
			&it.SpecialKey, &isLock, &it.Star, &it.Quality, &it.Level, &it.SpecialId,
			&it.PurchaseSource, &it.PurchaseCurrency, &it.PurchaseUnitPrice); err != nil {
			continue
		}
		it.ItemType = itemType
		it.Count = count
		it.NeedPwd = needPwd != 0
		it.IsLock = isLock != 0
		it.putAt = time.UnixMilli(putAtMS)
		consignItems = append(consignItems, it)
	}
	rows.Close()
	byID := make(map[int64]*consignmentItem, len(consignItems))
	for _, item := range consignItems {
		byID[item.ID] = item
	}
	gemRows, err := globalServer.store.db.Query(`SELECT consignment_id, gem_item_id
		FROM consignment_gems ORDER BY consignment_id, position`)
	if err != nil {
		log.Printf("[CONSIGN] load gems from db err: %v", err)
		return
	}
	for gemRows.Next() {
		var consignmentID int64
		var gemID int32
		if gemRows.Scan(&consignmentID, &gemID) == nil {
			if item := byID[consignmentID]; item != nil {
				item.GemList = append(item.GemList, gemID)
			}
		}
	}
	gemRows.Close()
	// 恢复 self-increment 序列
	var seq int64
	globalServer.store.db.QueryRow(`SELECT value FROM server_meta WHERE name='consign_seq'`).Scan(&seq)
	consignSeq = seq
	log.Printf("[CONSIGN] loaded %d items seq=%d from db", len(consignItems), consignSeq)
}

// consignNextSeq：取下一个寄售 ID（DB 序列自增，重启连续）。
func consignNextSeq() int64 {
	if globalServer == nil || globalServer.store == nil {
		consignSeq++
		return consignSeq
	}
	res, err := globalServer.store.db.Exec(`INSERT INTO server_meta (name, value)
		VALUES ('consign_seq', LAST_INSERT_ID(1))
		ON DUPLICATE KEY UPDATE value = LAST_INSERT_ID(value + 1)`)
	if err != nil {
		log.Printf("[CONSIGN] allocate sequence: %v", err)
		consignSeq++
		return consignSeq
	}
	v, err := res.LastInsertId()
	if err != nil || v <= 0 {
		log.Printf("[CONSIGN] read allocated sequence: %v", err)
		consignSeq++
		return consignSeq
	}
	consignSeq = v
	return v
}

// saveConsignmentItemDB 上架条目落库。
func saveConsignmentItemDB(it *consignmentItem) error {
	if globalServer == nil || globalServer.store == nil {
		return nil
	}
	np := 0
	if it.NeedPwd {
		np = 1
	}
	il := 0
	if it.IsLock {
		il = 1
	}
	tx, err := globalServer.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO consignment_items (id, seller_id, seller_name, item_id, item_type, count, price, need_pwd, pwd, put_at,
		special_key, is_lock, star, quality, level, special_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		it.ID, it.SellerID, it.SellerName, it.ItemId, it.ItemType, it.Count, it.Price, np, it.Pwd, it.putAt.UnixMilli(),
		it.SpecialKey, il, it.Star, it.Quality, it.Level, it.SpecialId); err != nil {
		return err
	}
	if it.PurchaseSource != purchaseSourceNone {
		if _, err := tx.Exec(`INSERT INTO consignment_item_purchase_origins
			(consignment_id, purchase_source, purchase_currency, purchase_unit_price) VALUES (?, ?, ?, ?)`,
			it.ID, it.PurchaseSource, it.PurchaseCurrency, it.PurchaseUnitPrice); err != nil {
			return err
		}
	}
	for position, gemID := range it.GemList {
		if _, err := tx.Exec(`INSERT INTO consignment_gems (consignment_id, position, gem_item_id) VALUES (?, ?, ?)`,
			it.ID, position, gemID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// removeConsignmentItemDB 购买/过期删除条目。
func removeConsignmentItemDB(id int64) {
	if globalServer == nil || globalServer.store == nil {
		return
	}
	globalServer.store.db.Exec(`DELETE FROM consignment_items WHERE id = ?`, id)
}

func consignmentMatches(it *consignmentItem, jobType protocol.JobType, itemType protocol.ItemType) bool {
	if it == nil {
		return false
	}
	if itemType != protocol.ItemType_NoneItem && it.ItemType != int32(itemType) {
		return false
	}
	if jobType == protocol.JobType_UnKnown || it.ItemType != int32(protocol.ItemType_EquipItem) {
		return true
	}
	if tables == nil {
		return false
	}
	row := tables.equipBase[int64(it.ItemId)]
	return row != nil && int32(num(row["JobId"])) == int32(jobType)
}

// consignmentSnapshot removes expired entries and returns a filtered stable
// snapshot. Client selectors use enum value 0 as "all".
func (s *Server) consignmentSnapshot(jobType protocol.JobType, itemType protocol.ItemType) []*consignmentItem {
	consignMu.Lock()
	now := time.Now()
	kept := consignItems[:0]
	for _, it := range consignItems {
		if now.Sub(it.putAt) >= consignTTL && s.consignmentRefund(it) {
			removeConsignmentItemDB(it.ID)
			continue
		}
		kept = append(kept, it)
	}
	consignItems = kept
	items := make([]*consignmentItem, 0, len(consignItems))
	for _, it := range consignItems {
		if consignmentMatches(it, jobType, itemType) {
			items = append(items, it)
		}
	}
	consignMu.Unlock()
	// The client opens page zero after a successful listing. Showing the oldest
	// rows first can hide a newly listed item as soon as the market has more
	// than one page, which looks like the listing silently failed.
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].ID != items[j].ID {
			return items[i].ID > items[j].ID
		}
		return items[i].putAt.After(items[j].putAt)
	})
	return items
}

func encodeConsignmentMap(it *consignmentItem) []byte {
	cm := &protocol.ConsignMap{
		ConsignItemId: it.ID,
		UnitId:        it.SellerID,
		Name:          it.SellerName,
		RemainTime:    int64(time.Until(it.putAt.Add(consignTTL)).Seconds()),
		Item: &protocol.NetItem{
			ItemId:   it.ItemId,
			ItemType: protocol.ItemType(it.ItemType),
			Count:    it.Count,
		},
		Price:   it.Price,
		NeedPwd: it.NeedPwd,
	}
	raw, err := proto.Marshal(cm)
	if err != nil {
		return nil
	}
	if it.ItemType == int32(protocol.ItemType_EquipItem) || itemIsEquip(it.ItemId) {
		bi := &bagItem{
			ItemId: it.ItemId, ItemType: int32(protocol.ItemType_EquipItem),
			IsLock: it.IsLock, Quality: it.Quality, Star: it.Star, Level: it.Level,
			SpecialKey: it.SpecialKey, SpecialId: it.SpecialId,
			GemList: append([]int32(nil), it.GemList...),
		}
		if et := encodeEquipTrans(bi); len(et) > 0 {
			raw = append(raw, pbAppendBytes(nil, 6, et)...)
		}
	}
	return raw
}

// appendConsignmentPage appends field-level ConsignMapList(tag2). Pages are
// zero-based because ConsignmentUI.currPage starts at zero and is sent as-is.
func appendConsignmentPage(base []byte, items []*consignmentItem, page int32) (raw []byte, pages int32) {
	pages = int32((len(items) + consignPageSize - 1) / consignPageSize)
	if pages < 1 {
		pages = 1
	}
	if page < 0 {
		page = 0
	}
	start := int(page) * consignPageSize
	if start < 0 || start >= len(items) {
		return base, pages
	}
	end := start + consignPageSize
	if end > len(items) {
		end = len(items)
	}
	for _, it := range items[start:end] {
		if entry := encodeConsignmentMap(it); len(entry) > 0 {
			base = append(base, pbAppendBytes(nil, 2, entry)...)
		}
	}
	return base, pages
}

// 20181 → 20182：寄售列表（按页）。raw 20182 手工编码 ConsignMapList(tag2)。
func (s *Server) onGetConsignment(ch *channel, req *protocol.C2M_GetConsignment) proto.Message {
	ss := ch.session
	if ch == nil || ss == nil || ss.playerID == 0 {
		resp := &protocol.M2C_GetConsignment{RpcId: req.RpcId}
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	items := s.consignmentSnapshot(req.JobType, req.ItemType)
	pages := int32((len(items) + consignPageSize - 1) / consignPageSize)
	if pages < 1 {
		pages = 1
	}
	base, err := proto.Marshal(&protocol.M2C_GetConsignment{RpcId: req.RpcId, TotalPage: pages})
	if err != nil {
		return nil
	}
	base, pages = appendConsignmentPage(base, items, req.Page)
	s.sendRawPush(ch, protocol.OpM2C_GetConsignment, base)
	log.Printf("[S=%d] get consignment page=%d total=%d pages=%d job=%d itemType=%d",
		ch.id, req.Page, len(items), pages, req.JobType, req.ItemType)
	return nil
}

// 20183 → 20184：上架。扣背包 → 生成寄售条目（ConsignItemId 自增）。
func (s *Server) onPutInConsignment(ch *channel, req *protocol.C2M_PutInConsignment) proto.Message {
	ss := ch.session
	resp := &protocol.M2C_PutInConsignment{RpcId: req.RpcId}
	if ch == nil || ss == nil || ss.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	it, ok := ss.bag[req.Index]
	if !ok || it == nil {
		resp.Message = "物品不存在"
		return resp
	}
	if req.Count <= 0 || req.Count > it.Count {
		resp.Message = "数量无效"
		return resp
	}
	if req.Price <= 0 {
		resp.Message = "价格无效"
		return resp
	}
	if _, ok := safePriceTotal(req.Price, req.Count); !ok {
		resp.Message = "价格或数量无效"
		return resp
	}
	if !ss.removeBagCount(req.Index, req.Count) {
		resp.Message = "数量无效"
		return resp
	}
	// 装备详情快照（背包格删除后 it 指针仍有效；客户端列表按 ItemType==1
	// 用 ConsignMap.EquipTransMessage 渲染，缺失会 NRE → 列表全空）
	repairBagItem(it)
	consignMu.Lock()
	id := consignNextSeq()
	consignMu.Unlock()
	item := &consignmentItem{
		ID:                id,
		SellerID:          ss.playerID,
		SellerName:        ss.name,
		ItemId:            it.ItemId,
		ItemType:          it.ItemType,
		Count:             req.Count,
		Price:             req.Price,
		NeedPwd:           req.Pwd != "",
		Pwd:               req.Pwd,
		putAt:             time.Now(),
		SpecialKey:        it.SpecialKey,
		IsLock:            it.IsLock,
		Star:              it.Star,
		Quality:           it.Quality,
		Level:             it.Level,
		SpecialId:         it.SpecialId,
		GemList:           append([]int32(nil), it.GemList...),
		PurchaseSource:    it.PurchaseSource,
		PurchaseCurrency:  it.PurchaseCurrency,
		PurchaseUnitPrice: it.PurchaseUnitPrice,
	}
	if err := saveConsignmentItemDB(item); err != nil {
		restoreBagItem(ss, req.Index, it, req.Count)
		log.Printf("[CONSIGN] save listing id=%d player=%d: %v", id, ss.playerID, err)
		resp.Message = "寄售上架失败，请稍后重试"
		return resp
	}
	consignMu.Lock()
	consignItems = append(consignItems, item)
	consignMu.Unlock()
	base, err := proto.Marshal(resp)
	if err != nil {
		return resp
	}
	base = encodeBagMapList(base, 1, bagPairs(ss.bag))
	s.sendRawPush(ch, protocol.OpM2C_PutInConsignment, base)
	s.saveData(ch)
	log.Printf("[S=%d] put in consignment id=%d item=%d count=%d price=%d", ch.id, id, it.ItemId, req.Count, req.Price)
	return nil
}

// 20185 → 20186：购买寄售物品。
func (s *Server) onBuyInConsignment(ch *channel, req *protocol.C2M_BuyInConsignment) proto.Message {
	resp := &protocol.M2C_BuyInConsignment{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	ss := ch.session
	consignMu.Lock()
	idx := -1
	for i, it := range consignItems {
		if it.ID == req.ConsignmentId {
			idx = i
			break
		}
	}
	if idx < 0 {
		consignMu.Unlock()
		resp.Message = "寄售物品不存在"
		return resp
	}
	it := consignItems[idx]
	if it.SellerID == ss.playerID {
		consignMu.Unlock()
		resp.Message = "不能购买自己的寄售"
		return resp
	}
	if it.NeedPwd && it.Pwd != req.Pwd {
		consignMu.Unlock()
		resp.Message = "密码错误"
		return resp
	}
	total, validPrice := safePriceTotal(it.Price, it.Count)
	if !validPrice {
		consignMu.Unlock()
		resp.Message = "寄售价格无效"
		return resp
	}
	stagedBag, message := stageConsignmentPurchase(ss, it, total)
	if message != "" {
		consignMu.Unlock()
		resp.Message = message
		return resp
	}
	mail := newConsignmentProceedsMail(total)
	if err := s.persistConsignmentSale(it.ID, it.SellerID, mail); err != nil {
		consignMu.Unlock()
		log.Printf("[CONSIGN] persist sale id=%d seller=%d: %v", it.ID, it.SellerID, err)
		resp.Message = "寄售交易失败，请稍后重试"
		return resp
	}

	// DB 删除和卖家邮件均成功后，才提交买家背包与内存寄售列表。
	ss.bag = stagedBag
	consignItems = append(consignItems[:idx], consignItems[idx+1:]...)
	consignMu.Unlock()

	s.deliverConsignmentProceeds(it.SellerID, mail)
	s.saveData(ch)
	base, err := proto.Marshal(resp)
	if err != nil {
		return resp
	}
	base = encodeBagMapList(base, 1, bagPairs(ss.bag))
	s.sendRawPush(ch, protocol.OpM2C_BuyInConsignment, base)
	log.Printf("[S=%d] buy consignment id=%d item=%d x%d price=%d totalStarCoin=%d from %d",
		ch.id, it.ID, it.ItemId, it.Count, it.Price, total, it.SellerID)
	return nil
}

func bagItemCount64(ss *session, itemID int32) int64 {
	if ss == nil {
		return 0
	}
	var total int64
	for _, item := range ss.bag {
		if item != nil && item.ItemId == itemID && item.Count > 0 {
			total += int64(item.Count)
		}
	}
	return total
}

func removeBagItemCount64(ss *session, itemID int32, count int64) bool {
	if ss == nil || count < 0 || bagItemCount64(ss, itemID) < count {
		return false
	}
	if isStarCoinItem(itemID) {
		canonicalizeStarCoins(ss)
	}
	for index, item := range ss.bag {
		if count == 0 {
			break
		}
		if item == nil || item.ItemId != itemID || item.Count <= 0 {
			continue
		}
		if int64(item.Count) <= count {
			count -= int64(item.Count)
			delete(ss.bag, index)
			continue
		}
		item.Count -= int32(count)
		count = 0
	}
	return count == 0
}

// stageConsignmentPurchase validates the complete exchange on a cloned bag.
// The online client treats GoodsBase 110205 as the consignment currency;
// NumericType 1028 copper coins are unrelated to this transaction.
func stageConsignmentPurchase(ss *session, item *consignmentItem, total int64) (map[int32]*bagItem, string) {
	if ss == nil || item == nil || total <= 0 || total > int64(1<<31-1) {
		return nil, "寄售价格无效"
	}
	if bagItemCount64(ss, consignmentCurrencyItemID) < total {
		return nil, "星币不足"
	}
	shadow := &session{bag: cloneBagMap(ss.bag)}
	if !removeBagItemCount64(shadow, consignmentCurrencyItemID, total) {
		return nil, "星币不足"
	}
	firstIndex, ok := addItemToBagInPlaceWithGrant(shadow, bagGrant{
		itemID: item.ItemId, count: item.Count,
		purchaseSource: purchaseSourceConsignment, purchaseCurrency: purchaseCurrencyStarCoin,
		purchaseUnitPrice: item.Price,
	})
	if !ok {
		return nil, "背包已满"
	}
	if purchased := shadow.bag[firstIndex]; purchased != nil &&
		(item.ItemType == int32(protocol.ItemType_EquipItem) || itemIsEquip(item.ItemId)) {
		purchased.ItemType = int32(protocol.ItemType_EquipItem)
		purchased.SpecialKey = item.SpecialKey
		purchased.IsLock = item.IsLock
		purchased.Star = item.Star
		purchased.Quality = item.Quality
		purchased.Level = item.Level
		purchased.SpecialId = item.SpecialId
		purchased.GemList = append([]int32(nil), item.GemList...)
	}
	return shadow.bag, ""
}

func nextConsignmentMailID() int64 {
	consignmentMailMu.Lock()
	defer consignmentMailMu.Unlock()
	next := time.Now().UnixNano()
	if next <= consignmentMailSeq {
		next = consignmentMailSeq + 1
	}
	consignmentMailSeq = next
	return next
}

func newConsignmentProceedsMail(total int64) *mailMsg {
	return &mailMsg{
		Id:         nextConsignmentMailID(),
		Title:      "寄售成交",
		Content:    "你寄售的物品已售出，星币货款已放入附件。",
		SenderName: "寄售行",
		RemainTime: time.Now().Add(30 * 24 * time.Hour).UnixMilli(),
		Items: []mailItemMsg{{
			ItemId: consignmentCurrencyItemID, Count: int32(total), IsHasItem: true,
		}},
	}
}

// persistConsignmentSale atomically removes the listing and creates the
// seller's proceeds mail. Tests without a store still exercise the live state.
func (s *Server) persistConsignmentSale(consignmentID, sellerID int64, mail *mailMsg) error {
	if s == nil || s.store == nil || s.store.db == nil {
		return nil
	}
	tx, err := s.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`DELETE FROM consignment_items WHERE id = ? AND seller_id = ?`, consignmentID, sellerID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("listing delete affected %d rows", affected)
	}
	if _, err := tx.Exec(`INSERT INTO player_mails
		(player_id, mail_id, title, content, sender_name, state, remain_time) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sellerID, mail.Id, mail.Title, mail.Content, mail.SenderName, mail.State, mail.RemainTime); err != nil {
		return err
	}
	for position, item := range mail.Items {
		if _, err := tx.Exec(`INSERT INTO player_mail_items
			(player_id, mail_id, position, item_id, item_count, is_locked, is_has_item) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			sellerID, mail.Id, position, item.ItemId, item.Count, item.IsLock, item.IsHasItem); err != nil {
			return err
		}
		if item.PurchaseSource != purchaseSourceNone {
			if _, err := tx.Exec(`INSERT INTO player_mail_item_purchase_origins
				(player_id, mail_id, position, purchase_source, purchase_currency, purchase_unit_price) VALUES (?, ?, ?, ?, ?, ?)`,
				sellerID, mail.Id, position, item.PurchaseSource, item.PurchaseCurrency, item.PurchaseUnitPrice); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Server) deliverConsignmentProceeds(sellerID int64, mail *mailMsg) {
	if mail == nil {
		return
	}
	if rc := s.findChannelByPlayerID(sellerID); rc != nil && rc.session != nil {
		rc.session.battleMu.Lock()
		rc.session.mails = append(rc.session.mails, mail)
		if mail.Id > rc.session.mailSeq {
			rc.session.mailSeq = mail.Id
		}
		s.saveData(rc)
		rc.session.battleMu.Unlock()
		log.Printf("[CONSIGN] delivered %d star coins to online seller %d by mail", mail.Items[0].Count, sellerID)
		return
	}
	log.Printf("[CONSIGN] delivered %d star coins to offline seller %d by mail", mail.Items[0].Count, sellerID)
}

// consignmentRefund：寄售过期 → 原物品退回卖家背包；背包满或离线时通过附件邮件返还，
// 绝不把物品价值折算成铜币。
func (s *Server) consignmentRefund(it *consignmentItem) bool {
	if it == nil || it.ItemId <= 0 || it.Count <= 0 {
		return false
	}
	if rc := s.findChannelByPlayerID(it.SellerID); rc != nil && rc.session != nil {
		if idx := rc.session.addItemToBagWithGrant(bagGrant{
			itemID: it.ItemId, count: it.Count,
			purchaseSource: it.PurchaseSource, purchaseCurrency: it.PurchaseCurrency,
			purchaseUnitPrice: it.PurchaseUnitPrice,
		}); idx >= 0 ||
			(isStarCoinItem(it.ItemId) && idx == starCoinBagSlot) {
			s.saveData(rc)
			if isStarCoinItem(it.ItemId) {
				s.pushMoney(rc)
				s.pushBagSnapshot(rc)
			}
			log.Printf("[CONSIGN] expired item %d x%d refunded to %d", it.ItemId, it.Count, it.SellerID)
			return true
		}
		// 在线但背包满：原物品进邮件，绝不能把物品价值转换成铜币。
		mail := newConsignmentRefundMail(it)
		rc.session.mails = append(rc.session.mails, mail)
		if mail.Id > rc.session.mailSeq {
			rc.session.mailSeq = mail.Id
		}
		s.saveData(rc)
		log.Printf("[CONSIGN] expired item %d x%d mailed back to %d (bag full)", it.ItemId, it.Count, it.SellerID)
		return true
	}
	// 离线：同样通过附件邮件返还原物品；不写入 coin。
	mail := newConsignmentRefundMail(it)
	if s != nil && s.store != nil && s.store.db != nil {
		tx, err := s.store.db.Begin()
		if err != nil {
			log.Printf("[CONSIGN] begin refund player=%d: %v", it.SellerID, err)
			return false
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`INSERT INTO player_mails
			(player_id, mail_id, title, content, sender_name, state, remain_time) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			it.SellerID, mail.Id, mail.Title, mail.Content, mail.SenderName, mail.State, mail.RemainTime); err != nil {
			log.Printf("[CONSIGN] persist refund mail player=%d: %v", it.SellerID, err)
			return false
		}
		for position, item := range mail.Items {
			if _, err := tx.Exec(`INSERT INTO player_mail_items
				(player_id, mail_id, position, item_id, item_count, is_locked, is_has_item) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				it.SellerID, mail.Id, position, item.ItemId, item.Count, item.IsLock, item.IsHasItem); err != nil {
				log.Printf("[CONSIGN] persist refund attachment player=%d: %v", it.SellerID, err)
				return false
			}
			if item.PurchaseSource != purchaseSourceNone {
				if _, err := tx.Exec(`INSERT INTO player_mail_item_purchase_origins
					(player_id, mail_id, position, purchase_source, purchase_currency, purchase_unit_price) VALUES (?, ?, ?, ?, ?, ?)`,
					it.SellerID, mail.Id, position, item.PurchaseSource, item.PurchaseCurrency, item.PurchaseUnitPrice); err != nil {
					log.Printf("[CONSIGN] persist refund attachment origin player=%d: %v", it.SellerID, err)
					return false
				}
			}
		}
		if _, err := tx.Exec(`DELETE FROM consignment_items WHERE id = ? AND seller_id = ?`, it.ID, it.SellerID); err != nil {
			log.Printf("[CONSIGN] remove refunded listing=%d: %v", it.ID, err)
			return false
		}
		if err := tx.Commit(); err != nil {
			log.Printf("[CONSIGN] commit refund player=%d: %v", it.SellerID, err)
			return false
		}
	}
	log.Printf("[CONSIGN] expired item %d x%d mailed back to offline seller %d", it.ItemId, it.Count, it.SellerID)
	return true
}

func newConsignmentRefundMail(it *consignmentItem) *mailMsg {
	return &mailMsg{
		Id: nextConsignmentMailID(), Title: "寄售退回", Content: "寄售已过期，物品已退回附件。", SenderName: "寄售行",
		RemainTime: time.Now().Add(30 * 24 * time.Hour).UnixMilli(),
		Items: []mailItemMsg{{
			ItemId: it.ItemId, Count: it.Count, IsHasItem: true,
			PurchaseSource: it.PurchaseSource, PurchaseCurrency: it.PurchaseCurrency,
			PurchaseUnitPrice: it.PurchaseUnitPrice,
		}},
	}
}
