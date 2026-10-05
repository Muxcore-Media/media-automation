//go:build race

package internal

// raceEnabled reports whether the race detector instruments this test binary.
// modernc.org/sqlite is pure Go, so race instrumentation slows queries ~25x.
const raceEnabled = true
