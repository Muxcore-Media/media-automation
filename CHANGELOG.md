# Changelog

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
