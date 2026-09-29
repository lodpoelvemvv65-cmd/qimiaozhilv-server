package main

import (
	"errors"

	json "github.com/goccy/go-json"
)

func init() {
	registerGMAction("player.stats_adjust", func(s *Server, playerID int64, raw json.RawMessage) (string, map[string]any, string, string) {
		return s.gmAdjustStats(playerID, raw)
	})
}

type gmStatsPayload struct {
	Energy    *string `json:"energy"`
	CharPoint *string `json:"charPoint"`
	Str       *string `json:"str"`
	Quk       *string `json:"quk"`
	Spi       *string `json:"spi"`
	Wim       *string `json:"wim"`
	Phy       *string `json:"phy"`
	Sta       *string `json:"sta"`
}

func (s *Server) gmAdjustStats(playerID int64, raw json.RawMessage) (string, map[string]any, string, string) {
	var payload gmStatsPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "rejected", nil, "invalid_payload", "属性参数错误"
	}
	if payload.Energy == nil && payload.CharPoint == nil && payload.Str == nil && payload.Quk == nil && payload.Spi == nil && payload.Wim == nil && payload.Phy == nil && payload.Sta == nil {
		return "rejected", nil, "invalid_payload", "请至少填写活力、潜能点或一项加点"
	}
	ch, release, err := s.gmResolveSession(playerID)
	if err != nil {
		return "rejected", nil, "player_not_found", "角色不存在"
	}
	defer release()
	ss := ch.session
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	before := gmStatsSnapshot(ss)
	if err := applyGMStatsAdjust(ss, payload); err != nil {
		return "rejected", nil, "invalid_stats", err.Error()
	}
	if err := s.persistGMSession(ch); err != nil {
		return "rejected", nil, "persist_failed", "属性变更保存失败"
	}
	if ch.conn != nil {
		s.pushPlayerAttrs(ch)
		s.pushUnitCharacter(ch)
	}
	return "completed", map[string]any{"before": before, "after": gmStatsSnapshot(ss)}, "", ""
}

func applyGMStatsAdjust(ss *session, payload gmStatsPayload) error {
	if ss == nil {
		return errors.New("角色状态不可用")
	}
	energy, energySet, err := parseGMOptionalInt32(payload.Energy, "活力", 0, 100000)
	if err != nil {
		return err
	}
	charPoint, charSet, err := parseGMOptionalInt32(payload.CharPoint, "潜能点", 0, 100000000)
	if err != nil {
		return err
	}
	str, strSet, err := parseGMOptionalInt32(payload.Str, "力量", 0, 100000000)
	if err != nil {
		return err
	}
	quk, qukSet, err := parseGMOptionalInt32(payload.Quk, "敏捷", 0, 100000000)
	if err != nil {
		return err
	}
	spi, spiSet, err := parseGMOptionalInt32(payload.Spi, "精神", 0, 100000000)
	if err != nil {
		return err
	}
	wim, wimSet, err := parseGMOptionalInt32(payload.Wim, "智慧", 0, 100000000)
	if err != nil {
		return err
	}
	phy, phySet, err := parseGMOptionalInt32(payload.Phy, "体质", 0, 100000000)
	if err != nil {
		return err
	}
	sta, staSet, err := parseGMOptionalInt32(payload.Sta, "耐力", 0, 100000000)
	if err != nil {
		return err
	}
	if energySet {
		ss.energy = energy
	}
	if charSet {
		ss.charPoint = charPoint
	}
	if strSet {
		ss.strAdd = str
	}
	if qukSet {
		ss.qukAdd = quk
	}
	if spiSet {
		ss.spiAdd = spi
	}
	if wimSet {
		ss.wimAdd = wim
	}
	if phySet {
		ss.phyAdd = phy
	}
	if staSet {
		ss.staAdd = sta
	}
	return nil
}

func gmStatsSnapshot(ss *session) map[string]any {
	if ss == nil {
		return nil
	}
	return map[string]any{
		"energy": ss.energy, "charPoint": ss.charPoint,
		"str": ss.strAdd, "quk": ss.qukAdd, "spi": ss.spiAdd, "wim": ss.wimAdd, "phy": ss.phyAdd, "sta": ss.staAdd,
	}
}
