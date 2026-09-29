package main

const teamMemberReturnBlockedMessage = "组队中只有队长可以带队回城，请先退出队伍"

// returnPartyToMainCity handles only player-initiated returns (Ctrl+G and
// return scrolls). A follower cannot split the party; the leader first ends
// combat for every online member and then moves that same group to town.
func (s *Server) returnPartyToMainCity(ch *channel, reason string, forceReload, grantProgress bool) (bool, string) {
	if ch == nil || ch.session == nil || ch.session.playerID <= 0 {
		return false, "请先登录"
	}

	pid := ch.session.playerID
	teamMu.Lock()
	_, team := s.teamEntryOf(pid)
	if team != nil && team.LeaderId != pid {
		teamMu.Unlock()
		return false, teamMemberReturnBlockedMessage
	}
	memberIDs := []int64{pid}
	if team != nil {
		memberIDs = append([]int64(nil), team.Members...)
	}
	teamMu.Unlock()

	members := make([]*channel, 0, len(memberIDs))
	for _, memberID := range memberIDs {
		if memberID == pid {
			members = append(members, ch)
			continue
		}
		if member := s.findChannelByPlayerID(memberID); member != nil {
			members = append(members, member)
		}
	}

	// A party defeat/victory normally schedules its own asynchronous scene
	// transition. This explicit return owns the destination, so mark each shared
	// fight settled before detaching anyone and prevent a second map change.
	for _, member := range members {
		member.session.battleMu.Lock()
		battle := member.session.battle
		if battle != nil && battle.party != nil {
			battle.party.mu.Lock()
			battle.party.settled = true
			battle.party.mu.Unlock()
		}
		member.session.battleMu.Unlock()
	}

	exits := make(map[int64]battleExitResult, len(members))
	for _, member := range members {
		exits[member.session.playerID] = s.clearBattleForTransition(member, reason)
	}

	x, y := mainCityReturnSpawn()
	if team == nil {
		exit := exits[pid]
		inMainCity := ch.session.mapID == 10004 || ch.session.mapID == 1000401
		if forceReload || !inMainCity || exit.hadFightState {
			if grantProgress {
				s.grantBeachReturnProgress(ch)
			}
			s.changeMap(ch, 10004, x, y)
		}
		return true, ""
	}

	for _, member := range members {
		if grantProgress {
			s.grantBeachReturnProgress(member)
		}
	}
	s.changeMapForParty(members, 10004, x, y)
	return true, ""
}
