package main

import (
	"fmt"
	"log"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mhqserver/protocol"
)

var skillLogicCatalog *SkillLogicCatalog
var skillLogicCatalogMu sync.Mutex

const combatTickerInterval = 100 * time.Millisecond

// The client removes a dead main-story monster four seconds after opcode
// 20082. Ending the battle earlier destroys the team while that coroutine is
// still pending and leaves a clickable ghost unit behind.
var battleVictoryDelay = 4200 * time.Millisecond

// Tests exercise visual packet encoding directly. main overrides this from
// the -battle-effects flag for the current client build.
var battleVisualEffectsEnabled = true

type battleEffectMetadata struct {
	modifierID int64
	durationMS int32
	stateKey   int32
	effectID   int32
	iconID     string
	iconDesc   string
	isBuff     bool
}

type skillExecutionContext struct {
	roll             func() float64
	damageMultiplier float64
	castCount        int
	triggerEvent     int32
	triggerAmount    int32
	session          *session
}

type activeSkillModifier struct {
	key     string
	source  CombatUnitRef
	holder  CombatUnitRef
	status  *SkillStatusPlan
	effects map[int32][]SkillEffect
}

func newSkillExecutionContext(battle *battleState, roll func() float64) *skillExecutionContext {
	if roll == nil {
		roll = rand.Float64
	}
	ctx := &skillExecutionContext{roll: roll, damageMultiplier: 1, castCount: 1}
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
	if battle.pvp != nil {
		battle.pvp.mu.Lock()
		defer battle.pvp.mu.Unlock()
		if battle.ended || battle.pvp.settled {
			return reject("pvp ended")
		}
	}
	if battle.party != nil {
		battle.party.mu.Lock()
		defer battle.party.mu.Unlock()
		if battle.ended {
			return reject("battle ended")
		}
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
	if !skillPlanCanCast(plan) {
		return reject("passive")
	}
	if battle.runtime == nil {
		battle.runtime = NewCombatRuntime(battle, ss.playerID, nil)
	}
	tickEvents := battle.runtime.Tick()
	tickHooks, tickErr := battle.triggerTimedEventHooks(tickEvents)
	if tickErr != nil {
		return reject(tickErr.Error())
	}
	s.emitCombatEvents(ch, battle, append(tickEvents, tickHooks...))
	if !battle.runtime.CanCast(battle.runtime.Player()) {
		return reject("cannot cast")
	}
	primary, ok := battle.playerSkillPrimaryTarget(plan)
	if !ok {
		return reject("no target")
	}
	resourceCast := authoritativeSkillResourceCast(skillID, plan.Cast)
	mpCost, hpCost, err := battle.skillResourceCost(resourceCast.SkillCastType, resourceCast.SkillCast)
	if err != nil {
		return reject(err.Error())
	}
	previousCooldown, hadPreviousCooldown := ss.skillCooldowns[skillID]
	if _, ready := ss.tryStartSkillCooldown(skillID, time.Now(), time.Duration(plan.CooldownMS)*time.Millisecond); !ready {
		return reject("cooldown")
	}
	battle.playerMP -= mpCost
	battle.playerHP -= hpCost
	player := battle.runtime.Player()
	events, err := battle.executeSkillPlan(plan, player, primary, rand.Float64)
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
	if battle.pvp != nil {
		syncPVPStateLocked(battle)
	}
	for _, recipient := range s.battleRecipients(ch, battle) {
		s.sendPush(recipient, protocol.OpM2C_PlaySkill, &protocol.M2C_PlaySkill{
			UnitId: ss.playerID, SkillId: skillID, CoolTime: plan.CooldownMS,
			TargetId: combatUnitID(primary), MpCost: mpCost,
		})
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
	s.emitCombatEvents(ch, battle, events)
	remainingCD := int32(0)
	if _, ok := ss.skillCooldowns[skillID]; ok {
		// Fresh casts must send the configured duration exactly. The only
		// currently configured global-CD modifier is a 100% reset, which removes
		// the map entry and therefore sends zero instead.
		remainingCD = plan.CooldownMS
	}
	s.sendPush(ch, protocol.OpM2C_StartCD, &protocol.M2C_StartCD{
		Id: skillID, SkillCD: remainingCD, Type: protocol.MainUIType_SkillSlot,
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
	s.monstersAttack(ch, battle)
	return resp, true
}

func authoritativeSkillResourceCast(skillID int32, fallback SkillCast) SkillCast {
	if tables == nil || tables.skillConfig == nil {
		return fallback
	}
	row := tables.skillConfig[int64(skillID)*100]
	if row == nil {
		return fallback
	}
	fallback.SkillCastType = int32(num(row["CastType"]))
	fallback.SkillCast = numf(row["CastValue"])
	return fallback
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
	skillLogicCatalogMu.Lock()
	defer skillLogicCatalogMu.Unlock()
	if skillLogicCatalog != nil {
		return nil
	}
	path := skillLogicFixturePath()
	catalog, err := LoadSkillLogicCatalog(path)
	if err != nil {
		return fmt.Errorf("load SkillLogicConfig: %w", err)
	}
	skillLogicCatalog = catalog
	return nil
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
	ctx := newSkillExecutionContext(battle, roll)
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
		branch := effect.Failure
		if ctx.roll()*100 < effect.ChancePercent {
			branch = effect.Success
		}
		return battle.executeSkillEffectsAtContext(branch, source, primary, holder, parent, ctx, inheritedDelayMS)
	case SkillEffectDelay:
		return battle.executeSkillEffectsAtContext(effect.Children, source, primary, holder, parent, ctx, addDelayMS(inheritedDelayMS, effect.DelayMS))
	case SkillEffectProjectile:
		delayMS := addDelayMS(inheritedDelayMS, effect.DelayMS)
		events := battle.skillVisualEvents(effect, source, primary, holder, delayMS)
		children, err := battle.executeSkillEffectsAtContext(effect.Children, source, primary, holder, parent, ctx, delayMS)
		return append(events, children...), err
	case SkillEffectVisual:
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
	targets := battle.resolveSkillTargets(effect.Target, source, primary, holder)
	if len(targets) == 0 {
		return nil, nil
	}
	var events []CombatEvent
	for _, target := range targets {
		var applied []CombatEvent
		var err error
		switch effect.Kind {
		case SkillEffectDamage:
			amount, critical := battle.damageOutcome(effect.Damage, source, target, ctx)
			if amount > 0 {
				if effect.Damage.Periodic && parent != nil {
					applied, err = battle.applyPeriodic(effect, parent, source, target, amount, true)
				} else {
					applied, err = battle.runtime.ApplyDamage(CombatDamageRequest{Source: source, Target: target, Amount: amount, IsCrit: critical})
				}
			}
		case SkillEffectHeal:
			amount := battle.treatmentAmount(effect.Treatment, source, target)
			if amount > 0 {
				if effect.Treatment.Periodic && parent != nil {
					applied, err = battle.applyPeriodic(effect, parent, source, target, amount, false)
				} else {
					applied, err = battle.runtime.ApplyHeal(source, target, amount)
				}
			}
		case SkillEffectShield:
			amount := battle.treatmentAmount(effect.Treatment, source, target)
			if amount > 0 {
				duration := time.Duration(0)
				if parent != nil {
					duration = secondsDuration(parent.DurationSeconds)
				}
				key := battle.registerEffectMeta(effect.ModifierID, "shield", parent)
				applied, err = battle.runtime.ApplyEffects(CombatEffectContext{Source: source, Target: target}, []CombatEffect{EffectSpec{
					Key: key, Kind: CombatEffectShield, Value: amount, Duration: duration,
					StackMode: stackMode(parent), MaxStacks: 10,
				}})
			}
		case SkillEffectStatus:
			applied, err = battle.applyStatusEffectWithContext(effect, source, target, primary, ctx)
		case SkillEffectLifeSteal, SkillEffectReflect, SkillEffectCounter:
			if ctx.triggerEvent != 0 && ctx.triggerAmount > 0 {
				applied, err = battle.applyTriggeredReactive(effect, source, target, parent, ctx)
				break
			}
			kind := CombatEffectLifesteal
			if effect.Kind == SkillEffectReflect {
				kind = CombatEffectReflect
			} else if effect.Kind == SkillEffectCounter {
				kind = CombatEffectCounter
			}
			percent := effect.ParamPercent
			if percent <= 0 && parent != nil {
				percent = parent.Value
			}
			if percent <= 0 {
				percent = 100
			}
			key := battle.registerEffectMeta(effect.ModifierID, string(effect.Kind), parent)
			applied, err = battle.runtime.ApplyEffects(CombatEffectContext{Source: source, Target: target}, []CombatEffect{EffectSpec{
				Key: key, Kind: kind, Percent: percent, Duration: statusDuration(parent),
				StackMode: stackMode(parent), MaxStacks: 10,
			}})
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
		event.EffectTargetType = effect.EffectType
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
	var events []CombatEvent
	for _, child := range effect.Children {
		if child.Trigger.Event != 24 {
			continue
		}
		applied, err := battle.executeSkillEffectAtContext(child, target, source, target, status, ctx, 0)
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
		applied, err := battle.runtime.ApplyEffects(CombatEffectContext{Source: source, Target: target}, []CombatEffect{EffectSpec{
			Key: key, Kind: CombatEffectAttribute, Attribute: attribute, Percent: status.Value,
			Duration: secondsDuration(status.DurationSeconds), StackMode: stackMode(status), MaxStacks: 10,
			Polarity: statusPolarity(status), Undispellable: !status.CanBeCleared,
		}})
		events = append(events, applied...)
		if err != nil {
			return events, err
		}
		appliedPersistent = true
	}
	if control := combatStatusForStateKey(status.StateKey); control != CombatStatusNone {
		key := battle.registerEffectMeta(status.ModifierID, "control", status)
		applied, err := battle.runtime.ApplyEffects(CombatEffectContext{Source: source, Target: target}, []CombatEffect{EffectSpec{
			Key: key, Kind: CombatEffectStatus, Status: control, Duration: secondsDuration(status.DurationSeconds),
			StackMode: stackMode(status), MaxStacks: 10, Polarity: statusPolarity(status),
			Undispellable: !status.CanBeCleared,
		}})
		events = append(events, applied...)
		if err != nil {
			return events, err
		}
		appliedPersistent = true
	}
	dynamic := make(map[int32][]SkillEffect)
	for _, child := range effect.Children {
		eventCode := child.Trigger.Event
		if eventCode == 24 {
			continue
		}
		installReactive := isReactiveSkillEffect(child.Kind) && (eventCode == 2 || eventCode == 5 || eventCode == 6)
		if eventCode != 0 && eventCode != 8 && !installReactive {
			dynamic[eventCode] = append(dynamic[eventCode], child)
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
	if len(dynamic) > 0 {
		suffix, hidden := "marker", false
		if appliedPersistent {
			suffix, hidden = "hook", true
		}
		key := battle.registerEffectMeta(status.ModifierID, suffix, status)
		applied, err := battle.runtime.ApplyEffects(CombatEffectContext{Source: source, Target: target}, []CombatEffect{EffectSpec{
			Key: key, Kind: CombatEffectStatus, Status: CombatStatusMarker,
			Duration: secondsDuration(status.DurationSeconds), StackMode: stackMode(status), MaxStacks: 10,
			Polarity: statusPolarity(status), Undispellable: !status.CanBeCleared, Hidden: hidden,
		}})
		events = append(events, applied...)
		if err != nil {
			return events, err
		}
		battle.storeModifierHook(key, source, target, status, dynamic)
		appliedPersistent = true
	}
	if !appliedPersistent && status.DurationSeconds != 0 {
		key := battle.registerEffectMeta(status.ModifierID, "marker", status)
		applied, err := battle.runtime.ApplyEffects(CombatEffectContext{Source: source, Target: target}, []CombatEffect{EffectSpec{
			Key: key, Kind: CombatEffectStatus, Status: CombatStatusMarker,
			Duration: secondsDuration(status.DurationSeconds), StackMode: stackMode(status), MaxStacks: 10,
			Polarity: statusPolarity(status), Undispellable: !status.CanBeCleared,
		}})
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
		return battle.runtime.ApplyEffects(CombatEffectContext{Source: source, Target: source}, []CombatEffect{EffectSpec{
			Key: key, Kind: CombatEffectAttribute, Attribute: CombatAttributeDamageMultiplier,
			Percent: value, Duration: statusDuration(parent), StackMode: stackMode(parent), MaxStacks: 10,
			Polarity: statusPolarity(parent), Undispellable: !parent.CanBeCleared, Hidden: true,
		}})
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
		applied, err := battle.runtime.ApplyEffects(CombatEffectContext{Source: holder, Target: holder}, []CombatEffect{EffectSpec{
			Key: key, Kind: CombatEffectAttribute, Attribute: attribute,
			Percent: effect.Status.Value * float64(stacks), StackMode: CombatEffectReplace,
			Polarity: statusPolarity(effect.Status), Undispellable: true, Hidden: true,
		}})
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

func (battle *battleState) storeModifierHook(key string, source, holder CombatUnitRef, status *SkillStatusPlan, effects map[int32][]SkillEffect) {
	if battle == nil || key == "" || status == nil || len(effects) == 0 {
		return
	}
	if battle.modifierHooks == nil {
		battle.modifierHooks = make(map[string]*activeSkillModifier)
	}
	battle.modifierHooks[modifierHookStorageKey(key, holder)] = &activeSkillModifier{
		key: key, source: source, holder: holder, status: status, effects: effects,
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
	var events []CombatEvent
	for _, key := range keys {
		hook := battle.modifierHooks[key]
		if hook == nil || hook.holder != holder || !battle.runtime.HasEffect(hook.holder, hook.key) {
			continue
		}
		effects := hook.effects[eventCode]
		if len(effects) == 0 {
			continue
		}
		previousEvent, previousAmount := ctx.triggerEvent, ctx.triggerAmount
		ctx.triggerEvent, ctx.triggerAmount = eventCode, amount
		applied, err := battle.executeSkillEffectsAtContext(effects, holder, eventSource, holder, hook.status, ctx, 0)
		ctx.triggerEvent, ctx.triggerAmount = previousEvent, previousAmount
		events = append(events, applied...)
		if err != nil {
			return events, err
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
	case SkillEffectCounter:
		trigger := battle.runtime.event(now, CombatEventCounter, source, target)
		trigger.Amount = amount
		damage, err := battle.runtime.applyDamageInternal(CombatDamageRequest{Source: source, Target: target, Amount: amount, Counter: true}, now)
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

func (battle *battleState) applyPeriodic(effect SkillEffect, status *SkillStatusPlan, source, target CombatUnitRef, amount int32, damage bool) ([]CombatEvent, error) {
	kind := CombatEffectHealOverTime
	suffix := "hot"
	if damage {
		kind, suffix = CombatEffectDamageOverTime, "dot"
	}
	interval := secondsDuration(status.TickSeconds)
	if interval <= 0 {
		interval = time.Second
	}
	key := battle.registerEffectMeta(status.ModifierID, suffix, status)
	return battle.runtime.ApplyEffects(CombatEffectContext{Source: source, Target: target}, []CombatEffect{EffectSpec{
		Key: key, Kind: kind, Value: amount, Duration: secondsDuration(status.DurationSeconds), Interval: interval,
		StackMode: stackMode(status), MaxStacks: 10, Polarity: statusPolarity(status),
		Undispellable: !status.CanBeCleared,
	}})
}

func (battle *battleState) resolveSkillTargets(plan SkillTargetPlan, source, primary, holder CombatUnitRef) []CombatUnitRef {
	if plan.Side == "" {
		if !primary.IsZero() {
			return []CombatUnitRef{primary}
		}
		return nil
	}
	if plan.Kind == SkillTargetSingle {
		var target CombatUnitRef
		switch plan.Side {
		case SkillTargetSelf:
			target = source
		case SkillTargetCastTarget:
			target = primary
		case SkillTargetEventSource:
			target = primary
		case SkillTargetHolder:
			target = holder
		default:
			target = primary
		}
		if target.IsZero() {
			return nil
		}
		if target == source || target == holder {
			unit, ok := battle.runtime.Unit(target)
			if !ok || !unit.Alive {
				return nil
			}
		} else if !battle.runtime.CanBeTargeted(target) {
			return nil
		}
		return []CombatUnitRef{target}
	}
	var candidates []CombatUnitRef
	wantAllies := plan.Side == SkillTargetAlly
	wantEnemies := plan.Side == SkillTargetEnemy
	if plan.Side == SkillTargetAny || (source.Side == CombatSidePlayer && wantAllies) {
		for _, ref := range battle.alliedPlayerRefs() {
			if battle.runtime.CanBeTargeted(ref) {
				candidates = append(candidates, ref)
			}
		}
	} else if source.Side == CombatSideMonster && wantEnemies {
		// Each member owns one monster-response turn. Keep that turn targeted at
		// the owning player; otherwise a multi-target monster skill is repeated
		// once per connected member.
		ref := battle.runtime.Player()
		if battle.runtime.CanBeTargeted(ref) {
			candidates = append(candidates, ref)
		}
	}
	if plan.Side == SkillTargetAny || (source.Side == CombatSidePlayer && wantEnemies) || (source.Side == CombatSideMonster && wantAllies) {
		if primary.Side == CombatSideMonster && battle.runtime.CanBeTargeted(primary) {
			candidates = append(candidates, primary)
		}
		for _, monster := range battle.monsters {
			ref := MonsterCombatUnit(monster.id)
			if ref == primary || !battle.runtime.CanBeTargeted(ref) {
				continue
			}
			candidates = append(candidates, ref)
		}
	}
	maximum := int(plan.MaxCount)
	if maximum <= 0 || maximum > len(candidates) {
		maximum = len(candidates)
	}
	return candidates[:maximum]
}

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

func (battle *battleState) treatmentAmount(plan *SkillTreatmentPlan, source, target CombatUnitRef) int32 {
	if plan == nil {
		return 0
	}
	amount := float64(battle.formulaBase(plan.Self.Stat, source))*plan.Self.Percent/100 +
		float64(battle.formulaBase(plan.Target.Stat, target))*plan.Target.Percent/100
	return positiveCombatAmount(amount)
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

func stackMode(status *SkillStatusPlan) CombatEffectStackMode {
	if status == nil {
		return CombatEffectReplace
	}
	switch status.OverlayType {
	case 1:
		return CombatEffectRefresh
	case 2, 3, 4:
		return CombatEffectStack
	default:
		return CombatEffectReplace
	}
}

func statusPolarity(status *SkillStatusPlan) CombatEffectPolarity {
	if status != nil && status.IsDebuff {
		return CombatEffectHarmful
	}
	if status != nil && (status.IsBuff || status.IsControl || status.HasHOT || status.HasDOT) {
		if status.IsControl || status.HasDOT {
			return CombatEffectHarmful
		}
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

func secondsDuration(seconds float64) time.Duration {
	if seconds <= 0 || math.IsNaN(seconds) {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

func (battle *battleState) registerEffectMeta(modifierID int64, suffix string, status *SkillStatusPlan) string {
	if modifierID == 0 && status != nil {
		modifierID = status.ModifierID
	}
	key := fmt.Sprintf("modifier:%d:%s", modifierID, suffix)
	if battle.effectMeta == nil {
		battle.effectMeta = make(map[string]battleEffectMetadata)
	}
	meta := battleEffectMetadata{modifierID: modifierID}
	if status != nil {
		meta.durationMS = int32(math.Round(status.DurationSeconds * 1000))
		meta.stateKey, meta.isBuff = status.StateKey, !status.IsDebuff
	}
	if skillLogicCatalog != nil {
		if modifier := skillLogicCatalog.Modifiers[modifierID]; modifier != nil {
			meta.stateKey, meta.effectID = modifier.StateKey, modifier.EffectID
			meta.iconID, meta.iconDesc = modifier.IconID, modifier.IconDescription
			meta.isBuff = modifier.BuffType == 0
		}
	}
	battle.effectMeta[key] = meta
	return key
}

func (s *Server) monstersAttack(ch *channel, battle *battleState) {
	if battle == nil || battle.ended || battle.playerHP <= 0 || battle.runtime == nil || ensureSkillLogicCatalog() != nil {
		return
	}
	tickEvents := battle.runtime.Tick()
	tickHooks, _ := battle.triggerTimedEventHooks(tickEvents)
	s.emitCombatEvents(ch, battle, append(tickEvents, tickHooks...))
	for _, monster := range battle.monsters {
		source := MonsterCombatUnit(monster.id)
		if !battle.runtime.CanCast(source) {
			continue
		}
		skillID := monsterSkillID(monster.monsterID)
		plan, err := skillLogicCatalog.Plan(skillID, 1)
		if err != nil {
			continue
		}
		primary := battle.runtime.Player()
		if plan.TeamType == 2 {
			primary = source
		}
		for _, recipient := range s.battleRecipients(ch, battle) {
			s.sendPush(recipient, protocol.OpM2C_MonsterPlaySkill, &protocol.M2C_MonsterPlaySkill{
				UnitId: monster.id, SkillId: skillID, TargetId: combatUnitID(primary),
			})
		}
		events, executeErr := battle.executeSkillPlan(plan, source, primary, rand.Float64)
		if executeErr != nil {
			continue
		}
		s.emitCombatEvents(ch, battle, events)
		if s.settleCombatLocked(ch, battle) {
			return
		}
	}
}

func monsterSkillID(monsterID int32) int32 {
	if tables != nil {
		if monster, ok := tables.monsterBase[int64(monsterID)]; ok {
			if groupID := num(monster["SkillGroupId"]); groupID > 0 {
				if group, ok := tables.skillGroup[groupID]; ok {
					if skills := arrOf(group["SkillsArr"]); len(skills) > 0 {
						if first, ok := skills[0].(map[string]interface{}); ok {
							if id := num(first["Skills_Id"]); id > 0 {
								return int32(id)
							}
						}
					}
				}
			}
		}
	}
	return 500001
}

func (s *Server) emitCombatEvents(ch *channel, battle *battleState, events []CombatEvent) {
	for _, recipient := range s.battleRecipients(ch, battle) {
		s.emitCombatEventsToChannel(recipient, battle, events, recipient == ch)
	}
}

func (s *Server) emitCombatEventsToChannel(ch *channel, battle *battleState, events []CombatEvent, includeLocalCooldown bool) {
	for _, event := range events {
		targetID := combatUnitID(event.Target)
		switch event.Type {
		case CombatEventVisual:
			// 技能特效：推 20077 PlaySkillEffect（客户端 PlayEffectEvent 按 EffectId 播放）。
			// ⚠ 过滤弹道类（EffectType==1）：客户端 PlayBulletEffect 调用
			// DG.Tweening DOMove(Transform,Vector3,float,bool) 但 DOTween 缺该重载 →
			// MissingMethodException 刷屏卡顿（Player.log 实证）。通用(2/3)与循环(4)
			// 走 PlayCommonEffect/PlayContinueEffect 安全分支 → 正常推。
			if event.EffectID <= 0 {
				continue
			}
			effectType := int32(0)
			if tables != nil {
				if row, ok := tables.effectConfig[int64(event.EffectID)]; ok {
					effectType = int32(num(row["EffectType"]))
				}
			}
			if effectType == 1 {
				continue // 弹道类触发客户端崩溃，跳过
			}
			s.sendPush(ch, protocol.OpM2C_PlaySkillEffect, &protocol.M2C_PlaySkillEffect{
				UnitId:           combatUnitID(event.Source),
				TargetId:         targetID,
				Time:             event.DelayMS,
				EffectPos:        event.EffectPos,
				EffectId:         event.EffectID,
				EffectTargetType: event.EffectTargetType,
				ActorId:          ch.session.playerID,
			})
		case CombatEventDamage:
			if event.Amount > 0 {
				s.sendPush(ch, protocol.OpM2C_BattleSkillRet, &protocol.M2C_BattleSkillRet{UnitId: targetID, ChangeHpValue: -event.Amount, IsCrit: event.IsCrit})
				// BattleSkillRet only drives hurt animation and floating text. Numeric
				// HP is authoritative and must be sent separately for the health bar.
				if event.Target.Side == CombatSideMonster {
					s.pushMonsterHPByID(ch, battle, targetID)
				} else {
					s.pushCombatHP(ch, targetID, event.HPAfter)
				}
			}
			if event.Key != "" {
				s.emitCombatTouch(ch, battle, event)
			}
		case CombatEventHeal, CombatEventLifesteal:
			if event.Amount > 0 {
				s.sendPush(ch, protocol.OpM2C_BattleSkillRet, &protocol.M2C_BattleSkillRet{UnitId: targetID, ChangeHpValue: event.Amount})
				if event.Target.Side == CombatSideMonster {
					s.pushMonsterHPByID(ch, battle, targetID)
				} else {
					s.pushCombatHP(ch, targetID, event.HPAfter)
				}
			}
			if event.Key != "" {
				s.emitCombatTouch(ch, battle, event)
			}
		case CombatEventShieldAbsorb, CombatEventImmune, CombatEventReflect, CombatEventCounter:
			s.emitCombatTouch(ch, battle, event)
		case CombatEventEffectApplied, CombatEventEffectRefreshed, CombatEventEffectStacked:
			if event.Hidden {
				continue
			}
			s.emitCombatState(ch, battle, event, protocol.ChangeType_Add)
			if event.Type == CombatEventEffectStacked {
				s.emitCombatTouch(ch, battle, event)
			}
		case CombatEventEffectRemoved, CombatEventEffectDispelled:
			if event.Hidden {
				continue
			}
			s.emitCombatState(ch, battle, event, protocol.ChangeType_Reduce)
		case CombatEventEffectExpired:
			// The client starts its own timer from the Add packet. Sending a
			// Reduce packet at natural expiry would insert the same buff again.
		case CombatEventResourceClamped:
			s.pushCombatHP(ch, targetID, event.HPAfter)
		case CombatEventDeath:
			if event.Target.Side == CombatSideMonster && battle.pvp == nil {
				if battle.selectedID == event.Target.ID {
					battle.selectedID = 0
				}
				s.sendPush(ch, protocol.OpM2C_MainstoryMonsterDead, &protocol.M2C_MainstoryMonsterDead{
					UnitId: targetID, ActorId: ch.session.playerID,
				})
			} else {
				s.sendPush(ch, protocol.OpM2C_UnitDead, &protocol.M2C_UnitDead{
					UnitId: targetID, ActorId: ch.session.playerID,
				})
			}
		case CombatEventGlobalCooldown:
			if !includeLocalCooldown {
				continue
			}
			now := time.Now()
			seen := make(map[int32]struct{}, len(ch.session.skillOrder))
			for _, skillID := range ch.session.skillOrder {
				if _, duplicate := seen[skillID]; duplicate {
					continue
				}
				seen[skillID] = struct{}{}
				remaining := int32(0)
				if readyAt, ok := ch.session.skillCooldowns[skillID]; ok && readyAt.After(now) {
					remaining = int32(minInt64(math.MaxInt32, readyAt.Sub(now).Milliseconds()))
				}
				s.sendPush(ch, protocol.OpM2C_StartCD, &protocol.M2C_StartCD{Id: skillID, SkillCD: remaining, Type: protocol.MainUIType_SkillSlot})
			}
		}
	}
}

func (s *Server) pushCombatHP(ch *channel, unitID int64, hp int32) {
	if ch == nil || ch.session == nil || unitID == 0 {
		return
	}
	if hp < 0 {
		hp = 0
	}
	s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
		UnitId: unitID, NumericType: 1001, Value: float32(hp), ActorId: ch.session.playerID,
	})
}

func (s *Server) emitCombatState(ch *channel, battle *battleState, event CombatEvent, change protocol.ChangeType) {
	meta := battle.effectMeta[event.Key]
	iconID, ok := normalizeClientBuffIcon(meta.iconID)
	if !ok {
		// Damage-only/internal modifiers commonly use IconID "0". Sending them
		// creates blank entries because the client always looks in Skill_fui.
		return
	}
	if meta.modifierID == 0 {
		meta.modifierID = modifierIDFromKey(event.Key)
	}
	durationMS := meta.durationMS
	if change == protocol.ChangeType_Reduce {
		// This client routes Reduce through BuffComponent.AddBuff as well.
		// A zero lifetime lets its 500ms cleanup timer remove forced clears.
		durationMS = 0
	}
	s.sendPush(ch, protocol.OpM2C_BattleChangeState, &protocol.M2C_BattleChangeState{
		Id: meta.modifierID, TargetUnitId: combatUnitID(event.Target), IconId: iconID,
		IconDesc: meta.iconDesc, Type: change, Time: durationMS, IsBuff: meta.isBuff,
		ActorId: ch.session.playerID,
	})
}

func (s *Server) emitCombatTouch(ch *channel, battle *battleState, event CombatEvent) {
	meta := battle.effectMeta[event.Key]
	s.sendPush(ch, protocol.OpM2C_BattleTouchState, &protocol.M2C_BattleTouchState{
		UnitId: combatUnitID(event.Source), TargetUnitId: combatUnitID(event.Target),
		StateType: meta.stateKey, UserData: maxInt32(event.Amount, event.Absorbed), EffectId: meta.effectID,
	})
}

func modifierIDFromKey(key string) int64 {
	parts := strings.Split(key, ":")
	if len(parts) < 3 || parts[0] != "modifier" {
		return 0
	}
	id, _ := strconv.ParseInt(parts[1], 10, 64)
	return id
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func combatUnitID(ref CombatUnitRef) int64 { return ref.ID }

func (s *Server) settleCombatLocked(ch *channel, battle *battleState) bool {
	if battle == nil || battle.ended {
		return true
	}
	if battle.pvp != nil {
		return s.settlePVPCombatLocked(ch, battle)
	}
	if battle.party != nil {
		return s.settlePartyCombatLocked(ch, battle)
	}
	if battle.playerHP <= 0 {
		battle.playerHP = 0
		if battle.runtime != nil {
			s.emitCombatEvents(ch, battle, battle.runtime.Cancel())
		}
		battle.ended = true
		persistFamilyBossProgress(ch.session.familyID, battle)
		ch.session.autoBattle = false
		ch.session.autoBattleEpoch++
		syncBattleHealthToSession(ch.session) // 败北残血写回（0 血）→ 吃药可恢复
		s.applyMagicBallRecover(ch)           // 魔法球：败北同样触发自动回满
		s.sendBattleDefeat(ch, battle)
		ch.session.battle = nil
		ch.session.idleBattle = false
		if battle.activity != nil {
			s.returnFromActivity(ch, battle.activity)
		} else if battle.battleType == trialBattleType {
			x, y := mainCityReturnSpawn()
			s.changeMap(ch, 10004, x, y)
		}
		return true
	}
	if len(battle.aliveMonsters()) == 0 {
		if battle.runtime != nil {
			s.emitCombatEvents(ch, battle, battle.runtime.Cancel())
		}
		battle.ended = true
		// Callers already hold battleMu. Invalidate the automatic worker without
		// recursively taking the same mutex. 胜利【保留 autoBattle 开关】：
		// 自动战斗持续——打完当前怪，玩家点下一个怪时 finishStartBattle
		// 重新拉起 worker（用户实测"打怪结束后打下一个不自动了"）。
		ch.session.autoBattleEpoch++
		if battleVictoryDelay <= 0 {
			s.emitVictory(ch, battle)
			return true
		}
		log.Printf("[S=%d] all monsters dead; victory in %s", ch.id, battleVictoryDelay)
		time.AfterFunc(battleVictoryDelay, func() {
			ss := ch.session
			if ss == nil {
				return
			}
			ss.battleMu.Lock()
			defer ss.battleMu.Unlock()
			if ss.battle != battle || !battle.ended {
				return
			}
			if s.closed.Load() {
				ss.battle = nil
				return
			}
			s.emitVictory(ch, battle)
		})
		return true
	}
	return false
}

func (s *Server) combatTickerLoop() {
	ticker := time.NewTicker(combatTickerInterval)
	defer ticker.Stop()
	for range ticker.C {
		if s.closed.Load() {
			return
		}
		s.mu.RLock()
		channels := make([]*channel, 0, len(s.conns))
		for _, ch := range s.conns {
			channels = append(channels, ch)
		}
		s.mu.RUnlock()
		for _, ch := range channels {
			if ch == nil || ch.session == nil {
				continue
			}
			ss := ch.session
			ss.battleMu.Lock()
			battle := ss.battle
			if battle != nil && !battle.ended && battle.runtime != nil {
				if battle.pvp != nil {
					battle.pvp.mu.Lock()
				}
				if battle.party != nil {
					battle.party.mu.Lock()
				}
				events := battle.runtime.Tick()
				hooks, hookErr := battle.triggerTimedEventHooks(events)
				if hookErr != nil {
					log.Printf("[S=%d] timed modifier hook: %v", ch.id, hookErr)
				}
				events = append(events, hooks...)
				if battle.pvp != nil {
					syncPVPStateLocked(battle)
				}
				s.emitCombatEvents(ch, battle, events)
				s.settleCombatLocked(ch, battle)
				if battle.party != nil {
					battle.party.mu.Unlock()
				}
				if battle.pvp != nil {
					battle.pvp.mu.Unlock()
				}
			}
			ss.battleMu.Unlock()
		}
	}
}
