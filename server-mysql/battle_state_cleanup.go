package main

import (
	"sort"
	"sync/atomic"
	"time"

	"mhqserver/protocol"
)

// Combat HUD states travel in M2C_BattleChangeState (20080). The original client
// handler (ET.M2C_BattleChangeStateHandler) always feeds the message into
// BuffComponent.AddBuff, which stores one entry per Id:
//
//	IL_0012: ldfld Field::buffList
//	IL_0013: ldfld Field::Id
//	IL_0019: callvirt TS::set_Item
//
// IconId is display data only and is never compared, and the only removal path
// is ET.BuffComponent.Update, driven by a 500ms TimerComponent timer:
//
//	if ServerNow > info.leastTime { RemoveBuff(info.Id) }
//
// A forced clear therefore has to resend the exact Id the Add used with Time=0
// so leastTime falls into the past. Replaying fixed icon names, or using a
// sentinel Id, cannot remove an orphaned state: it only inserts a new
// zero-lifetime entry under a different key.

// combatIconKey identifies one entry of the client's BuffComponent dictionary.
// The client keys buffList by the message Id and one scene can carry the same
// Id on several units, so a faithful replay needs both parts.
type combatIconKey struct {
	unitID int64
	id     int64
}

// combatIconState is the client-visible state stored under one combatIconKey.
type combatIconState struct {
	effectKey string
	iconID    string
	iconDesc  string
	isBuff    bool
	expiresAt time.Time
}

var combatStateIDSequence atomic.Int64

// nextCombatStateID returns a process-wide, monotonic 19-digit instance ID.
// The online server mints a new snowflake-family ID for every 20080 Add; the
// client uses it as the BuffComponent dictionary key.
func nextCombatStateID() int64 {
	for {
		previous := combatStateIDSequence.Load()
		next := time.Now().UnixNano()
		if next <= previous {
			next = previous + 1
		}
		if combatStateIDSequence.CompareAndSwap(previous, next) {
			return next
		}
	}
}

func assignCombatStateIDs(events []CombatEvent) {
	for index := range events {
		switch events[index].Type {
		case CombatEventEffectApplied, CombatEventEffectRefreshed, CombatEventEffectStacked:
			if events[index].StateID == 0 {
				events[index].StateID = nextCombatStateID()
			}
		}
	}
}

// combatStatesSettled reports whether this battle already finished and the
// owning session detached it. Late impacts can still arrive afterwards.
func (battle *battleState) combatStatesSettled() bool {
	return battle != nil && battle.ended && battle.owner != nil && battle.owner.battle == nil
}

func (s *Server) trackCombatIcon(ch *channel, unitID, id int64, state combatIconState) {
	if ch == nil || ch.session == nil || unitID == 0 || state.iconID == "" {
		return
	}
	ss := ch.session
	ss.combatIconMu.Lock()
	defer ss.combatIconMu.Unlock()
	if ss.combatIcons == nil {
		ss.combatIcons = make(map[combatIconKey]combatIconState)
	}
	ss.combatIcons[combatIconKey{unitID: unitID, id: id}] = state
}

type trackedCombatIcon struct {
	key   combatIconKey
	state combatIconState
}

func (s *Server) takeCombatEffectIcons(ch *channel, unitID int64, effectKey string) []trackedCombatIcon {
	if ch == nil || ch.session == nil {
		return nil
	}
	ss := ch.session
	now := time.Now()
	ss.combatIconMu.Lock()
	defer ss.combatIconMu.Unlock()
	var found []trackedCombatIcon
	for key, state := range ss.combatIcons {
		if !state.expiresAt.IsZero() && !state.expiresAt.After(now) {
			delete(ss.combatIcons, key)
			continue
		}
		if key.unitID == unitID && state.effectKey == effectKey {
			found = append(found, trackedCombatIcon{key: key, state: state})
			delete(ss.combatIcons, key)
		}
	}
	sort.Slice(found, func(first, second int) bool { return found[first].key.id < found[second].key.id })
	return found
}

func (s *Server) forgetCombatEffectIcons(ch *channel, unitID int64, effectKey string) {
	_ = s.takeCombatEffectIcons(ch, unitID, effectKey)
}

// clearPlayerBattleStates forces the client's combat HUD back to empty by
// resending every state this session still owns with Time=0, and returns how
// many states were cleared. It is called before ChangeMap so icons left behind
// by a detached battle (a delayed projectile impact that applied a state after
// settlement) cannot survive into the next scene.
func (s *Server) clearPlayerBattleStates(ch *channel) int {
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		return 0
	}
	ss := ch.session
	ss.combatIconMu.Lock()
	pending := make(map[combatIconKey]combatIconState, len(ss.combatIcons))
	now := time.Now()
	for key, state := range ss.combatIcons {
		if state.expiresAt.IsZero() || state.expiresAt.After(now) {
			pending[key] = state
		}
	}
	ss.combatIcons = nil
	ss.combatIconMu.Unlock()
	for key, state := range pending {
		s.sendPush(ch, protocol.OpM2C_BattleChangeState, &protocol.M2C_BattleChangeState{
			Id:           key.id,
			TargetUnitId: key.unitID,
			IconId:       state.iconID,
			IconDesc:     state.iconDesc,
			Type:         protocol.ChangeType_Reduce,
			Time:         0,
			IsBuff:       state.isBuff,
			ActorId:      ss.playerID,
		})
	}
	return len(pending)
}
