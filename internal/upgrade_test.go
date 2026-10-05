package internal

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Muxcore-Media/core/sdk/go/module/moduletest"

	automationv1 "github.com/Muxcore-Media/contracts-automation/muxcore/automation/v1"
)

// upgradeSnapshots lists the released versions whose databases the current
// code must open without data loss (ADR-0015). Fixtures live in
// testdata/upgrade/ and are produced per testdata/upgrade/README.md.
var upgradeSnapshots = []string{"v0.1.8"}

func openUpgradeModule(t *testing.T, dbPath string) *Module {
	t.Helper()
	m := NewModule(Config{DBPath: dbPath, GRPCAddr: "127.0.0.1:0"})
	if err := m.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return m
}

func TestUpgradeFromSnapshots(t *testing.T) {
	for _, tag := range upgradeSnapshots {
		t.Run(tag, func(t *testing.T) {
			ctx := context.Background()
			path := moduletest.CopyFixture(t, filepath.Join("testdata", "upgrade", tag+".db"))

			// Open twice to prove startup migration is idempotent.
			first := openUpgradeModule(t, path)
			if err := first.Stop(ctx); err != nil {
				t.Fatalf("Stop: %v", err)
			}
			m := openUpgradeModule(t, path)
			t.Cleanup(func() { _ = m.Stop(ctx) })

			fresh := openUpgradeModule(t, filepath.Join(t.TempDir(), "fresh.db"))
			t.Cleanup(func() { _ = fresh.Stop(ctx) })

			moduletest.RequireSchemaSuperset(t, moduletest.Schema(t, m.db), moduletest.Schema(t, fresh.db))

			// Seeded rows read back through the current API.
			q, err := m.GetQueue(ctx, &automationv1.GetQueueRequest{PageSize: 100})
			if err != nil {
				t.Fatalf("GetQueue: %v", err)
			}
			if q.Total != 3 || len(q.Items) != 3 {
				t.Fatalf("queue total=%d items=%d, want 3", q.Total, len(q.Items))
			}
			byID := map[string]*automationv1.QueueItem{}
			for _, it := range q.Items {
				byID[it.Id] = it
			}
			w1, w2, w3 := byID["w1"], byID["w2"], byID["w3"]
			if w1 == nil || w2 == nil || w3 == nil {
				t.Fatalf("missing queue items: %v", byID)
			}
			if w1.ItemType != "movie" || w1.ItemId != "m-100" || w1.TmdbId != 603 || w1.Title != "The Matrix" ||
				w1.Year != 1999 || !w1.Monitored || !w1.Missing || w1.QualityProfileId != "qp-hd" ||
				w1.LastSearched != "2026-01-02T03:04:05Z" {
				t.Errorf("w1 mismatch: %+v", w1)
			}
			if w2.SeasonNumber != 2 || w2.EpisodeNumber != 5 || w2.Missing || w2.QualityProfileId != "qp-uhd" || w2.Title != "Breaking Bad" {
				t.Errorf("w2 mismatch: %+v", w2)
			}
			if w3.Monitored || !w3.Missing || w3.EpisodeNumber != 1000 {
				t.Errorf("w3 mismatch: %+v", w3)
			}

			var absNum, score int
			var seriesType, seriesID, cleanTitles, acquired string
			if err := m.db.QueryRowContext(ctx, `SELECT absolute_number, series_type, series_id, clean_titles, current_score, file_acquired_at FROM wanted_items WHERE id='w2'`).
				Scan(&absNum, &seriesType, &seriesID, &cleanTitles, &score, &acquired); err != nil {
				t.Fatal(err)
			}
			if absNum != 15 || seriesType != "standard" || seriesID != "tv-1396" || cleanTitles != `["breaking bad"]` || score != 850 || acquired != "2026-01-03T10:00:00Z" {
				t.Errorf("w2 extra columns: %d %q %q %q %d %q", absNum, seriesType, seriesID, cleanTitles, score, acquired)
			}

			h, err := m.GetHistory(ctx, &automationv1.GetHistoryRequest{PageSize: 100})
			if err != nil {
				t.Fatalf("GetHistory: %v", err)
			}
			if h.Total != 3 || len(h.Records) != 3 {
				t.Fatalf("history total=%d records=%d, want 3", h.Total, len(h.Records))
			}
			hist := map[string]*automationv1.DownloadRecord{}
			for _, r := range h.Records {
				hist[r.Id] = r
			}
			h1 := hist["h1"]
			if h1 == nil || h1.WantedItemId != "w1" || h1.Guid != "guid-1" || h1.Title != "The.Matrix.1999.1080p.BluRay-GRP" ||
				h1.Indexer != "idx-a" || h1.Size != 8589934592 || h1.Score != 420 ||
				h1.DownloadUrl != "https://tracker.example/dl?passkey=SECRET123" || h1.DownloadProtocol != "torrent" ||
				h1.Status != "completed" || h1.SentAt != "2026-01-02T04:00:00Z" || h1.CompletedAt != "2026-01-02T06:00:00Z" ||
				h1.DownloadId != "dl-111" {
				t.Errorf("h1 mismatch: %+v", h1)
			}
			if h2 := hist["h2"]; h2 == nil || h2.Status != "sent" || h2.CompletedAt != "" || h2.DownloadProtocol != "usenet" {
				t.Errorf("h2 mismatch: %+v", h2)
			}
			if h3 := hist["h3"]; h3 == nil || h3.Status != "pending" || h3.DownloadId != "" {
				t.Errorf("h3 mismatch: %+v", h3)
			}

			dp, err := m.ListDelayProfiles(ctx, &automationv1.ListDelayProfilesRequest{})
			if err != nil {
				t.Fatalf("ListDelayProfiles: %v", err)
			}
			got := map[string]int32{}
			for _, p := range dp.Profiles {
				got[p.Protocol] = p.WaitMinutes
			}
			if len(got) != 3 || got["torrent"] != 30 || got["usenet"] != 5 || got["custom"] != 99 {
				t.Errorf("delay profiles = %v", got)
			}

			var seen int
			if err := m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM release_seen`).Scan(&seen); err != nil || seen != 2 {
				t.Errorf("release_seen count=%d err=%v, want 2", seen, err)
			}

			so, err := m.ListSeriesOverrides(ctx, &automationv1.ListSeriesOverridesRequest{})
			if err != nil {
				t.Fatalf("ListSeriesOverrides: %v", err)
			}
			if len(so.Overrides) != 2 {
				t.Fatalf("overrides = %v", so.Overrides)
			}
			if o := so.Overrides[0]; o.SeriesId != "tv-1396" || o.DelayMinutes != 60 ||
				len(o.PreferredGroups) != 2 || o.PreferredGroups[0] != "GRPA" || o.PreferredGroups[1] != "GRPB" ||
				len(o.IgnoredGroups) != 1 || o.IgnoredGroups[0] != "BADGRP" {
				t.Errorf("override tv-1396 mismatch: %+v", o)
			}
			if o := m.seriesOverride(ctx, "tv-37854"); o == nil || o.DelayMinutes != nil {
				t.Errorf("override tv-37854 should have nil delay: %+v", o)
			}

			// Columns added after the snapshot take their defaults on old rows.
			var badWanted int
			if err := m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wanted_items WHERE attempt_loop IS NOT 1`).Scan(&badWanted); err != nil || badWanted != 0 {
				t.Errorf("wanted_items.attempt_loop default: bad=%d err=%v", badWanted, err)
			}
			var badHist int
			if err := m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM download_history WHERE attempt_loop IS NOT 1 OR last_bytes IS NOT 0
				OR last_progress_at IS NOT '' OR infohash IS NOT '' OR infohash_v2 IS NOT '' OR save_path IS NOT ''
				OR files_fingerprint IS NOT '' OR import_paths IS NOT '' OR status_detail IS NOT ''`).Scan(&badHist); err != nil || badHist != 0 {
				t.Errorf("download_history new-column defaults: bad=%d err=%v", badHist, err)
			}
			var bl int
			if err := m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM release_blacklist`).Scan(&bl); err != nil || bl != 0 {
				t.Errorf("release_blacklist count=%d err=%v, want 0", bl, err)
			}

			moduletest.RequireIntegrity(t, m.db)
		})
	}
}
