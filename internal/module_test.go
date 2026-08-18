package internal

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	autov1 "github.com/Muxcore-Media/media-automation/proto/automationv1"
)

func newTestModule(t *testing.T) *Module {
	t.Helper()
	m := NewModule(Config{
		DBPath:   filepath.Join(t.TempDir(), "automation.db"),
		GRPCAddr: ":0",
	})
	ctx := context.Background()
	if err := m.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { m.Stop(ctx) })
	return m
}

func TestModuleInfo(t *testing.T) {
	m := NewModule(Config{})
	info := m.Info()
	if info.ID == "" {
		t.Error("module ID must not be empty")
	}
	if info.Version == "" {
		t.Error("module version must not be empty")
	}
	if len(info.Capabilities) == 0 || info.Capabilities[0] != "media.automation" {
		t.Errorf("expected media.automation capability, got %v", info.Capabilities)
	}
	if len(info.Roles) == 0 || info.Roles[0] != "automation" {
		t.Errorf("expected role automation, got %v", info.Roles)
	}
}

func TestAddToQueue(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	add, err := m.AddToQueue(ctx, &autov1.AddToQueueRequest{
		ItemType: "movie",
		ItemId:   "mv_550_test",
		TmdbId:   550,
		Title:    "Fight Club",
		Year:     1999,
	})
	if err != nil {
		t.Fatal(err)
	}
	if add.QueueId == "" {
		t.Fatal("expected non-empty queue ID")
	}

	queue, err := m.GetQueue(ctx, &autov1.GetQueueRequest{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if queue.Total != 1 {
		t.Errorf("expected 1 item in queue, got %d", queue.Total)
	}
	if queue.Items[0].Title != "Fight Club" {
		t.Errorf("expected 'Fight Club', got %s", queue.Items[0].Title)
	}
}

func TestAddToQueueWithQualityProfile(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	add, err := m.AddToQueue(ctx, &autov1.AddToQueueRequest{
		ItemType:         "movie",
		ItemId:           "mv_680",
		TmdbId:           680,
		Title:            "Pulp Fiction",
		Year:             1994,
		QualityProfileId: "qp_hd",
	})
	if err != nil {
		t.Fatal(err)
	}
	queue, err := m.GetQueue(ctx, &autov1.GetQueueRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if queue.Total != 1 {
		t.Fatalf("expected 1 item, got %d", queue.Total)
	}
	if queue.Items[0].QualityProfileId != "qp_hd" {
		t.Errorf("quality_profile_id: got %q", queue.Items[0].QualityProfileId)
	}
	if add.QueueId != queue.Items[0].Id {
		t.Errorf("queue id mismatch")
	}
}

func TestFilterByMinScore(t *testing.T) {
	in := []scoredRelease{
		{Title: "a", Score: 50},
		{Title: "b", Score: 100},
		{Title: "c", Score: 80},
	}
	out := filterByMinScore(in, 80)
	if len(out) != 2 {
		t.Fatalf("expected 2, got %d", len(out))
	}
	if out[0].Title != "b" && out[1].Title != "b" {
		t.Errorf("expected high-score releases kept")
	}
	if len(filterByMinScore(in, 0)) != 3 {
		t.Errorf("min_score 0 should keep all")
	}
}

func TestAddDuplicateToQueue(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	m.AddToQueue(ctx, &autov1.AddToQueueRequest{ItemType: "movie", ItemId: "m1", TmdbId: 1, Title: "Test"})
	m.AddToQueue(ctx, &autov1.AddToQueueRequest{ItemType: "movie", ItemId: "m1", TmdbId: 1, Title: "Test"})

	queue, err := m.GetQueue(ctx, &autov1.GetQueueRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if queue.Total != 1 {
		t.Errorf("expected 1 (unique), got %d", queue.Total)
	}
}

func TestGetQueuePagination(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		m.AddToQueue(ctx, &autov1.AddToQueueRequest{
			ItemType: "movie",
			ItemId:   fmt.Sprintf("m%d", i+1),
			TmdbId:   int32(100 + i),
			Title:    fmt.Sprintf("Movie %d", i+1),
			Year:     2000 + int32(i),
		})
	}

	page1, err := m.GetQueue(ctx, &autov1.GetQueueRequest{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page1.Items) != 2 {
		t.Errorf("expected 2 on page 1, got %d", len(page1.Items))
	}
	if page1.Total != 5 {
		t.Errorf("expected total 5, got %d", page1.Total)
	}

	page3, err := m.GetQueue(ctx, &autov1.GetQueueRequest{Page: 3, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page3.Items) != 1 {
		t.Errorf("expected 1 on page 3, got %d", len(page3.Items))
	}
}

func TestGetQueueFilter(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	m.AddToQueue(ctx, &autov1.AddToQueueRequest{ItemType: "movie", ItemId: "m1", TmdbId: 1, Title: "A Movie"})
	m.AddToQueue(ctx, &autov1.AddToQueueRequest{ItemType: "tv", ItemId: "t1", TmdbId: 2, Title: "A Show"})

	movies, err := m.GetQueue(ctx, &autov1.GetQueueRequest{Filter: "movie"})
	if err != nil {
		t.Fatal(err)
	}
	if movies.Total != 1 {
		t.Errorf("expected 1 movie, got %d", movies.Total)
	}
}

func TestGetHistoryEmpty(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	hist, err := m.GetHistory(ctx, &autov1.GetHistoryRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if hist.Total != 0 {
		t.Errorf("expected 0 history entries, got %d", hist.Total)
	}
}

func TestSearchItemNoIndexer(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	// Without an searcher module, SearchItem should return empty (not crash)
	results, err := m.SearchItem(ctx, &autov1.SearchItemRequest{
		ItemType: "movie",
		Query:    "Fight Club",
		Year:     1999,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results.Matches) != 0 {
		t.Errorf("expected 0 matches without indexer, got %d", len(results.Matches))
	}
}

func TestDispatchNoDownloader(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	_, err := m.Dispatch(ctx, &autov1.DispatchRequest{
		Guid:        "test-guid",
		Title:       "Test",
		DownloadUrl: "magnet:?xt=urn:btih:test",
		ItemType:    "movie",
	})
	if err == nil {
		t.Fatal("expected error without downloader")
	}
}

func TestDownloadHistoryStoresDownloadID(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)
	const downloaderID = "torrent-abc-123"

	m.mu.Lock()
	_, err := m.db.ExecContext(ctx,
		`INSERT INTO download_history (id, wanted_item_id, guid, title, indexer, size, score, download_url, download_protocol, status, sent_at, created_at, download_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'sent', ?, ?, ?)`,
		"dl_test_1", "item-1", "guid-1", "Test Title", "indexer",
		int64(100), int64(90), "magnet:?xt=urn:btih:test", "torrent",
		now, now, downloaderID,
	)
	m.mu.Unlock()
	if err != nil {
		t.Fatalf("insert history: %v", err)
	}

	hist, err := m.GetHistory(ctx, &autov1.GetHistoryRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if hist.Total != 1 {
		t.Fatalf("expected 1 history entry, got %d", hist.Total)
	}
	if hist.Records[0].DownloadId != downloaderID {
		t.Errorf("download_id: got %q, want %q", hist.Records[0].DownloadId, downloaderID)
	}

	var lookedUp string
	m.mu.RLock()
	err = m.db.QueryRowContext(ctx,
		`SELECT id FROM download_history WHERE download_id = ?`, downloaderID,
	).Scan(&lookedUp)
	m.mu.RUnlock()
	if err != nil {
		t.Fatalf("lookup by download_id: %v", err)
	}
	if lookedUp != "dl_test_1" {
		t.Errorf("lookup id: got %q, want dl_test_1", lookedUp)
	}
}

func TestPublishEventNilClient(t *testing.T) {
	m := newTestModule(t)
	m.publishEvent(context.Background(), "media.download.dispatched", []byte(`{"title":"x"}`))
}

func TestHealth(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	if err := m.Health(ctx); err != nil {
		t.Fatal("expected health to pass after init")
	}
}

func TestLifecycle(t *testing.T) {
	m := NewModule(Config{
		DBPath:   filepath.Join(t.TempDir(), "lifecycle.db"),
		GRPCAddr: ":0",
	})
	ctx := context.Background()
	if err := m.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestParseResolution(t *testing.T) {
	tests := []struct {
		name string
		want int
	}{
		{"The Movie 2160p WEB-DL", 2160},
		{"Movie 1080p BluRay", 1080},
		{"Film 720p HDTV", 720},
		{"Show S01E01 480p", 480},
		{"Movie 4K Remux", 2160},
		{"Movie UHD Remux", 2160},
		{"No resolution in name", 0},
	}
	for _, tt := range tests {
		got := parseResolution(tt.name)
		if got != tt.want {
			t.Errorf("parseResolution(%q) = %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestUpsertWantedPreservesLastSearched(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	m.upsertWanted(ctx, wantedEntry{
		ItemType: "movie", ItemID: "mv1", TmdbID: 1, Title: "A", Year: 2000, QualityProfileID: "qp1",
	})
	m.mu.Lock()
	m.db.Exec(`UPDATE wanted_items SET last_searched = '2020-01-01T00:00:00Z' WHERE item_id = 'mv1'`)
	m.mu.Unlock()

	m.upsertWanted(ctx, wantedEntry{
		ItemType: "movie", ItemID: "mv1", TmdbID: 1, Title: "A Updated", Year: 2000, QualityProfileID: "qp2",
	})

	queue, err := m.GetQueue(ctx, &autov1.GetQueueRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if queue.Total != 1 {
		t.Fatalf("expected 1, got %d", queue.Total)
	}
	if queue.Items[0].Title != "A Updated" {
		t.Errorf("title: %s", queue.Items[0].Title)
	}
	if queue.Items[0].LastSearched != "2020-01-01T00:00:00Z" {
		t.Errorf("last_searched should be preserved, got %q", queue.Items[0].LastSearched)
	}
	if queue.Items[0].QualityProfileId != "qp2" {
		t.Errorf("profile: %s", queue.Items[0].QualityProfileId)
	}
	if !queue.Items[0].Missing {
		t.Error("expected missing=true")
	}
}

func TestRemoveWantedOnFileAdded(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	m.upsertWanted(ctx, wantedEntry{ItemType: "movie", ItemID: "mv1", TmdbID: 1, Title: "A"})
	m.upsertWanted(ctx, wantedEntry{
		ItemType: "tv", ItemID: "ep1", TmdbID: 2, Title: "Show", SeasonNumber: 1, EpisodeNumber: 1,
	})

	m.removeWanted(ctx, "movie", "mv1")
	queue, err := m.GetQueue(ctx, &autov1.GetQueueRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if queue.Total != 1 || queue.Items[0].ItemType != "tv" {
		t.Fatalf("expected only tv row, got %+v", queue.Items)
	}

	m.removeWanted(ctx, "tv", "ep1")
	queue, err = m.GetQueue(ctx, &autov1.GetQueueRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if queue.Total != 0 {
		t.Errorf("expected empty queue, got %d", queue.Total)
	}
}

func TestPruneWantedNotInLibraries(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	m.upsertWanted(ctx, wantedEntry{ItemType: "movie", ItemID: "keep", TmdbID: 1, Title: "Keep"})
	m.upsertWanted(ctx, wantedEntry{ItemType: "movie", ItemID: "drop", TmdbID: 2, Title: "Drop"})
	m.upsertWanted(ctx, wantedEntry{ItemType: "tv", ItemID: "ep_keep", TmdbID: 3, Title: "Show"})

	seen := map[string]struct{}{
		"movie:keep": {},
		"tv:ep_keep": {},
	}
	m.pruneWantedNotInLibraries(ctx, seen, true, true)

	queue, err := m.GetQueue(ctx, &autov1.GetQueueRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if queue.Total != 2 {
		t.Fatalf("expected 2 after prune, got %d", queue.Total)
	}
}

func TestPruneWantedKeepsSeriesPackDropsSpecialsPack(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	m.upsertWanted(ctx, wantedEntry{
		ItemType: "tv", ItemID: "tv_tos", TmdbID: 253, Title: "Star Trek",
		SeasonNumber: 0, EpisodeNumber: 0, SeriesID: "tv_tos",
	})
	m.upsertWanted(ctx, wantedEntry{
		ItemType: "tv", ItemID: "tv_tos:S0:pack", TmdbID: 253, Title: "Star Trek",
		SeasonNumber: 0, EpisodeNumber: 0, SeriesID: "tv_tos",
	})
	m.upsertWanted(ctx, wantedEntry{
		ItemType: "tv", ItemID: "tv_tos:S1:pack", TmdbID: 253, Title: "Star Trek",
		SeasonNumber: 1, EpisodeNumber: 0, SeriesID: "tv_tos",
	})
	seen := map[string]struct{}{"tv:tv_tos:S1:pack": {}}
	m.pruneWantedNotInLibraries(ctx, seen, true, true)
	m.pruneSeasonZeroEpisodeDummies(ctx)

	var nSeries, nS0, nS1 int
	_ = m.db.QueryRow(`SELECT COUNT(*) FROM wanted_items WHERE item_id = 'tv_tos'`).Scan(&nSeries)
	_ = m.db.QueryRow(`SELECT COUNT(*) FROM wanted_items WHERE item_id = 'tv_tos:S0:pack'`).Scan(&nS0)
	_ = m.db.QueryRow(`SELECT COUNT(*) FROM wanted_items WHERE item_id = 'tv_tos:S1:pack'`).Scan(&nS1)
	if nSeries != 1 {
		t.Fatalf("series pack row kept=%d want 1", nSeries)
	}
	if nS0 != 0 {
		t.Fatal("S0 specials pack should be pruned")
	}
	if nS1 != 1 {
		t.Fatal("S01 pack in seen should remain")
	}
}

func TestSearchAndStoreDoesNotClearMissing(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	m.upsertWanted(ctx, wantedEntry{ItemType: "movie", ItemID: "mv1", TmdbID: 1, Title: "A"})
	var id string
	m.mu.RLock()
	m.db.QueryRow(`SELECT id FROM wanted_items WHERE item_id = 'mv1'`).Scan(&id)
	m.mu.RUnlock()

	m.searchAndStore(ctx, id, "movie", "mv1", "A", 1, 2000, 0, 0, 0, "", "", "", []string{cleanMatchTitle("A")}, true, 0, "")

	queue, err := m.GetQueue(ctx, &autov1.GetQueueRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if queue.Total != 1 || !queue.Items[0].Missing {
		t.Errorf("dispatch/search must not clear missing; got missing=%v total=%d",
			queue.Total > 0 && queue.Items[0].Missing, queue.Total)
	}
}

func TestTVWantedUsesEpisodeGrain(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	m.upsertWanted(ctx, wantedEntry{
		ItemType: "tv", ItemID: "ep_s1_1_2", TmdbID: 1396, Title: "Breaking Bad",
		Year: 2008, SeasonNumber: 1, EpisodeNumber: 2, QualityProfileID: "qp",
	})
	queue, err := m.GetQueue(ctx, &autov1.GetQueueRequest{Filter: "tv"})
	if err != nil {
		t.Fatal(err)
	}
	if queue.Total != 1 {
		t.Fatal(queue.Total)
	}
	item := queue.Items[0]
	if item.ItemId != "ep_s1_1_2" || item.SeasonNumber != 1 || item.EpisodeNumber != 2 {
		t.Errorf("episode grain: %+v", item)
	}
}

func TestSeasonPackEligible(t *testing.T) {
	if seasonPackEligible(2) {
		t.Fatal("2 missing should not prefer pack")
	}
	if !seasonPackEligible(3) {
		t.Fatal("3 missing should prefer pack")
	}
}

func TestPreferSeasonPackSkipsAnime(t *testing.T) {
	if preferSeasonPack(5, "anime") {
		t.Fatal("anime should not prefer season packs (absolute episodes)")
	}
	if !preferSeasonPack(5, "standard") {
		t.Fatal("standard with 5 missing should prefer pack")
	}
	if preferSeasonPack(2, "standard") {
		t.Fatal("standard with 2 missing should not prefer pack")
	}
}

func TestAddToQueueCoercesSeasonZeroDummy(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	add, err := m.AddToQueue(ctx, &autov1.AddToQueueRequest{
		ItemType:     "tv",
		ItemId:       "ep_tos_0_12",
		TmdbId:       253,
		Title:        "Star Trek",
		Year:         1966,
		SeasonNumber: 0,
		EpisodeNumber: 12,
		SeriesId:     "tv_tos",
	})
	if err != nil {
		t.Fatal(err)
	}
	if add.QueueId != "w_tv_tv_tos" {
		t.Fatalf("queue id = %s want w_tv_tv_tos", add.QueueId)
	}

	var season, episode int
	var itemID string
	err = m.db.QueryRow(`SELECT item_id, season_number, episode_number FROM wanted_items WHERE item_id = 'tv_tos'`).Scan(&itemID, &season, &episode)
	if err != nil {
		t.Fatal(err)
	}
	if season != 0 || episode != 0 {
		t.Fatalf("wanted grain season=%d episode=%d want pack 0/0", season, episode)
	}

	var dummy int
	_ = m.db.QueryRow(`SELECT COUNT(*) FROM wanted_items WHERE item_id = 'ep_tos_0_12'`).Scan(&dummy)
	if dummy != 0 {
		t.Fatal("S00E12 dummy row should not be inserted")
	}

	// leftover dummies (library sync from older binaries) are pruned
	insertSeasonZeroDummy(t, m, "ep_tos_0_3", "tv_tos", 3)
	m.pruneSeasonZeroEpisodeDummies(ctx)
	_ = m.db.QueryRow(`SELECT COUNT(*) FROM wanted_items WHERE season_number = 0 AND episode_number >= 1`).Scan(&dummy)
	if dummy != 0 {
		t.Fatalf("pruned leftovers still present: %d", dummy)
	}
	_ = m.db.QueryRow(`SELECT COUNT(*) FROM wanted_items WHERE item_id = 'tv_tos'`).Scan(&dummy)
	if dummy != 1 {
		t.Fatal("pack row should survive prune")
	}
}

func TestUpsertWantedRefusesSeasonZeroDummy(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	m.upsertWanted(ctx, wantedEntry{
		ItemType: "tv", ItemID: "ep_s00e12", TmdbID: 1, Title: "Show",
		SeasonNumber: 0, EpisodeNumber: 12, SeriesID: "tv_show",
	})
	var n int
	_ = m.db.QueryRow(`SELECT COUNT(*) FROM wanted_items`).Scan(&n)
	if n != 0 {
		t.Fatalf("upserted %d rows, want 0", n)
	}
	m.upsertWanted(ctx, wantedEntry{
		ItemType: "tv", ItemID: "tv_show", TmdbID: 1, Title: "Show",
		SeasonNumber: 0, EpisodeNumber: 0, SeriesID: "tv_show",
	})
	_ = m.db.QueryRow(`SELECT COUNT(*) FROM wanted_items`).Scan(&n)
	if n != 1 {
		t.Fatalf("pack upsert count=%d want 1", n)
	}
}

func TestAddToQueueAnimeAbsolute(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	_, err := m.AddToQueue(ctx, &autov1.AddToQueueRequest{
		ItemType:         "tv",
		ItemId:           "ep_abs_150",
		TmdbId:           65495,
		Title:            "One Piece",
		Year:             1999,
		SeasonNumber:     1,
		EpisodeNumber:    12,
		AbsoluteNumber:   150,
		SeriesType:       "anime",
		SeriesId:         "tv_65495",
		QualityProfileId: "qp",
	})
	if err != nil {
		t.Fatal(err)
	}

	var abs int
	var seriesType, seriesID string
	err = m.db.QueryRow(
		`SELECT absolute_number, series_type, series_id FROM wanted_items WHERE item_id = ?`,
		"ep_abs_150",
	).Scan(&abs, &seriesType, &seriesID)
	if err != nil {
		t.Fatal(err)
	}
	if abs != 150 || seriesType != "anime" || seriesID != "tv_65495" {
		t.Fatalf("got abs=%d type=%s series=%s", abs, seriesType, seriesID)
	}
}

func TestDecideGrab(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	base := profileDecision{MinScore: 50, CutoffScore: 200, UpgradeAllowed: true, UpgradeDelayMinutes: 60}

	if !decideGrab(base, true, 0, 80, time.Time{}, now) {
		t.Fatal("missing above min should grab")
	}
	if decideGrab(base, true, 0, 40, time.Time{}, now) {
		t.Fatal("missing below min should not grab")
	}
	if decideGrab(base, false, 100, 150, now.Add(-30*time.Minute), now) {
		t.Fatal("upgrade within delay should not grab")
	}
	if !decideGrab(base, false, 100, 150, now.Add(-2*time.Hour), now) {
		t.Fatal("upgrade after delay and better score should grab")
	}
	if decideGrab(base, false, 100, 100, now.Add(-2*time.Hour), now) {
		t.Fatal("equal score should not upgrade")
	}
	if decideGrab(base, false, 200, 300, now.Add(-2*time.Hour), now) {
		t.Fatal("at cutoff should not upgrade")
	}
	noUp := base
	noUp.UpgradeAllowed = false
	if decideGrab(noUp, false, 50, 150, now.Add(-2*time.Hour), now) {
		t.Fatal("upgrade_allowed=false should not grab")
	}
	noCutoff := profileDecision{MinScore: 0, CutoffScore: 0, UpgradeAllowed: true, UpgradeDelayMinutes: 0}
	if !decideGrab(noCutoff, false, 50, 51, now, now) {
		t.Fatal("no cutoff/delay should upgrade when better")
	}
}

func TestShouldKeepForUpgrade(t *testing.T) {
	p := profileDecision{UpgradeAllowed: true, CutoffScore: 200}
	if !shouldKeepForUpgrade(p, 100) {
		t.Fatal("below cutoff should keep")
	}
	if shouldKeepForUpgrade(p, 200) {
		t.Fatal("at cutoff should not keep")
	}
	p.UpgradeAllowed = false
	if shouldKeepForUpgrade(p, 50) {
		t.Fatal("upgrade disallowed should not keep")
	}
	p.UpgradeAllowed = true
	p.CutoffScore = 0
	if !shouldKeepForUpgrade(p, 999) {
		t.Fatal("cutoff 0 means no ceiling")
	}
}

func TestOnFileAddedKeepsUpgradeCandidate(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	m.upsertWanted(ctx, wantedEntry{
		ItemType: "movie", ItemID: "mv_up", TmdbID: 1, Title: "Upgradable",
		QualityProfileID: "",
	})
	m.mu.Lock()
	m.db.Exec(`INSERT INTO download_history (id, wanted_item_id, guid, title, indexer, size, score, download_url, download_protocol, status, sent_at, created_at, download_id)
		VALUES ('dl1', 'mv_up', 'g1', 'Upgradable.1080p', '', 0, 100, '', '', 'completed', datetime('now'), datetime('now'), 'd1')`)
	m.mu.Unlock()

	m.onFileAdded(ctx, "movie", "mv_up", "/data/Upgradable.1080p.mkv", "1080p")

	var missing, score int
	var acquired string
	m.mu.RLock()
	err := m.db.QueryRow(`SELECT missing, current_score, file_acquired_at FROM wanted_items WHERE item_id = 'mv_up'`).Scan(&missing, &score, &acquired)
	m.mu.RUnlock()
	if err != nil {
		t.Fatalf("wanted row gone: %v", err)
	}
	if missing != 0 {
		t.Errorf("expected missing=0, got %d", missing)
	}
	if score != 100 {
		t.Errorf("expected current_score=100 from history, got %d", score)
	}
	if acquired == "" {
		t.Error("expected file_acquired_at set")
	}
}

func TestHasInFlightDownload(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	m.upsertWanted(ctx, wantedEntry{ItemType: "movie", ItemID: "mv_fly", TmdbID: 3, Title: "Fly"})
	if m.hasInFlightDownload(ctx, "mv_fly") {
		t.Fatal("expected no in-flight")
	}
	m.mu.Lock()
	m.db.Exec(`INSERT INTO download_history (id, wanted_item_id, guid, title, score, status, created_at)
		VALUES ('dl3', 'mv_fly', 'g3', 'x', 10, 'sent', datetime('now'))`)
	m.mu.Unlock()
	if !m.hasInFlightDownload(ctx, "mv_fly") {
		t.Fatal("expected in-flight")
	}
}

func TestScoreTVReleaseBoost(t *testing.T) {
	boost := scoreTVReleaseBoost("Show.S01.Complete.1080p", 1, 0, 0, true, "standard")
	if boost < 40 {
		t.Fatalf("pack boost too low: %d", boost)
	}
	wrong := scoreTVReleaseBoost("Show.S02.Complete.1080p", 1, 0, 0, true, "standard")
	if wrong >= 0 {
		t.Fatalf("wrong season pack should be rejected, boost=%d", wrong)
	}
	wrongEp := scoreTVReleaseBoost("When Calls the Heart S13E02 Up in Smoke 1080p WEBRip", 10, 0, 0, true, "standard")
	if wrongEp >= 0 {
		t.Fatalf("single episode must not satisfy a season pack search, boost=%d", wrongEp)
	}
	epVsPack := scoreTVReleaseBoost("Marvel's Agents of S H I E L D 2013 S05 1080p BDrip x265", 0, 1, 0, false, "standard")
	if epVsPack >= 0 {
		t.Fatalf("episode wanted row must not take a season pack, boost=%d", epVsPack)
	}
	wrongEpNum := scoreTVReleaseBoost("Star Trek S03E24 Turnabout Intruder 1080p", 3, 12, 0, false, "standard")
	if wrongEpNum >= 0 {
		t.Fatalf("S03E12 wanted must not take S03E24, boost=%d", wrongEpNum)
	}
	matchEp := scoreTVReleaseBoost("Star Trek S03E12 1080p WEB", 3, 12, 0, false, "standard")
	if matchEp < 0 {
		t.Fatalf("matching S03E12 must not be rejected, boost=%d", matchEp)
	}
	noEp := scoreTVReleaseBoost("Star Trek 1080p WEB", 3, 12, 0, false, "standard")
	if noEp >= 0 {
		t.Fatalf("episode wanted must reject titles with no SxxExx, boost=%d", noEp)
	}
	fullSeries := scoreTVReleaseBoost("Breaking Bad (2008) S01-S05 1080p BluRay REMUX Dual Audio [Hindi+Eng] ~ RemuxDoc", 1, 1, 0, false, "standard")
	if fullSeries >= 0 {
		t.Fatalf("episode wanted must not take S01-S05 remux, boost=%d", fullSeries)
	}
	animeMiss := scoreTVReleaseBoost("Anime - 012 [1080p]", 0, 0, 150, false, "anime")
	if animeMiss >= 0 {
		t.Fatalf("anime must require absolute 150 in title, boost=%d", animeMiss)
	}
	anime := scoreTVReleaseBoost("Anime - 150 [1080p]", 0, 0, 150, false, "anime")
	if anime < 30 {
		t.Fatalf("anime absolute boost too low: %d", anime)
	}
}

func TestSkipWantedSearch(t *testing.T) {
	now := time.Now()
	interval := 15 * time.Minute
	if skipWantedSearch("", true, true, interval, now) {
		t.Fatal("never-searched missing item should run")
	}
	recent := now.Add(-2 * time.Minute).UTC().Format(time.RFC3339)
	if !skipWantedSearch(recent, true, true, interval, now) {
		t.Fatal("recently searched missing item should wait")
	}
	stale := now.Add(-20 * time.Minute).UTC().Format(time.RFC3339)
	if skipWantedSearch(stale, true, true, interval, now) {
		t.Fatal("stale missing item should search")
	}
	if !skipWantedSearch(recent, false, true, interval, now) {
		t.Fatal("upgrade search should wait 6h")
	}
	if !skipWantedSearch("", false, false, interval, now) {
		t.Fatal("upgrades disabled should skip owned items")
	}
}
