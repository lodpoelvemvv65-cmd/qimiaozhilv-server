package main

import (
	"errors"
	"time"

	json "github.com/goccy/go-json"
	"mhqserver/protocol"
)

func init() {
	registerGMAction("player.job_adjust", func(s *Server, playerID int64, raw json.RawMessage) (string, map[string]any, string, string) {
		return s.gmAdjustJob(playerID, raw)
	})
}

type gmJobPayload struct {
	JobType *string `json:"jobType"`
	Trans   *string `json:"trans"`
}

func (s *Server) gmAdjustJob(playerID int64, raw json.RawMessage) (string, map[string]any, string, string) {
	var payload gmJobPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "rejected", nil, "invalid_payload", "转职参数错误"
	}
	if payload.JobType == nil && payload.Trans == nil {
		return "rejected", nil, "invalid_payload", "请填写职业或转生"
	}
	ch, release, err := s.gmResolveSession(playerID)
	if err != nil {
		return "rejected", nil, "player_not_found", "角色不存在"
	}
	defer release()
	ss := ch.session
	ss.battleMu.Lock()
	before := gmJobSnapshot(ss)
	if err := applyGMJobAdjust(ss, payload); err != nil {
		ss.battleMu.Unlock()
		return "rejected", nil, "invalid_job", err.Error()
	}
	if err := s.persistGMSession(ch); err != nil {
		ss.battleMu.Unlock()
		return "rejected", nil, "persist_failed", "转职转生保存失败"
	}
	ss.battleMu.Unlock()
	if ch.conn != nil {
		s.pushPlayerAttrs(ch)
		s.pushUnitCharacter(ch)
		s.sendPush(ch, protocol.OpM2C_SendCharacter, &protocol.M2C_SendCharacter{
			UnitCharacter: buildUnitCharacter(ss), Id: ss.playerID, ActorId: ss.playerID,
		})
		s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: ss.playerID, NumericType: 1029, Value: float32(ss.trans), ActorId: ss.playerID,
		})
		// The stock HUD selects the job-sign page only during initialization.
		// Re-enter the current scene so an online GM adjustment refreshes the
		// visible transmigration icon without a client patch.
		s.changeMap(ch, ss.mapID, ss.x, ss.y)
		s.sendPush(ch, protocol.OpM2C_GetSkill, &protocol.M2C_GetSkill{
			SkillInfoLsit: ss.skillInfoList(), AutoSkillList: append([]int32(nil), ss.autoSkills...),
		})
		s.pushBagSnapshot(ch)
		s.broadcastPlayerUpdate(ch)
	}
	return "completed", map[string]any{"before": before, "after": gmJobSnapshot(ss)}, "", ""
}

func applyGMJobAdjust(ss *session, payload gmJobPayload) error {
	if ss == nil {
		return errors.New("角色状态不可用")
	}
	if payload.JobType != nil {
		jobType, _, err := parseGMOptionalInt32(payload.JobType, "职业", 1, 4)
		if err != nil {
			return err
		}
		if err := gmTransferJob(ss, jobType); err != nil {
			return err
		}
	}
	if payload.Trans != nil {
		trans, _, err := parseGMOptionalInt32(payload.Trans, "转生", 0, int64(transMaxLevel))
		if err != nil {
			return err
		}
		ss.trans = trans
		ss.recalcTransBonus()
	}
	return nil
}

func gmTransferJob(ss *session, jobType int32) error {
	targetJob := transferTargetJobID(ss.jobID, jobType)
	if targetJob == 0 {
		return errors.New("职业参数错误")
	}
	if jobTypeOf(ss.jobID) == jobType {
		return errors.New("已经是该职业")
	}
	var incompatible []int32
	for slot, item := range ss.worn {
		if item == nil || tables == nil {
			continue
		}
		row := tables.equipBase[int64(item.ItemId)]
		requiredJob := int32(num(row["JobId"]))
		if requiredJob != 0 && requiredJob != jobType {
			incompatible = append(incompatible, slot)
		}
	}
	if visibleBagItemCount(ss)+len(incompatible) > int(bagSlotCount) {
		return errors.New("背包空间不足，无法卸下旧职业装备")
	}
	for _, slot := range incompatible {
		index := nextBagIndex(ss)
		if index < 0 {
			return errors.New("背包已满")
		}
		ss.bag[index] = ss.worn[slot]
		delete(ss.worn, slot)
	}
	oldJob := ss.jobID
	oldBaseSkill := baseSkillOfJob(oldJob)
	ss.jobID = targetJob
	if ss.skills == nil {
		ss.skills = map[int32]int32{}
	}
	for skillID, level := range ss.skills {
		if !isJobSkill(targetJob, skillID) {
			delete(ss.skills, skillID)
			// 每升一级花 1 点技能点，转职丢弃旧职业技能时按等级全额返还。
			if skillID != oldBaseSkill {
				ss.skillPoint += level
			}
		}
	}
	ss.skillOrder = filterLearnedSkills(ss.skillOrder, ss.skills)
	ss.autoSkills = filterLearnedSkills(ss.autoSkills, ss.skills)
	ss.ensureCurrentJobBaseSkill()
	for index, slot := range ss.mainUISlots {
		if slot.Type == 1 && !isJobSkill(targetJob, slot.Id) {
			ss.mainUISlots[index] = mainUISlot{}
		}
	}
	ss.skillCooldowns = make(map[int32]time.Time)
	applySkinEquip(ss)
	ss.recalcTransBonus()
	return nil
}

func gmJobSnapshot(ss *session) map[string]any {
	if ss == nil {
		return nil
	}
	return map[string]any{"jobId": ss.jobID, "jobType": jobTypeOf(ss.jobID), "trans": ss.trans}
}
