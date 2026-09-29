package main

import (
	"log"
	"time"

	"mhqserver/protocol"
)

const forceOfflineGracePeriod = 2 * time.Minute

// cleanupPlayerSession is shared by normal disconnects and duplicate-login
// replacement. offlineRun prevents the replaced connection's deferred close
// from saving stale state over the new session a second time.
func (s *Server) cleanupPlayerSession(ch *channel) {
	if ch == nil || ch.session == nil || ch.session.playerID == 0 ||
		!ch.session.offlineRun.CompareAndSwap(false, true) {
		return
	}
	ss := ch.session
	playerID := ss.playerID
	if ss.state == sessInGame {
		checkpointOnlineReward(ch, time.Now())
		ss.currentPosition(time.Now(), moveSpeed)
		s.cancelPersonalPVPMatch(ss)
		s.cancelTradeForPlayer(playerID, "对方已离线，交易已取消")
		battle := s.leaveBattleOnClose(ch)
		// Rebuild the remaining combat team before removing this player's Unit.
		// Native battle handlers dereference TeamComponent.LeaderId immediately.
		s.leaveTeamOnClose(playerID)
		s.broadcastOffline(ch)
		restoreBattleReturnOnDisconnect(ss, battle)
		if s.store != nil {
			s.saveData(ch)
			s.store.SetPlayerOnline(playerID, false)
		}
	} else {
		// A replacement can close after LoginGate but before EnterGame. It has no
		// gameplay state to save, but any transferred/stale in-memory team entry
		// must still be removed.
		s.leaveTeamOnClose(playerID)
	}
	log.Printf("[S=%d] player %d offline superseded=%v", ch.id, playerID, ss.superseded.Load())
}

func restoreBattleReturnOnDisconnect(ss *session, battle *battleState) {
	if ss == nil || battle == nil {
		return
	}
	if battle.activity != nil && battle.activity.ReturnMap > 0 {
		ss.mapID = battle.activity.ReturnMap
		ss.resetMovement(battle.activity.ReturnX, battle.activity.ReturnY)
		return
	}
	if battle.battleType == trialBattleType || isManualEquipBattle(battle) || battle.mapID < 0 {
		x, y := mainCityReturnSpawn()
		ss.mapID = 10004
		ss.resetMovement(x, y)
	}
}

// replacePlayerSession makes the most recent successful LoginGate authoritative.
// The old TCP connection stays alive briefly so its built-in confirmation dialog
// can be clicked; it is excluded from routing immediately and accepts only pings.
func (s *Server) replacePlayerSession(current *channel, playerID int64) {
	if current == nil || current.session == nil || playerID <= 0 {
		return
	}
	s.loginMu.Lock()
	defer s.loginMu.Unlock()

	s.mu.RLock()
	duplicates := make([]*channel, 0, 1)
	for _, candidate := range s.conns {
		if candidate != current && candidate != nil && candidate.session != nil &&
			candidate.session.playerID == playerID && !candidate.session.superseded.Load() {
			duplicates = append(duplicates, candidate)
		}
	}
	s.mu.RUnlock()

	for _, old := range duplicates {
		if !old.session.superseded.CompareAndSwap(false, true) {
			continue
		}
		s.sendPush(old, protocol.OpG2C_ForceOffLine, &protocol.G2C_ForceOffLine{})
		s.cleanupPlayerSession(old)
		log.Printf("[S=%d] force offline player=%d replaced-by=%d", old.id, playerID, current.id)
		time.AfterFunc(forceOfflineGracePeriod, func() { s.removeChannel(old) })
	}
}
