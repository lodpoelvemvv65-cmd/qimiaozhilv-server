package main

import (
	"log"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// pet.go：宠物系统（opcode 20370-20390，见 文档/17-宠物系统.md）。
//
// 状态模型（state_extra.go petState）：单宠物，PetState 0=等待 1=跟随 2=嬉戏 3=锻炼 4=探险。
// 动作时长（客户端无档位 UI，服务器自定）：嬉戏 600s / 锻炼 900s / 探险 1800s。
// 推送通道：M2C_SyncPet(20370) 是唯一状态推送，UnitId 填玩家自身单位（MyUnit 自带 Pet
// 组件，M2C_SyncPetHandler 才能拿到 unit；否则 NRE 被吞、显示不生效）。
// 约定：业务拒绝保持 Error=0 + Message（客户端弹提示），仅未登录态用 errNotLogged。

const (
	petStateIdle       = 0 // 等待
	petStateFollow     = 1 // 跟随
	petStatePlay       = 2 // 嬉戏
	petStateExperience = 3 // 锻炼
	petStateExplore    = 4 // 探险

	petFeedLimit       = 10 // 每日喂食上限（M2C_GetPetInfo.eatCount 据此扣减）
	petQuickEndVoucher = 10 // 立即完成固定代金券价格
	petPlaySeconds     = 600
	petExpSeconds      = 900
	petExploreSeconds  = 1800
)

// pushSyncPet：推送宠物状态快照（喂食/升级/开始动作/结束动作后都要调）。
func (s *Server) pushSyncPet(ch *channel) {
	if ch == nil || ch.session == nil {
		return
	}
	ss := ch.session
	if ss.playerID == 0 {
		return
	}
	if ss.pet == nil {
		ss.pet = newPet()
	}
	p := ss.pet
	s.sendPush(ch, protocol.OpM2C_SyncPet, &protocol.M2C_SyncPet{
		PetId:    p.PetId,
		Level:    p.Level,
		Exp:      p.Exp,
		Intimacy: p.Intimacy,
		Name:     p.Name,
		IsShow:   p.IsShow,
		UnitId:   ss.playerID, // 客户端从玩家单位创建跟随宠物
		Active:   p.Active,
		ActorId:  ss.playerID,
	})
}

// onShowPet：20371 切换宠物跟随显示。IActorLocationMessage 无响应，父层 fire-and-forget 分发。
func (s *Server) onShowPet(ch *channel, req *protocol.C2M_ShowPet) {
	if ch == nil || ch.session == nil {
		return
	}
	p := ch.session.pet
	if p == nil {
		p = newPet()
		ch.session.pet = p
	}
	p.IsShow = !p.IsShow
	s.pushSyncPet(ch)
	s.saveData(ch)
	log.Printf("[S=%d] show pet isShow=%v", ch.id, p.IsShow)
}

// onGetPetInfo：20375 → 20376。宠物面板 ShowPage 查询。
func (s *Server) onGetPetInfo(ch *channel, req *protocol.C2M_GetPetInfo) proto.Message {
	resp := &protocol.M2C_GetPetInfo{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	p := ch.session.pet
	if p == nil {
		p = newPet()
		ch.session.pet = p
	}
	p.petDailyReset()
	remain := int64(0)
	if p.ActionEnd > 0 {
		remain = p.ActionEnd - time.Now().Unix()
		if remain < 0 {
			remain = 0
		}
	}
	resp.EatCount = p.EatCount
	resp.Active = p.Active
	resp.PetState = p.PetState
	resp.RemainTime = remain
	return resp
}

// onStartPetPlay：20377 → 20378。开始嬉戏（600s）。
func (s *Server) onStartPetPlay(ch *channel, req *protocol.C2M_StartPetPlay) proto.Message {
	resp := &protocol.M2C_StartPetPlay{RpcId: req.RpcId}
	p := ch.session.pet
	if p == nil {
		p = newPet()
		ch.session.pet = p
	}
	if p.PetState != 0 {
		resp.Message = "宠物正在忙碌中"
		return resp
	}
	p.petDailyReset()
	p.PetState = petStatePlay
	p.ActionEnd = time.Now().Unix() + petPlaySeconds
	p.Rewarded = false
	s.pushSyncPet(ch)
	s.saveData(ch)
	log.Printf("[S=%d] pet play start end=%d", ch.id, p.ActionEnd)
	return resp
}

// onStartPetExplore：20379 → 20380。开始探险（1800s）。
func (s *Server) onStartPetExplore(ch *channel, req *protocol.C2M_StartPetExplore) proto.Message {
	resp := &protocol.M2C_StartPetExplore{RpcId: req.RpcId}
	p := ch.session.pet
	if p == nil {
		p = newPet()
		ch.session.pet = p
	}
	if p.PetState != 0 {
		resp.Message = "宠物正在忙碌中"
		return resp
	}
	p.petDailyReset()
	p.PetState = petStateExplore
	p.ActionEnd = time.Now().Unix() + petExploreSeconds
	p.Rewarded = false
	s.pushSyncPet(ch)
	s.saveData(ch)
	log.Printf("[S=%d] pet explore start end=%d", ch.id, p.ActionEnd)
	return resp
}

// onStartPetExperience：20381 → 20382。开始锻炼（900s）。
func (s *Server) onStartPetExperience(ch *channel, req *protocol.C2M_StartPetExperience) proto.Message {
	resp := &protocol.M2C_StartPetExperience{RpcId: req.RpcId}
	p := ch.session.pet
	if p == nil {
		p = newPet()
		ch.session.pet = p
	}
	if p.PetState != 0 {
		resp.Message = "宠物正在忙碌中"
		return resp
	}
	p.petDailyReset()
	p.PetState = petStateExperience
	p.ActionEnd = time.Now().Unix() + petExpSeconds
	p.Rewarded = false
	s.pushSyncPet(ch)
	s.saveData(ch)
	log.Printf("[S=%d] pet experience start end=%d", ch.id, p.ActionEnd)
	return resp
}

// onEndPetAction：20383 → 20384。立即完成（扣 10 兑换券，按完整时长结算）。
func (s *Server) onEndPetAction(ch *channel, req *protocol.C2M_EndPetAction) proto.Message {
	resp := &protocol.M2C_EndPetAction{RpcId: req.RpcId}
	p := ch.session.pet
	if p == nil {
		p = newPet()
		ch.session.pet = p
	}
	if p.PetState == 0 {
		resp.Message = "没有进行中的动作"
		return resp
	}
	if !s.spendVoucher(ch, petQuickEndVoucher) {
		resp.Message = "兑换券不足"
		return resp
	}
	s.applyPetActionReward(ch)
	p.PetState = 0
	p.ActionEnd = 0
	s.pushSyncPet(ch)
	s.saveData(ch)
	log.Printf("[S=%d] pet quick end voucher=%d", ch.id, petQuickEndVoucher)
	return resp
}

// onGetPetActionReword：20385 → 20386。领取已完成动作的奖励。
func (s *Server) onGetPetActionReword(ch *channel, req *protocol.C2M_GetPetActionReword) proto.Message {
	resp := &protocol.M2C_GetPetActionReword{RpcId: req.RpcId}
	p := ch.session.pet
	if p == nil {
		p = newPet()
		ch.session.pet = p
	}
	if p.PetState == 0 {
		resp.Message = "没有可领取的奖励"
		return resp
	}
	if p.ActionEnd > time.Now().Unix() {
		resp.Message = "动作还未完成"
		return resp
	}
	if p.Rewarded {
		resp.Message = "奖励已领取"
		return resp
	}
	s.applyPetActionReward(ch)
	p.Rewarded = true
	p.PetState = 0
	p.ActionEnd = 0
	s.pushSyncPet(ch)
	s.saveData(ch)
	log.Printf("[S=%d] pet action reward claimed", ch.id)
	return resp
}

// onGetPetQuickEndPrice：20387 → 20388。查询立即完成价格（固定 10 兑换券）。
func (s *Server) onGetPetQuickEndPrice(ch *channel, req *protocol.C2M_GetPetQuickEndPrice) proto.Message {
	return &protocol.M2C_GetPetQuickEndPrice{RpcId: req.RpcId, Voucher: petQuickEndVoucher}
}

// onUpgradePet：20389 → 20390。宠物进化（index=PetConfig.UpgradeIdArr 下标，自建服只放行 0）。
func (s *Server) onUpgradePet(ch *channel, req *protocol.C2M_UpgradePet) proto.Message {
	resp := &protocol.M2C_UpgradePet{RpcId: req.RpcId}
	p := ch.session.pet
	if p == nil {
		p = newPet()
		ch.session.pet = p
	}
	if req.Index != 0 {
		resp.Message = "参数错误"
		return resp
	}
	if tables == nil {
		resp.Message = "配置未加载"
		return resp
	}
	row := tables.petConfig[int64(p.PetId)]
	upIds := arrOf(row["UpgradeIdArr"])
	if len(upIds) == 0 {
		resp.Message = "宠物已是最高形态"
		return resp
	}
	target := int32(num(upIds[0]))
	if p.Level < int32(num(row["NeedLevel"])) || p.Intimacy < int32(num(row["NeedIntimacy"])) {
		resp.Message = "等级或亲密度不足"
		return resp
	}
	p.PetId = target
	if nr, ok := tables.petConfig[int64(target)]; ok {
		if n, ok2 := nr["Name"].(string); ok2 && n != "" {
			p.Name = n
		}
	}
	s.pushSyncPet(ch)
	s.saveData(ch)
	log.Printf("[S=%d] pet upgrade to %d", ch.id, target)
	return resp
}

// applyPetActionReward：宠物动作奖励结算（结束动作/领奖/喂食共用）。
// 经验+100、亲密度+5、活跃度+10，并按 PetLevelConfig 曲线升级：
// needExp(L) = Exp + ExpByLevel*(L-1)，封顶 MaxLevel。
func (s *Server) applyPetActionReward(ch *channel) {
	if ch == nil || ch.session == nil {
		return
	}
	ss := ch.session
	p := ss.pet
	if p == nil {
		p = newPet()
		ss.pet = p
	}
	petExp := int32(float64(100) * ss.petExperienceMultiplier())
	if petExp < 100 {
		petExp = 100
	}
	p.Exp += petExp
	p.Intimacy += 5
	p.Active += 10
	if tables == nil {
		return
	}
	if lv := tables.petLevelConfig[int64(p.PetId)]; lv != nil {
		need := int32(num(lv["Exp"])) + int32(num(lv["ExpByLevel"]))*(p.Level-1)
		maxLv := int32(num(lv["MaxLevel"]))
		for p.Level < maxLv && p.Exp >= need {
			p.Exp -= need
			p.Level++
			need = int32(num(lv["Exp"])) + int32(num(lv["ExpByLevel"]))*(p.Level-1)
		}
	}
}

// petFeed：背包 UseGoods 钩子（父层在 onUseGoods 里对宠物物品调用）。
// EffectType 15=宠物粮食（解析 OtherParam 的 exp/intimacy/active），16=宠物经验卡
// （ExpRange×ContinuedSeconds 折算经验）。返回 true 表示已按宠物物品处理（调用方
// 沿用原 20276 背包推送 + 本函数已推 20370）。
func (s *Server) petFeed(ch *channel, gb map[string]interface{}) bool {
	if ch == nil || ch.session == nil || gb == nil {
		return false
	}
	effectType := int32(num(gb["EffectType"]))
	if effectType != 15 && effectType != 16 {
		return false
	}
	ss := ch.session
	p := ss.pet
	if p == nil {
		p = newPet()
		ss.pet = p
	}
	if effectType == 16 {
		// 宠物经验卡是锻炼经验倍率，不是一次性增加几百万经验，也不占喂食次数。
		s.activateItemBuff(ch, gb)
		s.pushSyncPet(ch)
		s.saveData(ch)
		return true
	}
	p.petDailyReset()
	if p.EatCount >= petFeedLimit {
		log.Printf("[S=%d] pet feed limit reached", ch.id)
		return false
	}
	p.EatCount++
	if effectType == 15 {
		param := ""
		if s, ok := gb["OtherParam"].(string); ok {
			param = s
		}
		exp, intimacy, active := parsePetFeedParam(param)
		p.Exp += exp
		p.Intimacy += intimacy
		p.Active += active
	}
	s.applyPetActionReward(ch)
	s.pushSyncPet(ch)
	s.saveData(ch)
	return true
}

// parsePetFeedParam：解析宠物粮食 OtherParam，格式 "exp,intimacy,active"（按 ',' 分隔，
// 也兼容线上 "exp:100" / "intimacy:20" / "exp:500_active:1" 写法）。任意格式容错，
// 首段无效时经验兜底 50。
func parsePetFeedParam(s string) (exp, intimacy, active int32) {
	exp = 50
	parts := strings.Split(s, ",")
	if len(parts) > 0 {
		if v := petFeedParamValue(parts[0]); v > 0 {
			exp = v
		}
	}
	if len(parts) > 1 {
		intimacy = petFeedParamValue(parts[1])
	}
	if len(parts) > 2 {
		active = petFeedParamValue(parts[2])
	}
	return exp, intimacy, active
}

// petFeedParamValue：从单个段提取数值。取最后一个 ':' 后的数字段（截到 '_' 或非数字），
// 纯数字段原样取；无数字返回 0。
func petFeedParamValue(part string) int32 {
	part = strings.TrimSpace(part)
	if idx := strings.LastIndex(part, ":"); idx >= 0 {
		part = part[idx+1:]
		if i := strings.IndexAny(part, "_"); i >= 0 {
			part = part[:i]
		}
		part = strings.TrimSpace(part)
	}
	start := -1
	for i, r := range part {
		if r >= '0' && r <= '9' {
			start = i
			break
		}
	}
	if start < 0 {
		return 0
	}
	end := start
	for end < len(part) && part[end] >= '0' && part[end] <= '9' {
		end++
	}
	v, _ := strconv.ParseInt(part[start:end], 10, 64)
	return int32(v)
}
