package main

import (
	"fmt"
	"log"
	"sort"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// mapmonster.go：地图字段怪（野怪）—— 按场景类型推怪 + 点击开对应战斗。
//
// 协议链路（已通过反汇编客户端 M2C_CreateMapMonsterHandler 确认）：
//   服务器推 M2C_CreateMapMonster(20320) → 客户端创建怪物实体并发 C2M_ClickMapUnit(20391) RPC
//   → 服务器回 M2C_ClickMapUnit(20392)（Message 空 = 成功创建 + 进入战斗）。
//
// 字段怪 Id 从 20 亿起，避免与玩家 id / 战斗单位 id 冲突。
// 场景怪来源（子代理 5d24aa57 结论 + 本文件扩展）：
//   - MainStory.json（(SceneId, Layer) → Monster_NArr）：10006 海滩、10011-10026 主线、
//     10052-10067 困难主线
//   - TrialCopy.json（MapId → MonsterId/MonsterCount/Pos）：10009 挑战、10036/10037/10038
//     挑战·中级/困难/个人Boss
//   - BossBase.json（_id=1000+layer → MonsterId）：10010 Boss 层 1..25
//   - ManulEquipMonsterConfig.json：10033/10034/10035 手动装备（难度 1/2/3）
//   - SpaceTravelConfig.json（MonsterIdArr 首位=Boss）：10039-10044 星空旅行
//   10005/10007/10008/10030-32（广场/殿堂/公园/PvP）线上无怪，不推。
//
// ConfigId 必须是 MapMonsterConfig.json 里的有效行：客户端
// M2C_CreateMapMonsterHandler 用 ConfigId 查 MapMonsterConfigCategory.Get(configId)
// 取 PrefabId/Level/Desc 建怪，推 MonsterBase id（10001 等）会查不到 → 建怪失败。
// 海滩初始怪使用正式行 10281（急速荷叶球，PrefabId=281），不再补造 1017。
//
// MapMonsterType：客户端 M2C_CreateMapMonsterHandler 按类型 switch——MainStory(0)/
// Trial(1)/WorldBoss(6) 只挂血条不注册点击；SpaceTravel(4)/ManulEquip(2)/MapCoin(3)/
// StarSoul(5)/DeathCopy(7) 才注册 AddClickAction。因此字段怪推 4(SpaceTravel)。

var fieldMonsterSeq int64 = 2000000000

// fieldMonsterMapType：字段怪的 MapMonsterType（客户端 ET.MapMonsterType 枚举，
// 4=SpaceTravel 才会注册点击动作）。
const (
	mapMonsterTypeManualEquip = int32(2)
	mapMonsterTypeMapCoin     = int32(3)
	mapMonsterTypeSpaceTravel = int32(4)
	mapMonsterTypeStarSoul    = int32(5)
	mapMonsterTypeDeathCopy   = int32(7)
)

const (
	defaultFieldMonsterConfigID = int32(1011)
	mapCoinMonsterConfigID      = int32(1005)
)

// defaultFieldPos 无出生点配置的场景字段怪站位（出生点周围）。
var defaultFieldPos = [][2]float32{{-1.8, -0.84}, {3.0, -0.84}}

func activityFieldMonster(activity *activityBattle) (configID, mapType int32, x, y float32, ok bool) {
	if activity == nil {
		return 0, 0, 0, 0, false
	}
	switch activity.Method {
	case activeWorldBoss:
		// The stock client has no dedicated clickable MapMonsterConfig row for
		// the activity boss. Its hidden-boss display row has the required model;
		// SpaceTravel is the native clickable branch and combat still resolves
		// the authoritative WorldBossConfig roster after the click.
		configID, mapType = defaultFieldMonsterConfigID, mapMonsterTypeSpaceTravel
	case activeStarSoul:
		if activity.Stage < 1 || activity.Stage > 3 {
			return 0, 0, 0, 0, false
		}
		configID, mapType = 1011+activity.Stage, mapMonsterTypeStarSoul
	case activeJourneyOfDeath:
		if activity.Stage < 1 || activity.Stage > 2 {
			return 0, 0, 0, 0, false
		}
		configID, mapType = 1014+activity.Stage, mapMonsterTypeDeathCopy
	default:
		return 0, 0, 0, 0, false
	}
	if tables == nil || tables.mapMonsterConfig[int64(configID)] == nil {
		return 0, 0, 0, 0, false
	}
	row := tables.mapMonsterConfig[int64(configID)]
	return configID, mapType, float32(numf(row["X"])), float32(numf(row["Y"])), true
}

func (s *Server) pushActivityFieldMonster(ch *channel, state *activitySceneState) bool {
	if ch == nil || ch.session == nil || state == nil || ch.session.currentActivityScene() != state ||
		ch.session.mapID != state.targetMap {
		return false
	}
	return s.pushActivityFieldMonsterFor(ch, state.activity)
}

func (s *Server) pushActivityFieldMonsterFor(ch *channel, activity *activityBattle) bool {
	if ch == nil || ch.session == nil || len(ch.session.fieldMonsterIDs) != 0 {
		return false
	}
	configID, mapType, x, y, ok := activityFieldMonster(activity)
	if !ok {
		return false
	}
	fieldMonsterSeq++
	unitID := fieldMonsterSeq
	ch.session.fieldMonsterIDs = append(ch.session.fieldMonsterIDs, unitID)
	if ch.session.fieldMonsterConfigIDs == nil {
		ch.session.fieldMonsterConfigIDs = make(map[int64]int32)
	}
	ch.session.fieldMonsterConfigIDs[unitID] = configID
	s.sendPush(ch, protocol.OpM2C_CreateMapMonster, &protocol.M2C_CreateMapMonster{
		Id: unitID, X: x, Y: y, ConfigId: configID, MapMonsterType: mapType,
		ActorId: ch.session.playerID,
	})
	return true
}

// mainStoryRosterForMap 按 mapID（sceneId*100+layer）查 MainStory 行；
// 返回 (region, monsterIDs, counts, 出生点, ok)。遍历匹配 (SceneId, Layer)。
func mainStoryRosterForMap(mapID int32) (region int32, ids []int32, counts []int, pos [][2]float32, ok bool) {
	if tables == nil {
		return 0, nil, nil, nil, false
	}
	sceneID := mapID / 100
	layer := mapID % 100
	for rid, row := range tables.mainStory {
		if int32(num(row["SceneId"])) == sceneID && int32(num(row["Layer"])) == layer {
			region = int32(rid)
			for gi := 1; gi <= 6; gi++ {
				arrKey := fmt.Sprintf("Monster_%dArr", gi)
				idKey := fmt.Sprintf("Monster_%d_Id", gi)
				cntKey := fmt.Sprintf("Monster_%d_Count", gi)
				for _, e := range arrOf(row[arrKey]) {
					eo, _ := e.(map[string]interface{})
					if eo == nil {
						continue
					}
					if mid := int32(num(eo[idKey])); mid > 0 {
						ids = append(ids, mid)
						counts = append(counts, int(num(eo[cntKey])))
					}
				}
			}
			for _, e := range arrOf(row["MonsterPosArr"]) {
				eo, _ := e.(map[string]interface{})
				if eo == nil {
					continue
				}
				pos = append(pos, [2]float32{float32(numf(eo["MonsterPos_X"])), float32(numf(eo["MonsterPos_Y"]))})
			}
			sort.Slice(pos, func(i, j int) bool { return pos[i][0] < pos[j][0] })
			return region, ids, counts, pos, true
		}
	}
	return 0, nil, nil, nil, false
}

// sceneRosterForMap 按 mapID 返回场景战斗怪物列表（非 MainStory 场景）：
// (monsterIDs, counts, 出生点, ok)。
func sceneRosterForMap(mapID int32) ([]int32, []int, [][2]float32, bool) {
	if tables == nil {
		return nil, nil, nil, false
	}
	sceneID := mapID / 100
	layer := mapID % 100

	// 挑战副本（TrialCopy：MapId 逐层，如 1000901/1000902…；_id 与层号无关）
	for _, row := range tables.trialCopy {
		if int32(num(row["MapId"])) != mapID {
			continue
		}
		mid := int32(num(row["MonsterId"]))
		cnt := int(num(row["MonsterCount"]))
		if mid <= 0 {
			return nil, nil, nil, false
		}
		if cnt <= 0 {
			cnt = 1
		}
		pos := [][2]float32{{float32(numf(row["Pos_X"])), float32(numf(row["Pos_Y"]))}}
		return []int32{mid}, []int{cnt}, pos, true
	}

	// Boss 场景（10010 层 1..25 → BossBase _id=1000+layer）
	// ⚠ BOSS 死亡（刷新时间内）→ 字段怪不生成（"BOSS 在刷新时间段内不会出现"）。
	if sceneID == 10010 && layer >= 1 && layer <= 25 {
		if row, ok := tables.bossBase[int64(1000+layer)]; ok {
			if !bossAlive(layer) {
				log.Printf("[BOSS] layer %d dead, field monster hidden", layer)
				return nil, nil, nil, false
			}
			if mid := int32(num(row["MonsterId"])); mid > 0 {
				return []int32{mid}, []int{1}, defaultFieldPos, true
			}
		}
		return nil, nil, nil, false
	}

	// 手工副本每层对应一种元素：1火、2风、3土、4暗。展示怪和
	// ManulEquipMonsterConfig 领主必须使用同一层号，不能在每层同时刷四种。
	if tier, manualLayer, manual := manualEquipMapInfo(mapID); manual {
		configID := int64(tier*1000 + manualLayer*100 + 1)
		if row := tables.manulEquipMonsterConfig[configID]; row != nil {
			if mid := int32(num(row["MonsterId"])); mid > 0 {
				return []int32{mid}, []int{1}, defaultFieldPos, true
			}
		}
		return nil, nil, nil, false
	}

	// 星空旅行（10039-10044 → SpaceTravelConfig 难度1 行 _id=1001+sceneIdx，MonsterIdArr）
	if sceneID >= 10039 && sceneID <= 10044 {
		sceneIdx := sceneID - 10039 // 0..5
		if row, ok := tables.spaceTravelConfig[int64(1001+sceneIdx)]; ok {
			var ids []int32
			for _, e := range arrOf(row["MonsterIdArr"]) {
				if mid := int32(num(e)); mid > 0 {
					ids = append(ids, mid)
				}
			}
			if len(ids) > 0 {
				// 首位 Boss + 其后小兵：字段怪取前 2，战斗取全部（counts 各 1）
				return ids, nil, defaultFieldPos, true
			}
		}
		return nil, nil, nil, false
	}

	return nil, nil, nil, false
}

// configIDForMonsterID：怪物 MonsterId → 客户端 MapMonsterConfig ConfigId。
// 客户端 M2C_CreateMapMonster(20320) 按 ConfigId 查 MapMonsterConfigCategory 取
// PrefabId 建模型（子代理 ce38dc7d 反汇编确认）；战斗怪模型 = MonsterBase.PrefabId。
// 因此字段怪要显示与战斗怪一致的线上模型：ConfigId = 10000 + PrefabId
// （客户端 MapMonsterConfig 已包含 10000+PrefabId 对应行）。
func configIDForMonsterID(mid int32) int32 {
	if tables == nil || mid <= 0 {
		return defaultFieldMonsterConfigID
	}
	if mb, ok := tables.monsterBase[int64(mid)]; ok {
		if pid := int32(num(mb["PrefabId"])); pid > 0 {
			for id, cfg := range tables.mapMonsterConfig {
				if int32(num(cfg["PrefabId"])) == pid {
					return int32(id)
				}
			}
		}
	}
	return defaultFieldMonsterConfigID
}

// fieldMonsterConfigID 按场景战斗怪的首个 MonsterId 决定字段怪造型：
// 字段怪与战斗怪共用同一 PrefabId → 场景里看到的怪 = 进战斗后的怪，模型一致。
func fieldMonsterConfigID(mapID int32) int32 {
	if _, ids, _, _, ok := mainStoryRosterForMap(mapID); ok && len(ids) > 0 {
		return configIDForMonsterID(ids[0])
	}
	if ids, _, _, ok := sceneRosterForMap(mapID); ok && len(ids) > 0 {
		return configIDForMonsterID(ids[0])
	}
	return defaultFieldMonsterConfigID
}

// sceneTransPos 返回场景传送点光圈位置（SceneTransConfig.TransPosArr）。
// 字段怪站位 = 光圈旁边（portalOffsetPos 偏移），不是光圈正中央：
// 站在光圈中央 → 玩家点怪误触光圈传送（用户实测"点击打怪不注意就跑回城"）。
func sceneTransPos(sceneID int32) [][2]float32 {
	if tables == nil {
		return nil
	}
	row, ok := tables.sceneTrans[int64(sceneID)]
	if !ok {
		return nil
	}
	var out [][2]float32
	for _, e := range arrOf(row["TransPosArr"]) {
		eo, _ := e.(map[string]interface{})
		if eo == nil {
			continue
		}
		out = append(out, [2]float32{float32(numf(eo["TransPos_x"])), float32(numf(eo["TransPos_y"]))})
	}
	return out
}

// sceneFieldMonsters 返回场景字段怪列表（位置 = 传送点光圈旁边，
// 挑战副本用 TrialCopy 专属 Pos；造型按场景 fieldMonsterConfigID）。
// ⚠ 怪不能站在传送光圈正中央：客户端点击光圈 → 打开传送选图/直接换层，
// 玩家点怪会误触光圈（用户实测"点击打怪时不注意就跑回城了"）。
// 因此从光圈中心偏移 portalOffset 格（朝向场景内部，光圈在边缘 x=±14 左右）。
func sceneFieldMonsters(mapID int32) []struct {
	configID int32
	mapType  int32
	x, y     float32
} {
	var out []struct {
		configID int32
		mapType  int32
		x, y     float32
	}
	sceneID := mapID / 100

	// MapMonsterConfig 1001..1004 correspond one-to-one with the scene's four
	// layers. Their online X/Y is the native centre formation position.
	if _, manualLayer, manual := manualEquipMapInfo(mapID); manual {
		configID := int32(1000 + manualLayer)
		cfg := tables.mapMonsterConfig[int64(configID)]
		out = append(out, struct {
			configID int32
			mapType  int32
			x, y     float32
		}{configID, mapMonsterTypeManualEquip, float32(numf(cfg["X"])), float32(numf(cfg["Y"]))})
		return out
	}

	// 星空旅行每层只有一个线上展示怪，MapMonsterConfig 1006..1011；
	// 表内 X/Y 就是场景专属阵位圈坐标，不能再放到传送门旁边。
	if sceneID >= 10039 && sceneID <= 10044 {
		cfgID := int32(1006 + sceneID - 10039)
		cfg := tables.mapMonsterConfig[int64(cfgID)]
		out = append(out, struct {
			configID int32
			mapType  int32
			x, y     float32
		}{cfgID, mapMonsterTypeSpaceTravel, float32(numf(cfg["X"])), float32(numf(cfg["Y"]))})
		return out
	}

	// 世界 BOSS 没有独立 MapMonsterConfig 行。用相同 PrefabId 的线上展示行；
	// 找不到时退到有效的隐藏 BOSS 行 1011，避免客户端查不存在的 ConfigId 崩溃。
	if ids, _, _, ok := sceneRosterForMap(mapID); ok && len(ids) > 0 && sceneID == 10010 {
		out = append(out, struct {
			configID int32
			mapType  int32
			x, y     float32
		}{configIDForMonsterID(ids[0]), mapMonsterTypeSpaceTravel, 0, -1.2})
	}
	return out
}

// portalOffset：字段怪离传送光圈的偏移（格）。光圈在场景边缘 x=±14 左右，
// 怪往场景内侧偏 2.5 格，避开点击光圈误触传送，同时仍靠近光圈可被看到。
const portalOffset float32 = 2.5

// portalOffsetPos：把传送光圈坐标偏移到光圈旁边（朝向场景内部）。
func portalOffsetPos(p [2]float32) (float32, float32) {
	ox := p[0] - portalOffset // 光圈 x 均为正（右边缘）或负（左边缘），统一向内偏
	if p[0] < 0 {
		ox = p[0] + portalOffset
	}
	return ox, p[1]
}

// isTrialScene：挑战副本场景（10009/10036/10037/10038）。
func isTrialScene(sceneID int32) bool {
	return sceneID == 10009 || sceneID == 10036 || sceneID == 10037 || sceneID == 10038
}

// pushFieldMonsters：进入有怪配置的场景后推送字段怪。
// 主城/广场/PvP 等无怪场景不推。
func (s *Server) pushFieldMonsters(ch *channel) {
	pid := ch.session.playerID
	if pid == 0 {
		return
	}
	mapID := ch.session.mapID
	if isActivityStageMap(mapID) {
		if state := ch.session.currentActivityScene(); state != nil {
			s.pushActivityFieldMonster(ch, state)
		} else if pending := s.pendingActivityForPlayer(ch.session.playerID); pending != nil && pending.targetMap == mapID {
			s.pushActivityFieldMonsterFor(ch, pending.activity)
		}
		return
	}
	if region, _, _, _, ok := mainStoryRosterForMap(mapID); ok {
		// 主线展示怪由客户端按 MainStory.MonsterPosArr 建在原生阵位圈上；
		// 点击后客户端发送 20048。不能再叠加 20320 字段怪。
		s.sendPush(ch, protocol.OpM2C_InitMainStoryMap, &protocol.M2C_InitMainStoryMap{
			MainStoryId: region,
			ActorId:     pid,
		})
		log.Printf("[S=%d] push main story map region=%d map=%d", ch.id, region, mapID)
		return
	}
	// World Bosses have a dedicated native client presentation. BossRefresh
	// expects BossBase._id and resolves MonsterBase/Prefab/Name/Level itself;
	// sending a fake 20320 field monster produces the wrong model and bypasses
	// the online BossComponent click flow.
	if layer := bossLayerForMapID(mapID); layer > 0 {
		if !bossAlive(layer) {
			log.Printf("[S=%d] boss layer %d is dead; no native boss presentation", ch.id, layer)
			return
		}
		ch.session.fieldBossUnitID = 0
		s.sendPush(ch, protocol.OpM2C_BossRefresh, &protocol.M2C_BossRefresh{
			BossId:  1000 + layer,
			ActorId: pid,
		})
		log.Printf("[S=%d] push native boss refresh bossBase=%d map=%d", ch.id, 1000+layer, mapID)
		return
	}
	if trialID, _, ok := trialCopyForMap(mapID); ok {
		// 试炼展示怪必须走客户端原生链路。20091 会按 TrialCopy 配置创建可点击
		// 展示怪，点击后客户端发送 20092；用 20320 伪装会绕过试炼组件，导致
		// BattleType、怪物清理和强制下一层全部失效。
		s.sendPush(ch, protocol.OpM2C_InitTrialCopyMap, &protocol.M2C_InitTrialCopyMap{
			TrialCopyId: trialID,
			ActorId:     pid,
		})
		log.Printf("[S=%d] push trial copy map trialId=%d map=%d", ch.id, trialID, mapID)
		return
	}
	fms := sceneFieldMonsters(mapID)
	if len(fms) == 0 {
		log.Printf("[S=%d] map %d has no field monsters", ch.id, mapID)
		return
	}
	layer := bossLayerForMapID(mapID)
	for _, fm := range fms {
		fieldMonsterSeq++
		seq := fieldMonsterSeq
		ch.session.fieldMonsterIDs = append(ch.session.fieldMonsterIDs, seq)
		if ch.session.fieldMonsterConfigIDs == nil {
			ch.session.fieldMonsterConfigIDs = make(map[int64]int32)
		}
		ch.session.fieldMonsterConfigIDs[seq] = fm.configID
		if layer > 0 {
			// BOSS 层：场景中只推【可点击字段怪】（M2C_CreateMapMonster 20320）。
			// ⚠ 不要在这里推 M2C_SendBossInfo(20061)：客户端 SendBossInfoHandler →
			// StartBossFightEvent（反汇编 1047）会设置 MyUnit.IsFight=true + 重排全队位置
			// + 建 BOSS 战斗单位 —— 这是【开战】流程。进图就推 → 玩家被锁进战斗态
			// （不能移动、无战斗 UI）→ 用户实测"进去就不能动、掉线"。
			// 20061 改在点击 BOSS 开战时推送（startSceneBattleByMap，见 battle.go）。
			ch.session.fieldBossUnitID = seq // BossDead 广播移除场景 BOSS 用
		}
		s.sendPush(ch, protocol.OpM2C_CreateMapMonster, &protocol.M2C_CreateMapMonster{
			Id:             seq,
			X:              fm.x,
			Y:              fm.y,
			ConfigId:       fm.configID,
			MapMonsterType: fm.mapType,
			ActorId:        pid,
		})
	}
	log.Printf("[S=%d] push field monsters %d for map %d", ch.id, len(fms), mapID)
}

// pushMapCoinMonster performs one independent encounter roll after a map has
// finished loading. The stock client maps type 3 directly to C2M_GetMapCoin;
// the active unit id is retained server-side because that request has no id.
func (s *Server) pushMapCoinMonster(ch *channel) bool {
	if s == nil || s.mapCoinRoll == nil || ch == nil || ch.session == nil ||
		ch.session.playerID <= 0 || tables == nil {
		return false
	}
	row := tables.mapMonsterConfig[int64(mapCoinMonsterConfigID)]
	if row == nil {
		return false
	}
	ss := ch.session
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	if ss.battle != nil {
		return false
	}
	resetMapCoinDaily(ss, time.Now())
	chance, _, _, dailyBase, _ := gameplayMapCoinSettings()
	if ss.mapCoinUnitID != 0 || ss.signin.MapCoinClaimed >= dailyBase+ss.signin.MapCoinExtra ||
		chance <= 0 || (chance < 100 && s.mapCoinRoll()*100 >= chance) {
		return false
	}
	fieldMonsterSeq++
	unitID := fieldMonsterSeq
	ss.mapCoinUnitID = unitID
	ss.fieldMonsterIDs = append(ss.fieldMonsterIDs, unitID)
	if ss.fieldMonsterConfigIDs == nil {
		ss.fieldMonsterConfigIDs = make(map[int64]int32)
	}
	ss.fieldMonsterConfigIDs[unitID] = mapCoinMonsterConfigID
	s.sendPush(ch, protocol.OpM2C_CreateMapMonster, &protocol.M2C_CreateMapMonster{
		Id: unitID, X: float32(numf(row["X"])), Y: float32(numf(row["Y"])),
		ConfigId: mapCoinMonsterConfigID, MapMonsterType: mapMonsterTypeMapCoin,
		ActorId: ss.playerID,
	})
	log.Printf("[S=%d] spawn map coin monster id=%d map=%d", ch.id, unitID, ss.mapID)
	return true
}

func consumeMapCoinMonster(ss *session) int64 {
	if ss == nil || ss.mapCoinUnitID == 0 {
		return 0
	}
	unitID := ss.mapCoinUnitID
	ss.mapCoinUnitID = 0
	delete(ss.fieldMonsterConfigIDs, unitID)
	for index, id := range ss.fieldMonsterIDs {
		if id == unitID {
			ss.fieldMonsterIDs = append(ss.fieldMonsterIDs[:index], ss.fieldMonsterIDs[index+1:]...)
			break
		}
	}
	return unitID
}

// clearFieldMonsters：换图前清除旧场景字段怪实体（M2C_DisposeMapMonster 20321）。
// 客户端 M2C_CreateMapMonster 创建的怪实体在 UnitComponent 里，不清理会残留
// （表现 = "换了场景还有旧场景的人/怪"）。
func (s *Server) clearFieldMonsters(ch *channel) {
	pid := ch.session.playerID
	if pid == 0 {
		return
	}
	for _, id := range ch.session.fieldMonsterIDs {
		s.sendPush(ch, protocol.OpM2C_DisposeMapMonster, &protocol.M2C_DisposeMapMonster{
			Id:      id,
			ActorId: pid,
		})
	}
	if len(ch.session.fieldMonsterIDs) > 0 {
		log.Printf("[S=%d] clear field monsters %d", ch.id, len(ch.session.fieldMonsterIDs))
	}
	ch.session.fieldMonsterIDs = nil
	ch.session.fieldMonsterConfigIDs = nil
	ch.session.mapCoinUnitID = 0
	ch.session.fieldBossUnitID = 0
	ch.session.bossChallengePending = false
}

// formatDurationCN：时长 → 中文显示（如 "1小时0分"）。
func formatDurationCN(d time.Duration) string {
	if d <= 0 {
		return "即将"
	}
	m := int(d.Minutes())
	if m < 1 {
		return "不到1分钟"
	}
	h := m / 60
	m = m % 60
	if h > 0 && m > 0 {
		return fmt.Sprintf("%d小时%d分", h, m)
	}
	if h > 0 {
		return fmt.Sprintf("%d小时", h)
	}
	return fmt.Sprintf("%d分钟", m)
}

// 20391 → 20392：点击地图单位（字段怪）。
// 响应 Message 留空 = 成功：客户端 b__1 回调据此 Remove(unit)（怪从场景消失）。
// 同时服务器按当前场景 (mapID) 开对应战斗：主线场景开 region 战斗，其余场景
// （挑战/Boss/手动装备/星空旅行）开场景战斗；是否扣体力由 CopyConfig 决定。
func (s *Server) onClickMapUnit(ch *channel, req *protocol.C2M_ClickMapUnit) proto.Message {
	log.Printf("[S=%d] click map unit id=%d type=%d", ch.id, req.Id, req.Type)
	resp := &protocol.M2C_ClickMapUnit{RpcId: req.RpcId}
	if ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	if ch.session.battleHP() <= 0 {
		resp.Message = battleEntryHealthMessage
		return resp
	}
	if pending := s.pendingActivityForPlayer(ch.session.playerID); pending != nil && pending.targetMap == ch.session.mapID {
		resp.Message = "队员场景尚未准备完成"
		return resp
	}
	if state := ch.session.currentActivityScene(); state != nil {
		started, n, message := s.startActivityBattle(ch, req, state)
		if !started {
			resp.Message = message
		} else {
			log.Printf("[S=%d] activity monster click -> battle map=%d monsters=%d", ch.id, ch.session.mapID, n)
		}
		return resp
	}
	region, _, _, _, ok := mainStoryRosterForMap(ch.session.mapID)
	var started bool
	var n int
	if req.Type == mapMonsterTypeManualEquip {
		started, n, _ = s.startManualEquipBattle(ch, req.Id)
	} else if ok {
		started, n = s.startMainStoryFightByRegion(ch, region)
	} else {
		started, n = s.startSceneBattleByMap(ch, ch.session.mapID)
	}
	if !started {
		if sessionHasBattle(ch) {
			log.Printf("[S=%d] duplicate field monster click ignored", ch.id)
			return resp
		}
		if ch.session.mapID/100 >= 10039 && ch.session.mapID/100 <= 10044 {
			if message := s.battleDailyDungeonQuotaFailure(ch, 1001, ch.session.mapID,
				battlePresentation{kind: presentationMainStory}, time.Now()); message != "" {
				resp.Message = message
				return resp
			}
		}
		// Boss 场景：BOSS 死亡期间 → 提示刷新倒计时（"BOSS 在刷新时间段内不会出现"）。
		if layer := bossLayerForMapID(ch.session.mapID); layer > 0 {
			if remain := bossDeadRemaining(layer); remain > 0 {
				msg := fmt.Sprintf("BOSS 已阵亡，%s后刷新", formatDurationCN(remain))
				resp.Message = msg
				return resp
			}
			if ch.session.energy < bossEnergyCost() {
				resp.Message = "体力不足"
				return resp
			}
		}
		if _, _, manual := manualEquipMapInfo(ch.session.mapID); manual {
			resp.Message = s.manualEquipBattleFailureMessage(ch)
			return resp
		}
		if ok && ch.session.energy < mainStoryEnergyCost(region) {
			resp.Message = "体力不足"
			return resp
		}
		// Boss 场景体力不足 → 明确提示（客户端弹错误，与主线一致）。
		if ch.session.mapID/100 == 10010 && ch.session.energy < bossEnergyCost() {
			resp.Message = "体力不足"
		} else {
			resp.Message = "开始战斗失败"
		}
	} else {
		log.Printf("[S=%d] field monster click -> battle map=%d region=%d monsters=%d", ch.id, ch.session.mapID, region, n)
	}
	return resp
}

func (s *Server) startActivityBattle(ch *channel, req *protocol.C2M_ClickMapUnit, state *activitySceneState) (bool, int, string) {
	if ch == nil || ch.session == nil || req == nil || state == nil || state.activity == nil {
		return false, 0, "活动场景状态无效"
	}
	configID, mapType, _, _, ok := activityFieldMonster(state.activity)
	if !ok || req.Type != mapType || ch.session.fieldMonsterConfigIDs[req.Id] != configID {
		return false, 0, "活动怪物已失效"
	}
	if ch.session.playerID != state.originID {
		return false, 0, teamLeaderDungeonEntryMessage
	}

	partyStartMu.Lock()
	defer partyStartMu.Unlock()
	if ch.session.currentActivityScene() != state {
		return false, 0, "活动怪物已失效"
	}
	participants := s.configuredBattleParticipants(ch, state.activity.copyID(), false)
	actualIDs := make([]int64, 0, len(participants))
	for _, member := range participants {
		if member != nil && member.session != nil {
			actualIDs = append(actualIDs, member.session.playerID)
		}
	}
	if !sameActivityParticipants(actualIDs, state.participantIDs) {
		return false, 0, "队伍成员已发生变化"
	}
	for _, playerID := range state.participantIDs {
		member := s.findChannelByPlayerID(playerID)
		seq := state.mapSeqs[playerID]
		if member == nil || member.session == nil || member.session.currentActivityScene() != state ||
			member.session.mapID != state.targetMap || !member.session.isCurrentMapStartupComplete(seq) {
			return false, 0, "队员场景尚未准备完成"
		}
	}
	ids, counts, ok := activityRoster(state.activity)
	if !ok {
		return false, 0, "活动怪物配置不存在"
	}
	units := ch.session.buildMonsterUnitsFromRoster(ids, counts)
	if len(units) != len(ids) || tables.mainStory[1001] == nil {
		return false, 0, "战斗配置不存在"
	}
	presentation := battlePresentation{
		kind: presentationMainStory, copyID: state.activity.copyID(),
		activity: state.activity, skipEnergy: state.skipEnergy,
	}
	ch.session.battleMu.Lock()
	started, count := s.finishStartBattleWithPresentation(ch, 1001, units, state.targetMap, presentation)
	ch.session.battleMu.Unlock()
	if !started {
		return false, 0, "开始战斗失败"
	}
	for _, playerID := range state.participantIDs {
		member := s.findChannelByPlayerID(playerID)
		if member == nil || member.session == nil {
			continue
		}
		member.session.clearActivityScene(state)
		s.clearFieldMonsters(member)
	}
	return true, count, ""
}

// startManualEquipBattle starts the selected elemental encounter. The field
// unit identifies one of MapMonsterConfig 1001..1004; the combat roster is the
// matching tier/element trio in ManulEquipMonsterConfig.
func (s *Server) startManualEquipBattle(ch *channel, fieldUnitID int64) (bool, int, int32) {
	if ch == nil || ch.session == nil || tables == nil {
		return false, 0, 0
	}
	ss := ch.session
	tier, layer, ok := manualEquipMapInfo(ss.mapID)
	if !ok {
		return false, 0, 0
	}
	fieldConfigID := ss.fieldMonsterConfigIDs[fieldUnitID]
	if fieldConfigID != 1000+layer {
		return false, 0, 0
	}
	configIDs := []int32{
		tier*1000 + layer*100 + 1,
		tier*1000 + layer*100 + 2,
		tier*1000 + layer*100 + 3,
	}
	monsterIDs := make([]int32, 0, len(configIDs))
	for _, configID := range configIDs {
		row := tables.manulEquipMonsterConfig[int64(configID)]
		monsterID := int32(num(row["MonsterId"]))
		if monsterID <= 0 {
			return false, 0, 0
		}
		monsterIDs = append(monsterIDs, monsterID)
	}

	partyStartMu.Lock()
	defer partyStartMu.Unlock()
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	if ss.battle != nil {
		return false, 0, configIDs[0]
	}
	units := ss.buildMonsterUnitsFromRoster(monsterIDs, []int{1, 1, 1})
	if len(units) != len(monsterIDs) {
		return false, 0, configIDs[0]
	}
	started, count := s.finishStartManualEquipBattle(ch, 1001, units, ss.mapID, configIDs)
	if started {
		presentation := battlePresentation{kind: presentationManualEquip}
		for _, member := range s.battleParticipantsForPresentation(ch, manualEquipCopyID, presentation) {
			s.clearFieldMonsters(member)
		}
	}
	return started, count, configIDs[0]
}

// 20322 -> 20323. Some client paths use the dedicated manual-equipment RPC
// instead of the generic map-unit click; both converge on the same battle.
func (s *Server) onStartManulEquipBattle(ch *channel, req *protocol.C2M_StartManulEquipBattle) proto.Message {
	resp := &protocol.M2C_StartManulEquipBattle{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Message = "请先登录"
		return resp
	}
	if ch.session.battleHP() <= 0 {
		resp.Message = battleEntryHealthMessage
		return resp
	}
	started, _, configID := s.startManualEquipBattle(ch, req.Id)
	resp.ConfigId = configID
	if !started {
		if sessionHasBattle(ch) {
			return resp
		}
		resp.Message = s.manualEquipBattleFailureMessage(ch)
	}
	return resp
}
