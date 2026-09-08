package internal

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	autov1 "github.com/Muxcore-Media/contracts-automation/muxcore/automation/v1"
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

// TestSeriesOverrideLookupApplied mirrors searchAndStore: load override by
// series_id then filter/boost release groups before grab.
func TestSeriesOverrideLookupApplied(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	raw := `[{"series_id":"series_anime","preferred_groups":["EMBER"],"ignored_groups":["CAMRG"]}]`
	if err := m.replaceSeriesOverridesJSON(ctx, m.db, raw); err != nil {
		t.Fatal(err)
	}
	o := m.seriesOverride(ctx, "series_anime")
	if o == nil {
		t.Fatal("expected series override")
	}
	in := []scoredRelease{
		{Title: "Show.S01E01.1080p.WEB-DL-CAMRG", Score: 200, GUID: "cam"},
		{Title: "Show.S01E01.1080p.WEB-DL-EMBER", Score: 100, GUID: "good"},
		{Title: "Show.S01E01.1080p.WEB-DL-OTHER", Score: 150, GUID: "plain"},
	}
	out := applyReleaseGroupOverrides(in, o.PreferredGroups, o.IgnoredGroups)
	if len(out) != 2 {
		t.Fatalf("len=%d want 2", len(out))
	}
	if out[0].GUID != "good" {
		t.Fatalf("expected EMBER first after boost, got %s score=%d", out[0].GUID, out[0].Score)
	}
	if m.seriesOverride(ctx, "missing") != nil {
		t.Fatal("expected nil for unknown series")
	}
}

func TestSeriesOverrideRPC(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	up, err := m.UpsertSeriesOverride(ctx, &autov1.UpsertSeriesOverrideRequest{
		Override: &autov1.SeriesOverride{
			SeriesId: "s1", DelayMinutes: 45,
			PreferredGroups: []string{"FLUX"}, IgnoredGroups: []string{"RARBG"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if up.Override.DelayMinutes != 45 || up.Override.PreferredGroups[0] != "FLUX" {
		t.Fatalf("%+v", up.Override)
	}
	if got := m.delayMinutesForItem(ctx, "torrent", "s1"); got != 45 {
		t.Fatalf("delay=%d", got)
	}
	list, err := m.ListSeriesOverrides(ctx, &autov1.ListSeriesOverridesRequest{})
	if err != nil || len(list.Overrides) != 1 || list.Overrides[0].SeriesId != "s1" {
		t.Fatalf("list %+v err=%v", list, err)
	}
	if _, err := m.DeleteSeriesOverride(ctx, &autov1.DeleteSeriesOverrideRequest{SeriesId: "s1"}); err != nil {
		t.Fatal(err)
	}
	if m.seriesOverride(ctx, "s1") != nil {
		t.Fatal("expected deleted")
	}
}
