# Roadmap

## Done

- [x] Multi-indexer fan-out — discover all `indexer` modules, parallel Search, merge/dedupe/score
- [x] Automatic import on download completion — correlate `download.completed` with history, call scanner `ImportPath`
- [x] Integration with media-scanner — discover `media.scanner`, target import of download save path
- [x] Upgrade / cutoff / delay — enforce quality profile `cutoff_score`, `upgrade_allowed`, `upgrade_delay_minutes` on search→dispatch
- [x] Delay profiles — protocol-based wait (torrent vs usenet) before grab
- [x] Custom format definitions (seeded defaults + admin CRUD)
- [x] Release profile groups

## Remaining

- [x] Per-series override of delay / release groups
