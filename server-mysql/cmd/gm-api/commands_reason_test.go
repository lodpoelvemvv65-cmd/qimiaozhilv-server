package main

import "testing"

func TestCommandRequiresReason(t *testing.T) {
	for _, action := range []string{"player.kick", "player.reload", "player.currency_adjust", "player.level_adjust", "player.mail", "player.items.grant"} {
		if commandRequiresReason(action) {
			t.Fatalf("%s should not require a reason", action)
		}
	}
}
