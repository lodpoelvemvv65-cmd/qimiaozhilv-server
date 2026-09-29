package main

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var (
	errFamilyNotFound         = errors.New("family not found")
	errFamilyLeaderChanged    = errors.New("family leader changed")
	errFamilyRequestMissing   = errors.New("family request missing")
	errFamilyApplicantMissing = errors.New("family applicant missing")
	errFamilyApplicantJoined  = errors.New("family applicant already joined")
)

// handleFamilyRequest commits request removal and membership changes as one
// transaction. The returned member is non-nil only for an accepted request.
func (s *Server) handleFamilyRequest(familyID, leaderID, applicantID int64, accept bool) (*familyMember, error) {
	if s == nil || s.store == nil || s.store.db == nil || familyID <= 0 || leaderID <= 0 || applicantID <= 0 {
		return nil, errFamilyNotFound
	}
	tx, err := s.store.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var currentLeader int64
	if err := tx.QueryRow(`SELECT leader FROM families WHERE id = ? FOR UPDATE`, familyID).Scan(&currentLeader); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errFamilyNotFound
		}
		return nil, err
	}
	if currentLeader != leaderID {
		return nil, errFamilyLeaderChanged
	}

	var requestedPlayerID int64
	if err := tx.QueryRow(`SELECT player_id FROM family_requests
		WHERE family_id = ? AND player_id = ? FOR UPDATE`, familyID, applicantID).Scan(&requestedPlayerID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errFamilyRequestMissing
		}
		return nil, err
	}
	if !accept {
		if _, err := tx.Exec(`DELETE FROM family_requests WHERE family_id = ? AND player_id = ?`, familyID, applicantID); err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	}

	member := &familyMember{ID: applicantID, LastLogin: time.Now().Unix()}
	var currentFamilyID int64
	if err := tx.QueryRow(`SELECT name, job_id, level, family_id FROM players
		WHERE id = ? FOR UPDATE`, applicantID).Scan(&member.Name, &member.Job, &member.Level, &currentFamilyID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if _, deleteErr := tx.Exec(`DELETE FROM family_requests WHERE family_id = ? AND player_id = ?`, familyID, applicantID); deleteErr != nil {
				return nil, deleteErr
			}
			if commitErr := tx.Commit(); commitErr != nil {
				return nil, commitErr
			}
			return nil, fmt.Errorf("%w: player %d", errFamilyApplicantMissing, applicantID)
		}
		return nil, err
	}
	if currentFamilyID != 0 {
		if _, err := tx.Exec(`DELETE FROM family_requests WHERE player_id = ?`, applicantID); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, errFamilyApplicantJoined
	}

	// players.family_id is authoritative. Remove any orphaned denormalized row
	// before inserting the one membership which matches it.
	if _, err := tx.Exec(`DELETE FROM family_members WHERE player_id = ?`, applicantID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`INSERT INTO family_members
		(family_id, player_id, name, job_id, level, last_login) VALUES (?, ?, ?, ?, ?, ?)`,
		familyID, member.ID, member.Name, jobTypeOf(member.Job), member.Level, member.LastLogin); err != nil {
		return nil, err
	}
	result, err := tx.Exec(`UPDATE players SET family_id = ? WHERE id = ? AND family_id = 0`, familyID, applicantID)
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errFamilyApplicantJoined
	}
	if _, err := tx.Exec(`DELETE FROM family_requests WHERE player_id = ?`, applicantID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return member, nil
}
