package main

import (
	"fmt"
	"log"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"mhqserver/protocol"
)

// boss.go：世界 BOSS 生命周期（10010 场景层 1..25，BossBase _id=1000+layer）。
//
// 线上刷新时间逐层读取 BossBase.RefreshInterval：5 分钟到 12 小时不等，
// 不能用统一的一小时常量；MonsterId=10255 篮球行者等。
//   - BOSS 存活：玩家进 10010 场景 → 生成 BOSS 字段怪（20320）+ 推 M2C_SendBossInfo
//     {BossId, UnitId} 注册 BOSS 单位（BOSS UI/血条/挑战入口）。
//   - BOSS 被击杀（字段怪战斗胜利结算）：全服广播
//       M2C_BossDead{UnitId}（各玩家移除自己场景中的 BOSS 单位）
//       M2C_BossBeDefeat（被击败播报）
//       世界聊天 M2C_SendNormalChat{Content:"玩家名 击败了 BOSS名！", Type=World,
//         IsSystemBrocast=true}（"谁击败了 BOSS"通报）
//     BOSS 标记死亡 → 字段怪消失、点击拒绝"BOSS 已阵亡，XX 后刷新"。
//   - 刷新：RefreshInterval 到点 → 全服广播 M2C_BossRefresh{BossId} → BOSS 复活。
//
// 死亡期间 BOSS 不出现：sceneFieldMonsters 对死亡层返回空（字段怪不生成），
// onClickMapUnit/startSceneBattleByMap 拒绝开战。
//
// 数据持久化（走数据库，重启不丢失）：world_boss_states 表存死亡时刻+击杀者；
// 内存 map 仅是热缓存，起服/首次访问时从 DB 恢复，到期复活时同步删 DB 记录。

type worldBossState struct {
	deadAt time.Time // 击杀时刻（零值 = 存活）
	killer string    // 击杀者名字
}

var (
	worldBossMu     sync.Mutex
	worldBossStates = map[int32]*worldBossState{} // bossID(1000+layer) → 状态（DB 持久化，重启保留）
)

// globalServer 由 main 注入（NewServer 后赋值），供全局状态存取 DB 使用。
var globalServer *Server

// bossLayerForMapID：10010 场景层号（1..25，BossBase 有行）；非 Boss 场景返回 0。
func bossLayerForMapID(mapID int32) int32 {
	if mapID/100 != 10010 {
		return 0
	}
	layer := mapID % 100
	if layer < 1 || layer > 25 {
		return 0
	}
	if _, ok := tables.bossBase[int64(1000+layer)]; !ok {
		return 0
	}
	return layer
}

// bossRefreshInterval：BOSS 刷新间隔（BossBase.RefreshInterval 毫秒；缺省 1 小时）。
func bossRefreshInterval(layer int32) time.Duration {
	if tables != nil {
		if row, ok := tables.bossBase[int64(1000+layer)]; ok {
			if ms := int64(num(row["RefreshInterval"])); ms > 0 {
				return time.Duration(ms) * time.Millisecond
			}
		}
	}
	return time.Hour
}

// bossDeadRemaining：BOSS 剩余刷新时长；存活（未击杀或已到期）返回 0。
// 到期时自动复活（删除内存状态 + 清理 DB 记录）。
func bossDeadRemaining(layer int32) time.Duration {
	worldBossMu.Lock()
	st := worldBossStates[layer]
	if st == nil || st.deadAt.IsZero() {
		// 从 DB 兜底恢复（重启后首次访问；DB 查询不取 worldBossMu，无死锁）
		if st = loadWorldBossStateFromDB(layer); st != nil {
			worldBossStates[layer] = st
		}
	}
	if st == nil || st.deadAt.IsZero() {
		worldBossMu.Unlock()
		return 0
	}
	remain := bossRefreshInterval(layer) - time.Since(st.deadAt)
	if remain <= 0 {
		// 到期复活：内存 + DB 一并清理
		delete(worldBossStates, layer)
		worldBossMu.Unlock()
		clearWorldBossStateDB(layer)
		return 0
	}
	worldBossMu.Unlock()
	return remain
}

// bossAlive：BOSS 是否存活（未击杀或已过刷新时间）。
func bossAlive(layer int32) bool { return bossDeadRemaining(layer) == 0 }

// bossName：BOSS 显示名（BossBase.Name）。
func bossName(layer int32) string {
	if tables != nil {
		if row, ok := tables.bossBase[int64(1000+layer)]; ok {
			if n := fmt.Sprintf("%v", row["Name"]); n != "" {
				return n
			}
		}
	}
	return fmt.Sprintf("BOSS %d", 1000+layer)
}

// markBossDead：击杀 BOSS → 记录死亡时刻 + 击杀者（内存 + DB 持久化，重启保留刷新倒计时）。
func markBossDead(layer int32, killer string) {
	worldBossMu.Lock()
	worldBossStates[layer] = &worldBossState{deadAt: time.Now(), killer: killer}
	worldBossMu.Unlock()
	if globalServer != nil && globalServer.store != nil {
		globalServer.store.db.Exec(`INSERT INTO world_boss_states (layer, dead_at, killer) VALUES (?, ?, ?)
			ON CONFLICT(layer) DO UPDATE SET dead_at=excluded.dead_at, killer=excluded.killer`,
			layer, time.Now().UnixMilli(), killer)
	}
	log.Printf("[BOSS] layer %d (%s) killed by %s, respawn in %s",
		layer, bossName(layer), killer, bossRefreshInterval(layer))
}

// loadWorldBossStateFromDB 从 DB 读世界 BOSS 死亡状态（重启恢复；无记录返回 nil）。
func loadWorldBossStateFromDB(layer int32) *worldBossState {
	if globalServer == nil || globalServer.store == nil {
		return nil
	}
	var deadAtMS int64
	var killer string
	err := globalServer.store.db.QueryRow(`SELECT dead_at, killer FROM world_boss_states WHERE layer = ?`, layer).
		Scan(&deadAtMS, &killer)
	if err != nil {
		return nil
	}
	if deadAtMS <= 0 {
		return nil
	}
	return &worldBossState{deadAt: time.UnixMilli(deadAtMS), killer: killer}
}

// clearWorldBossStateDB 清除 DB 中的 BOSS 死亡记录（到期复活时调用）。
func clearWorldBossStateDB(layer int32) {
	if globalServer == nil || globalServer.store == nil {
		return
	}
	globalServer.store.db.Exec(`DELETE FROM world_boss_states WHERE layer = ?`, layer)
}

// broadcastBossKill：全服广播 BOSS 被击杀（BossDead 移除单位 + BossBeDefeat + 世界聊天播报）。
// 调用方须持有发起者 battleMu（emitVictory 内）。
func (s *Server) broadcastBossKill(killerName string, layer int32) {
	name := bossName(layer)
	for _, ch2 := range s.onlineChannels() {
		if ch2 == nil || ch2.session == nil {
			continue
		}
		ss2 := ch2.session
		// BossDeadHandler ignores UnitId and removes BossComponent's native
		// presentation. Send it to every player currently on this boss layer.
		if ss2.mapID/100 == 10010 && ss2.mapID%100 == layer {
			s.sendPush(ch2, protocol.OpM2C_BossDead, &protocol.M2C_BossDead{
				UnitId:  ss2.fieldBossUnitID,
				ActorId: ss2.playerID,
			})
		}
		// BossBeDefeat：被击败播报（全服）。
		s.sendPush(ch2, protocol.OpM2C_BossBeDefeat, &protocol.M2C_BossBeDefeat{
			ActorId: ss2.playerID,
		})
		// 世界聊天通报"谁击败了 BOSS"。
		s.sendPush(ch2, protocol.OpM2C_SendNormalChat, &protocol.M2C_SendNormalChat{
			Content:         fmt.Sprintf("%s 击败了 %s！", killerName, name),
			Type:            protocol.ChatType_World,
			Name:            "系统",
			IsSystemBrocast: true,
			ActorId:         ss2.playerID,
		})
	}
	log.Printf("[BOSS] layer %d %s defeated by %s broadcast to %d players",
		layer, name, killerName, len(s.onlineChannels()))
}

// 20057 -> 20058: first half of the client's native world-boss click flow.
// The client obfuscates Key before sending 20059. The self-hosted server does
// not share the online xor secret, so the session-scoped pending flag is the
// authoritative replay guard while Key remains a non-zero challenge value.
func (s *Server) onStartBossFightRequest(ch *channel, req *protocol.C2M_StartBossFightRequest) proto.Message {
	resp := &protocol.M2C_StartBossFightRequest{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	ss := ch.session
	layer := bossLayerForMapID(ss.mapID)
	if layer == 0 {
		resp.Message = "当前不在BOSS场景"
		return resp
	}
	if remain := bossDeadRemaining(layer); remain > 0 {
		resp.Message = fmt.Sprintf("BOSS已阵亡，%s后刷新", formatDurationCN(remain))
		return resp
	}
	if sessionHasBattle(ch) {
		resp.Message = "已经在战斗中"
		return resp
	}
	if ss.energy < bossEnergyCost() {
		resp.Message = "BOSS体力不足"
		return resp
	}
	ss.bossChallengePending = true
	resp.Key = time.Now().UnixNano()
	if resp.Key == 0 {
		resp.Key = 1
	}
	return resp
}

// 20059 -> 20060: confirm the native challenge and start the dedicated boss
// presentation (20061). Monster identity and stats come exclusively from the
// current BossBase/MonsterBase rows.
func (s *Server) onStartBossFight(ch *channel, req *protocol.C2M_StartBossFight) proto.Message {
	resp := &protocol.M2C_StartBossFight{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	ss := ch.session
	if !ss.bossChallengePending {
		resp.Message = "BOSS挑战请求已失效"
		return resp
	}
	ss.bossChallengePending = false
	layer := bossLayerForMapID(ss.mapID)
	if layer == 0 {
		resp.Message = "当前不在BOSS场景"
		return resp
	}
	if remain := bossDeadRemaining(layer); remain > 0 {
		resp.Message = fmt.Sprintf("BOSS已阵亡，%s后刷新", formatDurationCN(remain))
		return resp
	}
	started, _ := s.startSceneBattleByMap(ch, ss.mapID)
	if !started {
		if sessionHasBattle(ch) {
			return resp
		}
		if ss.energy < bossEnergyCost() {
			resp.Message = "BOSS体力不足"
		} else {
			resp.Message = "BOSS战斗配置无效"
		}
	}
	return resp
}

// broadcastBossRefresh sends the native spawn message only to players already
// on the revived layer. The client handler creates the boss in its current
// ZoneScene without checking the map, so a global broadcast would create the
// boss in unrelated scenes such as the main city.
func (s *Server) broadcastBossRefresh(layer int32) int {
	bossID := int32(1000 + layer)
	sent := 0
	for _, ch := range s.onlineChannels() {
		if ch == nil || ch.session == nil || bossLayerForMapID(ch.session.mapID) != layer {
			continue
		}
		s.sendPush(ch, protocol.OpM2C_BossRefresh, &protocol.M2C_BossRefresh{
			BossId:  bossID,
			ActorId: ch.session.playerID,
		})
		sent++
	}
	return sent
}

// bossSweepLoop：定期检查所有死亡 BOSS 是否到期复活；到点只通知对应 BOSS 层。
func (s *Server) bossSweepLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if s.closed.Load() {
			return
		}
		worldBossMu.Lock()
		var revived []int32
		for layer, st := range worldBossStates {
			if st.deadAt.IsZero() {
				continue
			}
			if time.Since(st.deadAt) >= bossRefreshInterval(layer) {
				revived = append(revived, layer)
				delete(worldBossStates, layer)
			}
		}
		worldBossMu.Unlock()
		for _, layer := range revived {
			clearWorldBossStateDB(layer) // 复活 → DB 记录一并清除
			log.Printf("[BOSS] layer %d (%s) respawned", layer, bossName(layer))
			// BossRefreshHandler resolves BossBase by BossId, then resolves its
			// MonsterId and Prefab. The wire value is therefore BossBase._id,
			// not MonsterBase.MonsterId.
			s.broadcastBossRefresh(layer)
		}
	}
}
