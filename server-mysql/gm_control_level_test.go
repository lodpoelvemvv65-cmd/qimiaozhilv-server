package main

import (
	"testing"

	"mhqserver/internal/operationsconfig"
)

func TestApplyGMLevelAdjustGrantsOnlinePointsOnLevelUp(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.Progression.SkillPointLevels = 200
	})
	ss := &session{level: 1, charPoint: 0, skillPoint: 0}
	applyGMLevelAdjust(ss, 200, 123)
	if ss.level != 200 || ss.exp != 123 || ss.charPoint != 50 || ss.skillPoint != 1 {
		t.Fatalf("1->200: level=%d exp=%d char=%d skill=%d", ss.level, ss.exp, ss.charPoint, ss.skillPoint)
	}
	applyGMLevelAdjust(ss, 201, 0)
	if ss.level != 201 || ss.charPoint != 50 || ss.skillPoint != 1 {
		t.Fatalf("200->201: level=%d char=%d skill=%d", ss.level, ss.charPoint, ss.skillPoint)
	}
	applyGMLevelAdjust(ss, 400, 0)
	if ss.level != 400 || ss.charPoint != 100 || ss.skillPoint != 2 {
		t.Fatalf("201->400: level=%d char=%d skill=%d", ss.level, ss.charPoint, ss.skillPoint)
	}
}

func TestApplyGMLevelAdjustDoesNotClawBackOnLevelDown(t *testing.T) {
	ss := &session{level: 400, exp: 9, charPoint: 399, skillPoint: 2}
	applyGMLevelAdjust(ss, 1, 0)
	if ss.level != 1 || ss.exp != 0 || ss.charPoint != 399 || ss.skillPoint != 2 {
		t.Fatalf("400->1: level=%d exp=%d char=%d skill=%d", ss.level, ss.exp, ss.charPoint, ss.skillPoint)
	}
}
