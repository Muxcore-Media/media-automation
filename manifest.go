package manifest

import _ "embed"

// ManifestJSON is the module's muxcore.json, embedded at build time. It is
// the single source of the module's reported version (ADR-0021); read it with
// modulesdk.ManifestVersion.
//
//go:embed muxcore.json
var ManifestJSON []byte
