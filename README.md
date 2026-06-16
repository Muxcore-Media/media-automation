# Media Automation

[![CI](https://github.com/Muxcore-Media/media-automation/actions/workflows/ci.yml/badge.svg)](https://github.com/Muxcore-Media/media-automation/actions)
[![Go Version](https://img.shields.io/badge/Go-1.26-blue)](https://go.dev/)
[![License: GPL-3.0](https://img.shields.io/badge/License-GPL--3.0-blue.svg)](LICENSE)

**Automation engine for MuxCore — searches searchers, scores releases, and dispatches downloads for wanted media.**

A MuxCore sidecar module that bridges the gap between library management and downloading. It monitors wanted items, searches searchers, scores results by quality, and sends the best match to a downloader.

---

## How It Works

```
Admin UI ──→ media-automation ──→ searcher-module (search)
                │
                ├──→ http-downloader (dispatch)
                │
                └──→ SQLite (queue + history)
```

### Key Features

- **Quality scoring** — ranks releases by resolution (2160p > 1080p > 720p), format (Remux > BluRay > WEB-DL > HDTV), seeders, and size sanity
- **Search integration** — discovers and queries searcher-module via gRPC
- **Download dispatch** — sends selected releases to http-downloader
- **Wanted items queue** — persistence via SQLite with monitoring and missing state
- **Periodic RSS sync** — automatically searches for wanted items every 15 minutes
- **Download history** — tracks all dispatched downloads with status

---

## Configuration

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
| `MUXCORE_GRPC_INSECURE` | `false` | Disable TLS for dev |

---

## Quick Start

```bash
# Build
make build

# Run against local core (dev mode)
export MUXCORE_GRPC_INSECURE=true
./media-automation --muxcore-mesh-addr localhost:9090
```

---

## gRPC API

### `SearchItem`
Score-based search across all configured searchers.

```json
{
  "item_type": "movie",
  "query": "Fight Club",
  "year": 1999,
  "limit": 50
}
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
Queue management and history.

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
