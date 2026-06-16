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
