package main

// family_selfheal.go：家族归属引用的一致性自愈与摘除/解散。
//
// 背景：players.family_id 是家族归属的唯一权威，但服务端从不校验它指向的
// families 行是否还在。任何绕过 onDeleteFamily 的删除——运维直接执行 SQL、
// _work/cleanup_all_test_accounts.py 里的 DELETE FROM families、或解散过程中途
// 失败——都会留下悬空引用，而悬空引用会把角色永久锁死：
//
//	onGetFamily          familyID != 0 但 loadFamilyRow 返回 nil → 「家族不存在」
//	onCrateFamily        familyID != 0                          → 「已有家族」
//	onRequestEnterFamily familyID != 0                          → 「已有家族」
//
// 三条路都不修改 ss.familyID，玩家在游戏内没有任何操作能恢复。
//
// 实测案例（2026-09-14 本地库）：players.id=2（账号 a123123，角色名 "1"）
// family_id=69，而 families 表里没有 69、family_members 里也没有该玩家，
// 但 family_boss_states 残留 5 行 family_id=69。onDeleteFamily 会调
// deleteFamilyBossStateDB 清掉这 5 行（family.go 的解散分支），它们还在，
// 正好证明家族 69 当年是被绕过 onDeleteFamily 直接删掉的。
//
// 本文件提供：
//   healDanglingFamily   检测并清除悬空归属（在线路径自愈，幂等）
//   detachPlayerFromFamily 把角色从家族摘除（GM 与自愈共用）
//   dissolveFamily       解散整个家族（GM 版族长解散）
//   purgeOrphanFamilyData 清掉某个已不存在的家族遗留的孤儿行

import (
	"errors"
	"log"
)

// familyRowExists 判断 families 表里还有没有这一行。
//
// 不用 loadFamilyRow：那个函数会连成员和申请一起读，家族存在而成员表异常时
// 也可能返回 nil，用它做存在性判断会把「家族还在」误判成「家族已没」。
func (s *Server) familyRowExists(familyID int64) bool {
	if s == nil || s.store == nil || s.store.db == nil || familyID == 0 {
		return false
	}
	var id int64
	if err := s.store.db.QueryRow(`SELECT id FROM families WHERE id = ?`, familyID).Scan(&id); err != nil {
		return false
	}
	return true
}

// healDanglingFamily 清除指向已不存在的家族的归属引用，返回是否发生了修复。
//
// 必须放在任何「familyID != 0 就拒绝」的判定之前，否则角色会被自己的脏数据
// 挡在门外，永远走不到修复。幂等：familyID 为 0、或家族确实存在时不做任何事。
func (s *Server) healDanglingFamily(ch *channel) bool {
	if s == nil || ch == nil || ch.session == nil {
		return false
	}
	ss := ch.session
	if ss.familyID == 0 {
		return false
	}
	deadID := ss.familyID
	if s.familyRowExists(deadID) {
		return false
	}
	// 家族行已经不在，指向它的成员行和申请行同样是垃圾数据。一起清掉：
	// families.id 是自增的，万一以后 ID 被复用，残留的成员行会让旧角色
	// 出现在别人的新家族里。
	if err := s.clearPlayerFamilyRefs(ss.playerID); err != nil {
		// 清库失败时不动内存。内存清零而库里没清的话，下次登录又变回悬空，
		// 玩家会看到一个「有时能创建、有时不能」的随机状态，比稳定报错更难查。
		log.Printf("[S=%d] heal family ref player=%d family=%d: %v", ch.id, ss.playerID, deadID, err)
		return false
	}
	ss.familyID = 0
	ss.familyContribute = 0
	ss.personalContribute = 0
	s.purgeOrphanFamilyData(deadID)
	log.Printf("[S=%d] healed dangling family ref player=%d family=%d", ch.id, ss.playerID, deadID)
	return true
}

// playerFamilyIDFromDB 读角色在库里的家族归属，0 表示没有家族。
//
// 与会话里的 ss.familyID 不是一个东西：会话可能比库新，也可能比库旧（库被直接
// 清过而在线会话还留着旧 ID），需要判断「库里到底怎么记的」时读这个。
func (s *Server) playerFamilyIDFromDB(playerID int64) (int64, error) {
	if s == nil || s.store == nil || s.store.db == nil {
		return 0, errors.New("store unavailable")
	}
	if playerID <= 0 {
		return 0, errors.New("invalid player")
	}
	var familyID int64
	if err := s.store.db.QueryRow(`SELECT family_id FROM players WHERE id = ?`, playerID).Scan(&familyID); err != nil {
		return 0, err
	}
	return familyID, nil
}

// clearPlayerFamilyRefs 把角色从家族归属里摘干净：申请行、成员行、players 上的
// family_id 与两项贡献。三步放同一个事务，避免只摘了一半留下更难查的中间态。
func (s *Server) clearPlayerFamilyRefs(playerID int64) error {
	if s == nil || s.store == nil || s.store.db == nil {
		return errors.New("store unavailable")
	}
	if playerID <= 0 {
		return errors.New("invalid player")
	}
	tx, err := s.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM family_requests WHERE player_id = ?`, playerID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM family_members WHERE player_id = ?`, playerID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE players SET family_id = 0, family_contribute = 0, personal_contribute = 0 WHERE id = ?`, playerID); err != nil {
		return err
	}
	return tx.Commit()
}

// purgeOrphanFamilyData 清掉某个家族 ID 在所有家族表里的遗留行。
//
// 只在确认 families 里已经没有这个 ID 之后调用（自愈路径），否则会把一个
// 活着的家族的成员和 BOSS 进度删掉。
func (s *Server) purgeOrphanFamilyData(familyID int64) {
	if s == nil || s.store == nil || s.store.db == nil || familyID == 0 {
		return
	}
	for _, stmt := range []string{
		`DELETE FROM family_members WHERE family_id = ?`,
		`DELETE FROM family_requests WHERE family_id = ?`,
		`DELETE FROM family_boss_states WHERE family_id = ?`,
		`DELETE FROM family_boss_damage WHERE family_id = ?`,
		`DELETE FROM family_boss_reward_claims WHERE family_id = ?`,
	} {
		if _, err := s.store.db.Exec(stmt, familyID); err != nil {
			log.Printf("[FAMILY=%d] purge orphan row: %v", familyID, err)
		}
	}
	familyBossStates.mu.Lock()
	delete(familyBossStates.m, familyID)
	familyBossStates.mu.Unlock()
	// 领取状态同时有一份内存副本，只删库不清内存的话，重启前的那次统计还会
	// 把已经不存在家族的领取记录算进去。
	clearFamilyBossRewardClaims(familyID)
}

// familyDetachResult 描述一次摘除/解散真正改动了什么，供 GM 审计回显。
type familyDetachResult struct {
	FamilyID   int64  `json:"familyId"`
	FamilyName string `json:"familyName"`
	// RemovedMembers 是被清掉归属的角色 ID；解散时是全体成员。
	RemovedMembers []int64 `json:"removedMembers"`
	// Dissolved 为 true 表示家族行本身也被删了（摘除后无人 / 主动解散）。
	Dissolved bool `json:"dissolved"`
	// PromotedLeader 是摘除族长后继任的角色 ID；0 表示没有继任者。
	PromotedLeader int64 `json:"promotedLeader,omitempty"`
}

// detachPlayerFromFamily 把角色从家族摘除，并在必要时解散家族。
//
// 摘的是族长且家族还有别人时，把族长让给剩余成员里 ID 最小的一个——与
// deletePlayerRelations 处理删号时的策略一致，避免留下一个没有族长的家族。
// 摘完家族空了就整个解散。
//
// 调用方必须已经持有该角色的 session（在线取在线、离线临时装载）。
func (s *Server) detachPlayerFromFamily(playerID int64) (*familyDetachResult, error) {
	if s == nil || s.store == nil || s.store.db == nil {
		return nil, errors.New("store unavailable")
	}
	if playerID <= 0 {
		return nil, errors.New("invalid player")
	}
	var familyID int64
	if err := s.store.db.QueryRow(`SELECT family_id FROM players WHERE id = ?`, playerID).Scan(&familyID); err != nil {
		return nil, err
	}
	if familyID == 0 {
		return &familyDetachResult{}, nil
	}
	result := &familyDetachResult{FamilyID: familyID, RemovedMembers: []int64{playerID}}
	row := s.loadFamilyRow(familyID)
	if row == nil {
		// 家族行已经没了：只剩清引用这一步，顺手把孤儿行收拾掉。
		if err := s.clearPlayerFamilyRefs(playerID); err != nil {
			return nil, err
		}
		s.purgeOrphanFamilyData(familyID)
		result.Dissolved = true
		return result, nil
	}
	result.FamilyName = row.Name
	if err := s.clearPlayerFamilyRefs(playerID); err != nil {
		return nil, err
	}
	remaining := make([]*familyMember, 0, len(row.Members))
	for _, m := range row.Members {
		if m != nil && m.ID != playerID {
			remaining = append(remaining, m)
		}
	}
	row.Requests = removeInt64All(row.Requests, playerID)
	if len(remaining) == 0 {
		if err := s.dissolveFamilyRow(row); err != nil {
			return nil, err
		}
		result.Dissolved = true
		return result, nil
	}
	if row.Leader == playerID {
		row.Leader = remaining[0].ID
		result.PromotedLeader = remaining[0].ID
	}
	row.Members = remaining
	s.saveFamilyRow(row)
	return result, nil
}

// dissolveFamily 解散整个家族：全体成员清归属，家族行与 BOSS 数据一并删除。
func (s *Server) dissolveFamily(familyID int64) (*familyDetachResult, error) {
	if s == nil || s.store == nil || s.store.db == nil {
		return nil, errors.New("store unavailable")
	}
	row := s.loadFamilyRow(familyID)
	if row == nil {
		// 家族行不在，但可能还有角色挂在它上面（正是本次要治的悬空引用）。
		return s.dissolveDanglingFamily(familyID, "")
	}
	result := &familyDetachResult{FamilyID: familyID, FamilyName: row.Name, Dissolved: true}
	tx, err := s.store.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, m := range row.Members {
		if m == nil || m.ID <= 0 {
			continue
		}
		result.RemovedMembers = append(result.RemovedMembers, m.ID)
		if _, err := tx.Exec(`UPDATE players SET family_id = 0, family_contribute = 0, personal_contribute = 0 WHERE id = ?`, m.ID); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(`DELETE FROM family_requests WHERE family_id = ?`, familyID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM family_members WHERE family_id = ?`, familyID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM families WHERE id = ?`, familyID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.purgeOrphanFamilyData(familyID)
	s.syncFamilyDetachToOnline(result)
	return result, nil
}

// dissolveDanglingFamily 处理「家族行已不在，但还有角色挂在上面」的情况：
// 按 players.family_id 反查所有悬空角色，逐个清引用。name 仅用于回显。
func (s *Server) dissolveDanglingFamily(familyID int64, name string) (*familyDetachResult, error) {
	result := &familyDetachResult{FamilyID: familyID, FamilyName: name, Dissolved: true}
	rows, err := s.store.db.Query(`SELECT id FROM players WHERE family_id = ?`, familyID)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if err := s.clearPlayerFamilyRefs(id); err != nil {
			return nil, err
		}
		result.RemovedMembers = append(result.RemovedMembers, id)
	}
	s.purgeOrphanFamilyData(familyID)
	s.syncFamilyDetachToOnline(result)
	return result, nil
}

// dissolveFamilyRow 删掉一个已经读出来的家族行（摘除后无人时走这条）。
func (s *Server) dissolveFamilyRow(row *familyRow) error {
	if row == nil {
		return nil
	}
	tx, err := s.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM family_requests WHERE family_id = ?`, row.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM family_members WHERE family_id = ?`, row.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM families WHERE id = ?`, row.ID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// 不用 deleteFamilyBossStateDB：那个函数读包级 globalServer，测试进程里
	// globalServer 为 nil 时它什么都不做。purgeOrphanFamilyData 只依赖 s。
	s.purgeOrphanFamilyData(row.ID)
	return nil
}

// syncFamilyDetachToOnline 把已经落库的摘除同步给在线会话。
//
// 数据库已经改完，这里的会话同步失败只会让在线角色看到旧数据到下次登录为止，
// 所以只记日志不回滚——回滚反而会让库和会话重新分裂。
func (s *Server) syncFamilyDetachToOnline(result *familyDetachResult) {
	if s == nil || result == nil {
		return
	}
	for _, id := range result.RemovedMembers {
		ch := s.gmOnlineChannel(id)
		if ch == nil || ch.session == nil {
			continue
		}
		ss := ch.session
		ss.battleMu.Lock()
		ss.familyID = 0
		ss.familyContribute = 0
		ss.personalContribute = 0
		ss.battleMu.Unlock()
		s.pushMoney(ch)
		s.pushUnitCharacter(ch)
		s.broadcastPlayerUpdate(ch)
	}
}

// removeInt64All 删除列表里所有等于 value 的元素（removeInt64WithChanged 只删
// 第一个匹配项，不适合这里）。
func removeInt64All(list []int64, value int64) []int64 {
	if len(list) == 0 {
		return list
	}
	kept := make([]int64, 0, len(list))
	for _, item := range list {
		if item != value {
			kept = append(kept, item)
		}
	}
	return kept
}
