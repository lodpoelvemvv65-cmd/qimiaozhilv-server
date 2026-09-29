package main

import (
	"log"
	"math"
	"sort"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// mail.go：邮件系统（20281-20290 段，见 16 文档 §2.2）。
//
// 数据模型：session.mails 通过 player_mails/player_mail_items 持久化（插入序 = 展示序）；
// session.mailSeq 在 loadData 时从已有最大 Id 恢复。
//
// MailList / RewordArr 是客户端"字段级 ProtoMember"（repeated message），protoc 生成代码
// 未包含，响应体用 pb_extra.go 的手工编码辅助（pbAppendBytes）在 proto.Marshal 结果后追加。
// 客户端 currPage 从 1 开始；MailCount 语义 = 总页数，每页固定 10 封。

const mailPageSize = 10

// encodeMailItem：MailItem{ItemId=1(varint), Count=4(varint,int64),
// IsLock=5(varint,bool), IsHasItem=6(varint,bool)}。
func encodeMailItem(m mailItemMsg) []byte {
	var b []byte
	b = pbAppendVarint(b, 1, uint64(m.ItemId))
	if m.Count != 0 {
		b = pbAppendVarint(b, 4, uint64(m.Count))
	}
	if m.IsLock {
		b = pbAppendVarint(b, 5, 1)
	}
	if m.IsHasItem {
		b = pbAppendVarint(b, 6, 1)
	}
	return b
}

// encodeMail：Mail{Title=2(string), Content=3(string), RemainTime=4(varint,int64 剩余秒),
// SenderName=5(string), RewordArr=6(repeated MailItem), State=7(varint,0未领/1已领),
// Id=8(varint)}。
func encodeMail(m *mailMsg) []byte {
	if m == nil {
		return nil
	}
	var b []byte
	if m.Title != "" {
		b = pbAppendBytes(b, 2, []byte(m.Title))
	}
	if m.Content != "" {
		b = pbAppendBytes(b, 3, []byte(m.Content))
	}
	if m.RemainTime != 0 {
		b = pbAppendVarint(b, 4, uint64(m.RemainTime))
	}
	if m.SenderName != "" {
		b = pbAppendBytes(b, 5, []byte(m.SenderName))
	}
	for _, it := range m.Items {
		b = pbAppendBytes(b, 6, encodeMailItem(it))
	}
	b = pbAppendVarint(b, 7, uint64(m.State))
	b = pbAppendVarint(b, 8, uint64(m.Id))
	return b
}

// stageMailRewards validates every bag attachment before any mail state or
// currency is changed. Currency attachments do not occupy bag slots.
type mailCurrencyRewards struct {
	exp, coin, yuanBao, voucher int64
}

func stageMailRewards(ss *session, mails []*mailMsg) (map[int32]*bagItem, mailCurrencyRewards, bool) {
	var grants []bagGrant
	var currencies mailCurrencyRewards
	for _, mail := range mails {
		if mail == nil {
			continue
		}
		for _, item := range mail.Items {
			if item.ItemId <= 0 || item.Count <= 0 {
				continue
			}
			switch item.ItemId {
			case 110201:
				if !addInt64Checked(&currencies.exp, int64(item.Count)) {
					return nil, mailCurrencyRewards{}, false
				}
			case 110202:
				if !addInt64Checked(&currencies.yuanBao, int64(item.Count)) {
					return nil, mailCurrencyRewards{}, false
				}
			case 110203:
				if !addInt64Checked(&currencies.coin, int64(item.Count)) {
					return nil, mailCurrencyRewards{}, false
				}
			case 110204:
				if !addInt64Checked(&currencies.voucher, int64(item.Count)) {
					return nil, mailCurrencyRewards{}, false
				}
			default:
				grants = append(grants, bagGrant{
					itemID: item.ItemId, count: item.Count,
					purchaseSource: item.PurchaseSource, purchaseCurrency: item.PurchaseCurrency,
					purchaseUnitPrice: item.PurchaseUnitPrice,
				})
			}
		}
	}
	staged, _, ok := stageBagGrants(ss, grants)
	return staged, currencies, ok
}

func seedWelcomeMail(ss *session, now time.Time) bool {
	if ss == nil || len(ss.mails) != 0 || ss.mailSeq != 0 {
		return false
	}
	ss.mailSeq = 1
	ss.mails = []*mailMsg{{
		Id:         ss.mailSeq,
		Title:      "欢迎回家！",
		Content:    "欢迎来到《梦幻奇遇记》私服，祝你玩得开心！",
		SenderName: "系统",
		RemainTime: now.Add(30 * 24 * time.Hour).UnixMilli(),
		Items: []mailItemMsg{{
			ItemId: 110305, Count: 20, IsHasItem: true,
		}},
	}}
	return true
}

// pruneExpiredMails removes mails whose absolute expiry timestamp has passed.
// RemainTime=0 is the native permanent-mail convention.
func pruneExpiredMails(ss *session, now time.Time) bool {
	if ss == nil || len(ss.mails) == 0 {
		return false
	}
	nowMS := now.UnixMilli()
	kept := ss.mails[:0]
	changed := false
	for _, mail := range ss.mails {
		if mail != nil && mail.RemainTime > 0 && mail.RemainTime <= nowMS {
			changed = true
			continue
		}
		kept = append(kept, mail)
	}
	if changed {
		ss.mails = kept
	}
	return changed
}

func mailPageInfo(total, page int32) (start, end, pages int32) {
	if total <= 0 {
		return 0, 0, 0
	}
	pages = (total + mailPageSize - 1) / mailPageSize
	if page < 1 {
		page = 1
	}
	if page > pages {
		page = pages
	}
	start = (page - 1) * mailPageSize
	end = start + mailPageSize
	if end > total {
		end = total
	}
	return start, end, pages
}

// appendMailPage appends the page of MailList fields and returns total pages.
func appendMailPage(base []byte, mails []*mailMsg, page int32) ([]byte, int32) {
	start, end, pages := mailPageInfo(int32(len(mails)), page)
	for _, mail := range mails[start:end] {
		base = pbAppendBytes(base, 1, encodeMail(mail))
	}
	return base, pages
}

// appendBattleRewardMail keeps an already-won battle reward claimable when the
// live bag cannot stage every item. One mail is created per settlement, with
// deterministic item ordering and int32-sized attachment chunks matching the
// wire type used by the native client.
func appendBattleRewardMail(ss *session, items map[int32]int64) *mailMsg {
	if ss == nil || len(items) == 0 {
		return nil
	}
	ids := make([]int, 0, len(items))
	for itemID, count := range items {
		if itemID > 0 && count > 0 {
			ids = append(ids, int(itemID))
		}
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Ints(ids)
	if ss.mailSeq < 0 {
		ss.mailSeq = 0
	}
	ss.mailSeq++
	mail := &mailMsg{
		Id:         ss.mailSeq,
		Title:      "Battle reward delivery",
		Content:    "Your battle reward was sent by mail because the bag was full.",
		SenderName: "System",
		State:      0,
		RemainTime: 0,
	}
	for _, rawID := range ids {
		itemID := int32(rawID)
		remaining := items[itemID]
		for remaining > 0 {
			chunk := remaining
			if chunk > math.MaxInt32 {
				chunk = math.MaxInt32
			}
			mail.Items = append(mail.Items, mailItemMsg{
				ItemId: itemID, Count: int32(chunk), IsHasItem: true,
			})
			remaining -= chunk
		}
	}
	if len(mail.Items) == 0 {
		return nil
	}
	ss.mails = append(ss.mails, mail)
	return mail
}

// onGetMail：20281 → 20282。查询邮件列表（客户端页码从 1 开始）。
// 首次打开（无邮件且 id 发生器为 0）时播种一封欢迎邮件（110305 超小型生命药水 ×20）。
func (s *Server) onGetMail(ch *channel, req *protocol.C2M_GetMail) proto.Message {
	if ch == nil || ch.session == nil {
		return &protocol.M2C_GetMail{RpcId: req.RpcId, Error: errNotLogged, Message: "请先登录"}
	}
	ss := ch.session
	pruned := pruneExpiredMails(ss, time.Now())
	seeded := seedWelcomeMail(ss, time.Now())
	if seeded {
		log.Printf("[S=%d] seed welcome mail", ch.id)
	}
	if len(ss.mails) == 0 {
		// 空列表：MailCount 应为 0（客户端页码 "1/1" 显示空列表是错误表现）。
		// 注意：proto3 中 0 是零值，序列化后 tag2 被省略 → 客户端读缺省 0，正确。
		base, err := proto.Marshal(&protocol.M2C_GetMail{RpcId: req.RpcId, MailCount: 0})
		if err != nil {
			return &protocol.M2C_GetMail{RpcId: req.RpcId, Error: errBadParam, Message: "序列化失败"}
		}
		s.sendRawPush(ch, protocol.OpM2C_GetMail, base)
		log.Printf("[S=%d] get mail count=0", ch.id)
		return nil
	}
	base, err := proto.Marshal(&protocol.M2C_GetMail{RpcId: req.RpcId})
	if err != nil {
		return &protocol.M2C_GetMail{RpcId: req.RpcId, Error: errBadParam, Message: "序列化失败"}
	}
	var pages int32
	base, pages = appendMailPage(base, ss.mails, req.Page)
	base = pbAppendVarint(base, 2, uint64(pages))
	s.sendRawPush(ch, protocol.OpM2C_GetMail, base)
	if seeded || pruned {
		s.saveData(ch)
	}
	log.Printf("[S=%d] get mail page=%d count=%d pages=%d", ch.id, req.Page, len(ss.mails), pages)
	return nil
}

// onReceiveMail：20283 → 20284。全部附件确认能入包后才置 State=1；背包满
// 返回 Message，客户端弹提示并保留邮件未领取状态。
func (s *Server) onReceiveMail(ch *channel, req *protocol.C2M_ReceiveMail) proto.Message {
	if ch == nil || ch.session == nil {
		return &protocol.M2C_ReceiveMail{RpcId: req.RpcId, Error: errNotLogged, Message: "请先登录"}
	}
	ss := ch.session
	starBefore := starCoinBalance(ss)
	if pruneExpiredMails(ss, time.Now()) {
		s.saveData(ch)
	}
	var mail *mailMsg
	for _, m := range ss.mails {
		if m != nil && m.Id == req.MailId {
			mail = m
			break
		}
	}
	if mail == nil {
		return &protocol.M2C_ReceiveMail{RpcId: req.RpcId, Message: "邮件不存在"}
	}
	if mail.State == 1 {
		return &protocol.M2C_ReceiveMail{RpcId: req.RpcId, Message: "邮件已领取"}
	}
	staged, currencies, ok := stageMailRewards(ss, []*mailMsg{mail})
	if !ok {
		log.Printf("[S=%d] receive mail %d rejected: bag full", ch.id, mail.Id)
		return &protocol.M2C_ReceiveMail{RpcId: req.RpcId, Message: "背包已满"}
	}
	delta := currencyDelta{coin: currencies.coin, yuanBao: currencies.yuanBao, voucher: currencies.voucher}
	if !ss.canApplyCurrencyDelta(delta) {
		return &protocol.M2C_ReceiveMail{RpcId: req.RpcId, Message: "邮件货币已达上限"}
	}
	ss.bag = staged
	ss.exp += currencies.exp
	s.applyCurrencyDelta(ch, delta)
	mail.State = 1
	base, err := proto.Marshal(&protocol.M2C_ReceiveMail{RpcId: req.RpcId})
	if err != nil {
		return &protocol.M2C_ReceiveMail{RpcId: req.RpcId, Error: errBadParam, Message: "序列化失败"}
	}
	base = encodeBagMapList(base, 1, bagPairs(ss.bag))
	var pages int32
	start, end, pages := mailPageInfo(int32(len(ss.mails)), req.Page)
	for _, m := range ss.mails[start:end] {
		base = pbAppendBytes(base, 2, encodeMail(m))
	}
	base = pbAppendVarint(base, 3, uint64(pages))
	s.sendRawPush(ch, protocol.OpM2C_ReceiveMail, base)
	s.pushBagSnapshot(ch)
	if currencies.exp != 0 {
		s.pushPlayerProgress(ch)
	}
	if currencies.coin != 0 || currencies.yuanBao != 0 || currencies.voucher != 0 || starCoinBalance(ss) != starBefore {
		s.pushMoney(ch)
	}
	s.saveData(ch)
	log.Printf("[S=%d] receive mail %d items=%d", ch.id, mail.Id, len(mail.Items))
	return nil
}

// onDeleteMail：20285 → 20286。删除单封邮件，回剩余整页列表。
func (s *Server) onDeleteMail(ch *channel, req *protocol.C2M_DeleteMail) proto.Message {
	if ch == nil || ch.session == nil {
		return &protocol.M2C_DeleteMail{RpcId: req.RpcId, Error: errNotLogged, Message: "请先登录"}
	}
	ss := ch.session
	if pruneExpiredMails(ss, time.Now()) {
		s.saveData(ch)
	}
	for i, m := range ss.mails {
		if m != nil && m.Id == req.MailId {
			ss.mails = append(ss.mails[:i], ss.mails[i+1:]...)
			break
		}
	}
	base, err := proto.Marshal(&protocol.M2C_DeleteMail{RpcId: req.RpcId})
	if err != nil {
		return &protocol.M2C_DeleteMail{RpcId: req.RpcId, Error: errBadParam, Message: "序列化失败"}
	}
	start, end, pages := mailPageInfo(int32(len(ss.mails)), req.Page)
	for _, m := range ss.mails[start:end] {
		base = pbAppendBytes(base, 1, encodeMail(m))
	}
	base = pbAppendVarint(base, 2, uint64(pages))
	s.sendRawPush(ch, protocol.OpM2C_DeleteMail, base)
	s.saveData(ch)
	log.Printf("[S=%d] delete mail %d remain=%d", ch.id, req.MailId, len(ss.mails))
	return nil
}

// onReceiveAllMail：20287 → 20288。一键领取是整批原子操作；任一附件放不下
// 时所有邮件都保持未领取，客户端收到“背包已满”。
func (s *Server) onReceiveAllMail(ch *channel, req *protocol.C2M_ReceiveAllMail) proto.Message {
	if ch == nil || ch.session == nil {
		return &protocol.M2C_ReceiveAllMail{RpcId: req.RpcId, Error: errNotLogged, Message: "请先登录"}
	}
	ss := ch.session
	starBefore := starCoinBalance(ss)
	if pruneExpiredMails(ss, time.Now()) {
		s.saveData(ch)
	}
	var pending []*mailMsg
	for _, m := range ss.mails {
		if m == nil || m.State == 1 {
			continue
		}
		pending = append(pending, m)
	}
	staged, currencies, ok := stageMailRewards(ss, pending)
	if !ok {
		log.Printf("[S=%d] receive all mail rejected: bag full pending=%d", ch.id, len(pending))
		return &protocol.M2C_ReceiveAllMail{RpcId: req.RpcId, Message: "背包已满"}
	}
	delta := currencyDelta{coin: currencies.coin, yuanBao: currencies.yuanBao, voucher: currencies.voucher}
	if !ss.canApplyCurrencyDelta(delta) {
		return &protocol.M2C_ReceiveAllMail{RpcId: req.RpcId, Message: "邮件货币已达上限"}
	}
	ss.bag = staged
	ss.exp += currencies.exp
	s.applyCurrencyDelta(ch, delta)
	for _, m := range pending {
		m.State = 1
	}
	base, err := proto.Marshal(&protocol.M2C_ReceiveAllMail{RpcId: req.RpcId})
	if err != nil {
		return &protocol.M2C_ReceiveAllMail{RpcId: req.RpcId, Error: errBadParam, Message: "序列化失败"}
	}
	start, end, pages := mailPageInfo(int32(len(ss.mails)), 1)
	for _, m := range ss.mails[start:end] {
		base = pbAppendBytes(base, 1, encodeMail(m))
	}
	base = pbAppendVarint(base, 2, uint64(pages))
	s.sendRawPush(ch, protocol.OpM2C_ReceiveAllMail, base)
	s.pushBagSnapshot(ch)
	if currencies.exp != 0 {
		s.pushPlayerProgress(ch)
	}
	if currencies.coin != 0 || currencies.yuanBao != 0 || currencies.voucher != 0 || starCoinBalance(ss) != starBefore {
		s.pushMoney(ch)
	}
	s.saveData(ch)
	log.Printf("[S=%d] receive all mail claimed=%d total=%d", ch.id, len(pending), len(ss.mails))
	return nil
}

// onDeleteAllMail：20289 → 20290。一键清空全部邮件，MailCount=0（空 MailList 省略）。
func (s *Server) onDeleteAllMail(ch *channel, req *protocol.C2M_DeleteAllMail) proto.Message {
	if ch == nil || ch.session == nil {
		return &protocol.M2C_DeleteAllMail{RpcId: req.RpcId, Error: errNotLogged, Message: "请先登录"}
	}
	ss := ch.session
	if pruneExpiredMails(ss, time.Now()) {
		s.saveData(ch)
	}
	ss.mails = nil
	base, err := proto.Marshal(&protocol.M2C_DeleteAllMail{RpcId: req.RpcId, MailCount: 0})
	if err != nil {
		return &protocol.M2C_DeleteAllMail{RpcId: req.RpcId, Error: errBadParam, Message: "序列化失败"}
	}
	s.sendRawPush(ch, protocol.OpM2C_DeleteAllMail, base)
	s.saveData(ch)
	log.Printf("[S=%d] delete all mail", ch.id)
	return nil
}
