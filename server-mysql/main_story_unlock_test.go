package main

import (
	"testing"

	"mhqserver/protocol"
)

func TestMainStoryCityEntryResumesSavedLayer(t *testing.T) {
	loadOnlineTablesForTest(t)
	server := &Server{}
	ch, _ := consistencyChannel(8100, 10004)
	ch.session.killCount = make(map[int32]int32)
	for layer := int32(1); layer < 8; layer++ {
		stage, _, ok := mainStoryStageForMap(1001100 + layer)
		if !ok {
			t.Fatalf("missing configured layer %d", layer)
		}
		ch.session.killCount[mainStoryCompletionKey(stage.region)] = 1
	}
	ch.session.killCount[mainStoryResumeKey(10011)] = 8

	response, transition := server.onRequestEnterMap(ch, &protocol.C2M_RequestEnterMap{MapId: 1001101})
	if response.Message != "" || transition == nil {
		t.Fatalf("saved layer 8 entry rejected: response=%+v transition=%v", response, transition != nil)
	}
	transition()
	if ch.session.mapID != 1001108 {
		t.Fatalf("city entry map=%d, want saved layer 1001108", ch.session.mapID)
	}
}

func TestMainStoryResumeTracksEntryAndRestartsAfterFinalVictory(t *testing.T) {
	loadOnlineTablesForTest(t)
	ss := newSession()
	ss.killCount = make(map[int32]int32)
	if !setMainStoryResumeMap(ss, 1001108) {
		t.Fatal("entering layer 8 did not save its resume point")
	}
	city := newSession()
	city.mapID, city.killCount = 10004, ss.killCount
	if got := mainStoryEntryResumeMap(city, 1001101); got != 1001108 {
		t.Fatalf("saved city entry resolved=%d, want 1001108", got)
	}
	if !resetMainStoryResumeAfterFinalVictory(ss, 1001110) || ss.killCount[mainStoryResumeKey(10011)] != 1 {
		t.Fatalf("final victory resume=%d, want 1", ss.killCount[mainStoryResumeKey(10011)])
	}
}

func TestMainStoryCityEntryRepairsLegacyResumeFromCompletions(t *testing.T) {
	loadOnlineTablesForTest(t)
	ss := newSession()
	ss.mapID = 10004
	ss.killCount = make(map[int32]int32)
	for layer := int32(1); layer < 8; layer++ {
		stage, _, _ := mainStoryStageForMap(1001100 + layer)
		ss.killCount[mainStoryCompletionKey(stage.region)] = 1
	}
	if got := mainStoryEntryResumeMap(ss, 1001101); got != 1001108 {
		t.Fatalf("legacy city entry resolved=%d, want 1001108", got)
	}
	final, _, _ := mainStoryStageForMap(1001110)
	ss.killCount[mainStoryCompletionKey(final.region)] = 1
	if got := mainStoryEntryResumeMap(ss, 1001101); got != 1001101 {
		t.Fatalf("completed legacy chapter resolved=%d, want a new run at 1001101", got)
	}
}

func TestFirstNormalMainStoryEntryIsIndependentOfBeach(t *testing.T) {
	loadOnlineTablesForTest(t)
	server := &Server{}
	ch, _ := consistencyChannel(8101, 10004)

	// The first ordinary chapter is a separate city entry from the beach
	// tutorial. A fresh role must be able to enter it directly.
	response, transition := server.onRequestEnterMap(ch, &protocol.C2M_RequestEnterMap{MapId: 1001101})
	if response.Message != "" || transition == nil {
		t.Fatalf("fresh role could not enter first ordinary chapter: response=%+v transition=%v", response, transition != nil)
	}

	// The second chapter remains locked until the first chapter's last layer
	// is cleared; clearing beach alone must not unlock it.
	ch.session.killCount = map[int32]int32{mainStoryCompletionKey(1010): 1}
	response, transition = server.onRequestEnterMap(ch, &protocol.C2M_RequestEnterMap{MapId: 1001201})
	if response.Message == "" || transition != nil {
		t.Fatalf("beach-only role entered ordinary chapter 2: response=%+v transition=%v", response, transition != nil)
	}

	// Completing the previous chapter's last layer unlocks the next chapter's
	// first layer, while an arbitrary later chapter remains locked.
	ch.session.killCount[mainStoryCompletionKey(1020)] = 1
	response, transition = server.onRequestEnterMap(ch, &protocol.C2M_RequestEnterMap{MapId: 1001201})
	if response.Message != "" || transition == nil {
		t.Fatalf("role with chapter 1 clear could not enter chapter 2: response=%+v transition=%v", response, transition != nil)
	}
	response, transition = server.onRequestEnterMap(ch, &protocol.C2M_RequestEnterMap{MapId: 1001301})
	if response.Message == "" || transition != nil {
		t.Fatalf("role skipped into chapter 3: response=%+v transition=%v", response, transition != nil)
	}
	ch.session.killCount[mainStoryCompletionKey(1030)] = 1
	response, transition = server.onRequestEnterMap(ch, &protocol.C2M_RequestEnterMap{MapId: 1001301})
	if response.Message != "" || transition == nil {
		t.Fatalf("role with chapter 2 clear could not enter chapter 3: response=%+v transition=%v", response, transition != nil)
	}
}

func TestNonBeachMainStoryTransitionIsSequentialAndExact(t *testing.T) {
	loadOnlineTablesForTest(t)
	server := &Server{}
	ch, _ := consistencyChannel(8102, 1001101)

	if validMapTransition(ch.session.mapID, 1001111) {
		t.Fatal("nonexistent ordinary main-story layer was accepted")
	}
	if validMapTransition(10004, 1005211) {
		t.Fatal("nonexistent hard main-story layer was accepted")
	}

	response, transition := server.onRequestEnterMap(ch, &protocol.C2M_RequestEnterMap{MapId: 1001102})
	if response.Message == "" || transition != nil {
		t.Fatalf("uncleared layer 2 was accepted: response=%+v transition=%v", response, transition != nil)
	}
	ch.session.killCount = map[int32]int32{mainStoryCompletionKey(1011): 1}
	response, transition = server.onRequestEnterMap(ch, &protocol.C2M_RequestEnterMap{MapId: 1001103})
	if response.Message == "" || transition != nil {
		t.Fatalf("layer 3 skipped layer 2: response=%+v transition=%v", response, transition != nil)
	}
	response, transition = server.onRequestEnterMap(ch, &protocol.C2M_RequestEnterMap{MapId: 1001102})
	if response.Message != "" || transition == nil {
		t.Fatalf("cleared layer 1 could not enter layer 2: response=%+v transition=%v", response, transition != nil)
	}
}

func TestMalformedHardStoryRegionStillAdvancesByMapOrder(t *testing.T) {
	loadOnlineTablesForTest(t)
	server := &Server{}
	ch, _ := consistencyChannel(8104, 1006109)
	// MainStory's online row for 10061 layer 9 has key 1, not 1269. The
	// completion key must remain client-compatible while progression follows
	// the configured map order.
	ch.session.killCount = map[int32]int32{mainStoryCompletionKey(1): 1}
	response, transition := server.onRequestEnterMap(ch, &protocol.C2M_RequestEnterMap{MapId: 1006110})
	if response.Message != "" || transition == nil {
		t.Fatalf("layer 9 -> 10 was rejected by malformed region key: response=%+v transition=%v", response, transition != nil)
	}
}

func TestHardMainStoryChapterRequiresNormalCompletion(t *testing.T) {
	loadOnlineTablesForTest(t)
	server := &Server{}
	ch, _ := consistencyChannel(8103, 10004)
	ch.session.killCount = make(map[int32]int32)
	response, transition := server.onRequestEnterMap(ch, &protocol.C2M_RequestEnterMap{MapId: 1005201})
	if response.Message == "" || transition != nil {
		t.Fatalf("hard chapter opened without normal final victory: response=%+v transition=%v", response, transition != nil)
	}
	ch.session.killCount[mainStoryCompletionKey(1170)] = 1
	response, transition = server.onRequestEnterMap(ch, &protocol.C2M_RequestEnterMap{MapId: 1005201})
	if response.Message != "" || transition == nil {
		t.Fatalf("hard chapter remained locked after normal final victory: response=%+v transition=%v", response, transition != nil)
	}
}

func TestMainStoryTeamEntryRequiresEveryOnlineMemberUnlocked(t *testing.T) {
	loadOnlineTablesForTest(t)
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
	leader, _ := consistencyChannel(8201, 10004)
	member, _ := consistencyChannel(8202, 10004)
	server.conns[leader.id], server.conns[member.id] = leader, member
	leader.session.teamID, member.session.teamID = 1, 1
	leader.session.killCount = map[int32]int32{mainStoryCompletionKey(1011): 1}
	member.session.killCount = make(map[int32]int32)
	teamMu.Lock()
	teams[1] = &teamState{LeaderId: leader.session.playerID, Members: []int64{leader.session.playerID, member.session.playerID}}
	teamMu.Unlock()

	// Both characters are in the city and the leader has cleared ordinary
	// layer 1. The member has not, so entering layer 2 must be rejected for the
	// whole party.
	response, transition := server.onRequestEnterMap(leader, &protocol.C2M_RequestEnterMap{MapId: 1001102})
	if response.Message == "" || transition != nil {
		t.Fatalf("leader bypassed member lock: response=%+v transition=%v", response, transition != nil)
	}
	member.session.killCount[mainStoryCompletionKey(1011)] = 1
	response, transition = server.onRequestEnterMap(leader, &protocol.C2M_RequestEnterMap{MapId: 1001102})
	if response.Message != "" || transition == nil {
		t.Fatalf("unlocked party was rejected: response=%+v transition=%v", response, transition != nil)
	}
}

func TestEveryNonBeachMainStoryStageUsesConfiguredUnlockOrder(t *testing.T) {
	loadOnlineTablesForTest(t)
	stages := allMainStoryStages()
	if len(stages) != len(tables.mainStory) {
		t.Fatalf("ordered stages=%d, configured rows=%d", len(stages), len(tables.mainStory))
	}
	checkedScenes := make(map[int32]bool)
	for index, stage := range stages {
		if stage.sceneID == 10006 {
			continue
		}
		if !validMapTransition(10004, stage.mapID) {
			t.Fatalf("configured non-beach stage map=%d region=%d was rejected", stage.mapID, stage.region)
		}

		ss := newSession()
		ss.mapID = 10004
		ss.killCount = make(map[int32]int32)
		if message := nonBeachMainStoryTransitionMessage(ss, stage.mapID); message == "" {
			if !isMainStoryNormalEntry(stage) {
				t.Fatalf("stage map=%d opened without predecessor completion", stage.mapID)
			}
		} else if isMainStoryNormalEntry(stage) {
			t.Fatalf("first ordinary stage map=%d remained locked: %s", stage.mapID, message)
		}
		if isMainStoryNormalEntry(stage) {
			checkedScenes[stage.sceneID] = true
			continue
		}
		previous := stages[index-1]
		if previous.sceneID == 10006 {
			ss.killCount[beachHighestLayerKey] = previous.layer
		} else {
			ss.killCount[mainStoryCompletionKey(previous.region)] = 1
		}
		if message := nonBeachMainStoryTransitionMessage(ss, stage.mapID); message != "" {
			t.Fatalf("stage map=%d remained locked after predecessor map=%d: %s", stage.mapID, previous.mapID, message)
		}

		checkedScenes[stage.sceneID] = true
	}
	if len(checkedScenes) != 32 {
		t.Fatalf("checked non-beach scenes=%d, want 32", len(checkedScenes))
	}
	for sceneID := range checkedScenes {
		if validMapTransition(10004, sceneID*100+11) {
			t.Fatalf("scene %d accepted nonexistent layer 11", sceneID)
		}
	}
}
