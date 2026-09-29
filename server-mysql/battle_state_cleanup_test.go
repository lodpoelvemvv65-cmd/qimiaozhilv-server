package main

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

const combatStateShieldKey = "modifier:2109:shield"

func combatStateTestBattle(ss *session, key string, meta battleEffectMetadata) *battleState {
	return &battleState{
		owner:      ss,
		mapID:      ss.mapID,
		effectMeta: map[string]battleEffectMetadata{key: meta},
	}
}

func combatStateTestChannel(playerID int64, mapID int32) (*channel, *recordingConn) {
	ss := newSession()
	ss.state = sessInGame
	ss.playerID = playerID
	ss.mapID = mapID
	ss.x, ss.y = 1, 2
	ss.hp, ss.mp = 100, 50
	conn := &recordingConn{}
	return &channel{id: playerID, conn: conn, session: ss}, conn
}

func decodeCombatStates(t *testing.T, data []byte) []*protocol.M2C_BattleChangeState {
	t.Helper()
	var states []*protocol.M2C_BattleChangeState
	for _, frame := range decodeRecordedFrames(t, data) {
		if frame.opcode != protocol.OpM2C_BattleChangeState {
			continue
		}
		state := &protocol.M2C_BattleChangeState{}
		if err := proto.Unmarshal(frame.body, state); err != nil {
			t.Fatalf("decode 20080: %v", err)
		}
		states = append(states, state)
	}
	return states
}

// applyCombatState pushes one 20080 state the way emitCombatEvents does.
func applyCombatState(server *Server, ch *channel, battle *battleState, key string) {
	server.emitCombatEvents(ch, battle, []CombatEvent{{
		Type: CombatEventEffectApplied, Target: PlayerCombatUnit(ch.session.playerID), Key: key,
	}})
}

// The client keys 20080 states by Id, so a forced clear has to resend that exact
// Id with Time=0. A fixed Id or a fixed icon list cannot remove the state.
func TestCombatStateClearResendsTheAddedId(t *testing.T) {
	ch, conn := combatStateTestChannel(1001108, 1001108)
	server := &Server{}
	battle := combatStateTestBattle(ch.session, combatStateShieldKey, battleEffectMetadata{
		modifierID: 2109, durationMS: 8000, stateKey: 104, iconID: "bufficon_shield", isBuff: true,
	})

	applyCombatState(server, ch, battle, combatStateShieldKey)
	added := decodeCombatStates(t, conn.Bytes())
	if len(added) != 1 || added[0].Id < 1_000_000_000_000_000_000 || added[0].Id == 2109 || added[0].Type != protocol.ChangeType_Add {
		t.Fatalf("Add frame = %+v", added)
	}
	addedID := added[0].Id

	if cleared := server.clearPlayerBattleStates(ch); cleared != 1 {
		t.Fatalf("clearPlayerBattleStates = %d, want 1", cleared)
	}
	states := decodeCombatStates(t, conn.Bytes())
	if len(states) != 2 {
		t.Fatalf("20080 frames = %d, want the Add plus one clear", len(states))
	}
	clear := states[1]
	if clear.Id != addedID || clear.IconId != "bufficon_shield" || clear.TargetUnitId != ch.session.playerID {
		t.Fatalf("clear frame = %+v", clear)
	}
	if clear.Type != protocol.ChangeType_Reduce || clear.Time != 0 {
		t.Fatalf("clear frame must expire the same Id immediately: %+v", clear)
	}
	if cleared := server.clearPlayerBattleStates(ch); cleared != 0 {
		t.Fatalf("cleared states were resent %d times", cleared)
	}
}

// Modifiers without a configured ModifierID still receive an independent
// application ID, and the clear must replay that exact ID.
func TestCombatStateClearMintsIdWithoutModifierId(t *testing.T) {
	ch, conn := combatStateTestChannel(1001109, 1001101)
	server := &Server{}
	battle := combatStateTestBattle(ch.session, "modifier:77:dot", battleEffectMetadata{
		durationMS: 5000, stateKey: 101, iconID: "bufficon_bleed", isBuff: false,
	})

	applyCombatState(server, ch, battle, "modifier:77:dot")
	added := decodeCombatStates(t, conn.Bytes())
	if len(added) != 1 || added[0].Id < 1_000_000_000_000_000_000 || added[0].Id == 77 {
		t.Fatalf("Add frame = %+v", added)
	}
	if cleared := server.clearPlayerBattleStates(ch); cleared != 1 {
		t.Fatalf("clearPlayerBattleStates = %d, want 1", cleared)
	}
	clear := decodeCombatStates(t, conn.Bytes())[1]
	if clear.Id != added[0].Id || clear.IconId != "bufficon_bleed" || clear.IsBuff {
		t.Fatalf("clear frame = %+v", clear)
	}
}

// A map change with no combat behind it must stay silent. Replaying a fixed
// icon list here spammed every scene transition with states the client never had.
func TestMapChangeWithoutCombatStatesStaysSilent(t *testing.T) {
	ch, conn := combatStateTestChannel(1001110, 1001101)
	(&Server{}).changeMap(ch, 1001102, 3.5, -1.5)

	if states := decodeCombatStates(t, conn.Bytes()); len(states) != 0 {
		t.Fatalf("plain map change emitted %d combat states: %+v", len(states), states)
	}
}

func TestMapChangeClearsTrackedStatesBeforeChangeMap(t *testing.T) {
	ch, conn := combatStateTestChannel(1001111, 1001101)
	server := &Server{}
	battle := combatStateTestBattle(ch.session, combatStateShieldKey, battleEffectMetadata{
		modifierID: 2109, durationMS: 8000, stateKey: 104, iconID: "bufficon_shield", isBuff: true,
	})
	applyCombatState(server, ch, battle, combatStateShieldKey)
	addedID := decodeCombatStates(t, conn.Bytes())[0].Id

	server.changeMap(ch, 1001102, 3.5, -1.5)

	clearIndex, changeMapIndex := -1, -1
	for index, frame := range decodeRecordedFrames(t, conn.Bytes()) {
		if frame.opcode == protocol.OpM2C_ChangeMap {
			changeMapIndex = index
			continue
		}
		if frame.opcode != protocol.OpM2C_BattleChangeState {
			continue
		}
		state := &protocol.M2C_BattleChangeState{}
		if err := proto.Unmarshal(frame.body, state); err != nil {
			t.Fatal(err)
		}
		if state.Type == protocol.ChangeType_Reduce {
			if state.Id != addedID {
				t.Fatalf("clear frame Id = %d, want %d", state.Id, addedID)
			}
			clearIndex = index
		}
	}
	if clearIndex < 0 {
		t.Fatal("map change did not clear the tracked combat state")
	}
	if changeMapIndex < 0 {
		t.Fatal("map change did not emit ChangeMap")
	}
	if clearIndex > changeMapIndex {
		t.Fatalf("clear at %d came after ChangeMap at %d", clearIndex, changeMapIndex)
	}
	if cleared := server.clearPlayerBattleStates(ch); cleared != 0 {
		t.Fatalf("state survived the map change: %d left", cleared)
	}
}

func TestReduceForgetsTrackedCombatState(t *testing.T) {
	ch, conn := combatStateTestChannel(1001112, 1001104)
	server := &Server{}
	battle := combatStateTestBattle(ch.session, combatStateShieldKey, battleEffectMetadata{
		modifierID: 2109, durationMS: 8000, stateKey: 104, iconID: "bufficon_shield", isBuff: true,
	})

	applyCombatState(server, ch, battle, combatStateShieldKey)
	server.emitCombatEvents(ch, battle, []CombatEvent{{
		Type: CombatEventEffectRemoved, Target: PlayerCombatUnit(ch.session.playerID), Key: combatStateShieldKey,
	}})
	if cleared := server.clearPlayerBattleStates(ch); cleared != 0 {
		t.Fatalf("a reduced state was cleared again (%d)", cleared)
	}
	if states := decodeCombatStates(t, conn.Bytes()); len(states) != 2 {
		t.Fatalf("20080 frames = %d, want Add + Reduce", len(states))
	}
}

// The client expires its own timer from the Add packet, so no further packet is
// needed and the tracked entry must not force one later.
func TestExpiredCombatStateIsNotClearedAgain(t *testing.T) {
	ch, conn := combatStateTestChannel(1001113, 1001105)
	server := &Server{}
	battle := combatStateTestBattle(ch.session, combatStateShieldKey, battleEffectMetadata{
		modifierID: 2109, durationMS: 8000, stateKey: 104, iconID: "bufficon_shield", isBuff: true,
	})

	applyCombatState(server, ch, battle, combatStateShieldKey)
	server.emitCombatEvents(ch, battle, []CombatEvent{{
		Type: CombatEventEffectExpired, Target: PlayerCombatUnit(ch.session.playerID), Key: combatStateShieldKey,
	}})
	if cleared := server.clearPlayerBattleStates(ch); cleared != 0 {
		t.Fatalf("expired state was cleared again (%d)", cleared)
	}
	if states := decodeCombatStates(t, conn.Bytes()); len(states) != 1 {
		t.Fatalf("20080 frames = %d, want the Add alone", len(states))
	}
}

// A delayed projectile impact can apply a state after the fight was settled and
// the session battle detached. Clearing it immediately keeps the icon out of the
// field instead of leaving it until its configured duration elapses.
func TestLateCombatStateAfterSettlementClearsItself(t *testing.T) {
	ch, conn := combatStateTestChannel(1001114, 1001106)
	server := &Server{}
	battle := combatStateTestBattle(ch.session, combatStateShieldKey, battleEffectMetadata{
		modifierID: 2109, durationMS: 8000, stateKey: 104, iconID: "bufficon_shield", isBuff: true,
	})
	battle.ended = true

	applyCombatState(server, ch, battle, combatStateShieldKey)

	states := decodeCombatStates(t, conn.Bytes())
	if len(states) != 2 {
		t.Fatalf("20080 frames = %d, want the late Add plus its clear", len(states))
	}
	if states[0].Type != protocol.ChangeType_Add || states[1].Type != protocol.ChangeType_Reduce {
		t.Fatalf("frame types = %v then %v", states[0].Type, states[1].Type)
	}
	if states[1].Id != states[0].Id || states[1].Time != 0 {
		t.Fatalf("late clear = %+v, want Id %d with Time=0", states[1], states[0].Id)
	}
	if cleared := server.clearPlayerBattleStates(ch); cleared != 0 {
		t.Fatalf("late state was cleared twice (%d)", cleared)
	}
}

// Persistent item buffs share the Skill_fui icons but use the item id as their
// client key, so a combat clear must never replay an item buff id.
func TestCombatStateClearLeavesItemBuffsAlone(t *testing.T) {
	ch, conn := combatStateTestChannel(1001115, 1001107)
	ss := ch.session
	ss.ensureItemBuffs()
	ss.itemBuffMu.Lock()
	ss.itemBuffs[itemBuffBattleExp] = &activeItemBuff{
		ItemID: 110349, EffectType: 6, ExpiresAt: 9999999999999, Multiplier: 1.5,
	}
	ss.itemBuffMu.Unlock()

	server := &Server{}
	// The battle exp card and this modifier share bufficon_atkAdd.
	key := "modifier:2109:atk"
	battle := combatStateTestBattle(ss, key, battleEffectMetadata{
		modifierID: 2109, durationMS: 8000, stateKey: 102, iconID: "bufficon_atkAdd", isBuff: true,
	})
	applyCombatState(server, ch, battle, key)
	addedID := decodeCombatStates(t, conn.Bytes())[0].Id

	if cleared := server.clearPlayerBattleStates(ch); cleared != 1 {
		t.Fatalf("clearPlayerBattleStates = %d, want 1", cleared)
	}
	for _, state := range decodeCombatStates(t, conn.Bytes()) {
		if state.Type == protocol.ChangeType_Reduce && state.Id != addedID {
			t.Fatalf("clear did not pair the combat state instance: %+v", state)
		}
	}
	ss.itemBuffMu.Lock()
	buff := ss.itemBuffs[itemBuffBattleExp]
	ss.itemBuffMu.Unlock()
	if buff == nil || buff.ItemID != 110349 {
		t.Fatal("item buff was dropped by the combat state clear")
	}
}

// A party-wide state lands on several units under one Id. The clear has to
// replay every Add with its own TargetUnitId, otherwise the icons left on the
// other units survive the map change.
func TestCombatStateClearReplaysEveryTargetUnit(t *testing.T) {
	const teammate = int64(1001200)
	ch, conn := combatStateTestChannel(1001116, 1001108)
	server := &Server{}
	battle := combatStateTestBattle(ch.session, combatStateShieldKey, battleEffectMetadata{
		modifierID: 2109, durationMS: 8000, stateKey: 104, iconID: "bufficon_shield", isBuff: true,
	})
	server.emitCombatEvents(ch, battle, []CombatEvent{
		{Type: CombatEventEffectApplied, Target: PlayerCombatUnit(ch.session.playerID), Key: combatStateShieldKey},
		{Type: CombatEventEffectApplied, Target: PlayerCombatUnit(teammate), Key: combatStateShieldKey},
	})
	added := decodeCombatStates(t, conn.Bytes())
	if len(added) != 2 || added[0].Id == added[1].Id {
		t.Fatalf("Add frames = %+v, want one per unit", added)
	}
	addedByUnit := map[int64]int64{added[0].TargetUnitId: added[0].Id, added[1].TargetUnitId: added[1].Id}
	if cleared := server.clearPlayerBattleStates(ch); cleared != 2 {
		t.Fatalf("clearPlayerBattleStates = %d, want 2", cleared)
	}
	cleared := map[int64]int64{}
	for _, state := range decodeCombatStates(t, conn.Bytes()) {
		if state.Type != protocol.ChangeType_Reduce {
			continue
		}
		if state.Id != addedByUnit[state.TargetUnitId] || state.Time != 0 || state.IconId != "bufficon_shield" {
			t.Fatalf("clear frame = %+v", state)
		}
		cleared[state.TargetUnitId] = state.Id
	}
	if len(cleared) != 2 || cleared[ch.session.playerID] != addedByUnit[ch.session.playerID] || cleared[teammate] != addedByUnit[teammate] {
		t.Fatalf("cleared units = %v, want both units of the Add", cleared)
	}
}

func TestRepeatedCombatStateApplicationsUseUniqueIdsAndRemoveEveryInstance(t *testing.T) {
	ch, conn := combatStateTestChannel(1001117, 1001109)
	server := &Server{}
	battle := combatStateTestBattle(ch.session, combatStateShieldKey, battleEffectMetadata{
		modifierID: 2109, durationMS: 8000, stateKey: 104, iconID: "bufficon_shield", isBuff: true,
	})
	applyCombatState(server, ch, battle, combatStateShieldKey)
	server.emitCombatEvents(ch, battle, []CombatEvent{{
		Type: CombatEventEffectRefreshed, Target: PlayerCombatUnit(ch.session.playerID), Key: combatStateShieldKey,
	}})
	added := decodeCombatStates(t, conn.Bytes())
	if len(added) != 2 || added[0].Id == added[1].Id {
		t.Fatalf("repeated Add IDs = %+v, want two unique instances", added)
	}

	server.emitCombatEvents(ch, battle, []CombatEvent{{
		Type: CombatEventEffectDispelled, Target: PlayerCombatUnit(ch.session.playerID), Key: combatStateShieldKey,
	}})
	states := decodeCombatStates(t, conn.Bytes())
	if len(states) != 4 {
		t.Fatalf("20080 frames = %d, want two Add plus two Reduce", len(states))
	}
	want := map[int64]bool{added[0].Id: false, added[1].Id: false}
	for _, state := range states[2:] {
		if state.Type != protocol.ChangeType_Reduce || state.Time != 0 {
			t.Fatalf("unpaired removal = %+v", state)
		}
		if _, ok := want[state.Id]; !ok {
			t.Fatalf("removal used unknown Id %d", state.Id)
		}
		want[state.Id] = true
	}
	for id, seen := range want {
		if !seen {
			t.Fatalf("application Id %d was not removed", id)
		}
	}
}
