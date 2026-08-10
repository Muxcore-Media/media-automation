# Changelog

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
