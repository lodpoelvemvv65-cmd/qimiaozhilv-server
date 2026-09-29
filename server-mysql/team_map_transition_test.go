package main

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func TestMapStartupRestoresOnlyCompleteTeamSnapshot(t *testing.T) {
	teamMu.Lock()
	oldTeams, oldSeq := teams, teamSeq
	teams, teamSeq = make(map[int64]*teamState), 1
	teamMu.Unlock()
	t.Cleanup(func() {
		teamMu.Lock()
		teams, teamSeq = oldTeams, oldSeq
		teamMu.Unlock()
	})

	server := &Server{conns: make(map[int64]*channel)}
	members := make([]*channel, 0, 2)
	ids := []int64{88301, 88302}
	for _, id := range ids {
		member, _ := consistencyChannel(id, 1001102)
		member.session.teamID = 1
		seq := member.session.markMapChange()
		member.session.markMapReady(seq)
		member.session.markMapStartupComplete(seq)
		server.conns[member.id] = member
		members = append(members, member)
	}
	teamMu.Lock()
	teams[1] = &teamState{LeaderId: ids[0], Members: append([]int64(nil), ids...)}
	teamMu.Unlock()

	server.restoreTeamSnapshotAfterMapStartup(members[0])
	for _, member := range members {
		teamFrames := 0
		for _, frame := range decodeRecordedFrames(t, member.conn.(*recordingConn).Bytes()) {
			if frame.opcode != protocol.OpM2C_TeamMember {
				continue
			}
			teamFrames++
			var snapshot protocol.M2C_TeamMember
			if err := proto.Unmarshal(frame.body, &snapshot); err != nil {
				t.Fatal(err)
			}
			if snapshot.LeaderId != ids[0] || len(snapshot.UnitIds) != len(ids) {
				t.Fatalf("player %d received transient team snapshot %+v", member.session.playerID, &snapshot)
			}
		}
		if teamFrames != 1 {
			t.Fatalf("player %d team snapshot count=%d, want one", member.session.playerID, teamFrames)
		}
	}
}
