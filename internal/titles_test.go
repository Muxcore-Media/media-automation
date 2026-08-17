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
	if !releaseMatchesTitles("Fight.Club.1999.1080p.BluRay", titles, 1999) {
		t.Fatal("expected scene release to match")
	}
	if releaseMatchesTitles("Pulp.Fiction.1994.1080p.BluRay", titles, 1999) {
		t.Fatal("expected unrelated title to miss")
	}
	if !releaseMatchesTitles("anything", nil, 0) {
		t.Fatal("empty titles should not reject (legacy)")
	}
}

func TestReleaseMatchesTitlesRejectsSubstringHits(t *testing.T) {
	cases := []struct {
		want    string
		year    int
		release string
	}{
		{"Fight Club", 1999, "Female.Fight.Club.2016.1080p.BluRay"},
		{"Arthur", 1996, "Arthur.And.George.S01.WEB"},
		{"Arthur", 1996, "Arthur.C.Clarke.Mysterious.World.S01"},
		{"Franklin", 1997, "Franklin.and.Bash.S01E01.HDTV"},
		{"Madeline", 1988, "Madeline's.Madeline.2018.1080p"},
		{"Madeline", 1988, "SNL.Madeline.Kahn.1976"},
		{"Paddington Bear", 0, "The.Adventures.Of.Paddington.Bear.S01E01.WEB"},
	}
	for _, tc := range cases {
		titles := []string{cleanMatchTitle(tc.want)}
		if releaseMatchesTitles(tc.release, titles, tc.year) {
			t.Errorf("expected reject %q for want %q year %d", tc.release, tc.want, tc.year)
		}
	}
}

func TestReleaseMatchesTitlesAllowsTrueHits(t *testing.T) {
	cases := []struct {
		want    string
		year    int
		release string
	}{
		{"Fight Club", 1999, "Fight.Club.1999.1080p.BluRay-GROUP"},
		{"Paddington Bear", 0, "Paddington.Bear.S01E01.1080p.WEB"},
		{"Arthur", 1996, "Arthur.S01E01.1996.HDTV"},
		{"Arthur", 0, "Arthur.S01.Complete.720p"},
		{"My Neighbor Totoro", 1988, "My.Neighbor.Totoro.1988.1080p.BluRay"},
		{"Mister Rogers Neighborhood", 0, "Mister.Rogers.Neighborhood.S01E01.WEB"},
	}
	for _, tc := range cases {
		titles := []string{cleanMatchTitle(tc.want)}
		if !releaseMatchesTitles(tc.release, titles, tc.year) {
			t.Errorf("expected match %q for want %q year %d", tc.release, tc.want, tc.year)
		}
	}
}

func TestScoreReleaseTitleGate(t *testing.T) {
	titles := []string{cleanMatchTitle("Fight Club")}
	match := scoreRelease(&indexerv1.SearchResult{
		Title: "Fight.Club.1999.1080p.BluRay-GROUP", Seeders: 50, Size: 8 * 1024 * 1024 * 1024,
	}, titles, 1999)
	if match <= 0 {
		t.Fatalf("expected positive score, got %d", match)
	}
	miss := scoreRelease(&indexerv1.SearchResult{
		Title: "Completely.Unrelated.2020.1080p.BluRay", Seeders: 50, Size: 8 * 1024 * 1024 * 1024,
	}, titles, 1999)
	if miss != 0 {
		t.Fatalf("expected 0 for mismatch, got %d", miss)
	}
	wrongYear := scoreRelease(&indexerv1.SearchResult{
		Title: "Female.Fight.Club.2016.1080p.BluRay", Seeders: 50, Size: 8 * 1024 * 1024 * 1024,
	}, titles, 1999)
	if wrongYear != 0 {
		t.Fatalf("expected 0 for year/title mismatch, got %d", wrongYear)
	}
}
