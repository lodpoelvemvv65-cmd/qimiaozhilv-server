package main

import (
	"log"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// M2C_GetAddEnergyPrice.Price is formatted by the client as Price/10000.
// All coin values in the server are copper units, so 100 displayed gold is
// 1,000,000 copper. Keep the preview and deduction tied to these constants.
const (
	energyPurchasePrice  int64 = 100 * 10000
	energyPurchaseAmount int32 = 100
	energyMaximum        int32 = 10000
)

// misc.go：杂项人物/体力/签到/改名/传送 处理器（20316-20332, 20357-20364, 20411-20412, 20419-20423）。
// 这些模块大多只有简单查询/应答，客户端以 Message 判成功，此处做合理实现。

// ===================== 人物信息 / 改名 =====================

// 20357 → 20358：查询任意玩家基础信息（查看其他玩家面板用）。
func (s *Server) onGetUserInfo(ch *channel, req *protocol.C2M_GetUserInfo) proto.Message {
	resp := &protocol.M2C_GetUserInfo{RpcId: req.RpcId}
	if ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	target := s.findChannelByPlayerID(req.Id)
	if target == nil {
		resp.Error, resp.Message = errBadParam, "玩家不在线"
		return resp
	}
	resp.Name = target.session.name
	resp.Level = target.session.level
	resp.JobId = target.session.jobID
	log.Printf("[S=%d] get user info id=%d -> %q lv=%d job=%d", ch.id, req.Id, resp.Name, resp.Level, resp.JobId)
	return resp
}

// 20363 → 20364：修改昵称。
func (s *Server) onChangeNickName(ch *channel, req *protocol.C2M_ChangeNickName) proto.Message {
	resp := &protocol.M2C_ChangeNickName{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Message = "请先登录"
		return resp
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		resp.Message = "昵称不能为空"
		return resp
	}
	if len([]rune(name)) > 12 {
		resp.Message = "昵称过长"
		return resp
	}
	if !s.spendVoucher(ch, 1000) {
		resp.Message = "代金券不足，需要1000代金券"
		return resp
	}
	ch.session.name = name
	s.saveData(ch)
	s.pushUnitCharacter(ch)
	s.broadcastPlayerUpdate(ch)
	log.Printf("[S=%d] change nick name -> %q", ch.id, name)
	return resp
}

// 20411 → 20412：按名字查玩家 Id（响应无 Id 字段，客户端仅判 Message）。
func (s *Server) onGetUIDByName(ch *channel, req *protocol.C2M_GetUIDByName) proto.Message {
	resp := &protocol.M2C_GetUIDByName{RpcId: req.RpcId}
	if req.Name == "" {
		resp.Error, resp.Message = errBadParam, "名字为空"
		return resp
	}
	for _, ch2 := range s.onlineChannels() {
		if ch2.session.name == req.Name {
			log.Printf("[S=%d] get uid by name %q -> %d", ch.id, req.Name, ch2.session.playerID)
			return resp
		}
	}
	resp.Error, resp.Message = errBadParam, "玩家不在线"
	return resp
}

// 20426 → 20427：查看他人背包/穿戴（BagMapList tag1 全量，含装备与物品）。
func (s *Server) onGetOtherUserBag(ch *channel, req *protocol.C2M_GetOtherUserBag) proto.Message {
	resp := &protocol.M2C_GetOtherUserBag{RpcId: req.RpcId}
	if req.UserId == 0 {
		resp.Error, resp.Message = errBadParam, "参数错误"
		return resp
	}
	var bag map[int32]*bagItem
	if rc := s.findChannelByPlayerID(req.UserId); rc != nil && rc.session != nil {
		bag = rc.session.bag
	} else {
		var bagJSON string
		if err := s.store.db.QueryRow(`SELECT bag_json FROM players WHERE id = ?`, req.UserId).Scan(&bagJSON); err != nil {
			resp.Error, resp.Message = errBadParam, "玩家不存在"
			return resp
		}
		bag = bagFromJSON(bagJSON)
	}
	base, err := proto.Marshal(resp)
	if err != nil {
		return resp
	}
	base = encodeBagMapList(base, 1, bagPairs(bag))
	s.sendRawPush(ch, protocol.OpM2C_GetOtherUserBag, base)
	log.Printf("[S=%d] get other user bag userId=%d items=%d", ch.id, req.UserId, len(bag))
	return nil
}

// ===================== 体力 =====================

// 20316 → 20317：体力购买价格。
func (s *Server) onGetAddEnergyPrice(ch *channel, req *protocol.C2M_GetAddEnergyPrice) proto.Message {
	resp := &protocol.M2C_GetAddEnergyPrice{RpcId: req.RpcId}
	resp.Price = int32(energyPurchasePrice)
	resp.Energy = energyPurchaseAmount
	return resp
}

// 20318 → 20319：购买体力。
func (s *Server) onAddEnergy(ch *channel, req *protocol.C2M_AddEnergy) proto.Message {
	resp := &protocol.M2C_AddEnergy{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Message = "请先登录"
		return resp
	}
	if ch.session.energy >= energyMaximum {
		resp.Message = "体力已满"
		return resp
	}
	if !s.spendCoin(ch, energyPurchasePrice) {
		resp.Message = "铜币不足"
		return resp
	}
	ch.session.energy += energyPurchaseAmount
	if ch.session.energy > energyMaximum {
		ch.session.energy = energyMaximum
	}
	s.pushPlayerProgress(ch)
	s.saveData(ch)
	log.Printf("[S=%d] add energy +%d price=%d -> %d", ch.id, energyPurchaseAmount, energyPurchasePrice, ch.session.energy)
	return resp
}

// ===================== 地图传送 / 回城 =====================

// 20331 → 20332：地图传送（MapIndex 为传送档位，见 12 文档 §4.12）。
// 简化实现：MapIndex=0 回主城；其余成功空应答（客户端按需自行处理）。
func (s *Server) onTransferRequest(ch *channel, req *protocol.C2M_TransferRequest) proto.Message {
	resp := &protocol.M2C_TransferRequest{RpcId: req.RpcId}
	if ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	if req.MapIndex == 0 && ch.session.mapID/100 != 10004 {
		x, y := mainCityReturnSpawn()
		s.changeMap(ch, 10004, x, y)
	}
	log.Printf("[S=%d] transfer request index=%d", ch.id, req.MapIndex)
	return resp
}

// ===================== 签到（20419-20423） =====================

// signinDailyRewardRow resolves the row the unmodified client can actually
// render. The bundled daily table ends on 2026-01-01, so dates outside that
// range reuse the newest online row with the same month/day. This keeps the
// config id, item id and amount inside the client's own datatable instead of
// inventing a server-only reward.
func signinDailyRewardRow(now time.Time) (map[string]interface{}, int32, bool) {
	if tables == nil {
		return nil, 0, false
	}
	year, month, day := int32(now.Year()), int32(now.Month()), int32(now.Day())
	var exact map[string]interface{}
	var template map[string]interface{}
	templateYear := int32(-1)
	var nearest map[string]interface{}
	nearestDistance := int32(32)
	nearestYear := int32(-1)
	for _, row := range tables.signInReward {
		rowYear := int32(num(row["Year"]))
		rowMonth := int32(num(row["Month"]))
		rowDay := int32(num(row["Day"]))
		if rowMonth != month {
			continue
		}
		if rowYear == year && rowDay == day {
			exact = row
			break
		}
		if rowDay == day && rowYear > templateYear {
			template, templateYear = row, rowYear
		}
		distance := rowDay - day
		if distance < 0 {
			distance = -distance
		}
		if distance < nearestDistance || distance == nearestDistance && rowYear > nearestYear {
			nearest, nearestDistance, nearestYear = row, distance, rowYear
		}
	}
	row := exact
	if row == nil {
		row = template
	}
	// The online table starts at 2025-01-03. This only applies to a date such
	// as January 2 that has no same-day template row at all.
	if row == nil {
		row = nearest
	}
	if row == nil {
		return nil, 0, false
	}
	id := int32(num(row["_id"]))
	return row, id, id > 0
}

// signinMonthRewardRow applies the same year-template rule as the live server to the
// cumulative rewards. The stock UI has no rows for 2026 and therefore cannot
// display those buttons without a client datatable update, but the server still
// validates requests and persists state correctly for every configured id.
func signinMonthRewardRow(now time.Time, configID int32) (map[string]interface{}, bool) {
	if tables == nil || configID <= 0 {
		return nil, false
	}
	year, month := int32(now.Year()), int32(now.Month())
	templateYear := int32(-1)
	for _, row := range tables.signInRewardMonth {
		if int32(num(row["Month"])) != month {
			continue
		}
		rowYear := int32(num(row["Year"]))
		if rowYear == year {
			templateYear = year
			break
		}
		if rowYear > templateYear {
			templateYear = rowYear
		}
	}
	if templateYear < 0 {
		return nil, false
	}
	for _, row := range tables.signInRewardMonth {
		if int32(num(row["_id"])) == configID &&
			int32(num(row["Year"])) == templateYear &&
			int32(num(row["Month"])) == month {
			return row, true
		}
	}
	return nil, false
}

func signinGrants(row map[string]interface{}) []bagGrant {
	grants := make([]bagGrant, 0)
	for _, entry := range arrOf(row["RewardsArr"]) {
		reward, _ := entry.(map[string]interface{})
		if reward == nil {
			continue
		}
		itemID := int32(num(reward["_Id"]))
		count := int32(num(reward["Count"]))
		if itemID > 0 && count > 0 {
			grants = append(grants, bagGrant{itemID: itemID, count: count})
		}
	}
	return grants
}

func normalizeSigninMonth(state *signinState, now time.Time) bool {
	month := now.Format("200601")
	changed := false
	if state.LastMonth != month {
		state.LastMonth = month
		state.MonthCount = 0
		changed = true
	}
	if state.MonthGotMonth != month {
		state.MonthGotMonth = month
		state.MonthGotIDs = nil
		changed = true
	}
	return changed
}

func signinMonthRewardTimes(configID int32) (int32, bool) {
	if tables == nil {
		return 0, false
	}
	for _, row := range tables.signInRewardMonth {
		if int32(num(row["_id"])) == configID {
			times := int32(num(row["Times"]))
			return times, times > 0
		}
	}
	return 0, false
}

// pushActiveInfo：推送 M2C_SendActiveInfo(20423) 刷新活动（签到）面板状态。
func (s *Server) pushActiveInfo(ch *channel) {
	if ch == nil || ch.session == nil {
		return
	}
	ss := ch.session
	if ss.signin == nil {
		ss.signin = &signinState{}
	}
	now := time.Now().UTC()
	_, cur, _ := signinDailyRewardRow(now)
	if normalizeSigninMonth(ss.signin, now) {
		s.saveData(ch)
	}
	got := ss.signin.LastDay == now.Format("20060102")
	// M2C_SendActiveInfo：SigninCurrId=1 / IsGetSininReward=2 / ★gotSignInMonthList=3
	// (List<int>, 字段级 ProtoMember，生成代码不含→手工编码) / SigninInMonthCount=4。
	// 客户端 ActiveComponent.gotSigninMonthTimesList 据此判"本月累计奖励已领取"。
	base, err := proto.Marshal(&protocol.M2C_SendActiveInfo{
		SigninCurrId:       cur,
		IsGetSininReward:   got,
		SigninInMonthCount: ss.signin.MonthCount,
		ActorId:            ss.playerID,
	})
	if err != nil {
		return
	}
	for _, id := range ss.signin.MonthGotIDs {
		if times, ok := signinMonthRewardTimes(id); ok {
			base = pbAppendVarint(base, 3, uint64(times))
		}
	}
	s.sendRawPush(ch, protocol.OpM2C_SendActiveInfo, base)
}

// 20419 → 20420：今日签到领奖。
func (s *Server) onGetSigninReward(ch *channel, req *protocol.C2M_GetSigninReward) proto.Message {
	resp := &protocol.M2C_GetSigninReward{RpcId: req.RpcId}
	ss := ch.session
	if ss.signin == nil {
		ss.signin = &signinState{}
	}
	now := time.Now().UTC()
	normalizeSigninMonth(ss.signin, now)
	day := now.Format("20060102")
	if ss.signin.LastDay == day {
		resp.Message = "今日已签到"
		return resp
	}
	row, currentID, ok := signinDailyRewardRow(now)
	if !ok || req.ConfigId != currentID {
		resp.Message = "签到配置无效"
		return resp
	}
	grants := signinGrants(row)
	if len(grants) == 0 {
		resp.Message = "签到奖励配置错误"
		return resp
	}
	staged, _, ok := stageBagGrants(ss, grants)
	if !ok {
		resp.Message = "背包已满"
		return resp
	}
	ss.bag = staged
	ss.signin.LastDay = day
	ss.signin.MonthCount++
	s.pushBagSnapshot(ch)
	s.saveData(ch)
	s.pushActiveInfo(ch)
	log.Printf("[S=%d] signin reward monthCount=%d", ch.id, ss.signin.MonthCount)
	return resp
}

// 20421 → 20422：本月累计签到奖励。
// 按请求 configId 精确发放（客户端按 SignInRewardMonth._id 逐档点领）；
// 每档只可领一次（signinState.MonthGotIDs 记录），跨月重置。
func (s *Server) onGetSigninInMonthReward(ch *channel, req *protocol.C2M_GetSigninInMonthReward) proto.Message {
	resp := &protocol.M2C_GetSigninInMonthReward{RpcId: req.RpcId}
	ss := ch.session
	if ss.signin == nil {
		ss.signin = &signinState{}
	}
	now := time.Now().UTC()
	normalizeSigninMonth(ss.signin, now)
	month := now.Format("200601")
	// 已领记录去重
	gotIDs := ss.signin.MonthGotIDs
	if ss.signin.MonthGotMonth != month {
		gotIDs = nil
	}
	for _, id := range gotIDs {
		if id == req.ConfigId {
			resp.Message = "该档奖励已领取"
			return resp
		}
	}
	row, ok := signinMonthRewardRow(now, req.ConfigId)
	if !ok {
		resp.Message = "累计签到奖励不存在"
		return resp
	}
	if int32(num(row["Times"])) > ss.signin.MonthCount {
		resp.Message = "签到次数不足"
		return resp
	}
	grants := signinGrants(row)
	if len(grants) == 0 {
		resp.Message = "累计签到奖励配置错误"
		return resp
	}
	staged, _, ok := stageBagGrants(ss, grants)
	if !ok {
		resp.Message = "背包已满"
		return resp
	}
	ss.bag = staged
	ss.signin.MonthGotMonth = month
	ss.signin.MonthGotIDs = gotIDs
	ss.signin.MonthGotIDs = append(ss.signin.MonthGotIDs, req.ConfigId)
	s.pushBagSnapshot(ch)
	s.saveData(ch)
	s.pushActiveInfo(ch)
	log.Printf("[S=%d] signin month reward config=%d granted", ch.id, req.ConfigId)
	return resp
}
