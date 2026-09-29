package main

import (
	"log"
	"sort"
	"sync"
	"time"

	"mhqserver/protocol"
)

// Client StartMainStoryFightEventClass and StartBossFightEvent use this exact
// five-entry array: multiplier = 1 + hpDifficulty[memberCount-1].
var clientTeamHPDifficulty = [...]float64{0, 0.5, 0.96, 1.37, 1.72}

// partyStartMu gives every battle entry point one lock order. Without it two
// team members can each hold their session lock while trying to enroll the
// other member in the same battle.
var partyStartMu sync.Mutex

type partyBattle struct {
	mu             sync.Mutex
	targetMu       sync.Mutex
	members        map[int64]*battleState
	memberIDs      []int64
	settled        bool
	aborted        bool
	bossOnce       sync.Once
	monsterReadyAt time.Time
	// monsterTargetCursor makes single-target monster attacks deterministic
	// across the living party and carries the rotation across waves.
	monsterTargetCursor int
}

func (p *partyBattle) nextMonsterTarget(battle *battleState) CombatUnitRef {
	if p == nil || battle == nil || battle.runtime == nil || len(p.memberIDs) == 0 {
		return CombatUnitRef{}
	}
	p.targetMu.Lock()
	defer p.targetMu.Unlock()
	for checked := 0; checked < len(p.memberIDs); checked++ {
		index := p.monsterTargetCursor % len(p.memberIDs)
		p.monsterTargetCursor = (index + 1) % len(p.memberIDs)
		id := p.memberIDs[index]
		ref := PlayerCombatUnit(id)
		if battle.runtime.CanBeTargeted(ref) {
			return ref
		}
	}
	return CombatUnitRef{}
}

// shareCombatState combines the passives installed while each member battle
// was being created and then makes every member runtime use one canonical
// ephemeral state. The wrappers remain distinct so Runtime.Player still
// resolves to the member who is currently casting.
func (p *partyBattle) shareCombatState() {
	if p == nil || len(p.memberIDs) == 0 {
		return
	}
	var canonical *CombatRuntime
	sharedMeta := make(map[string]battleEffectMetadata)
	sharedHooks := make(map[string]*activeSkillModifier)
	for _, playerID := range p.memberIDs {
		battle := p.members[playerID]
		if battle == nil || battle.runtime == nil {
			continue
		}
		if canonical == nil {
			canonical = battle.runtime
		} else {
			local := battle.runtime.state()
			for _, effect := range local.effects {
				canonical.nextID++
				effect.id = canonical.nextID
				canonical.effects = append(canonical.effects, effect)
			}
			if local.eventSeq > canonical.eventSeq {
				canonical.eventSeq = local.eventSeq
			}
			battle.runtime.shared = canonical
		}
		for key, meta := range battle.effectMeta {
			sharedMeta[key] = meta
		}
		for _, hook := range battle.modifierHooks {
			if hook != nil {
				sharedHooks[modifierHookStorageKey(hook.key, hook.holder)] = hook
			}
		}
	}
	if canonical == nil {
		return
	}
	for _, playerID := range p.memberIDs {
		battle := p.members[playerID]
		if battle == nil || battle.runtime == nil {
			continue
		}
		if battle.runtime != canonical {
			battle.runtime.shared = canonical
		}
		battle.effectMeta = sharedMeta
		battle.modifierHooks = sharedHooks
	}
}

func (p *partyBattle) channels(s *Server) []*channel {
	if p == nil || s == nil {
		return nil
	}
	out := make([]*channel, 0, len(p.memberIDs))
	for _, playerID := range p.memberIDs {
		if ch := s.findChannelByPlayerID(playerID); ch != nil && ch.session != nil {
			out = append(out, ch)
		}
	}
	return out
}

func (p *partyBattle) settlementSnapshot() ([]int64, map[int64]*battleState) {
	if p == nil {
		return nil, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	ids := append([]int64(nil), p.memberIDs...)
	battles := make(map[int64]*battleState, len(ids))
	for _, id := range ids {
		battles[id] = p.members[id]
	}
	return ids, battles
}

func (s *Server) battleRecipients(origin *channel, battle *battleState) []*channel {
	if battle != nil && battle.pvp != nil {
		return battle.pvp.channels(s)
	}
	if battle == nil || battle.party == nil {
		if origin == nil {
			return nil
		}
		return []*channel{origin}
	}
	channels := battle.party.channels(s)
	if len(channels) == 0 && origin != nil {
		return []*channel{origin}
	}
	return channels
}

func (s *Server) configuredBattleParticipants(origin *channel, copyID int64, allowTrialTeam bool) []*channel {
	if origin == nil || origin.session == nil {
		return nil
	}
	row := tables.copyConfig[copyID]
	if row == nil || (!copySupportsTeam(copyID, row) && !allowTrialTeam) {
		return []*channel{origin}
	}

	teamMu.Lock()
	teamID := origin.session.teamID
	t := teams[teamID]
	var ids []int64
	if t != nil {
		ids = append(ids, t.Members...)
	}
	teamMu.Unlock()
	if len(ids) == 0 {
		return []*channel{origin}
	}

	seen := make(map[int64]bool, len(ids))
	participants := make([]*channel, 0, len(ids))
	for _, id := range ids {
		member := s.findChannelByPlayerID(id)
		if member == nil || member.session == nil || seen[id] {
			continue
		}
		// The client can only arrange units that already exist in this scene.
		// 10004 and 1000401 are two wire ids for the same city scene.
		if !sameSceneMapID(member.session.mapID, origin.session.mapID) {
			continue
		}
		seen[id] = true
		participants = append(participants, member)
	}
	if !seen[origin.session.playerID] {
		participants = append(participants, origin)
	}
	sort.Slice(participants, func(i, j int) bool {
		return participants[i].session.playerID < participants[j].session.playerID
	})
	return participants
}

func boolOf(value interface{}) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1"
	default:
		return num(value) != 0
	}
}

func scaleMonsterHPForTeam(units []*monsterUnit, memberCount int) {
	if memberCount < 1 {
		memberCount = 1
	}
	if memberCount > len(clientTeamHPDifficulty) {
		memberCount = len(clientTeamHPDifficulty)
	}
	multiplier := 1 + clientTeamHPDifficulty[memberCount-1]
	for _, unit := range units {
		if unit == nil {
			continue
		}
		unit.maxHP = int32(float64(unit.maxHP)*multiplier + 0.5)
		unit.hp = unit.maxHP
	}
}

// preparePartyBattleClients repairs the client-side prerequisites that every
// native battle presentation immediately dereferences. StartMainStoryFight
// and StartBossFight both call TeamComponent.Get(LeaderId).GetAll(), then read
// every member Unit/NumericComponent before arranging the formation. TCP
// ordering guarantees these snapshots are handled before the following battle
// presentation packet.
func (s *Server) preparePartyBattleClients(participants []*channel, preferredLeader int64) {
	if len(participants) < 2 {
		return
	}
	ids := make([]int64, 0, len(participants))
	present := make(map[int64]bool, len(participants))
	for _, member := range participants {
		if member == nil || member.session == nil || member.session.playerID == 0 {
			continue
		}
		id := member.session.playerID
		ids = append(ids, id)
		present[id] = true
	}
	if len(ids) < 2 {
		return
	}
	leader := preferredLeader
	teamMu.Lock()
	if _, team := s.teamEntryOf(preferredLeader); team != nil && present[team.LeaderId] {
		leader = team.LeaderId
	}
	teamMu.Unlock()
	if !present[leader] {
		leader = ids[0]
	}
	for _, recipient := range participants {
		if recipient == nil || recipient.session == nil {
			continue
		}
		// Rebuild TeamComponent only after all same-map Unit creation packets.
		s.sendTeamMember(recipient, leader, ids)
		for _, owner := range participants {
			if owner == nil || owner.session == nil {
				continue
			}
			ss := owner.session
			s.pushBattlePlayerAttrsListTo(recipient, ss, ss.battleHP(), ss.battleMP())
		}
		s.pushPartyLevelRefresh(recipient, participants)
	}
}

// pushPartyLevelRefresh retriggers the native level watcher after
// TeamComponent rebuilds its member heads. The watcher reads Trans while
// processing 1026, so send the authoritative trans first, then a distinct
// temporary level and the authoritative cumulative level. These are display
// nudges only and never alter the session or persisted character state.
func (s *Server) pushPartyLevelRefresh(recipient *channel, participants []*channel) {
	if recipient == nil || recipient.session == nil {
		return
	}
	for _, owner := range participants {
		if owner == nil || owner.session == nil || owner.session.playerID == 0 {
			continue
		}
		if owner != recipient && !s.scenePlayerVisibleTo(recipient, owner) {
			continue
		}
		ss := owner.session
		s.sendPush(recipient, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: ss.playerID, NumericType: 1029, Value: float32(ss.trans), ActorId: recipient.session.playerID,
		})
		temporary := ss.level - 1
		if temporary < 0 || temporary == ss.level {
			temporary = ss.level + 1
		}
		s.sendPush(recipient, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: ss.playerID, NumericType: 1026, Value: float32(temporary), ActorId: recipient.session.playerID,
		})
		s.sendPush(recipient, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: ss.playerID, NumericType: 1026, Value: float32(ss.level), ActorId: recipient.session.playerID,
		})
	}
}

func (s *Server) onSelectTeamMember(ch *channel, req *protocol.C2M_SelectTeamMember) *protocol.M2C_SelectTeamMember {
	resp := &protocol.M2C_SelectTeamMember{RpcId: req.RpcId}
	if ch == nil || ch.session == nil {
		return resp
	}
	ss := ch.session
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	battle := ss.battle
	if battle == nil || battle.ended {
		return resp
	}
	if battle.party != nil {
		battle.party.mu.Lock()
		defer battle.party.mu.Unlock()
		member := battle.party.members[req.Id]
		if member == nil || member.playerHP <= 0 || member.ended {
			return resp
		}
	} else if req.Id != ss.playerID {
		return resp
	}
	battle.selectedAllyID = req.Id
	log.Printf("[S=%d] select team member id=%d", ch.id, req.Id)
	return resp
}

func (s *Server) pushPartyBattleResources(participants []*channel) {
	for _, owner := range participants {
		if owner == nil || owner.session == nil || owner.session.battle == nil {
			continue
		}
		battle := owner.session.battle
		for _, recipient := range participants {
			if recipient == nil || recipient.session == nil {
				continue
			}
			for _, attr := range []struct {
				numericType int32
				value       int32
			}{
				{1002, battle.playerMaxHP},
				{1001, battle.playerHP},
				{1004, battle.playerMaxMP},
				{1003, battle.playerMP},
			} {
				s.sendPush(recipient, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
					UnitId: owner.session.playerID, NumericType: attr.numericType,
					Value: float32(attr.value), ActorId: recipient.session.playerID,
				})
			}
		}
	}
}

// detachCurrentBattleLocked removes one session from its current fight.
// Callers hold that session's battleMu. A party fight continues for the
// remaining members; if everybody still enrolled is already down, it is
// settled as a defeat immediately.
func (s *Server) detachCurrentBattleLocked(ch *channel, reason string) *battleState {
	if ch == nil || ch.session == nil {
		return nil
	}
	ss := ch.session
	battle := ss.battle
	if battle == nil {
		return nil
	}
	if battle.pvp != nil {
		duel := battle.pvp
		duel.mu.Lock()
		winnerID := int64(0)
		for _, id := range duel.memberIDs {
			if id != ss.playerID {
				winnerID = id
				break
			}
		}
		s.finishPVPBattleLocked(duel, winnerID, reason)
		duel.mu.Unlock()
		return battle
	}
	persistFamilyBossProgress(ss.familyID, battle)
	syncBattleHealthToSession(ss)
	battle.ended = true
	party := battle.party
	if party == nil {
		if battle.runtime != nil {
			s.emitCombatEvents(ch, battle, battle.runtime.Cancel())
		}
		ss.battle = nil
		return battle
	}

	party.mu.Lock()
	var events []CombatEvent
	if battle.runtime != nil {
		events = append(events, battle.runtime.clearUnitEffects(
			PlayerCombatUnit(ss.playerID), reason, battle.runtime.clock.Now())...)
	}
	if len(events) > 0 {
		s.emitCombatEvents(ch, battle, events)
	}
	delete(party.members, ss.playerID)
	memberIDs := party.memberIDs[:0]
	for _, playerID := range party.memberIDs {
		if playerID != ss.playerID {
			memberIDs = append(memberIDs, playerID)
		}
	}
	party.memberIDs = memberIDs
	for _, member := range party.members {
		if member.selectedAllyID == ss.playerID {
			member.selectedAllyID = 0
		}
	}
	for key, hook := range battle.modifierHooks {
		if hook != nil && hook.holder == PlayerCombatUnit(ss.playerID) {
			delete(battle.modifierHooks, key)
		}
	}

	settleDefeat := false
	if len(party.members) == 0 {
		party.settled = true
		if battle.runtime != nil {
			battle.runtime.Cancel()
		}
	} else if !party.settled {
		settleDefeat = true
		for _, member := range party.members {
			if member.playerHP > 0 {
				settleDefeat = false
				break
			}
		}
		if settleDefeat {
			party.settled = true
			for _, member := range party.members {
				member.playerHP = 0
				member.playerDefeated = true
				member.ended = true
				if member.owner != nil {
					member.owner.invalidateMainStoryAILocked()
				}
			}
		}
	}
	party.mu.Unlock()
	ss.battle = nil
	if settleDefeat {
		go s.finishPartyDefeat(party)
	}
	log.Printf("[S=%d] player %d detached from party battle reason=%s remaining=%d",
		ch.id, ss.playerID, reason, len(memberIDs))
	return battle
}

func (s *Server) leaveBattleOnClose(ch *channel) *battleState {
	if ch == nil || ch.session == nil {
		return nil
	}
	ss := ch.session
	ss.battleMu.Lock()
	battle := s.detachCurrentBattleLocked(ch, "disconnect")
	ss.idleBattle = false
	ss.invalidateMainStoryAILocked()
	ss.battleMu.Unlock()
	s.refreshPartyAfterDisconnect(battle)
	return battle
}

// refreshPartyAfterDisconnect updates the combat-only member list immediately.
// The authoritative team snapshot follows from leaveTeamOnClose, but this first
// snapshot also covers a battle whose team entry was already missing.
func (s *Server) refreshPartyAfterDisconnect(battle *battleState) {
	if battle == nil || battle.party == nil {
		return
	}
	ids, _ := battle.party.settlementSnapshot()
	participants := make([]*channel, 0, len(ids))
	for _, id := range ids {
		if member := s.findChannelByPlayerID(id); member != nil && member.session != nil {
			participants = append(participants, member)
		}
	}
	if len(participants) == 1 {
		member := participants[0]
		s.sendTeamMember(member, member.session.playerID, []int64{member.session.playerID})
		return
	}
	if len(participants) > 1 {
		s.preparePartyBattleClients(participants, participants[0].session.playerID)
	}
}

// settlePartyCombatLocked is called while the acting member's battleMu and
// party.mu are held. A downed member stops acting, but the shared fight ends
// only when every member is down or the shared monster roster is dead.
func (s *Server) settlePartyCombatLocked(ch *channel, battle *battleState) bool {
	party := battle.party
	if party == nil || party.settled {
		return true
	}
	if battle.playerHP <= 0 && !battle.playerDefeated {
		battle.playerHP = 0
		battle.playerDefeated = true
		if battle.owner != nil {
			battle.owner.invalidateMainStoryAILocked()
			logCombatHP(battle.owner, "party-down", "member defeated")
		}
	}

	allDefeated := true
	for _, member := range party.members {
		if member.playerHP > 0 {
			allDefeated = false
			break
		}
	}
	if allDefeated {
		party.settled = true
		for _, member := range party.members {
			member.playerHP = 0
			member.playerDefeated = true
			member.ended = true
			if member.owner != nil {
				member.owner.invalidateMainStoryAILocked()
			}
		}
		go s.finishPartyDefeat(party)
		return true
	}

	if len(battle.aliveMonsters()) == 0 {
		party.settled = true
		for _, member := range party.members {
			member.ended = true
			if member.owner != nil {
				member.owner.autoBattleEpoch++
			}
		}
		delay := battleVictoryDelay
		if delay < 0 {
			delay = 0
		}
		log.Printf("[S=%d] team monsters dead; victory in %s members=%d", ch.id, delay, len(party.members))
		time.AfterFunc(delay, func() { s.finishPartyVictory(party) })
		return true
	}
	return battle.playerDefeated
}

func (s *Server) finishPartyVictory(party *partyBattle) {
	configStateMu.RLock()
	defer configStateMu.RUnlock()
	if party == nil {
		return
	}
	party.mu.Lock()
	aborted := party.aborted
	party.mu.Unlock()
	if aborted {
		return
	}
	memberIDs, battles := party.settlementSnapshot()
	var mainStoryAILeader *channel
	var mainStoryAICompletedMap int32
	// Decide ownership before settling any member. Followers do not carry the
	// leader's mainStoryAIRunning flag, so discovering this during emitVictory
	// would let early followers return to town before the leader starts moving.
	for _, playerID := range memberIDs {
		ch := s.findChannelByPlayerID(playerID)
		if ch == nil || ch.session == nil {
			continue
		}
		ss := ch.session
		ss.battleMu.Lock()
		battle := battles[playerID]
		eligible := battle != nil && ss.battle == battle && battle.ended &&
			battle.activity == nil && (battle.copyID == 10001 || battle.copyID == 10003) &&
			ss.mainStoryAIRunning
		completedMapID := ss.mapID
		ss.battleMu.Unlock()
		if eligible && s.mainStoryAILeader(ch) {
			mainStoryAILeader = ch
			if isMainStoryFinalStageMap(completedMapID) {
				mainStoryAICompletedMap = completedMapID
			}
			break
		}
	}
	for _, playerID := range memberIDs {
		ch := s.findChannelByPlayerID(playerID)
		if ch == nil || ch.session == nil {
			continue
		}
		ss := ch.session
		ss.battleMu.Lock()
		battle := battles[playerID]
		if battle != nil && ss.battle == battle && battle.ended {
			if mainStoryAICompletedMap != 0 && battle.activity == nil &&
				ss.mapID == mainStoryAICompletedMap && isMainStoryFinalStageMap(ss.mapID) {
				battle.partyFinalReturnPending = true
			}
			if battle.runtime != nil {
				s.emitCombatEvents(ch, battle, battle.runtime.Cancel())
			}
			s.emitVictory(ch, battle)
		}
		ss.battleMu.Unlock()
	}
	if mainStoryAILeader != nil {
		if mainStoryAICompletedMap != 0 {
			s.scheduleMainStoryAIReturnToCity(mainStoryAILeader, mainStoryAICompletedMap)
		} else {
			s.scheduleMainStoryAINext(mainStoryAILeader)
		}
	}
	if len(memberIDs) > 1 {
		activity := false
		for _, battle := range battles {
			if battle != nil && battle.activity != nil {
				activity = true
				break
			}
		}
		if activity {
			s.continuePartyActivity(memberIDs, battles)
		}
	}
	s.broadcastFinalPartyHealth(party)
}

func (s *Server) finishPartyDefeat(party *partyBattle) {
	if party == nil {
		return
	}
	party.mu.Lock()
	aborted := party.aborted
	party.mu.Unlock()
	if aborted {
		return
	}
	memberIDs, battles := party.settlementSnapshot()
	settledMembers := make([]*channel, 0, len(memberIDs))
	configStateMu.RLock()
	for _, playerID := range memberIDs {
		ch := s.findChannelByPlayerID(playerID)
		if ch == nil || ch.session == nil {
			continue
		}
		ss := ch.session
		ss.battleMu.Lock()
		battle := battles[playerID]
		if battle != nil && ss.battle == battle && battle.ended {
			if battle.runtime != nil {
				s.emitCombatEvents(ch, battle, battle.runtime.Cancel())
			}
			persistFamilyBossProgress(ss.familyID, battle)
			s.settleFamilyBossBattle(ch, battle, false)
			s.recoverPlayerAfterDefeat(ch, battle)
			s.sendBattleDefeat(ch, battle)
			ss.battle = nil
			ss.idleBattle = false
			s.pushHealth(ch)
			settledMembers = append(settledMembers, ch)
		}
		ss.battleMu.Unlock()
	}
	configStateMu.RUnlock()
	// Every battle must be detached before scene startup can inspect any member.
	// Sending the whole party's ChangeMap frames under one transition lock also
	// prevents an early member's startup timer from rebuilding a half-old party.
	x, y := mainCityReturnSpawn()
	s.changeMapForParty(settledMembers, 10004, x, y)
	s.broadcastFinalPartyHealth(party)
}

func (s *Server) sendBattleVictory(ch *channel, battle *battleState) {
	if bossLayerForMapID(battle.mapID) > 0 {
		s.sendPush(ch, protocol.OpM2C_BossFightVictory, &protocol.M2C_BossFightVictory{ActorId: ch.session.playerID})
		return
	}
	s.sendPush(ch, protocol.OpM2C_BattleVictory, &protocol.M2C_BattleVictory{
		BattleType: battleTypeForClient(battle), ActorId: ch.session.playerID,
	})
}

func (s *Server) sendBattleDefeat(ch *channel, battle *battleState) {
	if bossLayerForMapID(battle.mapID) > 0 {
		s.sendPush(ch, protocol.OpM2C_BossFightDefeat, &protocol.M2C_BossFightDefeat{ActorId: ch.session.playerID})
		return
	}
	s.sendPush(ch, protocol.OpM2C_BattleDefeat, &protocol.M2C_BattleDefeat{
		BattleType: battleTypeForClient(battle), ActorId: ch.session.playerID,
	})
}
