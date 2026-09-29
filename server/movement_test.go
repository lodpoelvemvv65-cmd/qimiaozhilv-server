package main

import (
	"math"
	"testing"
	"time"
)

func TestMovementUsesInterpolatedPositionForRepeatedClicks(t *testing.T) {
	ss := newSession()
	ss.resetMovement(0, 0)
	started := time.Unix(100, 0)
	ss.beginMovement(started, 10, 0, 5)

	x, y := ss.beginMovement(started.Add(time.Second), 5, 5, 5)
	if math.Abs(float64(x-5)) > 0.001 || math.Abs(float64(y)) > 0.001 {
		t.Fatalf("second path started at (%.3f, %.3f), want (5, 0)", x, y)
	}
	if ss.moveStartX != x || ss.moveStartY != y || ss.moveTargetX != 5 || ss.moveTargetY != 5 {
		t.Fatalf("new movement state = start(%.3f, %.3f) target(%.3f, %.3f)",
			ss.moveStartX, ss.moveStartY, ss.moveTargetX, ss.moveTargetY)
	}
}

func TestMovementStopsAtDestination(t *testing.T) {
	ss := newSession()
	ss.resetMovement(-2, 1)
	started := time.Unix(200, 0)
	ss.beginMovement(started, 3, 1, 5)

	x, y := ss.currentPosition(started.Add(2*time.Second), 5)
	if x != 3 || y != 1 || ss.moving {
		t.Fatalf("completed movement = (%.3f, %.3f), moving=%v", x, y, ss.moving)
	}
}
