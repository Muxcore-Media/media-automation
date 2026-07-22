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

func TestSearchAndStoreDoesNotClearMissing(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	m.upsertWanted(ctx, wantedEntry{ItemType: "movie", ItemID: "mv1", TmdbID: 1, Title: "A"})
	var id string
	m.mu.RLock()
	m.db.QueryRow(`SELECT id FROM wanted_items WHERE item_id = 'mv1'`).Scan(&id)
	m.mu.RUnlock()

	m.searchAndStore(ctx, id, "movie", "A", 1, 2000, 0, 0, 0, "", "")

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

func TestScoreTVReleaseBoost(t *testing.T) {
	boost := scoreTVReleaseBoost("Show.S01.Complete.1080p", 1, 0, 0, true, "standard")
	if boost < 40 {
		t.Fatalf("pack boost too low: %d", boost)
	}
	wrong := scoreTVReleaseBoost("Show.S02.Complete.1080p", 1, 0, 0, true, "standard")
	if wrong >= 0 {
		t.Fatalf("wrong season pack should be rejected, boost=%d", wrong)
	}
	anime := scoreTVReleaseBoost("Anime - 150 [1080p]", 0, 0, 150, false, "anime")
	if anime < 30 {
		t.Fatalf("anime absolute boost too low: %d", anime)
	}
}
