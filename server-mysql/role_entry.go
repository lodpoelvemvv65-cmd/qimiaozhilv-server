package main

import (
	"log"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// onEnterGame handles 20027 -> 20028 and writes its response plus ordered
// scene-start pushes directly to the channel.
func (s *Server) onEnterGame(ch *channel, req *protocol.C2G_EnterGame) proto.Message {
	resp := &protocol.G2C_EnterGame{RpcId: req.RpcId}

	if ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	p, err := s.store.FirstPlayer(ch.session.accountID)
	if err != nil {
		log.Printf("[S=%d] FirstPlayer err=%v", ch.id, err)
		resp.Error, resp.Message = errBadParam, "角色不存在"
		return resp
	}
	if p.ID != ch.session.playerID {
		log.Printf("[S=%d] session player mismatch reserved=%d stored=%d", ch.id, ch.session.playerID, p.ID)
		resp.Error, resp.Message = errBadParam, "角色标识失效，请重新登录"
		return resp
	}

	s.store.SetPlayerLastLogin(p.ID)
	s.store.SetPlayerOnline(p.ID, true)

	dataMigrated := ch.session.loadData(p)
	dailyRunGranted := ch.session.ensureDailyNormalRunAllowance(time.Now())
	if dataMigrated || dailyRunGranted {
		// Persist the migration before returning character data so reconnecting
		// cannot recreate a negative trans-stage level.
		s.saveData(ch)
	}
	if normalizeActivityLoginMap(ch.session) {
		s.saveData(ch)
	}
	ch.session.startOnlineRewardClock(time.Now())
	s.autoAcceptInitialTasks(ch)
	firstEntry := ch.session.initialTaskUIPending

	resp.Id = p.ID
	resp.SkinId = ch.session.skinID
	resp.JobId = ch.session.jobID
	resp.IsOnline = true
	if firstEntry {
		// IsOnline=true makes EnterGameFinish read MyUnit before ChangeMap has
		// created it. The native first-entry response omits both fields.
		resp.SkinId = 0
		resp.IsOnline = false
	}

	out, err := proto.Marshal(resp)
	if err != nil {
		log.Printf("[S=%d] marshal enter game resp err=%v", ch.id, err)
		return nil
	}
	s.SendToChannel(ch, packOuter(protocol.OpG2C_EnterGame, out))
	log.Printf("[S=%d] resp opcode=%d len=%d", ch.id, protocol.OpG2C_EnterGame, len(out))

	// SendUnitInfo must precede ChangeMap. ChangeMapEvent retrieves the local
	// UnitCharacter by GlobalVariable.MyId when creating MyUnit.
	su := &protocol.M2C_SendUnitInfo{
		UnitCharacter: buildUnitCharacter(ch.session),
		ActorId:       p.ID,
	}
	suOut, err := proto.Marshal(su)
	if err != nil {
		log.Printf("[S=%d] marshal M2C_SendUnitInfo err=%v", ch.id, err)
		return nil
	}
	s.SendToChannel(ch, packOuter(protocol.OpM2C_SendUnitInfo, suOut))
	log.Printf("[S=%d] push M2C_SendUnitInfo id=%d name=%q job=%d skin=%d",
		ch.id, su.UnitCharacter.Id, su.UnitCharacter.NickName, su.UnitCharacter.JobId, su.UnitCharacter.SkinId)

	mapID := ch.session.mapID
	cm := &protocol.M2C_ChangeMap{
		MapId:   wireMapID(mapID),
		X:       ch.session.x,
		Y:       ch.session.y,
		ActorId: p.ID,
	}
	if firstEntry {
		mapID = 1000601
		cm.MapId = wireMapID(mapID)
		cm.X, cm.Y = 0, 0
		ch.session.mapID = mapID
	} else if mapID <= 0 {
		mapID = 1000601
		cm.MapId = wireMapID(mapID)
		cm.X, cm.Y = -1.8, -0.84
		ch.session.mapID = mapID
	}
	ch.session.resetMovement(cm.X, cm.Y)
	mapSeq := ch.session.markMapChange()
	// New roles follow the captured 20036 -> 20033 -> 20170 -> 20047 order.
	s.pushLoginMapSnapshot(ch, cm, firstEntry)
	ch.session.state = sessInGame
	log.Printf("[S=%d] push M2C_ChangeMap mapId=%d pos=(%.2f,%.2f)", ch.id, cm.MapId, cm.X, cm.Y)

	s.broadcastUnitsInMapExcept(ch)
	s.scheduleLoginMapStartup(ch, mapID, mapSeq, firstEntry)

	// Existing roles used to receive SyncPet/ActiveInfo immediately after
	// ChangeMap. The client handler dereferences MyUnit, which is not created
	// until the delayed map-start callback completes; on a busy login this
	// produced M2C_SyncPetHandler NullReferenceException and a frozen HUD.
	// Send this component-dependent state only after that startup barrier.
	if !firstEntry {
		time.AfterFunc(mapStartupDelay+50*time.Millisecond, func() {
			if s.isCurrentOnlineChannel(ch) && ch.session.isCurrentMapStartupComplete(mapSeq) {
				s.pushSyncPet(ch)
				s.pushActiveInfo(ch)
			}
		})
	}
	log.Printf("[S=%d] push numeric attrs level=%d charPoint=%d", ch.id, p.Level, ch.session.charPoint)

	s.tryApplyLauncherTeamPlans(time.Now())
	log.Printf("[S=%d] enter game ok playerID=%d", ch.id, p.ID)
	return nil
}
