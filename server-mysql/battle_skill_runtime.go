package main

import (
	"fmt"
	"log"
	"math"
	"math/rand"
	"sort"
	"time"

	"mhqserver/protocol"
)

var skillLogicCatalog *SkillLogicCatalog

const combatTickerInterval = 100 * time.Millisecond

// Keep the battle alive until the client's four-second monster-death coroutine finishes.
var battleVictoryDelay = 4200 * time.Millisecond

type battleEffectMetadata struct {
	modifierID int64
	durationMS int32
	// stateKey is the client's buff-bar slot (102 护盾 / 104 中毒 / 105 流血 …).
	// It is what M2C_GetBattleStateBuff reports; 客户端的状态特效按 modifier Id
	// 自己查表，服务端不下发 EffectId，所以这里不保留 effectID。
	stateKey int32
	iconID   string
	iconDesc string
	isBuff   bool
}

type skillExecutionContext struct {
	roll                  func() float64
	targetIntn            func(int) int
	damageMultiplier      float64
	castCount             int
	triggerEvent          int32
	triggerAmount         int32
	counterAttack         bool
	activeModifierEvents  map[modifierEventInvocation]bool
	session               *session
	statusAccuracyChecked bool
	missedDirectDamage    map[CombatUnitRef]bool
	// castTarget remembers who the running skill was originally cast at. A
	// modifier callback replaces `primary` with the event source, so
	// SkillTargetCastTarget would otherwise resolve to the wrong unit inside
	// hooks (310601 人体学 heals an ally and shields the healer instead). A
	// zero ref keeps the pre-existing `primary` fallback.
	castTarget CombatUnitRef
}

type activeSkillModifier struct {
	key     string
	source  CombatUnitRef
	holder  CombatUnitRef
	status  *SkillStatusPlan
	effects map[int32][]SkillEffect
	// castTarget is the skill's main target captured when the modifier was
	// applied. Callbacks reuse it so SkillTargetCastTarget still means "the
	// unit the skill was aimed at" while `primary` carries the event source.
	castTarget CombatUnitRef
}

type modifierEventInvocation struct {
	hookKey   string
	eventCode int32
}

func newSkillExecutionContext(battle *battleState, roll func() float64) *skillExecutionContext {
	if roll == nil {
		roll = rand.Float64
	}
	ctx := &skillExecutionContext{roll: roll, targetIntn: rand.Intn, damageMultiplier: 1, castCount: 1}
	if battle != nil {
		ctx.session = battle.owner
	}
	return ctx
}

// useSkillPlanLocked 对外包装：返回响应（业务失败 Error=0 防客户端崩）。
// 手动点击快捷栏技能槽：SlotId → 槽内技能 ID → castSkillLocked。
func (s *Server) useSkillPlanLocked(ch *channel, req *protocol.C2M_UseMainUISkill, resp *protocol.M2C_UseMainUISkill) *protocol.M2C_UseMainUISkill {
	out, _ := s.useSkillPlanLockedEx(ch, req, resp)
	return out
}

// useSkillPlanLockedEx：cast 成功返回 true（自动战斗 fallback 判断用）。
// 响应恒 Error=0（客户端 Session.Call 对 Error!=0 抛异常崩溃，Player.log 实证
// MainUI.cs:332），业务失败仅打日志 + 状态不变。
func (s *Server) useSkillPlanLockedEx(ch *channel, req *protocol.C2M_UseMainUISkill, resp *protocol.M2C_UseMainUISkill) (*protocol.M2C_UseMainUISkill, bool) {
	if ch == nil || ch.session == nil {
		return resp, false
	}
	ss := ch.session
	if ss.autoBattle {
		resp.Message = "自动战斗中不能手动释放技能"
		return resp, false
	}
	sl := ss.mainUISlotAt(req.SlotId)
	if sl.Type != 1 || sl.Id <= 0 {
		log.Printf("[S=%d] skill cast rejected: bad slot %d", ch.id, req.SlotId)
		return resp, false
	}
	return s.castSkillLocked(ch, sl.Id, resp)
}

// castSkillLocked 对 skillID 执行一次技能释放（战斗校验/资源/CD/效果）。
// 手动点击（经 useSkillPlanLockedEx 查槽）与自动战斗（autoBattleSkillIDsLocked）
// 共用。响应恒 Error=0（客户端 Session.Call 对 Error!=0 抛异常崩溃），
// 业务失败仅打日志 + 状态不变。
func (s *Server) castSkillLocked(ch *channel, skillID int32, resp *protocol.M2C_UseMainUISkill) (*protocol.M2C_UseMainUISkill, bool) {
	return s.castSkillLockedWithLog(ch, skillID, resp, true)
}

func (s *Server) castSkillLockedWithLog(ch *channel, skillID int32, resp *protocol.M2C_UseMainUISkill, logRejected bool) (*protocol.M2C_UseMainUISkill, bool) {
	return s.castSkillLockedWithLogAt(ch, skillID, resp, logRejected, time.Now())
}

func (s *Server) castSkillLockedWithLogAt(ch *channel, skillID int32, resp *protocol.M2C_UseMainUISkill, logRejected bool, now time.Time) (*protocol.M2C_UseMainUISkill, bool) {
	return s.castSkillLockedWithLogAtHeld(ch, skillID, resp, logRejected, now, false)
}

func (s *Server) castSkillLockedWithLogAtHeld(ch *channel, skillID int32, resp *protocol.M2C_UseMainUISkill, logRejected bool, now time.Time, heldLocks bool) (*protocol.M2C_UseMainUISkill, bool) {
	reject := func(reason string) (*protocol.M2C_UseMainUISkill, bool) {
		if logRejected {
			log.Printf("[S=%d] skill %d cast rejected: %s", ch.id, skillID, reason)
		}
		return resp, false
	}
	if ch == nil || ch.session == nil {
		return resp, false
	}
	ss := ch.session
	battle := ss.battle
	if battle == nil || battle.ended {
		return reject("not in battle")
	}
	if battle.scenePending {
		return reject("battle scene is loading")
	}
	if now.Before(ss.battleActionReadyAt) {
		return reject("public cooldown")
	}
	if !heldLocks && battle.pvp != nil {
		battle.pvp.mu.Lock()
		defer battle.pvp.mu.Unlock()
		if battle.ended || battle.pvp.settled {
			return reject("pvp ended")
		}
	}
	if !heldLocks && battle.party != nil {
		battle.party.mu.Lock()
		defer battle.party.mu.Unlock()
		if battle.ended {
			return reject("battle ended")
		}
	}
	if heldLocks && battle.pvp != nil && (battle.ended || battle.pvp.settled) {
		return reject("pvp ended")
	}
	if heldLocks && battle.party != nil && battle.ended {
		return reject("battle ended")
	}
	battle.owner = ss
	if err := ensureSkillLogicCatalog(); err != nil {
		return reject("catalog: " + err.Error())
	}
	level, learned := ss.skills[skillID]
	if !learned || level <= 0 {
		return reject("not learned")
	}
	plan, err := skillLogicCatalog.Plan(skillID, level)
	if err != nil {
		return reject(err.Error())
	}
	plan = withConfiguredSkillPresentation(plan)
	if !skillPlanCanCast(plan) {
		return reject("passive")
	}
	if battle.runtime == nil {
		battle.runtime = NewCombatRuntime(battle, ss.playerID, nil)
	}
	if !heldLocks {
		tickEvents := battle.runtime.Tick()
		tickHooks, tickErr := battle.triggerTimedEventHooks(tickEvents)
		if tickErr != nil {
			return reject(tickErr.Error())
		}
		s.emitCombatEvents(ch, battle, append(tickEvents, tickHooks...))
	}
	if !battle.runtime.CanCast(battle.runtime.Player()) {
		return reject("cannot cast")
	}
	primary, ok := battle.playerSkillPrimaryTarget(plan)
	if !ok {
		return reject("no target")
	}
	resourceCast := plan.Cast
	mpCost, hpCost, err := battle.skillResourceCost(resourceCast)
	if err != nil {
		return reject(err.Error())
	}
	cooldownMS := clientCombatCooldownMS(plan.CooldownMS)
	startCDTime := clientPublicActionIntervalMS(ss)
	previousCooldown, hadPreviousCooldown := ss.skillCooldowns[skillID]
	if _, ready := ss.tryStartSkillCooldown(skillID, now, time.Duration(cooldownMS)*time.Millisecond); !ready {
		return reject("cooldown")
	}
	battle.playerMP -= mpCost
	battle.playerHP -= hpCost
	player := battle.runtime.Player()
	deferred := battle.pvp == nil && skillPlanDealsDamage(plan)
	var events []CombatEvent
	if !deferred {
		events, err = battle.executeSkillPlan(plan, player, primary, rand.Float64)
	}
	if err != nil {
		battle.playerMP += mpCost
		battle.playerHP += hpCost
		if hadPreviousCooldown {
			ss.skillCooldowns[skillID] = previousCooldown
		} else {
			delete(ss.skillCooldowns, skillID)
		}
		return reject(err.Error())
	}
	if !deferred {
		recoveryEvents, _ := battle.runtime.ApplyHealthRecovery(player)
		events = append(events, recoveryEvents...)
	}
	ss.battleActionReadyAt = nextPlayerActionReadyAt(ss, now)
	if ss.autoBattle {
		ss.autoBattleNextCastAt = ss.battleActionReadyAt
	}
	if battle.pvp != nil {
		syncPVPStateLocked(battle)
	}
	playSkill := playerSkillPush{
		unitID: ss.playerID, skillID: skillID,
	}
	if battle.assemblingWave != nil {
		battle.assemblingWave.playerSkillPushes = append(battle.assemblingWave.playerSkillPushes, playSkill)
	} else {
		for _, recipient := range s.battleRecipients(ch, battle) {
			s.sendPush(recipient, protocol.OpM2C_PlaySkill, &protocol.M2C_PlaySkill{
				UnitId: playSkill.unitID, SkillId: playSkill.skillID,
			})
		}
	}
	if mpCost > 0 {
		for _, recipient := range s.battleRecipients(ch, battle) {
			s.sendPush(recipient, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
				UnitId: ss.playerID, NumericType: 1003, Value: float32(battle.playerMP), ActorId: recipient.session.playerID,
			})
		}
	}
	if hpCost > 0 {
		for _, recipient := range s.battleRecipients(ch, battle) {
			s.sendPush(recipient, protocol.OpM2C_BattleSkillRet, &protocol.M2C_BattleSkillRet{UnitId: ss.playerID, ChangeHpValue: -hpCost})
			s.pushCombatHP(recipient, ss.playerID, battle.playerHP)
		}
	}
	if deferred {
		s.schedulePlayerAttack(ch, battle, plan, primary)
	} else if battle.pvp == nil {
		s.emitCombatEvents(ch, battle, events)
	} else {
		s.emitPlayerSkillEvents(ch, battle, plan, events)
	}
	remainingCD := int32(0)
	if _, ok := ss.skillCooldowns[skillID]; ok {
		// The configured global-CD reset removes the map entry and sends zero.
		remainingCD = cooldownMS
	}
	s.sendPush(ch, protocol.OpM2C_StartCD, &protocol.M2C_StartCD{
		Time: startCDTime, Id: skillID, SkillCD: remainingCD, Type: protocol.MainUIType_SkillSlot,
	})
	log.Printf("[S=%d] skill %d cast resourceType=%d resourceValue=%.4f mpCost=%d hpCost=%d mp=%d/%d",
		ch.id, skillID, resourceCast.SkillCastType, resourceCast.SkillCast,
		mpCost, hpCost, battle.playerMP, battle.playerMaxMP)
	if s.settleCombatLocked(ch, battle) {
		return resp, true
	}
	if battle.pvp != nil {
		return resp, true // the opposing player supplies the next turn
	}
	return resp, true
}

func skillPlanCanCast(plan SkillPlan) bool {
	return plan.SkillType != 1 && skillPlanHasTrigger(plan, 1)
}

func skillPlanHasTrigger(plan SkillPlan, event int32) bool {
	for _, effect := range plan.Effects {
		if effect.Trigger.Scope == "skill" && effect.Trigger.Event == event {
			return true
		}
	}
	return false
}

func ensureSkillLogicCatalog() error {
	if skillLogicCatalog != nil {
		return nil
	}
	return fmt.Errorf("SkillLogicConfig is not loaded from MySQL")
}

func (battle *battleState) initializePassiveSkills(ss *session, roll func() float64) ([]CombatEvent, error) {
	if battle == nil || battle.runtime == nil || ss == nil {
		return nil, ErrCombatEnded
	}
	if err := ensureSkillLogicCatalog(); err != nil {
		return nil, err
	}
	ids := make([]int32, 0, len(ss.skills))
	for skillID, level := range ss.skills {
		if level > 0 {
			ids = append(ids, skillID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var events []CombatEvent
	player := battle.runtime.Player()
	for _, skillID := range ids {
		plan, err := skillLogicCatalog.Plan(skillID, ss.skills[skillID])
		if err != nil {
			return events, err
		}
		if !skillPlanHasTrigger(plan, 8) {
			continue
		}
		applied, err := battle.executeSkillTrigger(plan, 8, player, player, roll)
		events = append(events, applied...)
		if err != nil {
			return events, err
		}
	}
	return events, nil
}

func (battle *battleState) playerSkillPrimaryTarget(plan SkillPlan) (CombatUnitRef, bool) {
	if battle == nil || battle.runtime == nil {
		return CombatUnitRef{}, false
	}
	if plan.TeamType == 2 {
		if battle.selectedAllyID != 0 {
			selected := PlayerCombatUnit(battle.selectedAllyID)
			if battle.runtime.CanBeTargeted(selected) {
				return selected, true
			}
		}
		var selected CombatUnitRef
		var selectedHP, selectedMax int32
		for _, candidate := range battle.alliedPlayerRefs() {
			unit, ok := battle.runtime.Unit(candidate)
			if !ok || !unit.Alive {
				continue
			}
			if selected.IsZero() || int64(unit.HP)*int64(selectedMax) < int64(selectedHP)*int64(unit.MaxHP) {
				selected, selectedHP, selectedMax = candidate, unit.HP, unit.MaxHP
			}
		}
		return selected, !selected.IsZero()
	}
	if battle.selectedID != 0 {
		selected := MonsterCombatUnit(battle.selectedID)
		if battle.runtime.CanBeTargeted(selected) {
			return selected, true
		}
	}
	for _, monster := range battle.monsters {
		ref := MonsterCombatUnit(monster.id)
		if battle.runtime.CanBeTargeted(ref) {
			return ref, true
		}
	}
	return CombatUnitRef{}, false
}

func (battle *battleState) executeSkillPlan(plan SkillPlan, source, primary CombatUnitRef, roll func() float64) ([]CombatEvent, error) {
	return battle.executeSkillPlanWithContext(plan, source, primary, newSkillExecutionContext(battle, roll))
}

func (battle *battleState) executeSkillPlanWithContext(plan SkillPlan, source, primary CombatUnitRef, ctx *skillExecutionContext) ([]CombatEvent, error) {
	if ctx == nil {
		ctx = newSkillExecutionContext(battle, nil)
	}
	var events []CombatEvent
	precast, err := battle.executeSkillTriggerWithContext(plan, 16, source, primary, ctx)
	events = append(events, precast...)
	if err != nil {
		return events, err
	}
	if skillPlanDealsDamage(plan) {
		attackStart, err := battle.triggerModifierEvent(4, source, primary, ctx, 0)
		events = append(events, attackStart...)
		if err != nil {
			return events, err
		}
	}
	count := ctx.castCount
	if count < 1 {
		count = 1
	}
	if count > 10 {
		count = 10
	}
	for i := 0; i < count; i++ {
		castEvents, castErr := battle.executeSkillTriggerWithContext(plan, 1, source, primary, ctx)
		events = append(events, castEvents...)
		if castErr != nil {
			return events, castErr
		}
	}
	return events, nil
}

func skillPlanDealsDamage(plan SkillPlan) bool {
	var contains func(SkillEffect) bool
	contains = func(effect SkillEffect) bool {
		if effect.Kind == SkillEffectDamage {
			return true
		}
		for _, list := range [][]SkillEffect{effect.Success, effect.Failure, effect.Children} {
			for _, child := range list {
				if contains(child) {
					return true
				}
			}
		}
		return false
	}
	for _, effect := range plan.Effects {
		if effect.Trigger.Scope == "skill" && effect.Trigger.Event == 1 && contains(effect) {
			return true
		}
	}
	return false
}

func (battle *battleState) executeSkillTrigger(plan SkillPlan, triggerEvent int32, source, primary CombatUnitRef, roll func() float64) ([]CombatEvent, error) {
	return battle.executeSkillTriggerWithContext(plan, triggerEvent, source, primary, newSkillExecutionContext(battle, roll))
}

func (battle *battleState) executeSkillTriggerWithContext(plan SkillPlan, triggerEvent int32, source, primary CombatUnitRef, ctx *skillExecutionContext) ([]CombatEvent, error) {
	if battle == nil || battle.runtime == nil {
		return nil, ErrCombatEnded
	}
	if ctx == nil {
		ctx = newSkillExecutionContext(battle, nil)
	}
	var events []CombatEvent
	matching := make([]SkillEffect, 0, len(plan.Effects))
	for _, effect := range plan.Effects {
		if effect.Trigger.Scope == "skill" && effect.Trigger.Event == triggerEvent {
			matching = append(matching, effect)
		}
	}
	// A projectile resolves after the remaining cast options. Execute self
	// context modifiers first so short-lived damage/cast-count modifiers affect
	// that projectile even though this server settles the hit synchronously.
	if triggerEvent == 1 {
		sort.SliceStable(matching, func(i, j int) bool {
			return skillEffectContainsCastModifier(matching[i]) && !skillEffectContainsCastModifier(matching[j])
		})
	}
	for _, effect := range matching {
		applied, err := battle.executeSkillEffectAtContext(effect, source, primary, primary, nil, ctx, 0)
		events = append(events, applied...)
		if err != nil {
			return events, err
		}
	}
	return events, nil
}

func skillEffectContainsCastModifier(effect SkillEffect) bool {
	if effect.Kind == SkillEffectStatus {
		for _, child := range effect.Children {
			if child.Trigger.Event == 0 && (child.Kind == SkillEffectChangeDamage || child.Kind == SkillEffectChangeCastCount) {
				return true
			}
		}
	}
	for _, list := range [][]SkillEffect{effect.Success, effect.Failure, effect.Children} {
		for _, child := range list {
			if skillEffectContainsCastModifier(child) {
				return true
			}
		}
	}
	return false
}

func (battle *battleState) executeSkillEffect(effect SkillEffect, source, primary, holder CombatUnitRef, parent *SkillStatusPlan, roll func() float64) ([]CombatEvent, error) {
	return battle.executeSkillEffectAtContext(effect, source, primary, holder, parent, newSkillExecutionContext(battle, roll), 0)
}

func (battle *battleState) executeSkillEffectAt(effect SkillEffect, source, primary, holder CombatUnitRef, parent *SkillStatusPlan, roll func() float64, inheritedDelayMS int32) ([]CombatEvent, error) {
	return battle.executeSkillEffectAtContext(effect, source, primary, holder, parent, newSkillExecutionContext(battle, roll), inheritedDelayMS)
}

func (battle *battleState) executeSkillEffectAtContext(effect SkillEffect, source, primary, holder CombatUnitRef, parent *SkillStatusPlan, ctx *skillExecutionContext, inheritedDelayMS int32) ([]CombatEvent, error) {
	if ctx == nil {
		ctx = newSkillExecutionContext(battle, nil)
	}
	switch effect.Kind {
	case SkillEffectChance:
		chance, checksStatusAccuracy := battle.contestedStatusChance(effect.ChancePercent, effect.Success, source, primary, holder)
		branch := effect.Failure
		passed := ctx.roll()*100 < chance
		if passed {
			branch = effect.Success
		}
		previousCheck := ctx.statusAccuracyChecked
		ctx.statusAccuracyChecked = previousCheck || (passed && checksStatusAccuracy)
		events, err := battle.executeSkillEffectsAtContext(branch, source, primary, holder, parent, ctx, inheritedDelayMS)
		ctx.statusAccuracyChecked = previousCheck
		return events, err
	case SkillEffectDelay:
		return battle.executeSkillEffectsAtContext(effect.Children, source, primary, holder, parent, ctx, addDelayMS(inheritedDelayMS, effect.DelayMS))
	case SkillEffectProjectile:
		delayMS := addDelayMS(inheritedDelayMS, effect.DelayMS)
		// The client owns projectile interpolation. Send the configured projectile
		// effect and let its PlayBulletEffect handler move it to the target. The
		// impact is a separate immediate visual, matching the online wire order.
		events := battle.skillVisualEvents(effect, source, primary, holder, delayMS)
		if effect.ImpactEffectID > 0 {
			impact := effect
			impact.EffectID = effect.ImpactEffectID
			events = append(events, battle.skillVisualEvents(impact, source, primary, holder, projectileImpactDurationMS(effect))...)
		}
		children, err := battle.executeSkillEffectsAtContext(effect.Children, source, primary, holder, parent, ctx, delayMS)
		return append(events, children...), err
	case SkillEffectVisual:
		if configuredEffectType(effect.EffectID) == 4 {
			inheritedDelayMS = modifierVisualDurationMS(parent)
		}
		return battle.skillVisualEvents(effect, source, primary, holder, inheritedDelayMS), nil
	case SkillEffectChangeGlobalCD:
		return battle.changeGlobalCooldown(effect.ParamPercent, source, ctx), nil
	case SkillEffectChangeDamage:
		return battle.changeDamageMultiplier(effect, source, parent, ctx)
	case SkillEffectChangeCastCount:
		ctx.castCount = clampCastCount(effect.ParamPercent)
		return nil, nil
	case SkillEffectModifyStatus:
		return battle.modifyStatusFromHealth(effect, holder, parent)
	}
	targets := battle.resolveSkillTargetsWithIntn(effect.Target, source, primary, holder, ctx.castTarget, ctx.targetIntn)
	if len(targets) == 0 {
		return nil, nil
	}
	var events []CombatEvent
	for _, target := range targets {
		var applied []CombatEvent
		var err error
		var treatmentAmount int32
		var treatmentCritical bool
		switch effect.Kind {
		case SkillEffectDamage:
			if !battle.directDamageHits(effect.Damage, source, target, ctx) {
				continue
			}
			amount, critical := battle.damageOutcome(effect.Damage, source, target, ctx)
			if amount > 0 {
				if effect.Damage.Periodic && parent != nil {
					applied, err = battle.applyPeriodic(effect, parent, source, target, amount, true)
				} else {
					applied, err = battle.runtime.ApplyDamage(CombatDamageRequest{Source: source, Target: target, Amount: amount, IsCrit: critical, Counter: ctx.counterAttack})
				}
			}
		case SkillEffectHeal:
			if effect.Treatment.Periodic && parent != nil {
				treatmentAmount = battle.treatmentAmount(effect.Treatment, source, target)
				if treatmentAmount > 0 {
					applied, err = battle.applyPeriodic(effect, parent, source, target, treatmentAmount, false)
				}
			} else {
				treatmentAmount, treatmentCritical = battle.treatmentOutcome(effect.Treatment, source, target, ctx)
				if treatmentAmount > 0 {
					applied, err = battle.runtime.ApplyHeal(source, target, treatmentAmount)
					markTreatmentEventsCritical(applied, treatmentCritical)
				}
			}
		case SkillEffectShield:
			treatmentAmount, treatmentCritical = battle.treatmentOutcome(effect.Treatment, source, target, ctx)
			if treatmentAmount > 0 {
				duration := time.Duration(0)
				if parent != nil {
					duration = secondsDuration(parent.DurationSeconds)
				}
				key := battle.registerEffectMeta(effect.ModifierID, "shield", parent)
				applied, err = battle.runtime.ApplyEffects(CombatEffectContext{Source: source, Target: target}, []CombatEffect{modifierEffectSpec(parent, EffectSpec{
					Key: key, Kind: CombatEffectShield, Value: treatmentAmount, Duration: duration,
					StackMode: stackMode(parent), MaxStacks: gameplayEffectMaxStacks(),
				})})
				markTreatmentEventsCritical(applied, treatmentCritical)
			}
		case SkillEffectStatus:
			applied, err = battle.applyStatusEffectWithContext(effect, source, target, primary, ctx)
		case SkillEffectCounter:
			applied, err = battle.applySkillCounter(effect, source, target, parent, ctx)
		case SkillEffectLifeSteal, SkillEffectReflect:
			if ctx.triggerEvent != 0 && ctx.triggerAmount > 0 {
				applied, err = battle.applyTriggeredReactive(effect, source, target, parent, ctx)
				break
			}
			kind := CombatEffectLifesteal
			if effect.Kind == SkillEffectReflect {
				kind = CombatEffectReflect
			}
			percent := effect.ParamPercent
			if percent <= 0 && parent != nil {
				percent = parent.Value
			}
			if percent <= 0 {
				percent = 100
			}
			key := battle.registerEffectMeta(effect.ModifierID, string(effect.Kind), parent)
			applied, err = battle.runtime.ApplyEffects(CombatEffectContext{Source: source, Target: target}, []CombatEffect{modifierEffectSpec(parent, EffectSpec{
				Key: key, Kind: kind, Percent: percent, Duration: statusDuration(parent),
				StackMode: stackMode(parent), MaxStacks: gameplayEffectMaxStacks(),
			})})
		}
		events = append(events, applied...)
		if err != nil {
			return events, err
		}
		if effect.Kind == SkillEffectDamage && effect.Damage != nil && !effect.Damage.Periodic {
			triggered, triggerErr := battle.triggerDamageEvents(source, target, applied, ctx)
			events = append(events, triggered...)
			if triggerErr != nil {
				return events, triggerErr
			}
		}
		if effect.Kind == SkillEffectHeal && effect.Treatment != nil && !effect.Treatment.Periodic {
			triggered, triggerErr := battle.triggerModifierEvent(23, target, source, ctx, combatEventAmount(applied, CombatEventHeal))
			events = append(events, triggered...)
			if triggerErr != nil {
				return events, triggerErr
			}
		}
		if treatmentCritical {
			triggerAmount := treatmentAmount
			if effect.Kind == SkillEffectHeal {
				triggerAmount = combatEventAmount(applied, CombatEventHeal)
			}
			if triggerAmount > 0 {
				triggered, triggerErr := battle.triggerModifierEvent(7, target, source, ctx, triggerAmount)
				events = append(events, triggered...)
				if triggerErr != nil {
					return events, triggerErr
				}
			}
		}
	}
	return events, nil
}

func (battle *battleState) executeSkillEffects(effects []SkillEffect, source, primary, holder CombatUnitRef, parent *SkillStatusPlan, roll func() float64) ([]CombatEvent, error) {
	return battle.executeSkillEffectsAtContext(effects, source, primary, holder, parent, newSkillExecutionContext(battle, roll), 0)
}

func (battle *battleState) executeSkillEffectsAt(effects []SkillEffect, source, primary, holder CombatUnitRef, parent *SkillStatusPlan, roll func() float64, inheritedDelayMS int32) ([]CombatEvent, error) {
	return battle.executeSkillEffectsAtContext(effects, source, primary, holder, parent, newSkillExecutionContext(battle, roll), inheritedDelayMS)
}

func (battle *battleState) executeSkillEffectsAtContext(effects []SkillEffect, source, primary, holder CombatUnitRef, parent *SkillStatusPlan, ctx *skillExecutionContext, inheritedDelayMS int32) ([]CombatEvent, error) {
	var events []CombatEvent
	for _, child := range effects {
		applied, err := battle.executeSkillEffectAtContext(child, source, primary, holder, parent, ctx, inheritedDelayMS)
		events = append(events, applied...)
		if err != nil {
			return events, err
		}
	}
	return events, nil
}

func (battle *battleState) skillVisualEvents(effect SkillEffect, source, primary, holder CombatUnitRef, delayMS int32) []CombatEvent {
	if effect.EffectID <= 0 || battle == nil || battle.runtime == nil {
		return nil
	}
	targets := battle.resolveSkillTargets(effect.Target, source, primary, holder)
	events := make([]CombatEvent, 0, len(targets))
	for _, target := range targets {
		event := battle.runtime.event(battle.runtime.clock.Now(), CombatEventVisual, source, target)
		event.DelayMS = delayMS
		event.EffectID = effect.EffectID
		event.EffectPos = effect.EffectPosition
		// EffectType is the resource's playback kind, not EffectTargetType.
		// The skill selector already resolved the actual recipient. The client
		// only implements target/caster (0/1); 2/3 leave an unplayed prefab.
		event.EffectTargetType = 0
		events = append(events, event)
	}
	return events
}

func addDelayMS(left, right int32) int32 {
	value := int64(left) + int64(right)
	if value <= 0 {
		return 0
	}
	if value > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(value)
}

func (battle *battleState) applyStatusEffect(effect SkillEffect, source, target, primary CombatUnitRef, roll func() float64) ([]CombatEvent, error) {
	return battle.applyStatusEffectWithContext(effect, source, target, primary, newSkillExecutionContext(battle, roll))
}

func (battle *battleState) applyStatusEffectWithContext(effect SkillEffect, source, target, primary CombatUnitRef, ctx *skillExecutionContext) ([]CombatEvent, error) {
	status := effect.Status
	if status == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = newSkillExecutionContext(battle, nil)
	}
	if statusPolarity(status) == CombatEffectHarmful && !ctx.statusAccuracyChecked && ctx.directDamageMissed(target) {
		return nil, nil
	}
	if statusPolarity(status) == CombatEffectHarmful && !ctx.statusAccuracyChecked &&
		ctx.roll()*100 >= battle.statusContestChance(100, source, target) {
		return nil, nil
	}
	if battle.modifierBlocked(target, status) {
		return []CombatEvent{battle.modifierImmuneEvent(source, target)}, nil
	}
	// Remember who the skill was aimed at. `primary` is that unit for a normal
	// cast, while inside a modifier callback it already carries the event
	// source, so the execution context wins when it has a value.
	castTarget := ctx.castTarget
	if castTarget.IsZero() {
		castTarget = primary
	}
	var events []CombatEvent
	for _, child := range effect.Children {
		if child.Trigger.Event != 24 {
			continue
		}
		// Event 24 is "before this modifier is created". Its options resolve
		// against the same context as event 0/8 below: source is the caster,
		// primary is the skill's cast target and holder is the unit receiving
		// the modifier. 210601 (猛虎式射门) relies on this: its bullet modifier
		// sits on the enemy and its event 24 grants "自身" +30% physical attack,
		// which must land on the sportsman, not on the enemy holding the bullet.
		applied, err := battle.executeSkillEffectAtContext(child, source, primary, target, status, ctx, 0)
		events = append(events, applied...)
		if err != nil {
			return events, err
		}
	}
	appliedPersistent := false
	for index, attribute := range combatAttributesForValueKey(status.ValueKey) {
		if status.Value == 0 {
			continue
		}
		key := battle.registerEffectMeta(status.ModifierID, fmt.Sprintf("attribute-%d-%d", attribute, index), status)
		applied, err := battle.runtime.ApplyEffects(CombatEffectContext{Source: source, Target: target}, []CombatEffect{modifierEffectSpec(status, EffectSpec{
			Key: key, Kind: CombatEffectAttribute, Attribute: attribute, Percent: status.Value,
			Duration: secondsDuration(status.DurationSeconds), StackMode: stackMode(status), MaxStacks: gameplayEffectMaxStacks(),
			Polarity: statusPolarity(status), Undispellable: !status.CanBeCleared,
		})})
		events = append(events, applied...)
		if err != nil {
			return events, err
		}
		appliedPersistent = true
	}
	if control := combatStatusForStateKey(status.StateKey); control != CombatStatusNone {
		key := battle.registerEffectMeta(status.ModifierID, "control", status)
		applied, err := battle.runtime.ApplyEffects(CombatEffectContext{Source: source, Target: target}, []CombatEffect{modifierEffectSpec(status, EffectSpec{
			Key: key, Kind: CombatEffectStatus, Status: control, Duration: secondsDuration(status.DurationSeconds),
			StackMode: stackMode(status), MaxStacks: gameplayEffectMaxStacks(), Polarity: statusPolarity(status),
			Undispellable: !status.CanBeCleared,
		})})
		events = append(events, applied...)
		if err != nil {
			return events, err
		}
		appliedPersistent = true
	}
	dynamic := make(map[int32][]SkillEffect)
	futurePersistent := false
	for _, child := range effect.Children {
		eventCode := child.Trigger.Event
		if eventCode == 24 {
			continue
		}
		installReactive := isReactiveSkillEffect(child.Kind) && (eventCode == 2 || eventCode == 5 || eventCode == 6)
		if eventCode != 0 && eventCode != 8 && !installReactive {
			dynamic[eventCode] = append(dynamic[eventCode], child)
		}
		futurePersistent = futurePersistent || eventCode == 8 || installReactive
	}
	if len(dynamic) > 0 {
		suffix, hidden := "marker", false
		if appliedPersistent || futurePersistent {
			suffix, hidden = "hook", true
		}
		key := battle.registerEffectMeta(status.ModifierID, suffix, status)
		applied, err := battle.runtime.ApplyEffects(CombatEffectContext{Source: source, Target: target}, []CombatEffect{modifierEffectSpec(status, EffectSpec{
			Key: key, Kind: CombatEffectStatus, Status: CombatStatusMarker,
			Duration: secondsDuration(status.DurationSeconds), StackMode: stackMode(status), MaxStacks: gameplayEffectMaxStacks(),
			Polarity: statusPolarity(status), Undispellable: !status.CanBeCleared, Hidden: hidden,
		})})
		events = append(events, applied...)
		if err != nil {
			return events, err
		}
		battle.storeModifierHook(key, source, target, castTarget, status, dynamic)
		appliedPersistent = true
	}
	for _, child := range effect.Children {
		eventCode := child.Trigger.Event
		if eventCode == 24 {
			continue
		}
		installReactive := isReactiveSkillEffect(child.Kind) && (eventCode == 2 || eventCode == 5 || eventCode == 6)
		if eventCode != 0 && eventCode != 8 && !installReactive {
			continue
		}
		previousTrigger, previousAmount := ctx.triggerEvent, ctx.triggerAmount
		ctx.triggerEvent, ctx.triggerAmount = 0, 0
		applied, err := battle.executeSkillEffectAtContext(child, source, primary, target, status, ctx, 0)
		ctx.triggerEvent, ctx.triggerAmount = previousTrigger, previousAmount
		events = append(events, applied...)
		if err != nil {
			return events, err
		}
		if eventCode == 8 || installReactive {
			appliedPersistent = true
		}
	}
	if !appliedPersistent && status.DurationSeconds != 0 {
		key := battle.registerEffectMeta(status.ModifierID, "marker", status)
		applied, err := battle.runtime.ApplyEffects(CombatEffectContext{Source: source, Target: target}, []CombatEffect{modifierEffectSpec(status, EffectSpec{
			Key: key, Kind: CombatEffectStatus, Status: CombatStatusMarker,
			Duration: secondsDuration(status.DurationSeconds), StackMode: stackMode(status), MaxStacks: gameplayEffectMaxStacks(),
			Polarity: statusPolarity(status), Undispellable: !status.CanBeCleared,
		})})
		events = append(events, applied...)
		if err != nil {
			return events, err
		}
	}
	return events, nil
}

func isReactiveSkillEffect(kind SkillEffectKind) bool {
	switch kind {
	case SkillEffectLifeSteal, SkillEffectReflect, SkillEffectCounter:
		return true
	default:
		return false
	}
}

func clampCastCount(value float64) int {
	if math.IsNaN(value) || value < 1 {
		return 1
	}
	if value > 10 {
		return 10
	}
	return int(math.Round(value))
}

func (battle *battleState) changeDamageMultiplier(effect SkillEffect, source CombatUnitRef, parent *SkillStatusPlan, ctx *skillExecutionContext) ([]CombatEvent, error) {
	value := effect.ParamPercent
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return nil, ErrInvalidEffect
	}
	if effect.Trigger.Event == 0 && parent != nil && parent.DurationSeconds != 0 {
		key := battle.registerEffectMeta(parent.ModifierID, "damage-multiplier", parent)
		return battle.runtime.ApplyEffects(CombatEffectContext{Source: source, Target: source}, []CombatEffect{modifierEffectSpec(parent, EffectSpec{
			Key: key, Kind: CombatEffectAttribute, Attribute: CombatAttributeDamageMultiplier,
			Percent: value, Duration: statusDuration(parent), StackMode: stackMode(parent), MaxStacks: gameplayEffectMaxStacks(),
			Polarity: statusPolarity(parent), Undispellable: !parent.CanBeCleared, Hidden: true,
		})})
	}
	if ctx == nil {
		return nil, nil
	}
	ctx.damageMultiplier *= value
	if ctx.damageMultiplier > 1000 {
		ctx.damageMultiplier = 1000
	}
	return nil, nil
}

func (battle *battleState) changeGlobalCooldown(percent float64, source CombatUnitRef, ctx *skillExecutionContext) []CombatEvent {
	if battle == nil || battle.runtime == nil || ctx == nil || ctx.session == nil || math.IsNaN(percent) || percent == 0 {
		return nil
	}
	if percent > 100 {
		percent = 100
	}
	if percent < -1000 {
		percent = -1000
	}
	now := battle.runtime.clock.Now()
	for skillID, readyAt := range ctx.session.skillCooldowns {
		remaining := readyAt.Sub(now)
		if remaining <= 0 {
			delete(ctx.session.skillCooldowns, skillID)
			continue
		}
		remaining = time.Duration(float64(remaining) * (1 - percent/100))
		if remaining <= 0 {
			delete(ctx.session.skillCooldowns, skillID)
		} else {
			ctx.session.skillCooldowns[skillID] = now.Add(remaining)
		}
	}
	event := battle.runtime.event(now, CombatEventGlobalCooldown, source, source)
	event.Amount = int32(math.Round(percent))
	return []CombatEvent{event}
}

func (battle *battleState) modifyStatusFromHealth(effect SkillEffect, holder CombatUnitRef, parent *SkillStatusPlan) ([]CombatEvent, error) {
	if battle == nil || battle.runtime == nil || effect.Status == nil || parent == nil {
		return nil, nil
	}
	snapshot, ok := battle.runtime.Unit(holder)
	if !ok || snapshot.MaxHP <= 0 {
		return nil, nil
	}
	threshold := skillLevelArgument(parent.SkillID, parent.SkillLevel, "Args0")
	if threshold <= 0 {
		threshold = 10
	}
	missingPercent := float64(snapshot.MaxHP-snapshot.HP) * 100 / float64(snapshot.MaxHP)
	stacks := int(math.Floor((missingPercent + 1e-9) / threshold))
	if stacks < 0 {
		stacks = 0
	}
	if stacks > 100 {
		stacks = 100
	}
	var events []CombatEvent
	for index, attribute := range combatAttributesForValueKey(effect.Status.ValueKey) {
		key := fmt.Sprintf("modifier:%d:derived:%d:%d", parent.ModifierID, effect.Status.ModifierID, index)
		if stacks == 0 || effect.Status.Value == 0 {
			events = append(events, battle.runtime.RemoveEffectByKey(holder, holder, key, "recalculated")...)
			continue
		}
		applied, err := battle.runtime.ApplyEffects(CombatEffectContext{Source: holder, Target: holder}, []CombatEffect{modifierEffectSpec(effect.Status, EffectSpec{
			Key: key, Kind: CombatEffectAttribute, Attribute: attribute,
			Percent: effect.Status.Value * float64(stacks), StackMode: CombatEffectReplace,
			Polarity: statusPolarity(effect.Status), Undispellable: true, Hidden: true,
		})})
		events = append(events, applied...)
		if err != nil {
			return events, err
		}
	}
	return events, nil
}

func skillLevelArgument(skillID, level int32, key string) float64 {
	if tables == nil || skillID <= 0 || level <= 0 {
		return 0
	}
	row := tables.skillConfig[int64(skillID)*100+int64(level)]
	if row == nil {
		return 0
	}
	return numf(row[key])
}

func (battle *battleState) storeModifierHook(key string, source, holder, castTarget CombatUnitRef, status *SkillStatusPlan, effects map[int32][]SkillEffect) {
	if battle == nil || key == "" || status == nil || len(effects) == 0 {
		return
	}
	if battle.modifierHooks == nil {
		battle.modifierHooks = make(map[string]*activeSkillModifier)
	}
	battle.modifierHooks[modifierHookStorageKey(key, holder)] = &activeSkillModifier{
		key: key, source: source, holder: holder, status: status, effects: effects,
		castTarget: castTarget,
	}
}

func modifierHookStorageKey(key string, holder CombatUnitRef) string {
	return fmt.Sprintf("%d:%d:%s", holder.Side, holder.ID, key)
}

func (battle *battleState) triggerModifierEvent(eventCode int32, holder, eventSource CombatUnitRef, ctx *skillExecutionContext, amount int32) ([]CombatEvent, error) {
	if battle == nil || battle.runtime == nil || len(battle.modifierHooks) == 0 {
		return nil, nil
	}
	if ctx == nil {
		ctx = newSkillExecutionContext(battle, nil)
	}
	keys := make([]string, 0, len(battle.modifierHooks))
	for key := range battle.modifierHooks {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	// A modifier callback can install another modifier that listens for the
	// same event (the anatomy skill's critical heal installs its shield this way). The
	// newly installed hook belongs to the current event and must run now; keep
	// each storage key to one invocation so a refresh cannot duplicate it.
	queued := make(map[string]bool, len(keys))
	for _, key := range keys {
		queued[key] = true
	}
	var events []CombatEvent
	for index := 0; index < len(keys); index++ {
		key := keys[index]
		hook := battle.modifierHooks[key]
		if hook == nil || hook.holder != holder || !battle.runtime.HasEffect(hook.holder, hook.key) {
			continue
		}
		effects := hook.effects[eventCode]
		if len(effects) == 0 {
			continue
		}
		// A reactive heal/shield can crit or change HP and dispatch this same
		// callback again. Identify it by its stable holder/key, since callbacks
		// may also refresh and replace the hook while it is still executing.
		invocation := modifierEventInvocation{hookKey: key, eventCode: eventCode}
		if ctx.activeModifierEvents[invocation] {
			continue
		}
		if ctx.activeModifierEvents == nil {
			ctx.activeModifierEvents = make(map[modifierEventInvocation]bool)
		}
		ctx.activeModifierEvents[invocation] = true
		previousEvent, previousAmount := ctx.triggerEvent, ctx.triggerAmount
		previousCastTarget := ctx.castTarget
		ctx.triggerEvent, ctx.triggerAmount = eventCode, amount
		// Inside a callback `primary` is the event source, so hand the target
		// resolution the skill's own cast target instead.
		ctx.castTarget = hook.castTarget
		if ctx.castTarget.IsZero() {
			ctx.castTarget = holder
		}
		applied, err := battle.executeSkillEffectsAtContext(effects, holder, eventSource, holder, hook.status, ctx, 0)
		ctx.triggerEvent, ctx.triggerAmount = previousEvent, previousAmount
		ctx.castTarget = previousCastTarget
		delete(ctx.activeModifierEvents, invocation)
		events = append(events, applied...)
		if err != nil {
			return events, err
		}
		newKeys := make([]string, 0)
		for candidate := range battle.modifierHooks {
			if !queued[candidate] {
				queued[candidate] = true
				newKeys = append(newKeys, candidate)
			}
		}
		if len(newKeys) > 0 {
			sort.Strings(newKeys)
			keys = append(keys, newKeys...)
		}
	}
	return events, nil
}

func (battle *battleState) applyTriggeredReactive(effect SkillEffect, source, target CombatUnitRef, parent *SkillStatusPlan, ctx *skillExecutionContext) ([]CombatEvent, error) {
	percent := effect.ParamPercent
	if percent <= 0 && parent != nil {
		percent = parent.Value
	}
	if percent <= 0 {
		percent = 100
	}
	amount := percentAmount(int64(ctx.triggerAmount), percent)
	now := battle.runtime.clock.Now()
	switch effect.Kind {
	case SkillEffectLifeSteal:
		return battle.runtime.applyHealInternal(source, target, amount, CombatEventLifesteal, now)
	case SkillEffectReflect:
		trigger := battle.runtime.event(now, CombatEventReflect, source, target)
		trigger.Amount = amount
		damage, err := battle.runtime.applyDamageInternal(CombatDamageRequest{Source: source, Target: target, Amount: amount, Reflected: true}, now)
		return append([]CombatEvent{trigger}, damage...), err
	default:
		return nil, nil
	}
}

func (battle *battleState) triggerDamageEvents(source, target CombatUnitRef, applied []CombatEvent, ctx *skillExecutionContext) ([]CombatEvent, error) {
	amount := combatDamageAmount(applied, source, target)
	if amount <= 0 {
		return nil, nil
	}
	var events []CombatEvent
	for _, trigger := range []struct {
		code        int32
		holder      CombatUnitRef
		eventSource CombatUnitRef
	}{
		{5, source, target},
		{6, target, source},
		{23, target, source},
	} {
		triggered, err := battle.triggerModifierEvent(trigger.code, trigger.holder, trigger.eventSource, ctx, amount)
		events = append(events, triggered...)
		if err != nil {
			return events, err
		}
	}
	critical, killed := false, false
	for _, event := range applied {
		critical = critical || (event.Type == CombatEventDamage && event.Source == source && event.Target == target && event.IsCrit)
		killed = killed || (event.Type == CombatEventDeath && event.Target == target)
	}
	if critical {
		triggered, err := battle.triggerModifierEvent(7, source, target, ctx, amount)
		events = append(events, triggered...)
		if err != nil {
			return events, err
		}
	}
	if killed {
		triggered, err := battle.triggerModifierEvent(22, source, target, ctx, amount)
		events = append(events, triggered...)
		if err != nil {
			return events, err
		}
	}
	return events, nil
}

func (battle *battleState) triggerTimedEventHooks(timed []CombatEvent) ([]CombatEvent, error) {
	ctx := newSkillExecutionContext(battle, nil)
	var events []CombatEvent
	for _, event := range timed {
		var triggered []CombatEvent
		var err error
		switch event.Type {
		case CombatEventDamage, CombatEventHeal:
			if event.Amount > 0 {
				triggered, err = battle.triggerModifierEvent(23, event.Target, event.Source, ctx, event.Amount)
			}
		case CombatEventDeath:
			triggered, err = battle.triggerModifierEvent(22, event.Source, event.Target, ctx, 0)
		}
		events = append(events, triggered...)
		if err != nil {
			return events, err
		}
		if event.Type == CombatEventHeal && event.IsCrit && event.Amount > 0 {
			triggered, err = battle.triggerModifierEvent(7, event.Target, event.Source, ctx, event.Amount)
			events = append(events, triggered...)
			if err != nil {
				return events, err
			}
		}
	}
	return events, nil
}

func combatDamageAmount(events []CombatEvent, source, target CombatUnitRef) int32 {
	var amount int64
	for _, event := range events {
		if event.Type == CombatEventDamage && event.Source == source && event.Target == target {
			amount += int64(event.Amount)
		}
	}
	if amount > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(amount)
}

func combatEventAmount(events []CombatEvent, eventType CombatEventType) int32 {
	var amount int64
	for _, event := range events {
		if event.Type == eventType {
			amount += int64(event.Amount)
		}
	}
	if amount > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(amount)
}

// 周期效果（DOT/HOT）的节拍、时长与效果元数据登记见 battle_periodic.go。

func (battle *battleState) damageAmount(plan *SkillDamagePlan, source, target CombatUnitRef) int32 {
	amount, _ := battle.damageOutcome(plan, source, target, &skillExecutionContext{
		roll: func() float64 { return 1 }, damageMultiplier: 1, castCount: 1,
	})
	return amount
}

func (battle *battleState) damageOutcome(plan *SkillDamagePlan, source, target CombatUnitRef, ctx *skillExecutionContext) (int32, bool) {
	if plan == nil {
		return 0, false
	}
	var amount float64
	if plan.Self.Percent != 0 {
		base := float64(battle.formulaBase(plan.Self.Stat, source))
		if (plan.Self.Stat == SkillStatPhysicalAttack || plan.Self.Stat == SkillStatSpiritualAttack) &&
			(plan.Type == SkillDamagePhysical || plan.Type == SkillDamageSpiritual) {
			defense := CombatAttributePhysicalDefense
			if plan.Type == SkillDamageSpiritual {
				defense = CombatAttributeSpiritualDefense
			}
			base -= float64(battle.runtime.EffectiveAttribute(target, defense))
			if base < 1 {
				base = 1
			}
		}
		amount += base * plan.Self.Percent / 100
	}
	if plan.Target.Percent != 0 {
		amount += float64(battle.formulaBase(plan.Target.Stat, target)) * plan.Target.Percent / 100
	}
	if plan.Limited && plan.Limit.Percent > 0 {
		limit := float64(battle.formulaBase(plan.Limit.Stat, target)) * plan.Limit.Percent / 100
		if limit > 0 && amount > limit {
			amount = limit
		}
	}
	multiplier := battle.runtime.DamageMultiplier(source)
	if ctx != nil && ctx.damageMultiplier > 0 {
		multiplier *= ctx.damageMultiplier
	}
	amount *= multiplier
	if plan.Type == SkillDamagePhysical {
		amount *= 1 + math.Min(1000, battle.runtime.numericPercentValue(source, 1045))/100
	} else if plan.Type == SkillDamageSpiritual {
		amount *= 1 + math.Min(1000, battle.runtime.numericPercentValue(source, 1046))/100
	}
	if plan.Type != SkillDamageTrue {
		reduction := battle.runtime.EffectivePercentAttribute(target, CombatAttributeDamageReduction)
		if plan.Type == SkillDamagePhysical {
			reduction += battle.runtime.EffectivePercentAttribute(target, CombatAttributePhysicalDamageReduction)
		} else if plan.Type == SkillDamageSpiritual {
			reduction += battle.runtime.EffectivePercentAttribute(target, CombatAttributeSpiritualDamageReduction)
		}
		if reduction < -1000 {
			reduction = -1000
		}
		if reduction > 100 {
			reduction = 100
		}
		amount *= 1 - reduction/100
	}
	critical := false
	if plan.CanCrit && amount > 0 {
		chance := battle.runtime.EffectivePercentAttribute(source, CombatAttributeCritRate) -
			battle.runtime.EffectivePercentAttribute(target, CombatAttributeAntiCritRate)
		critValue := battle.runtime.EffectivePercentAttribute(source, CombatAttributeCritValue) -
			battle.runtime.EffectivePercentAttribute(target, CombatAttributeAntiCritValue)
		if plan.Type == SkillDamagePhysical {
			chance += battle.runtime.EffectivePercentAttribute(source, CombatAttributePhysicalCritRate) -
				battle.runtime.EffectivePercentAttribute(target, CombatAttributePhysicalAntiCritRate)
			critValue += battle.runtime.EffectivePercentAttribute(source, CombatAttributePhysicalCritValue) -
				battle.runtime.EffectivePercentAttribute(target, CombatAttributePhysicalAntiCritValue)
		} else if plan.Type == SkillDamageSpiritual {
			chance += battle.runtime.EffectivePercentAttribute(source, CombatAttributeSpiritualCritRate) -
				battle.runtime.EffectivePercentAttribute(target, CombatAttributeSpiritualAntiCritRate)
			critValue += battle.runtime.EffectivePercentAttribute(source, CombatAttributeSpiritualCritValue) -
				battle.runtime.EffectivePercentAttribute(target, CombatAttributeSpiritualAntiCritValue)
		}
		chance = math.Max(0, math.Min(100, chance))
		roll := rand.Float64
		if ctx != nil && ctx.roll != nil {
			roll = ctx.roll
		}
		critical = roll()*100 < chance
		if critical {
			critMultiplier := 1.5 + critValue/100
			critMultiplier = math.Max(1, math.Min(10, critMultiplier))
			amount *= critMultiplier
		}
	}
	return positiveCombatAmount(amount), critical
}

func (battle *battleState) formulaBase(stat SkillFormulaStat, unit CombatUnitRef) int32 {
	switch stat {
	case SkillStatPhysicalAttack:
		return battle.runtime.EffectiveAttribute(unit, CombatAttributePhysicalAttack)
	case SkillStatSpiritualAttack:
		return battle.runtime.EffectiveAttribute(unit, CombatAttributeSpiritualAttack)
	case SkillStatMaxHP:
		return battle.runtime.EffectiveAttribute(unit, CombatAttributeMaxHP)
	case SkillStatCurrentHP:
		if snapshot, ok := battle.runtime.Unit(unit); ok {
			return snapshot.HP
		}
	}
	return 0
}

func positiveCombatAmount(value float64) int32 {
	if math.IsNaN(value) || value <= 0 {
		return 0
	}
	if math.IsInf(value, 1) || value >= math.MaxInt32 {
		return math.MaxInt32
	}
	result := int32(math.Ceil(value))
	if result < 1 {
		return 1
	}
	return result
}

func combatAttributesForValueKey(key int32) []CombatAttribute {
	switch key {
	case 1:
		return []CombatAttribute{CombatAttributeMaxHP}
	case 2:
		return []CombatAttribute{CombatAttributeMaxMP}
	case 3:
		return []CombatAttribute{CombatAttributePhysicalAttack, CombatAttributeSpiritualAttack}
	case 4:
		return []CombatAttribute{CombatAttributePhysicalAttack}
	case 5:
		return []CombatAttribute{CombatAttributeSpiritualAttack}
	case 6:
		return []CombatAttribute{CombatAttributePhysicalDefense, CombatAttributeSpiritualDefense}
	case 7:
		return []CombatAttribute{CombatAttributePhysicalDefense}
	case 8:
		return []CombatAttribute{CombatAttributeSpiritualDefense}
	case 9:
		return []CombatAttribute{CombatAttributeCritRate}
	case 10:
		return []CombatAttribute{CombatAttributePhysicalCritRate}
	case 11:
		return []CombatAttribute{CombatAttributeSpiritualCritRate}
	case 12:
		return []CombatAttribute{CombatAttributeCritValue}
	case 13:
		return []CombatAttribute{CombatAttributePhysicalCritValue}
	case 14:
		return []CombatAttribute{CombatAttributeSpiritualCritValue}
	case 15:
		return []CombatAttribute{CombatAttributeAntiCritRate}
	case 16:
		return []CombatAttribute{CombatAttributePhysicalAntiCritRate}
	case 17:
		return []CombatAttribute{CombatAttributeSpiritualAntiCritRate}
	case 18:
		return []CombatAttribute{CombatAttributeAntiCritValue}
	case 19:
		return []CombatAttribute{CombatAttributePhysicalAntiCritValue}
	case 20:
		return []CombatAttribute{CombatAttributeSpiritualAntiCritValue}
	case 21:
		return []CombatAttribute{CombatAttributeDamageReduction}
	case 22:
		return []CombatAttribute{CombatAttributePhysicalDamageReduction}
	case 23:
		return []CombatAttribute{CombatAttributeSpiritualDamageReduction}
	case 24:
		return []CombatAttribute{CombatAttributeAuxiliary}
	case 25:
		return []CombatAttribute{CombatAttributeSpeed}
	case 26:
		return []CombatAttribute{CombatAttributeHit}
	case 27:
		return []CombatAttribute{CombatAttributeResistance}
	default:
		return nil
	}
}

func combatStatusForStateKey(key int32) CombatStatus {
	switch key {
	case 1:
		return CombatStatusInvincible
	case 4, 5:
		return CombatStatusUndying
	case 8:
		return CombatStatusSilenced
	case 9:
		return CombatStatusStunned
	case 10:
		return CombatStatusFrozen
	case 11:
		return CombatStatusPetrified
	case 15:
		return CombatStatusInvisible
	default:
		return CombatStatusNone
	}
}

func statusPolarity(status *SkillStatusPlan) CombatEffectPolarity {
	if status == nil {
		return CombatEffectNeutral
	}
	// StateKey is a mixed client protocol namespace: it contains both hostile
	// controls (silence/stun/freeze/petrify) and beneficial states (invincible,
	// undying, guaranteed-crit, shield, counter, reflect and invisibility).
	// Treating every non-zero key as a control made Resistance incorrectly
	// reject friendly buffs. Explicit state polarity is therefore checked
	// before the generic DOT/debuff flags; unknown keys remain neutral until
	// their semantics are defined instead of being guessed as harmful.
	switch status.StateKey {
	case 8, 9, 10, 11:
		return CombatEffectHarmful
	case 1, 4, 5, 14, 15, 100, 101, 102, 107:
		return CombatEffectBeneficial
	}
	if status.IsDebuff || status.HasDOT {
		return CombatEffectHarmful
	}
	if status.IsBuff || status.HasHOT {
		return CombatEffectBeneficial
	}
	return CombatEffectNeutral
}

func statusDuration(status *SkillStatusPlan) time.Duration {
	if status == nil {
		return 0
	}
	return secondsDuration(status.DurationSeconds)
}

func monsterSkillID(monsterID int32) int32 {
	return selectMonsterSkillID(monsterID, rand.Intn)
}

func selectMonsterSkillID(monsterID int32, intn func(int) int) int32 {
	if tables != nil {
		if monster, ok := tables.monsterBase[int64(monsterID)]; ok {
			if groupID := num(monster["SkillGroupId"]); groupID > 0 {
				if group, ok := tables.skillGroup[groupID]; ok {
					var skills []int32
					for _, raw := range arrOf(group["SkillsArr"]) {
						entry, _ := raw.(map[string]interface{})
						if id := int32(num(entry["Skills_Id"])); id > 0 {
							skills = append(skills, id)
						}
					}
					if len(skills) > 0 {
						return skills[intn(len(skills))]
					}
				}
			}
		}
	}
	return 500001
}
