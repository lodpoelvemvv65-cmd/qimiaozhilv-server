package main

import (
	"testing"
	"time"

	"mhqserver/protocol"
)

// 线上不发送同场景 ChangeMap 来清理战斗表现：连续特效（EffectType=4）由客户端
// 按特效自身的 Time 销毁（盾牌 EffectId=2109 为 10 秒）。战斗结束后服务端只保留
// 正常的一次换场，因此这里断言打完一场带连续特效的战斗后不再冒出第二个同场景
// ChangeMap。
func TestBattleExitDoesNotReloadSameSceneForContinuousVisual(t *testing.T) {
	loadOnlineTablesForTest(t)
	ss := newSession()
	ss.state, ss.playerID, ss.mapID = sessInGame, 7001, 10004
	ss.x, ss.y = 1.25, -2.5
	conn := &recordingConn{}
	ch := &channel{id: 7001, conn: conn, session: ss}
	battle := &battleState{owner: ss, mapID: ss.mapID}
	ss.battle = battle
	server := &Server{}

	server.emitCombatEvents(ch, battle, []CombatEvent{{
		Type: CombatEventVisual, Source: MonsterCombatUnit(9001),
		Target: PlayerCombatUnit(ss.playerID), EffectID: 3025,
	}})

	// 战斗结束：Cancel() 走的就是 battle-ended 移除，随后战斗解绑。
	server.emitCombatEvents(ch, battle, []CombatEvent{{
		Type: CombatEventEffectRemoved, Target: PlayerCombatUnit(ss.playerID),
		Key: "modifier:42:shield", Reason: "battle-ended",
	}})
	ss.battle = nil
	time.Sleep(200 * time.Millisecond)

	changes := 0
	for _, opcode := range recordedOpcodes(t, conn.Bytes()) {
		if opcode == protocol.OpM2C_ChangeMap {
			changes++
		}
	}
	if changes != 0 {
		t.Fatalf("battle exit emitted %d same-scene ChangeMap, want 0: %v",
			changes, recordedOpcodes(t, conn.Bytes()))
	}
}

// 战斗结束时 Cancel() 会移除全部状态，线上不发 Time=0，客户端按 Add 包里的
// Time 自行销毁；这里断言 battle-ended 的移除不会下发 20080。
func TestBattleEndedEffectRemovalDoesNotSendZeroTimeState(t *testing.T) {
	server := &Server{}
	conn := &recordingConn{}
	ss := newSession()
	ss.playerID = 42
	target := PlayerCombatUnit(ss.playerID)
	battle := &battleState{owner: ss, effectMeta: map[string]battleEffectMetadata{
		"modifier:42:shield": {modifierID: 42, stateKey: 2109, iconID: "2109", durationMS: 10000},
	}}
	server.emitCombatEvents(&channel{conn: conn, session: ss}, battle, []CombatEvent{{
		Type: CombatEventEffectRemoved, Target: target, Key: "modifier:42:shield", Reason: "battle-ended",
	}})
	if data := conn.Bytes(); len(data) != 0 {
		t.Fatalf("battle-ended forced a zero-time buff state: %x", data)
	}
}
