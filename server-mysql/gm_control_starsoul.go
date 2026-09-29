package main

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"strings"

	json "github.com/goccy/go-json"
)

func init() {
	registerGMAction("player.starsoul_adjust", func(s *Server, playerID int64, raw json.RawMessage) (string, map[string]any, string, string) {
		return s.gmAdjustStarSoul(playerID, raw)
	})
}

type gmStarSoulPayload struct {
	StarSoulID *string `json:"starSoulId"`
	TypeID     *string `json:"typeId"`
	PosType    *string `json:"posType"`
	Quality    *string `json:"quality"`
	Level      *string `json:"level"`
	Exp        *string `json:"exp"`
	Count      *string `json:"count"`
	IsLocked   *bool   `json:"isLocked"`
	Remove     *bool   `json:"remove"`
	Equip      *bool   `json:"equip"`
}

type gmStarSoulApplyResult struct {
	IDs   []int64
	Slots []int32
	Attrs bool
}

func (s *Server) gmAdjustStarSoul(playerID int64, raw json.RawMessage) (string, map[string]any, string, string) {
	var payload gmStarSoulPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "rejected", nil, "invalid_payload", "星魂参数错误"
	}
	if !gmStarSoulPayloadHasField(payload) {
		return "rejected", nil, "invalid_payload", "请填写星魂 ID 或发放参数"
	}
	ch, release, err := s.gmResolveSession(playerID)
	if err != nil {
		return "rejected", nil, "player_not_found", "角色不存在"
	}
	defer release()
	ss := ch.session
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	bag := ss.ensureStarSoulBag()
	before := gmStarSoulBagSnapshot(bag, nil)
	applied, err := applyGMStarSoulAdjust(bag, payload)
	if err != nil {
		return "rejected", nil, "invalid_starsoul", err.Error()
	}
	if err := s.persistGMSession(ch); err != nil {
		return "rejected", nil, "persist_failed", "星魂变更保存失败"
	}
	if ch.conn != nil {
		s.gmPushStarSoulChanges(ch, bag, applied)
	}
	return "completed", map[string]any{
		"before": before,
		"after":  gmStarSoulBagSnapshot(bag, applied.IDs),
	}, "", ""
}

func (s *Server) gmPushStarSoulChanges(ch *channel, bag *starSoulBag, applied gmStarSoulApplyResult) {
	if ch == nil || bag == nil {
		return
	}
	for _, id := range applied.IDs {
		s.pushStarSoulItem(ch, id, bag.Items[id])
	}
	seen := map[int32]bool{}
	for _, slot := range applied.Slots {
		if slot < 0 || slot >= starSoulSlotCount || seen[slot] {
			continue
		}
		seen[slot] = true
		s.pushStarSoulUsed(ch, slot)
	}
	if applied.Attrs {
		s.pushPlayerAttrs(ch)
	}
}

func gmStarSoulPayloadHasField(payload gmStarSoulPayload) bool {
	return payload.StarSoulID != nil || payload.TypeID != nil || payload.PosType != nil ||
		payload.Quality != nil || payload.Level != nil || payload.Exp != nil || payload.Count != nil ||
		payload.IsLocked != nil || payload.Remove != nil || payload.Equip != nil
}

func applyGMStarSoulAdjust(bag *starSoulBag, payload gmStarSoulPayload) (gmStarSoulApplyResult, error) {
	if bag == nil {
		return gmStarSoulApplyResult{}, errors.New("星魂背包不可用")
	}
	if bag.Items == nil {
		bag.Items = make(map[int64]*starSoulItem)
	}
	starSoulID, hasID, err := parseGMOptionalInt64(payload.StarSoulID, "星魂 ID", 1, math.MaxInt64)
	if err != nil {
		return gmStarSoulApplyResult{}, err
	}
	if hasID {
		if payload.TypeID != nil || payload.PosType != nil || payload.Quality != nil || payload.Count != nil {
			return gmStarSoulApplyResult{}, errors.New("调整已有星魂时不要填写发放参数")
		}
		return applyGMStarSoulMutate(bag, starSoulID, payload)
	}
	if payload.Remove != nil && *payload.Remove {
		return gmStarSoulApplyResult{}, errors.New("删除星魂时必须填写星魂 ID")
	}
	return applyGMStarSoulGrant(bag, payload)
}

func applyGMStarSoulGrant(bag *starSoulBag, payload gmStarSoulPayload) (gmStarSoulApplyResult, error) {
	typeID, _, err := parseGMOptionalInt32(payload.TypeID, "星魂类型", 1, math.MaxInt32)
	if err != nil {
		return gmStarSoulApplyResult{}, err
	}
	if tables == nil || tables.starSoulType[int64(typeID)] == nil {
		return gmStarSoulApplyResult{}, errors.New("星魂类型不存在")
	}
	posType, _, err := parseGMOptionalInt32(payload.PosType, "星魂部位", 0, int64(starSoulSlotCount-1))
	if err != nil {
		return gmStarSoulApplyResult{}, err
	}
	quality, _, err := parseGMOptionalInt32(payload.Quality, "星魂品质", 1, 6)
	if err != nil {
		return gmStarSoulApplyResult{}, err
	}
	count := int32(1)
	if payload.Count != nil {
		value, _, err := parseGMOptionalInt32(payload.Count, "发放数量", 1, 20)
		if err != nil {
			return gmStarSoulApplyResult{}, err
		}
		count = value
	}
	if payload.Equip != nil && *payload.Equip && count != 1 {
		return gmStarSoulApplyResult{}, errors.New("一次只能穿戴一枚新星魂")
	}
	if len(bag.Items)+int(count) > starSoulCapacity {
		return gmStarSoulApplyResult{}, errors.New("星魂背包已满")
	}
	level, levelSet, err := parseGMOptionalInt32(payload.Level, "星魂等级", 0, 20)
	if err != nil {
		return gmStarSoulApplyResult{}, err
	}
	exp, expSet, err := parseGMOptionalInt32(payload.Exp, "星魂经验", 0, math.MaxInt32)
	if err != nil {
		return gmStarSoulApplyResult{}, err
	}
	items := make([]*starSoulItem, 0, count)
	for i := int32(0); i < count; i++ {
		item := generateStarSoul(typeID, posType, quality)
		if item == nil || item.Main == 0 {
			return gmStarSoulApplyResult{}, errors.New("星魂属性配置不存在")
		}
		items = append(items, item)
	}
	result := gmStarSoulApplyResult{}
	for _, item := range items {
		if levelSet {
			item.Level = level
			applyStarSoulViceGrowth(item, rand.Intn)
		}
		if expSet {
			item.Exp = exp
			gmCapStarSoulExp(item)
		}
		bag.Items[item.ID] = item
		result.IDs = append(result.IDs, item.ID)
	}
	if payload.Equip != nil && *payload.Equip {
		if err := gmEquipStarSoul(bag, items[0], true, &result); err != nil {
			for _, item := range items {
				delete(bag.Items, item.ID)
			}
			return gmStarSoulApplyResult{}, err
		}
	}
	if payload.IsLocked != nil {
		for _, item := range items {
			item.IsLocked = *payload.IsLocked
		}
	}
	return result, nil
}

func applyGMStarSoulMutate(bag *starSoulBag, starSoulID int64, payload gmStarSoulPayload) (gmStarSoulApplyResult, error) {
	item := bag.Items[starSoulID]
	if item == nil {
		return gmStarSoulApplyResult{}, errors.New("星魂不存在")
	}
	result := gmStarSoulApplyResult{IDs: []int64{starSoulID}}
	if payload.Remove != nil && *payload.Remove {
		if item.IsUsed {
			slot := item.PosType
			if slot >= 0 && slot < starSoulSlotCount && bag.Used[slot] == item.ID {
				bag.Used[slot] = 0
				result.Slots = append(result.Slots, slot)
				result.Attrs = true
			}
		}
		delete(bag.Items, item.ID)
		return result, nil
	}
	if payload.Level == nil && payload.Exp == nil && payload.IsLocked == nil && payload.Equip == nil {
		return gmStarSoulApplyResult{}, errors.New("请至少填写等级、经验、锁定或穿戴")
	}
	level, levelSet, err := parseGMOptionalInt32(payload.Level, "星魂等级", 0, 20)
	if err != nil {
		return gmStarSoulApplyResult{}, err
	}
	exp, expSet, err := parseGMOptionalInt32(payload.Exp, "星魂经验", 0, math.MaxInt32)
	if err != nil {
		return gmStarSoulApplyResult{}, err
	}
	if payload.Equip != nil && *payload.Equip {
		if item.PosType < 0 || item.PosType >= starSoulSlotCount {
			return gmStarSoulApplyResult{}, errors.New("星魂部位无效")
		}
		if item.IsLocked && (payload.IsLocked == nil || *payload.IsLocked) {
			return gmStarSoulApplyResult{}, errors.New("锁定的星魂不能穿戴")
		}
	}
	if levelSet {
		item.Level = level
		applyStarSoulViceGrowth(item, rand.Intn)
		if item.IsUsed {
			result.Attrs = true
		}
	}
	if expSet {
		item.Exp = exp
		gmCapStarSoulExp(item)
	}
	if payload.IsLocked != nil && !*payload.IsLocked {
		item.IsLocked = false
	}
	if payload.Equip != nil {
		if err := gmEquipStarSoul(bag, item, *payload.Equip, &result); err != nil {
			return gmStarSoulApplyResult{}, err
		}
	}
	if payload.IsLocked != nil && *payload.IsLocked {
		item.IsLocked = true
	}
	return result, nil
}

func gmEquipStarSoul(bag *starSoulBag, item *starSoulItem, equip bool, result *gmStarSoulApplyResult) error {
	if bag == nil || item == nil || result == nil {
		return errors.New("星魂背包不可用")
	}
	if item.PosType < 0 || item.PosType >= starSoulSlotCount {
		return errors.New("星魂部位无效")
	}
	slot := item.PosType
	if !equip {
		if bag.Used[slot] == item.ID {
			bag.Used[slot] = 0
			item.IsUsed = false
			result.Slots = append(result.Slots, slot)
			result.Attrs = true
		}
		return nil
	}
	if item.IsLocked {
		return errors.New("锁定的星魂不能穿戴")
	}
	if oldID := bag.Used[slot]; oldID != 0 && oldID != item.ID {
		if old := bag.Items[oldID]; old != nil {
			old.IsUsed = false
			result.IDs = append(result.IDs, oldID)
		}
	}
	bag.Used[slot] = item.ID
	item.IsUsed = true
	result.Slots = append(result.Slots, slot)
	result.Attrs = true
	return nil
}

func gmCapStarSoulExp(it *starSoulItem) {
	if it == nil || it.Level >= 20 {
		return
	}
	row := starSoulLevelRow(it.Quality, it.Level+1)
	if row == nil {
		return
	}
	need := int32(num(row["NeedExp"]))
	if need > 0 && it.Exp >= need {
		it.Exp = need - 1
	}
}

// parseGMOptionalInt64 与 parseGMOptionalInt32 同规则，报错也写出具体上限/下限。
func parseGMOptionalInt64(raw *string, field string, min, max int64) (int64, bool, error) {
	if raw == nil {
		return 0, false, nil
	}
	text := strings.TrimSpace(*raw)
	if text == "" {
		return 0, false, fmt.Errorf("%s不能为空", field)
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("%s必须是数字", field)
	}
	if value < min {
		return 0, false, fmt.Errorf("%s不能小于 %d", field, min)
	}
	if value > max {
		return 0, false, fmt.Errorf("%s不能超过 %d", field, max)
	}
	return value, true, nil
}

func gmStarSoulItemSnapshot(it *starSoulItem) map[string]any {
	if it == nil {
		return nil
	}
	return map[string]any{
		"starSoulId": it.ID, "typeId": it.TypeID, "posType": it.PosType, "quality": it.Quality,
		"level": it.Level, "exp": it.Exp, "isLocked": it.IsLocked, "isUsed": it.IsUsed, "main": it.Main,
	}
}

func gmStarSoulBagSnapshot(bag *starSoulBag, ids []int64) []map[string]any {
	if bag == nil {
		return nil
	}
	if len(ids) == 0 {
		out := make([]map[string]any, 0, len(bag.Items))
		for _, it := range bag.Items {
			if snap := gmStarSoulItemSnapshot(it); snap != nil {
				out = append(out, snap)
			}
		}
		return out
	}
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		if snap := gmStarSoulItemSnapshot(bag.Items[id]); snap != nil {
			out = append(out, snap)
			continue
		}
		out = append(out, map[string]any{"starSoulId": id, "removed": true})
	}
	return out
}
