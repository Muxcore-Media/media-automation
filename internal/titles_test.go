package internal

import (
	"testing"

	indexerv1 "github.com/Muxcore-Media/contracts-indexer/muxcore/indexer/v1"
)

func TestCleanMatchTitle(t *testing.T) {
	if got := cleanMatchTitle("Fight.Club"); got != "fight club" {
		t.Errorf("got %q", got)
	}
	if got := cleanMatchTitle("The Matrix"); got != "matrix" {
		t.Errorf("got %q", got)
	}
}

func TestReleaseMatchesTitles(t *testing.T) {
	titles := []string{cleanMatchTitle("Fight Club")}
	if !releaseMatchesTitles("Fight.Club.1999.1080p.BluRay", titles) {
		t.Fatal("expected scene release to match")
	}
	if releaseMatchesTitles("Pulp.Fiction.1994.1080p.BluRay", titles) {
		t.Fatal("expected unrelated title to miss")
	}
	if !releaseMatchesTitles("anything", nil) {
		t.Fatal("empty titles should not reject (legacy)")
	}
}

func TestScoreReleaseTitleGate(t *testing.T) {
	titles := []string{cleanMatchTitle("Fight Club")}
	match := scoreRelease(&indexerv1.SearchResult{
		Title: "Fight.Club.1999.1080p.BluRay-GROUP", Seeders: 50, Size: 8 * 1024 * 1024 * 1024,
	}, titles)
	if match <= 0 {
		t.Fatalf("expected positive score, got %d", match)
	}
	miss := scoreRelease(&indexerv1.SearchResult{
		Title: "Completely.Unrelated.2020.1080p.BluRay", Seeders: 50, Size: 8 * 1024 * 1024 * 1024,
	}, titles)
	if miss != 0 {
		t.Fatalf("expected 0 for mismatch, got %d", miss)
	}
}
