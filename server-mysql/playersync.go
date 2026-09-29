package main

import (
	"log"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

type sceneVisibilityKey struct {
	recipientChannelID int64
	sourceChannelID    int64
}

type sceneVisibilityVersion struct {
	recipientMapSeq uint64
	sourceMapSeq    uint64
}

// playersync.go：同场景在线玩家同步（"显示玩家"）。
//
// 协议（子代理 da49bb2d 对照 _dump_prop 确认）：
//   - M2C_UnitsInMap(20036)：Units=1(List<UnitPosInfo>)、PvpUnits=2(List<UnitCharacter>)，
//     两列表【必须等长】（客户端用同一下标取位置+角色）；Handler 先
//     UnitComponent.RemoveAll() 再全量重建 → 列表【必须含自己】（否则 MyUnit 被清）。
//   - M2C_LeaveMap(20037)：UnitId=1，单位离开场景。
//   - M2C_EnterMap(20034)：UnitInfo=1 + PvpUnitCharacter=2（单玩家进场，本实现用全量 20036 替代）。
//   - M2C_PathfindingResult(20024)：Id=1 + 起终点/速度，移动广播（同场景其他玩家）。

// sameMapSession：两个会话是否在同一场景（按 mapID 精确匹配）。
func sameMapSession(a, b *session) bool {
	if a == nil || b == nil || a.playerID <= 0 || b.playerID <= 0 || !sameSceneMapID(a.mapID, b.mapID) {
		return false
	}
	if _, _, trial := trialCopyForMap(a.mapID); trial {
		return a.playerID == b.playerID
	}
	return true
}

// 10004 is the canonical city map sent by ChangeMap; 1000401 is the legacy
// city-layer id used by a few client requests and saved records. They are the
// same visible scene for player visibility and social interactions.
func sameSceneMapID(a, b int32) bool {
	if (a == 10004 || a == 1000401) && (b == 10004 || b == 1000401) {
		return true
	}
	return a == b
}

// pushUnitsInMap：向 ch 推送 20036（同场景其他在线玩家，**不含自己**）。
// 客户端 M2C_UnitsInMapHandler（反汇编 249/1111）：先 UnitComponent.RemoveAll()
// 清掉其他单位（RemoveAll 保留 MyUnit——941 反汇编：收集 Id != MyUnit.Id 才移除），
// 再对 Units 列表逐项 UnitFactory.Create。若列表含自己 → 已存在的 MyUnit 重复创建 →
// "An item with the same key has already been added. Key: <self>" → 处理器中断
// （用户实测：登录后反复报错，同场景他人/新单位不再创建，进 BOSS 层卡死）。
// 自己的单位由 M2C_SendUnitInfo（进游戏）/ M2C_ChangeMap（换图）创建/定位。
// ⚠ 必须在 pushFieldMonsters 之前推（20036 的 RemoveAll 会清掉已建的字段怪）。
func (s *Server) pushUnitsInMap(ch *channel) {
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		return
	}
	var unitChars []*protocol.UnitCharacter
	var pos [][2]float32
	var yAngles []int32
	for _, other := range s.onlineChannels() {
		if other == nil || other == ch || other.session == nil {
			continue
		}
		if other.session.playerID == 0 {
			continue
		}
		if !sameMapSession(ch.session, other.session) {
			continue
		}
		unitChars = append(unitChars, buildUnitCharacter(other.session))
		pos = append(pos, [2]float32{other.session.x, other.session.y})
		yAngles = append(yAngles, other.session.yAngle)
	}
	var base []byte
	base = appendUnitsInMap(base, unitChars, pos, yAngles)
	base = pbAppendVarint(base, 93, uint64(ch.session.playerID)) // ActorId = 接收者
	s.sendRawPush(ch, protocol.OpM2C_UnitsInMap, base)
	log.Printf("[S=%d] push UnitsInMap players=%d map=%d", ch.id, len(unitChars), ch.session.mapID)
}

// broadcastUnitsInMapExcept：向与 ch 同场景的【其他】在线玩家广播原生
// M2C_EnterMap(20034)，使新进场/换图玩家立即对先在线玩家可见。
//
// ⚠ 不能用 20036 广播：M2C_UnitsInMapHandler 先 UnitComponent.RemoveAll() 清掉
// 全部单位（含 NPC）再只重建玩家——场景稳定后收到 20036 会把 NPC 清空且不重建
// （用户实测：测试账号进主城广播 20036 → 玩家主城 NPC 全部消失）。
// 20025 不能用于进场：M2C_SendUnitInfoHandler 只发布
// UpdateCommonCharacterUI，根本不创建 Unit。随后发 20024 也只会因找不到
// Unit 而被客户端忽略，表现就是“后登录的人能看见先登录的人，先登录的人
// 永远看不见后来者”。20034 的 handler 才会 UnitFactory.Create、设置 HUD、
// 坐标、宠物并发布 EnterUnit。
func (s *Server) broadcastUnitsInMapExcept(ch *channel) {
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		return
	}
	for _, other := range s.onlineChannels() {
		if other == nil || other == ch || other.session == nil {
			continue
		}
		if !sameMapSession(ch.session, other.session) {
			continue
		}
		s.sendScenePlayerEnter(other, ch)
	}
	log.Printf("[S=%d] broadcast EnterMap pid=%d to same-map players", ch.id, ch.session.playerID)
}

// markScenePlayerVisible records directed visibility. Only the receiving
// scene must be ready; a ready player may safely display a teammate whose own
// client is still loading. Source and recipient map versions prevent stale
// records from surviving another map change or reconnect.
func (s *Server) markScenePlayerVisible(recipient, source *channel) bool {
	if recipient == nil || source == nil || recipient == source ||
		recipient.session == nil || source.session == nil ||
		!sameMapSession(recipient.session, source.session) {
		return false
	}
	recipientSeq, recipientReady := recipient.session.currentMapSceneVersion()
	sourceSeq, _ := source.session.currentMapSceneVersion()
	if !recipientReady {
		return false
	}
	key := sceneVisibilityKey{recipientChannelID: recipient.id, sourceChannelID: source.id}
	version := sceneVisibilityVersion{recipientMapSeq: recipientSeq, sourceMapSeq: sourceSeq}
	s.sceneVisibilityMu.Lock()
	defer s.sceneVisibilityMu.Unlock()
	if s.sceneVisibility == nil {
		s.sceneVisibility = make(map[sceneVisibilityKey]sceneVisibilityVersion)
	}
	if current, found := s.sceneVisibility[key]; found && current == version {
		return false
	}
	s.sceneVisibility[key] = version
	return true
}

func (s *Server) scenePlayerVisibleTo(recipient, source *channel) bool {
	if recipient == nil || source == nil || recipient.session == nil || source.session == nil {
		return false
	}
	if recipient == source {
		return true
	}
	recipientSeq, recipientReady := recipient.session.currentMapSceneVersion()
	sourceSeq, _ := source.session.currentMapSceneVersion()
	if !recipientReady || !sameMapSession(recipient.session, source.session) {
		return false
	}
	// Unit tests and pre-map synthetic sessions use version zero. Production
	// EnterGame always calls markMapChange before exposing a player online.
	if recipientSeq == 0 && sourceSeq == 0 {
		return true
	}
	key := sceneVisibilityKey{recipientChannelID: recipient.id, sourceChannelID: source.id}
	want := sceneVisibilityVersion{recipientMapSeq: recipientSeq, sourceMapSeq: sourceSeq}
	s.sceneVisibilityMu.Lock()
	visible := s.sceneVisibility[key] == want
	s.sceneVisibilityMu.Unlock()
	return visible
}

func (s *Server) sendScenePlayerEnter(recipient, source *channel) bool {
	if !s.markScenePlayerVisible(recipient, source) {
		return false
	}
	recipientSeq, _ := recipient.session.currentMapSceneVersion()
	sourceSeq, _ := source.session.currentMapSceneVersion()
	s.sendPush(recipient, protocol.OpM2C_EnterMap, &protocol.M2C_EnterMap{
		UnitInfo:         &protocol.UnitPosInfo{X: source.session.x, Y: source.session.y, YAngle: source.session.yAngle},
		PvpUnitCharacter: buildUnitCharacter(source.session),
		ActorId:          recipient.session.playerID,
	})
	// EnterMap FillNum only seeds HP/MP. Follow with the live resource
	// snapshot so late viewers do not keep another client's leftover bar.
	s.pushUnitResourceAttrs(recipient, source.session)
	log.Printf("[S=%d] push EnterMap recipientPid=%d sourceS=%d sourcePid=%d map=%d seq=%d/%d pos=(%.2f,%.2f)",
		recipient.id, recipient.session.playerID, source.id, source.session.playerID,
		recipient.session.mapID, recipientSeq, sourceSeq, source.session.x, source.session.y)
	return true
}

func (s *Server) pushUnitResourceAttrs(ch *channel, ss *session) {
	if s == nil || ch == nil || ch.session == nil || ss == nil || ss.playerID == 0 {
		return
	}
	hp, mp := ss.battleHP(), ss.battleMP()
	maxHP, maxMP := ss.playerMaxHp(), ss.playerMaxMp()
	if maxHP <= 0 {
		maxHP = 1
	}
	if maxMP < 0 {
		maxMP = 0
	}
	ss.storeTeamHeadHP(hp, maxHP)
	for _, attr := range []struct {
		numericType int32
		value       int32
	}{
		{1002, maxHP},
		{1001, hp},
		{1004, maxMP},
		{1003, mp},
	} {
		s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: ss.playerID, NumericType: attr.numericType,
			Value: float32(attr.value), ActorId: ch.session.playerID,
		})
	}
}

// syncReadyScenePlayers establishes both directed views after a scene load.
// A peer that is still loading rejects its receiving direction and will retry
// it from its own startup callback.
func (s *Server) syncReadyScenePlayers(ch *channel) {
	if ch == nil || ch.session == nil || ch.session.playerID <= 0 {
		return
	}
	for _, other := range s.onlineChannels() {
		if other == nil || other == ch || other.session == nil ||
			!sameMapSession(ch.session, other.session) {
			continue
		}
		s.sendScenePlayerEnter(ch, other)
		s.sendScenePlayerEnter(other, ch)
	}
}

// clearSceneUnitsForMapChange invokes the client's native UnitComponent.RemoveAll
// before ChangeMap. NPC Unit IDs are generated locally and cannot be derived
// from NPCBase config IDs, so targeted server messages cannot remove them.
// RemoveAll preserves MyUnit and the empty lists skip FillNum entirely, leaving
// the local player's level and HP/MP untouched. Scene startup recreates peers
// with complete UnitCharacter values before the team snapshot is restored.
func (s *Server) clearSceneUnitsForMapChange(ch *channel) {
	if ch == nil || ch.session == nil || ch.session.playerID <= 0 {
		return
	}
	s.sendPush(ch, protocol.OpM2C_UnitsInMap, &protocol.M2C_UnitsInMap{
		ActorId: ch.session.playerID,
	})
	log.Printf("[S=%d] clear old scene Units before ChangeMap player=%d map=%d",
		ch.id, ch.session.playerID, ch.session.mapID)
}

// broadcastPlayerUpdate refreshes an already-created scene player. EnterMap
// must only be used for creation: sending it again for a rename/equipment/job
// change makes UnitFactory.Create insert the same unit id twice. SendUnitInfo
// updates ClientUnitCharacter, while the full Numeric snapshot updates the
// existing Unit and drives skin/stat watchers.
func (s *Server) broadcastPlayerUpdate(ch *channel) {
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		return
	}
	ss := ch.session
	for _, other := range s.onlineChannels() {
		if other == nil || other == ch || other.session == nil || !sameMapSession(ss, other.session) {
			continue
		}
		s.sendPush(other, protocol.OpM2C_SendUnitInfo, &protocol.M2C_SendUnitInfo{
			UnitCharacter: buildUnitCharacter(ss),
			ActorId:       other.session.playerID,
		})
		s.pushPlayerAttrsTo(other, ss, ss.battleHP(), ss.battleMP())
	}
	log.Printf("[S=%d] broadcast player update pid=%d to same-map players", ch.id, ss.playerID)
}

// broadcastLeaveMap：ch 换图时向原场景其他玩家推 20037。
// 客户端用 Remove(UnitId, false) 处理该消息，保留可复用的场景资源。
func (s *Server) broadcastLeaveMap(ch *channel) {
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		return
	}
	pid := ch.session.playerID
	for _, other := range s.onlineChannels() {
		if other == nil || other == ch || other.session == nil {
			continue
		}
		if !sameMapSession(ch.session, other.session) {
			continue
		}
		base := appendLeaveMap(nil, pid)
		base = pbAppendVarint(base, 93, uint64(other.session.playerID))
		s.sendRawPush(other, protocol.OpM2C_LeaveMap, base)
	}
	log.Printf("[S=%d] broadcast LeaveMap pid=%d", ch.id, pid)
}

// broadcastOffline：ch 真正断线时向同场景其他玩家推 20038。
// 客户端用 Remove(UnitId, true) 彻底销毁玩家单位及其宠物显示对象；断线时若误发
// LeaveMap，宠物对象会在观察者客户端残留。
func (s *Server) broadcastOffline(ch *channel) {
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		return
	}
	pid := ch.session.playerID
	for _, other := range s.onlineChannels() {
		if other == nil || other == ch || other.session == nil {
			continue
		}
		if !sameMapSession(ch.session, other.session) {
			continue
		}
		s.sendPush(other, protocol.OpM2C_OffLine, &protocol.M2C_OffLine{
			UnitId:  pid,
			ActorId: other.session.playerID,
		})
	}
	log.Printf("[S=%d] broadcast OffLine pid=%d", ch.id, pid)
}

// broadcastMove：玩家移动 → 同场景其他玩家广播 20024（M2C_PathfindingResult 带 Id）。
// 客户端 PathfindingResultHandler 按 Id 定位单位并走位；对不存在的单位静默跳过。
func (s *Server) broadcastMove(ch *channel, pf *protocol.M2C_PathfindingResult) {
	if ch == nil || ch.session == nil || ch.session.playerID == 0 || pf == nil {
		return
	}
	for _, other := range s.onlineChannels() {
		if other == nil || other == ch || other.session == nil {
			continue
		}
		if !sameMapSession(ch.session, other.session) {
			continue
		}
		pf.ActorId = other.session.playerID
		s.sendPush(other, protocol.OpM2C_PathfindingResult, pf)
	}
}

// 确保 proto 包被引用（appendUnitsInMap 已用；避免误删导入）。
var _ = proto.Marshal
