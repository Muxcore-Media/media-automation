package internal

import (
	"fmt"
	"os"
	"strings"

	automationv1 "github.com/Muxcore-Media/contracts-automation/muxcore/automation/v1"
)

func fixtureDownloaderConfigured() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DOWNLOADER_ENGINE"))) {
	case "fixture", "fake":
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("QBIT_FIXTURE"))) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
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
		"downloader is in fixture mode (DOWNLOADER_ENGINE=fixture): refusing live indexer grab %q from %q",
		req.GetTitle(), req.GetIndexerName(),
	)
}
