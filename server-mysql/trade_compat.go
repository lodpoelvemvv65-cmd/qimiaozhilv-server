package main

import (
	"fmt"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

const (
	compatTradeColumns     = 10
	compatTradeSideColumns = 5
	compatTradeSideSlots   = 30
)

type compatTradeView struct {
	items          map[int32]*bagItem
	selfCoin       int64
	otherCoin      int64
	selfLocked     bool
	otherLocked    bool
	selfConfirmed  bool
	otherConfirmed bool
	message        string
}

func (s *Server) sendCompatTradeTip(ch *channel, message string) {
	if ch == nil || ch.session == nil || message == "" {
		return
	}
	s.sendPush(ch, protocol.OpM2C_SendTip, &protocol.M2C_SendTip{
		Message: message,
		ActorId: ch.session.playerID,
	})
}

func (s *Server) pushCompatTradeSnapshot(snapshot tradeSnapshot, open bool) {
	participants := []*channel{snapshot.first, snapshot.second}
	if open && snapshot.accepted {
		for _, participant := range participants {
			if participant == nil || participant.session == nil {
				continue
			}
			tradeMu.Lock()
			shouldOpen := !participant.session.tradeCompatOpen
			participant.session.tradeCompatOpen = true
			participant.session.tradeCompatClosed = false
			tradeMu.Unlock()
			if shouldOpen {
				s.sendPush(participant, protocol.OpM2C_OpenStoreUI, &protocol.M2C_OpenStoreUI{
					ActorId: -participant.session.playerID,
				})
			}
		}
	}

	message := snapshot.message
	if message == "" && open {
		switch {
		case snapshot.firstOut.locked && snapshot.secondOut.locked:
			message = "双方已锁定交易"
		case snapshot.firstOut.locked || snapshot.secondOut.locked:
			message = "一方已锁定交易"
		case len(snapshot.firstOut.items) > 0 || len(snapshot.secondOut.items) > 0 ||
			snapshot.firstOut.coin > 0 || snapshot.secondOut.coin > 0:
			message = "交易报价已更新"
		default:
			message = "交易已开始"
		}
	}
	for _, participant := range participants {
		s.sendCompatTradeTip(participant, message)
	}
}

func compatOfferView(offer tradeOffer, remote bool) map[int32]*bagItem {
	items := make(map[int32]*bagItem, len(offer.items))
	for index, value := range offer.items {
		if index >= compatTradeSideSlots {
			break
		}
		item := cloneBagItem(value.snapshot)
		if item == nil {
			continue
		}
		item.Count = value.count
		row := index / compatTradeSideColumns
		column := index % compatTradeSideColumns
		if remote {
			column += compatTradeSideColumns
		}
		items[int32(row*compatTradeColumns+column)] = item
	}
	return items
}

func compatTradeSnapshotView(ch *channel) (bool, compatTradeView) {
	if ch == nil || ch.session == nil {
		return false, compatTradeView{}
	}
	ss := ch.session
	tradeMu.Lock()
	defer tradeMu.Unlock()
	state := trades[ss.tradeID]
	if state == nil || !state.compat || !state.accepted {
		if ss.tradeCompatClosed {
			return true, compatTradeView{message: "交易已结束，请关闭交易界面"}
		}
		return false, compatTradeView{}
	}
	self := cloneTradeOffer(state.offers[ss.playerID])
	remote := tradeOffer{}
	for playerID, offer := range state.offers {
		if playerID != ss.playerID {
			remote = cloneTradeOffer(offer)
			break
		}
	}
	items := compatOfferView(self, false)
	for index, item := range compatOfferView(remote, true) {
		items[index] = item
	}
	return true, compatTradeView{
		items: items, selfCoin: self.coin, otherCoin: remote.coin,
		selfLocked: self.locked, otherLocked: remote.locked,
		selfConfirmed: self.confirmed, otherConfirmed: remote.confirmed,
	}
}

func appendCompatTradeView(base []byte, view compatTradeView) []byte {
	base = encodeBagMapList(base, 1, bagPairs(view.items))
	base = pbAppendVarint(base, 4, uint64(view.selfCoin))
	base = pbAppendVarint(base, 5, uint64(view.otherCoin))
	if view.selfLocked {
		base = pbAppendVarint(base, 6, 1)
	}
	if view.otherLocked {
		base = pbAppendVarint(base, 7, 1)
	}
	if view.selfConfirmed {
		base = pbAppendVarint(base, 8, 1)
	}
	if view.otherConfirmed {
		base = pbAppendVarint(base, 9, 1)
	}
	return base
}

func compatTradeOfferIndex(storeIndex int32) (int, bool) {
	if storeIndex < 0 || storeIndex >= storeSlotsPerPage {
		return 0, false
	}
	row, column := storeIndex/compatTradeColumns, storeIndex%compatTradeColumns
	if column >= compatTradeSideColumns {
		return 0, false
	}
	return int(row*compatTradeSideColumns + column), true
}

func resetTradeApprovals(state *tradeState) {
	for _, offer := range state.offers {
		if offer != nil {
			offer.locked = false
			offer.confirmed = false
		}
	}
}

func (s *Server) compatTradePutItem(ch *channel, req *protocol.C2M_PutInStore) (bool, proto.Message) {
	if ch == nil || ch.session == nil {
		return false, nil
	}
	ss := ch.session
	tradeMu.Lock()
	state := trades[ss.tradeID]
	if state == nil || !state.compat || !state.accepted {
		closed := ss.tradeCompatClosed
		tradeMu.Unlock()
		if closed {
			return true, &protocol.M2C_PutInStore{RpcId: req.RpcId, Message: "交易已结束，请关闭交易界面"}
		}
		return false, nil
	}
	response := &protocol.M2C_PutInStore{RpcId: req.RpcId}
	offer := state.offers[ss.playerID]
	if req.Page != 0 {
		tradeMu.Unlock()
		response.Message = "只能在我的报价页添加物品"
		return true, response
	}
	if offer == nil || offer.locked {
		tradeMu.Unlock()
		response.Message = "交易已锁定，不能修改报价"
		return true, response
	}
	request := protocolItemsFromOffer(offer)
	found := false
	for _, item := range request {
		if item.Index == req.BagIndex {
			item.Count += req.Count
			found = true
			break
		}
	}
	if !found {
		request = append(request, &protocol.TradeItem{Index: req.BagIndex, Count: req.Count})
	}
	items, reason := validateTradeOffer(ss, request, offer.coin)
	if reason != "" {
		tradeMu.Unlock()
		response.Message = reason
		return true, response
	}
	offer.items = items
	resetTradeApprovals(state)
	snapshot := snapshotTrade(state, "")
	tradeMu.Unlock()

	base, err := proto.Marshal(response)
	if err != nil {
		response.Message = "交易报价序列化失败"
		return true, response
	}
	_, view := compatTradeSnapshotView(ch)
	base = encodeBagMapList(base, 1, bagPairs(view.items))
	base = encodeBagMapList(base, 2, bagPairs(ss.bag))
	s.sendRawPush(ch, protocol.OpM2C_PutInStore, base)
	s.pushTradeSnapshot(snapshot, true)
	return true, nil
}

func (s *Server) compatTradeTakeItem(ch *channel, req *protocol.C2M_TakeOffStore) (bool, proto.Message) {
	if ch == nil || ch.session == nil {
		return false, nil
	}
	ss := ch.session
	tradeMu.Lock()
	state := trades[ss.tradeID]
	if state == nil || !state.compat || !state.accepted {
		closed := ss.tradeCompatClosed
		tradeMu.Unlock()
		if closed {
			return true, &protocol.M2C_TakeOffStore{RpcId: req.RpcId, Message: "交易已结束，请关闭交易界面"}
		}
		return false, nil
	}
	response := &protocol.M2C_TakeOffStore{RpcId: req.RpcId}
	offer := state.offers[ss.playerID]
	offerIndex, ownSlot := compatTradeOfferIndex(req.StoreIndex)
	if req.Page != 0 || !ownSlot || offerIndex >= len(offer.items) {
		tradeMu.Unlock()
		response.Message = "只能撤回我方报价中的物品"
		return true, response
	}
	if offer.locked {
		tradeMu.Unlock()
		response.Message = "交易已锁定，不能修改报价"
		return true, response
	}
	index := offerIndex
	if req.Count <= 0 || req.Count > offer.items[index].count {
		tradeMu.Unlock()
		response.Message = "数量无效"
		return true, response
	}
	offer.items[index].count -= req.Count
	if offer.items[index].count == 0 {
		offer.items = append(offer.items[:index], offer.items[index+1:]...)
	} else if offer.items[index].snapshot != nil {
		offer.items[index].snapshot.Count = offer.items[index].count
	}
	resetTradeApprovals(state)
	snapshot := snapshotTrade(state, "")
	tradeMu.Unlock()

	base, err := proto.Marshal(response)
	if err != nil {
		response.Message = "交易报价序列化失败"
		return true, response
	}
	base = encodeBagMapList(base, 1, bagPairs(ss.bag))
	_, view := compatTradeSnapshotView(ch)
	base = encodeBagMapList(base, 2, bagPairs(view.items))
	s.sendRawPush(ch, protocol.OpM2C_TakeOffStore, base)
	s.pushTradeSnapshot(snapshot, true)
	return true, nil
}

func (s *Server) compatTradeChangeCoin(ch *channel, rpcID int32, amount int64, add bool) (bool, int64, string) {
	if ch == nil || ch.session == nil {
		return false, 0, ""
	}
	ss := ch.session
	tradeMu.Lock()
	state := trades[ss.tradeID]
	if state == nil || !state.compat || !state.accepted {
		closed := ss.tradeCompatClosed
		tradeMu.Unlock()
		if closed {
			return true, 0, "交易已结束，请关闭交易界面"
		}
		return false, 0, ""
	}
	offer := state.offers[ss.playerID]
	if offer == nil || offer.locked {
		tradeMu.Unlock()
		return true, 0, "交易已锁定，不能修改报价"
	}
	if amount <= 0 {
		tradeMu.Unlock()
		return true, offer.coin, "数量无效"
	}
	coin := offer.coin
	if add {
		if coin < 0 || ss.coin < 0 || amount > ss.coin-coin || !canAddInt64Balance(coin, amount) {
			tradeMu.Unlock()
			return true, coin, "铜币不足"
		}
		coin += amount
	} else {
		if amount > coin {
			tradeMu.Unlock()
			return true, coin, "撤回铜币超过当前报价"
		}
		coin -= amount
	}
	offer.coin = coin
	resetTradeApprovals(state)
	snapshot := snapshotTrade(state, "")
	tradeMu.Unlock()
	s.pushTradeSnapshot(snapshot, true)
	return true, coin, ""
}

func (s *Server) compatTradeSort(ch *channel, req *protocol.C2M_SortStore) (bool, proto.Message) {
	if ch == nil || ch.session == nil {
		return false, nil
	}
	ss := ch.session
	tradeMu.Lock()
	state := trades[ss.tradeID]
	if state == nil || !state.compat || !state.accepted {
		closed := ss.tradeCompatClosed
		tradeMu.Unlock()
		if closed {
			return true, &protocol.M2C_SortStore{RpcId: req.RpcId, Message: "交易已结束，请关闭交易界面"}
		}
		return false, nil
	}
	tradeID := state.id
	offer := state.offers[ss.playerID]
	locked := offer != nil && offer.locked
	tradeMu.Unlock()

	if locked {
		return true, &protocol.M2C_SortStore{
			RpcId: req.RpcId, Message: "我方已锁定，请等待对方锁定",
		}
	}
	message := ""
	result := s.onTradeLock(ch, &protocol.C2M_TradeLock{TradeId: tradeID, Locked: true})
	if response, ok := result.(*protocol.M2C_TradeLock); ok {
		message = response.Message
	}
	response := &protocol.M2C_SortStore{RpcId: req.RpcId, Message: message}
	if message != "" {
		return true, response
	}
	_, view := compatTradeSnapshotView(ch)
	if view.message != "" {
		response.Message = "交易完成"
		return true, response
	}
	base, err := proto.Marshal(response)
	if err != nil {
		response.Message = fmt.Sprintf("交易状态序列化失败: %v", err)
		return true, response
	}
	base = encodeBagMapList(base, 1, bagPairs(view.items))
	s.sendRawPush(ch, protocol.OpM2C_SortStore, base)
	return true, nil
}

func (s *Server) compatTradeCancel(ch *channel, rpcID int32) (bool, proto.Message) {
	if ch == nil || ch.session == nil {
		return false, nil
	}
	ss := ch.session
	tradeMu.Lock()
	state := trades[ss.tradeID]
	active := state != nil && state.compat
	closed := ss.tradeCompatClosed
	tradeID := ss.tradeID
	tradeMu.Unlock()
	if !active && !closed {
		return false, nil
	}
	response := &protocol.M2C_ExtandStore{RpcId: rpcID}
	if active {
		result := s.onTradeCancel(ch, &protocol.C2M_TradeCancel{TradeId: tradeID})
		if value, ok := result.(*protocol.M2C_TradeCancel); ok {
			response.Message = value.Message
		}
	}
	tradeMu.Lock()
	ss.tradeCompatOpen = false
	ss.tradeCompatClosed = false
	tradeMu.Unlock()
	return true, response
}
