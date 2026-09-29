package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math"
	"math/rand"
	"sort"
	"sync"

	"google.golang.org/protobuf/proto"
	"mhqserver/protocol"
)

type familyBossBattleReward struct {
	BossID               int32
	FamilyContribution   int32
	PersonalContribution int32
}

var familyBossClaimMu sync.Mutex

type familyBossRewardClaimKey struct {
	familyID int64
	bossID   int32
	playerID int64
}

var familyBossRewardClaims = struct {
	mu     sync.Mutex
	status map[familyBossRewardClaimKey]bool
}{status: make(map[familyBossRewardClaimKey]bool)}

func familyBossRewardDB(s *Server) *sql.DB {
	if s != nil && s.store != nil && s.store.db != nil {
		return s.store.db
	}
	if globalServer != nil && globalServer.store != nil {
		return globalServer.store.db
	}
	return nil
}

func (s *Server) grantFamilyBossReward(familyID int64, bossID int32, playerID int64) bool {
	if familyID == 0 || bossID < 1 || bossID > 5 || playerID <= 0 {
		return false
	}
	if db := familyBossRewardDB(s); db != nil {
		_, err := db.Exec(`INSERT INTO family_boss_reward_claims
			(family_id, boss_id, player_id, claimed) VALUES (?, ?, ?, 0)
			ON DUPLICATE KEY UPDATE player_id = VALUES(player_id)`, familyID, bossID, playerID)
		if err != nil {
			log.Printf("grant family boss reward family=%d boss=%d player=%d: %v", familyID, bossID, playerID, err)
			return false
		}
		return true
	}
	key := familyBossRewardClaimKey{familyID: familyID, bossID: bossID, playerID: playerID}
	familyBossRewardClaims.mu.Lock()
	if _, exists := familyBossRewardClaims.status[key]; !exists {
		familyBossRewardClaims.status[key] = false
	}
	familyBossRewardClaims.mu.Unlock()
	return true
}

func (s *Server) canClaimFamilyBossReward(familyID int64, bossID int32, playerID int64) bool {
	if familyID == 0 || bossID < 1 || bossID > 5 || playerID <= 0 {
		return false
	}
	if db := familyBossRewardDB(s); db != nil {
		var claimed bool
		err := db.QueryRow(`SELECT claimed FROM family_boss_reward_claims
			WHERE family_id = ? AND boss_id = ? AND player_id = ?`, familyID, bossID, playerID).Scan(&claimed)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			log.Printf("read family boss reward family=%d boss=%d player=%d: %v", familyID, bossID, playerID, err)
		}
		return err == nil && !claimed
	}
	key := familyBossRewardClaimKey{familyID: familyID, bossID: bossID, playerID: playerID}
	familyBossRewardClaims.mu.Lock()
	claimed, exists := familyBossRewardClaims.status[key]
	familyBossRewardClaims.mu.Unlock()
	return exists && !claimed
}

func (s *Server) consumeFamilyBossReward(familyID int64, bossID int32, playerID int64) bool {
	if db := familyBossRewardDB(s); db != nil {
		result, err := db.Exec(`UPDATE family_boss_reward_claims SET claimed = 1
			WHERE family_id = ? AND boss_id = ? AND player_id = ? AND claimed = 0`, familyID, bossID, playerID)
		if err != nil {
			log.Printf("consume family boss reward family=%d boss=%d player=%d: %v", familyID, bossID, playerID, err)
			return false
		}
		rows, err := result.RowsAffected()
		return err == nil && rows == 1
	}
	key := familyBossRewardClaimKey{familyID: familyID, bossID: bossID, playerID: playerID}
	familyBossRewardClaims.mu.Lock()
	defer familyBossRewardClaims.mu.Unlock()
	claimed, exists := familyBossRewardClaims.status[key]
	if !exists || claimed {
		return false
	}
	familyBossRewardClaims.status[key] = true
	return true
}

func clearFamilyBossRewardClaims(familyID int64) {
	clearFamilyBossRewardClaimsForBoss(familyID, 0)
}

func clearFamilyBossRewardClaimsForBoss(familyID int64, bossID int32) {
	familyBossRewardClaims.mu.Lock()
	for key := range familyBossRewardClaims.status {
		if key.familyID == familyID && (bossID == 0 || key.bossID == bossID) {
			delete(familyBossRewardClaims.status, key)
		}
	}
	familyBossRewardClaims.mu.Unlock()
	if db := familyBossRewardDB(globalServer); db != nil {
		if bossID == 0 {
			_, _ = db.Exec(`DELETE FROM family_boss_reward_claims WHERE family_id = ?`, familyID)
			return
		}
		_, _ = db.Exec(`DELETE FROM family_boss_reward_claims WHERE family_id = ? AND boss_id = ?`, familyID, bossID)
	}
}

func (s *Server) settleFamilyBossBattle(ch *channel, battle *battleState, victory bool) familyBossBattleReward {
	var reward familyBossBattleReward
	if ch == nil || ch.session == nil || battle == nil || battle.mapID >= 0 || battle.familyBossSettled {
		return reward
	}
	battle.familyBossSettled = true
	reward.BossID = -battle.mapID
	if reward.BossID < 1 || reward.BossID > 5 || ch.session.familyID == 0 || tables == nil {
		return reward
	}
	row := tables.familyBossConfig[int64(reward.BossID)]
	if row == nil {
		return reward
	}
	recordFamilyBossParticipation(ch.session.familyID, reward.BossID, ch.session.playerID)

	reward.PersonalContribution = int32(num(row["PersonalContribute"]))
	reward.FamilyContribution = int32(num(row["Contribute"]))
	if reward.PersonalContribution < 0 || reward.FamilyContribution < 0 ||
		int64(ch.session.personalContribute) > int64(math.MaxInt32)-int64(reward.PersonalContribution) ||
		!ch.session.canApplyCurrencyDelta(currencyDelta{familyContribute: int64(reward.FamilyContribution)}) {
		log.Printf("[S=%d] family boss contribution exceeds balance limit: %d", ch.id, reward.FamilyContribution)
		return reward
	}
	ch.session.personalContribute += reward.PersonalContribution
	// Family contribution is a bounded int32 currency; the preflight above
	// guarantees this addition cannot wrap into a negative balance.
	ch.session.familyContribute += reward.FamilyContribution
	// Contribution is a visible NumericType currency; synchronize it at the
	// same settlement point as the server-side counters.
	s.pushMoney(ch)
	// The native boss ranking only contains damage and treatment columns, so it
	// cannot show the two contribution currencies. Send an explicit native tip
	// at settlement to make the awarded values visible without changing the
	// reward-claim protocol or granting them a second time.
	s.sendPush(ch, protocol.OpM2C_SendTip, &protocol.M2C_SendTip{
		Message: fmt.Sprintf("家族BOSS结算：家族贡献 +%d，个人贡献 +%d", reward.FamilyContribution, reward.PersonalContribution),
		ActorId: ch.session.playerID,
	})

	familyBossClaimMu.Lock()
	defer familyBossClaimMu.Unlock()
	state := familyBossState(ch.session.familyID, reward.BossID)
	stateChanged := false
	if victory {
		state.Hp = 0
		state.HasReward = true
		rememberFamilyBossDeath(state)
		stateChanged = true
		s.grantFamilyBossReward(ch.session.familyID, reward.BossID, ch.session.playerID)
	} else if len(battle.monsters) > 0 {
		hp := battle.monsters[0].hp
		if hp < 0 {
			hp = 0
		}
		state.Hp = hp
		if hp > 0 {
			state.HasReward = false
			state.DeadAt = 0
		} else {
			rememberFamilyBossDeath(state)
		}
		stateChanged = true
	}
	if stateChanged {
		saveFamilyBossStateDB(ch.session.familyID, familyBossAllStates(ch.session.familyID))
	}
	// The damage map is independent from the shared claim flag. Persist it at
	// settlement so the ranking remains available after a restart, including
	// while the family is waiting for its one shared reward claim.
	saveFamilyBossDamageDB(ch.session.familyID, reward.BossID)

	log.Printf("[S=%d] family boss %d settled victory=%v family=%d personal=%d claim=%v",
		ch.id, reward.BossID, victory, reward.FamilyContribution, reward.PersonalContribution, state.HasReward)
	return reward
}

// onGetFamilyBossReward follows the client's native per-character claim flow.
// Each participating character has one persisted claim; claiming does not
// revive the defeated boss or erase the family's participation record.
func (s *Server) onGetFamilyBossReward(ch *channel, req *protocol.C2M_GetFamilyBossReward) proto.Message {
	resp := &protocol.M2C_GetFamilyBossReward{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.familyID == 0 {
		resp.Message = "没有家族"
		return resp
	}
	if req.BossId < 1 || req.BossId > 5 {
		resp.Message = "BOSS 不存在"
		return resp
	}
	row := tables.familyBossConfig[int64(req.BossId)]
	if row == nil {
		resp.Message = "BOSS 配置不存在"
		return resp
	}

	familyBossClaimMu.Lock()
	defer familyBossClaimMu.Unlock()
	ss := ch.session
	state := familyBossState(ss.familyID, req.BossId)
	if state.Hp > 0 || !s.canClaimFamilyBossReward(ss.familyID, req.BossId, ss.playerID) {
		resp.Message = "BOSS 奖励不可领取"
		return resp
	}

	reward := newTrialReward()
	rollParentset(int32(num(row["Dropasubset"])), &reward)
	if itemID := rollFamilyBossBonusItem(row, rand.Float64); itemID > 0 {
		reward.addDrop(itemID, 1)
	}
	stagedBag, ok := stageTrialItems(ss, reward.items)
	if !ok {
		resp.Message = "背包空间不足"
		return resp
	}
	if !ss.canApplyCurrencyDelta(currencyDelta{coin: reward.coin, yuanBao: reward.yuanBao, voucher: reward.voucher}) {
		resp.Message = "奖励货币已达上限"
		return resp
	}
	if !s.consumeFamilyBossReward(ss.familyID, req.BossId, ss.playerID) {
		resp.Message = "BOSS 奖励已领取"
		return resp
	}

	ss.bag = stagedBag
	s.applyCurrencyDelta(ch, currencyDelta{coin: reward.coin, yuanBao: reward.yuanBao, voucher: reward.voucher})
	if reward.exp > 0 {
		reward.exp = s.gainExp(ch, reward.exp)
	} else {
		s.saveData(ch)
	}
	if len(reward.items) > 0 {
		s.pushBagSnapshot(ch)
	}

	base, err := proto.Marshal(resp)
	if err != nil {
		return resp
	}
	items := familyBossRewardItems(reward)
	for _, item := range items {
		raw, marshalErr := proto.Marshal(item)
		if marshalErr == nil {
			base = pbAppendBytes(base, 1, raw)
		}
	}
	s.sendRawPush(ch, protocol.OpM2C_GetFamilyBossReward, base)
	log.Printf("[S=%d] get family boss reward boss=%d parentset=%d items=%d",
		ch.id, req.BossId, int32(num(row["Dropasubset"])), len(items))
	return nil
}

func familyBossRewardItems(reward trialReward) []*protocol.ItemInfo {
	counts := make(map[int32]int64, len(reward.items)+4)
	for itemID, count := range reward.items {
		// 110205 is rendered by the native client from NumericType.Gem.  Do
		// not include it in ItemList, otherwise the reward panel classifies it
		// as a normal consumable.  The claim path still persists the balance in
		// the hidden compatibility stack and pushes NumericType=1041.
		if isStarCoinItem(itemID) {
			continue
		}
		if canAddInt64Balance(counts[itemID], count) {
			counts[itemID] += count
		}
	}
	if canAddInt64Balance(counts[110201], reward.exp) {
		counts[110201] += reward.exp
	}
	if canAddInt64Balance(counts[110202], reward.yuanBao) {
		counts[110202] += reward.yuanBao
	}
	if canAddInt64Balance(counts[110203], reward.coin) {
		counts[110203] += reward.coin
	}
	if canAddInt64Balance(counts[110204], reward.voucher) {
		counts[110204] += reward.voucher
	}
	ids := make([]int, 0, len(counts))
	for itemID, count := range counts {
		if count > 0 {
			ids = append(ids, int(itemID))
		}
	}
	sort.Ints(ids)
	var items []*protocol.ItemInfo
	for _, id := range ids {
		count := counts[int32(id)]
		for count > 0 {
			chunk := count
			if chunk > math.MaxInt32 {
				chunk = math.MaxInt32
			}
			items = append(items, &protocol.ItemInfo{Id: int32(id), Count: int32(chunk)})
			count -= chunk
		}
	}
	return items
}

func rollFamilyBossBonusItem(row map[string]interface{}, roll func() float64) int32 {
	defaultChance, defaultSkinShare, _, _ := gameplayFamilyBossSettings()
	chance := familyBossConfigRatio(row, "BonusDropChance", defaultChance)
	if chance <= 0 || unitRoll(roll()) >= chance {
		return 0
	}
	gems, skins := familyBossBonusPools()
	if len(gems) == 0 && len(skins) == 0 {
		return 0
	}
	pool := gems
	skinShare := familyBossConfigRatio(row, "BonusSkinShare", defaultSkinShare)
	chooseSkin := len(skins) > 0 && (len(gems) == 0 || unitRoll(roll()) < skinShare)
	if chooseSkin {
		pool = skins
	}
	return pool[int(unitRoll(roll())*float64(len(pool)))]
}

func familyBossConfigRatio(row map[string]interface{}, key string, fallback float64) float64 {
	value, ok := row[key]
	if !ok {
		return fallback
	}
	ratio := numf(value)
	if ratio > 1 {
		ratio /= 100
	}
	if ratio < 0 {
		return 0
	}
	if ratio > 1 {
		return 1
	}
	return ratio
}

func unitRoll(value float64) float64 {
	if math.IsNaN(value) || value <= 0 {
		return 0
	}
	if value >= 1 {
		return math.Nextafter(1, 0)
	}
	return value
}

// familyBossBonusPools accepts only gems and wearable skins. SkinBase alone
// also contains model records, so an EquipBase intersection is required.
func familyBossBonusPools() (gems []int32, skins []int32) {
	if tables == nil {
		return nil, nil
	}
	_, _, configuredGems, configuredSkins := gameplayFamilyBossSettings()
	if len(configuredGems) > 0 {
		for _, id := range configuredGems {
			if row := tables.materialBase[int64(id)]; row != nil && int32(num(row["MaterialType"])) == 2 {
				gems = append(gems, id)
			}
		}
	} else {
		for id, row := range tables.materialBase {
			if id > 0 && id <= math.MaxInt32 && int32(num(row["MaterialType"])) == 2 {
				gems = append(gems, int32(id))
			}
		}
	}
	if len(configuredSkins) > 0 {
		for _, id := range configuredSkins {
			if tables.skinBase[int64(id)] != nil && tables.equipBase[int64(id)] != nil {
				skins = append(skins, id)
			}
		}
	} else {
		for id := range tables.skinBase {
			if id <= 0 || id > math.MaxInt32 {
				continue
			}
			if _, ok := tables.equipBase[id]; ok {
				skins = append(skins, int32(id))
			}
		}
	}
	sort.Slice(gems, func(i, j int) bool { return gems[i] < gems[j] })
	sort.Slice(skins, func(i, j int) bool { return skins[i] < skins[j] })
	return gems, skins
}
