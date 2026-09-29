// pvp.go：点击其他玩家菜单（窥探/挑战 PK/查看角色）。
//
// 客户端 ClickOtherPlayerEvent（HotfixView 59/712）弹出玩家操作菜单：
//   b__0 查看角色 → C2M_GetCharacter{Id=对方}（equip.go onGetCharacter 已支持他人）
//   b__1 私聊（本地）
//   b__2 加好友 → C2M_AddFriend
//   b__3 邀请组队 → C2M_InviteTeam(20156)
//   b__4 申请组队 → C2M_RequestTeam(20154)
//   b__6 窥探/挑战 → C2M_StartPKFight{TargetId=对方}(20094)
// 之前 20094 无 case → 客户端 await 无响应 → 卡死。这里补响应。

package main

import (
	"log"
	"math"
	"sort"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// pvpBattle links the two mirrored battleState instances used by a duel. Each
// client treats the opposing player as an enemy Unit, while the server keeps
// the real player HP authoritative and mirrors it into the other side's
// monster-shaped combat target.
type pvpBattle struct {
	mu           sync.Mutex
	members      map[int64]*battleState
	memberIDs    []int64
	settled      bool
	rated        bool
	returnPoints map[int64]pvpReturnPoint
}

type pvpReturnPoint struct {
	mapID int32
	x     float32
	y     float32
}

const (
	personalPVPCopyID         int64 = 10008
	personalPVPBattleType     int32 = 21
	directPKCopyID            int64 = 10009
	directPKBattleType        int32 = 20
	personalPVPArenaMapID     int32 = 1000801
	personalPVPDailyBattleCap       = 100
)

func (p *pvpBattle) channels(s *Server) []*channel {
	if p == nil || s == nil {
		return nil
	}
	out := make([]*channel, 0, len(p.memberIDs))
	for _, id := range p.memberIDs {
		if ch := s.findChannelByPlayerID(id); ch != nil && ch.session != nil {
			out = append(out, ch)
		}
	}
	return out
}

func pvpEnemyUnit(target *session) *monsterUnit {
	return &monsterUnit{
		id: target.playerID, hp: target.battleHP(), maxHP: target.playerMaxHp(),
		phyAtk: target.playerPhyAtk(), spiAtk: target.playerSpiAtk(),
		phyDef: target.playerPhyDef(), spiDef: target.playerSpiDef(), alive: true,
		extraNumeric: map[int32]float64{
			1013: target.playerExtraNumeric(1013), 1014: target.playerExtraNumeric(1014),
			1015: target.playerExtraNumeric(1015), 1016: target.playerExtraNumeric(1016),
			1017: target.playerExtraNumeric(1017), 1018: target.playerExtraNumeric(1018),
			1019: target.playerExtraNumeric(1019), 1020: target.playerExtraNumeric(1020),
			1021: target.playerExtraNumeric(1021), 1022: target.playerExtraNumeric(1022),
			1023: target.playerExtraNumeric(1023), 1031: target.playerExtraNumeric(1031),
			1032: target.playerExtraNumeric(1032), 1033: target.playerExtraNumeric(1033),
		},
	}
}

func newPVPBattleState(owner, target *session, copyID int64, battleType int32) *battleState {
	battle := &battleState{
		copyID: copyID, mapID: owner.mapID, battleType: battleType,
		monsters:   []*monsterUnit{pvpEnemyUnit(target)},
		selectedID: target.playerID, playerHP: owner.battleHP(), playerMaxHP: owner.playerMaxHp(),
		playerMP: owner.battleMP(), playerMaxMP: owner.playerMaxMp(),
		phyAtk: owner.playerPhyAtk(), spiAtk: owner.playerSpiAtk(),
		phyDef: owner.playerPhyDef(), spiDef: owner.playerSpiDef(), owner: owner,
	}
	battle.runtime = NewCombatRuntime(battle, owner.playerID, nil)
	return battle
}

func sendStartPK(s *Server, ch *channel, targetIDs []int64, battleType int32) {
	var body []byte
	for _, id := range targetIDs {
		body = pbAppendVarint(body, 1, uint64(id))
	}
	body = pbAppendVarint(body, 2, uint64(battleType))
	body = pbAppendVarint(body, 93, uint64(ch.session.playerID))
	s.sendRawPush(ch, protocol.OpM2C_SendStartPK, body)
}

func hasMultiMemberTeam(s *Server, playerID int64) bool {
	teamMu.Lock()
	defer teamMu.Unlock()
	team := s.teamOf(playerID)
	return team != nil && len(team.Members) > 1
}

func personalPVPDay(now time.Time) string {
	_, _, location := gameplayPersonalPVPSettings()
	return now.In(location).Format("20060102")
}

func normalizePersonalPVPDay(state *signinState, now time.Time) bool {
	if state == nil {
		return false
	}
	day := personalPVPDay(now)
	if state.PVPBattleDay == day {
		return false
	}
	state.PVPBattleDay = day
	state.PVPBattleCount = 0
	return true
}

func (s *Server) pvpMatchingCountLocked() int32 {
	var count int32
	for _, online := range s.onlineChannels() {
		if online == nil || online.session == nil {
			continue
		}
		online.session.pvpStateMu.Lock()
		matching := online.session.signin != nil && online.session.signin.PVPIsMatching
		online.session.pvpStateMu.Unlock()
		if matching {
			count++
		}
	}
	return count
}

func (s *Server) pvpMatchingCount() int32 {
	s.pvpMu.Lock()
	defer s.pvpMu.Unlock()
	return s.pvpMatchingCountLocked()
}

func (s *Server) cancelPersonalPVPMatch(ss *session) {
	if ss == nil || ss.signin == nil {
		return
	}
	s.pvpMu.Lock()
	ss.pvpStateMu.Lock()
	ss.signin.PVPIsMatching = false
	ss.signin.PVPMatchCount = 0
	ss.pvpStateMu.Unlock()
	s.pvpMu.Unlock()
}

func (s *Server) personalPVPMatchFailure(ch *channel, now time.Time) string {
	if ch == nil || ch.session == nil || ch.session.playerID == 0 || ch.session.state != sessInGame {
		return "请先进入游戏"
	}
	ss := ch.session
	ss.pvpStateMu.Lock()
	if ss.signin == nil {
		ss.signin = &signinState{}
	}
	dayChanged := normalizePersonalPVPDay(ss.signin, now)
	battleCount := ss.signin.PVPBattleCount
	ss.pvpStateMu.Unlock()
	if dayChanged {
		s.saveData(ch)
	}
	if battleCount >= personalPVPDailyBattleCap {
		return "今日战斗次数已用完"
	}
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	if ss.battle != nil {
		return "战斗中不能参加个人竞技"
	}
	if ss.battleHP() <= 0 {
		return battleEntryHealthMessage
	}
	return ""
}

// startPersonalPVPMatch installs the rated CopyConfig=10008 duel. The caller
// holds pvpMu, keeping queue selection and both matching flags atomic.
func (s *Server) startPersonalPVPMatch(requester, waiting *channel, response *protocol.M2C_RequestPersonalPvp) bool {
	if requester == nil || waiting == nil || requester.session == nil || waiting.session == nil {
		return false
	}
	partyStartMu.Lock()
	defer partyStartMu.Unlock()
	first, second := requester, waiting
	if first.session.playerID > second.session.playerID {
		first, second = second, first
	}
	first.session.battleMu.Lock()
	second.session.battleMu.Lock()
	defer first.session.battleMu.Unlock()
	defer second.session.battleMu.Unlock()

	for _, participant := range []*channel{requester, waiting} {
		ss := participant.session
		ss.pvpStateMu.Lock()
		eligible := ss.signin != nil && ss.signin.PVPIsMatching &&
			ss.signin.PVPBattleCount < personalPVPDailyBattleCap
		ss.pvpStateMu.Unlock()
		if ss.state != sessInGame || !eligible || ss.battle != nil || ss.battleHP() <= 0 {
			return false
		}
	}

	left := newPVPBattleState(requester.session, waiting.session, personalPVPCopyID, personalPVPBattleType)
	right := newPVPBattleState(waiting.session, requester.session, personalPVPCopyID, personalPVPBattleType)
	duel := &pvpBattle{
		members: map[int64]*battleState{
			requester.session.playerID: left,
			waiting.session.playerID:   right,
		},
		memberIDs: []int64{requester.session.playerID, waiting.session.playerID},
		rated:     true,
		returnPoints: map[int64]pvpReturnPoint{
			requester.session.playerID: {
				mapID: requester.session.mapID, x: requester.session.x, y: requester.session.y,
			},
			waiting.session.playerID: {
				mapID: waiting.session.mapID, x: waiting.session.x, y: waiting.session.y,
			},
		},
	}
	left.pvp, right.pvp = duel, duel
	leftPassive, leftErr := left.initializePassiveSkills(requester.session, nil)
	rightPassive, rightErr := right.initializePassiveSkills(waiting.session, nil)
	if leftErr != nil || rightErr != nil {
		log.Printf("personal pvp passive init left=%v right=%v", leftErr, rightErr)
		return false
	}
	s.leaveTeamForSoloInstance(requester, personalPVPCopyID)
	s.leaveTeamForSoloInstance(waiting, personalPVPCopyID)

	for _, participant := range []*channel{requester, waiting} {
		participant.session.pvpStateMu.Lock()
		participant.session.signin.PVPIsMatching = false
		participant.session.signin.PVPMatchCount = 0
		participant.session.pvpStateMu.Unlock()
	}
	// The requesting client must finish its awaited 20326 call before scene and
	// combat pushes begin. Its opponent already received this response earlier.
	s.sendPush(requester, protocol.OpM2C_RequestPersonalPvp, response)
	arenaX, arenaY := sceneSpawn(personalPVPArenaMapID / 100)
	s.changeMap(requester, personalPVPArenaMapID, arenaX, arenaY)
	s.changeMap(waiting, personalPVPArenaMapID, arenaX, arenaY)
	left.mapID, right.mapID = personalPVPArenaMapID, personalPVPArenaMapID
	left.scenePending, right.scenePending = true, true
	requester.session.battle, waiting.session.battle = left, right

	requesterSeq, _ := requester.session.currentMapSceneVersion()
	waitingSeq, _ := waiting.session.currentMapSceneVersion()
	var startPresentation func()
	startPresentation = func() {
		if !requester.session.isCurrentMapReady(requesterSeq) ||
			!waiting.session.isCurrentMapReady(waitingSeq) {
			if requester.session.isCurrentMapChange(requesterSeq) &&
				waiting.session.isCurrentMapChange(waitingSeq) {
				time.AfterFunc(50*time.Millisecond, startPresentation)
				return
			}
			log.Printf("cancel personal pvp presentation %d vs %d: arena scene not ready",
				requester.session.playerID, waiting.session.playerID)
			return
		}
		partyStartMu.Lock()
		defer partyStartMu.Unlock()
		first, second := requester, waiting
		if first.session.playerID > second.session.playerID {
			first, second = second, first
		}
		first.session.battleMu.Lock()
		second.session.battleMu.Lock()
		defer first.session.battleMu.Unlock()
		defer second.session.battleMu.Unlock()
		if requester.session.battle != left || waiting.session.battle != right ||
			left.ended || right.ended || left.pvp == nil || left.pvp.settled {
			return
		}
		left.scenePending, right.scenePending = false, false
		s.sendTeamMember(requester, requester.session.playerID, []int64{requester.session.playerID})
		s.sendTeamMember(waiting, waiting.session.playerID, []int64{waiting.session.playerID})
		for _, recipient := range []*channel{requester, waiting} {
			s.pushPlayerAttrsTo(recipient, requester.session, left.playerHP, left.playerMP)
			s.pushPlayerAttrsTo(recipient, waiting.session, right.playerHP, right.playerMP)
		}
		sendStartPK(s, requester, []int64{waiting.session.playerID}, personalPVPBattleType)
		sendStartPK(s, waiting, []int64{requester.session.playerID}, personalPVPBattleType)
		s.emitCombatEvents(requester, left, leftPassive)
		s.emitCombatEvents(waiting, right, rightPassive)
		log.Printf("start personal pvp %d vs %d", requester.session.playerID, waiting.session.playerID)
	}
	time.AfterFunc(mapStartupDelay+mapTeamSnapshotDelay, startPresentation)
	return true
}

func (s *Server) onRequestPersonalPvp(ch *channel, req *protocol.C2M_RequestPersonalPvp) proto.Message {
	resp := &protocol.M2C_RequestPersonalPvp{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	s.pvpMu.Lock()
	defer s.pvpMu.Unlock()
	now := time.Now()
	if message := s.personalPVPMatchFailure(ch, now); message != "" {
		resp.Error, resp.Message = errBadParam, message
		return resp
	}
	ss := ch.session
	ss.pvpStateMu.Lock()
	ss.signin.PVPIsMatching = true
	ss.pvpStateMu.Unlock()
	matchCount := s.pvpMatchingCountLocked()
	ss.pvpStateMu.Lock()
	ss.signin.PVPMatchCount = matchCount
	ss.pvpStateMu.Unlock()

	candidates := s.onlineChannels()
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].session.playerID < candidates[j].session.playerID
	})
	for _, candidate := range candidates {
		if candidate == nil || candidate == ch || candidate.session == nil {
			continue
		}
		candidate.session.pvpStateMu.Lock()
		matching := candidate.session.signin != nil && candidate.session.signin.PVPIsMatching
		candidate.session.pvpStateMu.Unlock()
		if !matching {
			continue
		}
		if message := s.personalPVPMatchFailure(candidate, now); message != "" {
			candidate.session.pvpStateMu.Lock()
			candidate.session.signin.PVPIsMatching = false
			candidate.session.signin.PVPMatchCount = 0
			candidate.session.pvpStateMu.Unlock()
			s.saveData(candidate)
			continue
		}
		if s.startPersonalPVPMatch(ch, candidate, resp) {
			return nil
		}
	}
	matchCount = s.pvpMatchingCountLocked()
	ss.pvpStateMu.Lock()
	ss.signin.PVPMatchCount = matchCount
	ss.pvpStateMu.Unlock()
	s.saveData(ch)
	return resp
}

func (s *Server) onGetPvpBoardInfo(ch *channel, req *protocol.C2M_GetPvpBoardInfo) proto.Message {
	resp := &protocol.M2C_GetPvpBoardInfo{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	ss := ch.session
	if ss.signin == nil {
		ss.signin = &signinState{}
	}
	s.pvpMu.Lock()
	ss.pvpStateMu.Lock()
	dayChanged := normalizePersonalPVPDay(ss.signin, time.Now())
	resp.BattleCount = ss.signin.PVPBattleCount
	resp.Scord = ss.signin.PVPScore
	resp.IsMatch = ss.signin.PVPIsMatching
	ss.pvpStateMu.Unlock()
	if dayChanged {
		s.saveData(ch)
	}
	resp.MatchCount = s.pvpMatchingCountLocked()
	s.pvpMu.Unlock()
	playerCount, err := s.store.CountPlayers()
	if err != nil {
		resp.Error, resp.Message = errBadParam, "竞技排行读取失败"
		return resp
	}
	var players []*Player
	if playerCount > 500 {
		players, err = s.store.ListPlayersForRanking(0)
	} else {
		players, err = s.store.ListPlayers(0)
	}
	if err != nil {
		resp.Error, resp.Message = errBadParam, "竞技排行读取失败"
		return resp
	}
	type pvpEntry struct {
		id   int64
		info *protocol.PvpRankInfo
	}
	entries := make([]pvpEntry, 0, len(players))
	for _, player := range players {
		if player == nil {
			continue
		}
		state := player.Relations.signin
		if state == nil {
			state = &signinState{PVPScore: player.PVPScore}
		}
		entries = append(entries, pvpEntry{id: player.ID, info: &protocol.PvpRankInfo{
			Name: player.Name, Level: player.Level, Scord: state.PVPScore, JobIs: player.JobID,
		}})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].info.Scord == entries[j].info.Scord {
			return entries[i].id < entries[j].id
		}
		return entries[i].info.Scord > entries[j].info.Scord
	})
	if len(entries) > 100 {
		entries = entries[:100]
	}
	for _, entry := range entries {
		resp.RankList = append(resp.RankList, entry.info)
	}
	return resp
}

// onStartPKFight：20094 → 20095。双方必须先拥有对方的场景 Unit；随后给双方
// 下发各自的 TargetIdList，建立镜像权威战斗状态。客户端的 20096 handler 会
// 创建敌方队伍、移动双方到 PK 阵位并进入 BattleStart。
func (s *Server) onStartPKFight(ch *channel, req *protocol.C2M_StartPKFight) proto.Message {
	resp := &protocol.M2C_StartPKFight{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	if req.TargetId == 0 {
		resp.Error, resp.Message = errBadParam, "目标无效"
		return resp
	}
	if req.TargetId == ch.session.playerID {
		resp.Message = "不能挑战自己"
		return resp
	}
	target := s.findChannelByPlayerID(req.TargetId)
	if target == nil {
		resp.Error, resp.Message = errBadParam, "玩家不在线"
		return resp
	}
	if !sameMapSession(ch.session, target.session) {
		resp.Message = "目标不在当前场景"
		return resp
	}
	if hasMultiMemberTeam(s, ch.session.playerID) || hasMultiMemberTeam(s, target.session.playerID) {
		resp.Message = "组队状态下暂不能发起单人切磋"
		return resp
	}

	s.pvpMu.Lock()
	defer s.pvpMu.Unlock()
	partyStartMu.Lock()
	defer partyStartMu.Unlock()
	first, second := ch, target
	if first.session.playerID > second.session.playerID {
		first, second = second, first
	}
	first.session.battleMu.Lock()
	second.session.battleMu.Lock()
	defer first.session.battleMu.Unlock()
	defer second.session.battleMu.Unlock()
	if ch.session.battle != nil || target.session.battle != nil {
		resp.Message = "玩家正在战斗中"
		return resp
	}
	if ch.session.battleHP() <= 0 {
		resp.Message = battleEntryHealthMessage
		return resp
	}
	if target.session.battleHP() <= 0 {
		resp.Message = "对方生命值不足，暂时无法挑战"
		return resp
	}
	for _, participant := range []*channel{ch, target} {
		if participant.session.signin != nil {
			participant.session.pvpStateMu.Lock()
			participant.session.signin.PVPIsMatching = false
			participant.session.signin.PVPMatchCount = 0
			participant.session.pvpStateMu.Unlock()
		}
	}

	left := newPVPBattleState(ch.session, target.session, directPKCopyID, directPKBattleType)
	right := newPVPBattleState(target.session, ch.session, directPKCopyID, directPKBattleType)
	duel := &pvpBattle{
		members:   map[int64]*battleState{ch.session.playerID: left, target.session.playerID: right},
		memberIDs: []int64{ch.session.playerID, target.session.playerID},
	}
	left.pvp, right.pvp = duel, duel
	ch.session.battle, target.session.battle = left, right
	leftPassive, leftErr := left.initializePassiveSkills(ch.session, nil)
	rightPassive, rightErr := right.initializePassiveSkills(target.session, nil)
	if leftErr != nil || rightErr != nil {
		ch.session.battle, target.session.battle = nil, nil
		resp.Message = "战斗技能初始化失败"
		log.Printf("[S=%d] start pk passive init left=%v right=%v", ch.id, leftErr, rightErr)
		return resp
	}

	// Both clients need a valid own TeamComponent even for a one-on-one duel,
	// because both the start and end handlers dereference it without nil checks.
	s.sendTeamMember(ch, ch.session.playerID, []int64{ch.session.playerID})
	s.sendTeamMember(target, target.session.playerID, []int64{target.session.playerID})
	for _, recipient := range []*channel{ch, target} {
		s.pushPlayerAttrsTo(recipient, ch.session, left.playerHP, left.playerMP)
		s.pushPlayerAttrsTo(recipient, target.session, right.playerHP, right.playerMP)
	}
	sendStartPK(s, ch, []int64{target.session.playerID}, directPKBattleType)
	sendStartPK(s, target, []int64{ch.session.playerID}, directPKBattleType)
	s.emitCombatEvents(ch, left, leftPassive)
	s.emitCombatEvents(target, right, rightPassive)
	log.Printf("[S=%d] start pk fight %d vs %d", ch.id, ch.session.playerID, req.TargetId)
	return resp
}

// syncPVPStateLocked mirrors the acting battle into the opponent's real-player
// state and mirrors the actor into the opponent's enemy target. Caller holds
// duel.mu. Using the acting side explicitly avoids map-iteration order from
// overwriting a freshly applied hit with the opponent's older mirror value.
func syncPVPStateLocked(battle *battleState) {
	if battle == nil || battle.pvp == nil || len(battle.monsters) == 0 {
		return
	}
	targetID := battle.monsters[0].id
	targetBattle := battle.pvp.members[targetID]
	if targetBattle == nil {
		return
	}
	targetBattle.playerHP = battle.monsters[0].hp
	targetBattle.playerDefeated = targetBattle.playerHP <= 0
	if len(targetBattle.monsters) > 0 && battle.owner != nil && targetBattle.monsters[0].id == battle.owner.playerID {
		targetBattle.monsters[0].hp = battle.playerHP
		targetBattle.monsters[0].alive = battle.playerHP > 0
	}
}

func addPersonalPVPScore(current, delta int32) int32 {
	value := int64(current) + int64(delta)
	if value < 0 {
		return 0
	}
	if value > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(value)
}

func settleRatedPVP(duel *pvpBattle, winnerID int64, now time.Time) {
	if duel == nil || !duel.rated || winnerID == 0 {
		return
	}
	victoryDelta, defeatDelta, _ := gameplayPersonalPVPSettings()
	for playerID, battle := range duel.members {
		if battle == nil || battle.owner == nil {
			continue
		}
		ss := battle.owner
		ss.pvpStateMu.Lock()
		if ss.signin == nil {
			ss.signin = &signinState{}
		}
		normalizePersonalPVPDay(ss.signin, now)
		ss.signin.PVPBattleCount++
		ss.signin.PVPIsMatching = false
		ss.signin.PVPMatchCount = 0
		delta := defeatDelta
		if playerID == winnerID {
			delta = victoryDelta
		}
		ss.signin.PVPScore = addPersonalPVPScore(ss.signin.PVPScore, delta)
		ss.pvpStateMu.Unlock()
	}
}

// finishPVPBattleLocked ends both client battle scenes with the native PK
// result handlers. winnerID==0 means both sides are treated as defeated (for
// server shutdown); caller holds duel.mu.
func (s *Server) finishPVPBattleLocked(duel *pvpBattle, winnerID int64, reason string) {
	if duel == nil || duel.settled {
		return
	}
	duel.settled = true
	settleRatedPVP(duel, winnerID, time.Now())
	channels := duel.channels(s)
	for _, battle := range duel.members {
		if battle == nil {
			continue
		}
		battle.ended = true
		if battle.owner != nil {
			battle.owner.battle = nil
			battle.owner.autoBattle = false
			battle.owner.autoBattleEpoch++
			battle.owner.hp, battle.owner.mp = battle.owner.playerMaxHp(), battle.owner.playerMaxMp()
		}
	}
	for _, member := range channels {
		ss := member.session
		if battle := duel.members[ss.playerID]; battle != nil && battle.runtime != nil {
			s.emitCombatEvents(member, battle, battle.runtime.Cancel())
		}
		if ss.playerID == winnerID {
			s.sendPush(member, protocol.OpM2C_PKFightVictory, &protocol.M2C_PKFightVictory{ActorId: ss.playerID})
		} else {
			s.sendPush(member, protocol.OpM2C_PKFightDefeat, &protocol.M2C_PKFightDefeat{ActorId: ss.playerID})
		}
		s.saveData(member)
	}
	for _, recipient := range channels {
		for _, owner := range channels {
			s.pushPlayerAttrsTo(recipient, owner.session, owner.session.hp, owner.session.mp)
		}
		// M2C_SendStartPK creates a client-side enemy Team.  Native PK result
		// handlers clear its fight flags but do not dispose that enemy Team;
		// restore the player's real singleton team after the result so the
		// opponent head cannot remain in the normal team bar.
		s.sendTeamMember(recipient, recipient.session.playerID, []int64{recipient.session.playerID})
	}
	if duel.rated {
		online := make(map[int64]bool, len(channels))
		for _, member := range channels {
			online[member.session.playerID] = true
		}
		for playerID, battle := range duel.members {
			if battle == nil || battle.owner == nil || online[playerID] {
				continue
			}
			point, ok := duel.returnPoints[playerID]
			if !ok {
				point.mapID = 10004
				point.x, point.y = mainCityReturnSpawn()
			}
			battle.owner.mapID = point.mapID
			battle.owner.resetMovement(point.x, point.y)
		}
		for _, member := range channels {
			point, ok := duel.returnPoints[member.session.playerID]
			if !ok {
				point.mapID = 10004
				point.x, point.y = mainCityReturnSpawn()
			}
			s.changeMap(member, point.mapID, point.x, point.y)
		}
	}
	log.Printf("finish pk rated=%v winner=%d reason=%s", duel.rated, winnerID, reason)
}

func (s *Server) settlePVPCombatLocked(ch *channel, battle *battleState) bool {
	if battle == nil || battle.pvp == nil || battle.pvp.settled {
		return true
	}
	syncPVPStateLocked(battle)
	winnerID := int64(0)
	if battle.playerHP <= 0 && len(battle.monsters) > 0 {
		winnerID = battle.monsters[0].id
	} else if len(battle.monsters) == 0 || battle.monsters[0].hp <= 0 || !battle.monsters[0].alive {
		winnerID = ch.session.playerID
	} else {
		return false
	}
	s.finishPVPBattleLocked(battle.pvp, winnerID, "combat-ended")
	return true
}
