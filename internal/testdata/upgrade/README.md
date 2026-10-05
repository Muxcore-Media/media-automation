# Upgrade fixtures (ADR-0015, T-M2-06)

`v0.1.8.db` is a SQLite database created and seeded by the code at tag
`v0.1.8` (the previous release-train pin; also the module's only tag before
`v0.1.46`). `v0.1.8.schema.sql` is its `.schema`. `../upgrade_test.go` copies
it, opens it twice with the current `Module.Init`, and checks schema superset,
seeded rows, new-column defaults and integrity.

## Produced by

1. `git worktree add /tmp/media-automation-v0.1.8 v0.1.8`
2. Copy `seed_upgrade_test.go.txt` to `internal/seed_upgrade_test.go` there
   (build tag `upgradeseed`).
3. `UPGRADE_SEED_DB=/tmp/seed.db GOWORK=off go test -tags upgradeseed -run TestUpgradeSeed ./internal/`
   (the old go.sum no longer matches republished core tags; a throwaway
   `-modfile` with an empty sum and `GOSUMDB=off` was used).
4. `sqlite3 /tmp/seed.db 'PRAGMA journal_mode=DELETE; VACUUM'`, copy to
   `v0.1.8.db`, `sqlite3 v0.1.8.db .schema > v0.1.8.schema.sql`.

## Seed summary

- `wanted_items`: 3 rows (movie, TV episode with series/absolute/score data, unmonitored anime episode).
- `download_history`: 3 rows (completed, sent, pending), including a tracker URL with a passkey-like token.
- `delay_profiles`: torrent=30, usenet=5, custom=99.
- `release_seen`: 2 rows.
- `series_overrides`: 2 rows (one with preferred/ignored groups, one with NULL delay).

The tables `release_blacklist` and the stall columns did not exist at v0.1.8;
the test asserts they appear with defaults. Per ADR-0015, add a new snapshot
(and extend `upgradeSnapshots`) when the DDL changes in the next release.
