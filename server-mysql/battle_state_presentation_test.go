package main

import (
	"testing"

	"mhqserver/protocol"
)

func TestCombatStateOnlyTargetsClientUnitsWithBuffComponent(t *testing.T) {
	battle := &battleState{effectMeta: map[string]battleEffectMetadata{
		"modifier:42:test": {
			modifierID: 42,
			durationMS: 5000,
			iconID:     "bufficon_bleed",
			isBuff:     true,
		},
	}}
	eventTypes := []CombatEventType{
		CombatEventEffectApplied,
		CombatEventEffectRefreshed,
		CombatEventEffectStacked,
		CombatEventEffectRemoved,
		CombatEventEffectDispelled,
	}

	t.Run("monster state stays server side", func(t *testing.T) {
		conn := &recordingConn{}
		ss := newSession()
		ss.playerID = 100
		ch := &channel{conn: conn, session: ss}
		for _, eventType := range eventTypes {
			(&Server{}).emitCombatEvents(ch, battle, []CombatEvent{{
				Type:   eventType,
				Target: MonsterCombatUnit(200),
				Key:    "modifier:42:test",
			}})
		}
		for _, opcode := range recordedOpcodes(t, conn.Bytes()) {
			if opcode == protocol.OpM2C_BattleChangeState {
				t.Fatal("monster state emitted 20080 for a client unit without BuffComponent")
			}
		}
	})

	t.Run("player state remains visible", func(t *testing.T) {
		conn := &recordingConn{}
		ss := newSession()
		ss.playerID = 100
		ch := &channel{conn: conn, session: ss}
		(&Server{}).emitCombatEvents(ch, battle, []CombatEvent{{
			Type:   CombatEventEffectApplied,
			Target: PlayerCombatUnit(100),
			Key:    "modifier:42:test",
		}})
		opcodes := recordedOpcodes(t, conn.Bytes())
		if len(opcodes) != 1 || opcodes[0] != protocol.OpM2C_BattleChangeState {
			t.Fatalf("player state opcodes = %v, want [20080]", opcodes)
		}
	})
}
