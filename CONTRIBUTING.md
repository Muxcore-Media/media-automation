# Contributing

## Development Setup

1. Go 1.26+
2. Clone core, searcher-module, http-downloader, and this module as siblings
3. `make test` — run unit tests
4. `make lint` — golangci-lint
5. `make proto` — regenerate protobuf (requires protoc + plugins)

## Pull Request Process

1. Branch from `main`
2. CI must pass (lint + test + build)
3. Squash-merge to `main`

## Code Style

- No comments on exported code unless necessary
- Match existing patterns in the module
- All new RPCs need proto definitions and tests
