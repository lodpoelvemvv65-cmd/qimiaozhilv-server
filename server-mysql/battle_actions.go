package main

import (
	"fmt"
	"log"
	"math"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// 20048 → 20049：开始主线战斗。Region = MainStory._id。
// 响应 20049 Message 必须留空（客户端判空才发起战斗场景）。
// 随后推送 M2C_InitMainStoryMap(20047) + M2C_MainStoryMonsterInfo(20050)。
func (s *Server) onStartMainStoryFight(ch *channel, req *protocol.C2M_StartMainStoryFight) proto.Message {
	resp := &protocol.M2C_StartMainStoryFight{RpcId: req.RpcId}
	if ch.session.battleHP() <= 0 {
		logCombatHP(ch.session, "reject", fmt.Sprintf("S=%d path=start-main-story", ch.id))
		resp.Message = battleEntryHealthMessage
		return resp
	}
	region := mainStoryRegionForSession(ch.session, 0)
	if ch.session.energy < mainStoryEnergyCost(region) {
		resp.Message = "体力不足"
		return resp
	}
	// The beach scene has two native field encounter buttons.  Their Region
	// value is a local click slot, so keep the clicked-roster semantics there.
	// Every non-beach MainStory scene uses one chapter battle whose complete
	// Monster_NArr roster must be sent (layers 5-10 are split across groups).
	var ok bool
	var n int
	if ch.session.mapID/100 == 10006 {
		ok, n = s.startMainStoryEncounter(ch, region, req.Region)
	} else {
		ok, n = s.startMainStoryFightByRegion(ch, region)
	}
	if !ok {
		// Field-monster and main-story click paths can overlap. Treat a duplicate
		// request as an idempotent success so Session.Call does not disconnect.
		if sessionHasBattle(ch) {
			log.Printf("[S=%d] duplicate start main story fight region=%d ignored", ch.id, region)
			return resp
		}
		log.Printf("[S=%d] start main story fight failed requested=%d resolved=%d", ch.id, req.Region, region)
		resp.Message = "章节不存在或没有怪物"
		return resp
	}
	log.Printf("[S=%d] start main story fight region=%d monsters=%d", ch.id, region, n)
	return resp
}

type skillDamageMode uint8

const (
	skillDamageNone skillDamageMode = iota
	skillDamageAttackMultiplier
	skillDamageTargetCurrentHP
	skillDamagePlayerCurrentHP
	skillDamagePlayerMaxHP
)

type skillCastSpec struct {
	cd            int32
	castType      int32
	castValue     float64
	effectID      int32
	delay         int32
	friendly      bool
	damageMode    skillDamageMode
	targetCount   int
	damageValue   float64
	spiritualHit  bool
	ignoreDefense bool
}

// Team 来自已逆向校验的 SkillLogicConfig。SkillConfig 表本身没有 Team 字段。
// 友方和自身技能在当前单人战斗中都以玩家自身为目标，绝不能按敌方结算。
var friendlySkillIDs = map[int32]struct{}{
	110301: {}, 110303: {}, 110401: {}, 110404: {}, 110501: {}, 110502: {},
	110601: {}, 110602: {}, 110604: {},
	210404: {}, 210504: {},
	310301: {}, 310302: {}, 310401: {}, 310402: {}, 310404: {}, 310501: {},
	310304: {}, 310503: {}, 310504: {}, 310601: {},
	410304: {}, 410402: {}, 410404: {}, 410504: {}, 410602: {}, 410604: {},
}

// 这些技能只有持续状态、条件引爆或增减益。状态机尚未实现前安全跳过即时伤害。
var noImmediateDamageSkillIDs = map[int32]struct{}{
	210404: {}, 210504: {}, 310202: {}, 310303: {}, 310304: {}, 310403: {},
	310503: {}, 310603: {}, 310604: {}, 410403: {}, 410404: {}, 410602: {},
}

// 这两个技能的等级行 Args 是状态参数，伤害百分比由技能逻辑定义为生命百分比，
// 不能套用普通的攻击倍率解析。
var officerCurrentHPDamagePercent = map[int32]float64{1: 1, 2: 2, 3: 4, 4: 7}
var officerMaxHPDamagePercent = map[int32]float64{1: 2, 2: 4, 3: 7}

// 以下表逐项来自 SkillLogicConfig。目标数和伤害倍率使用明确字段或逻辑常量，
// 不再从 Args 最大值、EffectId 等表现字段推断。
var targetCountArgIndexBySkillID = map[int32]int{
	110101: 0,
	310201: 0, 310502: 0, 310602: 0,
	410101: 1, 410201: 1, 410202: 0, 410301: 1, 410302: 0,
	410303: 0, 410401: 1, 410501: 1, 410503: 0,
	420101: 1,
}

var fixedTargetCountBySkillID = map[int32]int{
	210201: 2,
	210501: 3,
	410502: 10,
	410601: 10,
	410603: 5,
}

var damageArgIndexBySkillID = map[int32]int{
	100001: 1,
	110101: 1, 110201: 0, 110202: 0, 110302: 0, 110304: 0,
	110402: 0, 110403: 0, 110504: 0, 120101: 0,
	200001: 1,
	210101: 0, 210201: 0, 210202: 0, 210301: 0, 210302: 0,
	210303: 0, 210304: 0, 210401: 0, 210402: 0, 210403: 0,
	210501: 1, 210502: 0, 210503: 0, 210601: 1, 210602: 0,
	210603: 3, 210604: 4, 220101: 0,
	300001: 1,
	310101: 0, 310201: 1, 310502: 1, 310602: 1, 320101: 0,
	410101: 2, 410201: 2, 410202: 1, 410301: 2, 410302: 1,
	410303: 1, 410401: 2, 410501: 2, 410503: 1, 410601: 1,
}

var fixedDamageBySkillIDAndLevel = map[int32]map[int32]float64{
	400001: {1: 193},
	410502: {1: 100, 2: 120, 3: 150, 4: 180},
	410603: {1: 50, 2: 50, 3: 50},
	420101: {
		1: 140, 2: 145, 3: 150, 4: 155, 5: 160,
		6: 165, 7: 170, 8: 175, 9: 180, 10: 185,
	},
}

var ignoreDefenseSkillIDs = map[int32]struct{}{
	110504: {},
}

func hasID(set map[int32]struct{}, id int32) bool {
	_, ok := set[id]
	return ok
}

func skillArg(row map[string]interface{}, index int) (float64, bool) {
	v, ok := row[fmt.Sprintf("Args%d", index)]
	if !ok {
		return 0, false
	}
	return numf(v), true
}

func skillDamageValue(skillID, level int32, levelCfg map[string]interface{}) (float64, bool) {
	if byLevel, ok := fixedDamageBySkillIDAndLevel[skillID]; ok {
		value, exists := byLevel[level]
		return value, exists
	}
	argIndex, ok := damageArgIndexBySkillID[skillID]
	if !ok {
		return 0, false
	}
	return skillArg(levelCfg, argIndex)
}

func skillTargetCount(skillID int32, levelCfg map[string]interface{}) int {
	if count, ok := fixedTargetCountBySkillID[skillID]; ok {
		return count
	}
	if argIndex, ok := targetCountArgIndexBySkillID[skillID]; ok {
		if v, ok := skillArg(levelCfg, argIndex); ok && v >= 1 && v <= 20 {
			return int(v)
		}
	}
	return 1
}

func parseSkillCastSpec(skillID, level int32, baseCfg, levelCfg map[string]interface{}) (skillCastSpec, error) {
	var spec skillCastSpec
	if baseCfg == nil || levelCfg == nil || level <= 0 {
		return spec, fmt.Errorf("skill config incomplete")
	}
	if int32(num(baseCfg["SkillType"])) == 1 {
		return spec, fmt.Errorf("passive skill cannot be cast")
	}

	spec.cd = int32(num(baseCfg["CD"]))
	spec.castType = int32(num(baseCfg["CastType"]))
	spec.castValue = numf(baseCfg["CastValue"])
	spec.effectID = int32(num(baseCfg["EffectId"]))
	spec.delay = int32(num(baseCfg["DelayTime"]))
	spec.friendly = hasID(friendlySkillIDs, skillID)
	job := skillID / 100000
	spec.spiritualHit = job == 3 || job == 4

	if spec.cd < 0 || spec.castType < 0 || spec.castType > 2 ||
		math.IsNaN(spec.castValue) || math.IsInf(spec.castValue, 0) || spec.castValue < 0 {
		return skillCastSpec{}, fmt.Errorf("invalid cast metadata")
	}
	if spec.friendly || hasID(noImmediateDamageSkillIDs, skillID) {
		return spec, nil
	}
	if skillID == 110503 {
		if value, ok := officerCurrentHPDamagePercent[level]; !ok {
			return skillCastSpec{}, fmt.Errorf("invalid skill level")
		} else {
			spec.damageMode = skillDamagePlayerCurrentHP
			spec.targetCount = 1
			spec.damageValue = value
			return spec, nil
		}
	}
	if skillID == 110603 {
		if value, ok := officerMaxHPDamagePercent[level]; !ok {
			return skillCastSpec{}, fmt.Errorf("invalid skill level")
		} else {
			spec.damageMode = skillDamagePlayerMaxHP
			spec.targetCount = 2
			if level >= 3 {
				spec.targetCount = 3
			}
			spec.damageValue = value
			return spec, nil
		}
	}
	damageValue, hasImmediateDamage := skillDamageValue(skillID, level, levelCfg)
	if !hasImmediateDamage {
		return spec, nil
	}

	spec.targetCount = skillTargetCount(skillID, levelCfg)
	if skillID == 110403 {
		spec.damageMode = skillDamageTargetCurrentHP
		spec.damageValue, _ = skillArg(levelCfg, 0)
	} else {
		spec.damageMode = skillDamageAttackMultiplier
		spec.damageValue = damageValue
		spec.ignoreDefense = hasID(ignoreDefenseSkillIDs, skillID)
	}
	if spec.targetCount <= 0 || spec.damageValue <= 0 || math.IsNaN(spec.damageValue) || math.IsInf(spec.damageValue, 0) {
		return skillCastSpec{}, fmt.Errorf("invalid damage arguments")
	}
	return spec, nil
}

func (b *battleState) selectSkillTargets(count int) []*monsterUnit {
	alive := b.aliveMonsters()
	if len(alive) == 0 || count <= 0 {
		return nil
	}
	if count > len(alive) {
		count = len(alive)
	}
	out := make([]*monsterUnit, 0, count)
	if b.selectedID != 0 {
		for _, m := range alive {
			if m.id == b.selectedID {
				out = append(out, m)
				break
			}
		}
	}
	for _, m := range alive {
		if len(out) >= count {
			break
		}
		if len(out) > 0 && out[0] == m {
			continue
		}
		out = append(out, m)
	}
	return out
}

// 20233 → 20234：点击技能槽使用技能。SlotId = 槽序号。
// 响应 20234 的 Message 必须留空，否则客户端误发 C2M_UseMainUIGoods。
func (s *Server) onUseMainUISkill(ch *channel, req *protocol.C2M_UseMainUISkill) proto.Message {
	resp := &protocol.M2C_UseMainUISkill{RpcId: req.RpcId}
	if ch == nil || ch.session == nil {
		resp.Error, resp.Message = errNotLogged, "login required"
		return resp
	}
	ch.session.battleMu.Lock()
	defer ch.session.battleMu.Unlock()
	return s.useMainUISkillLocked(ch, req, resp)
}

// useMainUISkillLocked contains the authoritative cast path. Callers must hold
// session.battleMu so manual input and the automatic battle worker cannot settle
// two actions against the same battle state concurrently.
func (s *Server) useMainUISkillLocked(ch *channel, req *protocol.C2M_UseMainUISkill, resp *protocol.M2C_UseMainUISkill) proto.Message {
	return s.useSkillPlanLocked(ch, req, resp)
}

func (s *Server) useMainUISkillLegacyLocked(ch *channel, req *protocol.C2M_UseMainUISkill, resp *protocol.M2C_UseMainUISkill) proto.Message {
	b := ch.session.battle
	if tables == nil {
		resp.Error, resp.Message = errBadParam, "配置未加载"
		return resp
	}
	if b == nil || b.ended {
		resp.Error, resp.Message = errBadParam, "未在战斗中"
		return resp
	}
	if len(ch.session.skillOrder) == 0 {
		resp.Error, resp.Message = errBadParam, "技能栏为空"
		return resp
	}
	if int(req.SlotId) < 0 || int(req.SlotId) >= len(ch.session.skillOrder) {
		resp.Error, resp.Message = errBadParam, "技能槽无效"
		return resp
	}

	skillID := ch.session.skillOrder[req.SlotId]
	level, learned := ch.session.skills[skillID]
	if !learned || level <= 0 {
		resp.Error, resp.Message = errBadParam, "skill not learned"
		return resp
	}
	// xx00 stores combat metadata. xx01..xxNN are level argument rows and
	// intentionally omit CD, cast and effect fields.
	cfg, ok := tables.skillConfig[int64(skillID)*100]
	if !ok {
		resp.Error, resp.Message = errBadParam, "技能配置不存在"
		return resp
	}

	levelCfg, ok := tables.skillConfig[int64(skillID)*100+int64(level)]
	if !ok {
		resp.Error, resp.Message = errBadParam, "skill level config missing"
		return resp
	}
	spec, err := parseSkillCastSpec(skillID, level, cfg, levelCfg)
	if err != nil {
		resp.Error, resp.Message = errBadParam, err.Error()
		return resp
	}
	if spec.damageMode == skillDamageNone {
		// 纯状态、治疗和护盾尚无服务端状态机。必须在扣资源、登记 CD、
		// 推送表现和怪物反击前拒绝，避免玩家消耗资源后得到一次空施放。
		resp.Error, resp.Message = errBadParam, "skill effect not implemented"
		return resp
	}
	pid := ch.session.playerID

	// 先确认战场仍有目标；实际受击列表再按技能目标数和当前选中目标生成。
	targets := b.aliveMonsters()
	if len(targets) == 0 {
		resp.Error, resp.Message = errBadParam, "没有可攻击目标"
		return resp
	}
	var hit []*monsterUnit
	if spec.damageMode != skillDamageNone {
		hit = b.selectSkillTargets(spec.targetCount)
		if len(hit) == 0 {
			resp.Error, resp.Message = errBadParam, "no attack target"
			return resp
		}
	}
	if err := ensureSkillLogicCatalog(); err != nil {
		resp.Error, resp.Message = errBadParam, err.Error()
		return resp
	}
	plan, err := skillLogicCatalog.Plan(skillID, level)
	if err != nil {
		resp.Error, resp.Message = errBadParam, err.Error()
		return resp
	}
	b.owner = ch.session
	mpCost, hpCost, err := b.skillResourceCost(plan.Cast)
	if err != nil {
		resp.Error, resp.Message = errBadParam, err.Error()
		return resp
	}
	if remaining, ready := ch.session.tryStartSkillCooldown(skillID, time.Now(), time.Duration(spec.cd)*time.Millisecond); !ready {
		resp.Error = errBadParam
		resp.Message = fmt.Sprintf("skill cooldown: %dms", maxInt64(1, remaining.Milliseconds()))
		return resp
	}
	b.playerMP -= mpCost
	b.playerHP -= hpCost
	targetID := pid
	if !spec.friendly {
		targetID = targets[0].id
		if len(hit) > 0 {
			targetID = hit[0].id
		}
	}

	// 1) 播放技能（20075）
	s.sendPush(ch, protocol.OpM2C_PlaySkill, &protocol.M2C_PlaySkill{
		UnitId:  pid,
		SkillId: skillID,
	})
	if mpCost > 0 {
		// 当前客户端版本没有注册 20079 handler，使用通用 Numeric 同步更新蓝条。
		s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: pid, NumericType: 1003, Value: float32(b.playerMP), ActorId: pid,
		})
	}
	if hpCost > 0 {
		s.sendPush(ch, protocol.OpM2C_BattleSkillRet, &protocol.M2C_BattleSkillRet{
			UnitId: pid, ChangeHpValue: -hpCost,
		})
	}
	if spec.effectID > 0 {
		s.sendPush(ch, protocol.OpM2C_PlaySkillEffect, &protocol.M2C_PlaySkillEffect{
			UnitId: pid, TargetId: targetID, Time: spec.delay, EffectId: spec.effectID,
		})
	}

	// 3) 逐个目标结算伤害（20078）
	for _, m := range hit {
		var dmg int32
		switch spec.damageMode {
		case skillDamageAttackMultiplier:
			dmg = b.attackMultiplierDamage(spec, m)
		case skillDamageTargetCurrentHP:
			dmg = int32(math.Ceil(float64(m.hp) * spec.damageValue / 100))
			if dmg < 1 {
				dmg = 1
			}
		case skillDamagePlayerCurrentHP:
			dmg = int32(math.Ceil(float64(b.playerHP) * spec.damageValue / 100))
			if dmg < 1 {
				dmg = 1
			}
		case skillDamagePlayerMaxHP:
			dmg = int32(math.Ceil(float64(b.playerMaxHP) * spec.damageValue / 100))
			if dmg < 1 {
				dmg = 1
			}
		}
		if dmg > m.hp {
			dmg = m.hp
		}
		m.hp -= dmg
		if b.mapID < 0 {
			recordFamilyBossDamage(ch.session.familyID, -b.mapID, pid, dmg)
		}
		s.sendPush(ch, protocol.OpM2C_BattleSkillRet, &protocol.M2C_BattleSkillRet{
			UnitId:        m.id,
			ChangeHpValue: -dmg,
		})
		// 20078 负责受击/飘字；20169 向现有怪物单位同步血量。
		s.pushMonsterHP(ch, m)
		log.Printf("[S=%d] player skill %d hits monster %d dmg=%d hp=%d/%d",
			ch.id, skillID, m.id, dmg, m.hp, m.maxHP)
		if m.hp <= 0 {
			m.hp = 0
			m.alive = false
			if b.selectedID == m.id {
				b.selectedID = 0
			}
			s.sendPush(ch, protocol.OpM2C_MainstoryMonsterDead, &protocol.M2C_MainstoryMonsterDead{UnitId: m.id})
		}
	}
	// 4) CD 转圈（20237）
	s.sendPush(ch, protocol.OpM2C_StartCD, &protocol.M2C_StartCD{
		Id:      skillID,
		SkillCD: spec.cd,
		Type:    protocol.MainUIType_SkillSlot,
	})

	// 5) 全灭 → 胜利
	if len(b.aliveMonsters()) == 0 {
		b.ended = true
		s.emitVictory(ch, b)
		return resp
	}

	// 6) 怪物反击（存活怪物各打玩家一次）
	s.monstersAttack(ch, b)
	return resp
}

// playerDamage：按等级行的百分比倍率结算玩家技能伤害。
// 公式：max(1, 玩家攻击 - 目标防御) × multiplier / 100。
func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func (b *battleState) playerDamage(multiplier float64, def int32, spiritual bool) int32 {
	atk := b.phyAtk
	if spiritual {
		atk = b.spiAtk
	}
	base := atk - def
	if base < 1 {
		base = 1
	}
	d := float64(base) * multiplier / 100
	if d < 1 {
		d = 1
	}
	return int32(d)
}

func (b *battleState) attackMultiplierDamage(spec skillCastSpec, target *monsterUnit) int32 {
	def := target.phyDef
	if spec.spiritualHit {
		def = target.spiDef
	}
	if spec.ignoreDefense {
		def = 0
	}
	return b.playerDamage(spec.damageValue, def, spec.spiritualHit)
}

func (b *battleState) aliveMonsters() []*monsterUnit {
	var out []*monsterUnit
	for _, m := range b.monsters {
		if m.alive {
			out = append(out, m)
		}
	}
	return out
}

// emitVictory：胜利结算 —— 20054 BattleVictory + 20084 SendReward。
// 同时累加击杀计数（KillSpecial 任务用），并持久化任务状态。
func (s *Server) emitVictory(ch *channel, b *battleState) {
	pid := ch.session.playerID
	logCombatHP(ch.session, "victory", fmt.Sprintf("S=%d region=%d map=%d copy=%d leftover=%d/%d",
		ch.id, b.region, b.mapID, b.copyID, b.playerHP, b.playerMaxHP))
	syncBattleHealthToSession(ch.session) // 胜利残血写回 → 非战斗吃药可恢复
	// 魔法球（战斗结束自动回满）：胜利结算触发（活跃 buff 一次性生效）。
	s.applyMagicBallRecover(ch)
	// 单人打怪（battle.party == nil）的残血写回只推给自己，队友头像要等下一次
	// 重建才对齐；这里排一次锁外补推（见 team_vital_sync.go）。队伍战斗已经
	// 全队广播，不用重复。
	if b.party == nil {
		s.scheduleTeamVitalSync(ch)
	}
	logCombatHP(ch.session, "victory-sync", fmt.Sprintf("S=%d leftover=%d/%d", ch.id, ch.session.battleHP(), sessionCombatMaxHP(ch.session)))
	var exp int64
	for _, m := range b.monsters {
		if !m.alive {
			exp += m.exp
		}
	}
	s.recordTaskMonsterKills(ch.session, b.monsters)
	beachLayerCompleted := markBeachLayerVictory(ch.session, b.region)
	mainStoryCompletedNow := false
	if b.activity == nil && (b.copyID == 10001 || b.copyID == 10003) &&
		tables != nil && tables.mainStory != nil && tables.mainStory[int64(b.region)] != nil {
		mainStoryCompletedNow = markMainStoryVictory(ch.session, b.region)
	}
	mainStoryFinalCompleted := b.activity == nil &&
		(b.copyID == 10001 || b.copyID == 10003) &&
		isMainStoryFinalStageMap(ch.session.mapID)
	if mainStoryFinalCompleted {
		resetMainStoryResumeAfterFinalVictory(ch.session, ch.session.mapID)
	}
	mainStoryAIRunning := b.activity == nil && ch.session.mainStoryAIRunning &&
		ch.session.mapID/100 != 10006
	if beachLayerCompleted {
		log.Printf("[S=%d] beach layer %d completed", ch.id, ch.session.mapID%100)
	}
	// 击杀计数变化 → 刷新涉及任务的显示状态（Running → Completed 可提交），
	// 客户端任务面板/追踪随 M2C_SendTaskState 更新（"1/3、2/3"进度由 GetTask 推送）。
	s.pushTaskProgressAfterKill(ch)
	// Copper is an explicit 110203 entry in MonsterBase's drop chain. Deriving
	// it from experience made the reward popup advertise money that was never
	// committed to the character and granted money to copies with no coin drop.
	coin := int64(0)
	var rewardItems []*protocol.RewardItem
	var drops trialReward
	if b.battleType == trialBattleType {
		drops = trialBattleDrops(b)
		exp += drops.exp
		coin = drops.coin
		rewardItems = s.applyTrialReward(ch, drops, false)
	} else if b.activity != nil || copyVictoryAwardsMonsterDrops(b.copyID) {
		// The original CopyRewordConfig enables MonsterBase drops for main-story,
		// boss, manual-equipment, idle and space-travel victories. Activities use
		// the same chain plus their dedicated rewards. Keep every bag/currency
		// mutation transactional through the existing staging helper.
		drops = battleVictoryDrops(b)
		exp += drops.exp
		coin += drops.coin
		rewardItems = s.applyTrialReward(ch, drops, false)
		if b.activity != nil {
			s.awardStarSoulActivity(ch, b)
		}
	}
	s.settleFamilyBossBattle(ch, b, true)
	if recordManualEquipVictory(ch.session, b.mapID) {
		tier, layer, _ := manualEquipMapInfo(b.mapID)
		log.Printf("[S=%d] manual equipment tier=%d cleared layer=%d", ch.id, tier, layer)
	}
	s.saveData(ch)
	displayExp := s.gainExp(ch, exp)
	ch.session.idleBattle = false
	if mainStoryFinalCompleted {
		// The current combat worker must stop before returning to town. A running
		// main-story AI keeps its own generation alive so it can restart this
		// chapter at layer one after the town scene has finished loading.
		ch.session.autoBattle = false
		ch.session.autoBattleEpoch++
		ch.session.autoBattleNextCastAt = time.Time{}
		if !mainStoryAIRunning {
			ch.session.mainStoryAIRunning = false
			ch.session.mainStoryAIEpoch++
		}
	}
	if b.battleType == trialBattleType {
		s.recordTrialVictory(ch, b.trialCopyID)
	}
	// 世界 BOSS 击杀：10010 场景战斗胜利且击杀了该层 BOSS 单位 →
	// 标记死亡 + 全服广播（BossDead 移除单位 / BossBeDefeat / 世界聊天"谁击败了 BOSS"）。
	if layer := bossLayerForMapID(b.mapID); layer > 0 {
		markAndBroadcast := func() {
			participants := bossKillParticipants(ch, b)
			monsterID := int32(0)
			if row, ok := tables.bossBase[int64(1000+layer)]; ok {
				monsterID = int32(num(row["MonsterId"]))
			}
			for _, m := range b.monsters {
				if !m.alive && (monsterID == 0 || m.monsterID == monsterID) {
					markBossDead(layer, bossKillParticipantNames(participants))
					s.broadcastBossKill(participants, layer)
					break
				}
			}
		}
		if b.party != nil {
			b.party.bossOnce.Do(markAndBroadcast)
		} else {
			markAndBroadcast()
		}
	}
	log.Printf("[S=%d] battle victory region=%d exp=%d coin=%d", ch.id, b.region, displayExp, coin)
	s.sendBattleVictory(ch, b)
	s.sendReward(ch, &protocol.M2C_SendReward{
		ItemList: rewardItems, Exp: displayExp, Coin: coin, ActorId: pid,
	})
	if beachLayerCompleted {
		// Main-story battles created from the native encounter path may not carry
		// a map id (the battle is tied to the player's current scene instead).
		// Use that authoritative scene id when scheduling the post-victory portal;
		// otherwise the delayed callback compares against mapID=0 and is discarded
		// until the next login rebuilds the scene startup state.
		s.scheduleBeachTransferPoint(ch, ch.session.mapID)
	}
	if mainStoryCompletedNow && !beachLayerCompleted && !mainStoryFinalCompleted {
		s.scheduleMainStoryTransferPoint(ch, ch.session.mapID)
	}
	ch.session.battle = nil
	if mainStoryFinalCompleted {
		completedMapID := ch.session.mapID
		if mainStoryAIRunning || b.partyFinalReturnPending {
			// The automatic runner must physically reach the final layer's exit
			// portal before entering town. Party settlement keeps every member in
			// this scene and schedules the leader-owned transition once after all
			// battles detach; solo settlement can schedule it here.
			if b.party == nil {
				time.AfterFunc(0, func() {
					s.scheduleMainStoryAIReturnToCity(ch, completedMapID)
				})
			}
			return
		}
		x, y := mainCityReturnSpawn()
		s.changeMap(ch, 10004, x, y)
		log.Printf("[S=%d] main-story final layer completed; return to city", ch.id)
		return
	}
	if mainStoryAIRunning {
		// Party victories are advanced once by finishPartyVictory after every
		// member has received the current reward. A solo AI run can schedule its
		// next layer immediately after this battle is detached.
		if b.party == nil {
			// emitVictory is normally called while battleMu is held by the
			// combat ticker or an input handler. Defer the continuation so it can
			// take the same lock for its epoch/map validation.
			time.AfterFunc(0, func() { s.scheduleMainStoryAINext(ch) })
		}
		return
	}
	if b.battleType == trialBattleType {
		// Party settlement emits every member's rewards first. Moving anyone here
		// would rebuild TeamComponent while another member is still in combat.
		if b.party != nil {
			return
		}
		s.advanceAfterTrialVictory(ch, b)
		return
	}
	if b.activity != nil {
		// A party is advanced once after every member has received the current
		// stage reward; solo activity stages are chained after this battle has
		// been detached so the next presentation cannot observe the old state.
		if b.party != nil {
			return
		}
		s.continueSoloActivity(ch, b)
		return
	}
	// 注意：胜利后【不】自动进入下一层——线上逻辑是玩家手动走到本层末尾的
	// 光圈（传送点）点击后经 C2M_RequestEnterMap(20031) 换层。自动切层会导致
	// 任务链（第 2 层荔枝JJ 交任务）之外的行为偏差（用户 2026-08-14 确认）。
}

// pushMonsterHP：用 20169 更新已经存在的怪物单位数值。
// 20053 是断线恢复时的整场重建消息，普通伤害发送它会重复创建同 ID 怪物。
func (s *Server) pushMonsterHP(ch *channel, m *monsterUnit) {
	if ch == nil || ch.session == nil || m == nil {
		return
	}
	hp := m.hp
	if hp < 0 {
		hp = 0
	}
	for _, attr := range []struct {
		numericType int32
		value       int32
	}{
		{numericType: 1002, value: m.maxHP},
		{numericType: 1001, value: hp},
	} {
		s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: m.id, NumericType: attr.numericType, Value: float32(attr.value), ActorId: ch.session.playerID,
		})
	}
}

// pushMonsterOpeningHP initializes the NumericComponent for newly-created
// battle units. MonsterUnitInfo only carries id/model data, so without this
// first snapshot the stock HUD can retain its prefab default (10000) until
// the monster takes damage.
func (s *Server) pushMonsterOpeningHP(ch *channel, units []*monsterUnit) {
	for _, unit := range units {
		s.pushMonsterHP(ch, unit)
	}
}

// pushMonsterHPByID：按战斗怪 id 推 Numeric 血量（runtime 伤害事件用）。
func (s *Server) pushMonsterHPByID(ch *channel, battle *battleState, monsterID int64) {
	if battle == nil {
		return
	}
	for _, m := range battle.monsters {
		if m.id == monsterID {
			s.pushMonsterHP(ch, m)
			if ch != nil && ch.session != nil {
				persistFamilyBossProgress(ch.session.familyID, battle)
			}
			return
		}
	}
}

// 20235 → 20236：使用主界面物品槽。实现在 mainui.go（物品槽消耗/喂宠物/刷新槽位与背包）。

// 20069 → 20070：自动战斗开关。技能选择与执行见 auto_battle.go。
func (s *Server) onAutoBattle(ch *channel, req *protocol.C2M_AutoBattle) proto.Message {
	resp := &protocol.M2C_AutoBattle{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	ch.session.battleMu.Lock()
	ch.session.autoBattleEnabled = req.IsAuto
	active := ch.session.battle != nil && !ch.session.battle.ended
	ch.session.battleMu.Unlock()
	epoch := ch.session.setAutoBattle(req.IsAuto && active)
	s.saveData(ch)
	if req.IsAuto && active {
		ch.session.battleMu.Lock()
		b := ch.session.battle
		candidates := append([]int32(nil), ch.session.autoSkills...)
		var mp, maxMP int32
		if b != nil {
			mp, maxMP = b.playerMP, b.playerMaxMP
		}
		ch.session.battleMu.Unlock()
		log.Printf("[S=%d] auto battle start savedSkills=%v mp=%d/%d epoch=%d",
			ch.id, candidates, mp, maxMP, epoch)
		go s.runAutoBattle(ch, epoch)
	}
	log.Printf("[S=%d] auto battle isAuto=%v active=%v epoch=%d", ch.id, req.IsAuto, active, epoch)
	return resp
}

// 20071 → 20072：选择敌人。后续单体技能优先命中所选目标。
// ⚠ 幂等成功：客户端 ClickBattleTargetEvent 用 Session.Call await 此请求，Error≠0 会
// 抛 RpcException 刷屏并可能拖垮客户端（Player.log 实证 "Rpc error ... C2M_SelectEnermy"）。
// 非战斗/目标不存在时静默忽略，始终返回空 Message 成功。
func (s *Server) onSelectEnermy(ch *channel, req *protocol.C2M_SelectEnermy) proto.Message {
	resp := &protocol.M2C_SelectEnermy{RpcId: req.RpcId}
	if ch == nil || ch.session == nil {
		return resp
	}
	ch.session.battleMu.Lock()
	defer ch.session.battleMu.Unlock()
	b := ch.session.battle
	if b == nil || b.ended {
		return resp // 非战斗：忽略选择
	}
	for _, m := range b.monsters {
		if m.id == req.Id && m.alive {
			b.selectedID = req.Id
			log.Printf("[S=%d] select enemy id=%d", ch.id, req.Id)
			return resp
		}
	}
	// 目标不存在/已死亡：静默成功（不打断客户端点击）
	return resp
}
