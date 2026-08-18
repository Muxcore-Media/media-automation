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

func TestAmbiguousSearchAlias(t *testing.T) {
	if !ambiguousSearchAlias("Breaking Bad", "BB") {
		t.Fatal("BB should be excluded from search")
	}
	if !ambiguousSearchAlias("Breaking Bad", "BrBa") {
		t.Fatal("BrBa should be excluded from search")
	}
	if ambiguousSearchAlias("Breaking Bad", "Breaking Bad") {
		t.Fatal("primary title is never ambiguous")
	}
	if ambiguousSearchAlias("Dune", "Dune") {
		t.Fatal("short primary title must remain searchable")
	}
	if ambiguousSearchAlias("My Neighbor Totoro", "Totoro") {
		t.Fatal("distinct long alias should remain searchable")
	}
}

func TestReleaseMatchesWantedMediaType(t *testing.T) {
	bb := []string{cleanMatchTitle("Breaking Bad")}
	if releaseMatchesWanted("Chelsea.FC.Season.Review.1999.00", "tv", bb, 2008) {
		t.Fatal("sports review must not match Breaking Bad")
	}
	if releaseMatchesWanted("BB.S01E01.HDTV", "tv", bb, 2008) {
		t.Fatal("acronym-only release should not match without BB in search titles")
	}
	if !releaseMatchesWanted("Breaking.Bad.S03E01.480p.BluRay", "tv", bb, 2008) {
		t.Fatal("real episode should match")
	}
	if releaseMatchesWanted("Breaking.Bad.2008.1080p.BluRay", "tv", bb, 2008) {
		t.Fatal("movie-shaped release should not satisfy a TV wanted item")
	}

	fc := []string{cleanMatchTitle("Fight Club")}
	if releaseMatchesWanted("Fight.Club.S01E01.HDTV", "movie", fc, 1999) {
		t.Fatal("Radarr rejects TV tokens for movies")
	}
	if !releaseMatchesWanted("Fight.Club.1999.1080p.BluRay", "movie", fc, 1999) {
		t.Fatal("scene movie should match")
	}
}

func TestScoreReleaseTitleGate(t *testing.T) {
	titles := []string{cleanMatchTitle("Fight Club")}
	match := scoreRelease(&indexerv1.SearchResult{
		Title: "Fight.Club.1999.1080p.BluRay-GROUP", Seeders: 50, Size: 8 * 1024 * 1024 * 1024,
	}, titles, 1999, "movie")
	if match <= 0 {
		t.Fatalf("expected positive score, got %d", match)
	}
	miss := scoreRelease(&indexerv1.SearchResult{
		Title: "Completely.Unrelated.2020.1080p.BluRay", Seeders: 50, Size: 8 * 1024 * 1024 * 1024,
	}, titles, 1999, "movie")
	if miss != 0 {
		t.Fatalf("expected 0 for mismatch, got %d", miss)
	}
	wrongYear := scoreRelease(&indexerv1.SearchResult{
		Title: "Female.Fight.Club.2016.1080p.BluRay", Seeders: 50, Size: 8 * 1024 * 1024 * 1024,
	}, titles, 1999, "movie")
	if wrongYear != 0 {
		t.Fatalf("expected 0 for year/title mismatch, got %d", wrongYear)
	}
}
