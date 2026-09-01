# Media Automation

[![CI](https://git.zem.systems/muxcore/media-automation/actions/workflows/ci.yml/badge.svg)](https://git.zem.systems/muxcore/media-automation/actions)
[![Go Version](https://img.shields.io/badge/Go-1.26-blue)](https://go.dev/)
[![License: GPL-3.0](https://img.shields.io/badge/License-GPL--3.0-blue.svg)](LICENSE)

**Automation engine for MuxCore — searches indexers, scores releases, and dispatches downloads for wanted media.**

A MuxCore sidecar module that bridges the gap between library management and downloading. It monitors wanted items, searches indexer modules, scores results by quality, and sends the best match to a downloader.

---

## How It Works

```
Admin UI ──→ media-automation ──→ indexer modules (parallel Search)
                │                      ├── site module A
                │                      ├── site module B
                │                      └── aggregator (optional)
                ├──→ downloader (dispatch)
                │
                └──→ SQLite (queue + history)
```

### Key Features

- **Wanted library sync** — merges missing items from movies/TV (gRPC), music (gRPC `ListMissing`), and books/comics/audiobooks (HTTP `/api/missing` on companion port gRPC+1)
- **Quality scoring** — `media-custom-formats` TRaSH scoring for movie/TV only; music/book/comic/audiobook use title-match + seeders fallback (no 2160p/Remux heuristics)
- **Upgrade / cutoff / delay** — after import, keeps wanted items below profile cutoff when upgrades are allowed; re-searches and grabs only strictly better scores after `upgrade_delay_minutes`
- **Protocol delay profiles** — waits before grab using seeded `delay_profiles` (default: torrent 15m, usenet 0)
- **Per-series overrides** — optional `delay_minutes` plus preferred/ignored release groups (`series_overrides_json` setting or `AUTOMATION_SERIES_OVERRIDES_JSON`)
- **Multi-indexer search** — discovers **all** modules advertising capability `indexer`, searches them in parallel, merges results, dedupes by GUID (or download URL), then scores and limits
- **Download dispatch** — sends selected releases to a downloader module. A season pack that is already `sent` or `completed` is not AddTorrent'd again; sibling episodes of the covered season skip indexer search. TV releases with a year glued to the title (`Franklin.2024`) must match the series year.
- **Wanted items queue** — persistence via SQLite with monitoring and missing state
- **Periodic RSS sync** — automatically searches for wanted items on an interval (default 15 minutes; mesh setting `rss_sync_minutes`)
- **Mesh settings** — capability `settings`: `enable_automatic_search`, `enable_automatic_upgrades`, `rss_sync_minutes`, `wanted_search_limit`, `max_release_gb`, plus stall knobs (`stall_timeout_minutes`, `stall_auto_mode`, `stall_loop_minutes`, `keep_stalled_partials`)
- **Download history** — tracks all dispatched downloads with status
- **Stall / blacklist** — a torrent with no byte progress is given up after a configurable stall timeout (default 3h, or auto loops of 1h then 6h). That GUID is blacklisted for the current attempt loop so the next search dispatches the next-best release. After every available torrent has been tried, a new loop retries them with a longer stall.
- **Import on complete** — on `download.completed`, asks media-scanner to `ImportPath` the torrent save path and marks history complete (or `import_failed`); `download.failed` marks history failed. On `media.*.file_added`, marks the wanted item owned (or removes it at cutoff / when upgrades disabled).

---

## Configuration

### Ops note: scanner watch directory

For import-on-complete to work, register the downloader’s `DOWNLOAD_DIR` (or whatever path torrents finish in) as a **media-scanner watch directory**. `ImportPath` only accepts paths under a registered watch dir; otherwise the history row is marked `import_failed`.

### CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--muxcore-mesh-addr` | - | Core gRPC address |
| `--muxcore-module-id` | `media-automation` | Module identity |

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `AUTOMATION_DB_PATH` | `/var/lib/media-automation/automation.db` | SQLite database path |
| `AUTOMATION_GRPC_ADDR` | `:9460` | gRPC listen address |
| `MUXCORE_GRPC_ADDR` | `localhost:9090` | Core mesh gRPC address |
| `MUXCORE_MODULE_ID` | `media-automation` | Module identity (overrides `--muxcore-module-id`) |
| `MUXCORE_INSECURE_DISABLE_TLS` | `false` | Disable TLS for dev |
| `AUTOMATION_SERIES_OVERRIDES_JSON` | `[]` | Seed/replace per-series overrides on init (JSON array) |
| `AUTOMATION_STALL_TIMEOUT_MINUTES` | `180` | No-progress timeout when auto mode is off |
| `AUTOMATION_STALL_AUTO_MODE` | `true` | Escalate stall timeout after each full pass of available torrents |
| `AUTOMATION_STALL_LOOP_MINUTES` | `60,360` | Comma-separated stall minutes per attempt loop (auto mode) |
| `AUTOMATION_KEEP_STALLED_PARTIALS` | `false` | Keep stalled/failed torrent data and resume matching hashes |
| `AUTOMATION_MAX_RELEASE_GB` | `80` | Skip indexer hits larger than this many GiB (`0` disables) |
| `AUTOMATION_WANTED_SEARCH_LIMIT` | `8` | Max wanted items searched per RSS cycle |
| `AUTOMATION_DOWNLOAD_DIR` | (from `MVP_DOWNLOADS_DIR`) | Torrent save path root for partial reuse and import |
| `AUTOMATION_USENET_GRAB_BONUS` | `8` | Ranking bonus for usenet on missing items (see `internal/ranking.go`) |
| `AUTOMATION_DEAD_TORRENT_PENALTY` | `45` | Score penalty when a torrent has zero seeders |

---

## Quick Start

```bash
# Build
make build

# Run against local core (dev mode)
export MUXCORE_INSECURE_DISABLE_TLS=true
./media-automation --muxcore-mesh-addr localhost:9090
```

---

## gRPC API

### `SearchItem`
Score-based search across all discovered indexer modules (parallel fan-out, merge, dedupe).

```json
{
  "item_type": "movie",
  "query": "Fight Club",
  "year": 1999,
  "limit": 50
}
```

### `SearchNow`
Run one wanted-search pass immediately (same as RSS, but ignores the last-searched gap). Use after deploy instead of waiting `rss_sync_minutes`.

```bash
grpcurl -plaintext -d '{}' 127.0.0.1:9460 muxcore.automation.v1.AutomationService/SearchNow
# or set the search_now setting to true (admin UI / mesh UpdateSetting)
```

### `Dispatch`
Send a selected release to the downloader.

```json
{
  "guid": "abc123",
  "title": "Fight Club 1999 1080p BluRay",
  "download_url": "magnet:?...",
  "download_protocol": "http"
}
```

### `AddToQueue` / `GetQueue` / `GetHistory`
Queue management and download history.

### Operator RPCs

| RPC | Purpose |
|-----|---------|
| `RemoveFromQueue` | Drop a wanted row; cancels in-flight grabs (`download_history` status `sent`) first |
| `SetMonitored` | Pause/resume search for a queue item without deleting it |
| `SearchNow` | One immediate wanted-search pass |
| `RetryImport` | Re-run scanner `ImportPath` for `import_failed` / stalled history rows |
| `ListBlocklist` / `ClearBlocklist` / `BlocklistRelease` | Per-wanted release blacklist |
| `ListDelayProfiles` / `UpsertDelayProfile` | Protocol grab delay (torrent vs usenet) |
| `ListCutoffUnmet` | Owned items below profile cutoff with upgrades enabled |

---

## Development

```bash
make test     # run tests with race detection
make lint     # golangci-lint
make fmt      # format code
make proto    # regenerate protobuf code
```

---

## License

GPL-3.0
