package main

import (
	"errors"
	"fmt"

	json "github.com/goccy/go-json"
	"mhqserver/protocol"
)

// gm_control_equip_manual.go：GM「手工装备」action（player.equip_manual）。
//
// 与 gm_control_equip.go 的 player.equip_adjust 是两件事：equip_adjust 只改
// 强化/星级/锁定/卸宝石，星级还卡在 15；这个 action 让运营一次性设置一件装备的
// 品质、星级、随机属性（ManulEquipAttribute id）、洗练词缀（EquipAffixConfig id）、
// 强化等级和宝石槽。
//
// 全部规则都从玩法实现里**原样复用**（gemConfig/equipAllowsGem/equipMaxHole、
// affixPoolForQuality/equipmentBonusCount/affixIsSixDimension），不另起一套判定，
// 保证「GM 能设出来的状态」= 「服务端认的状态」。
//
// 四条硬约束（都是为了让重登后一个字段都不变）：
//
//  1. 星级 ↔ 随机属性条数强制联动：星级 N 就必须正好 N 条。RandomAttrs 不受登录
//     修复影响（ensureEquipmentVariation 只管 MainAttr），这条纯靠 GM 侧把关。
//  2. 洗练词缀只能落成三种稳定态：空 / 正好 equipmentBonusCount(品质) 条 /
//     1 条六维。原因是 equip.go ensureEquipmentAffixCount 会**裁剪并补齐**：
//     不在 affixPoolForQuality(品质) 里的 id 被丢掉，条数不足时还会从池子里
//     按序补满。所以「1 条普通词缀」这种中间态重登必被改写成 2~3 条。
//  3. GemList 必须按 EquipBase.MaxHole 定长（空槽 0），否则客户端宝石槽显示错乱。
//  4. 星级下限是 1 而不是 0。equip.go repairBagItem 对 `Star == 0` 的装备会无条件
//     补回 `EquipBase.Star`（原版表里 996 行装备的 Star 全部 ≥ 1），而 repairBagItem
//     在每次 encodeEquipTrans（即每一次背包推送）和登录修复里都会跑。所以星级 0 不是
//     一个能存住的状态：GM 设成 0，下一次推送就被改回基础星级，只剩随机属性被清空，
//     反而留下「星级 N 却有 0 条随机属性」这种自相矛盾的存档。这里直接拒绝 0 并说明原因。
//
// 与 equip_adjust 的另一处刻意差异：这里**不**把星级和词缀绑在一起。
// equip_adjust 在 star == 0 时会顺手清空 AddAttrs（gm_control_equip.go 里旧实现的
// 耦合），本 action 里词缀只由 addAttrs 字段自己表达，星级和词缀是解耦的。
//
// GM 镶嵌不扣金币、不消耗背包宝石——这个 action 是「直接设置装备最终状态」，
// 不模拟一次真实的镶嵌流程，面板上需要明示这一点。

const (
	gmManualQualityMin = 1
	gmManualQualityMax = 6
	// gmManualStarMin 是 1 而不是 0：见文件头第 4 条，0 会被 repairBagItem 补回基础星级。
	gmManualStarMin = 1
)

func init() {
	registerGMAction("player.equip_manual", func(s *Server, playerID int64, raw json.RawMessage) (string, map[string]any, string, string) {
		return s.gmAdjustEquipManual(playerID, raw)
	})
}

// gmEquipManualPayload 全部字段都用指针，以区分「没提交」和「提交了 0 / 空数组」。
// RandomAttrs/AddAttrs/Gems 用 *[]int32：JSON null 是没提交，[] 是清空。
type gmEquipManualPayload struct {
	ServerID    *string  `json:"serverId"`
	Location    *string  `json:"location"`
	SlotIndex   *string  `json:"slotIndex"`
	Star        *string  `json:"star"`
	Quality     *string  `json:"quality"`
	Strength    *string  `json:"strength"`
	RandomAttrs *[]int32 `json:"randomAttrs"`
	AddAttrs    *[]int32 `json:"addAttrs"`
	Gems        *[]int32 `json:"gems"`
}

func gmEquipManualHasField(payload gmEquipManualPayload) bool {
	return payload.Star != nil || payload.Quality != nil || payload.Strength != nil ||
		payload.RandomAttrs != nil || payload.AddAttrs != nil || payload.Gems != nil
}

func (s *Server) gmAdjustEquipManual(playerID int64, raw json.RawMessage) (string, map[string]any, string, string) {
	var payload gmEquipManualPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "rejected", nil, "invalid_payload", "手工装备参数错误"
	}
	ch, release, err := s.gmResolveSession(playerID)
	if err != nil {
		return "rejected", nil, "player_not_found", "角色不存在"
	}
	defer release()
	ss := ch.session
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	before, after, err := applyGMEquipManualAdjust(ss, payload)
	if err != nil {
		return "rejected", nil, "invalid_equip", err.Error()
	}
	if err := s.persistGMSession(ch); err != nil {
		return "rejected", nil, "persist_failed", "手工装备变更保存失败"
	}
	if ch.conn != nil {
		s.pushBagSnapshot(ch)
		if body, encErr := encodePutOnResponse(ss, 0, ""); encErr == nil {
			s.sendRawPush(ch, protocol.OpM2C_PutOn, body)
		}
		s.pushPlayerAttrs(ch)
		s.pushUnitCharacter(ch)
	}
	return "completed", map[string]any{"before": before, "after": after}, "", ""
}

// applyGMEquipManualAdjust 是纯内存实现：先跑完全部校验，任何一个字段非法就整单
// 拒绝，绝不留下「改了一半」的装备；全部通过后才统一赋值。
func applyGMEquipManualAdjust(ss *session, payload gmEquipManualPayload) (map[string]any, map[string]any, error) {
	if ss == nil {
		return nil, nil, errors.New("角色状态不可用")
	}
	item, location, slot, err := gmFindEquip(ss, gmEquipPayload{
		ServerID: payload.ServerID, Location: payload.Location, SlotIndex: payload.SlotIndex,
	})
	if err != nil {
		return nil, nil, err
	}
	if !gmEquipManualHasField(payload) {
		return nil, nil, errors.New("请至少填写星级、品质、强化、随机属性、洗练词缀或宝石")
	}
	before := gmEquipManualSnapshot(item, location, slot)

	star, starSet, err := parseGMOptionalInt32(payload.Star, "星级", gmManualStarMin, gmEquipStarMax)
	if err != nil {
		return nil, nil, err
	}
	quality, qualitySet, err := parseGMOptionalInt32(payload.Quality, "装备品质", gmManualQualityMin, gmManualQualityMax)
	if err != nil {
		return nil, nil, err
	}
	strength, strengthSet, err := parseGMOptionalInt32(payload.Strength, "强化等级", 0, gmEquipStrengthMax)
	if err != nil {
		return nil, nil, err
	}
	// 随机属性的档位、词缀池、词缀槽位都随品质走，所以要先算出改完之后的品质再校验。
	nextStar, nextQuality := item.Star, item.Quality
	if starSet {
		nextStar = star
	}
	if qualitySet {
		nextQuality = quality
	}

	randomAttrs, randomSet, err := gmResolveManualRandomAttrs(payload, nextStar, nextQuality)
	if err != nil {
		return nil, nil, err
	}
	// 星级被改动时才强制核对条数，否则会留下「星级 N 却有 M 条」的自相矛盾存档。
	// 只核对“星级确实变了”的情况：玩法侧新发放的装备天生就是 EquipBase.Star 星级 +
	// 0 条随机属性（repairBagItem 只补 Star，不补 RandomAttrs），那是正常状态；
	// 若无条件核对，仅改强化或仅改宝石都会被误拒。
	if !randomSet && starSet && star != item.Star && int32(len(item.RandomAttrs)) != nextStar {
		return nil, nil, fmt.Errorf("星级 %d 需要 %d 条随机属性，装备当前有 %d 条，请一并提交随机属性",
			nextStar, nextStar, len(item.RandomAttrs))
	}
	addAttrs, affixSet, err := gmResolveManualAddAttrs(payload, nextQuality)
	if err != nil {
		return nil, nil, err
	}
	gems, gemsSet, err := gmResolveManualGems(payload, item)
	if err != nil {
		return nil, nil, err
	}

	// 全部校验通过，开始赋值。
	if qualitySet {
		item.Quality = quality
	}
	if starSet {
		item.Star = star
	}
	if strengthSet {
		item.Level = strength
	}
	if randomSet {
		item.RandomAttrs = randomAttrs
	}
	if affixSet {
		item.AddAttrs = addAttrs
	}
	if gemsSet {
		item.GemList = gems
	}
	return before, gmEquipManualSnapshot(item, location, slot), nil
}

// gmResolveManualRandomAttrs 校验随机属性。
//
// 返回 (ids, true, nil) 表示「按 ids 覆写」；返回 (nil, false, nil) 表示「本次没提交
// 随机属性，保持原值」——由调用方再核对现有条数是否与星级一致。星级下限是 1
// （见文件头第 4 条），所以空列表永远不是合法提交值：星级 N 就必须正好 N 条。
func gmResolveManualRandomAttrs(payload gmEquipManualPayload, star, quality int32) ([]int32, bool, error) {
	if payload.RandomAttrs == nil {
		return nil, false, nil
	}
	ids := *payload.RandomAttrs
	if int32(len(ids)) != star {
		return nil, false, fmt.Errorf("星级 %d 需要 %d 条随机属性，当前提交了 %d 条", star, star, len(ids))
	}
	if quality < gmManualQualityMin || quality > gmManualQualityMax {
		return nil, false, fmt.Errorf("装备品质必须在 %d 到 %d 之间才能设置随机属性（当前 %d）",
			gmManualQualityMin, gmManualQualityMax, quality)
	}
	if tables == nil || tables.manulEquipAttribute == nil {
		return nil, false, errors.New("属性配置未加载，无法校验随机属性")
	}
	seen := make(map[int32]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			return nil, false, fmt.Errorf("随机属性重复：%d", id)
		}
		seen[id] = true
		row := tables.manulEquipAttribute[int64(id)]
		if row == nil || num(row["Key"]) <= 0 {
			return nil, false, fmt.Errorf("随机属性 %d 不在属性表里", id)
		}
		// ManulEquipAttribute 用 101..126、201..226 … 601..626 分档，档位由品质唯一决定。
		if id/100 != quality {
			return nil, false, fmt.Errorf("随机属性 %d 不属于 %d 档属性池（品质 %d），请从该品质的候选里选",
				id, quality, quality)
		}
	}
	return gmCopyInt32s(ids), true, nil
}

// gmResolveManualAddAttrs 校验洗练词缀。只放行三种重登后不会被
// ensureEquipmentAffixCount 改写的状态，理由见文件头注释。
func gmResolveManualAddAttrs(payload gmEquipManualPayload, quality int32) ([]int32, bool, error) {
	if payload.AddAttrs == nil {
		return nil, false, nil
	}
	ids := *payload.AddAttrs
	if len(ids) == 0 {
		return nil, true, nil
	}
	if tables == nil || tables.equipAffix == nil {
		return nil, false, errors.New("词缀配置未加载，无法校验洗练词缀")
	}
	seen := make(map[int32]bool, len(ids))
	sixDimension := 0
	for _, id := range ids {
		if seen[id] {
			return nil, false, fmt.Errorf("洗练词缀重复：%d", id)
		}
		seen[id] = true
		if tables.equipAffix[int64(id)] == nil {
			return nil, false, fmt.Errorf("洗练词缀 %d 不在词缀表里", id)
		}
		if affixIsSixDimension(id) {
			sixDimension++
		}
	}
	if sixDimension > 0 {
		// 含六维时 ensureEquipmentAffixCount 整件跳过，所以只要条数对就能稳定。
		if len(ids) != 1 {
			return nil, false, fmt.Errorf("六维词缀只能 1 条，当前提交了 %d 条", len(ids))
		}
		return gmCopyInt32s(ids), true, nil
	}
	if quality < gmManualQualityMin || quality > gmManualQualityMax {
		return nil, false, fmt.Errorf("装备品质必须在 %d 到 %d 之间才能设置洗练词缀（当前 %d）",
			gmManualQualityMin, gmManualQualityMax, quality)
	}
	pool := affixPoolForQuality(quality)
	if len(pool) == 0 {
		return nil, false, fmt.Errorf("品质 %d 没有可用的词缀池，无法设置洗练词缀", quality)
	}
	target := equipmentBonusCount(quality)
	if target > len(pool) {
		target = len(pool)
	}
	if len(ids) != target {
		return nil, false, fmt.Errorf("品质 %d 的洗练词缀只能为空或正好 %d 条，当前提交了 %d 条（不足会被服务端按词缀池补齐，多余会被裁掉）",
			quality, target, len(ids))
	}
	valid := make(map[int32]bool, len(pool))
	for _, candidate := range pool {
		valid[candidate.id] = true
	}
	for _, id := range ids {
		if !valid[id] {
			return nil, false, fmt.Errorf("洗练词缀 %d 不在品质 %d 的词缀池内，重登会被服务端改写", id, quality)
		}
	}
	return gmCopyInt32s(ids), true, nil
}

// gmResolveManualGems 逐槽执行 gem.go 的同一套镶嵌规则，并把结果定长成
// EquipBase.MaxHole 个槽位（空槽 0），否则客户端宝石槽显示错乱。
func gmResolveManualGems(payload gmEquipManualPayload, item *bagItem) ([]int32, bool, error) {
	if payload.Gems == nil {
		return nil, false, nil
	}
	if item.IsLock {
		return nil, false, errors.New("装备已锁定，无法镶嵌")
	}
	maxHole := int(equipMaxHole(item.ItemId))
	if maxHole <= 0 {
		return nil, false, errors.New("该装备没有宝石槽")
	}
	gems := *payload.Gems
	if len(gems) != maxHole {
		return nil, false, fmt.Errorf("该装备有 %d 个宝石槽，必须正好提交 %d 项（空槽填 0），当前提交了 %d 项",
			maxHole, maxHole, len(gems))
	}
	slotsByKey := make(map[int32]int, maxHole)
	for slot, gemID := range gems {
		if gemID <= 0 {
			continue
		}
		cfg := gemConfig(gemID)
		if cfg == nil {
			return nil, false, fmt.Errorf("物品 %d 不是宝石，只能放入宝石！", gemID)
		}
		if !equipAllowsGem(item.ItemId, cfg) {
			return nil, false, fmt.Errorf("该部位不能镶嵌 %d 这种宝石！", gemID)
		}
		// GemKey 才是具体属性；GemType 只是客户端展示的宝石大类，同大类不同属性不冲突。
		key := int32(num(cfg["GemKey"]))
		if prev, ok := slotsByKey[key]; ok {
			return nil, false, fmt.Errorf("该装备已镶嵌相同属性宝石，请更换其他属性！（第 %d 与第 %d 个宝石孔同为属性 %d）",
				prev+1, slot+1, key)
		}
		slotsByKey[key] = slot
	}
	out := make([]int32, maxHole)
	copy(out, gems)
	return out, true, nil
}

// gmEquipManualSnapshot 是 before/after 用的完整快照。通用 gmEquipSnapshot 只覆盖
// 强化/星级/锁定/宝石，这里要额外带上品质、随机属性和词缀，否则面板看不到
// 「改到一半是什么状态」。
func gmEquipManualSnapshot(it *bagItem, location int, slot int32) map[string]any {
	if it == nil {
		return nil
	}
	return map[string]any{
		"serverId": it.ServerId, "itemId": it.ItemId, "location": location, "slotIndex": slot,
		"quality": it.Quality, "star": it.Star, "strength": it.Level, "isLocked": it.IsLock,
		"randomAttrs": gmCopyInt32s(it.RandomAttrs),
		"addAttrs":    gmCopyInt32s(it.AddAttrs),
		// gems 按槽位原样返回（含 0 空槽），前端要按孔位渲染。
		"gems": gmCopyInt32s(it.GemList),
	}
}

// gmCopyInt32s 返回一份非 nil 的副本：空列表要序列化成 []，不能是 null，
// 否则前端 `.map` 会炸。
func gmCopyInt32s(source []int32) []int32 {
	out := make([]int32, 0, len(source))
	return append(out, source...)
}
