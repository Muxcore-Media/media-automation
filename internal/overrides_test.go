package internal

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestParseReleaseGroup(t *testing.T) {
	if got := parseReleaseGroup("Show.S01E01.1080p.WEB-DL-FLUX"); got != "FLUX" {
		t.Fatalf("got %q", got)
	}
	if got := parseReleaseGroup("Movie.2020.Remux-SPARKS"); got != "SPARKS" {
		t.Fatalf("got %q", got)
	}
	if got := parseReleaseGroup("no group here"); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyReleaseGroupOverrides(t *testing.T) {
	in := []scoredRelease{
		{Title: "Show.S01E01-RARBG", Score: 100, GUID: "1"},
		{Title: "Show.S01E01-FLUX", Score: 90, GUID: "2"},
		{Title: "Show.S01E01-OTHER", Score: 95, GUID: "3"},
	}
	out := applyReleaseGroupOverrides(in, []string{"FLUX"}, []string{"RARBG"})
	if len(out) != 2 {
		t.Fatalf("len=%d", len(out))
	}
	if out[0].GUID != "2" {
		t.Fatalf("expected FLUX first after boost, got %s score=%d", out[0].Title, out[0].Score)
	}
}

func TestSeriesOverrideDelay(t *testing.T) {
	dir := t.TempDir()
	m := NewModule(Config{
		GRPCAddr: ":0",
		DBPath:   filepath.Join(dir, "a.db"),
	})
	ctx := context.Background()
	if err := m.Init(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop(ctx) })

	delay := 120
	raw := `[{"series_id":"tv_99","delay_minutes":120,"preferred_groups":["FLUX"],"ignored_groups":["RARBG"]}]`
	if err := m.replaceSeriesOverridesJSON(ctx, m.db, raw); err != nil {
		t.Fatal(err)
	}
	if got := m.delayMinutesForItem(ctx, "torrent", "tv_99"); got != delay {
		t.Fatalf("override delay=%d want %d", got, delay)
	}
	if got := m.delayMinutesForItem(ctx, "torrent", "other"); got != 15 {
		t.Fatalf("default torrent delay=%d want 15", got)
	}
	// Fresh release should not elapse under 120m override.
	if m.delayElapsed(ctx, "guid-new", "torrent", "tv_99", time.Now().UTC()) {
		t.Fatal("expected delay not elapsed")
	}
}
