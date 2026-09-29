package main

import (
	"fmt"
	"time"
)

type dailyDungeonQuota uint8

const (
	dailyDungeonQuotaNone dailyDungeonQuota = iota
	dailyDungeonQuotaSpaceTravel
	dailyDungeonQuotaDeathTower
	dailyDungeonQuotaFamilyBoss
)

func gameplayDungeonDay(now time.Time) string {
	_, _, _, location := gameplayDungeonSettings()
	return now.In(location).Format("20060102")
}

func dailyDungeonQuotaForBattle(copyID int64, presentation battlePresentation) dailyDungeonQuota {
	switch {
	case presentation.kind == presentationFamilyBoss:
		return dailyDungeonQuotaFamilyBoss
	case copyID == 10013:
		return dailyDungeonQuotaSpaceTravel
	case copyID == 10016 && presentation.activity != nil && presentation.activity.Stage == 1:
		return dailyDungeonQuotaDeathTower
	default:
		return dailyDungeonQuotaNone
	}
}

func dailyDungeonQuotaLabel(quota dailyDungeonQuota) string {
	switch quota {
	case dailyDungeonQuotaSpaceTravel:
		return "时空旅行战斗次数"
	case dailyDungeonQuotaDeathTower:
		return "死亡之塔战斗次数"
	case dailyDungeonQuotaFamilyBoss:
		return "家族 Boss 钥匙"
	default:
		return "副本次数"
	}
}

// refreshDailyDungeonQuotas restores limits only at the configured operating
// day boundary. A same-day configuration reload never refills spent quotas.
func (ss *session) refreshDailyDungeonQuotas(now time.Time) bool {
	if ss == nil {
		return false
	}
	ss.dungeonQuotaMu.Lock()
	defer ss.dungeonQuotaMu.Unlock()
	return ss.refreshDailyDungeonQuotasLocked(now)
}

func (ss *session) refreshDailyDungeonQuotasLocked(now time.Time) bool {
	if ss.signin == nil {
		ss.signin = &signinState{}
	}
	day := gameplayDungeonDay(now)
	if ss.signin.DungeonQuotaDay == day {
		changed := false
		if ss.signin.SpaceTravelRemaining < 0 {
			ss.signin.SpaceTravelRemaining = 0
			changed = true
		}
		if ss.signin.DeathTowerRemaining < 0 {
			ss.signin.DeathTowerRemaining = 0
			changed = true
		}
		if ss.signin.FamilyBossKeys < 0 {
			ss.signin.FamilyBossKeys = 0
			changed = true
		}
		return changed
	}
	spaceTravel, deathTower, familyBossKeys, _ := gameplayDungeonSettings()
	ss.signin.DungeonQuotaDay = day
	ss.signin.SpaceTravelRemaining = spaceTravel
	ss.signin.DeathTowerRemaining = deathTower
	ss.signin.FamilyBossKeys = familyBossKeys
	return true
}

func (ss *session) dailyDungeonQuotaRemaining(quota dailyDungeonQuota, now time.Time) (int32, bool) {
	if ss == nil {
		return 0, false
	}
	ss.dungeonQuotaMu.Lock()
	defer ss.dungeonQuotaMu.Unlock()
	changed := ss.refreshDailyDungeonQuotasLocked(now)
	switch quota {
	case dailyDungeonQuotaSpaceTravel:
		return ss.signin.SpaceTravelRemaining, changed
	case dailyDungeonQuotaDeathTower:
		return ss.signin.DeathTowerRemaining, changed
	case dailyDungeonQuotaFamilyBoss:
		return ss.signin.FamilyBossKeys, changed
	default:
		return 0, changed
	}
}

func (ss *session) consumeDailyDungeonQuotaLocked(quota dailyDungeonQuota, now time.Time) bool {
	if ss == nil {
		return false
	}
	ss.refreshDailyDungeonQuotasLocked(now)
	var remaining *int32
	switch quota {
	case dailyDungeonQuotaSpaceTravel:
		remaining = &ss.signin.SpaceTravelRemaining
	case dailyDungeonQuotaDeathTower:
		remaining = &ss.signin.DeathTowerRemaining
	case dailyDungeonQuotaFamilyBoss:
		remaining = &ss.signin.FamilyBossKeys
	default:
		return true
	}
	if *remaining <= 0 {
		return false
	}
	*remaining--
	return true
}

func (s *Server) dailyDungeonQuotaFailure(origin *channel, participants []*channel, quota dailyDungeonQuota, now time.Time) string {
	if quota == dailyDungeonQuotaNone {
		return ""
	}
	for _, member := range participants {
		if member == nil || member.session == nil {
			continue
		}
		remaining, changed := member.session.dailyDungeonQuotaRemaining(quota, now)
		if changed {
			s.saveData(member)
		}
		if remaining > 0 {
			continue
		}
		label := dailyDungeonQuotaLabel(quota)
		if origin != nil && origin.session != nil && member.session.playerID != origin.session.playerID {
			name := member.session.name
			if name == "" {
				name = fmt.Sprintf("玩家%d", member.session.playerID)
			}
			return fmt.Sprintf("队员%s%s不足", name, label)
		}
		return label + "不足"
	}
	return ""
}

func (s *Server) battleDailyDungeonQuotaFailure(ch *channel, region, mapID int32, presentation battlePresentation, now time.Time) string {
	if ch == nil || ch.session == nil {
		return ""
	}
	copyID := battleCopyConfig(region, mapID, presentation)
	quota := dailyDungeonQuotaForBattle(copyID, presentation)
	participants := s.battleParticipantsForPresentation(ch, copyID, presentation)
	return s.dailyDungeonQuotaFailure(ch, participants, quota, now)
}

// consumeDailyDungeonQuotas commits one charge for every actual participant.
// The caller has already installed every battle state while holding partyStartMu.
func (s *Server) consumeDailyDungeonQuotas(participants []*channel, quota dailyDungeonQuota, now time.Time) bool {
	if quota == dailyDungeonQuotaNone {
		return true
	}
	locked := make([]*session, 0, len(participants))
	for _, member := range participants {
		if member == nil || member.session == nil {
			continue
		}
		member.session.dungeonQuotaMu.Lock()
		locked = append(locked, member.session)
	}
	defer func() {
		for i := len(locked) - 1; i >= 0; i-- {
			locked[i].dungeonQuotaMu.Unlock()
		}
	}()

	for _, ss := range locked {
		ss.refreshDailyDungeonQuotasLocked(now)
		var remaining int32
		switch quota {
		case dailyDungeonQuotaSpaceTravel:
			remaining = ss.signin.SpaceTravelRemaining
		case dailyDungeonQuotaDeathTower:
			remaining = ss.signin.DeathTowerRemaining
		case dailyDungeonQuotaFamilyBoss:
			remaining = ss.signin.FamilyBossKeys
		}
		if remaining <= 0 {
			return false
		}
	}
	for _, ss := range locked {
		if !ss.consumeDailyDungeonQuotaLocked(quota, now) {
			return false
		}
	}
	return true
}
