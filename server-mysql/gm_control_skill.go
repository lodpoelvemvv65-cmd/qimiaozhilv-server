package main

import (
	"errors"
	"fmt"
	"math"

	json "github.com/goccy/go-json"
	"mhqserver/protocol"
)

func init() {
	registerGMAction("player.skills_adjust", func(s *Server, playerID int64, raw json.RawMessage) (string, map[string]any, string, string) {
		return s.gmAdjustSkills(playerID, raw)
	})
}

type gmSkillEntry struct {
	SkillID *string `json:"skillId"`
	Level   *string `json:"level"`
}

type gmSkillPayload struct {
	SkillPoint      *string        `json:"skillPoint"`
	SkillPointDelta *string        `json:"skillPointDelta"`
	Skills          []gmSkillEntry `json:"skills"`
}

func (s *Server) gmAdjustSkills(playerID int64, raw json.RawMessage) (string, map[string]any, string, string) {
	var payload gmSkillPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "rejected", nil, "invalid_payload", "技能参数错误"
	}
	if payload.SkillPoint == nil && payload.SkillPointDelta == nil && len(payload.Skills) == 0 {
		return "rejected", nil, "invalid_payload", "请至少填写技能点或一项技能修改"
	}
	ch, release, err := s.gmResolveSession(playerID)
	if err != nil {
		return "rejected", nil, "player_not_found", "角色不存在"
	}
	defer release()
	ss := ch.session
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	before := gmSkillSnapshot(ss)
	if err := applyGMSkillAdjust(ss, payload); err != nil {
		return "rejected", nil, "invalid_skill", err.Error()
	}
	if err := s.persistGMSession(ch); err != nil {
		return "rejected", nil, "persist_failed", "技能变更保存失败"
	}
	if ch.conn != nil {
		s.sendPush(ch, protocol.OpM2C_GetSkill, &protocol.M2C_GetSkill{
			SkillInfoLsit: ss.skillInfoList(),
			AutoSkillList: append([]int32(nil), ss.autoSkills...),
		})
		s.pushUnitCharacter(ch)
	}
	return "completed", map[string]any{"before": before, "after": gmSkillSnapshot(ss)}, "", ""
}

func applyGMSkillAdjust(ss *session, payload gmSkillPayload) error {
	if ss == nil {
		return errors.New("角色状态不可用")
	}
	if ss.skills == nil {
		ss.skills = make(map[int32]int32)
	}
	point := ss.skillPoint
	if payload.SkillPoint != nil {
		value, _, err := parseGMOptionalInt32(payload.SkillPoint, "技能点", 0, math.MaxInt32)
		if err != nil {
			return err
		}
		point = value
	}
	if payload.SkillPointDelta != nil {
		delta, _, err := parseGMOptionalInt32(payload.SkillPointDelta, "技能点变化", math.MinInt32, math.MaxInt32)
		if err != nil {
			return err
		}
		next := int64(point) + int64(delta)
		if next < 0 {
			return errors.New("技能点不能小于 0")
		}
		if next > math.MaxInt32 {
			return fmt.Errorf("技能点不能超过 %d", int64(math.MaxInt32))
		}
		point = int32(next)
	}
	changes := make([][2]int32, 0, len(payload.Skills))
	for _, entry := range payload.Skills {
		if entry.SkillID == nil || entry.Level == nil {
			return errors.New("技能 ID 和等级都要填写")
		}
		skillID, _, err := parseGMOptionalInt32(entry.SkillID, "技能 ID", 1, math.MaxInt32)
		if err != nil {
			return err
		}
		if !isJobSkill(ss.jobID, skillID) {
			return fmt.Errorf("技能 %d 不属于当前职业", skillID)
		}
		maxLevel := skillMaxLevel(skillID)
		level, _, err := parseGMOptionalInt32(entry.Level, "技能等级", 0, int64(maxLevel))
		if err != nil {
			return err
		}
		if level == 0 && skillID == baseSkillOfJob(ss.jobID) {
			return errors.New("不能遗忘职业普攻")
		}
		changes = append(changes, [2]int32{skillID, level})
	}
	ss.skillPoint = point
	for _, change := range changes {
		skillID, level := change[0], change[1]
		if level <= 0 {
			delete(ss.skills, skillID)
			ss.skillOrder = filterOutSkillID(ss.skillOrder, skillID)
			ss.autoSkills = filterOutSkillID(ss.autoSkills, skillID)
			continue
		}
		if _, learned := ss.skills[skillID]; !learned {
			ss.skillOrder = append(ss.skillOrder, skillID)
		}
		ss.skills[skillID] = level
	}
	return nil
}

func filterOutSkillID(ids []int32, skillID int32) []int32 {
	out := make([]int32, 0, len(ids))
	for _, id := range ids {
		if id != skillID {
			out = append(out, id)
		}
	}
	return out
}

func gmSkillSnapshot(ss *session) map[string]any {
	if ss == nil {
		return nil
	}
	skills := make([]map[string]any, 0, len(ss.skillOrder))
	for _, id := range ss.skillOrder {
		skills = append(skills, map[string]any{"skillId": id, "level": ss.skills[id]})
	}
	return map[string]any{"skillPoint": ss.skillPoint, "skills": skills}
}
