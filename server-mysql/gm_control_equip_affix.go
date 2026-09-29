package main

import (
	"errors"

	json "github.com/goccy/go-json"
	"mhqserver/protocol"
)

// gm_control_equip_affix.go：GM「调整词缀」action（player.equip_affix）。
//
// 和 gm_control_equip_manual.go 的 player.equip_manual 是两件事。手工装备那条要让
// 运营一次提交星级 / 品质 / 随机属性 / 词缀 / 强化 / 宝石，任何一处不满足都会整单被拒；
// 而「给这件装备换一组洗练词缀」本身跟星级和随机属性毫无关系，不该被迫一起提交，
// 也不该被「星级 5 需要 5 条随机属性」这种联动规则拦住。宝石（player.equip_gem）
// 当初拆出去的也是同一个理由，这里是同一个处理的第三个入口。
//
// 规则本身**一份都没有重写**：直接调用 gm_control_equip_manual.go 的
// gmResolveManualAddAttrs，词缀池（affixPoolForQuality）、槽位数
// （equipmentBonusCount）、六维判定（affixIsSixDimension）都还是玩法里的那一份实现。
// 所以「GM 能设出来的词缀状态」与手工装备那条 action 完全一致。
//
// 为什么只能落成三种状态——equip.go 的 ensureEquipmentAffixCount 在**每一次**
// encodeEquipTrans（即每一次背包推送）和登录修复里都会跑，它会丢弃不在
// affixPoolForQuality(品质) 里的 id、去重，然后从池子里按序**补齐**到
// equipmentBonusCount(品质) 条：
//
//  1. 空列表：被 `len > 0` 守卫跳过，稳定；
//  2. 正好 equipmentBonusCount(品质) 条（白绿 1 / 蓝紫 2 / 橙红 3）且都在该品质
//     词缀池内：稳定；
//  3. 1 条六维词缀（含满六维）：含六维时整件跳过修复，稳定。
//
// 「红装只给 1 条普通词缀」这种中间态重登必被改写成 3 条，所以这里直接拒绝并说明原因，
// 而不是存下一个下次登录就变样的存档。
//
// 品质不由这个 action 设：词缀池和槽位数都跟着装备**当前**品质走，改品质请用
// player.equip_manual。同样地，这个 action 只碰 AddAttrs，星级 / 品质 / 随机属性 /
// 强化 / 宝石在 before/after 里逐字段不变。
//
// GM 改词缀不扣金币、不消耗洗练道具——这是「直接设置装备最终状态」，不是模拟一次洗练。

func init() {
	registerGMAction("player.equip_affix", func(s *Server, playerID int64, raw json.RawMessage) (string, map[string]any, string, string) {
		return s.gmAdjustEquipAffix(playerID, raw)
	})
}

// gmEquipAffixPayload 只用 location + slotIndex 定位（player_items 的主键），
// server_id 对新生成的装备恒为 0，不能拿来定位。AddAttrs 用指针：nil 是「没提交」，
// [] 是「清空词缀」，两者必须区分开。
type gmEquipAffixPayload struct {
	ServerID  *string  `json:"serverId"`
	Location  *string  `json:"location"`
	SlotIndex *string  `json:"slotIndex"`
	AddAttrs  *[]int32 `json:"addAttrs"`
}

func (s *Server) gmAdjustEquipAffix(playerID int64, raw json.RawMessage) (string, map[string]any, string, string) {
	var payload gmEquipAffixPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "rejected", nil, "invalid_payload", "调整词缀参数错误"
	}
	ch, release, err := s.gmResolveSession(playerID)
	if err != nil {
		return "rejected", nil, "player_not_found", "角色不存在"
	}
	defer release()
	ss := ch.session
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	before, after, err := applyGMEquipAffixAdjust(ss, payload)
	if err != nil {
		return "rejected", nil, "invalid_equip", err.Error()
	}
	if err := s.persistGMSession(ch); err != nil {
		return "rejected", nil, "persist_failed", "词缀变更保存失败"
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

func applyGMEquipAffixAdjust(ss *session, payload gmEquipAffixPayload) (map[string]any, map[string]any, error) {
	if ss == nil {
		return nil, nil, errors.New("角色状态不可用")
	}
	item, location, slot, err := gmFindEquip(ss, gmEquipPayload{
		ServerID: payload.ServerID, Location: payload.Location, SlotIndex: payload.SlotIndex,
	})
	if err != nil {
		return nil, nil, err
	}
	if payload.AddAttrs == nil {
		return nil, nil, errors.New("请提交洗练词缀（空列表表示清空词缀）")
	}
	// 校验用的是手工装备那条 action 里的同一份实现，只把 payload 包装成它认的形状。
	// 词缀池和槽位数都按装备**当前**品质算，所以传 item.Quality 而不是外部传入的品质。
	addAttrs, _, err := gmResolveManualAddAttrs(gmEquipManualPayload{AddAttrs: payload.AddAttrs}, item.Quality)
	if err != nil {
		return nil, nil, err
	}
	before := gmEquipManualSnapshot(item, location, slot)
	item.AddAttrs = addAttrs
	return before, gmEquipManualSnapshot(item, location, slot), nil
}
