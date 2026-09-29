package main

import (
	"fmt"
	"log"
	"math"
	"time"

	"mhqserver/protocol"
)

// A short quiet period lets the victory/reward frames reach the client before
// the next ChangeMap. The scene startup and team snapshot delays are added
// separately before the next battle is created.
const mainStoryAITransitionDelay = 100 * time.Millisecond

// The native 20365 runner walks to the scene portal before the next map is
// loaded. The actual wait is derived from distance and move speed; this is only
// the lower bound used when the character is already beside the portal.
const mainStoryAIRunMinimumDelay = 100 * time.Millisecond

func mainStoryAIAutoTransitionDelay() time.Duration {
	return mainStoryAITransitionDelay
}

func (s *session) invalidateMainStoryAILocked() {
	if s == nil {
		return
	}
	s.mainStoryAIRunning = false
	s.mainStoryAIEpoch++
	// Keep autoBattleEnabled as the player's saved preference, but stop the
	// current worker immediately when the AI run is cancelled.
	s.autoBattle = false
	s.autoBattleEpoch++
	s.autoBattleNextCastAt = time.Time{}
}

func (s *session) stopMainStoryAI() {
	if s == nil {
		return
	}
	s.battleMu.Lock()
	s.invalidateMainStoryAILocked()
	s.battleMu.Unlock()
}

func (s *session) mainStoryAISnapshot() (bool, uint64) {
	if s == nil {
		return false, 0
	}
	s.battleMu.Lock()
	running, epoch := s.mainStoryAIRunning, s.mainStoryAIEpoch
	s.battleMu.Unlock()
	return running, epoch
}

func (s *Server) mainStoryAILeader(ch *channel) bool {
	if ch == nil || ch.session == nil {
		return false
	}
	teamMu.Lock()
	_, team := s.teamEntryOf(ch.session.playerID)
	leader := team == nil || team.LeaderId == ch.session.playerID
	teamMu.Unlock()
	return leader
}

// mainStoryAIRouteActive reports whether this player is the route owner or a
// follower currently controlled by that owner's 20365 run. Every client can
// emit C2M_RequestEnterMap when its character touches a portal, even though
// only the server-side route owner may advance the party.
func (s *Server) mainStoryAIRouteActive(ch *channel) bool {
	if s == nil || ch == nil || ch.session == nil {
		return false
	}
	if running, _ := ch.session.mainStoryAISnapshot(); running {
		return true
	}
	pid := ch.session.playerID
	teamMu.Lock()
	_, team := s.teamEntryOf(pid)
	leaderID := int64(0)
	if team != nil && team.LeaderId != pid {
		leaderID = team.LeaderId
	}
	teamMu.Unlock()
	if leaderID == 0 {
		return false
	}
	leader := s.findChannelByPlayerID(leaderID)
	if leader == nil || leader.session == nil {
		return false
	}
	running, _ := leader.session.mainStoryAISnapshot()
	return running
}

func (s *Server) mainStoryAIParticipantsReady(ch *channel, seq uint64) bool {
	if ch == nil || ch.session == nil || !ch.session.isCurrentMapStartupComplete(seq) {
		return false
	}
	teamMu.Lock()
	_, team := s.teamEntryOf(ch.session.playerID)
	var ids []int64
	if team != nil && team.LeaderId == ch.session.playerID {
		ids = append(ids, team.Members...)
	}
	teamMu.Unlock()
	for _, id := range ids {
		member := s.findChannelByPlayerID(id)
		if member == nil || member.session == nil || !sameSceneMapID(member.session.mapID, ch.session.mapID) {
			return false
		}
		memberSeq, ready := member.session.currentMapStartupVersion()
		if !ready || memberSeq == 0 {
			return false
		}
	}
	return true
}

func firstMainStoryStageForScene(sceneID int32) (mainStoryStage, bool) {
	for _, stage := range allMainStoryStages() {
		if stage.sceneID == sceneID {
			return stage, true
		}
	}
	return mainStoryStage{}, false
}

func (s *Server) sendMainStoryAIStopTip(ch *channel, message string) {
	if s == nil || ch == nil || ch.session == nil || message == "" {
		return
	}
	s.sendPush(ch, protocol.OpM2C_SendTip, &protocol.M2C_SendTip{
		Message: message,
		ActorId: ch.session.playerID,
	})
}

func (s *Server) mainStoryAIEnergyFailure(ch *channel, region int32) (*channel, string) {
	if s == nil || ch == nil || ch.session == nil {
		return nil, ""
	}
	cost := mainStoryEnergyCost(region)
	if cost <= 0 {
		return nil, ""
	}
	presentation := battlePresentation{kind: presentationMainStory}
	copyID := battleCopyConfig(region, 0, presentation)
	participants := s.battleParticipantsForPresentation(ch, copyID, presentation)
	leaderID := ch.session.playerID
	teamMu.Lock()
	if _, team := s.teamEntryOf(ch.session.playerID); team != nil {
		leaderID = team.LeaderId
	}
	teamMu.Unlock()
	for _, member := range participants {
		if member == nil || member.session == nil || member.session.energy >= cost {
			continue
		}
		role := "队员"
		if member.session.playerID == leaderID {
			role = "队长"
		}
		name := member.session.name
		if name == "" {
			name = fmt.Sprintf("玩家%d", member.session.playerID)
		}
		return member, fmt.Sprintf("%s%s体力不足，已停止自动跑图", role, name)
	}
	return nil, ""
}

func (s *Server) sendMainStoryAIStopTipToParty(ch *channel, region int32, message string) {
	if s == nil || ch == nil || ch.session == nil || message == "" {
		return
	}
	presentation := battlePresentation{kind: presentationMainStory}
	copyID := battleCopyConfig(region, 0, presentation)
	participants := s.battleParticipantsForPresentation(ch, copyID, presentation)
	for _, member := range participants {
		s.sendMainStoryAIStopTip(member, message)
	}
}

func (s *Server) stopMainStoryAIForEnergy(ch *channel, epoch uint64, region, nextMapID int32) bool {
	member, message := s.mainStoryAIEnergyFailure(ch, region)
	if member == nil || message == "" {
		return false
	}
	ss := ch.session
	ss.battleMu.Lock()
	if !ss.mainStoryAIRunning || ss.mainStoryAIEpoch != epoch {
		ss.battleMu.Unlock()
		return false
	}
	ss.invalidateMainStoryAILocked()
	ss.battleMu.Unlock()
	s.sendMainStoryAIStopTipToParty(ch, region, message)
	log.Printf("[S=%d] main-story AI stopped: player=%d energy=%d need=%d nextMap=%d message=%q",
		ch.id, member.session.playerID, member.session.energy, mainStoryEnergyCost(region), nextMapID, message)
	return true
}

// runMainStoryAIToPortal presents the movement that the native auto-runner
// performs between two layers. The old implementation changed maps directly,
// which skipped the walk animation and made auto-run visibly teleport.
func (s *Server) runMainStoryAIToPortal(ch *channel, epoch uint64, resume func()) {
	if s == nil || ch == nil || ch.session == nil || resume == nil {
		return
	}
	ss := ch.session
	ss.battleMu.Lock()
	if !ss.mainStoryAIRunning || ss.mainStoryAIEpoch != epoch || ss.battle != nil {
		ss.battleMu.Unlock()
		return
	}
	fromMap := ss.mapID
	startX, startY := ss.currentPosition(time.Now(), moveSpeed)
	ss.battleMu.Unlock()

	targetX, targetY := startX, startY
	sceneID := fromMap / 100
	if isMainStoryCityMap(fromMap) {
		sceneID = 10004
	}
	if points := sceneTransPos(sceneID); len(points) > 0 {
		// Field monsters stand well inside the portal. Walk to the portal edge
		// itself so the client visibly traverses the scene without entering the
		// trigger early and issuing a competing manual map request.
		targetX, targetY = points[0][0], points[0][1]
		if targetX > 0 {
			targetX -= 0.5
		} else if targetX < 0 {
			targetX += 0.5
		}
	}
	travelDelay := mainStoryAIRunMinimumDelay
	if distance := math.Hypot(float64(targetX-startX), float64(targetY-startY)); distance > 0.0001 {
		travelDelay = time.Duration(distance / float64(moveSpeed) * float64(time.Second))
		if travelDelay < mainStoryAIRunMinimumDelay {
			travelDelay = mainStoryAIRunMinimumDelay
		}
		now := time.Now()
		startX, startY = ss.beginMovement(now, targetX, targetY, moveSpeed)
		path := &protocol.M2C_PathfindingResult{
			Id: ss.playerID, X: startX, Y: startY, TX: targetX, TY: targetY,
			MoveSpeed: moveSpeed, ActorId: ss.playerID,
		}
		s.sendPush(ch, protocol.OpM2C_PathfindingResult, path)
		s.broadcastMove(ch, path)
		s.moveTeamFollowers(ch, path, now)
	}
	time.AfterFunc(travelDelay, func() {
		if s.closed.Load() || !s.isCurrentOnlineChannel(ch) {
			return
		}
		ss.battleMu.Lock()
		valid := ss.mainStoryAIRunning && ss.mainStoryAIEpoch == epoch && ss.mapID == fromMap && ss.battle == nil
		ss.battleMu.Unlock()
		if valid {
			resume()
		}
	})
}

// prepareMainStoryAIPortalRun starts the visible post-combat movement without
// reloading the current scene. EffectType=4 预制体由客户端按特效自身的 Time
// 销毁，线上不发同场景 ChangeMap，重载只会多出一次读条与闪屏。
func (s *Server) prepareMainStoryAIPortalRun(ch *channel, epoch uint64, mapID int32, resume func()) {
	if s == nil || ch == nil || ch.session == nil || resume == nil {
		return
	}
	ss := ch.session
	ss.battleMu.Lock()
	valid := ss.mainStoryAIRunning && ss.mainStoryAIEpoch == epoch && ss.mapID == mapID && ss.battle == nil
	ss.battleMu.Unlock()
	if !valid {
		return
	}
	resume()
}

func (s *Server) mainStoryAIBattleMembers(ch *channel) []*channel {
	if ch == nil || ch.session == nil {
		return nil
	}
	ch.session.battleMu.Lock()
	battle := ch.session.battle
	ch.session.battleMu.Unlock()
	if battle != nil && battle.party != nil {
		if members := battle.party.channels(s); len(members) > 0 {
			return members
		}
	}
	return []*channel{ch}
}

// armMainStoryAIAutoBattle starts an in-fight auto worker for every current
// participant. 20365 must not depend on, or persist, the separate 20069 switch.
func (s *Server) armMainStoryAIAutoBattle(ch *channel) {
	if s == nil || ch == nil || ch.session == nil {
		return
	}
	for _, member := range s.mainStoryAIBattleMembers(ch) {
		if member == nil || member.session == nil {
			continue
		}
		ss := member.session
		ss.battleMu.Lock()
		inFight := ss.battle != nil && !ss.battle.ended
		persisted := ss.autoBattleEnabled
		ss.battleMu.Unlock()
		if !inFight {
			continue
		}
		epoch := ss.setAutoBattle(true)
		go s.runAutoBattle(member, epoch)
		log.Printf("[S=%d] main-story AI auto armed player=%d persisted=%v", member.id, ss.playerID, persisted)
	}
}

// scheduleMainStoryAINext advances one layer inside the current chapter. It
// deliberately never crosses a chapter boundary; the final-layer settlement
// already returns the player (or party) to the city.
func (s *Server) scheduleMainStoryAINext(ch *channel) {
	if s == nil || ch == nil || ch.session == nil || ch.session.mapID/100 == 10006 || !s.mainStoryAILeader(ch) {
		return
	}
	running, aiEpoch := ch.session.mainStoryAISnapshot()
	if !running {
		return
	}
	currentMap := ch.session.mapID
	next, ok := nextMainStoryStageMap(currentMap)
	if !ok {
		return
	}
	// 目的地 ChangeMap 就是客户端原生的换场边界，这里不再叠加额外的等待，
	// 否则队员会先对着空场景等一段固定延迟才看到怪物。
	delay := mainStoryAIAutoTransitionDelay()
	time.AfterFunc(delay, func() {
		if s.closed.Load() || !s.isCurrentOnlineChannel(ch) {
			return
		}
		ss := ch.session
		ss.battleMu.Lock()
		valid := ss.mainStoryAIRunning && ss.mainStoryAIEpoch == aiEpoch &&
			ss.mapID == currentMap && ss.battle == nil && ss.battleHP() > 0
		ss.battleMu.Unlock()
		if !valid {
			return
		}
		if s.stopMainStoryAIForEnergy(ch, aiEpoch, next.region, next.mapID) {
			return
		}

		s.prepareMainStoryAIPortalRun(ch, aiEpoch, currentMap, func() {
			s.runMainStoryAIToPortal(ch, aiEpoch, func() {
				x, y := sceneSpawn(next.sceneID)
				if s.mainStoryAILeader(ch) {
					s.changeMapForTeamLeader(ch, next.mapID, x, y)
				} else {
					s.changeMap(ch, next.mapID, x, y)
				}
				seq, _ := ss.currentMapSceneVersion()
				if seq == 0 {
					return
				}
				// finishMapStartup runs at mapStartupDelay and the team snapshot follows
				// mapTeamSnapshotDelay. Start only after both are available to the native
				// battle presentation.
				s.waitMainStoryAIReady(ch, aiEpoch, next.mapID, next.mapID, seq, 30*time.Second, func() {
					if s.closed.Load() || !s.isCurrentOnlineChannel(ch) || ss.mapID != next.mapID ||
						!s.mainStoryAIParticipantsReady(ch, seq) {
						return
					}
					running, currentEpoch := ss.mainStoryAISnapshot()
					if !running || currentEpoch != aiEpoch {
						return
					}
					started, count, failureMessage := s.startMainStoryFightByRegionWithReason(ch, next.region)
					if !started {
						ss.stopMainStoryAI()
						s.sendMainStoryAIStopTip(ch, failureMessage)
						log.Printf("[S=%d] main-story AI stopped: next layer start failed map=%d region=%d",
							ch.id, next.mapID, next.region)
						return
					}
					// 20365 starts a temporary AI run and must not depend on the
					// separately persisted 20069 auto-battle preference. Re-arm every
					// participant explicitly for the newly created layer.
					s.armMainStoryAIAutoBattle(ch)
					log.Printf("[S=%d] main-story AI advanced map=%d region=%d monsters=%d",
						ch.id, next.mapID, next.region, count)
				})
			})
		})
	})
}

// scheduleMainStoryAIReturnToCity follows the same native route for the final
// layer: walk to that layer's exit portal, enter the city, wait for the city
// scene to finish loading, then continue from the city's right portal. This is
// kept leader-owned so a party changes scene together exactly once.
func (s *Server) scheduleMainStoryAIReturnToCity(ch *channel, completedMapID int32) {
	if s == nil || ch == nil || ch.session == nil || !s.mainStoryAILeader(ch) {
		return
	}
	running, epoch := ch.session.mainStoryAISnapshot()
	if !running {
		return
	}
	// Returning to town also immediately changes scene; the destination load
	// supplies the required combat-HUD cleanup boundary.
	wait := mainStoryAIAutoTransitionDelay()
	time.AfterFunc(wait, func() {
		if s.closed.Load() || !s.isCurrentOnlineChannel(ch) {
			return
		}
		ss := ch.session
		ss.battleMu.Lock()
		valid := ss.mainStoryAIRunning && ss.mainStoryAIEpoch == epoch &&
			ss.mapID == completedMapID && ss.battle == nil && ss.battleHP() > 0
		ss.battleMu.Unlock()
		if !valid {
			return
		}
		s.prepareMainStoryAIPortalRun(ch, epoch, completedMapID, func() {
			s.runMainStoryAIToPortal(ch, epoch, func() {
				x, y := mainCityReturnSpawn()
				s.changeMapForTeamLeader(ch, 10004, x, y)
				log.Printf("[S=%d] main-story final layer portal reached; entering city", ch.id)
				s.waitMainStoryAIReady(ch, epoch, 10004, completedMapID, 0, 30*time.Second, func() {
					if s.closed.Load() || !s.isCurrentOnlineChannel(ch) {
						return
					}
					s.scheduleMainStoryAIChapterRestart(ch, completedMapID)
				})
			})
		})
	})
}

// scheduleMainStoryAIChapterRestart preserves the native final-layer return to
// town, then starts the same chapter again at layer one. Beach remains on its
// separate tutorial/portal flow and never enters this loop.
func (s *Server) scheduleMainStoryAIChapterRestart(ch *channel, completedMapID int32) {
	if s == nil || ch == nil || ch.session == nil || !s.mainStoryAILeader(ch) {
		return
	}
	completed, _, ok := mainStoryStageForMap(completedMapID)
	if !ok || completed.sceneID == 10006 || !isMainStoryFinalStageMap(completedMapID) {
		return
	}
	first, ok := firstMainStoryStageForScene(completed.sceneID)
	if !ok {
		return
	}
	running, aiEpoch := ch.session.mainStoryAISnapshot()
	if !running {
		return
	}

	// emitVictory has already moved every participant to town. Wait until that
	// scene and its team snapshot are complete before beginning the next loop.
	s.waitMainStoryAIReady(ch, aiEpoch, 10004, completedMapID, 0, 30*time.Second, func() {
		if s.closed.Load() || !s.isCurrentOnlineChannel(ch) {
			return
		}
		ss := ch.session
		citySeq, cityReady := ss.currentMapStartupVersion()
		ss.battleMu.Lock()
		valid := ss.mainStoryAIRunning && ss.mainStoryAIEpoch == aiEpoch &&
			isMainStoryCityMap(ss.mapID) && ss.battle == nil && ss.battleHP() > 0 &&
			cityReady && citySeq != 0
		ss.battleMu.Unlock()
		if !valid || !s.mainStoryAIParticipantsReady(ch, citySeq) {
			return
		}
		if s.stopMainStoryAIForEnergy(ch, aiEpoch, first.region, first.mapID) {
			return
		}

		s.runMainStoryAIToPortal(ch, aiEpoch, func() {
			x, y := sceneSpawn(first.sceneID)
			s.changeMapForTeamLeader(ch, first.mapID, x, y)
			seq, _ := ss.currentMapSceneVersion()
			if seq == 0 {
				return
			}
			s.waitMainStoryAIReady(ch, aiEpoch, first.mapID, first.mapID, seq, 30*time.Second, func() {
				if s.closed.Load() || !s.isCurrentOnlineChannel(ch) || ss.mapID != first.mapID ||
					!s.mainStoryAIParticipantsReady(ch, seq) {
					return
				}
				running, currentEpoch := ss.mainStoryAISnapshot()
				if !running || currentEpoch != aiEpoch {
					return
				}
				started, count, failureMessage := s.startMainStoryFightByRegionWithReason(ch, first.region)
				if !started {
					ss.stopMainStoryAI()
					s.sendMainStoryAIStopTip(ch, failureMessage)
					log.Printf("[S=%d] main-story AI stopped: chapter restart failed map=%d region=%d",
						ch.id, first.mapID, first.region)
					return
				}
				s.armMainStoryAIAutoBattle(ch)
				log.Printf("[S=%d] main-story AI chapter restarted scene=%d map=%d region=%d monsters=%d",
					ch.id, first.sceneID, first.mapID, first.region, count)
			})
		})
	})
}
