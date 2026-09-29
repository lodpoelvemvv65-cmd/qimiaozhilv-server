package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// onDelRole implements the real role-selection delete flow. The account is
// retained so the same connection can immediately create a new role.
func (s *Server) onDelRole(ch *channel, req *protocol.C2R_DelRole) proto.Message {
	resp := &protocol.R2C_DelRole{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.accountID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	var playerID int64
	err := s.store.db.QueryRow(`SELECT id FROM players WHERE id = ? AND account_id = ?`,
		req.UserId, ch.session.accountID).Scan(&playerID)
	if errors.Is(err, sql.ErrNoRows) {
		resp.Message = "角色不存在或不属于当前账号"
		return resp
	}
	if err != nil {
		resp.Message = "删除角色失败"
		return resp
	}
	if online := s.findChannelByPlayerID(playerID); online != nil && online != ch && online.session != nil && online.session.state == sessInGame {
		resp.Message = "角色当前在线，请先下线"
		return resp
	}
	if err := s.deletePlayerRelations(playerID); err != nil {
		log.Printf("[S=%d] delete role player=%d: %v", ch.id, playerID, err)
		resp.Message = "删除角色失败"
		return resp
	}
	ss := ch.session
	ss.playerID, ss.jobID, ss.skinID, ss.familyID = 0, 0, 0, 0
	ss.name = ""
	ss.state = sessLogged
	log.Printf("[S=%d] delete role ok account=%d player=%d", ch.id, ss.accountID, playerID)
	return resp
}

func removeInt64(list []int64, value int64) ([]int64, bool) {
	kept := list[:0]
	removed := false
	for _, current := range list {
		if current == value {
			removed = true
			continue
		}
		kept = append(kept, current)
	}
	return kept, removed
}

// deletePlayerRelations removes every persisted reference which would
// otherwise leave a ghost family member, friend or consignment entry.
func (s *Server) deletePlayerRelations(playerID int64) error {
	rows, err := s.store.db.Query(`SELECT id FROM families ORDER BY id`)
	if err != nil {
		return err
	}
	var familyIDs []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			familyIDs = append(familyIDs, id)
		}
	}
	rows.Close()
	for _, familyID := range familyIDs {
		row := s.loadFamilyRow(familyID)
		if row == nil {
			continue
		}
		changed := false
		before := len(row.Members)
		row.removeMember(playerID)
		changed = changed || before != len(row.Members)
		row.Requests, changed = removeInt64WithChanged(row.Requests, playerID, changed)
		if row.Leader == playerID {
			if len(row.Members) == 0 {
				if _, err := s.store.db.Exec(`DELETE FROM families WHERE id = ?`, familyID); err != nil {
					return err
				}
				if _, err := s.store.db.Exec(`DELETE FROM family_boss_states WHERE family_id = ?`, familyID); err != nil {
					return err
				}
				familyBossStates.mu.Lock()
				delete(familyBossStates.m, familyID)
				familyBossStates.mu.Unlock()
				continue
			}
			row.Leader = row.Members[0].ID
			changed = true
		}
		if changed {
			s.saveFamilyRow(row)
		}
	}

	friendRows, err := s.store.db.Query(`SELECT id, friends_json FROM players WHERE id <> ?`, playerID)
	if err != nil {
		return err
	}
	type friendUpdate struct {
		id   int64
		json string
	}
	var updates []friendUpdate
	for friendRows.Next() {
		var id int64
		var raw string
		if friendRows.Scan(&id, &raw) != nil {
			continue
		}
		friends := friendsFromJSON(raw)
		if _, ok := friends[playerID]; ok {
			delete(friends, playerID)
			updates = append(updates, friendUpdate{id: id, json: friendsToJSON(friends)})
		}
	}
	friendRows.Close()
	for _, update := range updates {
		if _, err := s.store.db.Exec(`UPDATE players SET friends_json = ? WHERE id = ?`, update.json, update.id); err != nil {
			return err
		}
		if online := s.findChannelByPlayerID(update.id); online != nil && online.session != nil {
			online.session.socialMu.Lock()
			delete(online.session.friends, playerID)
			delete(online.session.friendReqFrom, playerID)
			delete(online.session.friendReqTo, playerID)
			online.session.socialMu.Unlock()
		}
	}
	if _, err := s.store.db.Exec(`DELETE FROM consignment_items WHERE seller_id = ?`, playerID); err != nil {
		return err
	}
	result, err := s.store.db.Exec(`DELETE FROM players WHERE id = ?`, playerID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return fmt.Errorf("deleted rows = %d", affected)
	}
	return nil
}

func removeInt64WithChanged(list []int64, value int64, already bool) ([]int64, bool) {
	result, removed := removeInt64(list, value)
	return result, already || removed
}
