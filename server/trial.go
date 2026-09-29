package main

import (
	"log"
	"math"
	"math/rand"
	"sort"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

const trialBattleType int32 = 2

type trialReward struct {
	exp     int64
	coin    int64
	yuanBao int64
	voucher int64
	items   map[int32]int64
}

func newTrialReward() trialReward {
	return trialReward{items: make(map[int32]int64)}
}

// trialCopyForMap resolves the authoritative trial row for a scene layer.
func trialCopyForMap(mapID int32) (int32, map[string]interface{}, bool) {
	if tables == nil {
		return 0, nil, false
	}
	for id, row := range tables.trialCopy {
		if int32(num(row["MapId"])) == mapID {
			return int32(id), row, true
		}
	}
	return 0, nil, false
}

func trialCopyByID(id int32) (map[string]interface{}, bool) {
	if tables == nil || id <= 0 {
		return nil, false
	}
	row, ok := tables.trialCopy[int64(id)]
	return row, ok
}

// onStartTrialCopyFight handles the native 20092 trial click RPC. The client
// removes its display monster and creates combat units from UnitIdList. Do not
// also send 20047/20050 or every trial monster is created twice.
func (s *Server) onStartTrialCopyFight(ch *channel, req *protocol.C2M_StartTrialCopyFight) proto.Message {
	resp := &protocol.M2C_StartTrialCopyFight{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Message = "请先登录"
		return resp
	}
	ss := ch.session
	if ss.battleHP() <= 0 {
		resp.Message = battleEntryHealthMessage
		return resp
	}
	trialID, row, ok := trialCopyForMap(ss.mapID)
	if !ok {
		resp.Message = "当前不在试炼之地"
		return resp
	}
	resp.TrialCopyId = trialID
	teamMu.Lock()
	_, team := s.teamEntryOf(ss.playerID)
	notLeader := team != nil && team.LeaderId != ss.playerID
	teamMu.Unlock()
	if notLeader {
		// This client treats non-zero RPC errors as fatal in several battle
		// continuations, so reject through Message while keeping Error=0.
		resp.Message = "组队试炼请由队长发起"
		return resp
	}

	partyStartMu.Lock()
	defer partyStartMu.Unlock()
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	if ss.battle != nil {
		if ss.battle.battleType == trialBattleType && ss.battle.trialCopyID == trialID {
			for _, unit := range ss.battle.monsters {
				resp.UnitIdList = append(resp.UnitIdList, unit.id)
			}
			log.Printf("[S=%d] duplicate start trial fight trialId=%d ignored", ch.id, trialID)
			return resp
		}
		resp.Message = "战斗正在进行中"
		return resp
	}

	monsterID := int32(num(row["MonsterId"]))
	count := int(num(row["MonsterCount"]))
	if count <= 0 {
		count = 1
	}
	units := ss.buildMonsterUnitsFromRoster([]int32{monsterID}, []int{count})
	if len(units) == 0 {
		resp.Message = "试炼怪物配置不存在"
		return resp
	}
	region := int32(1001)
	if tables.mainStory[int64(region)] == nil {
		resp.Message = "战斗场景配置不存在"
		return resp
	}
	started, _ := s.finishStartTrialBattle(ch, region, units, ss.mapID)
	if !started {
		resp.Message = "开始试炼失败"
		return resp
	}
	for _, unit := range units {
		resp.UnitIdList = append(resp.UnitIdList, unit.id)
	}
	log.Printf("[S=%d] start trial fight trialId=%d map=%d monsters=%d", ch.id, trialID, ss.mapID, len(units))
	return resp
}

func randomDropCount(row map[string]interface{}) int64 {
	minCount := int64(num(row["MinCount"]))
	maxCount := int64(num(row["MaxCount"]))
	if minCount <= 0 {
		minCount = 1
	}
	if maxCount < minCount {
		maxCount = minCount
	}
	if maxCount == minCount {
		return minCount
	}
	return minCount + rand.Int63n(maxCount-minCount+1)
}

// rollParentset processes every Parentset subset. Each SonSet independently
// selects one weighted entry; id 0 is the configured no-drop result.
func rollParentset(parentsetID int32, reward *trialReward) {
	if reward == nil || tables == nil || parentsetID <= 0 {
		return
	}
	parent, ok := tables.parentset[int64(parentsetID)]
	if !ok {
		return
	}
	for _, subsetValue := range arrOf(parent["SubsetArr"]) {
		subset, _ := subsetValue.(map[string]interface{})
		if subset == nil {
			continue
		}
		son, ok := tables.sonSet[num(subset["_Id"])]
		if !ok {
			continue
		}
		drops := arrOf(son["DropArr"])
		var total int64
		for _, dropValue := range drops {
			drop, _ := dropValue.(map[string]interface{})
			if drop != nil {
				if weight := num(drop["Weight"]); weight > 0 {
					total += weight
				}
			}
		}
		if total <= 0 {
			continue
		}
		roll := rand.Int63n(total)
		for _, dropValue := range drops {
			drop, _ := dropValue.(map[string]interface{})
			if drop == nil {
				continue
			}
			weight := num(drop["Weight"])
			if weight <= 0 {
				continue
			}
			if roll >= weight {
				roll -= weight
				continue
			}
			itemID := int32(num(drop["_Id"]))
			if itemID != 0 {
				reward.addDrop(itemID, randomDropCount(drop))
			}
			break
		}
	}
}

func (reward *trialReward) addDrop(itemID int32, count int64) {
	if reward == nil || itemID <= 0 || count <= 0 {
		return
	}
	switch itemID {
	case 110201:
		reward.exp += count
	case 110202:
		reward.yuanBao += count
	case 110203:
		reward.coin += count
	case 110204:
		reward.voucher += count
	default:
		if reward.items == nil {
			reward.items = make(map[int32]int64)
		}
		reward.items[itemID] += count
	}
}

func trialBattleDrops(battle *battleState) trialReward {
	reward := newTrialReward()
	if battle == nil || tables == nil {
		return reward
	}
	for _, monster := range battle.monsters {
		if monster == nil || monster.alive {
			continue
		}
		row, ok := tables.monsterBase[int64(monster.monsterID)]
		if ok {
			rollParentset(int32(num(row["Dropasubset"])), &reward)
		}
	}
	return reward
}

func trialRowsThrough(highestID int32) []map[string]interface{} {
	if tables == nil || highestID <= 0 {
		return nil
	}
	ids := make([]int, 0, len(tables.trialCopy))
	for id := range tables.trialCopy {
		if id <= int64(highestID) {
			ids = append(ids, int(id))
		}
	}
	sort.Ints(ids)
	rows := make([]map[string]interface{}, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, tables.trialCopy[int64(id)])
	}
	return rows
}

func trialRewardsForRows(rows []map[string]interface{}) trialReward {
	reward := newTrialReward()
	if tables == nil {
		return reward
	}
	for _, row := range rows {
		monsterID := int32(num(row["MonsterId"]))
		count := int(num(row["MonsterCount"]))
		if count <= 0 {
			count = 1
		}
		monster, ok := tables.monsterBase[int64(monsterID)]
		if !ok {
			continue
		}
		reward.exp += num(monster["Exp"]) * int64(count)
		parentsetID := int32(num(monster["Dropasubset"]))
		for i := 0; i < count; i++ {
			rollParentset(parentsetID, &reward)
		}
	}
	return reward
}

func rewardStackLimit(itemID int32) int64 {
	if tables != nil {
		for _, table := range []map[int64]map[string]interface{}{tables.goodsBase, tables.materialBase} {
			if row := table[int64(itemID)]; row != nil {
				if limit := num(row["MaxAmount"]); limit > 0 {
					return limit
				}
			}
		}
	}
	return 999
}

// addTrialItemToBag supports multi-stack rewards and one-slot-per-instance
// equipment. It is only applied to a cloned bag during transaction staging.
func addTrialItemToBag(ss *session, itemID int32, count int64) bool {
	if ss == nil || ss.bag == nil || count <= 0 {
		return false
	}
	template := newBagItem(itemID)
	if template.ItemType == int32(protocol.ItemType_EquipItem) {
		for count > 0 {
			idx := nextBagIndex(ss)
			if idx < 0 {
				return false
			}
			item := newBagItem(itemID)
			item.Count = 1
			ss.bag[idx] = item
			count--
		}
		return true
	}

	limit := rewardStackLimit(itemID)
	indices := make([]int, 0, len(ss.bag))
	for index := range ss.bag {
		indices = append(indices, int(index))
	}
	sort.Ints(indices)
	for _, indexValue := range indices {
		item := ss.bag[int32(indexValue)]
		if item == nil || item.ItemId != itemID || item.ItemType != template.ItemType || int64(item.Count) >= limit {
			continue
		}
		add := limit - int64(item.Count)
		if add > count {
			add = count
		}
		item.Count += int32(add)
		count -= add
		if count == 0 {
			return true
		}
	}
	for count > 0 {
		idx := nextBagIndex(ss)
		if idx < 0 {
			return false
		}
		add := limit
		if add > count {
			add = count
		}
		item := newBagItem(itemID)
		item.Count = int32(add)
		ss.bag[idx] = item
		count -= add
	}
	return true
}

func stageTrialItems(ss *session, items map[int32]int64) (map[int32]*bagItem, bool) {
	if ss == nil {
		return nil, false
	}
	shadow := &session{bag: bagFromJSON(bagToJSON(ss.bag))}
	if shadow.bag == nil {
		shadow.bag = make(map[int32]*bagItem)
	}
	ids := make([]int, 0, len(items))
	for id := range items {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	for _, id := range ids {
		if !addTrialItemToBag(shadow, int32(id), items[int32(id)]) {
			return nil, false
		}
	}
	return shadow.bag, true
}

func appendRewardCount(list []*protocol.RewardItem, itemID int32, itemType protocol.ItemType, count int64) []*protocol.RewardItem {
	for count > 0 {
		chunk := count
		if chunk > math.MaxInt32 {
			chunk = math.MaxInt32
		}
		list = append(list, &protocol.RewardItem{Id: itemID, ItemType: itemType, Count: int32(chunk)})
		count -= chunk
	}
	return list
}

func trialRewardItems(reward trialReward) []*protocol.RewardItem {
	ids := make([]int, 0, len(reward.items))
	for id := range reward.items {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	var list []*protocol.RewardItem
	for _, idValue := range ids {
		id := int32(idValue)
		list = appendRewardCount(list, id, protocol.ItemType(newBagItem(id).ItemType), reward.items[id])
	}
	if reward.yuanBao > 0 {
		list = appendRewardCount(list, 110202, protocol.ItemType_GoodsItem, reward.yuanBao)
	}
	if reward.voucher > 0 {
		list = appendRewardCount(list, 110204, protocol.ItemType_GoodsItem, reward.voucher)
	}
	return list
}

// applyTrialReward commits an immediate battle drop. A full bag cannot cancel
// an already won fight, so non-fitting item drops are omitted as one group;
// currencies remain payable and the event is logged.
func (s *Server) applyTrialReward(ch *channel, reward trialReward, _ bool) []*protocol.RewardItem {
	ss := ch.session
	items, ok := stageTrialItems(ss, reward.items)
	if ok {
		ss.bag = items
		if len(reward.items) > 0 {
			s.pushBagSnapshot(ch)
		}
	} else if len(reward.items) > 0 {
		log.Printf("[S=%d] trial battle item drop omitted: bag full", ch.id)
		reward.items = nil
	}
	ss.coin += reward.coin
	ss.yuanBao += reward.yuanBao
	ss.voucher += reward.voucher
	if reward.coin != 0 || reward.yuanBao != 0 || reward.voucher != 0 {
		s.pushMoney(ch)
	}
	return trialRewardItems(reward)
}

func (s *Server) recordTrialVictory(ch *channel, trialID int32) {
	if ch == nil || ch.session == nil || trialID <= 0 {
		return
	}
	ss := ch.session
	if ss.signin == nil {
		ss.signin = &signinState{}
	}
	if trialID > ss.signin.TrialHighestID {
		ss.signin.TrialHighestID = trialID
		s.saveData(ch)
		log.Printf("[S=%d] trial record updated highest=%d", ch.id, trialID)
	}
}

func cloneTrialReward(reward trialReward) trialReward {
	cloned := reward
	cloned.items = make(map[int32]int64, len(reward.items))
	for itemID, count := range reward.items {
		cloned.items[itemID] = count
	}
	return cloned
}

// trialTeamSnapshot returns the authoritative party only when the victorious
// player is its leader. It remains as a compatibility path for a legacy solo
// trial state created before all same-layer party members joined one battle.
func (s *Server) trialTeamSnapshot(ch *channel) (int64, []int64) {
	if ch == nil || ch.session == nil {
		return 0, nil
	}
	pid := ch.session.playerID
	teamMu.Lock()
	defer teamMu.Unlock()
	_, team := s.teamEntryOf(pid)
	if team == nil || team.LeaderId != pid {
		return 0, nil
	}
	return team.LeaderId, append([]int64(nil), team.Members...)
}

func trialVictoryDestination(trialID int32) (mapID int32, x, y float32, tierComplete bool, ok bool) {
	row, ok := trialCopyByID(trialID)
	if !ok {
		return 0, 0, 0, false, false
	}
	level := int32(num(row["Level"]))
	currentMap := int32(num(row["MapId"]))
	for _, next := range tables.trialCopy {
		if int32(num(next["Level"])) == level && int32(num(next["MapId"])) == currentMap+1 {
			nextMap := int32(num(next["MapId"]))
			x, y := sceneSpawn(nextMap / 100)
			return nextMap, x, y, false, true
		}
	}
	x, y = mainCityReturnSpawn()
	return 10004, x, y, true, true
}

// advanceAfterTrialVictory is called while the winner's battleMu is already
// held. Followers are locked one at a time, rewarded, persisted and moved
// before the leader. Never lock the leader again here: sync.Mutex is not
// re-entrant and doing so would freeze the victory settlement.
func (s *Server) advanceAfterTrialVictory(ch *channel, battle *battleState, exp int64, reward trialReward, coin int64) {
	if ch == nil || ch.session == nil || battle == nil {
		return
	}
	targetMap, x, y, tierComplete, ok := trialVictoryDestination(battle.trialCopyID)
	if !ok {
		return
	}
	sourceMap := battle.mapID
	leader, memberIDs := s.trialTeamSnapshot(ch)
	movedFollowers := 0
	for _, playerID := range memberIDs {
		if playerID == ch.session.playerID {
			continue
		}
		member := s.findChannelByPlayerID(playerID)
		if member == nil || member.session == nil {
			continue
		}
		ss := member.session
		ss.battleMu.Lock()
		if !sameSceneMapID(ss.mapID, sourceMap) || ss.battle != nil {
			ss.battleMu.Unlock()
			continue
		}

		memberReward := cloneTrialReward(reward)
		rewardItems := s.applyTrialReward(member, memberReward, false)
		s.recordTaskMonsterKills(ss, battle.monsters)
		s.pushTaskProgressAfterKill(member)
		displayExp := experienceAfterMultiplier(ss, exp)
		s.gainExp(member, exp)
		ss.idleBattle = false
		s.recordTrialVictory(member, battle.trialCopyID)
		s.sendPush(member, protocol.OpM2C_SendReward, &protocol.M2C_SendReward{
			ItemList: rewardItems,
			Exp:      displayExp,
			Coin:     coin,
			ActorId:  ss.playerID,
		})
		s.changeMap(member, targetMap, x, y)
		ss.battleMu.Unlock()
		movedFollowers++
		log.Printf("[S=%d] trial follower victory leader=%d trialId=%d exp=%d map=%d",
			member.id, ch.session.playerID, battle.trialCopyID, displayExp, targetMap)
	}

	s.changeMap(ch, targetMap, x, y)
	if leader != 0 && len(memberIDs) > 0 {
		// Every ChangeMap/UnitsInMap packet must be queued before rebuilding the
		// client TeamComponent, otherwise it can reference a Unit not yet created.
		s.sendTeamSnapshot(leader, memberIDs)
	}
	if tierComplete {
		log.Printf("[S=%d] trial tier complete trialId=%d -> main city followers=%d", ch.id, battle.trialCopyID, movedFollowers)
		return
	}
	log.Printf("[S=%d] trial force next layer %d -> %d followers=%d", ch.id, sourceMap, targetMap, movedFollowers)
}

// advancePartyAfterTrialVictory runs after finishPartyVictory has rewarded and
// cleared every member battle. All ChangeMap packets are queued before the team
// snapshot so no client rebuilds TeamComponent against a member still in the
// previous scene or combat formation.
func (s *Server) advancePartyAfterTrialVictory(memberIDs []int64, battles map[int64]*battleState) {
	if len(memberIDs) < 2 {
		return
	}
	var source *battleState
	for _, playerID := range memberIDs {
		if battle := battles[playerID]; battle != nil && battle.battleType == trialBattleType {
			source = battle
			break
		}
	}
	if source == nil {
		return
	}
	targetMap, x, y, tierComplete, ok := trialVictoryDestination(source.trialCopyID)
	if !ok {
		return
	}
	moved := make([]int64, 0, len(memberIDs))
	for _, playerID := range memberIDs {
		member := s.findChannelByPlayerID(playerID)
		if member == nil || member.session == nil {
			continue
		}
		ss := member.session
		ss.battleMu.Lock()
		if ss.battle == nil && sameSceneMapID(ss.mapID, source.mapID) {
			s.changeMap(member, targetMap, x, y)
			moved = append(moved, playerID)
		}
		ss.battleMu.Unlock()
	}

	var leader int64
	var teamIDs []int64
	teamMu.Lock()
	for _, playerID := range moved {
		if _, team := s.teamEntryOf(playerID); team != nil {
			leader = team.LeaderId
			teamIDs = append([]int64(nil), team.Members...)
			break
		}
	}
	teamMu.Unlock()
	if leader != 0 && len(teamIDs) > 0 {
		s.sendTeamSnapshot(leader, teamIDs)
	}
	if tierComplete {
		log.Printf("trial party tier complete trialId=%d -> main city members=%v", source.trialCopyID, moved)
		return
	}
	log.Printf("trial party force next layer %d -> %d members=%v", source.mapID, targetMap, moved)
}

// onGetAllTrialCopyReward implements the butler's daily aggregate claim. The
// full reward is rolled and staged before any live state is changed.
func (s *Server) onGetAllTrialCopyReward(ch *channel, req *protocol.C2M_GetAllTrialCopyReword) proto.Message {
	resp := &protocol.M2C_GetAllTrialCopyReword{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Message = "请先登录"
		return resp
	}
	ss := ch.session
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	if ss.signin == nil {
		ss.signin = &signinState{}
	}
	if ss.signin.TrialHighestID <= 0 {
		resp.Message = "请先通关至少一层试炼之地"
		return resp
	}
	today := time.Now().Format("20060102")
	if ss.signin.TrialRewardDay == today {
		resp.Message = "今日挑战奖励已经领取"
		return resp
	}
	if ss.battle != nil {
		resp.Message = "战斗中无法领取挑战奖励"
		return resp
	}
	rows := trialRowsThrough(ss.signin.TrialHighestID)
	if len(rows) == 0 {
		resp.Message = "当前没有可领取的挑战奖励"
		return resp
	}
	reward := trialRewardsForRows(rows)
	stagedBag, ok := stageTrialItems(ss, reward.items)
	if !ok {
		resp.Message = "背包空间不足，请清理后再领取"
		return resp
	}

	ss.bag = stagedBag
	ss.coin += reward.coin
	ss.yuanBao += reward.yuanBao
	ss.voucher += reward.voucher
	ss.signin.TrialRewardDay = today
	// gainExp applies the active experience-card multiplier and persists all
	// state, including the claim date set above.
	displayExp := experienceAfterMultiplier(ss, reward.exp)
	s.gainExp(ch, reward.exp)
	if len(reward.items) > 0 {
		s.pushBagSnapshot(ch)
	}
	if reward.coin != 0 || reward.yuanBao != 0 || reward.voucher != 0 {
		s.pushMoney(ch)
	}
	s.sendPush(ch, protocol.OpM2C_SendReward, &protocol.M2C_SendReward{
		ItemList: trialRewardItems(reward),
		Exp:      displayExp,
		Coin:     reward.coin,
		ActorId:  ss.playerID,
	})
	resp.Message = "挑战奖励领取成功"
	log.Printf("[S=%d] claim all trial rewards highest=%d rows=%d exp=%d coin=%d items=%d",
		ch.id, ss.signin.TrialHighestID, len(rows), reward.exp, reward.coin, len(reward.items))
	return resp
}
