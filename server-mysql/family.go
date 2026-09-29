package main

import (
	"errors"
	"log"
	"math"
	"sort"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// family.go：家族系统（20123-20152 段）。
//
// 协议（文档 20 同段 + protocol_dump）：
//   20123/24 CrateFamily{Name} → Info
//   20125/26 GetFamily → Info + FamilyMemberInfoList(tag1) + RequestInfoList(tag2)
//   20127/28 FindFamily{Name} → Info
//   20129/30 DeleteFamily（队长解散）
//   20131/32 RequestEnterFamily{Name} → 申请（加入家族 requests 列表）
//   20133/34 LeaveFamily（退出）
//   20135/36 HandleEnterFamiy{IsAgree, Id}（队长处理申请：同意入族/拒绝）
//   20137/38 DeleteFamilyMember{UnitId}（队长踢人）
//   20139/41 GetFamilyBossInfo → BossInfoList(tag1) = [BossInfo{Index,Hp,MaxHp,HasReward}]
//   20142/43 StartFamilyBossFight{BossId} → 开家族 BOSS 战
//   20144 SendFamilyBossInfo{BossId,UnitId,Hp}（进家族场景推送）
//   20145 ReSendFamilyBossInfo{BossId,UnitId,Hp}（血同步）
//   20148/49 GetBossDamageMap{BossId} → BossDamageMap{TotalDamage}(tag2) +
//       BossDamagePerMemberMap{Id,Name,Damage,Treat}(tag1)
//   20150/52 GetFamilyBossReward{BossId} → ItemList(tag1) = [ItemInfo{Id,Count}]
//
// FamilyInfo{Level=1, Hornor=2, Name=3, Notice=5, PositinMapList=4(字段级)}
// FamilyMemberInfo{Id=1, Name=2, Job=3, Level=4, LastLginTime=5, ContributionList=6(字段级)}
// FamilyContributionMap{Id=1, Value=2}
//
// 数据：families、family_members、family_requests 表；players 表 family_id/
// family_contribute/personal_contribute 列。家族 BOSS 血量按家族独立并写入
// family_boss_states；每次伤害、退出和击杀均落库，服务重启后继续原血量。

// familyMember 家族成员。
type familyMember struct {
	ID        int64  `json:"id"`
	Name      string `json:"n"`
	Job       int32  `json:"j"`
	Level     int32  `json:"l"`
	LastLogin int64  `json:"t"`
}

// familyBossHP 家族 BOSS 状态（每家族 5 个）。
type familyBossHP struct {
	Hp        int32 // 0 means the boss is currently dead
	MaxHp     int32
	HasReward bool
	DeadAt    int64 // unix ms of the killing blow; 0 if alive
}

type familyBossDamageKey struct {
	familyID int64
	bossID   int32
}

var familyBossDamage = struct {
	mu           sync.Mutex
	m            map[familyBossDamageKey]map[int64]int32
	treat        map[familyBossDamageKey]map[int64]int32
	participants map[familyBossDamageKey]map[int64]bool
	loaded       map[familyBossDamageKey]bool
}{
	m:            make(map[familyBossDamageKey]map[int64]int32),
	treat:        make(map[familyBossDamageKey]map[int64]int32),
	participants: make(map[familyBossDamageKey]map[int64]bool),
	loaded:       make(map[familyBossDamageKey]bool),
}

// ensureFamilyBossDamageLoadedLocked restores a persisted ranking the first
// time a family/boss pair is observed after a process restart. The caller
// must hold familyBossDamage.mu.
func ensureFamilyBossDamageLoadedLocked(key familyBossDamageKey) {
	if familyBossDamage.loaded[key] {
		return
	}
	familyBossDamage.loaded[key] = true
	if globalServer == nil || globalServer.store == nil || globalServer.store.db == nil {
		return
	}
	rows, err := globalServer.store.db.Query(`SELECT player_id, damage, treat, participated
		FROM family_boss_damage WHERE family_id = ? AND boss_id = ?`, key.familyID, key.bossID)
	if err != nil {
		log.Printf("load family boss damage family=%d boss=%d: %v", key.familyID, key.bossID, err)
		return
	}
	defer rows.Close()
	loaded := make(map[int64]int32)
	loadedTreat := make(map[int64]int32)
	loadedParticipants := make(map[int64]bool)
	for rows.Next() {
		var playerID int64
		var damage, treat int64
		var participated bool
		if err := rows.Scan(&playerID, &damage, &treat, &participated); err != nil {
			log.Printf("scan family boss damage family=%d boss=%d: %v", key.familyID, key.bossID, err)
			continue
		}
		if playerID <= 0 {
			continue
		}
		if participated {
			loadedParticipants[playerID] = true
		}
		if damage < 0 {
			damage = 0
		}
		if damage > math.MaxInt32 {
			damage = math.MaxInt32
		}
		if damage > 0 {
			loaded[playerID] = int32(damage)
		}
		if treat > math.MaxInt32 {
			treat = math.MaxInt32
		}
		if treat > 0 {
			loadedTreat[playerID] = int32(treat)
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("read family boss damage family=%d boss=%d: %v", key.familyID, key.bossID, err)
	}
	if len(loaded) > 0 {
		familyBossDamage.m[key] = loaded
	}
	if len(loadedTreat) > 0 {
		familyBossDamage.treat[key] = loadedTreat
	}
	if len(loadedParticipants) > 0 {
		familyBossDamage.participants[key] = loadedParticipants
	}
}

func saveFamilyBossDamageDB(familyID int64, bossID int32) {
	if familyID == 0 || bossID < 1 || bossID > 5 || globalServer == nil ||
		globalServer.store == nil || globalServer.store.db == nil {
		return
	}
	key := familyBossDamageKey{familyID: familyID, bossID: bossID}
	familyBossDamage.mu.Lock()
	ensureFamilyBossDamageLoadedLocked(key)
	snapshot := make(map[int64]int32, len(familyBossDamage.m[key]))
	for playerID, damage := range familyBossDamage.m[key] {
		snapshot[playerID] = damage
	}
	treatment := make(map[int64]int32, len(familyBossDamage.treat[key]))
	for playerID, amount := range familyBossDamage.treat[key] {
		treatment[playerID] = amount
	}
	participants := make(map[int64]bool, len(familyBossDamage.participants[key]))
	for playerID, participated := range familyBossDamage.participants[key] {
		participants[playerID] = participated
	}
	familyBossDamage.mu.Unlock()
	players := make(map[int64]struct{}, len(snapshot)+len(treatment)+len(participants))
	for playerID := range snapshot {
		players[playerID] = struct{}{}
	}
	for playerID := range treatment {
		players[playerID] = struct{}{}
	}
	for playerID := range participants {
		players[playerID] = struct{}{}
	}
	for playerID := range players {
		damage := snapshot[playerID]
		amount := treatment[playerID]
		participated := participants[playerID]
		if _, err := globalServer.store.db.Exec(`INSERT INTO family_boss_damage
			(family_id, boss_id, player_id, damage, treat, participated) VALUES (?, ?, ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE damage = VALUES(damage), treat = VALUES(treat), participated = VALUES(participated)`,
			familyID, bossID, playerID, damage, amount, participated); err != nil {
			log.Printf("save family boss damage family=%d boss=%d player=%d: %v", familyID, bossID, playerID, err)
		}
	}
}

func recordFamilyBossDamage(familyID int64, bossID int32, playerID int64, amount int32) {
	recordFamilyBossStat(familyID, bossID, playerID, amount, false)
}

func recordFamilyBossTreatment(familyID int64, bossID int32, playerID int64, amount int32) {
	recordFamilyBossStat(familyID, bossID, playerID, amount, true)
}

func recordFamilyBossStat(familyID int64, bossID int32, playerID int64, amount int32, treatment bool) {
	if familyID == 0 || bossID < 1 || bossID > 5 || playerID <= 0 || amount <= 0 {
		return
	}
	familyBossDamage.mu.Lock()
	key := familyBossDamageKey{familyID: familyID, bossID: bossID}
	ensureFamilyBossDamageLoadedLocked(key)
	if familyBossDamage.participants[key] == nil {
		familyBossDamage.participants[key] = make(map[int64]bool)
	}
	familyBossDamage.participants[key][playerID] = true
	target := familyBossDamage.m
	if treatment {
		target = familyBossDamage.treat
	}
	if target[key] == nil {
		target[key] = make(map[int64]int32)
	}
	previous := target[key][playerID]
	if int64(amount) > int64(math.MaxInt32)-int64(previous) {
		target[key][playerID] = math.MaxInt32
	} else {
		target[key][playerID] = previous + amount
	}
	// Persist the cumulative value immediately. HP is already persisted on
	// every battle checkpoint, so a restart cannot lose the damage ranking that
	// belongs with that HP snapshot.
	familyBossDamage.mu.Unlock()
	saveFamilyBossDamageDB(familyID, bossID)
}

func recordFamilyBossParticipation(familyID int64, bossID int32, playerID int64) {
	if familyID == 0 || bossID < 1 || bossID > 5 || playerID <= 0 {
		return
	}
	key := familyBossDamageKey{familyID: familyID, bossID: bossID}
	familyBossDamage.mu.Lock()
	ensureFamilyBossDamageLoadedLocked(key)
	if familyBossDamage.participants[key] == nil {
		familyBossDamage.participants[key] = make(map[int64]bool)
	}
	familyBossDamage.participants[key][playerID] = true
	familyBossDamage.mu.Unlock()
	saveFamilyBossDamageDB(familyID, bossID)
}

func familyBossDamageSnapshot(familyID int64, bossID int32) map[int64]int32 {
	return familyBossStatSnapshot(familyID, bossID, false)
}

func familyBossTreatmentSnapshot(familyID int64, bossID int32) map[int64]int32 {
	return familyBossStatSnapshot(familyID, bossID, true)
}

func familyBossParticipantSnapshot(familyID int64, bossID int32) map[int64]bool {
	familyBossDamage.mu.Lock()
	defer familyBossDamage.mu.Unlock()
	key := familyBossDamageKey{familyID: familyID, bossID: bossID}
	ensureFamilyBossDamageLoadedLocked(key)
	out := make(map[int64]bool, len(familyBossDamage.participants[key]))
	for playerID, participated := range familyBossDamage.participants[key] {
		out[playerID] = participated
	}
	return out
}

func familyBossStatSnapshot(familyID int64, bossID int32, treatment bool) map[int64]int32 {
	familyBossDamage.mu.Lock()
	defer familyBossDamage.mu.Unlock()
	key := familyBossDamageKey{familyID: familyID, bossID: bossID}
	ensureFamilyBossDamageLoadedLocked(key)
	input := familyBossDamage.m[key]
	if treatment {
		input = familyBossDamage.treat[key]
	}
	out := make(map[int64]int32, len(input))
	for id, amount := range input {
		out[id] = amount
	}
	return out
}

func clearFamilyBossDamage(familyID int64, bossID int32) {
	familyBossDamage.mu.Lock()
	key := familyBossDamageKey{familyID: familyID, bossID: bossID}
	delete(familyBossDamage.m, key)
	delete(familyBossDamage.treat, key)
	delete(familyBossDamage.participants, key)
	delete(familyBossDamage.loaded, key)
	familyBossDamage.mu.Unlock()
	if globalServer != nil && globalServer.store != nil && globalServer.store.db != nil {
		if _, err := globalServer.store.db.Exec(`DELETE FROM family_boss_damage WHERE family_id = ? AND boss_id = ?`, familyID, bossID); err != nil {
			log.Printf("clear family boss damage family=%d boss=%d: %v", familyID, bossID, err)
		}
	}
}

// familyBossStates 家族 id → 5 个 BOSS 状态（DB 持久化，重启保留血量）。
var familyBossStates = struct {
	mu sync.Mutex
	m  map[int64]*[5]familyBossHP
}{m: make(map[int64]*[5]familyBossHP)}

// familyBossMaxHP returns the same authoritative HP used when the combat unit
// is built: FamilyBossConfig.MonsterId -> MonsterBase.Hp.
func familyBossMaxHP(bossID int32) int32 {
	if tables != nil {
		config := tables.familyBossConfig[int64(bossID)]
		monsterID := int32(num(config["MonsterId"]))
		if row := tables.monsterBase[int64(monsterID)]; row != nil {
			if hp := int32(num(row["Hp"])); hp > 0 {
				return hp
			}
		}
	}
	return 1
}

// familyBossState 返回某家族的 BOSS 状态（首次访问从 DB 恢复；无记录初始化满血并落库）。
func familyBossState(familyID int64, bossID int32) *familyBossHP {
	familyBossStates.mu.Lock()
	defer familyBossStates.mu.Unlock()
	arr := familyBossStates.m[familyID]
	if arr == nil {
		arr = loadFamilyBossStateDB(familyID)
		if arr == nil {
			arr = &[5]familyBossHP{}
			needInsert := false
			for i := 0; i < 5; i++ {
				arr[i].MaxHp = familyBossMaxHP(int32(i + 1))
				arr[i].Hp = arr[i].MaxHp
				if arr[i].MaxHp > 0 {
					needInsert = true
				}
			}
			if needInsert {
				saveFamilyBossStateDB(familyID, arr)
			}
		}
		familyBossStates.m[familyID] = arr
	}
	refreshFamilyBossStatesLocked(familyID, arr, familyBossNow())
	if bossID < 1 || bossID > 5 {
		return &arr[0]
	}
	return &arr[bossID-1]
}

// familyBossAllStates 返回某家族完整 5 BOSS 状态数组（内部上锁，供持久化）。
func familyBossAllStates(familyID int64) *[5]familyBossHP {
	familyBossStates.mu.Lock()
	defer familyBossStates.mu.Unlock()
	arr := familyBossStates.m[familyID]
	if arr != nil {
		return arr
	}
	return nil
}

// loadFamilyBossStateDB 从 DB 读某家族 5 个 BOSS 状态（无记录返回 nil）。
func loadFamilyBossStateDB(familyID int64) *[5]familyBossHP {
	if globalServer == nil || globalServer.store == nil {
		return nil
	}
	arr := &[5]familyBossHP{}
	rows, err := globalServer.store.db.Query(`SELECT boss_id, hp, max_hp, has_reward, dead_at
		FROM family_boss_states WHERE family_id = ?`, familyID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var bid int32
		var hp, maxHp int32
		var hasReward int
		var deadAt int64
		if err := rows.Scan(&bid, &hp, &maxHp, &hasReward, &deadAt); err != nil {
			continue
		}
		if bid < 1 || bid > 5 {
			continue
		}
		if deadAt < 0 {
			deadAt = 0
		}
		arr[bid-1] = familyBossHP{Hp: hp, MaxHp: maxHp, HasReward: hasReward != 0, DeadAt: deadAt}
		found = true
	}
	if !found {
		return nil
	}
	return arr
}

// saveFamilyBossStateDB 写回某家族 5 个 BOSS 状态到 DB（满血行初始化 / 战斗后扣血或击杀时调用）。
func saveFamilyBossStateDB(familyID int64, arr *[5]familyBossHP) {
	if globalServer == nil || globalServer.store == nil || arr == nil {
		return
	}
	for i := 0; i < 5; i++ {
		r := 0
		if arr[i].HasReward {
			r = 1
		}
		globalServer.store.db.Exec(`INSERT INTO family_boss_states (family_id, boss_id, hp, max_hp, has_reward, dead_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE hp=VALUES(hp), max_hp=VALUES(max_hp), has_reward=VALUES(has_reward), dead_at=VALUES(dead_at)`,
			familyID, i+1, arr[i].Hp, arr[i].MaxHp, r, arr[i].DeadAt)
	}
}

// persistFamilyBossProgress writes the exact int32 combat HP to the family
// state. The generic 20169 client packet carries float32 and can round a
// one-point change at boss-scale HP, so it must never be used as persistence
// authority.
func persistFamilyBossProgress(familyID int64, battle *battleState) {
	if familyID == 0 || battle == nil || battle.mapID >= 0 {
		return
	}
	bossID := -battle.mapID
	if bossID < 1 || bossID > 5 || len(battle.monsters) == 0 {
		return
	}
	hp := battle.monsters[0].hp
	if hp < 0 {
		hp = 0
	}
	st := familyBossState(familyID, bossID)
	st.Hp = hp
	if hp > 0 {
		st.HasReward = false
		st.DeadAt = 0
	} else {
		rememberFamilyBossDeath(st)
	}
	saveFamilyBossStateDB(familyID, familyBossAllStates(familyID))
}

// deleteFamilyBossStateDB 解散家族时清除其 BOSS 状态。
func deleteFamilyBossStateDB(familyID int64) {
	clearFamilyBossRewardClaims(familyID)
	if globalServer == nil || globalServer.store == nil {
		return
	}
	globalServer.store.db.Exec(`DELETE FROM family_boss_states WHERE family_id = ?`, familyID)
	globalServer.store.db.Exec(`DELETE FROM family_boss_damage WHERE family_id = ?`, familyID)
	globalServer.store.db.Exec(`DELETE FROM family_boss_reward_claims WHERE family_id = ?`, familyID)
}

// ===================== 家族基础 =====================

// familyInfoProto 组装 FamilyInfo（Level/Hornor/Name/Notice）。
func familyInfoProto(f *familyRow) *protocol.FamilyInfo {
	if f == nil {
		return nil
	}
	return &protocol.FamilyInfo{
		Level:  f.Level,
		Hornor: f.Hornor,
		Name:   f.Name,
		Notice: f.Notice,
	}
}

// familyInfoWire includes the field-level PositinMapList declared by the
// Unity client but omitted from the generated Go message.
func familyInfoWire(f *familyRow) []byte {
	if f == nil {
		return nil
	}
	base, err := proto.Marshal(familyInfoProto(f))
	if err != nil {
		return nil
	}
	leaderName := ""
	for _, member := range f.Members {
		if member != nil && member.ID == f.Leader {
			leaderName = member.Name
			break
		}
	}
	if leaderName == "" {
		leaderName = f.Name
	}
	position, err := proto.Marshal(&protocol.FamilyPositionMap{
		Name: leaderName, Position: protocol.FamilyPosition_FamilyLeader,
	})
	if err == nil {
		base = pbAppendBytes(base, 4, position)
	}
	return base
}

func clientLastLogin(stored int64, online bool) int64 {
	if online {
		return 0
	}
	if stored > 0 && stored < 1_000_000_000_000 {
		return stored * 1000
	}
	return stored
}

func sexTypeOfJob(jobID int32) protocol.SexType {
	if jobTypeOf(jobID) == 3 {
		return protocol.SexType_Famale
	}
	return protocol.SexType_Male
}

// 20123 → 20124：创建家族。
func (s *Server) onCrateFamily(ch *channel, req *protocol.C2M_CrateFamily) proto.Message {
	resp := &protocol.M2C_CrateFamily{RpcId: req.RpcId}
	ss := ch.session
	if len(req.Name) == 0 || len([]rune(req.Name)) > 12 {
		resp.Message = "家族名不合法"
		return resp
	}
	// 先清悬空归属再判「已有家族」：family_id 指向已删除的家族时，直接拒绝会让
	// 角色永远建不了家族（见 family_selfheal.go）。
	s.healDanglingFamily(ch)
	if ss.familyID != 0 {
		resp.Message = "已有家族"
		return resp
	}
	// 名字查重
	var cnt int
	s.store.db.QueryRow(`SELECT COUNT(*) FROM families WHERE name = ?`, req.Name).Scan(&cnt)
	if cnt > 0 {
		resp.Message = "家族名已存在"
		return resp
	}
	tx, err := s.store.db.Begin()
	if err != nil {
		resp.Message = "创建失败"
		return resp
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO families (name, leader) VALUES (?, ?)`, req.Name, ss.playerID)
	if err != nil {
		resp.Message = "创建失败"
		return resp
	}
	fid, _ := res.LastInsertId()
	if _, err := tx.Exec(`INSERT INTO family_members (family_id, player_id, name, job_id, level, last_login)
		VALUES (?, ?, ?, ?, ?, ?)`, fid, ss.playerID, ss.name, jobTypeOf(ss.jobID), ss.level, time.Now().Unix()); err != nil {
		resp.Message = "创建失败"
		return resp
	}
	if _, err := tx.Exec(`UPDATE players SET family_id = ? WHERE id = ?`, fid, ss.playerID); err != nil {
		resp.Message = "创建失败"
		return resp
	}
	if err := tx.Commit(); err != nil {
		resp.Message = "创建失败"
		return resp
	}
	ss.familyID = fid
	s.saveData(ch)
	s.pushMoney(ch)
	s.pushUnitCharacter(ch)
	s.broadcastPlayerUpdate(ch)
	log.Printf("[S=%d] create family %q id=%d leader=%d", ch.id, req.Name, fid, ss.playerID)
	resp.Info = &protocol.FamilyInfo{Level: 1, Name: req.Name}
	return resp
}

// 20125 → 20126：获取我的家族（Info + 成员 + 申请列表）。
// M2C_GetFamily 的 FamilyMemberInfoList(tag1)/RequestInfoList(tag2) 为字段级 List
// （生成代码不含）→ 手工编码 raw 20126 响应。
func (s *Server) onGetFamily(ch *channel, req *protocol.C2M_GetFamily) proto.Message {
	resp := &protocol.M2C_GetFamily{RpcId: req.RpcId}
	ss := ch.session
	// 家族行被绕过 onDeleteFamily 的删除清掉后 family_id 会悬空，此路每次都报
	// 「家族不存在」且永远不自愈。先清悬空归属再走正常分支（见 family_selfheal.go）。
	s.healDanglingFamily(ch)
	if ss.familyID == 0 {
		resp.Message = "没有家族"
		return resp
	}
	row := s.loadFamilyRow(ss.familyID)
	if row == nil {
		resp.Message = "家族不存在"
		return resp
	}
	// Append an enriched Info message below so the client receives its
	// field-level position list as well as the generated scalar properties.
	resp.Info = nil
	base, err := proto.Marshal(resp)
	if err != nil {
		resp.Message = "序列化失败"
		return resp
	}
	if info := familyInfoWire(row); len(info) > 0 {
		base = pbAppendBytes(base, 3, info)
	}
	for _, m := range row.Members {
		if m == nil {
			continue
		}
		mi := &protocol.FamilyMemberInfo{
			Id:           m.ID,
			Name:         m.Name,
			Job:          protocol.JobType(jobTypeOf(m.Job)),
			Level:        m.Level,
			LastLginTime: clientLastLogin(m.LastLogin, s.findChannelByPlayerID(m.ID) != nil),
		}
		raw, err2 := proto.Marshal(mi)
		if err2 == nil {
			raw = appendFamilyContributionList(raw, 6, mi, m.ID,
				s.personalContributeOf(ss, m.ID))
			base = pbAppendBytes(base, 1, raw)
		}
	}
	for _, rid := range row.Requests {
		var nm string
		var job, lv int32
		s.store.db.QueryRow(`SELECT name, job_id, level FROM players WHERE id = ?`, rid).Scan(&nm, &job, &lv)
		if nm == "" {
			continue
		}
		ri, _ := proto.Marshal(&protocol.RequestAddFriendInfo{
			Id: rid, Name: nm, Job: protocol.JobType(jobTypeOf(job)),
			Level: lv, Sex: sexTypeOfJob(job),
		})
		base = pbAppendBytes(base, 2, ri)
	}
	s.sendRawPush(ch, protocol.OpM2C_GetFamily, base)
	log.Printf("[S=%d] get family id=%d members=%d requests=%d", ch.id, ss.familyID, len(row.Members), len(row.Requests))
	return nil
}

// 20127 → 20128：按名字查找家族。
func (s *Server) onFindFamily(ch *channel, req *protocol.C2M_FindFamily) proto.Message {
	resp := &protocol.M2C_FindFamily{RpcId: req.RpcId}
	var id int64
	if err := s.store.db.QueryRow(`SELECT id FROM families WHERE name = ?`, req.Name).Scan(&id); err != nil {
		resp.Message = "家族不存在"
		return resp
	}
	row := s.loadFamilyRow(id)
	if row == nil {
		resp.Message = "家族不存在"
		return resp
	}
	resp.Info = familyInfoProto(row)
	return resp
}

// 20129 → 20130：解散家族（仅队长）。
func (s *Server) onDeleteFamily(ch *channel, req *protocol.C2M_DeleteFamily) proto.Message {
	resp := &protocol.M2C_DeleteFamily{RpcId: req.RpcId}
	ss := ch.session
	row := s.loadFamilyRow(ss.familyID)
	if row == nil {
		resp.Message = "没有家族"
		return resp
	}
	if row.Leader != ss.playerID {
		resp.Message = "只有族长可以解散家族"
		return resp
	}
	for _, m := range row.Members {
		s.store.db.Exec(`UPDATE players SET family_id = 0, family_contribute = 0, personal_contribute = 0 WHERE id = ?`, m.ID)
		if rc := s.findChannelByPlayerID(m.ID); rc != nil && rc.session != nil {
			rc.session.familyID = 0
			rc.session.familyContribute = 0
			rc.session.personalContribute = 0
			s.saveData(rc)
			s.pushMoney(rc)
			s.pushUnitCharacter(rc)
		}
	}
	s.store.db.Exec(`DELETE FROM families WHERE id = ?`, ss.familyID)
	familyBossStates.mu.Lock()
	delete(familyBossStates.m, ss.familyID)
	familyBossStates.mu.Unlock()
	deleteFamilyBossStateDB(ss.familyID) // 家族 BOSS 状态一并清除（走数据库）
	ss.familyID = 0
	ss.familyContribute = 0
	ss.personalContribute = 0
	s.saveData(ch)
	s.pushMoney(ch)
	s.pushUnitCharacter(ch)
	s.broadcastPlayerUpdate(ch)
	log.Printf("[S=%d] delete family id=%d", ch.id, row.ID)
	return resp
}

// 20131 → 20132：申请加入家族（按名字）。
func (s *Server) onRequestEnterFamily(ch *channel, req *protocol.C2M_RequestEnterFamily) proto.Message {
	resp := &protocol.M2C_RequestEnterFamily{RpcId: req.RpcId}
	ss := ch.session
	// 同 onCrateFamily：悬空 family_id 会让角色申请不了任何家族。
	s.healDanglingFamily(ch)
	if ss.familyID != 0 {
		resp.Message = "已有家族"
		return resp
	}
	var fid int64
	if err := s.store.db.QueryRow(`SELECT id FROM families WHERE name = ?`, req.Name).Scan(&fid); err != nil {
		resp.Message = "家族不存在"
		return resp
	}
	result, err := s.store.db.Exec(`INSERT IGNORE INTO family_requests (family_id, player_id) VALUES (?, ?)`, fid, ss.playerID)
	if err != nil {
		log.Printf("[S=%d] request enter family id=%d player=%d: %v", ch.id, fid, ss.playerID, err)
		resp.Message = "申请加入家族失败"
		return resp
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		log.Printf("[S=%d] inspect family request id=%d player=%d: %v", ch.id, fid, ss.playerID, err)
		resp.Message = "申请加入家族失败"
		return resp
	}
	if inserted == 0 {
		resp.Message = "已申请，等待族长处理"
		return resp
	}
	log.Printf("[S=%d] request enter family %q id=%d by %d", ch.id, req.Name, fid, ss.playerID)
	return resp
}

// 20133 → 20134：退出家族（队长需先转让或解散）。
func (s *Server) onLeaveFamily(ch *channel, req *protocol.C2M_LeaveFamily) proto.Message {
	resp := &protocol.M2C_LeaveFamily{RpcId: req.RpcId}
	ss := ch.session
	row := s.loadFamilyRow(ss.familyID)
	if row == nil {
		resp.Message = "没有家族"
		return resp
	}
	if row.Leader == ss.playerID {
		resp.Message = "族长不能直接退出（可解散家族）"
		return resp
	}
	row.removeMember(ss.playerID)
	if len(row.Members) == 0 {
		s.store.db.Exec(`DELETE FROM families WHERE id = ?`, row.ID)
	} else {
		s.saveFamilyRow(row)
	}
	ss.familyID = 0
	ss.familyContribute = 0
	ss.personalContribute = 0
	s.store.db.Exec(`UPDATE players SET family_id = 0, family_contribute = 0, personal_contribute = 0 WHERE id = ?`, ss.playerID)
	s.saveData(ch)
	s.pushMoney(ch)
	s.pushUnitCharacter(ch)
	s.broadcastPlayerUpdate(ch)
	log.Printf("[S=%d] leave family id=%d", ch.id, row.ID)
	return resp
}

// 20135 → 20136：族长处理入族申请（IsAgree=true 同意 / false 拒绝）。
func (s *Server) onHandleEnterFamiy(ch *channel, req *protocol.C2M_HandleEnterFamiy) proto.Message {
	resp := &protocol.M2C_HandleEnterFamiy{RpcId: req.RpcId}
	ss := ch.session
	if ss.familyID == 0 {
		resp.Message = "没有家族"
		return resp
	}
	member, err := s.handleFamilyRequest(ss.familyID, ss.playerID, req.Id, req.IsAgree)
	if err != nil {
		switch {
		case errors.Is(err, errFamilyNotFound):
			resp.Message = "没有家族"
		case errors.Is(err, errFamilyLeaderChanged):
			resp.Message = "只有族长可以处理申请"
		case errors.Is(err, errFamilyRequestMissing):
			resp.Message = "申请不存在"
		case errors.Is(err, errFamilyApplicantMissing):
			resp.Message = "玩家不存在"
		case errors.Is(err, errFamilyApplicantJoined):
			resp.Message = "对方已有家族"
		default:
			log.Printf("[S=%d] handle family request family=%d player=%d agree=%v: %v",
				ch.id, ss.familyID, req.Id, req.IsAgree, err)
			resp.Message = "处理家族申请失败"
		}
		return resp
	}
	if member != nil {
		if rc := s.findChannelByPlayerID(req.Id); rc != nil && rc.session != nil {
			rc.session.familyID = ss.familyID
			s.pushUnitCharacter(rc)
			s.broadcastPlayerUpdate(rc)
		}
	}
	log.Printf("[S=%d] handle enter family id=%d agree=%v", ch.id, req.Id, req.IsAgree)
	return resp
}

// 20137 → 20138：族长踢人。
func (s *Server) onDeleteFamilyMember(ch *channel, req *protocol.C2M_DeleteFamilyMember) proto.Message {
	resp := &protocol.M2C_DeleteFamilyMember{RpcId: req.RpcId}
	ss := ch.session
	row := s.loadFamilyRow(ss.familyID)
	if row == nil {
		resp.Message = "没有家族"
		return resp
	}
	if row.Leader != ss.playerID {
		resp.Message = "只有族长可以踢人"
		return resp
	}
	if req.UnitId == ss.playerID {
		resp.Message = "不能踢自己"
		return resp
	}
	row.removeMember(req.UnitId)
	s.saveFamilyRow(row)
	s.store.db.Exec(`UPDATE players SET family_id = 0, family_contribute = 0, personal_contribute = 0 WHERE id = ?`, req.UnitId)
	if rc := s.findChannelByPlayerID(req.UnitId); rc != nil && rc.session != nil {
		rc.session.familyID = 0
		rc.session.familyContribute = 0
		rc.session.personalContribute = 0
		s.saveData(rc)
		s.pushMoney(rc)
		s.pushUnitCharacter(rc)
		s.broadcastPlayerUpdate(rc)
	}
	log.Printf("[S=%d] delete family member %d", ch.id, req.UnitId)
	return resp
}

// ===================== 家族 BOSS =====================

// 20139 → 20141：家族 BOSS 信息（5 个 BOSS 的 Hp/MaxHp/HasReward）。
// BossInfoList(tag1) 为字段级 List → 手工编码 raw 20141。
func (s *Server) onGetFamilyBossInfo(ch *channel, req *protocol.C2M_GetFamilyBossInfo) proto.Message {
	resp := &protocol.M2C_GetFamilyBossInfo{RpcId: req.RpcId}
	ss := ch.session
	if ss.familyID == 0 {
		resp.Message = "没有家族"
		return resp
	}
	base, err := proto.Marshal(resp)
	if err != nil {
		return resp
	}
	for i := 1; i <= 5; i++ {
		st := familyBossState(ss.familyID, int32(i))
		raw, err2 := proto.Marshal(&protocol.BossInfo{
			Index:     int32(i),
			Hp:        st.Hp,
			MaxHp:     st.MaxHp,
			HasReward: st.Hp <= 0 && s.canClaimFamilyBossReward(ss.familyID, int32(i), ss.playerID),
		})
		if err2 == nil {
			base = pbAppendBytes(base, 1, raw)
		}
	}
	s.sendRawPush(ch, protocol.OpM2C_GetFamilyBossInfo, base)
	log.Printf("[S=%d] get family boss info family=%d", ch.id, ss.familyID)
	return nil
}

// 20142 → 20143：开家族 BOSS 战。BossId=1..5（FamilyBossConfig）。
// 客户端 20144 专用事件会独立创建战斗队伍、BOSS 和 HUD；不能叠加 20047/20050。
func (s *Server) onStartFamilyBossFight(ch *channel, req *protocol.C2M_StartFamilyBossFight) proto.Message {
	log.Printf("[S=%d] family boss fight entry familyID=%d bossID=%d", ch.id, ch.session.familyID, req.BossId)
	resp := &protocol.M2C_StartFamilyBossFight{RpcId: req.RpcId}
	ss := ch.session
	if ss.battleHP() <= 0 {
		resp.Message = battleEntryHealthMessage
		return resp
	}
	if ss.familyID == 0 {
		resp.Message = "没有家族"
		return resp
	}
	if req.BossId < 1 || req.BossId > 5 {
		resp.Message = "BOSS 不存在"
		return resp
	}
	row, ok := tables.familyBossConfig[int64(req.BossId)]
	if !ok {
		resp.Message = "BOSS 配置不存在"
		return resp
	}
	mid := int32(num(row["MonsterId"]))
	st := familyBossState(ss.familyID, req.BossId)
	if st.Hp <= 0 {
		resp.Message = "BOSS 已被击败"
		return resp
	}
	presentation := battlePresentation{kind: presentationFamilyBoss, familyBossID: req.BossId}
	if message := s.battleDailyDungeonQuotaFailure(ch, 1001, -req.BossId, presentation, time.Now()); message != "" {
		resp.Message = message
		return resp
	}
	partyStartMu.Lock()
	defer partyStartMu.Unlock()
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	if ss.battle != nil {
		resp.Message = "已在战斗中"
		return resp
	}
	units := ss.buildMonsterUnitsFromRoster([]int32{mid}, []int{1})
	if len(units) == 0 {
		resp.Message = "BOSS 配置异常"
		return resp
	}
	// 家族 BOSS 血 = 家族状态（Hp 非满血时继承当前血）
	if st.Hp < st.MaxHp {
		units[0].hp = st.Hp
		units[0].maxHP = st.MaxHp
	}
	// region 用客户端必有行 1001；battle.mapID=-BossId 标记家族 BOSS 击杀结算。
	log.Printf("[S=%d] family boss fight: building units mid=%d hp=%d/%d", ch.id, mid, units[0].hp, units[0].maxHP)
	presentation.deferFamilyBossForPlayerID = ss.playerID
	started, _ := s.finishStartBattleWithPresentation(ch, 1001, units, -req.BossId, presentation)
	log.Printf("[S=%d] family boss fight: finished started=%v", ch.id, started)
	if !started {
		resp.Message = "开始战斗失败"
		return resp
	}
	log.Printf("[S=%d] start family boss %d fight monster=%d", ch.id, req.BossId, mid)
	return resp
}

// 20148 → 20149：BOSS 伤害统计（简化：TotalDamage=已扣血量，成员均分）。
// BossDamagePerMemberMap(tag1)/BossDamageMap(tag2) 为字段级 → 手工编码 raw 20149。
func (s *Server) onGetBossDamageMap(ch *channel, req *protocol.C2M_GetBossDamageMap) proto.Message {
	resp := &protocol.M2C_GetBossDamageMap{RpcId: req.RpcId}
	ss := ch.session
	// 家族行被绕过 onDeleteFamily 的删除清掉后 family_id 会悬空，此路每次都报
	// 「家族不存在」且永远不自愈。先清悬空归属再走正常分支（见 family_selfheal.go）。
	s.healDanglingFamily(ch)
	if ss.familyID == 0 {
		resp.Message = "没有家族"
		return resp
	}
	row := s.loadFamilyRow(ss.familyID)
	if row == nil {
		resp.Message = "家族不存在"
		return resp
	}
	st := familyBossState(ss.familyID, req.BossId)
	total := st.MaxHp - st.Hp
	if total < 0 {
		total = 0
	}
	base, err := proto.Marshal(resp)
	if err != nil {
		return resp
	}
	damage := familyBossDamageSnapshot(ss.familyID, req.BossId)
	treatment := familyBossTreatmentSnapshot(ss.familyID, req.BossId)
	participants := familyBossParticipantSnapshot(ss.familyID, req.BossId)
	base = pbAppendBytes(base, 1,
		encodeBossDamageRankWithParticipation(total, row.Members, damage, treatment, participants))
	s.sendRawPush(ch, protocol.OpM2C_GetBossDamageMap, base)
	log.Printf("[S=%d] get boss damage map boss=%d total=%d", ch.id, req.BossId, total)
	return nil
}

func encodeBossDamageRank(total int32, members []*familyMember) []byte {
	return encodeBossDamageRankWithDamage(total, members, nil)
}

func encodeBossDamageRankWithDamage(total int32, members []*familyMember, damage map[int64]int32) []byte {
	return encodeBossDamageRankWithDamageAndTreatment(total, members, damage, nil)
}

func encodeBossDamageRankWithDamageAndTreatment(total int32, members []*familyMember, damage, treatment map[int64]int32) []byte {
	return encodeBossDamageRankWithParticipation(total, members, damage, treatment, nil)
}

func encodeBossDamageRankWithParticipation(total int32, members []*familyMember, damage, treatment map[int64]int32, participants map[int64]bool) []byte {
	rank, _ := proto.Marshal(&protocol.BossDamageMap{TotalDamage: total})
	if len(members) == 0 {
		return rank
	}
	ordered := make([]*familyMember, 0, len(members))
	for _, member := range members {
		if member != nil && (len(participants) == 0 || participants[member.ID]) {
			ordered = append(ordered, member)
		}
	}
	if len(ordered) == 0 {
		return rank
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := damage[ordered[i].ID], damage[ordered[j].ID]
		if left != right {
			return left > right
		}
		return ordered[i].ID < ordered[j].ID
	})
	per := int32(0)
	// Keep the legacy equal-share fallback only for a completely empty
	// history.  A treatment-only history is valid (for example, a healer can
	// restore a teammate before another member deals damage); inventing damage
	// in that case makes the native Damage progress bar disagree with the
	// recorded participation result.
	if len(damage) == 0 && len(treatment) == 0 {
		per = total / int32(len(ordered))
	}
	for _, member := range ordered {
		if member == nil {
			continue
		}
		memberDamage := per
		if len(damage) > 0 {
			memberDamage = damage[member.ID]
		}
		raw, err := proto.Marshal(&protocol.BossDamagePerMemberMap{
			Id: member.ID, Name: member.Name, Damage: memberDamage, Treat: treatment[member.ID],
		})
		if err == nil {
			rank = pbAppendBytes(rank, 2, raw)
		}
	}
	return rank
}

// ===================== 辅助 =====================

// familyRow 家族行（内存态 + 懒加载 members/requests）。
type familyRow struct {
	ID       int64
	Name     string
	Leader   int64
	Level    int32
	Hornor   int32
	Notice   string
	Members  []*familyMember
	Requests []int64
}

func (f *familyRow) removeMember(pid int64) {
	kept := f.Members[:0]
	for _, m := range f.Members {
		if m.ID != pid {
			kept = append(kept, m)
		}
	}
	f.Members = kept
}

// loadFamilyRow loads a family and its normalized member/request rows.
func (s *Server) loadFamilyRow(fid int64) *familyRow {
	if fid == 0 {
		return nil
	}
	row := &familyRow{}
	err := s.store.db.QueryRow(`SELECT id, name, leader, level, hornor, notice
		FROM families WHERE id = ?`, fid).
		Scan(&row.ID, &row.Name, &row.Leader, &row.Level, &row.Hornor, &row.Notice)
	if err != nil {
		return nil
	}
	rows, err := s.store.db.Query(`SELECT player_id, name, job_id, level, last_login
		FROM family_members WHERE family_id = ? ORDER BY player_id`, fid)
	if err != nil {
		return nil
	}
	for rows.Next() {
		member := &familyMember{}
		if err := rows.Scan(&member.ID, &member.Name, &member.Job, &member.Level, &member.LastLogin); err != nil {
			rows.Close()
			return nil
		}
		row.Members = append(row.Members, member)
	}
	if closeRows(rows) != nil {
		return nil
	}
	rows, err = s.store.db.Query(`SELECT player_id FROM family_requests WHERE family_id = ? ORDER BY player_id`, fid)
	if err != nil {
		return nil
	}
	for rows.Next() {
		var playerID int64
		if err := rows.Scan(&playerID); err != nil {
			rows.Close()
			return nil
		}
		row.Requests = append(row.Requests, playerID)
	}
	if closeRows(rows) != nil {
		return nil
	}
	return row
}

// saveFamilyRow atomically replaces normalized family member/request rows.
func (s *Server) saveFamilyRow(row *familyRow) {
	if row == nil {
		return
	}
	tx, err := s.store.db.Begin()
	if err != nil {
		log.Printf("[FAMILY=%d] begin save: %v", row.ID, err)
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE families SET name=?, leader=?, level=?, hornor=?, notice=? WHERE id=?`,
		row.Name, row.Leader, row.Level, row.Hornor, row.Notice, row.ID); err != nil {
		log.Printf("[FAMILY=%d] update: %v", row.ID, err)
		return
	}
	if _, err := tx.Exec(`DELETE FROM family_members WHERE family_id = ?`, row.ID); err != nil {
		return
	}
	if _, err := tx.Exec(`DELETE FROM family_requests WHERE family_id = ?`, row.ID); err != nil {
		return
	}
	for _, member := range row.Members {
		if member == nil {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO family_members
			(family_id, player_id, name, job_id, level, last_login) VALUES (?, ?, ?, ?, ?, ?)`,
			row.ID, member.ID, member.Name, member.Job, member.Level, member.LastLogin); err != nil {
			log.Printf("[FAMILY=%d] insert member: %v", row.ID, err)
			return
		}
	}
	for _, playerID := range row.Requests {
		if _, err := tx.Exec(`INSERT INTO family_requests (family_id, player_id) VALUES (?, ?)`, row.ID, playerID); err != nil {
			log.Printf("[FAMILY=%d] insert request: %v", row.ID, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("[FAMILY=%d] commit: %v", row.ID, err)
	}
}

// familyContributeOf / personalContributeOf：读取成员贡献（在线读 session，离线查 DB）。
func (s *Server) familyContributeOf(me *session, pid int64) int32 {
	if me != nil && me.playerID == pid {
		return me.familyContribute
	}
	if rc := s.findChannelByPlayerID(pid); rc != nil && rc.session != nil {
		return rc.session.familyContribute
	}
	var v int32
	s.store.db.QueryRow(`SELECT family_contribute FROM players WHERE id = ?`, pid).Scan(&v)
	return v
}

func (s *Server) personalContributeOf(me *session, pid int64) int32 {
	if me != nil && me.playerID == pid {
		return me.personalContribute
	}
	if rc := s.findChannelByPlayerID(pid); rc != nil && rc.session != nil {
		return rc.session.personalContribute
	}
	var v int32
	s.store.db.QueryRow(`SELECT personal_contribute FROM players WHERE id = ?`, pid).Scan(&v)
	return v
}
