package main

import (
	"path/filepath"
	"testing"

	"mhqserver/protocol"
)

func TestQuizScoreBoardUsesLightweightSortedSnapshot(t *testing.T) {
	store, err := OpenStore(mysqlTestDSN(t, filepath.Join(t.TempDir(), "quiz-ranking")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)

	type testPlayer struct {
		name        string
		level       int32
		score       int32
		timeMS      int64
		hasActivity bool
	}
	players := []testPlayer{
		{name: "slow", level: 11, score: 100, timeMS: 5000, hasActivity: true},
		{name: "fast-first", level: 22, score: 100, timeMS: 3000, hasActivity: true},
		{name: "fast-second", level: 33, score: 100, timeMS: 3000, hasActivity: true},
		{name: "inactive", level: 44},
	}
	for _, entry := range players {
		accountID, createErr := store.CreateAccount(entry.name, "password")
		if createErr != nil {
			t.Fatal(createErr)
		}
		playerID, createErr := store.CreatePlayer(accountID, entry.name, 1, 1)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, updateErr := store.db.Exec(`UPDATE players SET level = ? WHERE id = ?`, entry.level, playerID); updateErr != nil {
			t.Fatal(updateErr)
		}
		if !entry.hasActivity {
			continue
		}
		tx, beginErr := store.db.Begin()
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		if saveErr := saveSigninTx(tx, playerID, &signinState{QuizScore: entry.score, QuizTimeMS: entry.timeMS}); saveErr != nil {
			tx.Rollback()
			t.Fatal(saveErr)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			t.Fatal(commitErr)
		}
	}

	records, err := store.ListQuizScores(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Name != "fast-first" || records[1].Name != "fast-second" {
		t.Fatalf("limited quiz ranking = %+v", records)
	}

	server := &Server{store: store}
	response := server.onGetQuestScordInfo(
		&channel{session: featureTestSession(99)},
		&protocol.C2M_GetQuestScordInfo{},
	).(*protocol.M2C_GetQuestScordInfo)
	if response.Error != 0 {
		t.Fatalf("quiz ranking error = %d, message = %q", response.Error, response.Message)
	}
	want := []struct {
		name  string
		level int32
	}{{"fast-first", 22}, {"fast-second", 33}, {"slow", 11}, {"inactive", 44}}
	if len(response.InfoList) != len(want) {
		t.Fatalf("quiz ranking count = %d, want %d", len(response.InfoList), len(want))
	}
	for index, expected := range want {
		got := response.InfoList[index]
		if got.Name != expected.name || got.Level != expected.level {
			t.Fatalf("quiz ranking[%d] = %+v, want name %q level %d", index, got, expected.name, expected.level)
		}
	}
	if inactive := response.InfoList[3]; inactive.Scord != 0 || inactive.Time != 0 {
		t.Fatalf("inactive quiz ranking entry = %+v", inactive)
	}
}
