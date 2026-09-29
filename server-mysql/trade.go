package main

import (
	"fmt"
	"log"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

const (
	tradeInviteTimeout = 30 * time.Second
	tradeMaxDistance   = 6.0
	tradeMaxItems      = 8
)

type tradeOfferItem struct {
	index    int32
	count    int32
	itemID   int32
	serverID int64
	snapshot *bagItem
}

type tradeOffer struct {
	items     []tradeOfferItem
	coin      int64
	locked    bool
	confirmed bool
}

type tradeState struct {
	id         int64
	requester  *channel
	target     *channel
	createdAt  time.Time
	accepted   bool
	completing bool
	compat     bool
	offers     map[int64]*tradeOffer
}

type tradeSnapshot struct {
	id        int64
	first     *channel
	second    *channel
	accepted  bool
	compat    bool
	message   string
	firstOut  tradeOffer
	secondOut tradeOffer
}

var (
	tradeMu  sync.Mutex
	trades   = make(map[int64]*tradeState)
	tradeSeq atomic.Int64
)

func playerInBattle(ss *session) bool {
	if ss == nil {
		return false
	}
	ss.battleMu.Lock()
	inBattle := ss.battle != nil
	ss.battleMu.Unlock()
	return inBattle
}

func tradeParticipantsReady(first, second *channel) string {
	if first == nil || first.session == nil || second == nil || second.session == nil {
		return "交易对象已离线"
	}
	if !sameMapSession(first.session, second.session) {
		return "双方已不在同一场景"
	}
	if playerInBattle(first.session) || playerInBattle(second.session) {
		return "战斗中不能交易"
	}
	dx := float64(first.session.x - second.session.x)
	dy := float64(first.session.y - second.session.y)
	if math.Hypot(dx, dy) > tradeMaxDistance {
		return "距离过远，交易已取消"
	}
	return ""
}

func cloneTradeOffer(value *tradeOffer) tradeOffer {
	if value == nil {
		return tradeOffer{}
	}
	out := *value
	out.items = make([]tradeOfferItem, 0, len(value.items))
	for _, item := range value.items {
		item.snapshot = cloneBagItem(item.snapshot)
		out.items = append(out.items, item)
	}
	return out
}

func snapshotTrade(state *tradeState, message string) tradeSnapshot {
	if state == nil {
		return tradeSnapshot{message: message}
	}
	firstID, secondID := int64(0), int64(0)
	if state.requester != nil && state.requester.session != nil {
		firstID = state.requester.session.playerID
	}
	if state.target != nil && state.target.session != nil {
		secondID = state.target.session.playerID
	}
	return tradeSnapshot{
		id: state.id, first: state.requester, second: state.target,
		accepted: state.accepted, compat: state.compat, message: message,
		firstOut:  cloneTradeOffer(state.offers[firstID]),
		secondOut: cloneTradeOffer(state.offers[secondID]),
	}
}

func closeTradeLocked(state *tradeState, message string) tradeSnapshot {
	snapshot := snapshotTrade(state, message)
	if state == nil {
		return snapshot
	}
	delete(trades, state.id)
	for _, participant := range []*channel{state.requester, state.target} {
		if participant != nil && participant.session != nil && participant.session.tradeID == state.id {
			participant.session.tradeID = 0
			participant.session.tradeCompatOpen = false
			participant.session.tradeCompatClosed = state.compat && state.accepted
		}
	}
	return snapshot
}

func encodeTradeItem(item tradeOfferItem) []byte {
	var body []byte
	body = pbAppendVarint(body, 1, uint64(item.index))
	if item.snapshot != nil {
		copy := cloneBagItem(item.snapshot)
		copy.Count = item.count
		body = pbAppendBytes(body, 2, encodeBagMap(item.index, copy))
	}
	body = pbAppendVarint(body, 3, uint64(item.count))
	return body
}

func (s *Server) sendTradeStateTo(ch, other *channel, id int64, self, remote tradeOffer, open bool, message string) {
	if ch == nil || ch.session == nil {
		return
	}
	otherID := int64(0)
	if other != nil && other.session != nil {
		otherID = other.session.playerID
	}
	base, err := proto.Marshal(&protocol.M2C_TradeState{
		TradeId: id, SelfId: ch.session.playerID, OtherId: otherID,
		SelfCoin: self.coin, OtherCoin: remote.coin,
		SelfLocked: self.locked, OtherLocked: remote.locked,
		SelfConfirmed: self.confirmed, OtherConfirmed: remote.confirmed,
		Open: open, Message: message, ActorId: ch.session.playerID,
	})
	if err != nil {
		return
	}
	for _, item := range self.items {
		base = pbAppendBytes(base, 4, encodeTradeItem(item))
	}
	for _, item := range remote.items {
		base = pbAppendBytes(base, 5, encodeTradeItem(item))
	}
	s.sendRawPush(ch, protocol.OpM2C_TradeState, base)
}

func (s *Server) pushTradeSnapshot(snapshot tradeSnapshot, open bool) {
	if snapshot.compat {
		s.pushCompatTradeSnapshot(snapshot, open)
		return
	}
	s.sendTradeStateTo(snapshot.first, snapshot.second, snapshot.id,
		snapshot.firstOut, snapshot.secondOut, open, snapshot.message)
	s.sendTradeStateTo(snapshot.second, snapshot.first, snapshot.id,
		snapshot.secondOut, snapshot.firstOut, open, snapshot.message)
}

func (s *Server) cancelTradeForPlayer(playerID int64, message string) {
	if playerID <= 0 {
		return
	}
	tradeMu.Lock()
	var state *tradeState
	for _, candidate := range trades {
		if candidate.requester != nil && candidate.requester.session != nil && candidate.requester.session.playerID == playerID ||
			candidate.target != nil && candidate.target.session != nil && candidate.target.session.playerID == playerID {
			state = candidate
			break
		}
	}
	if state == nil || state.completing {
		tradeMu.Unlock()
		return
	}
	snapshot := closeTradeLocked(state, message)
	tradeMu.Unlock()
	s.pushTradeSnapshot(snapshot, false)
}

func (s *Server) expireTrade(tradeID int64) {
	tradeMu.Lock()
	state := trades[tradeID]
	if state == nil || state.accepted || state.completing || time.Since(state.createdAt) < tradeInviteTimeout {
		tradeMu.Unlock()
		return
	}
	snapshot := closeTradeLocked(state, "交易邀请已超时")
	tradeMu.Unlock()
	s.pushTradeSnapshot(snapshot, false)
}

func (s *Server) onRequestTrade(ch *channel, req *protocol.C2M_RequestTrade) proto.Message {
	return s.requestTrade(ch, req, false)
}

func (s *Server) onRequestTradeCompat(ch *channel, req *protocol.C2M_RequestTrade) proto.Message {
	return s.requestTrade(ch, req, true)
}

func (s *Server) requestTrade(ch *channel, req *protocol.C2M_RequestTrade, compat bool) proto.Message {
	resp := &protocol.M2C_RequestTrade{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID <= 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	if req.TargetId <= 0 || req.TargetId == ch.session.playerID {
		resp.Message = "交易对象无效"
		return resp
	}
	target := s.findChannelByPlayerID(req.TargetId)
	if target == nil || target.session == nil {
		resp.Message = "交易对象不在线"
		return resp
	}
	if reason := tradeParticipantsReady(ch, target); reason != "" {
		resp.Message = reason
		return resp
	}

	tradeMu.Lock()
	if ch.session.tradeID != 0 {
		tradeMu.Unlock()
		resp.Message = "你正在进行其他交易"
		return resp
	}
	if target.session.tradeID != 0 {
		tradeMu.Unlock()
		resp.Message = "对方正在进行其他交易"
		return resp
	}
	id := tradeSeq.Add(1)
	state := &tradeState{
		id: id, requester: ch, target: target, createdAt: time.Now(),
		compat: compat,
		offers: map[int64]*tradeOffer{
			ch.session.playerID:     &tradeOffer{},
			target.session.playerID: &tradeOffer{},
		},
	}
	trades[id] = state
	ch.session.tradeID = id
	target.session.tradeID = id
	ch.session.tradeCompatOpen, ch.session.tradeCompatClosed = false, false
	target.session.tradeCompatOpen, target.session.tradeCompatClosed = false, false
	tradeMu.Unlock()

	// Older clients do not contain the later trade message types. They receive
	// the same request-list envelope with a positive requester UnitId and a
	// negative ActorId marker; the client-side handler turns the displayed ID
	// negative after resolving the character, keeping it separate from teams.
	s.sendPush(target, protocol.OpM2C_RequestList, &protocol.M2C_RequestList{
		UnitId:  ch.session.playerID,
		TimeOut: int32(tradeInviteTimeout / time.Millisecond),
		ActorId: -ch.session.playerID,
	})
	time.AfterFunc(tradeInviteTimeout, func() { s.expireTrade(id) })
	log.Printf("[TRADE=%d] invite %d -> %d", id, ch.session.playerID, target.session.playerID)
	return resp
}

func (s *Server) onHandleTradeByRequester(ch *channel, requesterID int64, accept bool, rpcID int32) proto.Message {
	tradeMu.Lock()
	var tradeID int64
	for id, state := range trades {
		if state != nil && state.target == ch && state.requester != nil && state.requester.session != nil &&
			state.requester.session.playerID == requesterID {
			tradeID = id
			break
		}
	}
	tradeMu.Unlock()
	if tradeID == 0 {
		return &protocol.M2C_HandleTrade{RpcId: rpcID, Message: "交易申请已失效"}
	}
	return s.onHandleTrade(ch, &protocol.C2M_HandleTrade{TradeId: tradeID, Accept: accept, RpcId: rpcID})
}

func (s *Server) onHandleTrade(ch *channel, req *protocol.C2M_HandleTrade) proto.Message {
	resp := &protocol.M2C_HandleTrade{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID <= 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	tradeMu.Lock()
	state := trades[req.TradeId]
	if state == nil || state.target != ch || state.completing {
		tradeMu.Unlock()
		resp.Message = "交易邀请已失效"
		return resp
	}
	if time.Since(state.createdAt) >= tradeInviteTimeout {
		snapshot := closeTradeLocked(state, "交易邀请已超时")
		tradeMu.Unlock()
		s.pushTradeSnapshot(snapshot, false)
		resp.Message = "交易邀请已超时"
		return resp
	}
	if !req.Accept {
		snapshot := closeTradeLocked(state, "对方拒绝了交易")
		tradeMu.Unlock()
		s.pushTradeSnapshot(snapshot, false)
		return resp
	}
	if reason := tradeParticipantsReady(state.requester, state.target); reason != "" {
		snapshot := closeTradeLocked(state, reason)
		tradeMu.Unlock()
		s.pushTradeSnapshot(snapshot, false)
		resp.Message = reason
		return resp
	}
	state.accepted = true
	snapshot := snapshotTrade(state, "")
	tradeMu.Unlock()
	s.pushTradeSnapshot(snapshot, true)
	log.Printf("[TRADE=%d] accepted", state.id)
	return resp
}

func validateTradeOffer(ss *session, request []*protocol.TradeItem, coin int64) ([]tradeOfferItem, string) {
	if ss == nil || coin < 0 || coin > ss.coin {
		return nil, "铜币数量无效"
	}
	if len(request) > tradeMaxItems {
		return nil, "最多可放入 8 种物品"
	}
	seen := make(map[int32]bool, len(request))
	items := make([]tradeOfferItem, 0, len(request))
	for _, value := range request {
		if value == nil || value.Index < 0 || value.Index >= bagSlotCount || value.Count <= 0 || seen[value.Index] {
			return nil, "交易物品数量或位置无效"
		}
		seen[value.Index] = true
		item := ss.bag[value.Index]
		if item == nil || item.IsLock || item.Count < value.Count {
			return nil, "物品不存在、数量不足或已锁定"
		}
		if item.ItemType == int32(protocol.ItemType_EquipItem) && (item.Count != 1 || value.Count != 1) {
			return nil, "装备只能按单件交易"
		}
		copy := cloneBagItem(item)
		copy.Count = value.Count
		items = append(items, tradeOfferItem{
			index: value.Index, count: value.Count, itemID: item.ItemId,
			serverID: item.ServerId, snapshot: copy,
		})
	}
	return items, ""
}

func (s *Server) onTradeOffer(ch *channel, req *protocol.C2M_TradeOffer) proto.Message {
	resp := &protocol.M2C_TradeOffer{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID <= 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	items, reason := validateTradeOffer(ch.session, req.Items, req.Coin)
	if reason != "" {
		resp.Message = reason
		return resp
	}
	tradeMu.Lock()
	state := trades[req.TradeId]
	offer := (*tradeOffer)(nil)
	if state != nil {
		offer = state.offers[ch.session.playerID]
	}
	if state == nil || !state.accepted || state.completing || offer == nil {
		tradeMu.Unlock()
		resp.Message = "当前没有可修改的交易"
		return resp
	}
	if offer.locked {
		tradeMu.Unlock()
		resp.Message = "请先取消锁定再修改交易内容"
		return resp
	}
	offer.items = items
	offer.coin = req.Coin
	for _, participantOffer := range state.offers {
		participantOffer.locked = false
		participantOffer.confirmed = false
	}
	snapshot := snapshotTrade(state, "")
	tradeMu.Unlock()
	s.pushTradeSnapshot(snapshot, true)
	return resp
}

func (s *Server) onTradeLock(ch *channel, req *protocol.C2M_TradeLock) proto.Message {
	resp := &protocol.M2C_TradeLock{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID <= 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	tradeMu.Lock()
	state := trades[req.TradeId]
	var offer *tradeOffer
	if state != nil {
		offer = state.offers[ch.session.playerID]
	}
	if state == nil || !state.accepted || state.completing || offer == nil {
		tradeMu.Unlock()
		resp.Message = "交易已失效"
		return resp
	}
	if req.Locked {
		if reason := validateStoredTradeOffer(ch.session, offer); reason != "" {
			tradeMu.Unlock()
			resp.Message = reason
			return resp
		}
	}
	offer.locked = req.Locked
	offer.confirmed = false
	if !req.Locked {
		for _, current := range state.offers {
			current.confirmed = false
		}
	}
	if req.Locked && allTradeOffersLocked(state) {
		state.completing = true
		snapshot := snapshotTrade(state, "")
		tradeMu.Unlock()
		resp.Message = s.completeAndCloseTrade(snapshot)
		return resp
	}
	snapshot := snapshotTrade(state, "")
	tradeMu.Unlock()
	s.pushTradeSnapshot(snapshot, true)
	return resp
}

func protocolItemsFromOffer(offer *tradeOffer) []*protocol.TradeItem {
	if offer == nil {
		return nil
	}
	items := make([]*protocol.TradeItem, 0, len(offer.items))
	for _, item := range offer.items {
		items = append(items, &protocol.TradeItem{Index: item.index, Count: item.count})
	}
	return items
}

// validateStoredTradeOffer rechecks the item identity captured when an
// offer was created. A player must not be able to replace or move a bag
// instance between offering and locking.
func validateStoredTradeOffer(ss *session, offer *tradeOffer) string {
	if ss == nil || offer == nil || offer.coin < 0 || offer.coin > ss.coin {
		return "trade coin changed"
	}
	for _, value := range offer.items {
		item := ss.bag[value.index]
		if item == nil || item.IsLock || item.ItemId != value.itemID || item.ServerId != value.serverID ||
			value.count <= 0 || item.Count < value.count {
			return fmt.Sprintf("item at slot %d changed", value.index)
		}
		if item.ItemType == int32(protocol.ItemType_EquipItem) && value.count != 1 {
			return fmt.Sprintf("equipment count at slot %d is invalid", value.index)
		}
	}
	return ""
}

func removeOfferedItems(bag map[int32]*bagItem, offer tradeOffer) ([]*bagItem, error) {
	transfers := make([]*bagItem, 0, len(offer.items))
	for _, value := range offer.items {
		item := bag[value.index]
		if item == nil || item.IsLock || item.ItemId != value.itemID || item.ServerId != value.serverID ||
			value.count <= 0 || item.Count < value.count {
			return nil, fmt.Errorf("格子 %d 的物品已变化", value.index)
		}
		if item.ItemType == int32(protocol.ItemType_EquipItem) && value.count != 1 {
			return nil, fmt.Errorf("格子 %d 的装备数量无效", value.index)
		}
		transfer := cloneBagItem(item)
		transfer.Count = value.count
		transfers = append(transfers, transfer)
		item.Count -= value.count
		if item.Count == 0 {
			delete(bag, value.index)
		}
	}
	return transfers, nil
}

func addTransferredItem(bag map[int32]*bagItem, transfer *bagItem) error {
	if transfer == nil || transfer.Count <= 0 {
		return fmt.Errorf("交易物品无效")
	}
	remaining := transfer.Count
	if transfer.ItemType == int32(protocol.ItemType_EquipItem) {
		for remaining > 0 {
			index := nextBagIndex(&session{bag: bag})
			if index < 0 {
				return fmt.Errorf("背包空间不足")
			}
			item := cloneBagItem(transfer)
			item.Count = 1
			bag[index] = item
			remaining--
		}
		return nil
	}
	limit64 := rewardStackLimit(transfer.ItemId)
	if limit64 <= 0 || limit64 > int64(^uint32(0)>>1) {
		limit64 = 999
	}
	limit := int32(limit64)
	for index := int32(0); index < bagSlotCount && remaining > 0; index++ {
		item := bag[index]
		if item == nil || item.ItemId != transfer.ItemId || item.ItemType != transfer.ItemType ||
			!samePurchaseOrigin(item, transfer) || item.Count >= limit {
			continue
		}
		amount := limit - item.Count
		if amount > remaining {
			amount = remaining
		}
		item.Count += amount
		remaining -= amount
	}
	for remaining > 0 {
		index := nextBagIndex(&session{bag: bag})
		if index < 0 {
			return fmt.Errorf("背包空间不足")
		}
		amount := limit
		if amount > remaining {
			amount = remaining
		}
		item := cloneBagItem(transfer)
		item.Count = amount
		bag[index] = item
		remaining -= amount
	}
	return nil
}

func stageTradeExchange(state tradeSnapshot) (map[int32]*bagItem, int64, map[int32]*bagItem, int64, error) {
	if state.first == nil || state.first.session == nil || state.second == nil || state.second.session == nil {
		return nil, 0, nil, 0, fmt.Errorf("交易对象已离线")
	}
	first, second := state.first.session, state.second.session
	if state.firstOut.coin < 0 || state.firstOut.coin > first.coin ||
		state.secondOut.coin < 0 || state.secondOut.coin > second.coin {
		return nil, 0, nil, 0, fmt.Errorf("交易铜币数量已变化")
	}
	firstBag, secondBag := cloneBagMap(first.bag), cloneBagMap(second.bag)
	firstItems, err := removeOfferedItems(firstBag, state.firstOut)
	if err != nil {
		return nil, 0, nil, 0, err
	}
	secondItems, err := removeOfferedItems(secondBag, state.secondOut)
	if err != nil {
		return nil, 0, nil, 0, err
	}
	for _, item := range secondItems {
		if err := addTransferredItem(firstBag, item); err != nil {
			return nil, 0, nil, 0, fmt.Errorf("%s的%s", first.name, err)
		}
	}
	for _, item := range firstItems {
		if err := addTransferredItem(secondBag, item); err != nil {
			return nil, 0, nil, 0, fmt.Errorf("%s的%s", second.name, err)
		}
	}
	if state.secondOut.coin > math.MaxInt64-(first.coin-state.firstOut.coin) ||
		state.firstOut.coin > math.MaxInt64-(second.coin-state.secondOut.coin) {
		return nil, 0, nil, 0, fmt.Errorf("交易铜币溢出")
	}
	firstCoin := first.coin - state.firstOut.coin + state.secondOut.coin
	secondCoin := second.coin - state.secondOut.coin + state.firstOut.coin
	return firstBag, firstCoin, secondBag, secondCoin, nil
}

func (s *Server) completeTrade(snapshot tradeSnapshot) error {
	if reason := tradeParticipantsReady(snapshot.first, snapshot.second); reason != "" {
		return fmt.Errorf("%s", reason)
	}
	firstBag, firstCoin, secondBag, secondCoin, err := stageTradeExchange(snapshot)
	if err != nil {
		return err
	}
	first, second := snapshot.first.session, snapshot.second.session
	if err := s.store.SaveTradeExchange(first.playerID, firstCoin, firstBag,
		second.playerID, secondCoin, secondBag); err != nil {
		return fmt.Errorf("保存交易失败: %w", err)
	}
	first.bag, first.coin = firstBag, firstCoin
	second.bag, second.coin = secondBag, secondCoin
	s.pushBagSnapshot(snapshot.first)
	s.pushBagSnapshot(snapshot.second)
	s.pushMoney(snapshot.first)
	s.pushMoney(snapshot.second)
	return nil
}

func (s *Server) completeAndCloseTrade(snapshot tradeSnapshot) string {
	if err := s.completeTrade(snapshot); err != nil {
		tradeMu.Lock()
		current := trades[snapshot.id]
		closed := closeTradeLocked(current, err.Error())
		tradeMu.Unlock()
		s.pushTradeSnapshot(closed, false)
		return err.Error()
	}
	tradeMu.Lock()
	current := trades[snapshot.id]
	closed := closeTradeLocked(current, "交易完成")
	tradeMu.Unlock()
	s.pushTradeSnapshot(closed, false)
	log.Printf("[TRADE=%d] complete players=%d/%d", snapshot.id,
		snapshot.first.session.playerID, snapshot.second.session.playerID)
	return ""
}

func (s *Server) onTradeConfirm(ch *channel, req *protocol.C2M_TradeConfirm) proto.Message {
	resp := &protocol.M2C_TradeConfirm{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID <= 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	tradeMu.Lock()
	state := trades[req.TradeId]
	var offer *tradeOffer
	if state != nil {
		offer = state.offers[ch.session.playerID]
	}
	if state == nil || !state.accepted || state.completing || offer == nil {
		tradeMu.Unlock()
		resp.Message = "交易已失效"
		return resp
	}
	if req.Confirmed && (!offer.locked || !allTradeOffersLocked(state)) {
		tradeMu.Unlock()
		resp.Message = "双方锁定后才能确认交易"
		return resp
	}
	offer.confirmed = req.Confirmed
	if !allTradeOffersConfirmed(state) {
		snapshot := snapshotTrade(state, "")
		tradeMu.Unlock()
		s.pushTradeSnapshot(snapshot, true)
		return resp
	}
	state.completing = true
	snapshot := snapshotTrade(state, "")
	tradeMu.Unlock()

	resp.Message = s.completeAndCloseTrade(snapshot)
	return resp
}

func allTradeOffersLocked(state *tradeState) bool {
	if state == nil || len(state.offers) != 2 {
		return false
	}
	for _, offer := range state.offers {
		if offer == nil || !offer.locked {
			return false
		}
	}
	return true
}

func allTradeOffersConfirmed(state *tradeState) bool {
	if !allTradeOffersLocked(state) {
		return false
	}
	for _, offer := range state.offers {
		if !offer.confirmed {
			return false
		}
	}
	return true
}

func (s *Server) onTradeCancel(ch *channel, req *protocol.C2M_TradeCancel) proto.Message {
	resp := &protocol.M2C_TradeCancel{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID <= 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	tradeMu.Lock()
	state := trades[req.TradeId]
	if state == nil || state.offers[ch.session.playerID] == nil || state.completing {
		tradeMu.Unlock()
		resp.Message = "交易已失效"
		return resp
	}
	snapshot := closeTradeLocked(state, "交易已取消")
	tradeMu.Unlock()
	s.pushTradeSnapshot(snapshot, false)
	return resp
}

func (s *Server) tradeSweepLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		tradeMu.Lock()
		states := make([]*tradeState, 0, len(trades))
		for _, state := range trades {
			states = append(states, state)
		}
		tradeMu.Unlock()
		for _, state := range states {
			if state == nil || state.completing {
				continue
			}
			if !state.accepted && time.Since(state.createdAt) >= tradeInviteTimeout {
				s.expireTrade(state.id)
				continue
			}
			if state.accepted {
				if reason := tradeParticipantsReady(state.requester, state.target); reason != "" {
					pid := int64(0)
					if state.requester != nil && state.requester.session != nil {
						pid = state.requester.session.playerID
					}
					s.cancelTradeForPlayer(pid, reason)
				}
			}
		}
	}
}
