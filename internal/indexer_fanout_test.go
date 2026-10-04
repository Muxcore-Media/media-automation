package internal

import (
	"context"
	"fmt"
	"testing"

	autov1 "github.com/Muxcore-Media/contracts-automation/muxcore/automation/v1"
	indexerv1 "github.com/Muxcore-Media/contracts-indexer/muxcore/indexer/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeIndexerClient struct {
	indexerv1.IndexerServiceClient
	name    string
	results []*indexerv1.SearchResult
	err     error
}

func (f *fakeIndexerClient) Search(ctx context.Context, in *indexerv1.SearchRequest, opts ...grpc.CallOption) (*indexerv1.SearchResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &indexerv1.SearchResponse{Results: f.results, Total: int32(len(f.results))}, nil
}

func TestDialAddrForModule(t *testing.T) {
	t.Setenv("MUXCORE_MESH_DIAL_LOCAL", "")
	if got := dialAddrForModule("indexer-a", "127.0.0.1:9401"); got != "127.0.0.1:9401" {
		t.Errorf("explicit host preserved, got %q", got)
	}
	if got := dialAddrForModule("indexer-a", ":9401"); got != "indexer-a:9401" {
		t.Errorf("empty host → module DNS, got %q", got)
	}
	if got := dialAddrForModule("indexer-a", "0.0.0.0:9401"); got != "indexer-a:9401" {
		t.Errorf("wildcard → module DNS, got %q", got)
	}
	if got := dialAddrForModule("", "127.0.0.1:9401"); got != "127.0.0.1:9401" {
		t.Errorf("empty id should keep addr, got %q", got)
	}
	if got := dialAddrForModule("x", "not-a-hostport"); got != "not-a-hostport" {
		t.Errorf("got %q", got)
	}
	t.Setenv("MUXCORE_MESH_DIAL_LOCAL", "true")
	if got := dialAddrForModule("indexer-a", ":9401"); got != "127.0.0.1:9401" {
		t.Errorf("local dial mode, got %q", got)
	}
}

func TestDedupeScoredReleasesByGUID(t *testing.T) {
	in := []scoredRelease{
		{GUID: "g1", Title: "A.1080p", IndexerName: "low", Score: 10, DownloadURL: "magnet:a"},
		{GUID: "g1", Title: "A.1080p", IndexerName: "high", Score: 50, DownloadURL: "magnet:b"},
		{GUID: "g2", Title: "B.720p", IndexerName: "other", Score: 20, DownloadURL: "magnet:c"},
	}
	out := dedupeScoredReleases(in)
	if len(out) != 2 {
		t.Fatalf("expected 2 after dedupe, got %d", len(out))
	}
	if out[0].GUID != "g1" || out[0].IndexerName != "high" || out[0].Score != 50 {
		t.Errorf("expected highest g1 first, got %+v", out[0])
	}
	if out[1].GUID != "g2" {
		t.Errorf("expected g2 second, got %+v", out[1])
	}
}

func TestDedupeScoredReleasesByDownloadURL(t *testing.T) {
	in := []scoredRelease{
		{Title: "A", Score: 5, DownloadURL: "magnet:same"},
		{Title: "A-alt", Score: 40, DownloadURL: "magnet:same"},
	}
	out := dedupeScoredReleases(in)
	if len(out) != 1 {
		t.Fatalf("expected 1, got %d", len(out))
	}
	if out[0].Score != 40 {
		t.Errorf("expected score 40, got %d", out[0].Score)
	}
}

func TestParallelIndexerSearchMerge(t *testing.T) {
	clients := map[string]indexerv1.IndexerServiceClient{
		"a": &fakeIndexerClient{name: "a", results: []*indexerv1.SearchResult{
			{Guid: "1", Title: "Fight.Club.1999.1080p.BluRay", IndexerName: "TPB", DownloadUrl: "magnet:1", Seeders: 10},
		}},
		"b": &fakeIndexerClient{name: "b", results: []*indexerv1.SearchResult{
			{Guid: "2", Title: "Fight.Club.1999.2160p.Remux", IndexerName: "1337x", DownloadUrl: "magnet:2", Seeders: 5},
		}},
	}
	got, limited := parallelIndexerSearch(context.Background(), clients, &indexerv1.SearchRequest{Query: "Fight Club"}, nil)
	if limited {
		t.Fatal("unexpected rate limit")
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 results, got %d", len(got))
	}
	names := map[string]bool{}
	for _, r := range got {
		names[r.GetIndexerName()] = true
	}
	if !names["TPB"] || !names["1337x"] {
		t.Errorf("expected both indexers, got %v", names)
	}
}

func TestParallelIndexerSearchPartialFailure(t *testing.T) {
	clients := map[string]indexerv1.IndexerServiceClient{
		"ok": &fakeIndexerClient{results: []*indexerv1.SearchResult{
			{Guid: "1", Title: "Fight.Club.1999.1080p", IndexerName: "ok", DownloadUrl: "magnet:1"},
		}},
		"bad": &fakeIndexerClient{err: fmt.Errorf("boom")},
	}
	got, limited := parallelIndexerSearch(context.Background(), clients, &indexerv1.SearchRequest{Query: "Fight Club"}, nil)
	if limited {
		t.Fatal("generic error should not count as rate limit")
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 result from healthy indexer, got %d", len(got))
	}
	if got[0].GetIndexerName() != "ok" {
		t.Errorf("got %+v", got[0])
	}
}

func TestParallelIndexerSearchRateLimited(t *testing.T) {
	clients := map[string]indexerv1.IndexerServiceClient{
		"pb": &fakeIndexerClient{err: status.Error(codes.ResourceExhausted, "apibay 429")},
	}
	got, limited := parallelIndexerSearch(context.Background(), clients, &indexerv1.SearchRequest{Query: "Arthur"}, nil)
	if !limited {
		t.Fatal("expected rateLimited")
	}
	if len(got) != 0 {
		t.Fatalf("expected no results, got %d", len(got))
	}
}

func TestParallelIndexerSearchGUIDDedupeAcrossModules(t *testing.T) {
	clients := map[string]indexerv1.IndexerServiceClient{
		"a": &fakeIndexerClient{results: []*indexerv1.SearchResult{
			{Guid: "same", Title: "Fight.Club.1999.720p.WEB", IndexerName: "a", DownloadUrl: "magnet:a", Seeders: 1},
		}},
		"b": &fakeIndexerClient{results: []*indexerv1.SearchResult{
			{Guid: "same", Title: "Fight.Club.1999.1080p.BluRay", IndexerName: "b", DownloadUrl: "magnet:b", Seeders: 50},
		}},
	}
	raw, _ := parallelIndexerSearch(context.Background(), clients, &indexerv1.SearchRequest{Query: "Fight Club"}, nil)
	scored := scoreReleases(raw, "Fight Club", []string{"fight club"}, 1999, "movie")
	scored = dedupeScoredReleases(scored)
	if len(scored) != 1 {
		t.Fatalf("expected 1 after dedupe, got %d (%+v)", len(scored), scored)
	}
	// BluRay + more seeders should outrank WEB
	if scored[0].IndexerName != "b" {
		t.Errorf("expected indexer b (higher quality), got %+v", scored[0])
	}
}

func TestSearchItemFanOutTwoIndexers(t *testing.T) {
	m := newTestModule(t)
	m.testIndexerClients = map[string]indexerv1.IndexerServiceClient{
		"piratebay": &fakeIndexerClient{results: []*indexerv1.SearchResult{
			{Guid: "pb1", Title: "Fight.Club.1999.1080p.BluRay", IndexerName: "PirateBay", DownloadUrl: "magnet:pb", Seeders: 20, Size: 8_000_000_000},
		}},
		"leet": &fakeIndexerClient{results: []*indexerv1.SearchResult{
			{Guid: "lx1", Title: "Fight.Club.1999.2160p.Remux", IndexerName: "1337x", DownloadUrl: "magnet:lx", Seeders: 10, Size: 40_000_000_000},
		}},
	}

	resp, err := m.SearchItem(context.Background(), &autov1.SearchItemRequest{
		ItemType: "movie",
		Query:    "Fight Club",
		Year:     1999,
		Limit:    50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Matches) != 2 {
		t.Fatalf("expected 2 matches from both modules, got %d", len(resp.Matches))
	}
	seen := map[string]bool{}
	for _, match := range resp.Matches {
		seen[match.IndexerName] = true
	}
	if !seen["PirateBay"] || !seen["1337x"] {
		t.Errorf("expected PirateBay and 1337x, got %v", seen)
	}
}

func TestSearchItemFanOutRespectsLimit(t *testing.T) {
	m := newTestModule(t)
	var results []*indexerv1.SearchResult
	for i := 0; i < 5; i++ {
		results = append(results, &indexerv1.SearchResult{
			Guid:        fmt.Sprintf("g%d", i),
			Title:       fmt.Sprintf("Fight.Club.1999.1080p.BluRay.x%d", i),
			IndexerName: "bulk",
			DownloadUrl: fmt.Sprintf("magnet:%d", i),
			Seeders:     int32(10 + i),
			Size:        8_000_000_000,
		})
	}
	m.testIndexerClients = map[string]indexerv1.IndexerServiceClient{
		"a": &fakeIndexerClient{results: results},
		"b": &fakeIndexerClient{results: results}, // duplicates by guid → deduped to 5 then limited
	}

	resp, err := m.SearchItem(context.Background(), &autov1.SearchItemRequest{
		ItemType: "movie",
		Query:    "Fight Club",
		Limit:    3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Matches) != 3 {
		t.Fatalf("expected limit 3, got %d", len(resp.Matches))
	}
}
