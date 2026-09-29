package main

import (
	"math"
	"time"
)

const (
	originalBattleOpeningDelay = 5 * time.Second
	monsterActionInterval      = 6 * time.Second
	maxPublicActionInterval    = 6 * time.Second
	minPublicActionInterval    = 4 * time.Second
	monsterSpeedPerLevel       = 0.045
)

// The original client capture starts the first action about five seconds after
// the battle presentation, then keeps all later actions on a six-second cycle.
//
// 这里**没有**"共享回合"：玩家的出手间隔只由玩家自己上一次成功出手的时刻决定，
// 与怪物那一侧无关（怪物有自己的 monsterReadyAt，见 claimMonsterActionWindow）。
// 手动与自动出手都使用玩家自己的 battleActionReadyAt；怪物恰好同拍时可合并
// 表现波次，但不会把玩家下一次出手重新对齐到怪物时钟。
func publicActionIntervalFromSpeed(speed float64) time.Duration {
	if math.IsNaN(speed) || math.IsInf(speed, 0) || speed < 0 {
		speed = 0
	}
	intervalMS := math.Round(float64(maxPublicActionInterval/time.Millisecond) - speed)
	minimumMS := float64(minPublicActionInterval / time.Millisecond)
	maximumMS := float64(maxPublicActionInterval / time.Millisecond)
	if intervalMS < minimumMS {
		intervalMS = minimumMS
	}
	if intervalMS > maximumMS {
		intervalMS = maximumMS
	}
	return time.Duration(intervalMS) * time.Millisecond
}

// The online StartCD samples fit 6000ms-Speed with a 4000ms floor:
// low-speed characters remain at 6000ms, while captured characters reached
// 4746ms, 4554ms and the 4000ms floor. NumericType 1031 already contains the
// complete CharacterGrowth, equipment, star-soul, rebirth and pet value.
func playerPublicActionInterval(ss *session) time.Duration {
	if ss == nil {
		return maxPublicActionInterval
	}
	return publicActionIntervalFromSpeed(ss.playerExtraNumeric(1031))
}

// Monster actions use the same 6000-Speed conversion as player actions, but
// their speed source is not a player NumericType. Captured ordinary monsters
// fit Level*0.045: level 10 rounds to the 6000ms ceiling and ten level-7600
// monsters sustained a 5657.732ms mean cadence (Speed=342 -> 5658ms).
// Family bosses use their separately captured 4.71s family-battle cadence,
// which also drives their four stacking attributes.
func monsterPublicActionInterval(battle *battleState) time.Duration {
	if battle == nil {
		return monsterActionInterval
	}
	if battle.mapID < 0 {
		return gameplayFamilyBossAttributeStackInterval()
	}
	if tables == nil || tables.monsterBase == nil {
		return monsterActionInterval
	}
	for _, monster := range battle.monsters {
		if monster == nil {
			continue
		}
		row := tables.monsterBase[int64(monster.monsterID)]
		if row == nil {
			continue
		}
		return publicActionIntervalFromSpeed(math.Round(numf(row["Level"]) * monsterSpeedPerLevel))
	}
	return monsterActionInterval
}

func nextPlayerActionReadyAt(ss *session, now time.Time) time.Time {
	return now.Add(playerPublicActionInterval(ss))
}

func initializeBattleOpeningCadence(ss *session, battle *battleState, startedAt time.Time) time.Time {
	readyAt := startedAt.Add(originalBattleOpeningDelay)
	if ss != nil {
		ss.battleActionReadyAt = readyAt
		ss.autoBattleNextCastAt = readyAt
	}
	if battle != nil {
		battle.monsterReadyAt = readyAt
	}
	return readyAt
}

// clientCombatCooldownMS keeps the server cooldown identical to SkillConfig.CD.
// NumericType 1031 is a speed point attribute, not a percentage multiplier for
// the client skill cooldown. Keeping this conversion explicit prevents a large
// agility-derived speed value from turning a 12-second skill into a few ms.
func clientCombatCooldownMS(baseMS int32) int32 {
	if baseMS <= 0 {
		return 0
	}
	return baseMS
}

func clientCombatInterval(base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	baseMS := base.Milliseconds()
	if baseMS > int64(1<<31-1) {
		baseMS = int64(1<<31 - 1)
	}
	return time.Duration(clientCombatCooldownMS(int32(baseMS))) * time.Millisecond
}

// StartCD.Time drives MainUISlotComponent.SetPublicCD and temporarily disables
// every other usable slot. StartCD.SkillCD independently carries the selected
// skill's own cooldown; online PlaySkill frames contain neither value.
func clientPublicActionIntervalMS(ss *session) int32 {
	return int32(playerPublicActionInterval(ss) / time.Millisecond)
}
