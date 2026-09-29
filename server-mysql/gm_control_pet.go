package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	json "github.com/goccy/go-json"
)

const gmPetNameMaxRunes = 64

type gmPetPayload struct {
	PetID       *string `json:"petId"`
	Level       *string `json:"level"`
	Exp         *string `json:"exp"`
	Intimacy    *string `json:"intimacy"`
	Name        *string `json:"name"`
	IsShow      *bool   `json:"isShow"`
	Active      *string `json:"active"`
	EatCount    *string `json:"eatCount"`
	ResetAction *bool   `json:"resetAction"`
}

func (s *Server) gmAdjustPet(playerID int64, raw json.RawMessage) (string, map[string]any, string, string) {
	var payload gmPetPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "rejected", nil, "invalid_payload", "宠物参数错误"
	}
	if !gmPetPayloadHasField(payload) {
		return "rejected", nil, "invalid_payload", "请至少填写一项宠物修改"
	}
	ch, release, err := s.gmResolveSession(playerID)
	if err != nil {
		return "rejected", nil, "player_not_found", "角色不存在"
	}
	defer release()
	ss := ch.session
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	if ss.pet == nil {
		ss.pet = newPet()
	}
	before := clonePetState(ss.pet)
	if err := applyGMPetAdjust(ss.pet, payload); err != nil {
		return "rejected", nil, "invalid_pet", err.Error()
	}
	if err := s.persistGMSession(ch); err != nil {
		return "rejected", nil, "persist_failed", "宠物变更保存失败"
	}
	if ch.conn != nil {
		s.pushSyncPet(ch)
	}
	return "completed", map[string]any{"before": petSnapshot(before), "after": petSnapshot(ss.pet)}, "", ""
}

func gmPetPayloadHasField(payload gmPetPayload) bool {
	return payload.PetID != nil || payload.Level != nil || payload.Exp != nil || payload.Intimacy != nil ||
		payload.Name != nil || payload.IsShow != nil || payload.Active != nil || payload.EatCount != nil ||
		payload.ResetAction != nil
}

func applyGMPetAdjust(p *petState, payload gmPetPayload) error {
	if p == nil {
		return errors.New("宠物状态不可用")
	}
	petID := p.PetId
	petChanged := false
	if payload.PetID != nil {
		value, _, err := parseGMOptionalInt32(payload.PetID, "宠物 ID", 1, math.MaxInt32)
		if err != nil {
			return err
		}
		if _, ok := gmPetConfigName(value); !ok {
			return errors.New("宠物 ID 不存在")
		}
		petChanged = value != p.PetId
		petID = value
	}
	maxLevel := gmPetMaxLevel(petID)
	level, levelSet, err := parseGMOptionalInt32(payload.Level, "宠物等级", 1, int64(maxLevel))
	if err != nil {
		return err
	}
	exp, expSet, err := parseGMOptionalInt32(payload.Exp, "宠物经验", 0, math.MaxInt32)
	if err != nil {
		return err
	}
	intimacy, intimacySet, err := parseGMOptionalInt32(payload.Intimacy, "亲密度", 0, math.MaxInt32)
	if err != nil {
		return err
	}
	active, activeSet, err := parseGMOptionalInt32(payload.Active, "活跃度", 0, math.MaxInt32)
	if err != nil {
		return err
	}
	eatCount, eatCountSet, err := parseGMOptionalInt32(payload.EatCount, "今日喂食次数", 0, int64(petFeedLimit))
	if err != nil {
		return err
	}
	name := p.Name
	nameSet := false
	if payload.Name != nil {
		name = strings.TrimSpace(*payload.Name)
		if name == "" {
			return errors.New("宠物名不能为空")
		}
		if utf8.RuneCountInString(name) > gmPetNameMaxRunes {
			return fmt.Errorf("宠物名不能超过 %d 个字符", gmPetNameMaxRunes)
		}
		nameSet = true
	} else if petChanged {
		if configName, ok := gmPetConfigName(petID); ok {
			name = configName
			nameSet = true
		}
	}
	resetAction := petChanged
	if payload.ResetAction != nil {
		resetAction = *payload.ResetAction || petChanged
	}
	p.PetId = petID
	if nameSet {
		p.Name = name
	}
	if levelSet {
		p.Level = level
	}
	if expSet {
		p.Exp = exp
	}
	if intimacySet {
		p.Intimacy = intimacy
	}
	if activeSet {
		p.Active = active
	}
	if eatCountSet {
		p.EatCount = eatCount
	}
	if payload.IsShow != nil {
		p.IsShow = *payload.IsShow
	}
	if resetAction {
		p.PetState = petStateIdle
		p.ActionEnd = 0
		p.Rewarded = false
	}
	return nil
}

// parseGMOptionalInt32 解析 GM 面板传来的可选数值字段。
//
// 报错必须写出**具体上限/下限**（「宠物等级不能超过 100」而不是「宠物等级无效」）：
// GM 后台过去只有一个裸状态码，运营看到「请求失败（422）」完全不知道该改多少。
// 先按 64 位解析再比对范围，超 int32 的输入才能落到「不能超过 2147483647」，
// 而不是被误报成「必须是数字」。
func parseGMOptionalInt32(raw *string, field string, min, max int64) (int32, bool, error) {
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
	return int32(value), true, nil
}

func gmPetConfigName(petID int32) (string, bool) {
	if tables == nil || tables.petConfig == nil {
		return "", false
	}
	row := tables.petConfig[int64(petID)]
	if row == nil {
		return "", false
	}
	name, _ := row["Name"].(string)
	if strings.TrimSpace(name) == "" {
		name = fmt.Sprintf("宠物%d", petID)
	}
	return name, true
}

func gmPetMaxLevel(petID int32) int32 {
	if tables == nil || tables.petLevelConfig == nil {
		return 100
	}
	row := tables.petLevelConfig[int64(petID)]
	if row == nil {
		return 100
	}
	maxLevel := int32(num(row["MaxLevel"]))
	if maxLevel < 1 {
		return 100
	}
	return maxLevel
}

func clonePetState(p *petState) *petState {
	if p == nil {
		return nil
	}
	copied := *p
	return &copied
}

func petSnapshot(p *petState) map[string]any {
	if p == nil {
		return nil
	}
	return map[string]any{
		"petId": p.PetId, "level": p.Level, "exp": p.Exp, "intimacy": p.Intimacy, "name": p.Name,
		"isShow": p.IsShow, "active": p.Active, "eatCount": p.EatCount, "petState": p.PetState,
	}
}
