package main

import (
	"errors"
	"math"

	json "github.com/goccy/go-json"
	"mhqserver/protocol"
)

// 装备星级与强化等级的线上上限。星级上限取 internal/operationsconfig 的
// manualStarMax=20，强化上限取 equip_crafting.go onStrengthEquip 的 20 级封顶。
const (
	gmEquipStarMax     = 20
	gmEquipStrengthMax = 20
)

func init() {
	registerGMAction("player.equip_adjust", func(s *Server, playerID int64, raw json.RawMessage) (string, map[string]any, string, string) {
		return s.gmAdjustEquip(playerID, raw)
	})
}

type gmEquipPayload struct {
	ServerID  *string `json:"serverId"`
	Location  *string `json:"location"`
	SlotIndex *string `json:"slotIndex"`
	Strength  *string `json:"strength"`
	Star      *string `json:"star"`
	IsLocked  *bool   `json:"isLocked"`
	ClearGems *bool   `json:"clearGems"`
}

func (s *Server) gmAdjustEquip(playerID int64, raw json.RawMessage) (string, map[string]any, string, string) {
	var payload gmEquipPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "rejected", nil, "invalid_payload", "装备参数错误"
	}
	ch, release, err := s.gmResolveSession(playerID)
	if err != nil {
		return "rejected", nil, "player_not_found", "角色不存在"
	}
	defer release()
	ss := ch.session
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	before, after, err := applyGMEquipAdjust(ss, payload)
	if err != nil {
		return "rejected", nil, "invalid_equip", err.Error()
	}
	if err := s.persistGMSession(ch); err != nil {
		return "rejected", nil, "persist_failed", "装备变更保存失败"
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

func applyGMEquipAdjust(ss *session, payload gmEquipPayload) (map[string]any, map[string]any, error) {
	if ss == nil {
		return nil, nil, errors.New("角色状态不可用")
	}
	item, location, slot, err := gmFindEquip(ss, payload)
	if err != nil {
		return nil, nil, err
	}
	if payload.Strength == nil && payload.Star == nil && payload.IsLocked == nil && (payload.ClearGems == nil || !*payload.ClearGems) {
		return nil, nil, errors.New("请至少填写强化、星级、锁定或卸宝石")
	}
	before := gmEquipSnapshot(item, location, slot)
	// 强化 0..20、星级 0..20 与线上上限一致（internal/operationsconfig/manual_equip.go
	// 的 manualStarMax=20、equip_crafting.go:245 的 20 级封顶）。这里过去卡强化 >=1、
	// 星级 <=15，既改不回 0 也够不到线上的 20 星。
	strength, strengthSet, err := parseGMOptionalInt32(payload.Strength, "强化等级", 0, gmEquipStrengthMax)
	if err != nil {
		return nil, nil, err
	}
	star, starSet, err := parseGMOptionalInt32(payload.Star, "星级", 0, gmEquipStarMax)
	if err != nil {
		return nil, nil, err
	}
	if payload.ClearGems != nil && *payload.ClearGems {
		if err := gmClearEquipGems(ss, item, slot, location); err != nil {
			return nil, nil, err
		}
		if location == itemLocationBag {
			item = ss.bag[slot]
			if item == nil {
				return nil, nil, errors.New("装备数据异常")
			}
		}
	}
	if strengthSet {
		item.Level = strength
	}
	if starSet {
		item.Star = star
		if star == 0 {
			item.AddAttrs = nil
		} else {
			ensureEquipmentAffixCount(item)
		}
	}
	if payload.IsLocked != nil {
		item.IsLock = *payload.IsLocked
	}
	return before, gmEquipSnapshot(item, location, slot), nil
}

func gmFindEquip(ss *session, payload gmEquipPayload) (*bagItem, int, int32, error) {
	serverID, hasID, err := parseGMOptionalInt64(payload.ServerID, "装备实例", 1, math.MaxInt64)
	if err != nil {
		return nil, 0, 0, err
	}
	location, hasLoc, err := parseGMOptionalInt32(payload.Location, "位置", 1, 3)
	if err != nil {
		return nil, 0, 0, err
	}
	slot, hasSlot, err := parseGMOptionalInt32(payload.SlotIndex, "槽位", 0, math.MaxInt32)
	if err != nil {
		return nil, 0, 0, err
	}
	if hasID {
		for loc, items := range gmEquipMaps(ss) {
			for index, item := range items {
				if item != nil && item.ServerId == serverID {
					if item.ItemType != 1 && !itemIsEquip(item.ItemId) {
						return nil, 0, 0, errors.New("只能调整装备")
					}
					return item, loc, index, nil
				}
			}
		}
		return nil, 0, 0, errors.New("装备不存在")
	}
	if !hasLoc || !hasSlot {
		return nil, 0, 0, errors.New("请填写装备实例或位置")
	}
	items := gmEquipMaps(ss)[int(location)]
	item := items[slot]
	if item == nil {
		return nil, 0, 0, errors.New("装备不存在")
	}
	if item.ItemType != 1 && !itemIsEquip(item.ItemId) {
		return nil, 0, 0, errors.New("只能调整装备")
	}
	return item, int(location), slot, nil
}

func gmEquipMaps(ss *session) map[int]map[int32]*bagItem {
	if ss.bag == nil {
		ss.bag = map[int32]*bagItem{}
	}
	if ss.worn == nil {
		ss.worn = map[int32]*bagItem{}
	}
	if ss.store == nil {
		ss.store = map[int32]*bagItem{}
	}
	return map[int]map[int32]*bagItem{itemLocationBag: ss.bag, itemLocationWorn: ss.worn, itemLocationStore: ss.store}
}

func gmClearEquipGems(ss *session, item *bagItem, slot int32, location int) error {
	if item == nil {
		return errors.New("装备不存在")
	}
	grants := make([]bagGrant, 0)
	for _, gemID := range item.GemList {
		if gemID > 0 {
			grants = append(grants, bagGrant{itemID: gemID, count: 1, source: "GM 卸宝石"})
		}
	}
	if len(grants) == 0 {
		return nil
	}
	staged, _, ok := stageBagGrants(ss, grants)
	if !ok {
		return errors.New("背包空间不足，无法卸下宝石")
	}
	ss.bag = staged
	target := item
	if location == itemLocationBag {
		target = ss.bag[slot]
	}
	if target == nil {
		return errors.New("装备数据异常")
	}
	for i := range target.GemList {
		target.GemList[i] = 0
	}
	return nil
}

func gmEquipSnapshot(it *bagItem, location int, slot int32) map[string]any {
	if it == nil {
		return nil
	}
	gems := make([]int32, 0, len(it.GemList))
	for _, gemID := range it.GemList {
		if gemID > 0 {
			gems = append(gems, gemID)
		}
	}
	return map[string]any{
		"serverId": it.ServerId, "itemId": it.ItemId, "location": location, "slotIndex": slot,
		"strength": it.Level, "star": it.Star, "isLocked": it.IsLock, "gems": gems,
	}
}
