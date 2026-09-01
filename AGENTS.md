# AGENTS.md — media-automation

MuxCore sidecar module (`media-automation`). Workspace deploy and SSH: [`../AGENTS.md`](../AGENTS.md). Default ports: [`_mvp/PORTS.md`](../_mvp/PORTS.md).

## Module identity

| Field | Value |
|-------|-------|
| Directory | `media-automation` |
| Capabilities | `media.automation`, `settings` (see `muxcore.json`) |
| Contracts | `github.com/Muxcore-Media/contracts-automation` — `AutomationService` v0.1.0 |

## Agent rules

- Modules run as gRPC sidecars; capabilities are the security boundary.
- TLS required in production (`MUXCORE_INSECURE_DISABLE_TLS` is dev-only). Peer dials use `internal/peer.go` (`dialPeer`).
- Match existing Go patterns; run `gofmt` and package tests before finishing.
- Cross-module events: prefer `github.com/Muxcore-Media/contracts-media/events` over deprecated `core/pkg/contracts` aliases.
- Do not edit polluted workspace dumps (see `MASTER-ROADMAP.md` Appendix H).
- Local `proto/automationv1/` is **DEPRECATED** — generate from `contracts-automation` (`make proto`).

## Build

```bash
cd media-automation
nix-shell -p go golangci-lint --run 'export GOCACHE=/tmp/gocache-media-automation; go test ./...'
```
