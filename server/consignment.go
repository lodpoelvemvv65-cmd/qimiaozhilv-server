package main

import (
	"encoding/json"
	"log"
	"sort"
	"strings"
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
	SpecialKey int32
	IsLock     bool
	Star       int32
	Quality    int32
	Level      int32
	SpecialId  int32
	GemList    []int32
}

var (
	consignMu       sync.Mutex
	consignSeq      int64              // 寄售 ID 序列（DB server_meta 持久化，重启连续）
	consignItems    []*consignmentItem // 内存热缓存（起服从 DB 加载）
	consignPageSize = 12
	consignTTL      = 72 * time.Hour
)

// loadConsignmentsFromDB 起服加载全部寄售条目进内存（DB 为准）。
func loadConsignmentsFromDB() {
	if globalServer == nil || globalServer.store == nil {
		return
	}
	consignMu.Lock()
	defer consignMu.Unlock()
	consignItems = consignItems[:0]
	rows, err := globalServer.store.db.Query(`SELECT id, seller_id, seller_name, item_id, item_type, count, price, need_pwd, pwd, put_at,
		special_key, is_lock, star, quality, level, special_id, gems
		FROM consignment_items ORDER BY id`)
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
		var gemsTxt string
		if err := rows.Scan(&it.ID, &it.SellerID, &it.SellerName, &it.ItemId, &itemType, &count,
			&it.Price, &needPwd, &it.Pwd, &putAtMS,
			&it.SpecialKey, &isLock, &it.Star, &it.Quality, &it.Level, &it.SpecialId, &gemsTxt); err != nil {
			continue
		}
		it.ItemType = itemType
		it.Count = count
		it.NeedPwd = needPwd != 0
		it.IsLock = isLock != 0
		it.GemList = parseGemsJSON(gemsTxt)
		it.putAt = time.UnixMilli(putAtMS)
		consignItems = append(consignItems, it)
	}
	// 恢复 self-increment 序列
	var seq int64
	globalServer.store.db.QueryRow(`SELECT value FROM server_meta WHERE name='consign_seq'`).Scan(&seq)
	consignSeq = seq
	log.Printf("[CONSIGN] loaded %d items seq=%d from db", len(consignItems), consignSeq)
}

// gemsJSON/gemsFromJSON：宝石槽位 JSON 编码（DB 列 gems）。
func gemsJSON(gems []int32) string {
	b, _ := json.Marshal(gems)
	return string(b)
}

func parseGemsJSON(s string) []int32 {
	s = strings.TrimSpace(s)
	if s == "" || s == "null" || s == "[]" {
		return nil
	}
	var out []int32
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

// consignNextSeq：取下一个寄售 ID（DB 序列自增，重启连续）。
func consignNextSeq() int64 {
	if globalServer == nil || globalServer.store == nil {
		consignSeq++
		return consignSeq
	}
	globalServer.store.db.Exec(`INSERT INTO server_meta (name, value) VALUES ('consign_seq', 1)
		ON CONFLICT(name) DO UPDATE SET value = value + 1`)
	var v int64
	globalServer.store.db.QueryRow(`SELECT value FROM server_meta WHERE name='consign_seq'`).Scan(&v)
	consignSeq = v
	return v
}

// saveConsignmentItemDB 上架条目落库。
func saveConsignmentItemDB(it *consignmentItem) {
	if globalServer == nil || globalServer.store == nil {
		return
	}
	np := 0
	if it.NeedPwd {
		np = 1
	}
	il := 0
	if it.IsLock {
		il = 1
	}
	globalServer.store.db.Exec(`INSERT INTO consignment_items (id, seller_id, seller_name, item_id, item_type, count, price, need_pwd, pwd, put_at,
		special_key, is_lock, star, quality, level, special_id, gems)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		it.ID, it.SellerID, it.SellerName, it.ItemId, it.ItemType, it.Count, it.Price, np, it.Pwd, it.putAt.UnixMilli(),
		it.SpecialKey, il, it.Star, it.Quality, it.Level, it.SpecialId, gemsJSON(it.GemList))
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
		if now.Sub(it.putAt) >= consignTTL {
			s.consignmentRefund(it) // 过期退还
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
	item := &consignmentItem{
		ID:         id,
		SellerID:   ss.playerID,
		SellerName: ss.name,
		ItemId:     it.ItemId,
		ItemType:   it.ItemType,
		Count:      req.Count,
		Price:      req.Price,
		NeedPwd:    req.Pwd != "",
		Pwd:        req.Pwd,
		putAt:      time.Now(),
		SpecialKey: it.SpecialKey,
		IsLock:     it.IsLock,
		Star:       it.Star,
		Quality:    it.Quality,
		Level:      it.Level,
		SpecialId:  it.SpecialId,
		GemList:    append([]int32(nil), it.GemList...),
	}
	consignItems = append(consignItems, item)
	consignMu.Unlock()
	saveConsignmentItemDB(item) // 上架条目走数据库（重启保留）
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
	ss := ch.session
	resp := &protocol.M2C_BuyInConsignment{RpcId: req.RpcId}
	if ch == nil || ss == nil || ss.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	consignMu.Lock()
	var idx = -1
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
	if ss.coin < total {
		consignMu.Unlock()
		resp.Message = "铜币不足"
		return resp
	}
	// 成交：扣买家钱 → 删条目 → 物品入买家包 → 钱给卖家（邮件）
	ss.coin -= total
	consignItems = append(consignItems[:idx], consignItems[idx+1:]...)
	seller := &consignmentItem{ID: it.ID, SellerID: it.SellerID, SellerName: it.SellerName,
		ItemId: it.ItemId, ItemType: it.ItemType, Count: it.Count, Price: it.Price}
	consignMu.Unlock()
	removeConsignmentItemDB(it.ID) // 成交条目从 DB 删除

	idxBag := ss.addItemToBag(it.ItemId, it.Count)
	if idxBag < 0 {
		// 背包满：回滚（钱退回、条目恢复）
		ss.coin += total
		consignMu.Lock()
		consignItems = append(consignItems, it)
		consignMu.Unlock()
		saveConsignmentItemDB(it) // 回滚 → DB 重新写入
		resp.Message = "背包已满"
		return resp
	}
	s.consignmentPaySeller(seller, total)
	s.saveData(ch)
	base, err := proto.Marshal(resp)
	if err != nil {
		return resp
	}
	base = encodeBagMapList(base, 1, bagPairs(ss.bag))
	s.sendRawPush(ch, protocol.OpM2C_BuyInConsignment, base)
	// 推 20169 铜币（NumericType 1028）
	s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
		UnitId: ss.playerID, NumericType: 1028, Value: float32(ss.coin), ActorId: ss.playerID,
	})
	log.Printf("[S=%d] buy consignment id=%d item=%d x%d price=%d total=%d from %d",
		ch.id, it.ID, it.ItemId, it.Count, it.Price, total, it.SellerID)
	return nil
}

// consignmentPaySeller：寄售货款入卖家铜币；卖家离线/在线都直接到账（DB 更新）。
func (s *Server) consignmentPaySeller(it *consignmentItem, total int64) {
	// 在线卖家：直接加 session.coin + 推 1028
	if rc := s.findChannelByPlayerID(it.SellerID); rc != nil && rc.session != nil {
		rc.session.coin += total
		s.saveData(rc)
		s.sendPush(rc, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: rc.session.playerID, NumericType: 1028, Value: float32(rc.session.coin), ActorId: rc.session.playerID,
		})
		return
	}
	// 离线卖家：直接 SQL 加钱
	s.store.db.Exec(`UPDATE players SET coin = coin + ? WHERE id = ?`, total, it.SellerID)
	log.Printf("[CONSIGN] pay seller %d %d coin (offline)", it.SellerID, total)
}

// consignmentRefund：寄售过期 → 物品退回卖家背包；离线卖家按上架价退铜币（等价物）。
func (s *Server) consignmentRefund(it *consignmentItem) {
	if rc := s.findChannelByPlayerID(it.SellerID); rc != nil && rc.session != nil {
		if idx := rc.session.addItemToBag(it.ItemId, it.Count); idx >= 0 {
			s.saveData(rc)
			log.Printf("[CONSIGN] expired item %d x%d refunded to %d", it.ItemId, it.Count, it.SellerID)
			return
		}
		// 在线但背包满：退铜币
		rc.session.coin += it.Price * int64(it.Count)
		s.saveData(rc)
		s.sendPush(rc, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: rc.session.playerID, NumericType: 1028, Value: float32(rc.session.coin), ActorId: rc.session.playerID,
		})
		log.Printf("[CONSIGN] expired item %d x%d -> coin refund to %d (bag full)", it.ItemId, it.Count, it.SellerID)
		return
	}
	// 离线：等价铜币到账
	s.store.db.Exec(`UPDATE players SET coin = coin + ? WHERE id = ?`, it.Price*int64(it.Count), it.SellerID)
	log.Printf("[CONSIGN] expired item %d x%d -> coin refund to %d (offline)", it.ItemId, it.Count, it.SellerID)
}
