# Main-story loop readiness

The 2026-09-10 local log shows five final-layer rewards at 19:40:22-25,
followed by staggered returns to town. No chapter restart followed.
The previous continuation checked readiness only once after 1000 ms. Runtime
also delays ChangeMap by 700 ms for combat HUD expiry; serialized scene
startup and five-member settlement can add further delay. A not-yet-ready
member caused a silent return with no subsequent continuation.

The runner now rechecks readiness every 100 ms for town and next-layer entry.
It requires the same AI epoch, online leader, no active battle, positive HP,
the expected map/version and every member's completed scene startup. Once
ready it gives the team snapshot another 200 ms before resuming. Stop commands,
disconnects and changed map versions invalidate pending callbacks. A 30-second
readiness timeout stops the AI with a user-facing reason and diagnostic log.

This preserves final victory -> town -> same chapter layer one, not a jump to
the next chapter. Waiting does not start duplicate battles or change the saved
auto-skill preference. Tests cover normal looping, delayed HUD return, a late
party member, cancellation and readiness timeout.
