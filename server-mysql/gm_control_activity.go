package main

import (
	"errors"
	"time"

	json "github.com/goccy/go-json"
)

func init() {
	registerGMAction("player.activity_adjust", func(s *Server, playerID int64, raw json.RawMessage) (string, map[string]any, string, string) {
		return s.gmAdjustActivity(playerID, raw)
	})
}

type gmActivityPayload struct {
	SpaceTravelRemaining *string `json:"spaceTravelRemaining"`
	DeathTowerRemaining  *string `json:"deathTowerRemaining"`
	FamilyBossKeys       *string `json:"familyBossKeys"`
}

func (s *Server) gmAdjustActivity(playerID int64, raw json.RawMessage) (string, map[string]any, string, string) {
	var payload gmActivityPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "rejected", nil, "invalid_payload", "日常次数参数错误"
	}
	if payload.SpaceTravelRemaining == nil && payload.DeathTowerRemaining == nil && payload.FamilyBossKeys == nil {
		return "rejected", nil, "invalid_payload", "请至少填写一项日常次数"
	}
	ch, release, err := s.gmResolveSession(playerID)
	if err != nil {
		return "rejected", nil, "player_not_found", "角色不存在"
	}
	defer release()
	ss := ch.session
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	ss.dungeonQuotaMu.Lock()
	defer ss.dungeonQuotaMu.Unlock()
	before := gmActivitySnapshot(ss)
	if err := applyGMActivityAdjust(ss, payload, time.Now()); err != nil {
		return "rejected", nil, "invalid_activity", err.Error()
	}
	if err := s.persistGMSession(ch); err != nil {
		return "rejected", nil, "persist_failed", "日常次数保存失败"
	}
	return "completed", map[string]any{"before": before, "after": gmActivitySnapshot(ss)}, "", ""
}

func applyGMActivityAdjust(ss *session, payload gmActivityPayload, now time.Time) error {
	if ss == nil {
		return errors.New("角色状态不可用")
	}
	if ss.signin == nil {
		ss.signin = &signinState{}
	}
	space, spaceSet, err := parseGMOptionalInt32(payload.SpaceTravelRemaining, "时空旅行次数", 0, 10000)
	if err != nil {
		return err
	}
	tower, towerSet, err := parseGMOptionalInt32(payload.DeathTowerRemaining, "死亡之塔次数", 0, 10000)
	if err != nil {
		return err
	}
	keys, keysSet, err := parseGMOptionalInt32(payload.FamilyBossKeys, "家族 Boss 钥匙", 0, 10000)
	if err != nil {
		return err
	}
	ss.refreshDailyDungeonQuotasLocked(now)
	if spaceSet {
		ss.signin.SpaceTravelRemaining = space
	}
	if towerSet {
		ss.signin.DeathTowerRemaining = tower
	}
	if keysSet {
		ss.signin.FamilyBossKeys = keys
	}
	ss.signin.DungeonQuotaDay = gameplayDungeonDay(now)
	return nil
}

func gmActivitySnapshot(ss *session) map[string]any {
	if ss == nil || ss.signin == nil {
		return nil
	}
	return map[string]any{
		"spaceTravelRemaining": ss.signin.SpaceTravelRemaining,
		"deathTowerRemaining":  ss.signin.DeathTowerRemaining,
		"familyBossKeys":       ss.signin.FamilyBossKeys,
		"dungeonQuotaDay":      ss.signin.DungeonQuotaDay,
	}
}
