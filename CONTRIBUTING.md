# Contributing

## Development Setup

1. Go 1.26+
2. Dependencies resolve from published GitHub tags (`GOPRIVATE='github.com/Muxcore-Media/*'`); `go.mod` has no filesystem `replace` directives. For cross-module work use the umbrella `go.work`
3. `make test` — run unit tests
4. `make lint` — golangci-lint
5. `make proto` — regenerate protobuf (protoc 25.3, `protoc-gen-go` v1.36.6 and `protoc-gen-go-grpc` v1.5.1 on `PATH`; override with `PROTOC=...`)

## Pull Request Process

1. Branch from `master`
2. CI must pass (lint + test + build)
3. Squash-merge to `master`

## Code Style

- No comments on exported code unless necessary
- Match existing patterns in the module
- All new RPCs need proto definitions and tests
