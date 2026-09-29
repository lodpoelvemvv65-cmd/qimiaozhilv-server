package main

import (
	"fmt"
	"log"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// skill.go：技能系统（20227-20250 段）。
//
// 技能数据模型：
//   session.skills    skillID → 等级（已学技能）
//   session.skillOrder 技能栏顺序（= 已学顺序，槽位按此渲染，SlotId=槽序号）
//   session.autoSkills 自动施放技能列表（仅含已学技能）
//
// 新号只有职业基础攻击（baseSkillOfJob），其余技能通过 C2M_LearnSkill 学习/升级，
// 学习校验按 SkillConfig.level-0 行的 PreSkillId（前置）/ MaxLevel（等级上限）。

// jobSkillMap 各职业全部可学技能（SkillLogicConfig 基础 ID）。
// 基础攻击按职业区分：Officer=100001、Sportsman=200001、Nurse=300001、Superman=400001；
// 前缀 11/21/31/41 对应四大职业技能；各职业另有 2 个大招（12/22/32/42 前缀）。
var jobSkillMap = map[protocol.JobType][]int32{
	protocol.JobType_Officer: {
		100001, 110101, 110201, 110202, 110301, 110302, 110303, 110304,
		110401, 110402, 110403, 110404, 110501, 110502, 110503, 110504,
		110601, 110602, 110603, 110604, 120101, 120201,
	},
	protocol.JobType_Sportsman: {
		200001, 210101, 210201, 210202, 210301, 210302, 210303, 210304,
		210401, 210402, 210403, 210404, 210501, 210502, 210503, 210504,
		210601, 210602, 210603, 210604, 220101, 220201,
	},
	protocol.JobType_Nurse: {
		300001, 310101, 310201, 310202, 310301, 310302, 310303, 310304,
		310401, 310402, 310403, 310404, 310501, 310502, 310503, 310504,
		310601, 310602, 310603, 310604, 320101, 320201,
	},
	protocol.JobType_Superman: {
		400001, 410101, 410201, 410202, 410301, 410302, 410303, 410304,
		410401, 410402, 410403, 410404, 410501, 410502, 410503, 410504,
		410601, 410602, 410603, 410604, 420101, 420201,
	},
}

// baseSkillOfJob 返回职业基础攻击技能 ID（新号唯一已学技能）。
func baseSkillOfJob(jobID int32) int32 {
	switch protocol.JobType(jobTypeOf(jobID)) {
	case protocol.JobType_Sportsman:
		return 200001
	case protocol.JobType_Nurse:
		return 300001
	case protocol.JobType_Superman:
		return 400001
	default:
		return 100001 // Officer / 未知职业兜底
	}
}

// jobSkillIDs 返回职业全部可学技能列表（无此职业时兜底 Officer）。
func jobSkillIDs(jobID int32) []int32 {
	if ids, ok := jobSkillMap[protocol.JobType(jobTypeOf(jobID))]; ok {
		return ids
	}
	return jobSkillMap[protocol.JobType_Officer]
}

// isJobSkill 判断技能是否属于该职业。
func isJobSkill(jobID, skillID int32) bool {
	for _, id := range jobSkillIDs(jobID) {
		if id == skillID {
			return true
		}
	}
	return false
}

// skillConfigBase 读技能 level-0 配置（含 PreSkillId / MaxLevel）。
func skillConfigBase(skillID int32) (map[string]interface{}, bool) {
	if tables == nil {
		return nil, false
	}
	cfg, ok := tables.skillConfig[int64(skillID)*100]
	return cfg, ok
}

// skillMaxLevel 返回技能等级上限（SkillConfig level-0 行 MaxLevel）。
func skillMaxLevel(skillID int32) int32 {
	if cfg, ok := skillConfigBase(skillID); ok {
		if ml := int32(num(cfg["MaxLevel"])); ml > 0 {
			return ml
		}
	}
	return 1
}

// skillPreSkillId 返回技能学习前置技能 ID（level-0 行 PreSkillId，0=无前置）。
func skillPreSkillId(skillID int32) int32 {
	if cfg, ok := skillConfigBase(skillID); ok {
		return int32(num(cfg["PreSkillId"]))
	}
	return 0
}

// skillInfoList 生成技能面板列表：职业【全部】技能（已学带等级，未学 SkillLevel=0）。
// 客户端 SkillUI.AwakeAsync 遍历 SkillInfoLsit 渲染技能槽：已学的显示等级，
// 未学的（SkillLevel=0）显示"学习"按钮 → 点击发 C2M_LearnSkill。
// 之前只返回已学技能 → 客户端只有普通攻击一个槽 → 未学技能不显示 → "学不了"。
func (s *session) skillInfoList() []*protocol.SkillInfo {
	ids := jobSkillIDs(s.jobID)
	out := make([]*protocol.SkillInfo, 0, len(ids))
	for _, id := range ids {
		lv := int32(0)
		if v, ok := s.skills[id]; ok {
			lv = v
		}
		out = append(out, &protocol.SkillInfo{SkillId: id, SkillLevel: lv})
	}
	return out
}

// learnedSkillInfoList 只返回已学技能（按 skillOrder 顺序；主界面技能槽用）。
func (s *session) learnedSkillInfoList() []*protocol.SkillInfo {
	out := make([]*protocol.SkillInfo, 0, len(s.skillOrder))
	for _, id := range s.skillOrder {
		out = append(out, &protocol.SkillInfo{SkillId: id, SkillLevel: s.skills[id]})
	}
	return out
}

// mainUISlotList 由 mainui.go 实现：玩家自定义快捷栏（技能槽 DropSkill / 物品槽 DropItem）。

// saveSkills 将当前技能状态写入 DB。
func (s *Server) saveSkills(ch *channel) {
	if ch.session == nil || ch.session.playerID <= 0 {
		return
	}
	s.saveData(ch)
}

// 20227 → 20228：主 UI 设置查询。返回玩家自定义快捷栏（mainui.go 实现）。
// 20229 → 20230：把技能拖到快捷栏槽（mainui.go 实现：DropSkill{SlotId, SkillId} 设置槽）。

// 20239 → 20240：技能查询。返回职业全部技能（未学 SkillLevel=0 → 客户端显示学习按钮）。
func (s *Server) onGetSkill(ch *channel, req *protocol.C2M_GetSkill) proto.Message {
	ch.session.battleMu.Lock()
	defer ch.session.battleMu.Unlock()
	resp := &protocol.M2C_GetSkill{
		RpcId:         req.RpcId,
		SkillInfoLsit: ch.session.skillInfoList(),
		AutoSkillList: ch.session.autoSkills,
	}
	log.Printf("[S=%d] get skill job=%d skills=%d auto=%d",
		ch.id, ch.session.jobID, len(resp.SkillInfoLsit), len(resp.AutoSkillList))
	return resp
}

// 20241 → 20242：保存自动施放技能。仅保留已学技能，写 DB。
func (s *Server) onSaveAutoSkill(ch *channel, req *protocol.C2M_SaveAutoSkill) proto.Message {
	ch.session.battleMu.Lock()
	defer ch.session.battleMu.Unlock()
	resp := &protocol.M2C_SaveAutoSkill{RpcId: req.RpcId}
	ch.session.autoSkills = filterLearnedSkills(req.AutoSkillList, ch.session.skills)
	s.saveSkills(ch)
	log.Printf("[S=%d] save auto skill list=%v", ch.id, ch.session.autoSkills)
	return resp
}

// 20243 → 20244：学习 / 升级技能。
// 线上（SkillLearn 表，反汇编 SkillUI 无客户端校验，纯服务器判定）：
//   - 首次学习：扣技能点 1，不扣技能书。
//   - 已学技能升级：不扣技能点，只扣 SkillLearn 当前等级行配置的技能书。
//   - LearnLevel：目标等级行的总等级门槛；终极被动依次为 1/6001/13001/21501。
//   - 技能书在 MultiShop（多商店 Type4，14001-14006，铜币购买）/ FamilyShop 有售。
//
// ⚠ 客户端健壮性限制（Player.log 实证）：
//   - 客户端 Session.Call 对 Error!=0 的响应直接抛 Rpc error → SkillUI.cs:225 崩溃掉线。
//     因此【任何校验失败必须返回成功响应（Error=0），状态不变】，客户端不崩。
//   - 技能书（20216-20219/20221）在 MaterialBase（ItemType 3，背包可显示），
//     扣书后推 M2C_SendBag(20260) 刷新背包。
func (s *Server) onLearnSkill(ch *channel, req *protocol.C2M_LearnSkill) proto.Message {
	ch.session.battleMu.Lock()
	defer ch.session.battleMu.Unlock()
	ss := ch.session
	resp := &protocol.M2C_LearnSkill{
		RpcId:         req.RpcId,
		SkillInfoLsit: ss.skillInfoList(),
		AutoSkillList: append([]int32(nil), ss.autoSkills...),
	}
	sid := req.SkillId

	if !isJobSkill(ss.jobID, sid) {
		log.Printf("[S=%d] learn rejected: not job skill %d", ch.id, sid)
		resp.Message = "该技能不属于当前职业"
		return resp // 失败不返回 Error（客户端会崩），通过 Message 提示且状态不变
	}
	cur, learned := ss.skills[sid]
	maxLvl := skillMaxLevel(sid)
	if learned && cur >= maxLvl {
		log.Printf("[S=%d] learn rejected: %d already max lv%d", ch.id, sid, maxLvl)
		resp.Message = "技能已达到最高等级"
		return resp
	}
	if !learned {
		if pre := skillPreSkillId(sid); pre != 0 {
			if _, ok := ss.skills[pre]; !ok {
				log.Printf("[S=%d] learn rejected: %d prereq %d not learned", ch.id, sid, pre)
				resp.Message = "请先学习前置技能"
				return resp
			}
		}
	}
	target := cur + 1
	if required := skillRequiredLevel(sid, target); required > 0 && ss.level < required {
		log.Printf("[S=%d] learn skill %d -> lv%d rejected: level=%d need=%d",
			ch.id, sid, target, ss.level, required)
		resp.Message = fmt.Sprintf("需要等级%d", required)
		return resp
	}
	if !learned && ss.skillPoint <= 0 {
		log.Printf("[S=%d] learn skill %d -> lv%d rejected: no skillPoint", ch.id, sid, target)
		resp.Message = "技能点不足"
		return resp
	}
	if learned {
		// SkillLearn 的等级后缀表示升级前的当前等级：01 用于 1->2，
		// 02 用于 2->3，...；00 行不在首次学习时消费。
		materialLevel := cur
		if mat, missing, ok := missingSkillLearnMaterial(ss, sid, materialLevel); ok {
			resp.Message = fmt.Sprintf("缺少%s x%d", skillLearnMaterialName(mat.itemID), missing)
			log.Printf("[S=%d] upgrade skill %d -> lv%d rejected: missing item=%d count=%d",
				ch.id, sid, target, mat.itemID, missing)
			return resp
		}
		if !s.takeSkillLearnMaterials(ss, sid, materialLevel) {
			resp.Message = "技能升级材料不足"
			log.Printf("[S=%d] upgrade skill %d -> lv%d rejected: material deduction failed", ch.id, sid, target)
			return resp
		}
	}

	if learned {
		ss.skills[sid] = target
		log.Printf("[S=%d] upgrade skill %d -> lv%d cost configured books only",
			ch.id, sid, ss.skills[sid])
	} else {
		ss.skills[sid] = 1
		ss.skillOrder = append(ss.skillOrder, sid)
		ss.skillPoint--
		log.Printf("[S=%d] learn skill %d cost 1 skillPoint", ch.id, sid)
	}
	s.saveSkills(ch)
	s.saveData(ch)
	if learned {
		s.pushBagSnapshot(ch) // 升级扣书后刷新背包
	} else {
		s.pushUnitCharacter(ch) // 首次学习扣点后刷新角色面板
	}

	resp.SkillInfoLsit = ss.skillInfoList()
	resp.AutoSkillList = append([]int32(nil), ss.autoSkills...)
	log.Printf("[S=%d] learn skill %d -> lv%d learned=%d",
		ch.id, sid, ss.skills[sid], len(ss.skills))
	return resp
}

// skillRequiredLevel reads the target SkillConfig level row. Missing
// LearnLevel is the protobuf/JSON default 0 and therefore imposes no gate.
func skillRequiredLevel(skillID, targetLv int32) int32 {
	if tables == nil || targetLv <= 0 {
		return 0
	}
	row := tables.skillConfig[int64(skillID)*100+int64(targetLv)]
	if row == nil {
		return 0
	}
	return int32(num(row["LearnLevel"]))
}

// missingSkillLearnMaterial returns the first configured material shortage.
// The check is separate from deduction so a failed request never consumes a
// partial set of books.
func missingSkillLearnMaterial(ss *session, skillID, targetLv int32) (skillLearnMaterial, int32, bool) {
	if ss == nil {
		return skillLearnMaterial{}, 0, true
	}
	for _, mat := range skillLearnMaterials(skillID, targetLv) {
		have := ss.bagCountOf(mat.itemID)
		if have < mat.count {
			return mat, mat.count - have, true
		}
	}
	return skillLearnMaterial{}, 0, false
}

func skillLearnMaterialName(itemID int32) string {
	if tables != nil {
		if row, ok := tables.materialBase[int64(itemID)]; ok {
			if name, ok := row["Name"].(string); ok && strings.TrimSpace(name) != "" {
				return strings.TrimSpace(name)
			}
		}
	}
	return fmt.Sprintf("物品%d", itemID)
}

// takeSkillLearnMaterials：检查并扣除技能书材料（SkillLearn.MaterialArr）。
// 背包任一书不足 → 不扣任何书返回 false（调用方静默拒绝）；无配置 → 免费返回 true。
func (s *Server) takeSkillLearnMaterials(ss *session, skillID, targetLv int32) bool {
	if ss == nil {
		return false
	}
	mats := skillLearnMaterials(skillID, targetLv)
	if len(mats) == 0 {
		return true
	}
	for _, mat := range mats {
		if ss.bagCountOf(mat.itemID) < mat.count {
			return false
		}
	}
	for _, mat := range mats {
		ss.removeBagCountByItem(mat.itemID, mat.count)
	}
	return true
}

// skillLearnMaterial 一条技能书材料（SkillLearn.MaterialArr 元素）。
type skillLearnMaterial struct {
	itemID int32
	count  int32
}

// skillLearnMaterials 读 SkillLearn 表（_id=技能ID×100+升级前当前等级）的材料表。
// 线上每级 1 本对应职业技能秘籍（军官 20217 / 运动员 20219 / 护士 20216 / 超人 20218），
// 高级等级 2-3 本。行不存在返回空（调用方按配置缺失处理）。
func skillLearnMaterials(skillID, currentLv int32) []skillLearnMaterial {
	if tables == nil {
		return nil
	}
	row, ok := tables.skillLearn[int64(skillID)*100+int64(currentLv)]
	if !ok {
		return nil
	}
	var out []skillLearnMaterial
	for _, e := range arrOf(row["MaterialArr"]) {
		eo, _ := e.(map[string]interface{})
		if eo == nil {
			continue
		}
		if id := int32(num(eo["_Id"])); id > 0 {
			out = append(out, skillLearnMaterial{itemID: id, count: int32(num(eo["Count"]))})
			if out[len(out)-1].count <= 0 {
				out[len(out)-1].count = 1
			}
		}
	}
	return out
}

// consumeBagItem 跨格消耗背包物品 count 个（不足返回 false，不消耗）。
func consumeBagItem(ss *session, itemID, count int32) bool {
	if ss == nil || count <= 0 {
		return false
	}
	need := count
	// bag 是稀疏 map（key 可能不连续），必须遍历 map 而非按 1..len 扫。
	for idx, it := range ss.bag {
		if it == nil || it.ItemId != itemID {
			continue
		}
		if it.Count > need {
			it.Count -= need
			return true
		}
		need -= it.Count
		delete(ss.bag, idx)
		if need <= 0 {
			return true
		}
	}
	return false
}

// spentSkillPoints returns points consumed by learning non-basic skills.
// Upgrades consume books only, so each learned skill contributes one point.
func (s *session) spentSkillPoints() int32 {
	if s == nil {
		return 0
	}
	base := baseSkillOfJob(s.jobID)
	var spent int32
	for id, level := range s.skills {
		if id != base && level > 0 {
			spent++
		}
	}
	return spent
}

// resetSkillMaterialGrants reconstructs every book consumed to reach the
// current levels. The paid-reset client confirmation explicitly promises all
// materials back; the free-reset confirmation explicitly says none are returned.
func (s *session) resetSkillMaterialGrants() []bagGrant {
	if s == nil {
		return nil
	}
	base := baseSkillOfJob(s.jobID)
	counts := make(map[int32]int32)
	for id, level := range s.skills {
		if id == base || level <= 0 {
			continue
		}
		// First learning consumes a skill point, not SkillLearn[00]. Only
		// books consumed by upgrades are returned by the paid reset.
		for current := int32(1); current < level; current++ {
			for _, material := range skillLearnMaterials(id, current) {
				counts[material.itemID] += material.count
			}
		}
	}
	ids := make([]int32, 0, len(counts))
	for id := range counts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	grants := make([]bagGrant, 0, len(ids))
	for _, id := range ids {
		grants = append(grants, bagGrant{itemID: id, count: counts[id]})
	}
	return grants
}

// resetSkills 重置为仅基础攻击，清空自动技能。调用方负责返还技能点和持久化。
func (s *session) resetSkills() {
	base := baseSkillOfJob(s.jobID)
	s.skills = map[int32]int32{base: 1}
	s.skillOrder = []int32{base}
	s.autoSkills = nil
}

// 20245 → 20246：免费重置技能。
func (s *Server) onFreeReSetSkill(ch *channel, req *protocol.C2M_FreeReSetSkill) proto.Message {
	ch.session.battleMu.Lock()
	defer ch.session.battleMu.Unlock()
	resp := &protocol.M2C_FreeReSetSkill{RpcId: req.RpcId}
	refunded := ch.session.spentSkillPoints()
	ch.session.skillPoint += refunded
	ch.session.resetSkills()
	s.saveData(ch)
	s.pushUnitCharacter(ch)
	log.Printf("[S=%d] free reset skill ok base=%d points=%d materials=0",
		ch.id, ch.session.skillOrder[0], refunded)
	return resp
}

// 20247 → 20248：查询重置技能价格（代金券 Voucher）。
// 客户端只规定价格由响应 Voucher 给出，包内没有独立价格表；按已投入技能点计价。
func (s *Server) onGetReSetSkillPrice(ch *channel, req *protocol.C2M_GetReSetSkillPrice) proto.Message {
	ch.session.battleMu.Lock()
	defer ch.session.battleMu.Unlock()
	price := ch.session.spentSkillPoints()
	resp := &protocol.M2C_GetReSetSkillPrice{RpcId: req.RpcId, Voucher: price}
	log.Printf("[S=%d] get reset skill price voucher=%d", ch.id, price)
	return resp
}

// 20249 → 20250：付费重置技能。扣询价数目的代金券并返还全部技能书材料。
func (s *Server) onReSetSkill(ch *channel, req *protocol.C2M_ReSetSkill) proto.Message {
	ch.session.battleMu.Lock()
	defer ch.session.battleMu.Unlock()
	resp := &protocol.M2C_ReSetSkill{RpcId: req.RpcId}
	ss := ch.session
	price := ss.spentSkillPoints()
	if ss.voucher < int64(price) {
		resp.Message = fmt.Sprintf("代金券不足，需要%d", price)
		return resp
	}
	grants := ss.resetSkillMaterialGrants()
	stagedBag, _, ok := stageBagGrants(ss, grants)
	if !ok {
		resp.Message = "背包空间不足，无法返还技能材料"
		return resp
	}
	ss.voucher -= int64(price)
	ss.bag = stagedBag
	ss.skillPoint += price
	ss.resetSkills()
	s.pushMoney(ch)
	s.pushBagSnapshot(ch)
	s.pushUnitCharacter(ch)
	s.saveData(ch)
	log.Printf("[S=%d] paid reset skill ok base=%d points=%d materials=%v voucher=%d",
		ch.id, ss.skillOrder[0], price, grants, ss.voucher)
	return resp
}

// removeFromList 从切片移除指定值（保留其余顺序）。
func removeFromList(list []int32, v int32) []int32 {
	out := make([]int32, 0, len(list))
	for _, id := range list {
		if id != v {
			out = append(out, id)
		}
	}
	return out
}
