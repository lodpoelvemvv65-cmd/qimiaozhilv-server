package main

import (
	"fmt"
	"log"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

func sessionCombatMaxHP(ss *session) int32 {
	if ss == nil {
		return 0
	}
	if ss.battle != nil && ss.battle.playerMaxHP > 0 {
		return ss.battle.playerMaxHP
	}
	return ss.playerMaxHp()
}

func logCombatHP(ss *session, action, extra string) {
	if ss == nil {
		return
	}
	log.Printf("[combat.hp] action=%s uid=%d raw=%d current=%d max=%d map=%d in_battle=%t %s",
		action, ss.playerID, ss.hp, ss.battleHP(), sessionCombatMaxHP(ss), ss.mapID, ss.battle != nil, strings.TrimSpace(extra))
}

func shouldLogBattleHPWrite(requested, before, after, maxHP int32) bool {
	if after == before && requested >= 0 {
		return false
	}
	if requested < 0 || after == 0 || before == 0 || after < before {
		return true
	}
	jump := after - before
	if jump >= 100000 {
		return true
	}
	return maxHP >= 1000 && jump >= maxHP/100
}

func combatHPCaller() string {
	_, file, line, ok := runtime.Caller(3)
	if !ok {
		return ""
	}
	file = strings.ReplaceAll(file, `\`, "/")
	if index := strings.LastIndex(file, "/"); index >= 0 {
		file = file[index+1:]
	}
	return file + ":" + strconv.Itoa(line)
}

func logCombatHPWrite(ss *session, requested, before, after int32) {
	if ss == nil || !shouldLogBattleHPWrite(requested, before, after, sessionCombatMaxHP(ss)) {
		return
	}
	log.Printf("[combat.hp] action=write uid=%d requested=%d before=%d after=%d raw=%d max=%d in_battle=%t caller=%s",
		ss.playerID, requested, before, after, ss.hp, sessionCombatMaxHP(ss), ss.battle != nil, combatHPCaller())
}

func combatMonsterSummary(units []*monsterUnit) string {
	if len(units) == 0 {
		return "monsters=0"
	}
	type group struct {
		id  int32
		atk int32
		n   int
	}
	index := map[int32]int{}
	groups := make([]group, 0, len(units))
	for _, unit := range units {
		if unit == nil {
			continue
		}
		if pos, ok := index[unit.monsterID]; ok {
			groups[pos].n++
			continue
		}
		index[unit.monsterID] = len(groups)
		groups = append(groups, group{id: unit.monsterID, atk: unit.phyAtk, n: 1})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].id < groups[j].id })
	parts := make([]string, 0, len(groups))
	for _, item := range groups {
		parts = append(parts, fmt.Sprintf("%dx%d atk=%d", item.id, item.n, item.atk))
	}
	return "monsters=" + strings.Join(parts, ",")
}

func combatDamageLogIDs(runtime *CombatRuntime, source, target CombatUnitRef) (int64, int32) {
	uid := int64(0)
	if target.Side == CombatSidePlayer {
		uid = target.ID
	} else if runtime != nil {
		uid = runtime.playerID
	}
	if runtime == nil || runtime.battle == nil || source.Side != CombatSideMonster {
		return uid, 0
	}
	for _, unit := range runtime.battle.monsters {
		if unit != nil && unit.id == source.ID {
			return uid, unit.monsterID
		}
	}
	return uid, 0
}
