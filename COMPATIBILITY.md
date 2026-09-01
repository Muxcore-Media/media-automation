# Compatibility

## Core Version

| Module Version | Core Version | Status |
|----------------|-------------|--------|
| v0.1.43        | v0.4.0+     | Current |

## Contracts

| Contract | Version | Interface |
|----------|---------|-----------|
| `github.com/Muxcore-Media/contracts-automation` | v0.1.0 | `AutomationService` (`SearchItem`, `Dispatch`, queue/history, operator RPCs) |

Soft dependencies (discovery by capability, not a declared contract):

- `indexer` — parallel `Search`
- `downloader.torrent` / `downloader.usenet` — grab dispatch
- `media.scoring` — `media-custom-formats` quality profiles
- `media.scanner` — `ImportPath` on download complete
- `media.library.movies` / `media.library.tv` — gRPC `ListMissing`
- `media.library.music` — gRPC `ListMissing`
- `media.books` / `media.comics` / `media.audiobooks` — HTTP `GET /api/missing` on gRPC port+1

## Breaking Changes

Pre-1.0 module. Proto changes land in `contracts-automation` first.
