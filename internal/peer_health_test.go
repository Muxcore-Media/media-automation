package internal

import (
	"context"
	"errors"
	"testing"
	"time"

	autov1 "github.com/Muxcore-Media/contracts-automation/muxcore/automation/v1"
	cdlv1 "github.com/Muxcore-Media/contracts-downloader/muxcore/downloader/v1"
	indexerv1 "github.com/Muxcore-Media/contracts-indexer/muxcore/indexer/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func TestPeerHealthBenchesAfterRepeatedFailures(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
	h := &peerHealth{now: clk.now}
	unavailable := status.Error(codes.Unavailable, "connection refused")

	for i := 0; i < peerMinObservations-1; i++ {
		h.record("idx-a", 10*time.Millisecond, unavailable)
	}
	if !h.usable("idx-a") {
		t.Fatal("benched before reaching the minimum observation count")
	}
	h.record("idx-a", 10*time.Millisecond, unavailable)
	if h.usable("idx-a") {
		t.Fatal("expected idx-a benched after repeated failures")
	}

	clk.t = clk.t.Add(peerBenchCooldown)
	if !h.usable("idx-a") {
		t.Fatal("expected a trial call once the cooldown ends")
	}
	h.record("idx-a", 10*time.Millisecond, nil)
	if !h.usable("idx-a") {
		t.Fatal("successful trial should un-bench")
	}
	if got := h.snapshot()[0]; got.Observations != 1 || got.Score != 100 {
		t.Fatalf("window should restart after the trial, got %+v", got)
	}
}

func TestPeerHealthFailedTrialRebenches(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	h := &peerHealth{now: clk.now}
	h.bench("dl-a", errors.New("refused"))
	clk.t = clk.t.Add(peerBenchCooldown)
	h.record("dl-a", time.Millisecond, status.Error(codes.Unavailable, "still down"))
	if h.usable("dl-a") {
		t.Fatal("failed trial should bench again")
	}
}

func TestPeerHealthIgnoresNonHealthErrors(t *testing.T) {
	h := &peerHealth{}
	for i := 0; i < 10; i++ {
		h.record("idx-a", 0, status.Error(codes.NotFound, "no results"))
		h.record("idx-a", 0, status.Error(codes.ResourceExhausted, "rate limited"))
		h.record("idx-a", 0, context.Canceled)
	}
	if len(h.snapshot()) != 0 {
		t.Fatalf("non-health errors should not be recorded: %+v", h.snapshot())
	}
}

func TestScoreWindow(t *testing.T) {
	ok := peerObservation{outcome: peerSuccess, latency: 100 * time.Millisecond}
	slow := peerObservation{outcome: peerSuccess, latency: 2 * peerLatencyTarget}
	fail := peerObservation{outcome: peerFailure}
	timeout := peerObservation{outcome: peerTimeout}

	cases := []struct {
		name   string
		window []peerObservation
		want   float64
	}{
		{"empty", nil, 100},
		{"healthy", []peerObservation{ok, ok, ok, ok}, 100},
		{"slow", []peerObservation{slow, slow}, 100 - peerLatencyPenalty},
		{"half failures", []peerObservation{ok, ok, fail, fail}, 100 - peerFailurePenalty/2},
		{"all timeouts", []peerObservation{timeout, timeout}, 0},
	}
	for _, tc := range cases {
		if got := scoreWindow(tc.window); got != tc.want {
			t.Errorf("%s: score=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestParallelIndexerSearchSkipsBenchedIndexer(t *testing.T) {
	h := &peerHealth{}
	h.bench("down", errors.New("refused"))
	up := &fakeIndexerClient{results: []*indexerv1.SearchResult{{Guid: "1", Title: "Fight.Club.1999.1080p"}}}
	down := &countingIndexerClient{}
	clients := map[string]indexerv1.IndexerServiceClient{"up": up, "down": down}

	got, _ := parallelIndexerSearch(context.Background(), clients, &indexerv1.SearchRequest{Query: "Fight Club"}, h)
	if len(got) != 1 {
		t.Fatalf("results=%d want 1", len(got))
	}
	if down.calls != 0 {
		t.Fatalf("benched indexer was searched %d times", down.calls)
	}
}

func TestParallelIndexerSearchUsesBenchedWhenNothingElse(t *testing.T) {
	h := &peerHealth{}
	h.bench("only", errors.New("refused"))
	only := &countingIndexerClient{}
	_, _ = parallelIndexerSearch(context.Background(), map[string]indexerv1.IndexerServiceClient{"only": only}, &indexerv1.SearchRequest{Query: "x"}, h)
	if only.calls != 1 {
		t.Fatalf("sole indexer should still be searched, calls=%d", only.calls)
	}
}

func TestParallelIndexerSearchRecordsOutcomes(t *testing.T) {
	h := &peerHealth{}
	clients := map[string]indexerv1.IndexerServiceClient{
		"bad": &fakeIndexerClient{err: status.Error(codes.Unavailable, "down")},
	}
	for i := 0; i < peerMinObservations; i++ {
		_, _ = parallelIndexerSearch(context.Background(), clients, &indexerv1.SearchRequest{Query: "x"}, h)
	}
	if h.usable("bad") {
		t.Fatal("indexer failing every search should be benched")
	}
}

type countingIndexerClient struct {
	indexerv1.IndexerServiceClient
	calls int
}

func (c *countingIndexerClient) Search(ctx context.Context, in *indexerv1.SearchRequest, opts ...grpc.CallOption) (*indexerv1.SearchResponse, error) {
	c.calls++
	return &indexerv1.SearchResponse{}, nil
}

// unreachableDownloader fails every AddTorrent as if the module were down.
type unreachableDownloader struct {
	cdlv1.DownloaderServiceClient
	calls int
}

func (u *unreachableDownloader) AddTorrent(ctx context.Context, in *cdlv1.AddTorrentRequest, opts ...grpc.CallOption) (*cdlv1.AddTorrentResponse, error) {
	u.calls++
	return nil, status.Error(codes.Unavailable, "connection refused")
}

func (u *unreachableDownloader) GetTorrent(ctx context.Context, in *cdlv1.GetTorrentRequest, opts ...grpc.CallOption) (*cdlv1.GetTorrentResponse, error) {
	return nil, status.Error(codes.Unavailable, "connection refused")
}

// withFakeDownloaders wires m to a fixed set of torrent downloader modules.
func withFakeDownloaders(m *Module, order []string, clients map[string]cdlv1.DownloaderServiceClient) {
	m.downloaderPool.discover = func(ctx context.Context, capability string) ([]downloaderCandidate, error) {
		if capability != "downloader.torrent" {
			return nil, nil
		}
		out := make([]downloaderCandidate, 0, len(order))
		for _, id := range order {
			out = append(out, downloaderCandidate{id: id, addr: id + ":9000"})
		}
		return out, nil
	}
	m.downloaderPool.dial = func(id, addr string) (*downloaderConn, error) {
		return &downloaderConn{id: id, addr: addr, torrent: clients[id]}, nil
	}
}

func TestDispatchFailsOverToNextDownloader(t *testing.T) {
	m := newTestModule(t)
	t.Setenv("AUTOMATION_DOWNLOADER_TORRENT", "")
	primary := &unreachableDownloader{}
	backup := &fakeDownloaderClient{torrentID: "backup-1"}
	withFakeDownloaders(m, []string{"dl-primary", "dl-backup"}, map[string]cdlv1.DownloaderServiceClient{
		"dl-primary": primary,
		"dl-backup":  backup,
	})

	ctx := context.Background()
	resp, err := m.Dispatch(ctx, &autov1.DispatchRequest{
		Guid:             "guid-failover",
		Title:            "Fight.Club.1999.1080p",
		DownloadUrl:      "magnet:?xt=urn:btih:failover1",
		DownloadProtocol: "torrent",
		ItemType:         "movie",
		ItemId:           "mv_failover",
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if resp.GetDownloadId() != "backup-1" || primary.calls != 1 || backup.calls != 1 {
		t.Fatalf("id=%q primary=%d backup=%d", resp.GetDownloadId(), primary.calls, backup.calls)
	}
	if m.downloaderHealth.usable("dl-primary") {
		t.Fatal("unreachable downloader should be benched")
	}

	var module string
	if err := m.db.QueryRowContext(ctx, `SELECT downloader_module FROM download_history WHERE download_id = ?`, "backup-1").Scan(&module); err != nil {
		t.Fatal(err)
	}
	if module != "dl-backup" {
		t.Fatalf("downloader_module=%q want dl-backup", module)
	}

	// The next grab goes straight to the backup while the primary is benched.
	if _, err := m.Dispatch(ctx, &autov1.DispatchRequest{
		Guid:             "guid-failover-2",
		Title:            "Se7en.1995.1080p",
		DownloadUrl:      "magnet:?xt=urn:btih:failover2",
		DownloadProtocol: "torrent",
		ItemType:         "movie",
		ItemId:           "mv_failover_2",
	}); err != nil {
		t.Fatalf("second Dispatch: %v", err)
	}
	if primary.calls != 1 || backup.calls != 2 {
		t.Fatalf("primary=%d backup=%d after second dispatch", primary.calls, backup.calls)
	}
}

func TestDispatchDoesNotFailOverOnTimeout(t *testing.T) {
	m := newTestModule(t)
	t.Setenv("AUTOMATION_DOWNLOADER_TORRENT", "")
	slow := &slowDownloader{}
	backup := &fakeDownloaderClient{torrentID: "backup-1"}
	withFakeDownloaders(m, []string{"dl-slow", "dl-backup"}, map[string]cdlv1.DownloaderServiceClient{
		"dl-slow":   slow,
		"dl-backup": backup,
	})
	_, err := m.Dispatch(context.Background(), &autov1.DispatchRequest{
		Guid:             "guid-timeout",
		Title:            "Heat.1995.1080p",
		DownloadUrl:      "magnet:?xt=urn:btih:timeout1",
		DownloadProtocol: "torrent",
		ItemType:         "movie",
		ItemId:           "mv_timeout",
	})
	if err == nil {
		t.Fatal("expected the timeout to surface")
	}
	if backup.calls != 0 {
		t.Fatal("a timed-out add may have landed; must not retry on another client")
	}
}

type slowDownloader struct {
	cdlv1.DownloaderServiceClient
}

func (s *slowDownloader) AddTorrent(ctx context.Context, in *cdlv1.AddTorrentRequest, opts ...grpc.CallOption) (*cdlv1.AddTorrentResponse, error) {
	return nil, status.Error(codes.DeadlineExceeded, "deadline exceeded")
}

func TestTorrentSnapshotUsesRecordedDownloader(t *testing.T) {
	m := newTestModule(t)
	t.Setenv("AUTOMATION_DOWNLOADER_TORRENT", "")
	first := &fakeDownloaderClient{torrents: map[string]*cdlv1.TorrentInfo{"t-1": {Name: "on-first"}}}
	second := &fakeDownloaderClient{torrents: map[string]*cdlv1.TorrentInfo{}}
	withFakeDownloaders(m, []string{"dl-second", "dl-first"}, map[string]cdlv1.DownloaderServiceClient{
		"dl-first":  first,
		"dl-second": second,
	})
	if err := m.ensureTorrentDownloader(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := m.activeDownloader("torrent"); got != "dl-second" {
		t.Fatalf("active=%q", got)
	}
	snap, ok, missing := m.torrentSnapshot(context.Background(), "dl-first", "t-1")
	if !ok || missing || snap.name != "on-first" {
		t.Fatalf("snapshot via recorded module: ok=%v missing=%v snap=%+v", ok, missing, snap)
	}
}

func TestOrderDownloaderCandidates(t *testing.T) {
	mods := []downloaderCandidate{{id: "downloader-sabnzbd"}, {id: "downloader-native-usenet"}}
	got := orderDownloaderCandidates("downloader.usenet", "", mods)
	if len(got) != 2 || got[0].id != "downloader-native-usenet" || got[1].id != "downloader-sabnzbd" {
		t.Fatalf("usenet order: %+v", got)
	}
	got = orderDownloaderCandidates("downloader.usenet", "downloader-sabnzbd", mods)
	if got[0].id != "downloader-sabnzbd" || len(got) != 2 {
		t.Fatalf("preferred first: %+v", got)
	}
	torrent := []downloaderCandidate{{id: "downloader-qbittorrent"}, {id: "downloader-native-torrent"}}
	got = orderDownloaderCandidates("downloader.torrent", "", torrent)
	if len(got) != 1 || got[0].id != "downloader-native-torrent" {
		t.Fatalf("torrent pass skips qbittorrent: %+v", got)
	}
}
