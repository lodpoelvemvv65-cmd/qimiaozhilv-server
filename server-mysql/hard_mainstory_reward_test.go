package main

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"mhqserver/internal/operationsconfig"
	"mhqserver/protocol"
)

func TestEveryHardMainStoryNonBossLayerUsesOnlineNormalReward(t *testing.T) {
	loadOnlineTablesForTest(t)
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.HardMainStory.NormalRewardMultiplier = 3
	})
	checked := 0
	for region, row := range tables.mainStory {
		sceneID, layer := int32(num(row["SceneId"])), int32(num(row["Layer"]))
		if sceneID < 10052 || sceneID > 10067 || layer < 1 || layer > 9 {
			continue
		}
		ids, counts := rosterFromMainStoryConfig(row)
		units := newSession().buildMonsterUnitsFromRoster(ids, counts)
		for _, unit := range units {
			unit.alive = false
			unit.hp = 0
		}
		reward := battleVictoryDrops(&battleState{region: int32(region), monsters: units})
		var expectedExp int64
		for index, id := range ids {
			normal := tables.monsterBase[int64(id-hardMainStoryMonsterOffset)]
			if normal == nil || num(normal["Exp"]) <= 0 {
				t.Fatalf("hard region %d monster %d has no online normal counterpart reward", region, id)
			}
			expectedExp += num(normal["Exp"]) * int64(counts[index]) * 3
		}
		if reward.exp != expectedExp || reward.exp <= 0 {
			t.Fatalf("hard region %d reward exp=%d, want triple online counterpart %d", region, reward.exp, expectedExp)
		}
		checked++
	}
	if checked != 16*9 {
		t.Fatalf("checked hard non-boss layers=%d, want 144", checked)
	}
}

func TestHardMainStoryFallbackRollsOnlineDropChainThreeTimes(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.HardMainStory.NormalRewardMultiplier = 3
	})
	previous := tables
	tables = &datatables{
		mainStory: map[int64]map[string]interface{}{
			1: {"SceneId": int64(10052), "Layer": int64(1)},
		},
		monsterBase: map[int64]map[string]interface{}{
			1001: {"Exp": int64(100), "Dropasubset": int64(1)},
			1270: {"Exp": int64(0), "Dropasubset": int64(0)},
		},
		parentset: map[int64]map[string]interface{}{
			1: {"SubsetArr": []interface{}{map[string]interface{}{"_Id": int64(11)}}},
		},
		sonSet: map[int64]map[string]interface{}{
			11: {"DropArr": []interface{}{map[string]interface{}{
				"_Id": int64(20001), "Weight": int64(1), "MinCount": int64(2), "MaxCount": int64(2),
			}}},
		},
	}
	t.Cleanup(func() { tables = previous })

	reward := battleVictoryDrops(&battleState{region: 1, monsters: []*monsterUnit{{
		monsterID: 1270, hp: 0, alive: false,
	}}})
	if reward.exp != 300 || reward.items[20001] != 6 {
		t.Fatalf("triple hard main-story reward exp=%d items=%v", reward.exp, reward.items)
	}
}

func TestHardMainStoryVictorySendsNonEmptyNativeReward(t *testing.T) {
	loadOnlineTablesForTest(t)
	server, ss, ch := activityTestChannel(t, 7010)
	ss.mapID = 1005201
	row := tables.mainStory[1171]
	ids, counts := rosterFromMainStoryConfig(row)
	units := ss.buildMonsterUnitsFromRoster(ids, counts)
	for _, unit := range units {
		unit.alive = false
		unit.hp = 0
	}
	battle := &battleState{
		region: 1171, mapID: ss.mapID, copyID: 10001, monsters: units,
		playerHP: ss.playerMaxHp(), playerMaxHP: ss.playerMaxHp(),
		playerMP: ss.playerMaxMp(), playerMaxMP: ss.playerMaxMp(),
	}
	ss.battle = battle
	server.emitVictory(ch, battle)

	for _, frame := range decodeRecordedFrames(t, ch.conn.(*recordingConn).Bytes()) {
		if frame.opcode != protocol.OpM2C_SendReward {
			continue
		}
		var reward protocol.M2C_SendReward
		if err := proto.Unmarshal(frame.body, &reward); err != nil {
			t.Fatal(err)
		}
		if reward.Exp <= 0 {
			t.Fatalf("hard main-story native reward is empty: %+v", &reward)
		}
		return
	}
	t.Fatal("hard main-story victory did not send M2C_SendReward")
}
