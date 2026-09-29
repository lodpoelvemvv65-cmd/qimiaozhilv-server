# Legacy equipment attribute keys

The 2026-08-23 mapping correction (7a29c1e) changed the old reversed pairs
20/21 (constitution/stamina), 22/30 (physical reduction/damage increase) and
23/31 (mental reduction/damage increase). Existing MainAttr dictionaries were
not migrated at that time.

Before accepting game connections, the server now repairs unambiguous pairs
in player_item_main_attributes using the EquipBase configuration loaded from
MySQL. Bag, worn and warehouse items are covered. Login performs the same
repair before sending equipment or calculating character attributes.

A move requires all of the following:

- The stored key has no nonzero template field and is not the special key.
- The paired key has a nonzero template field or is the special key.
- The paired key is absent from the instance, including an explicit zero.

The SQL transaction changes only attribute_type, preserving the original
DOUBLE value exactly. It does not reroll equipment, change template values,
quality, stars, strengthening, affixes, gems or purchase origins. Repeated
startup/login is idempotent. Successful changes log player/location/slot/item,
old and new keys and the retained delta under EQUIP-ATTR-MIGRATION.

If both attributes are valid or both keys are present, historical and current
instances cannot be reliably distinguished without an original snapshot.
Those ambiguous records are deliberately left unchanged.

Example: item 121155 has Sta=500 and no Phy. The legacy key 20 delta
0.08361881226301193 moves to key 21. Its base stamina bonus becomes about
541.8094, and the extra zero-constitution tooltip row disappears.

Tests: go test ./...; set MHQ_TEST_MYSQL_DSN to run
TestLegacyEquipmentAttributeMigrationMySQL against an isolated test database.
TestLegacyShoeMigrationAlignsWireAndStamina checks both the wire keys and the
server attribute bonus. Keep a database backup before rollout: reverting the
binary alone does not undo migrated attribute keys. Do not restore an old
snapshot over subsequent player progress without review.

## Evidence-backed and cross-profession repairs

The offline `cmd/repair-equipment-attributes` command defaults to preview.
Stop the game process and back up MySQL before passing `-execute`. It locks
selected equipment attributes and commits all edits in one transaction.

For the weapon 121214, `logs/runtime/server-blue-mech.err.log:11522` records
the 2026-08-22 roll for session 8 (player 2, confirmed at line 3337), before
the mapping correction. Its unchanged double values identify the historical
pair. The identical same-profession clone is covered by the same fingerprint:

```powershell
go run ./cmd/repair-equipment-attributes -swap-players 2,3902 -swap-item 121214 -expected-20 0.1814524084329605 -expected-21 0.14883722364902496 -normalize-players 3903,3904,3905
```

The fingerprint is checked before swapping, and the already-swapped state is
a no-op. A changed pair aborts rather than overwriting newer rolls. Other
dual-attribute items without historical evidence are not guessed or swapped.

Cross-profession cloning now rebuilds the main-attribute key set against the
target MySQL template. Shared keys preserve deltas. Physical/mental attack,
critical rate and critical effect deltas transfer to their opposite channel
only if the old channel is absent and the new one has no stored value. Other
new target attributes receive zero delta (their full base value still applies).
Unsupported keys are omitted, not exchanged arbitrarily between primary stats.
The same normalization repairs existing explicitly selected clones only when
unsupported keys are present. Quality, stars, affixes, sockets, item provenance
and unrelated player state remain untouched.
