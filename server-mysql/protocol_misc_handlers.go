package main

import (
	"log"
	"sort"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func (s *Server) onGetBattleStateBuff(ch *channel, req *protocol.C2M_GetBattleStateBuff) proto.Message {
	resp := &protocol.M2C_GetBattleStateBuff{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	ss := ch.session
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	battle := ss.battle
	if battle == nil || battle.runtime == nil {
		return resp
	}
	target := CombatUnitRef{Side: CombatSidePlayer, ID: ss.playerID}
	if req.UnitId != 0 && req.UnitId != ss.playerID {
		for _, monster := range battle.monsters {
			if monster != nil && monster.id == req.UnitId {
				target = CombatUnitRef{Side: CombatSideMonster, ID: monster.id}
				break
			}
		}
	}
	for _, effect := range battle.runtime.state().effects {
		if effect == nil || effect.hidden || battle.runtime.normalize(effect.target) != target {
			continue
		}
		meta := battle.effectMeta[effect.key]
		if meta.stateKey == 0 {
			continue
		}
		userData := effect.value
		if userData < 0 {
			userData = -userData
		}
		resp.InfoList = append(resp.InfoList, &protocol.StateBuffInfo{
			StateType: meta.stateKey,
			Layer:     int32(effect.stacks),
			UserData:  userData,
		})
	}
	sort.SliceStable(resp.InfoList, func(i, j int) bool {
		return resp.InfoList[i].StateType < resp.InfoList[j].StateType
	})
	return resp
}

func transferTargetJobID(currentJob, jobType int32) int32 {
	if jobType < 1 || jobType > 4 {
		return 0
	}
	// ClientUnitCharacter encodes sex in the low bit: odd=male, even=female.
	sexOffset := int32(1)
	if currentJob > 0 && currentJob%2 == 0 {
		sexOffset = 2
	}
	return (jobType-1)*2 + sexOffset
}

func (s *Server) onTransferJob(ch *channel, req *protocol.C2M_TransferJob) proto.Message {
	resp := &protocol.M2C_TransferJob{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	ss := ch.session
	targetJob := transferTargetJobID(ss.jobID, req.JobType)
	if targetJob == 0 {
		resp.Message = "职业参数错误"
		return resp
	}
	if jobTypeOf(ss.jobID) == req.JobType {
		resp.Message = "已经是该职业"
		return resp
	}
	var incompatible []int32
	for slot, item := range ss.worn {
		if item == nil || tables == nil {
			continue
		}
		row := tables.equipBase[int64(item.ItemId)]
		requiredJob := int32(num(row["JobId"]))
		if requiredJob != 0 && requiredJob != req.JobType {
			incompatible = append(incompatible, slot)
		}
	}
	if visibleBagItemCount(ss)+len(incompatible) > int(bagSlotCount) {
		resp.Message = "背包空间不足，无法卸下旧职业装备"
		return resp
	}
	for _, slot := range incompatible {
		index := nextBagIndex(ss)
		if index < 0 {
			resp.Message = "背包已满"
			return resp
		}
		ss.bag[index] = ss.worn[slot]
		delete(ss.worn, slot)
	}
	oldJob := ss.jobID
	oldBaseSkill := baseSkillOfJob(oldJob)
	ss.jobID = targetJob
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
	s.pushPlayerAttrs(ch)
	s.pushUnitCharacter(ch)
	s.sendPush(ch, protocol.OpM2C_SendCharacter, &protocol.M2C_SendCharacter{
		UnitCharacter: buildUnitCharacter(ss), Id: ss.playerID, ActorId: ss.playerID,
	})
	s.pushBagSnapshot(ch)
	s.saveData(ch)
	s.broadcastPlayerUpdate(ch)
	log.Printf("[S=%d] transfer job %d -> %d family=%d", ch.id, oldJob, targetJob, req.JobType)
	return resp
}

func (s *Server) onStartTestBattleFight(ch *channel, req *protocol.C2M_StartTestBattleFight) proto.Message {
	return &protocol.M2C_StartTestBattleFight{RpcId: req.RpcId, Message: "测试战斗入口未在正式客户端开放"}
}

func (s *Server) onEndTestBattleFight(ch *channel, req *protocol.C2M_EndTestBattleFight) proto.Message {
	if ch != nil && ch.session != nil && sessionHasBattle(ch) {
		s.onQuitBattle(ch, &protocol.C2M_QuitBattle{})
	}
	return &protocol.M2C_EndTestBattleFight{RpcId: req.RpcId}
}

func (s *Server) onTestRequest(req *protocol.C2M_TestRequest) proto.Message {
	return &protocol.M2C_TestRequest{RpcId: req.RpcId, Response: req.Request}
}

func (s *Server) onReload(req *protocol.C2M_Reload) proto.Message {
	return &protocol.M2C_Reload{RpcId: req.RpcId, Message: "运行中热重载未开放"}
}

func (s *Server) onGMDelOtherUserBag(req *protocol.C2M_GMDelOtherUserBag) proto.Message {
	return &protocol.M2C_GMDelOtherUserBag{RpcId: req.RpcId, Message: "未授权的GM操作"}
}
