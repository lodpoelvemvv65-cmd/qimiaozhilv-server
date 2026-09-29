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
	"sort"
	"sync"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// pvpBattle links the two mirrored battleState instances used by a duel. Each
// client treats the opposing player as an enemy Unit, while the server keeps
// the real player HP authoritative and mirrors it into the other side's
// monster-shaped combat target.
type pvpBattle struct {
	mu        sync.Mutex
	members   map[int64]*battleState
	memberIDs []int64
	settled   bool
}

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
			1023: target.playerExtraNumeric(1023),
		},
	}
}

func newPVPBattleState(owner, target *session) *battleState {
	battle := &battleState{
		mapID: owner.mapID, battleType: 1, monsters: []*monsterUnit{pvpEnemyUnit(target)},
		selectedID: target.playerID, playerHP: owner.battleHP(), playerMaxHP: owner.playerMaxHp(),
		playerMP: owner.battleMP(), playerMaxMP: owner.playerMaxMp(),
		phyAtk: owner.playerPhyAtk(), spiAtk: owner.playerSpiAtk(),
		phyDef: owner.playerPhyDef(), spiDef: owner.playerSpiDef(), owner: owner,
	}
	battle.runtime = NewCombatRuntime(battle, owner.playerID, nil)
	return battle
}

func sendStartPK(s *Server, ch *channel, targetIDs []int64) {
	var body []byte
	for _, id := range targetIDs {
		body = pbAppendVarint(body, 1, uint64(id))
	}
	body = pbAppendVarint(body, 2, 1)
	body = pbAppendVarint(body, 93, uint64(ch.session.playerID))
	s.sendRawPush(ch, protocol.OpM2C_SendStartPK, body)
}

func hasMultiMemberTeam(s *Server, playerID int64) bool {
	teamMu.Lock()
	defer teamMu.Unlock()
	team := s.teamOf(playerID)
	return team != nil && len(team.Members) > 1
}

func (s *Server) pvpMatchingCount() int32 {
	var count int32
	for _, online := range s.onlineChannels() {
		if online != nil && online.session != nil && online.session.signin != nil && online.session.signin.PVPIsMatching {
			count++
		}
	}
	return count
}

func (s *Server) onRequestPersonalPvp(ch *channel, req *protocol.C2M_RequestPersonalPvp) proto.Message {
	resp := &protocol.M2C_RequestPersonalPvp{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	ss := ch.session
	if ss.signin == nil {
		ss.signin = &signinState{}
	}
	if ss.signin.PVPBattleCount >= 100 {
		resp.Error, resp.Message = errBadParam, "今日战斗次数已用完"
		return resp
	}
	ss.signin.PVPIsMatching = true
	ss.signin.PVPMatchCount = s.pvpMatchingCount()
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
	resp.BattleCount = ss.signin.PVPBattleCount
	resp.Scord = ss.signin.PVPScore
	resp.IsMatch = ss.signin.PVPIsMatching
	resp.MatchCount = s.pvpMatchingCount()
	players, err := s.store.ListPlayers(0)
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
		state := signinFromJSON(player.Signin)
		if state == nil {
			state = &signinState{}
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

	left := newPVPBattleState(ch.session, target.session)
	right := newPVPBattleState(target.session, ch.session)
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
	sendStartPK(s, ch, []int64{target.session.playerID})
	sendStartPK(s, target, []int64{ch.session.playerID})
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

// finishPVPBattleLocked ends both client battle scenes with the native PK
// result handlers. winnerID==0 means both sides are treated as defeated (for
// server shutdown); caller holds duel.mu.
func (s *Server) finishPVPBattleLocked(duel *pvpBattle, winnerID int64, reason string) {
	if duel == nil || duel.settled {
		return
	}
	duel.settled = true
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
	log.Printf("finish pk winner=%d reason=%s", winnerID, reason)
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
