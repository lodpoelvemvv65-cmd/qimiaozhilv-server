package main

import (
	"errors"

	json "github.com/goccy/go-json"
	"mhqserver/protocol"
)

// gm_control_equip_gem.go：GM「宝石镶嵌」action（player.equip_gem）。
//
// 为什么和 player.equip_manual 分开：宝石是装备上一个独立的操作面，不隶属「手工打造」。
// 给一件普通装备换宝石不该被迫走一遍星级 / 随机属性 / 洗练词缀的校验，也不该因为
// 「星级 N 需要 N 条随机属性」而被连带拒绝。所以这里是一个只有宝石的独立 action，
// 界面上也是角色详情页里和「调整装备」「手工装备」并列的独立入口。
//
// 规则实现**不另起一套**：直接复用 gmResolveManualGems，也就是 gem.go 的
// gemConfig / equipAllowsGem / equipMaxHole 三件套。因此「手工装备那边能镶的宝石，
// 这里也能镶；手工那边报的错，这里报的是同一句话」，两处永远不会判定不一致。
//
// 语义是「设置这件装备宝石槽的最终状态」，不是「模拟一次镶嵌动作」：
//
//   - 提交的是**定长** EquipBase.MaxHole 项（空槽填 0），既能镶嵌、也能替换、
//     也能整件卸下（全填 0），由调用方一次性表达结果；
//   - 不扣金币、不消耗背包里的宝石——这是 GM 直接改存档状态，不是玩法镶嵌流程
//     （玩法侧收费与扣料在 gem.go 的镶嵌入口，本 action 不经过它）；
//   - 装备处于锁定（IsLock）状态时整单拒绝，与玩法侧「装备已锁定，无法镶嵌」一致；
//   - 校验全部通过后才赋值，被拒时宝石槽一个字节都不动。
//
// 与 player.equip_adjust 的 clearGems 的关系：那个是「卸下并回背包」（会往背包里发
// 宝石），本 action 只清空槽位、不返还物品。两者语义不同，都需要时分别使用。

type gmEquipGemPayload struct {
	ServerID  *string  `json:"serverId"`
	Location  *string  `json:"location"`
	SlotIndex *string  `json:"slotIndex"`
	Gems      *[]int32 `json:"gems"`
}

func init() {
	registerGMAction("player.equip_gem", func(s *Server, playerID int64, raw json.RawMessage) (string, map[string]any, string, string) {
		return s.gmAdjustEquipGem(playerID, raw)
	})
}

func (s *Server) gmAdjustEquipGem(playerID int64, raw json.RawMessage) (string, map[string]any, string, string) {
	var payload gmEquipGemPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "rejected", nil, "invalid_payload", "宝石镶嵌参数错误"
	}
	ch, release, err := s.gmResolveSession(playerID)
	if err != nil {
		return "rejected", nil, "player_not_found", "角色不存在"
	}
	defer release()
	ss := ch.session
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	before, after, err := applyGMEquipGemAdjust(ss, payload)
	if err != nil {
		return "rejected", nil, "invalid_equip", err.Error()
	}
	if err := s.persistGMSession(ch); err != nil {
		return "rejected", nil, "persist_failed", "宝石镶嵌变更保存失败"
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

// applyGMEquipGemAdjust 是纯内存实现：先跑完全部校验，任何一颗宝石非法就整单拒绝，
// 绝不留下「镶了一半」的装备。校验通过后一次性覆盖 GemList。
func applyGMEquipGemAdjust(ss *session, payload gmEquipGemPayload) (map[string]any, map[string]any, error) {
	if ss == nil {
		return nil, nil, errors.New("角色状态不可用")
	}
	item, location, slot, err := gmFindEquip(ss, gmEquipPayload{
		ServerID: payload.ServerID, Location: payload.Location, SlotIndex: payload.SlotIndex,
	})
	if err != nil {
		return nil, nil, err
	}
	if payload.Gems == nil {
		return nil, nil, errors.New("请至少选择宝石孔（全部留空表示卸下所有宝石）")
	}
	before := gmEquipManualSnapshot(item, location, slot)
	// 复用 gmResolveManualGems：宝石规则只有这一份实现，包装成手工装备的 payload 形状
	// 传进去，避免把同一套 gemConfig/equipAllowsGem/equipMaxHole 判定抄第二遍。
	// 它同时负责「装备已锁定」「该装备没有宝石槽」「长度必须正好 MaxHole 项」
	// 「该部位不能镶嵌这种宝石」「同一属性不能镶嵌两颗」全部判定。
	gems, _, err := gmResolveManualGems(gmEquipManualPayload{Gems: payload.Gems}, item)
	if err != nil {
		return nil, nil, err
	}
	item.GemList = gems
	return before, gmEquipManualSnapshot(item, location, slot), nil
}
