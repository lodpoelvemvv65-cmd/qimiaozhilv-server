package main

import (
	"encoding/json"
	"log"
	"math/rand"
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
// 数据：families 表（members_json/requests_json）；players 表 family_id/
// family_contribute/personal_contribute 列。家族 BOSS 血量按家族独立并写入
// family_boss_states；每次伤害、退出、击杀和领奖均落库，服务重启后继续原血量。

// familyMember 家族成员（members_json 条目）。
type familyMember struct {
	ID        int64  `json:"id"`
	Name      string `json:"n"`
	Job       int32  `json:"j"`
	Level     int32  `json:"l"`
	LastLogin int64  `json:"t"`
}

// familyBossHP 家族 BOSS 状态（每家族 5 个）。
type familyBossHP struct {
	Hp        int32 // 当前血（0=已击杀）
	MaxHp     int32
	HasReward bool // 击杀后可领奖
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
	rows, err := globalServer.store.db.Query(`SELECT boss_id, hp, max_hp, has_reward
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
		if err := rows.Scan(&bid, &hp, &maxHp, &hasReward); err != nil {
			continue
		}
		if bid < 1 || bid > 5 {
			continue
		}
		arr[bid-1] = familyBossHP{Hp: hp, MaxHp: maxHp, HasReward: hasReward != 0}
		found = true
	}
	if !found {
		return nil
	}
	return arr
}

// saveFamilyBossStateDB 写回某家族 5 个 BOSS 状态到 DB（满血行初始化 / 战斗后扣血 / 领奖均调用）。
func saveFamilyBossStateDB(familyID int64, arr *[5]familyBossHP) {
	if globalServer == nil || globalServer.store == nil || arr == nil {
		return
	}
	for i := 0; i < 5; i++ {
		r := 0
		if arr[i].HasReward {
			r = 1
		}
		globalServer.store.db.Exec(`INSERT INTO family_boss_states (family_id, boss_id, hp, max_hp, has_reward)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(family_id, boss_id) DO UPDATE SET hp=excluded.hp, max_hp=excluded.max_hp, has_reward=excluded.has_reward`,
			familyID, i+1, arr[i].Hp, arr[i].MaxHp, r)
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
	}
	saveFamilyBossStateDB(familyID, familyBossAllStates(familyID))
}

// deleteFamilyBossStateDB 解散家族时清除其 BOSS 状态。
func deleteFamilyBossStateDB(familyID int64) {
	if globalServer == nil || globalServer.store == nil {
		return
	}
	globalServer.store.db.Exec(`DELETE FROM family_boss_states WHERE family_id = ?`, familyID)
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
	members := []*familyMember{{ID: ss.playerID, Name: ss.name, Job: jobTypeOf(ss.jobID), Level: ss.level, LastLogin: time.Now().Unix()}}
	mb, _ := json.Marshal(members)
	res, err := s.store.db.Exec(`INSERT INTO families (name, leader, members_json) VALUES (?, ?, ?)`,
		req.Name, ss.playerID, string(mb))
	if err != nil {
		resp.Message = "创建失败"
		return resp
	}
	fid, _ := res.LastInsertId()
	ss.familyID = fid
	s.store.db.Exec(`UPDATE players SET family_id = ? WHERE id = ?`, fid, ss.playerID)
	s.saveData(ch)
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
	}
	s.store.db.Exec(`DELETE FROM families WHERE id = ?`, ss.familyID)
	familyBossStates.mu.Lock()
	delete(familyBossStates.m, ss.familyID)
	familyBossStates.mu.Unlock()
	deleteFamilyBossStateDB(ss.familyID) // 家族 BOSS 状态一并清除（走数据库）
	ss.familyID = 0
	s.saveData(ch)
	s.pushUnitCharacter(ch)
	s.broadcastPlayerUpdate(ch)
	log.Printf("[S=%d] delete family id=%d", ch.id, row.ID)
	return resp
}

// 20131 → 20132：申请加入家族（按名字）。
func (s *Server) onRequestEnterFamily(ch *channel, req *protocol.C2M_RequestEnterFamily) proto.Message {
	resp := &protocol.M2C_RequestEnterFamily{RpcId: req.RpcId}
	ss := ch.session
	if ss.familyID != 0 {
		resp.Message = "已有家族"
		return resp
	}
	var fid int64
	if err := s.store.db.QueryRow(`SELECT id FROM families WHERE name = ?`, req.Name).Scan(&fid); err != nil {
		resp.Message = "家族不存在"
		return resp
	}
	row := s.loadFamilyRow(fid)
	if row == nil {
		resp.Message = "家族不存在"
		return resp
	}
	for _, rid := range row.Requests {
		if rid == ss.playerID {
			resp.Message = "已申请，等待族长处理"
			return resp
		}
	}
	row.Requests = append(row.Requests, ss.playerID)
	s.saveFamilyRow(row)
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
	s.pushUnitCharacter(ch)
	s.broadcastPlayerUpdate(ch)
	log.Printf("[S=%d] leave family id=%d", ch.id, row.ID)
	return resp
}

// 20135 → 20136：族长处理入族申请（IsAgree=true 同意 / false 拒绝）。
func (s *Server) onHandleEnterFamiy(ch *channel, req *protocol.C2M_HandleEnterFamiy) proto.Message {
	resp := &protocol.M2C_HandleEnterFamiy{RpcId: req.RpcId}
	ss := ch.session
	row := s.loadFamilyRow(ss.familyID)
	if row == nil {
		resp.Message = "没有家族"
		return resp
	}
	if row.Leader != ss.playerID {
		resp.Message = "只有族长可以处理申请"
		return resp
	}
	// 从申请列表移除
	found := false
	kept := row.Requests[:0]
	for _, rid := range row.Requests {
		if rid == req.Id {
			found = true
		} else {
			kept = append(kept, rid)
		}
	}
	row.Requests = kept
	if !found {
		s.saveFamilyRow(row)
		resp.Message = "申请不存在"
		return resp
	}
	if req.IsAgree {
		// 同意：申请者入族
		if rc := s.findChannelByPlayerID(req.Id); rc != nil && rc.session != nil {
			if rc.session.familyID != 0 {
				resp.Message = "对方已有家族"
				return resp
			}
			rc.session.familyID = row.ID
			row.Members = append(row.Members, &familyMember{
				ID: req.Id, Name: rc.session.name, Job: jobTypeOf(rc.session.jobID),
				Level: rc.session.level, LastLogin: time.Now().Unix(),
			})
			s.store.db.Exec(`UPDATE players SET family_id = ? WHERE id = ?`, row.ID, req.Id)
			s.saveData(rc)
			s.pushUnitCharacter(rc)
			s.broadcastPlayerUpdate(rc)
		} else {
			// 离线申请者：直接入族（名字从 DB 读）
			var nm string
			var job, lv int32
			s.store.db.QueryRow(`SELECT name, job_id, level FROM players WHERE id = ?`, req.Id).
				Scan(&nm, &job, &lv)
			if nm == "" {
				resp.Message = "玩家不存在"
				return resp
			}
			row.Members = append(row.Members, &familyMember{
				ID: req.Id, Name: nm, Job: job, Level: lv, LastLogin: 0,
			})
			s.store.db.Exec(`UPDATE players SET family_id = ? WHERE id = ?`, row.ID, req.Id)
		}
	}
	s.saveFamilyRow(row)
	s.saveData(ch)
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
			HasReward: st.HasReward,
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
	started, _ := s.finishStartDedicatedBossBattle(ch, 1001, units, -req.BossId, req.BossId)
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
	base = pbAppendBytes(base, 1, encodeBossDamageRank(total, row.Members))
	s.sendRawPush(ch, protocol.OpM2C_GetBossDamageMap, base)
	log.Printf("[S=%d] get boss damage map boss=%d total=%d", ch.id, req.BossId, total)
	return nil
}

func encodeBossDamageRank(total int32, members []*familyMember) []byte {
	rank, _ := proto.Marshal(&protocol.BossDamageMap{TotalDamage: total})
	if len(members) == 0 {
		return rank
	}
	per := total / int32(len(members))
	for _, member := range members {
		if member == nil {
			continue
		}
		raw, err := proto.Marshal(&protocol.BossDamagePerMemberMap{
			Id: member.ID, Name: member.Name, Damage: per, Treat: 0,
		})
		if err == nil {
			rank = pbAppendBytes(rank, 2, raw)
		}
	}
	return rank
}

// 20150 → 20152：领取家族 BOSS 奖励（击杀后 HasReward；发 FamilyBossConfig 贡献 +
// 掉落链 Parentset→SonSet 随机物品；领后 BOSS 复活）。
func (s *Server) onGetFamilyBossReward(ch *channel, req *protocol.C2M_GetFamilyBossReward) proto.Message {
	resp := &protocol.M2C_GetFamilyBossReward{RpcId: req.RpcId}
	ss := ch.session
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
	st := familyBossState(ss.familyID, req.BossId)
	if !st.HasReward {
		resp.Message = "BOSS 尚未被击败"
		return resp
	}
	// 掉落：Dropasubset → Parentset → SonSet 随机 1 件入背包
	dropID := int32(num(row["Dropasubset"]))
	itemID := familyBossDropItem(dropID)
	if itemID > 0 && ss.addItemToBag(itemID, 1) < 0 {
		resp.Message = "背包已满"
		return resp
	}
	// 物品确认入包后才提交贡献和领奖状态。
	pc := int32(num(row["PersonalContribute"]))
	fc := int32(num(row["Contribute"]))
	ss.personalContribute += pc
	ss.familyContribute += fc
	// 复活 BOSS
	st.Hp = st.MaxHp
	st.HasReward = false
	saveFamilyBossStateDB(ss.familyID, familyBossAllStates(ss.familyID)) // 血量/领奖状态走数据库
	s.saveData(ch)
	// ItemList(tag1) 为字段级 → 手工编码 raw 20152
	base, err := proto.Marshal(resp)
	if err != nil {
		return resp
	}
	if itemID > 0 {
		raw, _ := proto.Marshal(&protocol.ItemInfo{Id: itemID, Count: 1})
		base = pbAppendBytes(base, 1, raw)
	}
	s.sendRawPush(ch, protocol.OpM2C_GetFamilyBossReward, base)
	log.Printf("[S=%d] get family boss reward boss=%d item=%d pc=%d fc=%d", ch.id, req.BossId, itemID, pc, fc)
	return nil
}

// familyBossDropItem：Dropasubset(Parentset) → SubsetArr → SonSet.DropArr 按权重随机 1 件。
func familyBossDropItem(parentsetID int32) int32 {
	ps, ok := tables.parentset[int64(parentsetID)]
	if !ok {
		return 0
	}
	subsets := arrOf(ps["SubsetArr"])
	if len(subsets) == 0 {
		return 0
	}
	// 取第一个子集
	var subsetID int64
	if eo, ok2 := subsets[0].(map[string]interface{}); ok2 {
		subsetID = int64(num(eo["_Id"]))
	} else {
		subsetID = int64(num(subsets[0]))
	}
	son, ok := tables.sonSet[subsetID]
	if !ok {
		return 0
	}
	drops := arrOf(son["DropArr"])
	total := 0
	for _, d := range drops {
		eo, _ := d.(map[string]interface{})
		if eo == nil {
			continue
		}
		total += int(num(eo["Weight"]))
	}
	if total <= 0 {
		return 0
	}
	r := rand.Intn(total)
	for _, d := range drops {
		eo, _ := d.(map[string]interface{})
		if eo == nil {
			continue
		}
		r -= int(num(eo["Weight"]))
		if r < 0 {
			return int32(num(eo["_Id"]))
		}
	}
	return 0
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

// loadFamilyRow 从 DB 读家族（含 members/requests 解析）。
func (s *Server) loadFamilyRow(fid int64) *familyRow {
	if fid == 0 {
		return nil
	}
	row := &familyRow{}
	var membersJSON, requestsJSON string
	err := s.store.db.QueryRow(`SELECT id, name, leader, level, hornor, notice, members_json, requests_json
		FROM families WHERE id = ?`, fid).
		Scan(&row.ID, &row.Name, &row.Leader, &row.Level, &row.Hornor, &row.Notice, &membersJSON, &requestsJSON)
	if err != nil {
		return nil
	}
	_ = json.Unmarshal([]byte(membersJSON), &row.Members)
	_ = json.Unmarshal([]byte(requestsJSON), &row.Requests)
	if row.Members == nil {
		row.Members = []*familyMember{}
	}
	if row.Requests == nil {
		row.Requests = []int64{}
	}
	return row
}

// saveFamilyRow 写回家族（members/requests JSON）。
func (s *Server) saveFamilyRow(row *familyRow) {
	mb, _ := json.Marshal(row.Members)
	rb, _ := json.Marshal(row.Requests)
	s.store.db.Exec(`UPDATE families SET name=?, leader=?, level=?, hornor=?, notice=?, members_json=?, requests_json=? WHERE id=?`,
		row.Name, row.Leader, row.Level, row.Hornor, row.Notice, string(mb), string(rb), row.ID)
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
