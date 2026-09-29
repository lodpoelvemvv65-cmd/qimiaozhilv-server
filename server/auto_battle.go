package main

import (
	"time"

	"mhqserver/protocol"
)

const (
	autoBattlePollInterval  = 250 * time.Millisecond
	autoBattleRetryInterval = time.Second
	autoBattleDefaultAction = 5 * time.Second
)

// setAutoBattle changes the generation as well as the switch. Starting a new
// worker for every enable request is safe because workers from earlier
// generations observe the epoch mismatch and exit.
func (s *session) setAutoBattle(enabled bool) uint64 {
	s.battleMu.Lock()
	defer s.battleMu.Unlock()
	s.autoBattleEpoch++
	s.autoBattle = enabled
	s.autoBattleNextCastAt = time.Time{}
	s.autoBattleSkillCursor = 0
	return s.autoBattleEpoch
}

func (s *session) stopAutoBattle() {
	if s == nil {
		return
	}
	s.battleMu.Lock()
	s.autoBattle = false
	s.autoBattleEpoch++
	s.autoBattleNextCastAt = time.Time{}
	s.autoBattleSkillCursor = 0
	s.battleMu.Unlock()
}

// autoBattleSkillIDsLocked returns learned automatic skills in round-robin
// order, followed by the profession's basic attack as a cooldown/resource
// fallback. This prevents the first saved skill from starving later skills.
// Callers must hold battleMu.
func (s *session) autoBattleSkillIDsLocked() []int32 {
	if s == nil {
		return nil
	}
	base := baseSkillOfJob(s.jobID)
	seen := make(map[int32]struct{}, len(s.autoSkills)+1)
	out := make([]int32, 0, len(s.autoSkills)+1)
	for _, skillID := range s.autoSkills {
		// The basic attack is always the final fallback, even when the client
		// includes it in the configured automatic-skill list.
		if skillID == base {
			continue
		}
		if _, learned := s.skills[skillID]; !learned {
			continue
		}
		if _, duplicate := seen[skillID]; duplicate {
			continue
		}
		seen[skillID] = struct{}{}
		out = append(out, skillID)
	}
	if len(out) > 1 {
		cursor := s.autoBattleSkillCursor % len(out)
		out = append(append([]int32(nil), out[cursor:]...), out[:cursor]...)
	}
	if _, learned := s.skills[base]; learned {
		out = append(out, base)
	}
	return out
}

// advanceAutoBattleCursorLocked moves the cursor to the skill after the one
// that successfully cast. Callers must hold battleMu.
func (s *session) advanceAutoBattleCursorLocked(skillID int32) {
	base := baseSkillOfJob(s.jobID)
	seen := make(map[int32]struct{}, len(s.autoSkills))
	filtered := make([]int32, 0, len(s.autoSkills))
	for _, id := range s.autoSkills {
		if id == base {
			continue
		}
		if _, learned := s.skills[id]; !learned {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		filtered = append(filtered, id)
	}
	for index, id := range filtered {
		if id == skillID {
			s.autoBattleSkillCursor = (index + 1) % len(filtered)
			return
		}
	}
}

func (s *Server) runAutoBattle(ch *channel, epoch uint64) {
	ticker := time.NewTicker(autoBattlePollInterval)
	defer ticker.Stop()
	for range ticker.C {
		if !s.autoBattleStep(ch, epoch) {
			return
		}
	}
}

// autoBattleStep attempts at most one successful cast. Errors such as cooldown,
// insufficient resources or a temporarily invalid target fall through to the
// next configured skill without being sent as unsolicited RPC responses.
func (s *Server) autoBattleStep(ch *channel, epoch uint64) bool {
	return s.autoBattleStepAt(ch, epoch, time.Now())
}

func (s *Server) autoBattleStepAt(ch *channel, epoch uint64, now time.Time) bool {
	if ch == nil || ch.session == nil {
		return false
	}
	ss := ch.session
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	if !ss.autoBattle || ss.autoBattleEpoch != epoch {
		return false
	}
	if ss.battle == nil || ss.battle.ended {
		// 战斗结束（胜利/换场）：只让当前 worker 退出（epoch 失效），
		// 【保留 autoBattle 开关】——用户开着自动战斗，打完当前怪后
		// 点下一个怪开新战斗时 finishStartBattle 会重新拉起 worker，
		// 实现"自动战斗持续打怪"而不是打完就停需要手动。
		ss.autoBattleEpoch++
		return false
	}
	if now.Before(ss.autoBattleNextCastAt) {
		return true
	}

	// Keep the epoch check and the cast in the same critical section. Without
	// this, a repeated enable could invalidate this worker after its check but
	// before it cast, allowing an old generation to perform one extra action.
	resp := &protocol.M2C_UseMainUISkill{}
	for _, skillID := range ss.autoBattleSkillIDsLocked() {
		if _, cast := s.castSkillLockedWithLog(ch, skillID, resp, false); cast {
			ss.advanceAutoBattleCursorLocked(skillID)
			ss.autoBattleNextCastAt = now.Add(autoBattleActionInterval(ss.jobID))
			return true
		}
	}
	ss.autoBattleNextCastAt = now.Add(autoBattleRetryInterval)
	return true
}

// autoBattleActionInterval is the shared attack cadence. SkillConfig.DelayTime
// is only the visual/projectile delay; using it here caused six automatic
// skills to fire about once per second and consume an entire MP bar at once.
func autoBattleActionInterval(jobID int32) time.Duration {
	basicSkillID := baseSkillOfJob(jobID)
	if tables != nil && tables.skillConfig != nil {
		if row := tables.skillConfig[int64(basicSkillID)*100]; row != nil {
			if cooldownMS := int64(num(row["CD"])); cooldownMS > 0 {
				return time.Duration(cooldownMS) * time.Millisecond
			}
		}
	}
	return autoBattleDefaultAction
}
