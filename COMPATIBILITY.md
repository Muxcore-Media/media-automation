# Compatibility

## Core Version

| Module Version | Core Version | Status |
|----------------|-------------|--------|
| v0.1.0         | v0.4.0+     | Current |

## Contracts

None defined — this module provides its own gRPC API and depends on modules advertising `indexer` (one or many) and `downloader`. Soft dependencies: `media-custom-formats` (scoring / quality profiles), `media-scanner` (`ImportPath` on download complete), `media-movies` / `media-tvshows` (wanted sync).

## Breaking Changes

This is a pre-1.0 module. Interfaces may change without notice.
