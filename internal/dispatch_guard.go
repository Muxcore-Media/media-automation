package internal

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	automationv1 "github.com/Muxcore-Media/contracts-automation/muxcore/automation/v1"
)

// fixtureDownloaderConfigured reports whether the fixture-only dispatch guard
// is active. It fails closed (ADR-0016 §3, ADR-0008): only an explicit
// DOWNLOADER_ENGINE=live (or the deprecated alias "anacrolix") disables the
// guard. Unset, empty, "fixture", "fake" and any unknown value enable it.
// QBIT_FIXTURE=1|true|on|yes forces the guard on regardless of the engine.
func fixtureDownloaderConfigured() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("QBIT_FIXTURE"))) {
	case "1", "true", "on", "yes":
		return true
	}
	engine := strings.ToLower(strings.TrimSpace(os.Getenv("DOWNLOADER_ENGINE")))
	switch engine {
	case "live", "anacrolix":
		return false
	case "", "fixture", "fake":
		return true
	}
	warnUnknownEngineOnce(engine)
	return true
}

var unknownEngineWarned sync.Map

func warnUnknownEngineOnce(engine string) {
	if _, loaded := unknownEngineWarned.LoadOrStore(engine, struct{}{}); !loaded {
		log.Printf("media-automation: unknown DOWNLOADER_ENGINE %q; fixture-only dispatch guard stays ON (use \"live\" to enable live acquisition)", engine)
	}
}

func fixtureIndexerGrab(indexerName, guid, downloadURL string) bool {
	if strings.Contains(strings.ToLower(strings.TrimSpace(indexerName)), "fixture") {
		return true
	}
	g := strings.ToLower(strings.TrimSpace(guid))
	if strings.Contains(g, "fixture") {
		return true
	}
	u := strings.ToLower(strings.TrimSpace(downloadURL))
	return strings.Contains(u, "fixture")
}

func validateDispatchGrab(req *automationv1.DispatchRequest) error {
	if req == nil || !fixtureDownloaderConfigured() {
		return nil
	}
	if fixtureIndexerGrab(req.GetIndexerName(), req.GetGuid(), req.GetDownloadUrl()) {
		return nil
	}
	return fmt.Errorf(
		"downloader is in fixture mode (DOWNLOADER_ENGINE unset or fixture; set DOWNLOADER_ENGINE=live to enable live acquisition): refusing live indexer grab %q from %q",
		req.GetTitle(), req.GetIndexerName(),
	)
}
