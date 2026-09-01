package internal

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	autov1 "github.com/Muxcore-Media/contracts-automation/muxcore/automation/v1"
	cdlv1 "github.com/Muxcore-Media/contracts-downloader/muxcore/downloader/v1"
	formatsv1 "github.com/Muxcore-Media/media-custom-formats/proto/formatsv1"
	"google.golang.org/grpc"
)

type fakeFormatsClient struct {
	formatsv1.FormatServiceClient
	profiles []*formatsv1.QualityProfile
}

func (f *fakeFormatsClient) ListProfiles(ctx context.Context, in *formatsv1.ListProfilesRequest, opts ...grpc.CallOption) (*formatsv1.ListProfilesResponse, error) {
	_ = ctx
	_ = in
	_ = opts
	return &formatsv1.ListProfilesResponse{Profiles: f.profiles}, nil
}

func TestRemoveFromQueueCancelsInflight(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	fake := &fakeDownloaderClient{
		torrents: map[string]*cdlv1.TorrentInfo{"tor-active": {Id: "tor-active"}},
	}
	m.downloaderClient = fake

	add, err := m.AddToQueue(ctx, &autov1.AddToQueueRequest{
		ItemType: "movie", ItemId: "mv-cancel", Title: "Cancel Me", Year: 2020,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	m.mu.Lock()
	_, err = m.db.Exec(`
		INSERT INTO download_history (id, wanted_item_id, guid, title, status, created_at, download_id, download_protocol)
		VALUES ('h-cancel', 'mv-cancel', 'g1', 'Grab', 'sent', ?, 'tor-active', 'http')`, now)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := m.RemoveFromQueue(ctx, &autov1.RemoveFromQueueRequest{QueueId: add.QueueId}); err != nil {
		t.Fatal(err)
	}
	if len(fake.removed) != 1 || fake.removed[0] != "tor-active" {
		t.Fatalf("removed=%v want [tor-active]", fake.removed)
	}
	var st string
	m.mu.RLock()
	err = m.db.QueryRow(`SELECT status FROM download_history WHERE id = 'h-cancel'`).Scan(&st)
	m.mu.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	if st != "cancelled" {
		t.Fatalf("status=%q want cancelled", st)
	}
}

func TestSetMonitoredPreservesOnLibrarySync(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	add, err := m.AddToQueue(ctx, &autov1.AddToQueueRequest{
		ItemType: "book", ItemId: "bk-mon", Title: "Paused", Year: 2020,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetMonitored(ctx, &autov1.SetMonitoredRequest{QueueId: add.QueueId, Monitored: false}); err != nil {
		t.Fatal(err)
	}
	m.upsertWanted(ctx, wantedEntry{ItemType: "book", ItemID: "bk-mon", Title: "Paused", Year: 2020})
	var monitored int
	m.mu.RLock()
	err = m.db.QueryRow(`SELECT monitored FROM wanted_items WHERE id = ?`, add.QueueId).Scan(&monitored)
	m.mu.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	if monitored != 0 {
		t.Fatalf("monitored=%d want 0 after library sync", monitored)
	}
}

func TestListCutoffUnmet(t *testing.T) {
	m := newTestModule(t)
	m.formatsClient = &fakeFormatsClient{profiles: []*formatsv1.QualityProfile{{
		Id: "hd", CutoffScore: 100, UpgradeAllowed: true,
	}}}
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)
	m.mu.Lock()
	_, err := m.db.Exec(`
		INSERT INTO wanted_items (id, item_type, item_id, tmdb_id, title, monitored, missing, current_score, quality_profile_id, created_at, updated_at)
		VALUES ('w_movie_below', 'movie', 'mv-below', 1, 'Below', 1, 0, 50, 'hd', ?, ?),
		       ('w_movie_above', 'movie', 'mv-above', 2, 'Above', 1, 0, 120, 'hd', ?, ?)`,
		now, now, now, now)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	resp, err := m.ListCutoffUnmet(ctx, &autov1.ListCutoffUnmetRequest{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Total != 1 || len(resp.Items) != 1 {
		t.Fatalf("total=%d items=%d want 1/1", resp.Total, len(resp.Items))
	}
	if resp.Items[0].ItemId != "mv-below" {
		t.Fatalf("item=%q want mv-below", resp.Items[0].ItemId)
	}
}

func TestRetryImport(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "import.mkv")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	fake := &fakeScannerClient{}
	m.testScannerClient = fake
	now := time.Now().UTC().Format(time.RFC3339)
	m.mu.Lock()
	_, err := m.db.Exec(`
		INSERT INTO download_history (id, wanted_item_id, guid, title, status, created_at, save_path)
		VALUES ('h-retry', 'mv1', 'g1', 'Retry Me', 'import_failed', ?, ?)`, now, path)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	resp, err := m.RetryImport(ctx, &autov1.RetryImportRequest{HistoryId: "h-retry"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Attempted != 1 {
		t.Fatalf("attempted=%d want 1", resp.Attempted)
	}
	var st string
	m.mu.RLock()
	err = m.db.QueryRow(`SELECT status FROM download_history WHERE id = 'h-retry'`).Scan(&st)
	m.mu.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	if st != "completed" {
		t.Fatalf("status=%q want completed", st)
	}
}

func TestRemoveFromQueueAndBlocklist(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	add, err := m.AddToQueue(ctx, &autov1.AddToQueueRequest{
		ItemType: "movie", ItemId: "mv1", Title: "Test", Year: 2020,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.RemoveFromQueue(ctx, &autov1.RemoveFromQueueRequest{QueueId: add.QueueId}); err != nil {
		t.Fatal(err)
	}
	q, err := m.GetQueue(ctx, &autov1.GetQueueRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Items) != 0 {
		t.Fatalf("expected empty queue, got %d", len(q.Items))
	}

	m.blacklistRelease(ctx, "w_movie_mv1", "guid-1", 1, "stall")
	list, err := m.ListBlocklist(ctx, &autov1.ListBlocklistRequest{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 {
		t.Fatalf("expected 1 blocklist entry, got %d", list.Total)
	}
	if _, err := m.BlocklistRelease(ctx, &autov1.BlocklistReleaseRequest{
		WantedItemId: "w_movie_mv2", Guid: "guid-2", Reason: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	list, err = m.ListBlocklist(ctx, &autov1.ListBlocklistRequest{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 2 {
		t.Fatalf("expected 2 blocklist entries after BlocklistRelease, got %d", list.Total)
	}
	cleared, err := m.ClearBlocklist(ctx, &autov1.ClearBlocklistRequest{ClearAll: true})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Removed != 2 {
		t.Fatalf("expected removed=2, got %d", cleared.Removed)
	}
}

func TestDelayProfileCRUD(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	list, err := m.ListDelayProfiles(ctx, &autov1.ListDelayProfilesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Profiles) < 1 {
		t.Fatal("expected seeded delay profiles")
	}
	up, err := m.UpsertDelayProfile(ctx, &autov1.UpsertDelayProfileRequest{
		Protocol: "torrent", WaitMinutes: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if up.Profile.WaitMinutes != 30 {
		t.Fatalf("got %d", up.Profile.WaitMinutes)
	}
	if m.delayMinutesFor(ctx, "torrent") != 30 {
		t.Fatal("delay not applied")
	}
}
