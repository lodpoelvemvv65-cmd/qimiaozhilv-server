package main

import (
	"log"
	"time"
)

func (s *Server) waitMainStoryAIReady(ch *channel, epoch uint64, targetMap, pendingMap int32, seq uint64, timeout time.Duration, resume func()) {
	deadline := time.Now().Add(timeout)
	var readyAt time.Time
	var check func()
	check = func() {
		if s.closed.Load() || !s.isCurrentOnlineChannel(ch) || !s.mainStoryAILeader(ch) {
			return
		}
		ss := ch.session
		ss.battleMu.Lock()
		active := ss.mainStoryAIRunning && ss.mainStoryAIEpoch == epoch
		mapID, inBattle, alive := ss.mapID, ss.battle != nil, ss.battleHP() > 0
		ss.battleMu.Unlock()
		if !active || inBattle || !alive || (mapID != targetMap && mapID != pendingMap) {
			return
		}
		currentSeq, _ := ss.currentMapSceneVersion()
		if mapID == targetMap && seq != 0 && currentSeq != seq {
			return
		}
		ready := mapID == targetMap && currentSeq != 0 && s.mainStoryAIParticipantsReady(ch, currentSeq)
		if ready {
			if readyAt.IsZero() {
				readyAt = time.Now()
			}
			if time.Since(readyAt) >= mapTeamSnapshotDelay {
				resume()
				return
			}
		} else {
			readyAt = time.Time{}
		}
		if time.Now().After(deadline) {
			ss.battleMu.Lock()
			cancelled := ss.mainStoryAIRunning && ss.mainStoryAIEpoch == epoch
			if cancelled {
				ss.invalidateMainStoryAILocked()
			}
			ss.battleMu.Unlock()
			if cancelled {
				s.sendMainStoryAIStopTip(ch, "队伍换图等待超时，自动跑图已停止")
				log.Printf("[S=%d] main-story AI stopped: scene readiness timeout map=%d target=%d epoch=%d", ch.id, mapID, targetMap, epoch)
			}
			return
		}
		time.AfterFunc(mainStoryAITransitionDelay, check)
	}
	log.Printf("[S=%d] main-story AI waiting for scene target=%d pending=%d epoch=%d", ch.id, targetMap, pendingMap, epoch)
	time.AfterFunc(mainStoryAITransitionDelay, check)
}
