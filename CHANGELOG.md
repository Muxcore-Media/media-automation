# Changelog

## [0.1.51] - 2026-10-05


### Security
- gRPC server and peer dials use mesh TLS (meshtls, sdk/go/module v0.6.5) unless the dev insecure flag is set (ADR-0016/0017).

## [0.1.50] - 2026-10-05

### Changed
- Built on core v0.6.14 / sdk/go/module v0.6.4: unregisters on shutdown and re-registers after core restarts (ADR-0022).

## [0.1.49] - 2026-10-05


### Changed
- Reported version comes from muxcore.json (ADR-0021); built on core v0.6.12 / sdk/go/module v0.6.3 (mesh enrollment, ADR-0017).

## [0.1.48] - 2026-10-05


### Security
- Fixture-only dispatch guard now fails closed (ADR-0016 §3, ADR-0008; NFR-SEC-010, FR-INS-003). Previously it was only enabled when `DOWNLOADER_ENGINE` was explicitly `fixture`/`fake`, so an unset engine allowed live indexer grabs. Now unset/empty/`fixture`/`fake` and unknown values (with a logged warning) enable the guard; only `DOWNLOADER_ENGINE=live` (or deprecated alias `anacrolix`) disables it. `QBIT_FIXTURE` still forces the guard on. Matches downloader-native-torrent v0.3.11.

## [0.1.47] - 2026-10-05


### Added
- Upgrade test (`internal/upgrade_test.go`, ADR-0015 / T-M2-06): opens a v0.1.8 snapshot database (`internal/testdata/upgrade/`) with the current code twice and asserts schema superset, seeded-row read-back, new-column defaults and integrity. No migration bugs found.

## [0.1.46] - 2026-10-05


### Fixed
- Upserting a TV wanted row no longer deletes sibling episode / season-pack / series-pack rows that share the series `tmdb_id`. Placeholder reconciliation by `tmdb_id` now only merges into library rows for movies; for other types it just drops stale `tmdb_<id>` request placeholders. This restores season-pack coverage (siblings of a grabbed pack are skipped instead of re-searched).
- Regenerated the legacy `proto/automationv1` stubs (protoc 25.3, protoc-gen-go v1.36.6, protoc-gen-go-grpc v1.5.1); the committed raw descriptor was corrupt and panicked at package init. Go API unchanged.

### Changed
- Go modules resolve from published tags only (no filesystem `replace`); `make proto` uses `$(PROTOC)` and plugins from `PATH`.
- CI workflows synced from the umbrella templates (GitHub-hosted runners only).

## [0.1.45] — 2026-09-08

### Added
- `ListSeriesOverrides` / `UpsertSeriesOverride` / `DeleteSeriesOverride` so households can set per-show grab delay and preferred/ignored release groups without editing `AUTOMATION_SERIES_OVERRIDES_JSON`.

## [0.1.44] — 2026-09-08

### Added
- `UpdateQueueItem` applies `quality_profile_id` / `monitored` immediately. `queue_id` matches wanted `id`, library `item_id`, or TV `series_id` so a household profile change updates every episode row, not just the next library sync.

### Fixed
- Stall snapshots map downloader `TorrentStatus` enums to the string labels the reap loop already understands.

## [0.1.43] — 2026-08-22

### Added (cycle 133–134 hardening)
- `status_detail` column on `download_history`; `GetHistory` returns `status_detail` and derived `status_label` on each `DownloadRecord` (proto fields 15–16) for admin-ui / request-media status UX.
- `HistoryStatusLabel()` and `classifyImportError()` map machine status + scanner errors to short operator-facing reasons (watch dir, timeout, missing path, no importable files).
- 8s default peer-discovery timeout (`withPeerTimeout`) on indexer, downloader, and library module discovery when callers omit a deadline.
- Wanted sync for **books**, **comics**, and **audiobooks** via companion HTTP APIs (library gRPC port + 1); prune respects per-library sync success.

### Changed
- Central `finishHistoryStatus()` / `noteImportFailedDetail()` persist detail on download lifecycle, import retries, stall reap, and sibling supersede.

## [0.1.42] — 2026-08-21

### Fixed
- `retryImportFailed` uses a 2h `ImportPath` timeout (was 5m) and logs at Info/Warn when retries find nothing or import zero files — large mesh-assembled season packs can complete import after assemble.

## [0.1.41] — 2026-08-21

### Fixed
- `download.completed` no longer marks history `completed` when `ImportPath` imported zero files; stays `import_failed` so RSS retry can run after assemble fixes.

## [0.1.40] — 2026-08-21

### Fixed
- Mesh import paths map `storage://torrent/pending` (and `pending_*` partial dirs) to `storage://torrent/{infohash}` / `btih_{infohash}` once metadata is known, matching downloader-native-torrent save-path renames.

## [0.1.39] — 2026-08-20

### Added
- On `download.completed` (and file-imported completion), immediately `RemoveTorrent(delete_files)` other in-flight grabs for the same wanted item and mark them `superseded`.

## [0.1.38] — 2026-08-20

### Fixed
- `download.completed` import targets preserve `storage://…` URIs (mesh torrent assemble). `filepath.Clean` no longer collapses `storage://` into `storage:/`, so scanner `ImportPath` can stream from StorageService.

## [0.1.37] — 2026-08-20

### Added
- Operator RPCs: `RemoveFromQueue`, `ListBlocklist` / `ClearBlocklist`, `ListDelayProfiles` / `UpsertDelayProfile`, `ListCutoffUnmet` for admin queue/blocklist/delay/cutoff UX.

## [0.1.36] — 2026-08-18

### Added
- `SearchNow` RPC and `search_now` setting run one wanted-search pass immediately (ignores the RSS last-searched gap) so a deploy can be verified without waiting 15 minutes.

## [0.1.35] — 2026-08-18

### Fixed
- `download.started` persists `btih_{infohash}` when the downloader has already renamed a `pending_*` partial dir, so later same-hash grabs reuse the short identity.

## [0.1.34] — 2026-08-18

### Fixed
- Leftover `{cwd}/partials` (vault `mvp/partials`) is moved into `AUTOMATION_DOWNLOAD_DIR/partials` on start. After a successful import, `cleanupWantedPartials` also prunes abandoned dirs under the cwd leftover, not only siblings of the kept watch-dir path.

## [0.1.33] — 2026-08-18

### Fixed
- ImportPath never double-joins `save_path` onto a torrent file that already includes that prefix. `download.started` / `completed` persist the resulting absolute file list as `import_paths`.

## [0.1.32] — 2026-08-18

### Fixed
- Episode-grain searches never grab a multi-season remux (`S01-S05`). Hits larger than `max_release_gb` (default 80 GiB) are skipped before `AddTorrent` so a pack wanted row cannot occupy disk with a full-series remux.

## [0.1.31] — 2026-08-18

### Fixed
- Same-infohash search hits merge trackers on the first grab (not only attempt loop 2), and an HTTP proxy URL is swapped for a magnet sibling so history stores `infohash` immediately.

## [0.1.30] — 2026-08-18

### Fixed
- `media.file.imported` completes matching `import_failed` / `sent` history (path under the torrent save dir, or TMDB + season + episode for episode-grain wanted rows) so a library copy does not stay stuck until the next ImportPath retry.

## [0.1.29] — 2026-08-18

### Fixed
- Pack searches never dispatch a single-episode or title-year mismatch (`Franklin-2025-S01E02` for Franklin 1997 S5). Grain/year is filtered again before Dispatch so metadata wait is not consumed.

## [0.1.28] — 2026-08-18

### Fixed
- The same GUID, magnet hash, URL, or title cannot be `AddTorrent`’d again in the current attempt loop after it already failed or stalled. Dummy wanted rows were bursting the same Winnie the Pooh pack six times in a few seconds.

## [0.1.27] — 2026-08-18

### Fixed
- `sent` torrents whose download id is gone from the downloader (`GetTorrent` not found) stall immediately instead of waiting the stall timeout. Hours-old grabs after a downloader restart can move on. Transient RPC errors still wait for the timeout.

## [0.1.26] — 2026-08-18

### Added
- RSS search logs skip counts (`skipped_pack_cover`, `skipped_season0`, `skipped_recent`) and wanted-sync upsert totals at Info.

### Fixed
- Library sync keeps series-pack wanted rows (`season == 0 && episode == 0`) instead of deleting them because they are not ListMissing episodes. Leftover `:S0:pack` specials packs are still pruned.

## [0.1.25] — 2026-08-18

### Fixed
- Episode-grain searches only grab a title whose parsed `SxxExx` matches the wanted season and episode (`S03E12` will not take `S03E24`). A −200 grain penalty is a hard reject, not a score that quality can still outrun. Anime requires the absolute episode in the title.

## [0.1.24] — 2026-08-18

### Fixed
- Do not create season-0 dummy wanted rows (`S00E12`). `AddToQueue` coerces them to a series pack (`episode == 0`); library sync skips TMDB specials; existing dummies are pruned on each RSS cycle.

## [0.1.23] — 2026-08-18

### Fixed
- Multi-season titles that list seasons with spaces instead of a hyphen (`S01   S04`) are treated as a pack spanning those seasons, so sibling wanted rows skip search.

## [0.1.22] — 2026-08-18

### Fixed
- `keep_stalled_partials` save paths are absolute under `AUTOMATION_DOWNLOAD_DIR` / `MVP_DOWNLOADS_DIR` so torrents land in the scanner watch dir instead of `{cwd}/partials`.

## [0.1.21] — 2026-08-18

### Fixed
- Season-0 episode placeholders (`S00E01` dummy rows) never search; a SHIELD dummy had dispatched a S05 pack. Episode-grain picks also reject season-pack titles (`S05` without `E`).

## [0.1.20] — 2026-08-18

### Fixed
- RSS search runs **before** stall reap, and reap is capped at 20s. Vault has no downloader process; `FindByCapability("downloader")` blocked the cycle after `rss cycle starting` with zero skip/dispatch logs.

## [0.1.19] — 2026-08-18

### Fixed
- RSS search no longer waits for a full `syncWantedFromLibraries`. Vault sat 10–15 minutes with zero dispatches after restart while TV ListMissing ran. Library sync now runs in the background (one at a time). Cycle start/finish is Info.

## [0.1.18] — 2026-08-18

### Fixed
- Retry `import_failed` history on each RSS cycle (before library sync) so a later scanner watch-dir fix can complete a grab already on disk.
- Season-0 placeholders also skip search when the series has a `stalled` or `import_failed` grab, not only `sent`/`completed`.

## [0.1.17] — 2026-08-18

### Fixed
- Season-pack searches reject single-episode titles (`S10` pack no longer grabs `S13E02`). The release must cover the wanted season.

## [0.1.16] — 2026-08-18

### Fixed
- Season-0 request placeholders (`S00E12` dummy rows) skip indexer search once the series already has a `sent` or `completed` grab. Vault Star Trek was re-searching the same S03E24 for every placeholder every RSS cycle.

## [0.1.15] — 2026-08-18

### Fixed
- TV grabs whose **title is immediately followed by a year** must match the series year (`Franklin.2024` is not Franklin 1997; `paddington.bear.1989` is not Paddington Bear 1976). Years after `Sxx` (air dates) are still ignored.

## [0.1.14] — 2026-08-18

### Fixed
- Skip indexer search for other wanted rows of the same series when a sent/completed pack title already covers that season (`S15.Complete`, `S01-S05`). Single-episode titles do not block siblings.

## [0.1.13] — 2026-08-18

### Fixed
- Treat **completed** grabs as already taken, not only `sent`. After a season pack finishes, sibling episode wanted rows were AddTorrent'ing the same release again (King of the Hill S15).

## [0.1.12] — 2026-08-18

### Fixed
- Do not `AddTorrent` again when the same GUID, magnet hash, URL, or title is already `sent`. Season packs matching many episode wanted rows (Breaking Bad remux, Paddington S01) were being dispatched once per episode.

## [0.1.11] — 2026-08-18

### Added
- Collect v1 (`btih`) and v2 (`btmh`) magnet hashes on every grab; merge same-hash magnets (union trackers) on stall loop 2+ and when a kept partial already exists.
- Setting `keep_stalled_partials` (default off): leave stalled/failed torrent data on disk, resume matching hashes from that save path, and delete leftover `partials/{item}/` dirs after a successful import.

## [0.1.10] — 2026-08-18

### Fixed
- On `download.completed`, import the torrent's **file paths** instead of the shared downloads root so each completion does not rescan every other release sitting in the watch dir.

## [0.1.9] — 2026-08-18

### Fixed
- Failed / stalled torrents are **blacklisted for the current attempt loop** so the next search tries the next-best release instead of re-dispatching the same dead GUID (the vault Arthur / When Calls the Heart metadata-timeout loop).
- `sent` downloads with **no byte progress** are reaped after a stall timeout, removed from the downloader, and no longer block `hasInFlightDownload` forever.
- In-flight items no longer refresh `last_searched`, so a stalled grab can be replaced on the next search cycle instead of waiting another RSS interval.
- Missed `download.failed` events: if the downloader reports `error`, history is marked failed immediately and the GUID is blacklisted.

### Added
- Stall watchdog (1-minute tick + RSS cycle): progress via `GetTorrent`; no-progress → `stalled` + blacklist + `RemoveTorrent`.
- Mesh settings / env: `stall_timeout_minutes` (default 180), `stall_auto_mode` (default on), `stall_loop_minutes` (default `60,360` — 1h then 6h). Auto mode starts a new loop after every available torrent has been tried.
- Indexer search timeout raised 30s → 90s so Prowlarr fan-out across many public indexers is not canceled mid-search.

## [0.1.8] — 2026-08-10

### Tests
- Offline regression: Dispatch → `download.completed` → scanner `ImportPath`
- Queue/history pagination load + clamp tests at admin-ui page sizes (25/10)
- Series override lookup + preferred/ignored release-group application coverage

## [0.1.7] — 2026-08-10

### Fixed
- Sync Info()/muxcore.json version to **0.1.7**.

## v0.1.6 (2026-08-10)

- Publish `media.import.failed` when scanner is unavailable or `ImportPath` errors (contracts v0.5.3)

## v0.1.3 (2026-08-10)

- Anime absolute-episode polish: `AddToQueue` / `SearchItem` accept `absolute_number`/`absolute` + `series_type` (+ optional `series_id`); skip season-pack wanted rows for `series_type=anime` so absolute searches stay episode-grain

## v0.1.2 (2026-08-09)

- Per-series overrides: `series_overrides` table + setting/`AUTOMATION_SERIES_OVERRIDES_JSON` (`delay_minutes`, preferred/ignored release groups)
- Drain wanted rows before indexer searches (avoid holding SQLite cursor across RPCs)
- GetQueue / GetHistory release `mu` before SQLite work so admin `/automation` stays interactive during library sync / ImportPath
- Enforce quality-profile cutoff, upgrades, and upgrade delay: keep wanted rows after import when below cutoff and `upgrade_allowed`; grab only strictly better scores after `upgrade_delay_minutes`
- Apply `min_score` on local scoring fallback; skip Dispatch while a download for the item is still `sent`
- Protocol delay profiles (`delay_profiles` + `release_seen`): wait before grab (seeded torrent 15m / usenet 0)
- Mesh `settings` capability: `enable_automatic_search`, `enable_automatic_upgrades`, `rss_sync_minutes` (default 15)
- Score via `media-custom-formats` when reachable; otherwise local heuristic scoring
- Multi-indexer fan-out: discover all `indexer` modules, parallel `Search`, merge/dedupe by GUID (or download URL), then score and limit
- Persist downloader id on `download_history.download_id` at Dispatch; expose on `GetHistory`
- On `download.completed` / `download.failed`: correlate history by `download_id`, call media-scanner `ImportPath` on success, update history status (`completed` / `failed` / `import_failed`)

## v0.1.1 (2026-08-09)

- Patch release of MVP host hardening (see GitHub Release notes)

## v0.1.0 (2026-06-14)

- Initial release
- `AutomationService` gRPC API (SearchItem, Dispatch, AddToQueue, GetQueue, GetHistory)
- Quality scoring engine (resolution, format, seeders, size)
- Search integration via searcher-module
- Download dispatch via http-downloader
- Wanted items queue with SQLite persistence
- Periodic RSS sync loop (15 min)
- Download history tracking
