package main

import (
	"errors"
	"path/filepath"
	"testing"

	"mhqserver/protocol"
)

func familyMembershipTestServer(t *testing.T) (*Server, *channel, *channel, int64) {
	t.Helper()
	store, err := OpenStore(mysqlTestDSN(t, filepath.Join(t.TempDir(), "family-membership.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)

	const leaderID, applicantID, familyID = int64(101), int64(202), int64(77)
	if _, err := store.db.Exec(`INSERT INTO players (id, account_id, name, job_id, skin_id)
		VALUES (?, 1, 'leader', 1, 1), (?, 2, 'applicant', 3, 3)`, leaderID, applicantID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO families (id, name, leader) VALUES (?, 'target-family', ?)`, familyID, leaderID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO family_members
		(family_id, player_id, name, job_id, level, last_login) VALUES (?, ?, 'leader', 1, 1, 0)`, familyID, leaderID); err != nil {
		t.Fatal(err)
	}

	server := &Server{store: store, conns: make(map[int64]*channel)}
	leaderSession := newSession()
	leaderSession.state, leaderSession.playerID, leaderSession.name = sessInGame, leaderID, "leader"
	leaderSession.jobID, leaderSession.skinID, leaderSession.familyID = 1, 1, familyID
	applicantSession := newSession()
	applicantSession.state, applicantSession.playerID, applicantSession.name = sessInGame, applicantID, "applicant"
	applicantSession.jobID, applicantSession.skinID = 3, 3
	leader := &channel{id: leaderID, session: leaderSession, conn: &recordingConn{}}
	applicant := &channel{id: applicantID, session: applicantSession, conn: &recordingConn{}}
	server.conns[leaderID], server.conns[applicantID] = leader, applicant
	return server, leader, applicant, familyID
}

func TestFamilyApplicantCanJoinAfterLeaderApproval(t *testing.T) {
	server, leader, applicant, familyID := familyMembershipTestServer(t)

	requested := server.onRequestEnterFamily(applicant, &protocol.C2M_RequestEnterFamily{
		RpcId: 1, Name: "target-family",
	}).(*protocol.M2C_RequestEnterFamily)
	if requested.Message != "" {
		t.Fatalf("request response=%+v", requested)
	}

	approved := server.onHandleEnterFamiy(leader, &protocol.C2M_HandleEnterFamiy{
		RpcId: 2, Id: applicant.session.playerID, IsAgree: true,
	}).(*protocol.M2C_HandleEnterFamiy)
	if approved.Message != "" {
		t.Fatalf("approval response=%+v", approved)
	}
	if applicant.session.familyID != familyID {
		t.Fatalf("online family id=%d, want %d", applicant.session.familyID, familyID)
	}

	var playerFamilyID, memberCount, requestCount int64
	if err := server.store.db.QueryRow(`SELECT family_id FROM players WHERE id = ?`, applicant.session.playerID).Scan(&playerFamilyID); err != nil {
		t.Fatal(err)
	}
	if err := server.store.db.QueryRow(`SELECT COUNT(*) FROM family_members WHERE player_id = ?`, applicant.session.playerID).Scan(&memberCount); err != nil {
		t.Fatal(err)
	}
	if err := server.store.db.QueryRow(`SELECT COUNT(*) FROM family_requests WHERE player_id = ?`, applicant.session.playerID).Scan(&requestCount); err != nil {
		t.Fatal(err)
	}
	if playerFamilyID != familyID || memberCount != 1 || requestCount != 0 {
		t.Fatalf("persisted membership family=%d members=%d requests=%d", playerFamilyID, memberCount, requestCount)
	}
}

func TestFamilyApprovalClearsRequestsToOtherFamilies(t *testing.T) {
	server, leader, applicant, familyID := familyMembershipTestServer(t)
	if _, err := server.store.db.Exec(`INSERT INTO families (id, name, leader) VALUES (88, 'other-family', 303)`); err != nil {
		t.Fatal(err)
	}
	if _, err := server.store.db.Exec(`INSERT INTO family_requests (family_id, player_id) VALUES (?, ?), (88, ?)`,
		familyID, applicant.session.playerID, applicant.session.playerID); err != nil {
		t.Fatal(err)
	}

	approved := server.onHandleEnterFamiy(leader, &protocol.C2M_HandleEnterFamiy{
		Id: applicant.session.playerID, IsAgree: true,
	}).(*protocol.M2C_HandleEnterFamiy)
	if approved.Message != "" {
		t.Fatalf("approval response=%+v", approved)
	}
	var requestCount int
	if err := server.store.db.QueryRow(`SELECT COUNT(*) FROM family_requests WHERE player_id = ?`, applicant.session.playerID).Scan(&requestCount); err != nil {
		t.Fatal(err)
	}
	if requestCount != 0 {
		t.Fatalf("requests after approval=%d, want 0", requestCount)
	}
}

func TestFamilyApprovalRemovesOrphanedRequest(t *testing.T) {
	server, leader, _, familyID := familyMembershipTestServer(t)
	const missingPlayerID int64 = 999
	if _, err := server.store.db.Exec(`INSERT INTO family_requests (family_id, player_id) VALUES (?, ?)`, familyID, missingPlayerID); err != nil {
		t.Fatal(err)
	}

	_, err := server.handleFamilyRequest(familyID, leader.session.playerID, missingPlayerID, true)
	if !errors.Is(err, errFamilyApplicantMissing) {
		t.Fatalf("orphan approval error=%v", err)
	}
	var requestCount int
	if err := server.store.db.QueryRow(`SELECT COUNT(*) FROM family_requests WHERE player_id = ?`, missingPlayerID).Scan(&requestCount); err != nil {
		t.Fatal(err)
	}
	if requestCount != 0 {
		t.Fatalf("orphan requests after approval=%d, want 0", requestCount)
	}
}
