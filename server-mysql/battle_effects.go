package main

import (
	"errors"
	"fmt"
	"log"
	"math"
	"math/rand"
	"sort"
	"time"
)

var (
	ErrCombatEnded   = errors.New("combat has ended")
	ErrInvalidUnit   = errors.New("invalid combat unit")
	ErrUnitDead      = errors.New("combat unit is dead")
	ErrInvalidEffect = errors.New("invalid combat effect")
)

// CombatClock makes effect boundaries deterministic in tests. The runtime is
// deliberately driven by Tick instead of owning goroutines or timers.
type CombatClock interface {
	Now() time.Time
}

type systemCombatClock struct{}

func (systemCombatClock) Now() time.Time { return time.Now() }

type CombatUnitSide uint8

const (
	CombatSideNone CombatUnitSide = iota
	CombatSidePlayer
	CombatSideMonster
)

// CombatUnitRef is the only unit identity accepted by the effect runtime.
// Monster IDs are battle unit IDs, not MonsterBase IDs.
type CombatUnitRef struct {
	Side CombatUnitSide
	ID   int64
}

func PlayerCombatUnit(id int64) CombatUnitRef {
	return CombatUnitRef{Side: CombatSidePlayer, ID: id}
}

func MonsterCombatUnit(id int64) CombatUnitRef {
	return CombatUnitRef{Side: CombatSideMonster, ID: id}
}

func (r CombatUnitRef) IsZero() bool { return r.Side == CombatSideNone }

type CombatAttribute uint8

const (
	CombatAttributeNone CombatAttribute = iota
	CombatAttributeMaxHP
	CombatAttributeMaxMP
	CombatAttributePhysicalAttack
	CombatAttributeSpiritualAttack
	CombatAttributePhysicalDefense
	CombatAttributeSpiritualDefense
	CombatAttributeCritRate
	CombatAttributePhysicalCritRate
	CombatAttributeSpiritualCritRate
	CombatAttributeCritValue
	CombatAttributePhysicalCritValue
	CombatAttributeSpiritualCritValue
	CombatAttributeAntiCritRate
	CombatAttributePhysicalAntiCritRate
	CombatAttributeSpiritualAntiCritRate
	CombatAttributeAntiCritValue
	CombatAttributePhysicalAntiCritValue
	CombatAttributeSpiritualAntiCritValue
	CombatAttributeDamageReduction
	CombatAttributePhysicalDamageReduction
	CombatAttributeSpiritualDamageReduction
	CombatAttributeAuxiliary
	CombatAttributeSpeed
	CombatAttributeHit
	CombatAttributeResistance
	CombatAttributeDamageMultiplier
)

type CombatStatus uint8

const (
	CombatStatusNone CombatStatus = iota
	CombatStatusStunned
	CombatStatusSilenced
	CombatStatusFrozen
	CombatStatusPetrified
	CombatStatusInvisible
	CombatStatusInvincible
	CombatStatusUndying
	CombatStatusMarker
)

type CombatEffectKind uint8

const (
	CombatEffectNone CombatEffectKind = iota
	CombatEffectInstantDamage
	CombatEffectInstantHeal
	CombatEffectShield
	CombatEffectAttribute
	CombatEffectDamageOverTime
	CombatEffectHealOverTime
	CombatEffectStatus
	CombatEffectDispel
	CombatEffectLifesteal
	CombatEffectReflect
	CombatEffectCounter
)

type CombatEffectStackMode uint8

const (
	CombatEffectReplace CombatEffectStackMode = iota
	CombatEffectRefresh
	CombatEffectStack
)

type CombatEffectPolarity uint8

const (
	CombatEffectNeutral CombatEffectPolarity = iota
	CombatEffectBeneficial
	CombatEffectHarmful
)

type CombatDispelFilter uint8

const (
	CombatDispelNone CombatDispelFilter = iota
	CombatDispelHarmful
	CombatDispelBeneficial
	CombatDispelAll
)

type CombatEventType uint8

const (
	CombatEventEffectApplied CombatEventType = iota + 1
	CombatEventEffectRefreshed
	CombatEventEffectStacked
	CombatEventEffectRemoved
	CombatEventEffectExpired
	CombatEventEffectDispelled
	CombatEventDamage
	CombatEventHeal
	CombatEventShieldAbsorb
	CombatEventImmune
	CombatEventDeath
	CombatEventResourceClamped
	CombatEventLifesteal
	CombatEventReflect
	CombatEventCounter
	CombatEventVisual
	CombatEventGlobalCooldown
	CombatEventAttributeChanged
)

type CombatEvent struct {
	Sequence         int64
	StateID          int64
	At               time.Time
	Type             CombatEventType
	Source           CombatUnitRef
	Target           CombatUnitRef
	Key              string
	Amount           int32
	Absorbed         int32
	HPAfter          int32
	MaxHP            int32
	Stacks           int
	Status           CombatStatus
	Attribute        CombatAttribute
	AttributeValue   float64
	StackAdded       bool
	Reason           string
	DelayMS          int32
	EffectID         int32
	EffectPos        int32
	EffectTargetType int32
	IsCrit           bool
	Hidden           bool
}

type CombatUnitSnapshot struct {
	Ref    CombatUnitRef
	HP     int32
	MaxHP  int32
	Alive  bool
	Shield int32
}

type CombatEffectContext struct {
	Source CombatUnitRef
	Target CombatUnitRef
}

// CombatEffect is the stable contract consumed by a skill plan. Loaders can
// either emit EffectSpec values or implement this interface directly.
type CombatEffect interface {
	Apply(*CombatRuntime, CombatEffectContext) ([]CombatEvent, error)
}

// EffectSpec is the generic loader-facing representation. Value is a flat
// amount; Percent is used by attribute modifiers and reactive effects.
type EffectSpec struct {
	Key                string
	Kind               CombatEffectKind
	ModifierID         int64
	ModifierTag        int32
	Value              int32
	Percent            float64
	CriticalChance     float64
	CriticalMultiplier float64
	Duration           time.Duration
	Interval           time.Duration
	StackMode          CombatEffectStackMode
	MaxStacks          int
	Attribute          CombatAttribute
	Status             CombatStatus
	Polarity           CombatEffectPolarity
	DispelFilter       CombatDispelFilter
	DispelCount        int
	Undispellable      bool
	Hidden             bool
}

func (s EffectSpec) Apply(runtime *CombatRuntime, context CombatEffectContext) ([]CombatEvent, error) {
	if runtime == nil {
		return nil, ErrInvalidEffect
	}
	return runtime.ApplyEffect(context, s)
}

// ApplyEffects preserves declaration order, which lets a skill plan perform
// immediate damage before applying its secondary state. Events emitted before
// an invalid effect are returned to the caller; combat mutations are not
// rolled back because they may already have been synchronized to the client.
func (r *CombatRuntime) ApplyEffects(context CombatEffectContext, effects []CombatEffect) ([]CombatEvent, error) {
	var events []CombatEvent
	for index, effect := range effects {
		if effect == nil {
			return events, fmt.Errorf("effect %d: %w", index, ErrInvalidEffect)
		}
		applied, err := effect.Apply(r, context)
		events = append(events, applied...)
		if err != nil {
			return events, fmt.Errorf("effect %d: %w", index, err)
		}
	}
	return events, nil
}

type CombatDamageRequest struct {
	Source    CombatUnitRef
	Target    CombatUnitRef
	Amount    int32
	Periodic  bool
	Reflected bool
	Counter   bool
	IsCrit    bool
}

type activeCombatEffect struct {
	id          int64
	key         string
	kind        CombatEffectKind
	modifierID  int64
	modifierTag int32
	source      CombatUnitRef
	target      CombatUnitRef
	value       int32
	// overlayValue is the sum of every layer's own value in one overlay bucket.
	// value*stacks would be wrong whenever two different skills feed the same
	// bucket with different numbers: the tick owes the sum, not the first value
	// repeated. Identical values give the same total either way.
	overlayValue       int32
	percent            float64
	criticalChance     float64
	criticalMultiplier float64
	attribute          CombatAttribute
	status             CombatStatus
	polarity           CombatEffectPolarity
	undispellable      bool
	stacks             int
	maxStacks          int
	appliedAt          time.Time
	expiresAt          time.Time
	interval           time.Duration
	nextTick           time.Time
	shield             int64
	hidden             bool
}

type combatUnitAccess struct {
	ref       CombatUnitRef
	hp        *int32
	baseMaxHP int32
	monster   *monsterUnit
	battle    *battleState
}

func (u combatUnitAccess) alive() bool {
	if u.hp == nil || *u.hp <= 0 {
		return false
	}
	return u.monster == nil || u.monster.alive
}

func (u combatUnitAccess) setDead() {
	if u.hp != nil {
		*u.hp = 0
	}
	if u.monster != nil {
		u.monster.alive = false
	}
}

// CombatRuntime owns only ephemeral battle effects. Base HP and attributes
// remain in battleState/monsterUnit, so canceling the runtime cannot leak
// status into a later battle.
type CombatRuntime struct {
	battle   *battleState
	playerID int64
	clock    CombatClock
	// shared points at the canonical runtime state for a party fight. Each
	// member keeps its own runtime wrapper (and therefore its own Player and
	// battleState), while effects, event sequencing and cancellation are shared.
	shared            *CombatRuntime
	effects           []*activeCombatEffect
	nextID            int64
	eventSeq          int64
	cancelled         bool
	advancing         bool
	inFlight          int
	familyBossScaling *familyBossCombatScaling
}

func NewCombatRuntime(battle *battleState, playerID int64, clock CombatClock) *CombatRuntime {
	if clock == nil {
		clock = systemCombatClock{}
	}
	return &CombatRuntime{battle: battle, playerID: playerID, clock: clock}
}

func (r *CombatRuntime) state() *CombatRuntime {
	if r != nil && r.shared != nil {
		return r.shared
	}
	return r
}

func (r *CombatRuntime) Player() CombatUnitRef { return PlayerCombatUnit(r.playerID) }

func (r *CombatRuntime) Cancelled() bool {
	state := r.state()
	return state == nil || state.cancelled
}

func (r *CombatRuntime) Cancel() []CombatEvent {
	state := r.state()
	if state == nil || state.cancelled {
		return nil
	}
	now := r.clock.Now()
	var events []CombatEvent
	for _, effect := range append([]*activeCombatEffect(nil), state.effects...) {
		events = append(events, r.removeEffect(effect, CombatEventEffectRemoved, "battle-ended", now)...)
	}
	state.effects = nil
	state.cancelled = true
	return events
}

func (r *CombatRuntime) checkActive() error {
	state := r.state()
	if r == nil || state == nil || state.cancelled || r.battle == nil {
		return ErrCombatEnded
	}
	if r.battle.ended {
		r.Cancel()
		return ErrCombatEnded
	}
	return nil
}

func (r *CombatRuntime) normalize(ref CombatUnitRef) CombatUnitRef {
	if ref.Side == CombatSidePlayer && ref.ID == 0 {
		ref.ID = r.playerID
	}
	return ref
}

func (r *CombatRuntime) resolve(ref CombatUnitRef) (combatUnitAccess, bool) {
	if r == nil || r.battle == nil {
		return combatUnitAccess{}, false
	}
	ref = r.normalize(ref)
	switch ref.Side {
	case CombatSidePlayer:
		playerBattle := r.battle
		if ref.ID != r.playerID {
			if r.battle.party == nil {
				return combatUnitAccess{}, false
			}
			playerBattle = r.battle.party.members[ref.ID]
			if playerBattle == nil {
				return combatUnitAccess{}, false
			}
		}
		return combatUnitAccess{
			ref: ref, hp: &playerBattle.playerHP, baseMaxHP: playerBattle.playerMaxHP, battle: playerBattle,
		}, true
	case CombatSideMonster:
		for _, monster := range r.battle.monsters {
			if monster != nil && monster.id == ref.ID {
				return combatUnitAccess{
					ref: ref, hp: &monster.hp, baseMaxHP: monster.maxHP,
					monster: monster, battle: r.battle,
				}, true
			}
		}
	}
	return combatUnitAccess{}, false
}

func (r *CombatRuntime) Unit(ref CombatUnitRef) (CombatUnitSnapshot, bool) {
	if r == nil {
		return CombatUnitSnapshot{}, false
	}
	unit, ok := r.resolve(ref)
	if !ok {
		return CombatUnitSnapshot{}, false
	}
	return CombatUnitSnapshot{
		Ref: unit.ref, HP: *unit.hp, MaxHP: r.effectiveAttribute(unit.ref, CombatAttributeMaxHP),
		Alive: unit.alive(), Shield: r.totalShield(unit.ref),
	}, true
}

func (r *CombatRuntime) event(at time.Time, eventType CombatEventType, source, target CombatUnitRef) CombatEvent {
	state := r.state()
	state.eventSeq++
	return CombatEvent{Sequence: state.eventSeq, At: at, Type: eventType, Source: source, Target: target}
}

func (r *CombatRuntime) baseAttribute(unit combatUnitAccess, attribute CombatAttribute) int32 {
	if unit.monster != nil {
		if numericType := combatAttributeNumericType(attribute); numericType != 0 {
			value := unit.monster.extraNumeric[numericType]
			if ratioNumericType(numericType) {
				value *= 100
			}
			return int32(math.Round(value))
		}
	}
	if unit.monster == nil && unit.battle != nil && unit.battle.owner != nil {
		ss := unit.battle.owner
		switch attribute {
		case CombatAttributePhysicalCritRate:
			return int32(math.Round(ss.playerExtraNumeric(1013)))
		case CombatAttributeSpiritualCritRate:
			return int32(math.Round(ss.playerExtraNumeric(1014)))
		case CombatAttributePhysicalCritValue:
			return int32(math.Round(ss.playerExtraNumeric(1015)))
		case CombatAttributeSpiritualCritValue:
			return int32(math.Round(ss.playerExtraNumeric(1016)))
		case CombatAttributePhysicalAntiCritRate:
			return int32(math.Round(ss.playerExtraNumeric(1018)))
		case CombatAttributeSpiritualAntiCritRate:
			return int32(math.Round(ss.playerExtraNumeric(1020)))
		case CombatAttributePhysicalAntiCritValue:
			return int32(math.Round(ss.playerExtraNumeric(1019)))
		case CombatAttributeSpiritualAntiCritValue:
			return int32(math.Round(ss.playerExtraNumeric(1021)))
		case CombatAttributePhysicalDamageReduction:
			return int32(math.Round(ss.playerExtraNumeric(1022)))
		case CombatAttributeSpiritualDamageReduction:
			return int32(math.Round(ss.playerExtraNumeric(1023)))
		case CombatAttributeSpeed:
			return int32(math.Round(ss.playerExtraNumeric(1031)))
		case CombatAttributeHit:
			return int32(math.Round(ss.playerExtraNumeric(1032)))
		case CombatAttributeResistance:
			return int32(math.Round(ss.playerExtraNumeric(1033)))
		}
	}
	switch attribute {
	case CombatAttributeMaxHP:
		return unit.baseMaxHP
	case CombatAttributeMaxMP:
		if unit.monster == nil {
			return unit.battle.playerMaxMP
		}
		return 0
	case CombatAttributePhysicalAttack:
		if unit.monster != nil {
			return unit.monster.phyAtk
		}
		return unit.battle.phyAtk
	case CombatAttributeSpiritualAttack:
		if unit.monster != nil {
			return unit.monster.spiAtk
		}
		return unit.battle.spiAtk
	case CombatAttributePhysicalDefense:
		if unit.monster != nil {
			return unit.monster.phyDef
		}
		return unit.battle.phyDef
	case CombatAttributeSpiritualDefense:
		if unit.monster != nil {
			return unit.monster.spiDef
		}
		return unit.battle.spiDef
	default:
		return 0
	}
}

func combatAttributeNumericType(attribute CombatAttribute) int32 {
	switch attribute {
	case CombatAttributePhysicalCritRate:
		return 1013
	case CombatAttributeSpiritualCritRate:
		return 1014
	case CombatAttributePhysicalCritValue:
		return 1015
	case CombatAttributeSpiritualCritValue:
		return 1016
	case CombatAttributeAuxiliary:
		return 1017
	case CombatAttributePhysicalAntiCritRate:
		return 1018
	case CombatAttributePhysicalAntiCritValue:
		return 1019
	case CombatAttributeSpiritualAntiCritRate:
		return 1020
	case CombatAttributeSpiritualAntiCritValue:
		return 1021
	case CombatAttributePhysicalDamageReduction:
		return 1022
	case CombatAttributeSpiritualDamageReduction:
		return 1023
	case CombatAttributeSpeed:
		return 1031
	case CombatAttributeHit:
		return 1032
	case CombatAttributeResistance:
		return 1033
	default:
		return 0
	}
}

func ratioNumericType(numericType int32) bool {
	return numericType >= 1013 && numericType <= 1023
}

func (r *CombatRuntime) basePointAttribute(unit combatUnitAccess, attribute CombatAttribute) float64 {
	numericType := combatAttributeNumericType(attribute)
	if numericType == 0 {
		return float64(r.baseAttribute(unit, attribute))
	}
	var value float64
	if unit.monster != nil {
		value = unit.monster.extraNumeric[numericType]
	} else if unit.battle != nil && unit.battle.owner != nil {
		value = unit.battle.owner.playerExtraNumeric(numericType)
	}
	// Equipment/MonsterBase stores rates as fractions (0.1 means 10%). The
	// client GetFloatPercent helper multiplies the same fields by 100.
	if ratioNumericType(numericType) {
		value *= 100
	}
	return value
}

// ratioNumericType 报告这个 NumericType 存的是"比率"（0.1 表示 10%）还是整数点数。
//
// 名单的依据是**字段量纲**，两条独立证据：
//   * EquipBase 里这些比率字段的取值范围是 0.005–0.9（Pcrir/Mcrir/Dvo/Rpcri/Nphyi…），
//     而 Hit(1–200) / Res(1–170) / PhyAtk / PhyDef / Hp 都是整数点数，量纲与它们一致；
//   * 客户端把小数显示成百分比的工具 GetFloatPercent 只覆盖到「辅助值」，
//     Hit/Res 不在里面；
//   * 线上抓包里比率字段全部以小数下发（Dvo=0.05266、Pcrir=0.30、Pcri=0.48），
//     而 1031/1032/1033 **一次都没出现过**。
//
// 之前把 1032/1033 也算作比率，会让玩家的 Hit/Res 被放大 100 倍：毕业装
// Hit=200 变成 20000、Res=245.6 变成 24560，直接把 statusContestChance 打穿
// （玩家→怪物顶到 100%、怪物→玩家压到 6%）。同一属性在整数路径 baseAttribute
// 里拿到的是 200/245.6，两条路径自相矛盾，也说明 ×100 是多余的。
//
// 注意：客户端快照推的是 playerExtraNumeric()，本来就不含这个 ×100，
// 所以改这里不影响下发给客户端的数值。

func (r *CombatRuntime) EffectiveAttribute(ref CombatUnitRef, attribute CombatAttribute) int32 {
	if r == nil {
		return 0
	}
	return r.effectiveAttribute(ref, attribute)
}

func (r *CombatRuntime) effectiveAttribute(ref CombatUnitRef, attribute CombatAttribute) int32 {
	unit, ok := r.resolve(ref)
	if !ok {
		return 0
	}
	base := int64(r.baseAttribute(unit, attribute))
	var flat int64
	percent, familyBossScaled := r.familyBossAttributePercent(unit.ref, attribute)
	for _, effect := range r.state().effects {
		if effect.target == unit.ref && effect.kind == CombatEffectAttribute && effect.attribute == attribute {
			flat += int64(effect.value) * int64(effect.stacks)
			percent += effect.percent * float64(effect.stacks)
		}
	}
	value := float64(base)*(1+percent/100) + float64(flat)
	if combatAttributeUsesPoints(attribute) && !familyBossScaled {
		value = float64(base) + percent + float64(flat)
	}
	minimum := float64(0)
	if attribute == CombatAttributeMaxHP {
		minimum = 1
	}
	if math.IsNaN(value) || value < minimum {
		value = minimum
	}
	if value > float64(math.MaxInt32) {
		value = float64(math.MaxInt32)
	}
	return int32(math.Round(value))
}

func combatAttributeUsesPoints(attribute CombatAttribute) bool {
	return attribute >= CombatAttributeCritRate
}

func (r *CombatRuntime) EffectivePercentAttribute(ref CombatUnitRef, attribute CombatAttribute) float64 {
	if r == nil {
		return 0
	}
	unit, ok := r.resolve(ref)
	if !ok {
		return 0
	}
	base := r.basePointAttribute(unit, attribute)
	value := base
	var flat float64
	percent, familyBossScaled := r.familyBossAttributePercent(unit.ref, attribute)
	for _, effect := range r.state().effects {
		if effect.target == unit.ref && effect.kind == CombatEffectAttribute && effect.attribute == attribute {
			flat += float64(effect.value * int32(effect.stacks))
			percent += effect.percent * float64(effect.stacks)
		}
	}
	if familyBossScaled {
		value = base*(1+percent/100) + flat
	} else {
		value += flat + percent
	}
	if math.IsNaN(value) {
		return 0
	}
	return value
}

func (r *CombatRuntime) DamageMultiplier(ref CombatUnitRef) float64 {
	if r == nil {
		return 1
	}
	ref = r.normalize(ref)
	multiplier := 1.0
	for _, effect := range r.state().effects {
		if effect.target != ref || effect.kind != CombatEffectAttribute || effect.attribute != CombatAttributeDamageMultiplier {
			continue
		}
		value := effect.percent
		if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		for i := 0; i < effect.stacks; i++ {
			multiplier *= value
			if multiplier >= 1000 {
				return 1000
			}
		}
	}
	return multiplier
}

func (r *CombatRuntime) HasStatus(ref CombatUnitRef, status CombatStatus) bool {
	if r == nil {
		return false
	}
	ref = r.normalize(ref)
	for _, effect := range r.state().effects {
		if effect.target == ref && effect.kind == CombatEffectStatus && effect.status == status {
			return true
		}
	}
	return false
}

func (r *CombatRuntime) CanAct(ref CombatUnitRef) bool {
	unit, ok := r.Unit(ref)
	if !ok || !unit.Alive {
		return false
	}
	return !r.hasAnyStatus(unit.Ref, CombatStatusStunned, CombatStatusFrozen, CombatStatusPetrified)
}

func (r *CombatRuntime) CanCast(ref CombatUnitRef) bool {
	return r.CanAct(ref) && !r.HasStatus(ref, CombatStatusSilenced)
}

func (r *CombatRuntime) CanBeTargeted(ref CombatUnitRef) bool {
	unit, ok := r.Unit(ref)
	return ok && unit.Alive && !r.HasStatus(unit.Ref, CombatStatusInvisible)
}

func (r *CombatRuntime) hasAnyStatus(ref CombatUnitRef, statuses ...CombatStatus) bool {
	ref = r.normalize(ref)
	for _, effect := range r.state().effects {
		if effect.target != ref || effect.kind != CombatEffectStatus {
			continue
		}
		for _, status := range statuses {
			if effect.status == status {
				return true
			}
		}
	}
	return false
}

func (r *CombatRuntime) ApplyEffect(context CombatEffectContext, spec EffectSpec) ([]CombatEvent, error) {
	if err := r.checkActive(); err != nil {
		return nil, err
	}
	events := r.advanceTo(r.clock.Now())
	context.Source = r.normalize(context.Source)
	context.Target = r.normalize(context.Target)
	target, ok := r.resolve(context.Target)
	if !ok {
		return events, ErrInvalidUnit
	}
	if !target.alive() {
		return events, ErrUnitDead
	}
	if err := validateEffectSpec(spec); err != nil {
		return events, err
	}
	now := r.clock.Now()
	switch spec.Kind {
	case CombatEffectInstantDamage:
		damageEvents, err := r.applyDamageInternal(CombatDamageRequest{
			Source: context.Source, Target: context.Target, Amount: spec.Value,
		}, now)
		return append(events, damageEvents...), err
	case CombatEffectInstantHeal:
		healEvents, err := r.applyHealInternal(context.Source, context.Target, spec.Value, CombatEventHeal, now)
		return append(events, healEvents...), err
	case CombatEffectDispel:
		dispelEvents, err := r.dispelInternal(context.Source, context.Target, spec.DispelFilter, spec.DispelCount, now)
		return append(events, dispelEvents...), err
	default:
		applyEvents, err := r.addPersistentEffect(context, spec, now)
		if err == nil {
			applyEvents = r.familyBossPersistentEffectEvents(context, spec, applyEvents, now)
		}
		return append(events, applyEvents...), err
	}
}

func validateEffectSpec(spec EffectSpec) error {
	if spec.Kind <= CombatEffectNone || spec.Kind > CombatEffectCounter ||
		math.IsNaN(spec.Percent) || math.IsInf(spec.Percent, 0) ||
		math.IsNaN(spec.CriticalChance) || math.IsInf(spec.CriticalChance, 0) ||
		math.IsNaN(spec.CriticalMultiplier) || math.IsInf(spec.CriticalMultiplier, 0) ||
		spec.CriticalChance < 0 || spec.CriticalChance > 100 ||
		(spec.CriticalChance > 0 && spec.CriticalMultiplier < 1) || spec.Duration < 0 || spec.Interval < 0 ||
		spec.StackMode > CombatEffectStack || spec.MaxStacks > 1000 ||
		spec.Polarity > CombatEffectHarmful || spec.DispelCount < 0 {
		return ErrInvalidEffect
	}
	switch spec.Kind {
	case CombatEffectInstantDamage, CombatEffectInstantHeal, CombatEffectShield:
		if spec.Value <= 0 {
			return ErrInvalidEffect
		}
	case CombatEffectAttribute:
		if spec.Attribute <= CombatAttributeNone || spec.Attribute > CombatAttributeDamageMultiplier ||
			(spec.Value == 0 && spec.Percent == 0) {
			return ErrInvalidEffect
		}
	case CombatEffectDamageOverTime, CombatEffectHealOverTime:
		if spec.Value <= 0 || spec.Interval <= 0 {
			return ErrInvalidEffect
		}
	case CombatEffectStatus:
		if spec.Status <= CombatStatusNone || spec.Status > CombatStatusMarker {
			return ErrInvalidEffect
		}
	case CombatEffectDispel:
		if spec.DispelFilter == CombatDispelNone || spec.DispelFilter > CombatDispelAll {
			return ErrInvalidEffect
		}
	case CombatEffectLifesteal, CombatEffectReflect:
		if spec.Percent <= 0 {
			return ErrInvalidEffect
		}
	}
	return nil
}

func (r *CombatRuntime) addPersistentEffect(context CombatEffectContext, spec EffectSpec, now time.Time) ([]CombatEvent, error) {
	key := spec.Key
	if key == "" {
		key = fmt.Sprintf("effect:%d:%d:%d:%d:%d", spec.Kind, spec.Attribute, spec.Status, context.Source.Side, context.Source.ID)
	}
	maxStacks := spec.MaxStacks
	if maxStacks <= 0 {
		maxStacks = 1
	}
	var events []CombatEvent
	if existing := r.findEffect(context.Target, key); existing != nil {
		switch spec.StackMode {
		case CombatEffectRefresh:
			existing.modifierID = spec.ModifierID
			existing.modifierTag = spec.ModifierTag
			existing.value = spec.Value
			existing.percent = spec.Percent
			// A refresh rewrites the amount of every layer it already holds, the
			// same way the attribute path recomputes value*stacks, so the bucket's
			// next tick owes the new per-layer amount for each of them.
			if existing.kind == CombatEffectDamageOverTime || existing.kind == CombatEffectHealOverTime {
				existing.overlayValue = clampEffectAmount(int64(spec.Value) * int64(existing.stacks))
			}
			existing.criticalChance = spec.CriticalChance
			existing.criticalMultiplier = spec.CriticalMultiplier
			existing.expiresAt = expiry(now, spec.Duration)
			event := r.event(now, CombatEventEffectRefreshed, context.Source, context.Target)
			event.Key, event.Stacks, event.Status, event.Attribute = key, existing.stacks, existing.status, existing.attribute
			event.Hidden = existing.hidden
			events = append(events, event)
			events = append(events, r.clampUnitHP(context.Source, context.Target, key, now)...)
			return events, nil
		case CombatEffectStack:
			oldStacks := existing.stacks
			existing.modifierID = spec.ModifierID
			existing.modifierTag = spec.ModifierTag
			if existing.stacks < existing.maxStacks {
				existing.stacks++
			}
			existing.expiresAt = expiry(now, spec.Duration)
			existing.criticalChance = spec.CriticalChance
			existing.criticalMultiplier = spec.CriticalMultiplier
			if existing.stacks > oldStacks {
				switch existing.kind {
				case CombatEffectShield:
					existing.shield += int64(spec.Value)
				case CombatEffectDamageOverTime, CombatEffectHealOverTime:
					// Each layer brings its own amount, so the bucket's next tick
					// owes the sum of the layers rather than value*stacks.
					existing.overlayValue += spec.Value
				}
			}
			event := r.event(now, CombatEventEffectStacked, context.Source, context.Target)
			event.Key, event.Stacks, event.Status, event.Attribute = key, existing.stacks, existing.status, existing.attribute
			event.StackAdded = existing.stacks > oldStacks
			event.Hidden = existing.hidden
			events = append(events, event)
			events = append(events, r.clampUnitHP(context.Source, context.Target, key, now)...)
			return events, nil
		default:
			// Replacement is atomic. In particular, replacing a MaxHP buff must
			// not clamp HP against the transient state between old and new effects.
			if r.detachEffect(existing) {
				event := r.event(now, CombatEventEffectRemoved, existing.source, existing.target)
				event.Key, event.Stacks, event.Status, event.Attribute, event.Reason =
					existing.key, existing.stacks, existing.status, existing.attribute, "replaced"
				event.Hidden = existing.hidden
				events = append(events, event)
			}
		}
	}
	state := r.state()
	state.nextID++
	effect := &activeCombatEffect{
		id: state.nextID, key: key, kind: spec.Kind, source: context.Source, target: context.Target,
		modifierID: spec.ModifierID, modifierTag: spec.ModifierTag,
		value: spec.Value, overlayValue: spec.Value, percent: spec.Percent,
		criticalChance: spec.CriticalChance, criticalMultiplier: spec.CriticalMultiplier,
		attribute: spec.Attribute, status: spec.Status,
		polarity: effectPolarity(spec), undispellable: spec.Undispellable, stacks: 1,
		maxStacks: maxStacks, appliedAt: now, expiresAt: expiry(now, spec.Duration), interval: spec.Interval,
		hidden: spec.Hidden,
	}
	if effect.kind == CombatEffectShield {
		effect.shield = int64(effect.value)
	}
	if effect.kind == CombatEffectDamageOverTime || effect.kind == CombatEffectHealOverTime {
		effect.nextTick = now.Add(effect.interval)
	}
	state.effects = append(state.effects, effect)
	event := r.event(now, CombatEventEffectApplied, context.Source, context.Target)
	event.Key, event.Amount, event.Stacks, event.Status, event.Attribute = key, spec.Value, 1, spec.Status, spec.Attribute
	event.Hidden = spec.Hidden
	events = append(events, event)
	events = append(events, r.clampUnitHP(context.Source, context.Target, key, now)...)
	return events, nil
}

func effectPolarity(spec EffectSpec) CombatEffectPolarity {
	if spec.Polarity != CombatEffectNeutral {
		return spec.Polarity
	}
	switch spec.Kind {
	case CombatEffectDamageOverTime:
		return CombatEffectHarmful
	case CombatEffectHealOverTime, CombatEffectShield, CombatEffectLifesteal, CombatEffectReflect, CombatEffectCounter:
		return CombatEffectBeneficial
	case CombatEffectAttribute:
		if spec.Value < 0 || spec.Percent < 0 {
			return CombatEffectHarmful
		}
		return CombatEffectBeneficial
	case CombatEffectStatus:
		switch spec.Status {
		case CombatStatusStunned, CombatStatusSilenced, CombatStatusFrozen, CombatStatusPetrified:
			return CombatEffectHarmful
		default:
			return CombatEffectBeneficial
		}
	default:
		return CombatEffectNeutral
	}
}

// clampEffectAmount keeps a per-tick amount inside int32 after summing layers.
func clampEffectAmount(value int64) int32 {
	if value > math.MaxInt32 {
		return math.MaxInt32
	}
	if value < math.MinInt32 {
		return math.MinInt32
	}
	return int32(value)
}

func expiry(now time.Time, duration time.Duration) time.Time {
	if duration <= 0 {
		return time.Time{}
	}
	return now.Add(duration)
}

func (r *CombatRuntime) findEffect(target CombatUnitRef, key string) *activeCombatEffect {
	target = r.normalize(target)
	for _, effect := range r.state().effects {
		if effect.target == target && effect.key == key {
			return effect
		}
	}
	return nil
}

func (r *CombatRuntime) Tick() []CombatEvent {
	if err := r.checkActive(); err != nil {
		return nil
	}
	return r.advanceTo(r.clock.Now())
}

func (r *CombatRuntime) advanceTo(now time.Time) []CombatEvent {
	state := r.state()
	if state == nil || state.cancelled || state.advancing {
		return nil
	}
	if r.battle == nil || r.battle.ended {
		r.Cancel()
		return nil
	}
	state.advancing = true
	defer func() { state.advancing = false }()
	var events []CombatEvent
	for {
		effect := r.nextDueEffect(now)
		if effect == nil {
			break
		}
		tickAt := effect.nextTick
		effect.nextTick = effect.nextTick.Add(effect.interval)
		amount := clampEffectAmount(int64(effect.overlayValue))
		if effect.kind == CombatEffectDamageOverTime {
			tickEvents, _ := r.applyDamageInternal(CombatDamageRequest{
				Source: effect.source, Target: effect.target, Amount: amount, Periodic: true,
			}, tickAt)
			for index := range tickEvents {
				tickEvents[index].Key = effect.key
			}
			events = append(events, tickEvents...)
		} else {
			critical := effect.criticalChance > 0 && rand.Float64()*100 < effect.criticalChance
			if critical {
				amount = positiveCombatAmount(float64(amount) * effect.criticalMultiplier)
			}
			tickEvents, _ := r.applyHealInternal(effect.source, effect.target, amount, CombatEventHeal, tickAt)
			for index := range tickEvents {
				tickEvents[index].Key = effect.key
				tickEvents[index].IsCrit = critical
			}
			events = append(events, tickEvents...)
		}
	}
	expired := make([]*activeCombatEffect, 0)
	for _, effect := range append([]*activeCombatEffect(nil), r.state().effects...) {
		if !effect.expiresAt.IsZero() && !effect.expiresAt.After(now) {
			expired = append(expired, effect)
		}
	}
	sort.SliceStable(expired, func(i, j int) bool {
		if expired[i].expiresAt.Equal(expired[j].expiresAt) {
			return expired[i].id < expired[j].id
		}
		return expired[i].expiresAt.Before(expired[j].expiresAt)
	})
	for _, effect := range expired {
		events = append(events, r.removeEffect(effect, CombatEventEffectExpired, "expired", effect.expiresAt)...)
	}
	events = append(events, r.advanceFamilyBossScalingTo(now)...)
	return events
}

func (r *CombatRuntime) nextDueEffect(now time.Time) *activeCombatEffect {
	var due *activeCombatEffect
	for _, effect := range r.state().effects {
		if effect.interval <= 0 || effect.nextTick.IsZero() || effect.nextTick.After(now) {
			continue
		}
		if !effect.expiresAt.IsZero() && effect.nextTick.After(effect.expiresAt) {
			continue
		}
		if due == nil || effect.nextTick.Before(due.nextTick) ||
			(effect.nextTick.Equal(due.nextTick) && effect.id < due.id) {
			due = effect
		}
	}
	return due
}

func (r *CombatRuntime) ApplyDamage(request CombatDamageRequest) ([]CombatEvent, error) {
	if err := r.checkActive(); err != nil {
		return nil, err
	}
	events := r.advanceTo(r.clock.Now())
	damageEvents, err := r.applyDamageInternal(request, r.clock.Now())
	return append(events, damageEvents...), err
}

func (r *CombatRuntime) applyDamageInternal(request CombatDamageRequest, at time.Time) ([]CombatEvent, error) {
	request.Source = r.normalize(request.Source)
	request.Target = r.normalize(request.Target)
	target, ok := r.resolve(request.Target)
	if !ok {
		return nil, ErrInvalidUnit
	}
	if !target.alive() {
		return nil, ErrUnitDead
	}
	if request.Amount <= 0 {
		return nil, ErrInvalidEffect
	}
	if r.hasAnyStatus(request.Target, CombatStatusInvincible) {
		event := r.event(at, CombatEventImmune, request.Source, request.Target)
		event.Amount, event.HPAfter, event.MaxHP = request.Amount, *target.hp, r.effectiveAttribute(request.Target, CombatAttributeMaxHP)
		uid, monsterID := combatDamageLogIDs(r, request.Source, request.Target)
		log.Printf("[combat.damage] uid=%d monster=%d source=%d:%d target=%d:%d requested=%d actual=0 absorbed=0 hp_before=%d hp_after=%d max_hp=%d dead=false immune=true periodic=%t reflected=%t counter=%t crit=%t",
			uid, monsterID, request.Source.Side, request.Source.ID, request.Target.Side, request.Target.ID,
			request.Amount, *target.hp, *target.hp, event.MaxHP,
			request.Periodic, request.Reflected, request.Counter, request.IsCrit)
		return []CombatEvent{event}, nil
	}

	hpBefore := *target.hp
	maxHP := r.effectiveAttribute(request.Target, CombatAttributeMaxHP)
	reflectPercent := r.reactivePercent(request.Target, CombatEffectReflect)
	remaining := int64(request.Amount)
	shieldAbsorbed := int64(0)
	var events []CombatEvent
	for _, shield := range r.shieldsFor(request.Target) {
		if remaining <= 0 {
			break
		}
		absorbed := remaining
		if absorbed > shield.shield {
			absorbed = shield.shield
		}
		shield.shield -= absorbed
		remaining -= absorbed
		shieldAbsorbed += absorbed
		event := r.event(at, CombatEventShieldAbsorb, shield.source, request.Target)
		event.Key, event.Absorbed, event.Amount, event.HPAfter = shield.key, int32(absorbed), int32(remaining), *target.hp
		events = append(events, event)
		if shield.shield == 0 {
			events = append(events, r.removeEffect(shield, CombatEventEffectRemoved, "depleted", at)...)
		}
	}

	actual := int64(0)
	if remaining > 0 {
		actual = remaining
		minimumHP := int64(0)
		if r.hasAnyStatus(request.Target, CombatStatusUndying) {
			minimumHP = 1
		}
		maximumDamage := int64(*target.hp) - minimumHP
		if maximumDamage < 0 {
			maximumDamage = 0
		}
		if actual > maximumDamage {
			actual = maximumDamage
		}
		*target.hp -= int32(actual)
	}
	damageEvent := r.event(at, CombatEventDamage, request.Source, request.Target)
	damageEvent.Amount, damageEvent.Absorbed, damageEvent.HPAfter = int32(actual), int32(shieldAbsorbed), *target.hp
	damageEvent.MaxHP = maxHP
	damageEvent.IsCrit = request.IsCrit
	events = append(events, damageEvent)

	targetDied := *target.hp <= 0
	uid, monsterID := combatDamageLogIDs(r, request.Source, request.Target)
	log.Printf("[combat.damage] uid=%d monster=%d source=%d:%d target=%d:%d requested=%d actual=%d absorbed=%d hp_before=%d hp_after=%d max_hp=%d dead=%t immune=false periodic=%t reflected=%t counter=%t crit=%t",
		uid, monsterID, request.Source.Side, request.Source.ID, request.Target.Side, request.Target.ID,
		request.Amount, actual, shieldAbsorbed, hpBefore, *target.hp, maxHP,
		targetDied, request.Periodic, request.Reflected, request.Counter, request.IsCrit)
	if targetDied {
		target.setDead()
	}

	if actual > 0 && !request.Periodic && !request.Reflected {
		if lifesteal := r.reactivePercent(request.Source, CombatEffectLifesteal); lifesteal > 0 {
			heal := percentAmount(actual, lifesteal)
			healEvents, _ := r.applyHealInternal(request.Source, request.Source, heal, CombatEventLifesteal, at)
			events = append(events, healEvents...)
		}
		if lifesteal := r.staticLifestealPercent(request.Source); lifesteal > 0 {
			heal := percentAmount(actual, lifesteal)
			healEvents, _ := r.applyHealInternal(request.Source, request.Source, heal, CombatEventLifesteal, at)
			events = append(events, healEvents...)
		}
		if reflectPercent > 0 && r.unitAlive(request.Source) {
			reflected := percentAmount(actual, reflectPercent)
			trigger := r.event(at, CombatEventReflect, request.Target, request.Source)
			trigger.Amount = reflected
			events = append(events, trigger)
			reflectedEvents, _ := r.applyDamageInternal(CombatDamageRequest{
				Source: request.Target, Target: request.Source, Amount: reflected, Reflected: true,
			}, at)
			events = append(events, reflectedEvents...)
		}
		if !targetDied && !request.Counter {
			events = append(events, r.counterAttackReaction(request.Target, request.Source, at)...)
		}
	}
	if targetDied {
		events = append(events, r.clearUnitEffects(request.Target, "death", at)...)
		// State removals must reach the client before the unit-death packet.
		// The client removes a monster on 20082, while its 20080 handler still
		// resolves the target unit to reach BuffComponent.
		death := r.event(at, CombatEventDeath, request.Source, request.Target)
		death.HPAfter, death.MaxHP = 0, damageEvent.MaxHP
		events = append(events, death)
	}
	return events, nil
}

func (r *CombatRuntime) numericPercentValue(ref CombatUnitRef, numericType int32) float64 {
	unit, ok := r.resolve(ref)
	if !ok {
		return 0
	}
	var value float64
	if unit.monster != nil {
		value = unit.monster.extraNumeric[numericType]
	} else if unit.battle != nil && unit.battle.owner != nil {
		value = unit.battle.owner.playerExtraNumeric(numericType)
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return 0
	}
	return math.Round(value*100*1_000_000) / 1_000_000
}

func (r *CombatRuntime) staticLifestealPercent(ref CombatUnitRef) float64 {
	chance := math.Min(100, r.numericPercentValue(ref, 1042))
	amount := r.numericPercentValue(ref, 1043)
	if chance <= 0 || amount <= 0 || (chance < 100 && rand.Float64()*100 >= chance) {
		return 0
	}
	return amount
}

func percentAmount(amount int64, percent float64) int32 {
	raw := math.Ceil(float64(amount) * percent / 100)
	if math.IsNaN(raw) || raw <= 0 {
		return 1
	}
	if math.IsInf(raw, 1) || raw >= float64(math.MaxInt32) {
		return math.MaxInt32
	}
	value := int64(raw)
	if value < 1 {
		value = 1
	}
	if value > math.MaxInt32 {
		value = math.MaxInt32
	}
	return int32(value)
}

func (r *CombatRuntime) ApplyHeal(source, target CombatUnitRef, amount int32) ([]CombatEvent, error) {
	if err := r.checkActive(); err != nil {
		return nil, err
	}
	events := r.advanceTo(r.clock.Now())
	healEvents, err := r.applyHealInternal(source, target, amount, CombatEventHeal, r.clock.Now())
	return append(events, healEvents...), err
}

func (r *CombatRuntime) ApplyHealthRecovery(ref CombatUnitRef) ([]CombatEvent, error) {
	if err := r.checkActive(); err != nil {
		return nil, err
	}
	ref = r.normalize(ref)
	percent := r.numericPercentValue(ref, 1044)
	if percent <= 0 {
		return nil, nil
	}
	maximum := r.effectiveAttribute(ref, CombatAttributeMaxHP)
	if maximum <= 0 {
		return nil, nil
	}
	amount := percentAmount(int64(maximum), percent)
	return r.applyHealInternal(ref, ref, amount, CombatEventHeal, r.clock.Now())
}

func (r *CombatRuntime) applyHealInternal(source, targetRef CombatUnitRef, amount int32, eventType CombatEventType, at time.Time) ([]CombatEvent, error) {
	source = r.normalize(source)
	targetRef = r.normalize(targetRef)
	target, ok := r.resolve(targetRef)
	if !ok {
		return nil, ErrInvalidUnit
	}
	if !target.alive() {
		return nil, ErrUnitDead
	}
	if amount <= 0 {
		return nil, ErrInvalidEffect
	}
	maxHP := r.effectiveAttribute(targetRef, CombatAttributeMaxHP)
	missing := maxHP - *target.hp
	actual := amount
	if actual > missing {
		actual = missing
	}
	if actual < 0 {
		actual = 0
	}
	*target.hp += actual
	event := r.event(at, eventType, source, targetRef)
	event.Amount, event.HPAfter, event.MaxHP = actual, *target.hp, maxHP
	return []CombatEvent{event}, nil
}

func (r *CombatRuntime) Dispel(source, target CombatUnitRef, filter CombatDispelFilter, count int) ([]CombatEvent, error) {
	if err := r.checkActive(); err != nil {
		return nil, err
	}
	events := r.advanceTo(r.clock.Now())
	dispelEvents, err := r.dispelInternal(source, target, filter, count, r.clock.Now())
	return append(events, dispelEvents...), err
}

func (r *CombatRuntime) dispelInternal(source, target CombatUnitRef, filter CombatDispelFilter, count int, at time.Time) ([]CombatEvent, error) {
	source, target = r.normalize(source), r.normalize(target)
	if _, ok := r.resolve(target); !ok {
		return nil, ErrInvalidUnit
	}
	if filter <= CombatDispelNone || filter > CombatDispelAll || count < 0 {
		return nil, ErrInvalidEffect
	}
	var candidates []*activeCombatEffect
	for _, effect := range r.state().effects {
		if effect.target != target || effect.undispellable {
			continue
		}
		if filter == CombatDispelAll ||
			(filter == CombatDispelHarmful && effect.polarity == CombatEffectHarmful) ||
			(filter == CombatDispelBeneficial && effect.polarity == CombatEffectBeneficial) {
			candidates = append(candidates, effect)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].id < candidates[j].id })
	if count > 0 && len(candidates) > count {
		candidates = candidates[:count]
	}
	var events []CombatEvent
	for _, effect := range candidates {
		removed := r.removeEffect(effect, CombatEventEffectDispelled, "dispelled", at)
		for i := range removed {
			removed[i].Source = source
		}
		events = append(events, removed...)
	}
	return events, nil
}

func (r *CombatRuntime) reactivePercent(ref CombatUnitRef, kind CombatEffectKind) float64 {
	ref = r.normalize(ref)
	var total float64
	for _, effect := range r.state().effects {
		if effect.target == ref && effect.kind == kind {
			total += effect.percent * float64(effect.stacks)
		}
	}
	if total < 0 || math.IsNaN(total) {
		return 0
	}
	return total
}

func (r *CombatRuntime) shieldsFor(ref CombatUnitRef) []*activeCombatEffect {
	ref = r.normalize(ref)
	var shields []*activeCombatEffect
	for _, effect := range r.state().effects {
		if effect.target == ref && effect.kind == CombatEffectShield && effect.shield > 0 {
			shields = append(shields, effect)
		}
	}
	sort.SliceStable(shields, func(i, j int) bool { return shields[i].id < shields[j].id })
	return shields
}

func (r *CombatRuntime) totalShield(ref CombatUnitRef) int32 {
	var total int64
	for _, shield := range r.shieldsFor(ref) {
		total += shield.shield
		if total >= math.MaxInt32 {
			return math.MaxInt32
		}
	}
	return int32(total)
}

func (r *CombatRuntime) unitAlive(ref CombatUnitRef) bool {
	if ref.IsZero() {
		return false
	}
	unit, ok := r.resolve(ref)
	return ok && unit.alive()
}

func (r *CombatRuntime) detachEffect(effect *activeCombatEffect) bool {
	if effect == nil {
		return false
	}
	state := r.state()
	for i, candidate := range state.effects {
		if candidate == effect {
			state.effects = append(state.effects[:i], state.effects[i+1:]...)
			return true
		}
	}
	return false
}

func (r *CombatRuntime) removeEffect(effect *activeCombatEffect, eventType CombatEventType, reason string, at time.Time) []CombatEvent {
	if effect == nil {
		return nil
	}
	externalFamilyBossAttribute := r.familyBossExternalAttributeEffect(
		effect.target, effect.kind, effect.attribute, effect.percent,
	) && reason != "battle-ended" && reason != "death"
	var events []CombatEvent
	if externalFamilyBossAttribute {
		events = append(events, r.advanceFamilyBossScalingTo(at)...)
	}
	if !r.detachEffect(effect) {
		return nil
	}
	event := r.event(at, eventType, effect.source, effect.target)
	event.Key, event.Stacks, event.Status, event.Attribute, event.Reason = effect.key, effect.stacks, effect.status, effect.attribute, reason
	event.Hidden = effect.hidden
	events = append(events, event)
	if externalFamilyBossAttribute {
		events = append(events, r.resetFamilyBossScaling(effect.attribute, at)...)
	}
	if effect.kind == CombatEffectAttribute && effect.attribute == CombatAttributeMaxHP {
		events = append(events, r.clampUnitHP(effect.source, effect.target, effect.key, at)...)
	}
	return events
}

func (r *CombatRuntime) HasEffect(target CombatUnitRef, key string) bool {
	return r != nil && r.findEffect(target, key) != nil
}

func (r *CombatRuntime) RemoveEffectByKey(source, target CombatUnitRef, key, reason string) []CombatEvent {
	if r == nil || key == "" {
		return nil
	}
	effect := r.findEffect(target, key)
	if effect == nil {
		return nil
	}
	removed := r.removeEffect(effect, CombatEventEffectRemoved, reason, r.clock.Now())
	for i := range removed {
		removed[i].Source = r.normalize(source)
	}
	return removed
}

func (r *CombatRuntime) clampUnitHP(source, target CombatUnitRef, key string, at time.Time) []CombatEvent {
	unit, ok := r.resolve(target)
	if !ok || !unit.alive() {
		return nil
	}
	maxHP := r.effectiveAttribute(target, CombatAttributeMaxHP)
	if *unit.hp <= maxHP {
		return nil
	}
	*unit.hp = maxHP
	clamped := r.event(at, CombatEventResourceClamped, source, target)
	clamped.Key, clamped.HPAfter, clamped.MaxHP, clamped.Attribute = key, *unit.hp, maxHP, CombatAttributeMaxHP
	return []CombatEvent{clamped}
}

func (r *CombatRuntime) clearUnitEffects(ref CombatUnitRef, reason string, at time.Time) []CombatEvent {
	ref = r.normalize(ref)
	var events []CombatEvent
	for _, effect := range append([]*activeCombatEffect(nil), r.state().effects...) {
		if effect.target == ref {
			events = append(events, r.removeEffect(effect, CombatEventEffectRemoved, reason, at)...)
		}
	}
	return events
}
