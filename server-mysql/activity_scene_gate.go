package main

import (
	"log"
	"sort"
	"time"
)

const activitySceneReadyTimeout = 10 * time.Second

// pendingActivityStart keeps battle presentation behind the asynchronous map
// load. The native 20050 handler immediately dereferences every team Unit, so
// every participant must finish scene startup and exchange EnterMap first.
type pendingActivityStart struct {
	id             uint64
	originID       int64
	participantIDs []int64
	mapSeqs        map[int64]uint64
	targetMap      int32
	activity       *activityBattle
	skipEnergy     bool
	armed          bool
}

// activitySceneState is shared by every participant after an interactive
// activity scene is ready and before its display monster is clicked. The
// native client keeps normal movement enabled during this interval.
type activitySceneState struct {
	originID       int64
	participantIDs []int64
	mapSeqs        map[int64]uint64
	targetMap      int32
	activity       *activityBattle
	skipEnergy     bool
}

func (s *Server) hasPendingActivityStart(playerID int64) bool {
	if s == nil || playerID <= 0 {
		return false
	}
	s.activityStartMu.Lock()
	defer s.activityStartMu.Unlock()
	for _, pending := range s.pendingActivities {
		if pendingActivityContains(pending, playerID) {
			return true
		}
	}
	return false
}

func pendingActivityContains(pending *pendingActivityStart, playerID int64) bool {
	if pending == nil {
		return false
	}
	for _, id := range pending.participantIDs {
		if id == playerID {
			return true
		}
	}
	return false
}

func (s *Server) queueActivityStage(origin *channel, participants []*channel, mapID int32, x, y float32, activity *activityBattle, skipEnergy bool) bool {
	if s == nil || origin == nil || origin.session == nil || activity == nil || mapID == 0 || len(participants) == 0 {
		return false
	}

	partyStartMu.Lock()
	partyLocked := true
	defer func() {
		if partyLocked {
			partyStartMu.Unlock()
		}
	}()

	ids := make([]int64, 0, len(participants))
	seen := make(map[int64]bool, len(participants))
	for _, member := range participants {
		if member == nil || member.session == nil || !s.isCurrentOnlineChannel(member) {
			return false
		}
		ss := member.session
		if seen[ss.playerID] || s.hasPendingActivityStart(ss.playerID) {
			return false
		}
		ss.battleMu.Lock()
		busy := ss.battle != nil
		ss.battleMu.Unlock()
		if busy {
			return false
		}
		seen[ss.playerID] = true
		ids = append(ids, ss.playerID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if copyRequiresSoloTeamExit(activity.copyID()) {
		s.leaveTeamForSoloInstance(origin, activity.copyID())
	}

	s.activityStartMu.Lock()
	if s.pendingActivities == nil {
		s.pendingActivities = make(map[uint64]*pendingActivityStart)
	}
	s.activityStartSeq++
	pending := &pendingActivityStart{
		id:             s.activityStartSeq,
		originID:       origin.session.playerID,
		participantIDs: ids,
		mapSeqs:        make(map[int64]uint64, len(ids)),
		targetMap:      mapID,
		activity:       activity.clone(),
		skipEnergy:     skipEnergy,
	}
	s.pendingActivities[pending.id] = pending
	s.activityStartMu.Unlock()
	time.AfterFunc(activitySceneReadyTimeout, func() {
		s.expirePendingActivityStart(pending.id)
	})

	// Move the captured participants directly. This also handles an activity
	// request from a non-leader without leaving the accepted party behind.
	for _, id := range ids {
		member := s.findChannelByPlayerID(id)
		if member == nil || member.session == nil {
			removed := s.removePendingActivityStart(pending.id)
			partyStartMu.Unlock()
			partyLocked = false
			if removed != nil {
				s.returnPendingActivityParticipants(pending, "participant disconnected during map change")
			}
			return false
		}
		seq := s.changeMapForActivity(member, mapID, x, y)
		s.activityStartMu.Lock()
		current := s.pendingActivities[pending.id]
		if current == pending {
			pending.mapSeqs[id] = seq
		}
		s.activityStartMu.Unlock()
		if current != pending {
			return false
		}
	}
	for _, id := range ids {
		member := s.findChannelByPlayerID(id)
		if !s.finishActivityMapStartup(member, pending.targetMap, pending.mapSeqs[id]) {
			removed := s.removePendingActivityStart(pending.id)
			partyStartMu.Unlock()
			partyLocked = false
			if removed != nil {
				s.returnPendingActivityParticipants(pending, "activity scene initialization failed")
			}
			return false
		}
	}

	s.activityStartMu.Lock()
	if s.pendingActivities[pending.id] != pending {
		s.activityStartMu.Unlock()
		return false
	}
	pending.armed = true
	s.activityStartMu.Unlock()

	partyStartMu.Unlock()
	partyLocked = false
	s.tryStartPendingActivity(origin)
	return true
}

// finishActivityMapStartup establishes the exact packet prerequisites for the
// native battle presenter without an artificial delay. Dedicated activity
// scenes have no SceneTransConfig rows, so sending StartupTransPoint for them
// would make InitScenePoint dereference a missing config on the client.
func (s *Server) finishActivityMapStartup(ch *channel, mapID int32, seq uint64) bool {
	if ch == nil || ch.session == nil {
		return false
	}
	s.mapTransitionMu.Lock()
	defer s.mapTransitionMu.Unlock()
	if !s.isCurrentOnlineChannel(ch) || ch.session.mapID != mapID || !ch.session.markMapReady(seq) {
		return false
	}
	s.syncReadyScenePlayers(ch)
	if !ch.session.markMapStartupComplete(seq) {
		return false
	}
	log.Printf("[S=%d] complete activity map startup immediately mapId=%d seq=%d", ch.id, mapID, seq)
	return true
}

func (s *Server) pendingActivityForPlayer(playerID int64) *pendingActivityStart {
	s.activityStartMu.Lock()
	defer s.activityStartMu.Unlock()
	for _, pending := range s.pendingActivities {
		if pending.armed && pendingActivityContains(pending, playerID) {
			return pending
		}
	}
	return nil
}

func (s *Server) tryStartPendingActivity(ready *channel) {
	if s == nil || ready == nil || ready.session == nil {
		return
	}
	pending := s.pendingActivityForPlayer(ready.session.playerID)
	if pending == nil {
		return
	}

	invalid := false
	allReady := true
	for _, id := range pending.participantIDs {
		member := s.findChannelByPlayerID(id)
		seq := pending.mapSeqs[id]
		if member == nil || member.session == nil || member.session.mapID != pending.targetMap ||
			!member.session.isCurrentMapChange(seq) {
			invalid = true
			break
		}
		if !member.session.isCurrentMapStartupComplete(seq) {
			allReady = false
		}
	}
	if invalid {
		if s.removePendingActivityStart(pending.id) != nil {
			s.returnPendingActivityParticipants(pending, "participant scene changed")
		}
		return
	}
	if !allReady {
		return
	}

	// Keep the reservation visible until the ordinary party-battle lock is
	// held. Otherwise another request can occupy a member after the gate is
	// removed but before this activity installs its battle state.
	configStateMu.RLock()
	partyStartMu.Lock()
	s.mapTransitionMu.Lock()
	if s.removePendingActivityStart(pending.id) == nil {
		s.mapTransitionMu.Unlock()
		partyStartMu.Unlock()
		configStateMu.RUnlock()
		return
	}
	reason := s.finishPendingActivityStartLocked(pending)
	s.mapTransitionMu.Unlock()
	partyStartMu.Unlock()
	configStateMu.RUnlock()
	if reason != "" {
		s.returnPendingActivityParticipants(pending, reason)
	}
}

func (s *Server) removePendingActivityStart(id uint64) *pendingActivityStart {
	s.activityStartMu.Lock()
	defer s.activityStartMu.Unlock()
	pending := s.pendingActivities[id]
	delete(s.pendingActivities, id)
	return pending
}

func (s *Server) expirePendingActivityStart(id uint64) {
	pending := s.removePendingActivityStart(id)
	if pending == nil {
		return
	}
	s.returnPendingActivityParticipants(pending, "scene startup timeout")
}

// finishPendingActivityStartLocked runs with configStateMu.RLock and
// partyStartMu held. It returns a cancellation reason after the pending
// reservation has been removed.
func (s *Server) finishPendingActivityStartLocked(pending *pendingActivityStart) string {
	origin := s.findChannelByPlayerID(pending.originID)
	if origin == nil || origin.session == nil {
		return "origin offline"
	}
	participants := s.configuredBattleParticipants(origin, pending.activity.copyID(), false)
	actualIDs := make([]int64, 0, len(participants))
	for _, member := range participants {
		if member != nil && member.session != nil {
			actualIDs = append(actualIDs, member.session.playerID)
		}
	}
	if !sameActivityParticipants(actualIDs, pending.participantIDs) {
		return "party changed during scene startup"
	}
	if _, _, _, _, interactive := activityFieldMonster(pending.activity); interactive {
		state := &activitySceneState{
			originID:       pending.originID,
			participantIDs: append([]int64(nil), pending.participantIDs...),
			mapSeqs:        make(map[int64]uint64, len(pending.mapSeqs)),
			targetMap:      pending.targetMap,
			activity:       pending.activity.clone(),
			skipEnergy:     pending.skipEnergy,
		}
		for playerID, seq := range pending.mapSeqs {
			state.mapSeqs[playerID] = seq
		}
		for _, playerID := range state.participantIDs {
			member := s.findChannelByPlayerID(playerID)
			if member == nil || member.session == nil {
				return "participant offline before activity monster spawn"
			}
			member.session.setActivityScene(state)
		}
		// OperaComponent.ClickTarget reads TeamComponent before raycasting and
		// has no nil guard. Rebuild the team only after every EnterMap packet so
		// the immediately-created activity monster is clickable without delay.
		for _, member := range participants {
			s.pushTeamMember(member)
		}
		for _, playerID := range state.participantIDs {
			member := s.findChannelByPlayerID(playerID)
			s.pushActivityFieldMonster(member, state)
			s.pushMapCoinMonster(member)
		}
		log.Printf("[S=%d] ready active scene %d %s difficulty=%d stage=%d participants=%d map=%d",
			origin.id, state.activity.ActiveID, state.activity.Method, state.activity.Difficulty,
			state.activity.Stage, len(state.participantIDs), state.targetMap)
		return ""
	}

	ids, counts, ok := activityRoster(pending.activity)
	if !ok {
		return "monster roster missing"
	}
	units := origin.session.buildMonsterUnitsFromRoster(ids, counts)
	if len(units) != len(ids) || tables.mainStory[1001] == nil {
		return "battle configuration missing"
	}
	presentation := battlePresentation{
		kind:       presentationMainStory,
		copyID:     pending.activity.copyID(),
		activity:   pending.activity,
		skipEnergy: pending.skipEnergy,
	}

	origin.session.battleMu.Lock()
	started, _ := s.finishStartBattleWithPresentation(origin, 1001, units, pending.targetMap, presentation)
	origin.session.battleMu.Unlock()
	if !started {
		return "battle start validation failed"
	}
	log.Printf("[S=%d] start ready active %d %s difficulty=%d stage=%d monsters=%d participants=%d",
		origin.id, pending.activity.ActiveID, pending.activity.Method, pending.activity.Difficulty,
		pending.activity.Stage, len(units), len(pending.participantIDs))
	return ""
}

func sameActivityParticipants(left, right []int64) bool {
	if len(left) != len(right) {
		return false
	}
	left = append([]int64(nil), left...)
	right = append([]int64(nil), right...)
	sort.Slice(left, func(i, j int) bool { return left[i] < left[j] })
	sort.Slice(right, func(i, j int) bool { return right[i] < right[j] })
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (s *Server) returnPendingActivityParticipants(pending *pendingActivityStart, reason string) {
	if pending == nil || pending.activity == nil {
		return
	}
	log.Printf("cancel active %d stage=%d participants=%v: %s", pending.activity.ActiveID,
		pending.activity.Stage, pending.participantIDs, reason)
	configStateMu.RLock()
	defer configStateMu.RUnlock()
	partyStartMu.Lock()
	defer partyStartMu.Unlock()
	for _, id := range pending.participantIDs {
		member := s.findChannelByPlayerID(id)
		if member == nil || member.session == nil || member.session.mapID != pending.targetMap ||
			!member.session.isCurrentMapChange(pending.mapSeqs[id]) {
			continue
		}
		member.session.battleMu.Lock()
		busy := member.session.battle != nil
		member.session.battleMu.Unlock()
		if !busy {
			s.changeMap(member, pending.activity.ReturnMap, pending.activity.ReturnX, pending.activity.ReturnY)
		}
	}
}
