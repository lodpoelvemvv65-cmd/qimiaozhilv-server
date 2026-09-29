package main

import (
	"log"
	"time"

	"mhqserver/protocol"
)

// The wire protocol encodes a map as sceneId*100 + layer. Keep the legacy
// logical city id 10004 in player data, but send its valid layer-1 id so the
// client does not decode it as the nonexistent scene 100.
func wireMapID(mapID int32) int32 {
	if mapID == 10004 {
		return 1000401
	}
	return mapID
}

const (
	mapStartupDelay      = 750 * time.Millisecond
	mapTeamSnapshotDelay = 200 * time.Millisecond
	beachPortalDelay     = 500 * time.Millisecond
	// The native client needs a short interval after scene startup to create
	// NPCs and its task window. Online traces show this unsolicited window
	// about one second after the initial team snapshot.
	initialTaskUIPushDelay = time.Second
)

func (s *Server) scheduleMapStartup(ch *channel, mapID int32, seq uint64, initializeNumerics bool) {
	s.scheduleMapStartupWithOptions(ch, mapID, seq, mapStartupOptions{
		initializeNumerics:     initializeNumerics,
		initializePresentation: true,
	})
}

type mapStartupOptions struct {
	initializeNumerics     bool
	initializePresentation bool
}

func (s *Server) scheduleLoginMapStartup(ch *channel, mapID int32, seq uint64, firstEntry bool) {
	if ch != nil && ch.session != nil {
		// Existing roles need their first complete Numeric snapshot after the
		// scene has created MyUnit.  Keep that requirement alive if another
		// login-time map transition supersedes this startup callback.
		ch.session.loginNumericPending.Store(!firstEntry)
	}
	s.scheduleMapStartupWithOptions(ch, mapID, seq, mapStartupOptions{
		initializeNumerics:     !firstEntry,
		initializePresentation: !firstEntry,
	})
}

func (s *Server) scheduleMapStartupWithOptions(ch *channel, mapID int32, seq uint64, options mapStartupOptions) {
	if ch == nil || ch.session == nil {
		return
	}
	time.AfterFunc(mapStartupDelay, func() {
		s.finishMapStartupWithOptions(ch, mapID, seq, options)
	})
}

func beachTransferPointUnlocked(ss *session, mapID int32) bool {
	if mapID/100 != 10006 {
		return true
	}
	layer := mapID % 100
	if layer < 1 || layer > 10 {
		return true
	}
	return beachHighestCompletedLayer(ss) >= layer
}

func mainStoryTransferPointUnlocked(ss *session, mapID int32) bool {
	region, ok := mainStoryRegionForMapID(mapID)
	if !ok {
		return true
	}
	return mainStoryCompleted(ss, region)
}

func (s *Server) pushStartupTransPoint(ch *channel, mapID int32) {
	s.sendPush(ch, protocol.OpM2C_StartupTransPoint, &protocol.M2C_StartupTransPoint{
		MapId: wireMapID(mapID), ActorId: ch.session.playerID,
	})
	log.Printf("[S=%d] push M2C_StartupTransPoint mapId=%d", ch.id, wireMapID(mapID))
}

func (s *Server) scheduleBeachTransferPoint(ch *channel, mapID int32) {
	if ch == nil || ch.session == nil {
		return
	}
	seq, ready := ch.session.currentMapStartupVersion()
	if !ready {
		return
	}
	time.AfterFunc(beachPortalDelay, func() {
		if s.isCurrentOnlineChannel(ch) && ch.session.mapID == mapID &&
			ch.session.isCurrentMapStartupComplete(seq) && beachTransferPointUnlocked(ch.session, mapID) {
			s.pushStartupTransPoint(ch, mapID)
		}
	})
}

func (s *Server) scheduleMainStoryTransferPoint(ch *channel, mapID int32) {
	if ch == nil || ch.session == nil {
		return
	}
	seq, ready := ch.session.currentMapSceneVersion()
	if !ready {
		return
	}
	time.AfterFunc(beachPortalDelay, func() {
		if s.isCurrentOnlineChannel(ch) && ch.session.mapID == mapID &&
			ch.session.isCurrentMapStartupComplete(seq) && mainStoryTransferPointUnlocked(ch.session, mapID) {
			s.pushStartupTransPoint(ch, mapID)
		}
	})
}

func (s *Server) finishMapStartup(ch *channel, mapID int32, seq uint64, initializeNumerics bool) bool {
	return s.finishMapStartupWithOptions(ch, mapID, seq, mapStartupOptions{
		initializeNumerics:     initializeNumerics,
		initializePresentation: true,
	})
}

func (s *Server) finishMapStartupWithOptions(ch *channel, mapID int32, seq uint64, options mapStartupOptions) bool {
	s.mapTransitionMu.Lock()
	if !s.isCurrentOnlineChannel(ch) || !ch.session.markMapReady(seq) {
		s.mapTransitionMu.Unlock()
		return false
	}
	// On login the scene Unit exists before MainUI. Deferring the online
	// batch snapshot until this scene-start callback keeps Numeric watchers
	// from touching an unconstructed HUD while still preceding transfer points.
	if options.initializeNumerics {
		s.pushInitialPlayerAttrs(ch)
		ch.session.loginNumericPending.Store(false)
	} else {
		// Post-login ChangeMap preserves MyUnit, so a leftover client bar
		// would otherwise survive into town. Refresh the live resources after
		// the local unit exists.
		s.pushUnitResourceAttrs(ch, ch.session)
	}
	s.syncReadyScenePlayers(ch)
	if options.initializePresentation {
		s.pushFieldMonsters(ch)
	}
	// Rebuild the top-left item-buff icons only after the scene has created the
	// local HUD.  Buff state is restored from MySQL during EnterGame; without
	// this second-stage push a reconnect would keep the capacity but lose the
	// visible magic-ball icon until the player manually queried buff time.
	if options.initializeNumerics || options.initializePresentation {
		s.pushActiveItemBuffs(ch)
	}
	if mainStoryTransferPointUnlocked(ch.session, mapID) {
		s.pushStartupTransPoint(ch, mapID)
	} else {
		log.Printf("[S=%d] defer main-story transfer point until victory mapId=%d", ch.id, mapID)
	}
	if !ch.session.markMapStartupComplete(seq) {
		s.mapTransitionMu.Unlock()
		return false
	}
	s.mapTransitionMu.Unlock()
	// Map-coin creation takes battleMu. Keep that lock outside mapTransitionMu:
	// battle settlement can schedule a map change while it still owns battleMu.
	// Holding both in the opposite order deadlocks settlement and scene startup.
	if s.isCurrentOnlineChannel(ch) && ch.session.mapID == mapID &&
		ch.session.isCurrentMapStartupComplete(seq) {
		s.pushMapCoinMonster(ch)
	}
	// The shop catalog is consumed by the patched ShopUI filter.  Every login
	// path must receive it after the scene is ready; relying only on the
	// initial-task branch left newly created roles (and accounts without a
	// pending task UI) with catalogLoaded=false, so their local static ShopBase
	// rows—including disabled products—remained visible indefinitely.
	// Only the live process owns the unsolicited catalog push. Unit tests build
	// isolated Server values to assert the native startup frame order; keeping
	// their snapshots untouched avoids coupling those protocol-order tests to
	// the optional shop extension.
	//
	// 新角色首进不走这里：线上首进延迟批次本身就是 20216/20370/20423/20170，
	// 而 pushFirstEntryReadyState 会在场景就绪后补发 20423（含商城目录）。
	// 在这里再推一次会让新角色收到两个 20423，且早的那一个落在 MyUnit 创建前，
	// 与「组件相关状态必须等启动屏障之后」的既有约束冲突。
	if globalServer == s && !ch.session.initialTaskUIPending {
		s.pushActiveInfo(ch)
	}
	s.tryStartPendingActivity(ch)
	time.AfterFunc(mapTeamSnapshotDelay, func() {
		if s.isCurrentOnlineChannel(ch) && ch.session.isCurrentMapStartupComplete(seq) {
			s.restoreTeamSnapshotAfterMapStartup(ch)
			if ch.session.initialTaskUIPending {
				time.AfterFunc(initialTaskUIPushDelay, func() {
					if s.isCurrentOnlineChannel(ch) && ch.session.isCurrentMapStartupComplete(seq) {
						s.pushFirstEntryReadyState(ch)
					}
				})
			}
		}
	})
	return true
}

// pushFirstEntryReadyState runs after EnterGameFinish has created MyUnit and
// the main HUD. The native server sends this second-stage batch roughly two
// seconds after ChangeMap; sending Pet/Active earlier dereferences missing
// client components during a new role's first session.
func (s *Server) pushFirstEntryReadyState(ch *channel) {
	if ch == nil || ch.session == nil || !ch.session.initialTaskUIPending {
		return
	}
	s.pushInitialTaskUI(ch)
	s.pushSyncPet(ch)
	s.pushActiveInfo(ch)
	s.pushActiveItemBuffs(ch)
	s.pushInitialPlayerAttrs(ch)
}

// onGetStateReback restores the current team before target interaction resumes.
func (s *Server) onGetStateReback(ch *channel, req *protocol.C2M_GetStateReback) {
	pid := ch.session.playerID
	if pid == 0 {
		return
	}
	s.pushTeamMember(ch)
	log.Printf("[S=%d] get state reback actorId=%d; restore team for player=%d", ch.id, req.ActorId, pid)
}

// pushLoginMapSnapshot starts the local player's scene. The empty UnitsInMap
// matches the native login sequence and clears stale scene entities without
// recreating peers before ChangeMap. A newly created role also receives the
// native Numeric and map-presentation packets immediately after ChangeMap.
func (s *Server) pushLoginMapSnapshot(ch *channel, change *protocol.M2C_ChangeMap, firstEntry bool) {
	s.clearSceneUnitsForMapChange(ch)
	s.sendPush(ch, protocol.OpM2C_ChangeMap, change)
	if firstEntry {
		s.pushInitialPlayerAttrs(ch)
		s.pushFieldMonsters(ch)
	}
}

// stopMovementForMapChange cancels the old scene's client-side MoveToAsync
// before ChangeMap starts its asynchronous scene load. The original client
// handles M2C_Stop by publishing MoveStop, whose view event plays Idle.
func (s *Server) stopMovementForMapChange(ch *channel) {
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		return
	}
	ss := ch.session
	// A pending party-follow timer belongs to the old scene. Invalidate it
	// before the asynchronous ChangeMap begins so it cannot replay its target
	// after the party arrives in a new map.
	ss.teamMoveSeq.Add(1)
	x, y := ss.stopMovement(time.Now(), moveSpeed)
	s.sendPush(ch, protocol.OpM2C_Stop, &protocol.M2C_Stop{
		Id: ss.playerID, X: x, Y: y, YAngle: ss.yAngle, ActorId: ss.playerID,
	})
}

// changeMap pushes the native scene packets and persists the authoritative map
// before returning. Trial progression relies on this write surviving a server
// restart, rather than on any client-side state.
func (s *Server) changeMap(ch *channel, mapID int32, x, y float32) uint64 {
	return s.changeMapWithStartupDelay(ch, mapID, x, y, true)
}

// changeMapForActivity sends the map transition without arming the generic
// 750ms timer. Activity entry completes scene visibility synchronously after
// every captured participant has received ChangeMap.
func (s *Server) changeMapForActivity(ch *channel, mapID int32, x, y float32) uint64 {
	return s.changeMapWithStartupDelay(ch, mapID, x, y, false)
}

func (s *Server) changeMapWithStartupDelay(ch *channel, mapID int32, x, y float32, scheduleStartup bool) uint64 {
	s.mapTransitionMu.Lock()
	defer s.mapTransitionMu.Unlock()
	oldMapID := ch.session.mapID
	mapSeq := s.changeMapVisiblePhase(ch, mapID, x, y)
	s.changeMapPersistPhase(ch, oldMapID, mapID, mapSeq, scheduleStartup)
	return mapSeq
}

// changeMapVisiblePhase 是 ChangeMap 对客户端可见的那一半：广播离场、清场、
// 推送 M2C_ChangeMap 并更新会话地图与坐标。调用方必须持有 mapTransitionMu。
func (s *Server) changeMapVisiblePhase(ch *channel, mapID int32, x, y float32) uint64 {
	pid := ch.session.playerID
	s.cancelTradeForPlayer(pid, "换图后交易已取消")
	s.broadcastLeaveMap(ch)
	ch.session.clearActivityScene(nil)
	s.clearFieldMonsters(ch)
	mapSeq := ch.session.markMapChange()
	s.stopMovementForMapChange(ch)
	s.clearSceneUnitsForMapChange(ch)
	// ChangeMap cannot cancel 20080 icons: the client keeps them on the preserved
	// MyUnit until its own timer expires them. Resend the exact Ids this session
	// still holds with Time=0 before the scene is rebuilt.
	if cleared := s.clearPlayerBattleStates(ch); cleared > 0 {
		log.Printf("[S=%d] cleared %d leftover combat states before map %d", ch.id, cleared, mapID)
	}
	s.sendPush(ch, protocol.OpM2C_ChangeMap, &protocol.M2C_ChangeMap{
		MapId: wireMapID(mapID), X: x, Y: y, ActorId: pid,
	})
	ch.session.mapID = mapID
	ch.session.resetMovement(x, y)
	setMainStoryResumeMap(ch.session, mapID)
	return mapSeq
}

// changeMapPersistPhase 是 ChangeMap 的服务端收尾：落库、试炼快照刷新，以及
// 让场景在 mapStartupDelay 后完成初始化的定时器。
// 定时器回调 finishMapStartup 同样要抢 mapTransitionMu，因此调用方要保证所有
// 成员的切图帧已经发完再进入这里（见 changeMapForParty）。
// 调用方必须持有 mapTransitionMu。
func (s *Server) changeMapPersistPhase(ch *channel, oldMapID, mapID int32, mapSeq uint64, scheduleStartup bool) {
	s.saveData(ch)
	if isTrialMap(oldMapID) || isTrialMap(mapID) {
		s.refreshTeamSnapshotsForPlayer(ch.session.playerID)
	}
	if scheduleStartup {
		// A launcher-team fan-out may be the first scene the client finishes
		// after login.  Carry the pending login snapshot into that replacement
		// map; ordinary post-login transitions keep the native no-snapshot path.
		s.scheduleMapStartup(ch, mapID, mapSeq, ch.session.loginNumericPending.Load())
	}
	log.Printf("[S=%d] change map -> mapId=%d pos=(%.2f,%.2f)", ch.id, mapID, ch.session.x, ch.session.y)
}

// changeMapForParty 让整队在同一瞬间切到同一个场景：先把每个成员的 ChangeMap
// 帧连续推出去，再逐个落库并挂场景启动定时器。
// 逐个调用 changeMap 时，前一名成员 750ms 后的场景启动会一直持有
// mapTransitionMu 做完整个场景初始化（重建场景单位、队伍快照……），把后面成员的
// 切图挡在锁外；实测 5 人队伍回城时每人相差约 0.7 秒、全队走完要 3 秒，画面上
// 就是一个一个地回城。
func (s *Server) changeMapForParty(members []*channel, mapID int32, x, y float32) {
	if len(members) == 0 {
		return
	}
	s.mapTransitionMu.Lock()
	defer s.mapTransitionMu.Unlock()
	type pendingMapChange struct {
		ch       *channel
		oldMapID int32
		seq      uint64
	}
	pending := make([]pendingMapChange, 0, len(members))
	for _, member := range members {
		if member == nil || member.session == nil || member.session.playerID == 0 {
			continue
		}
		oldMapID := member.session.mapID
		pending = append(pending, pendingMapChange{
			ch: member, oldMapID: oldMapID, seq: s.changeMapVisiblePhase(member, mapID, x, y),
		})
	}
	for _, p := range pending {
		s.changeMapPersistPhase(p.ch, p.oldMapID, mapID, p.seq, true)
	}
	log.Printf("party change map -> mapId=%d pos=(%.2f,%.2f) members=%d", mapID, x, y, len(pending))
}

func isTrialMap(mapID int32) bool {
	_, _, ok := trialCopyForMap(mapID)
	return ok
}

// trialTier describes the native trial difficulty containing mapID. The client
// has four fixed entry buttons and always requests the first map of that tier.
func trialTier(mapID int32) (level, entryMap int32, ok bool) {
	_, row, ok := trialCopyForMap(mapID)
	if !ok {
		return 0, 0, false
	}
	level = int32(num(row["Level"]))
	for _, candidate := range tables.trialCopy {
		if int32(num(candidate["Level"])) != level {
			continue
		}
		candidateMap := int32(num(candidate["MapId"]))
		if entryMap == 0 || candidateMap < entryMap {
			entryMap = candidateMap
		}
	}
	return level, entryMap, entryMap > 0
}

func nextTrialMapInTier(highestID, level int32) (int32, bool) {
	highest, ok := trialCopyByID(highestID)
	if !ok || int32(num(highest["Level"])) != level {
		return 0, false
	}
	nextMap := int32(num(highest["MapId"])) + 1
	_, next, ok := trialCopyForMap(nextMap)
	if !ok || int32(num(next["Level"])) != level {
		return 0, false
	}
	return nextMap, true
}

// trialEntryResumeMap translates the client's fixed tier-entry request into
// the player's pending layer. The current persisted trial map wins; completed
// progress repairs legacy rows that were previously overwritten with layer 1.
func trialEntryResumeMap(ss *session, requestedMap int32) int32 {
	if ss == nil || ss.signin == nil {
		return requestedMap
	}
	level, entryMap, ok := trialTier(requestedMap)
	if !ok || requestedMap != entryMap {
		return requestedMap
	}
	if currentLevel, _, currentOK := trialTier(ss.mapID); currentOK && currentLevel == level {
		currentID, _, _ := trialCopyForMap(ss.mapID)
		if currentID > ss.signin.TrialHighestID {
			return ss.mapID
		}
	}
	if nextMap, found := nextTrialMapInTier(ss.signin.TrialHighestID, level); found {
		return nextMap
	}
	return requestedMap
}

// restorePersistedTrialLayer runs after both players.map_id and
// player_activity.trial_highest_id have been loaded. It repairs states written
// by the old fixed-entry handler, while leaving intentional city exits alone.
func restorePersistedTrialLayer(ss *session) bool {
	if ss == nil || ss.signin == nil {
		return false
	}
	level, _, ok := trialTier(ss.mapID)
	if !ok {
		return false
	}
	currentID, _, _ := trialCopyForMap(ss.mapID)
	if currentID > ss.signin.TrialHighestID {
		return false
	}
	nextMap, found := nextTrialMapInTier(ss.signin.TrialHighestID, level)
	if !found || nextMap == ss.mapID {
		return false
	}
	oldMap := ss.mapID
	ss.mapID = nextMap
	ss.x, ss.y = sceneSpawn(nextMap / 100)
	ss.resetMovement(ss.x, ss.y)
	log.Printf("restore persisted trial layer player=%d map=%d -> %d highest=%d",
		ss.playerID, oldMap, nextMap, ss.signin.TrialHighestID)
	return true
}

func validMapTransition(current, target int32) bool {
	targetCity := target == 10004 || target == 1000401
	if targetCity {
		return true
	}
	if target == 1000611 {
		return current/100 == 10006 && current%100 == 10
	}
	if target >= 1000601 && target <= 1000610 {
		if current/100 == 10006 {
			currentLayer := current % 100
			targetLayer := target % 100
			return targetLayer == currentLayer || targetLayer == currentLayer+1
		}
		return target == 1000601
	}
	if isManualEquipSceneID(target / 100) {
		_, _, ok := manualEquipMapInfo(target)
		return ok
	}
	// SceneTransConfig is only a scene-level whitelist. MainStory also has a
	// finite set of configured layers, so reject forged/nonexistent layer ids
	// before the generic scene check accepts them.
	if isMainStoryScene(target / 100) {
		_, _, _, _, ok := mainStoryRosterForMap(target)
		return ok
	}
	return knownScene(target / 100)
}

// onRequestEnterMap handles the client's native 20031 scene request. Trial
// buttons contain fixed first-layer map IDs, so the server resolves those IDs
// against persisted progress before validating or saving the transition.
func (s *Server) onRequestEnterMap(ch *channel, req *protocol.C2M_RequestEnterMap) (*protocol.M2C_RequestEnterMap, func()) {
	resp := &protocol.M2C_RequestEnterMap{RpcId: req.RpcId}
	if ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp, nil
	}
	// 20365's automatic main-story runner walks into the native portal trigger
	// before its server-side movement callback changes the scene. The client
	// consequently emits a competing 20031 request (at the final layer this
	// is an out-of-range layer and used to show "无法进入该层"). The runner is
	// authoritative for the entire route, so consume that request silently and
	// let the pending physical-portal callback perform the transition.
	if s.mainStoryAIRouteActive(ch) {
		log.Printf("[S=%d] ignore portal request during main-story AI map=%d target=%d", ch.id, ch.session.mapID, req.MapId)
		return resp, nil
	}
	if isManualEquipSceneID(req.MapId / 100) {
		currentCity := ch.session.mapID == 10004 || ch.session.mapID == 1000401
		if req.MapId%100 != 1 || !currentCity {
			resp.Message = "无法进入该手工副本层"
			return resp, nil
		}
	}
	targetMap := trialEntryResumeMap(ch.session, req.MapId)
	targetMap = manualEquipEntryResumeMap(ch.session, targetMap)
	targetMap = mainStoryEntryResumeMap(ch.session, targetMap)
	targetMap = manualEquipPortalTargetMap(ch.session, targetMap)
	log.Printf("[S=%d] request enter map %d resolved=%d", ch.id, req.MapId, targetMap)
	if !validMapTransition(ch.session.mapID, targetMap) {
		log.Printf("[S=%d] ignore out-of-order map request current=%d target=%d", ch.id, ch.session.mapID, targetMap)
		resp.Message = "无法进入该层"
		return resp, nil
	}
	if ch.session.mapID/100 == 10006 && targetMap/100 == 10006 {
		currentLayer := ch.session.mapID % 100
		targetLayer := targetMap % 100
		if targetLayer == currentLayer+1 && beachHighestCompletedLayer(ch.session) < currentLayer {
			log.Printf("[S=%d] reject beach advance without victory current=%d target=%d progress=%d",
				ch.id, ch.session.mapID, targetMap, beachHighestCompletedLayer(ch.session))
			resp.Message = "请先完成本层战斗"
			return resp, nil
		}
	}
	if message := nonBeachMainStoryTransitionMessage(ch.session, targetMap); message != "" {
		log.Printf("[S=%d] reject non-beach main-story transition current=%d target=%d: %s",
			ch.id, ch.session.mapID, targetMap, message)
		resp.Message = message
		return resp, nil
	}
	if message := s.mainStoryTeamEntryFailure(ch, targetMap); message != "" {
		log.Printf("[S=%d] reject team main-story transition current=%d target=%d: %s",
			ch.id, ch.session.mapID, targetMap, message)
		resp.Message = message
		return resp, nil
	}
	if targetMap != ch.session.mapID {
		// A player-initiated portal cancels any pending 20365 continuation.
		// The automatic runner changes maps directly and therefore does not pass
		// through this request handler.
		ch.session.stopMainStoryAI()
	}
	// Keep the original beach region checks intact. Non-beach stages use the
	// configured map order above because MainStory's 10061 layer 9 is keyed as
	// region 1; comparing region+1 would reject its valid layer-10 transition.
	if ch.session.mapID/100 == 10006 || targetMap/100 == 10006 {
		if currentRegion, currentOK := mainStoryRegionForMapID(ch.session.mapID); currentOK {
			if targetRegion, targetOK := mainStoryRegionForMapID(targetMap); targetOK {
				if targetRegion != currentRegion && !mainStoryCompleted(ch.session, currentRegion) {
					log.Printf("[S=%d] reject main-story advance without victory currentRegion=%d targetRegion=%d", ch.id, currentRegion, targetRegion)
					resp.Message = "璇峰厛瀹屾垚鏈眰鎴樻枟"
					return resp, nil
				}
				if targetRegion != currentRegion {
					// MainStory rows are ordered by scene/layer. Only the configured
					// next row may be entered; arbitrary skips remain rejected.
					if targetRegion != currentRegion+1 {
						resp.Message = "鏃犳硶杩涘叆璇ュ眰"
						return resp, nil
					}
				}
			}
		}
	}
	if targetMap == ch.session.mapID ||
		((targetMap == 1000401 || targetMap == 10004) && (ch.session.mapID == 1000401 || ch.session.mapID == 10004)) {
		return resp, nil
	}
	privateTrial := isTrialMap(targetMap)
	if !privateTrial {
		if message := s.teamLeaderDungeonEntryFailure(ch); message != "" {
			resp.Message = message
			return resp, nil
		}
	}
	var transition func()
	switch {
	case targetMap == 10004 || targetMap == 1000401:
		x, y := mainCityReturnSpawn()
		fromTrial := isTrialMap(ch.session.mapID)
		transition = func() {
			if fromTrial {
				s.changeMap(ch, 10004, x, y)
			} else {
				s.changeMapForTeamLeader(ch, 10004, x, y)
			}
		}
	case targetMap >= 1000601 && targetMap <= 1000610:
		transition = func() { s.changeMapForTeamLeader(ch, targetMap, -1.8, -0.84) }
	case targetMap == 1000611:
		x, y := mainCityReturnSpawn()
		transition = func() { s.changeMapForTeamLeader(ch, 10004, x, y) }
	default:
		x, y := sceneSpawn(targetMap / 100)
		transition = func() {
			if privateTrial {
				partyStartMu.Lock()
				s.leaveTeamForSoloInstance(ch, 10004)
				s.changeMap(ch, targetMap, x, y)
				partyStartMu.Unlock()
			} else {
				s.changeMapForTeamLeader(ch, targetMap, x, y)
			}
		}
	}
	return resp, transition
}
