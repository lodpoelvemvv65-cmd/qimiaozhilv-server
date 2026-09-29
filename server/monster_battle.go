package main

type monsterDamageMode uint8

const (
	monsterDamageAttack monsterDamageMode = iota
	monsterDamageTargetMaxHP
	monsterDamageNone
)

type monsterSkillSpec struct {
	mode      monsterDamageMode
	percent   int32
	spiritual bool
}

var monsterSkillSpecs = map[int32]monsterSkillSpec{
	// Values come from the damage options in the extracted SkillLogicConfig.
	// SkillConfig.Desc is not authoritative (for example 500001 and 500003 differ).
	500001: {mode: monsterDamageAttack, percent: 100},
	500002: {mode: monsterDamageAttack, percent: 120},
	500003: {mode: monsterDamageAttack, percent: 200},
	500004: {mode: monsterDamageAttack, percent: 300},
	500005: {mode: monsterDamageAttack, percent: 130},
	500006: {mode: monsterDamageAttack, percent: 130},
	500007: {mode: monsterDamageAttack, percent: 255},
	500008: {mode: monsterDamageAttack, percent: 130},
	500009: {mode: monsterDamageNone}, // burn only; periodic state is not implemented
	500010: {mode: monsterDamageAttack, percent: 30},
	500011: {mode: monsterDamageAttack, percent: 130},
	500012: {mode: monsterDamageTargetMaxHP, percent: 20},
	500013: {mode: monsterDamageAttack, percent: 130},
	500014: {mode: monsterDamageAttack, percent: 400},
	500015: {mode: monsterDamageAttack, percent: 100, spiritual: true},
	500016: {mode: monsterDamageAttack, percent: 120, spiritual: true},
	500017: {mode: monsterDamageAttack, percent: 200, spiritual: true},
	500018: {mode: monsterDamageAttack, percent: 300, spiritual: true},
	500019: {mode: monsterDamageAttack, percent: 130, spiritual: true},
	500020: {mode: monsterDamageAttack, percent: 130, spiritual: true},
	500021: {mode: monsterDamageAttack, percent: 255, spiritual: true},
	500022: {mode: monsterDamageAttack, percent: 130, spiritual: true},
	500023: {mode: monsterDamageNone}, // burn only; periodic state is not implemented
	500024: {mode: monsterDamageAttack, percent: 30, spiritual: true},
	500025: {mode: monsterDamageAttack, percent: 130, spiritual: true},
	500026: {mode: monsterDamageTargetMaxHP, percent: 20},
	500027: {mode: monsterDamageAttack, percent: 130, spiritual: true},
	500028: {mode: monsterDamageAttack, percent: 400, spiritual: true},
	500029: {mode: monsterDamageAttack, percent: 300},
	500030: {mode: monsterDamageAttack, percent: 300, spiritual: true},
	500031: {mode: monsterDamageNone},
	500032: {mode: monsterDamageNone},
	500033: {mode: monsterDamageNone},
	500034: {mode: monsterDamageNone},
	500035: {mode: monsterDamageNone},
	500036: {mode: monsterDamageNone},
	500037: {mode: monsterDamageNone},
	500038: {mode: monsterDamageNone},
	500039: {mode: monsterDamageAttack, percent: 130},
	500040: {mode: monsterDamageAttack, percent: 130},
	500041: {mode: monsterDamageAttack, percent: 130},
	500042: {mode: monsterDamageNone},
}

func monsterSkillSpecFor(skillID int32) monsterSkillSpec {
	if spec, ok := monsterSkillSpecs[skillID]; ok {
		return spec
	}
	// TODO: Add the remaining audited monster skills as their mechanics are verified.
	return monsterSkillSpec{mode: monsterDamageAttack, percent: 100}
}

func (b *battleState) monsterDamage(m *monsterUnit, skillID int32) int32 {
	if b == nil || m == nil {
		return 0
	}
	spec := monsterSkillSpecFor(skillID)
	switch spec.mode {
	case monsterDamageNone:
		return 0
	case monsterDamageTargetMaxHP:
		return positivePercent(b.playerMaxHP, spec.percent)
	default:
		attack, defense := m.phyAtk, b.phyDef
		if spec.spiritual {
			attack, defense = m.spiAtk, b.spiDef
		}
		base := attack - defense
		if base < 1 {
			base = 1
		}
		return positivePercent(base, spec.percent)
	}
}

func positivePercent(value, percent int32) int32 {
	if value <= 0 || percent <= 0 {
		return 0
	}
	damage := int64(value) * int64(percent) / 100
	if damage < 1 {
		return 1
	}
	const maxInt32 = int64(^uint32(0) >> 1)
	if damage > maxInt32 {
		return int32(maxInt32)
	}
	return int32(damage)
}
