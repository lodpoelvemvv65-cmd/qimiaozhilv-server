package main

import (
	"math"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

var familyBossOpeningScaledNumericTypes = [...]int32{
	1009, 1010, 1011, 1012,
	1013, 1014, 1015, 1016, 1017,
	1018, 1019, 1020, 1021,
}

var familyBossStackedAttributes = [...]CombatAttribute{
	CombatAttributePhysicalAttack,
	CombatAttributeSpiritualAttack,
	CombatAttributePhysicalDefense,
	CombatAttributeSpiritualDefense,
}

type familyBossCombatScaling struct {
	target      CombatUnitRef
	stepPercent float64
	interval    time.Duration
	nextTick    time.Time
	stacks      map[CombatAttribute]int
}

func applyFamilyBossOpeningMultiplier(units []*monsterUnit, presentation battlePresentation) bool {
	if presentation.kind != presentationFamilyBoss || presentation.familyBossID <= 0 || tables == nil {
		return false
	}
	row := tables.familyBossConfig[int64(presentation.familyBossID)]
	if row == nil {
		return false
	}
	multiplier, ok := familyBossAttributeMultiplier(row)
	if !ok {
		return false
	}
	monsterID := int32(num(row["MonsterId"]))
	applied := false
	for _, unit := range units {
		if unit == nil || unit.monsterID != monsterID {
			continue
		}
		unit.phyAtk = scaledFamilyBossInteger(unit.phyAtk, multiplier)
		unit.spiAtk = scaledFamilyBossInteger(unit.spiAtk, multiplier)
		unit.phyDef = scaledFamilyBossInteger(unit.phyDef, multiplier)
		unit.spiDef = scaledFamilyBossInteger(unit.spiDef, multiplier)
		if unit.extraNumeric == nil {
			unit.extraNumeric = make(map[int32]float64)
		}
		for _, numericType := range familyBossOpeningScaledNumericTypes {
			unit.extraNumeric[numericType] *= multiplier
		}
		applied = true
	}
	return applied
}

func familyBossAttributeMultiplier(row map[string]interface{}) (float64, bool) {
	if row == nil {
		return 0, false
	}
	characterAdd := numf(row["CharacterAdd"])
	multiplier := gameplayFamilyBossAttributeBaseMultiplier() + characterAdd
	if characterAdd < 0 || math.IsNaN(characterAdd) || math.IsInf(characterAdd, 0) ||
		multiplier <= 0 || math.IsNaN(multiplier) || math.IsInf(multiplier, 0) {
		return 0, false
	}
	return multiplier, true
}

func scaledFamilyBossInteger(value int32, multiplier float64) int32 {
	scaled := math.Round(float64(value) * multiplier)
	if scaled >= math.MaxInt32 {
		return math.MaxInt32
	}
	if scaled <= math.MinInt32 {
		return math.MinInt32
	}
	return int32(scaled)
}

func (r *CombatRuntime) initializeFamilyBossCombatScaling(presentation battlePresentation, startedAt time.Time) {
	if r == nil || presentation.kind != presentationFamilyBoss || presentation.familyBossID <= 0 ||
		tables == nil || len(r.battle.monsters) != 1 {
		return
	}
	row := tables.familyBossConfig[int64(presentation.familyBossID)]
	if _, ok := familyBossAttributeMultiplier(row); !ok {
		return
	}
	stepPercent := numf(row["CharacterAdd"]) * 100
	interval := gameplayFamilyBossAttributeStackInterval()
	if stepPercent <= 0 || interval <= 0 {
		return
	}
	boss := r.battle.monsters[0]
	r.familyBossScaling = &familyBossCombatScaling{
		target: MonsterCombatUnit(boss.id), stepPercent: stepPercent,
		interval: interval, nextTick: startedAt.Add(interval),
		stacks: make(map[CombatAttribute]int, len(familyBossStackedAttributes)),
	}
}

func familyBossNumericType(attribute CombatAttribute) int32 {
	switch attribute {
	case CombatAttributePhysicalAttack:
		return 1009
	case CombatAttributeSpiritualAttack:
		return 1010
	case CombatAttributePhysicalDefense:
		return 1011
	case CombatAttributeSpiritualDefense:
		return 1012
	default:
		return combatAttributeNumericType(attribute)
	}
}

func isFamilyBossStackedAttribute(attribute CombatAttribute) bool {
	for _, candidate := range familyBossStackedAttributes {
		if candidate == attribute {
			return true
		}
	}
	return false
}

func (r *CombatRuntime) familyBossAttributePercent(ref CombatUnitRef, attribute CombatAttribute) (float64, bool) {
	state := r.state()
	if state == nil || state.familyBossScaling == nil {
		return 0, false
	}
	scaling := state.familyBossScaling
	if r.normalize(ref) != scaling.target || !isFamilyBossStackedAttribute(attribute) {
		return 0, false
	}
	return scaling.stepPercent * float64(scaling.stacks[attribute]), true
}

func (r *CombatRuntime) familyBossAttributeValue(attribute CombatAttribute) float64 {
	state := r.state()
	if state == nil || state.familyBossScaling == nil {
		return 0
	}
	if numericType := familyBossNumericType(attribute); numericType >= 1013 && numericType <= 1021 {
		return r.EffectivePercentAttribute(state.familyBossScaling.target, attribute) / 100
	}
	return float64(r.EffectiveAttribute(state.familyBossScaling.target, attribute))
}

func (r *CombatRuntime) familyBossAttributeChangeEvents(at time.Time, attributes []CombatAttribute) []CombatEvent {
	state := r.state()
	if state == nil || state.familyBossScaling == nil {
		return nil
	}
	events := make([]CombatEvent, 0, len(attributes))
	for _, attribute := range attributes {
		event := r.event(at, CombatEventAttributeChanged, state.familyBossScaling.target, state.familyBossScaling.target)
		event.Attribute = attribute
		event.AttributeValue = r.familyBossAttributeValue(attribute)
		events = append(events, event)
	}
	return events
}

func (r *CombatRuntime) advanceFamilyBossScalingTo(now time.Time) []CombatEvent {
	state := r.state()
	if state == nil || state.familyBossScaling == nil || state.cancelled {
		return nil
	}
	scaling := state.familyBossScaling
	if scaling.stacks == nil {
		scaling.stacks = make(map[CombatAttribute]int, len(familyBossStackedAttributes))
	}
	var events []CombatEvent
	for !scaling.nextTick.After(now) {
		tickAt := scaling.nextTick
		scaling.nextTick = scaling.nextTick.Add(scaling.interval)
		for _, attribute := range familyBossStackedAttributes {
			scaling.stacks[attribute]++
		}
		events = append(events, r.familyBossAttributeChangeEvents(tickAt, familyBossStackedAttributes[:])...)
	}
	return events
}

func (r *CombatRuntime) resetFamilyBossScaling(attribute CombatAttribute, at time.Time) []CombatEvent {
	state := r.state()
	if state == nil || state.familyBossScaling == nil || !isFamilyBossStackedAttribute(attribute) {
		return nil
	}
	if state.familyBossScaling.stacks == nil {
		state.familyBossScaling.stacks = make(map[CombatAttribute]int, len(familyBossStackedAttributes))
	}
	state.familyBossScaling.stacks[attribute] = 0
	return r.familyBossAttributeChangeEvents(at, []CombatAttribute{attribute})
}

func (r *CombatRuntime) familyBossExternalAttributeEffect(target CombatUnitRef, kind CombatEffectKind, attribute CombatAttribute, percent float64) bool {
	if kind != CombatEffectAttribute || percent == 0 {
		return false
	}
	_, applies := r.familyBossAttributePercent(target, attribute)
	return applies
}

func (r *CombatRuntime) familyBossPersistentEffectEvents(context CombatEffectContext, spec EffectSpec, events []CombatEvent, at time.Time) []CombatEvent {
	if !r.familyBossExternalAttributeEffect(context.Target, spec.Kind, spec.Attribute, spec.Percent) {
		return events
	}
	for _, event := range events {
		switch event.Type {
		case CombatEventEffectApplied:
			return append(events, r.resetFamilyBossScaling(spec.Attribute, at)...)
		case CombatEventEffectStacked:
			if event.StackAdded {
				return append(events, r.resetFamilyBossScaling(spec.Attribute, at)...)
			}
		case CombatEventEffectRefreshed:
			return append(events, r.familyBossAttributeChangeEvents(at, []CombatAttribute{spec.Attribute})...)
		}
	}
	return events
}

func familyBossCombatAttributeForNumeric(numericType int32) CombatAttribute {
	switch numericType {
	case 1009:
		return CombatAttributePhysicalAttack
	case 1010:
		return CombatAttributeSpiritualAttack
	case 1011:
		return CombatAttributePhysicalDefense
	case 1012:
		return CombatAttributeSpiritualDefense
	case 1013:
		return CombatAttributePhysicalCritRate
	case 1014:
		return CombatAttributeSpiritualCritRate
	case 1015:
		return CombatAttributePhysicalCritValue
	case 1016:
		return CombatAttributeSpiritualCritValue
	case 1017:
		return CombatAttributeAuxiliary
	case 1018:
		return CombatAttributePhysicalAntiCritRate
	case 1019:
		return CombatAttributePhysicalAntiCritValue
	case 1020:
		return CombatAttributeSpiritualAntiCritRate
	case 1021:
		return CombatAttributeSpiritualAntiCritValue
	case 1022:
		return CombatAttributePhysicalDamageReduction
	case 1023:
		return CombatAttributeSpiritualDamageReduction
	default:
		return CombatAttributeNone
	}
}

func familyBossClientNumericValue(runtime *CombatRuntime, boss *monsterUnit, numericType int32) float32 {
	if runtime == nil || boss == nil {
		return 0
	}
	switch numericType {
	case 1001:
		return float32(boss.hp)
	case 1002:
		return float32(boss.maxHP)
	}
	attribute := familyBossCombatAttributeForNumeric(numericType)
	if attribute == CombatAttributeNone {
		return 0
	}
	target := MonsterCombatUnit(boss.id)
	if numericType >= 1013 {
		return float32(runtime.EffectivePercentAttribute(target, attribute) / 100)
	}
	return float32(runtime.EffectiveAttribute(target, attribute))
}

func (s *Server) pushFamilyBossOpeningAttributes(ch *channel, battle *battleState, boss *monsterUnit) {
	if ch == nil || ch.session == nil || battle == nil || battle.runtime == nil || boss == nil {
		return
	}
	attributes := make([]unitNumericAttribute, 0, 17)
	for numericType := int32(1001); numericType <= 1023; numericType++ {
		if numericType > 1002 && numericType < 1009 {
			continue
		}
		attributes = append(attributes, unitNumericAttribute{
			numericType: numericType,
			value:       familyBossClientNumericValue(battle.runtime, boss, numericType),
		})
	}
	s.pushFamilyBossAttributeList(ch, boss.id, attributes)
}

func (s *Server) pushFamilyBossAttributeChanges(ch *channel, events []CombatEvent) {
	if ch == nil || ch.session == nil || len(events) == 0 {
		return
	}
	attributes := make([]unitNumericAttribute, 0, len(events))
	for _, event := range events {
		numericType := familyBossNumericType(event.Attribute)
		if numericType == 0 {
			continue
		}
		attributes = append(attributes, unitNumericAttribute{numericType: numericType, value: float32(event.AttributeValue)})
	}
	if len(attributes) > 0 {
		s.pushFamilyBossAttributeList(ch, events[0].Target.ID, attributes)
	}
}

func (s *Server) pushFamilyBossAttributeList(ch *channel, unitID int64, attributes []unitNumericAttribute) {
	base, err := proto.Marshal(&protocol.M2C_SyncUnitAttributeList{
		UnitId: unitID, ActorId: ch.session.playerID,
	})
	if err != nil {
		return
	}
	body := appendAttributeMapList(base, 2, attributes)
	s.SendToChannel(ch, packOuter(protocol.OpM2C_SyncUnitAttributeList, body))
}
