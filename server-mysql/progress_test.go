package main

import (
	"testing"

	"mhqserver/internal/operationsconfig"
)

func TestGainExpGrantsSkillPointEveryConfiguredLevels(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.Progression.ExperienceGainPercent = 100
		config.Progression.SkillPointLevels = 200
	})
	server := &Server{}
	ss := newSession()
	ss.playerID = 1
	ss.jobID = 1
	ss.level = 199
	ch := &channel{id: 1, session: ss, conn: &recordingConn{}}

	if got := server.gainExp(ch, expNeed(199)); got != expNeed(199) {
		t.Fatalf("gain at 199 = %d, want %d", got, expNeed(199))
	}
	if ss.level != 200 || ss.charPoint != 1 || ss.skillPoint != 1 || ss.exp != 0 {
		t.Fatalf("after 199->200: level=%d char=%d skill=%d exp=%d", ss.level, ss.charPoint, ss.skillPoint, ss.exp)
	}

	if got := server.gainExp(ch, expNeed(200)); got != expNeed(200) {
		t.Fatalf("gain at 200 = %d, want %d", got, expNeed(200))
	}
	if ss.level != 201 || ss.charPoint != 1 || ss.skillPoint != 1 || ss.exp != 0 {
		t.Fatalf("after 200->201: level=%d char=%d skill=%d exp=%d", ss.level, ss.charPoint, ss.skillPoint, ss.exp)
	}

	ss.level, ss.exp, ss.charPoint, ss.skillPoint = 399, 0, 0, 1
	needed := expNeed(399) + expNeed(400)
	if got := server.gainExp(ch, needed); got != needed {
		t.Fatalf("gain at 399 = %d, want %d", got, needed)
	}
	if ss.level != 401 || ss.charPoint != 1 || ss.skillPoint != 2 || ss.exp != 0 {
		t.Fatalf("after 399->401: level=%d char=%d skill=%d exp=%d", ss.level, ss.charPoint, ss.skillPoint, ss.exp)
	}
}

func TestGainExpDoesNotClawBackExistingSkillPoints(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.Progression.ExperienceGainPercent = 100
		config.Progression.SkillPointLevels = 200
	})
	ss := newSession()
	ss.playerID = 2
	ss.jobID = 1
	ss.level = 20
	ss.charPoint = 5
	ss.skillPoint = 19
	ch := &channel{id: 2, session: ss, conn: &recordingConn{}}
	server := &Server{}
	if got := server.gainExp(ch, expNeed(20)); got != expNeed(20) {
		t.Fatalf("gain at 20 = %d, want %d", got, expNeed(20))
	}
	if ss.level != 21 || ss.charPoint != 5 || ss.skillPoint != 19 || ss.exp != 0 {
		t.Fatalf("after 20->21: level=%d char=%d skill=%d exp=%d", ss.level, ss.charPoint, ss.skillPoint, ss.exp)
	}
}

func TestProgressionPointsForLevelsMatchesOnlineGrant(t *testing.T) {
	tests := []struct {
		from, to, trans, interval int32
		char, skill               int32
	}{
		{1, 200, 0, 200, 50, 1},
		{199, 200, 0, 200, 1, 1},
		{200, 201, 0, 200, 0, 0},
		{3, 4, 0, 200, 1, 0},
		{4, 5, 0, 200, 0, 0},
		{13000, 15374, 2, 200, 593, 11},
		{13000, 19070, 2, 200, 1517, 30},
		{13000, 21500, 2, 200, 2125, 42},
		{400, 1, 0, 200, 0, 0},
	}
	for _, test := range tests {
		charPoints, skillPoints := progressionPointsForLevels(test.from, test.to, test.trans, test.interval)
		if charPoints != test.char || skillPoints != test.skill {
			t.Fatalf("%d->%d interval %d = %d/%d, want %d/%d",
				test.from, test.to, test.interval, charPoints, skillPoints, test.char, test.skill)
		}
	}
}
