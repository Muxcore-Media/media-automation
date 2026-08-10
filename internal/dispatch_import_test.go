package internal

import (
	"context"
	"testing"
	"time"

	cdlv1 "github.com/Muxcore-Media/contracts-downloader/muxcore/downloader/v1"
	"github.com/Muxcore-Media/core/pkg/contracts"
	autov1 "github.com/Muxcore-Media/media-automation/proto/automationv1"
	"google.golang.org/grpc"
)

// fakeDownloaderClient records AddTorrent and returns a fixed torrent id (offline).
type fakeDownloaderClient struct {
	cdlv1.DownloaderServiceClient
	torrentID string
	calls     int
	lastURL   string
}

func (f *fakeDownloaderClient) AddTorrent(ctx context.Context, in *cdlv1.AddTorrentRequest, opts ...grpc.CallOption) (*cdlv1.AddTorrentResponse, error) {
	f.calls++
	f.lastURL = in.GetTorrentUrl()
	id := f.torrentID
	if id == "" {
		id = "torrent-fixture-1"
	}
	return &cdlv1.AddTorrentResponse{TorrentId: id, Name: in.GetCategory()}, nil
}

// TestDispatchDownloadCompletedImportPath is the offline regression:
// Dispatch → history download_id → download.completed → scanner ImportPath.
func TestDispatchDownloadCompletedImportPath(t *testing.T) {
	m := newTestModule(t)
	const (
		downloadID = "dl-fixture-fight-club"
		savePath   = "/downloads/Fight.Club.1999.1080p.Fixture"
		magnet     = "magnet:?xt=urn:btih:fixturefightclub1999"
	)

	fakeDL := &fakeDownloaderClient{torrentID: downloadID}
	m.downloaderClient = fakeDL
	fakeScan := &fakeScannerClient{}
	m.testScannerClient = fakeScan

	ctx := context.Background()
	disp, err := m.Dispatch(ctx, &autov1.DispatchRequest{
		Guid:             "guid-fight-club-fixture",
		Title:            "Fight Club",
		DownloadUrl:      magnet,
		DownloadProtocol: "torrent",
		ItemType:         "movie",
		ItemId:           "mv_550",
		IndexerName:      "fixture-indexer",
		Score:            130,
		Size:             8192,
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if disp.GetDownloadId() != downloadID {
		t.Fatalf("download_id: got %q want %q", disp.GetDownloadId(), downloadID)
	}
	if disp.GetStatus() != "sent" {
		t.Fatalf("status: got %q want sent", disp.GetStatus())
	}
	if fakeDL.calls != 1 || fakeDL.lastURL != magnet {
		t.Fatalf("AddTorrent calls=%d url=%q", fakeDL.calls, fakeDL.lastURL)
	}

	hist, err := m.GetHistory(ctx, &autov1.GetHistoryRequest{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if hist.GetTotal() != 1 {
		t.Fatalf("history total: got %d want 1", hist.GetTotal())
	}
	rec := hist.GetRecords()[0]
	if rec.GetDownloadId() != downloadID {
		t.Fatalf("history download_id: got %q want %q", rec.GetDownloadId(), downloadID)
	}
	if rec.GetStatus() != "sent" {
		t.Fatalf("history status before complete: got %q want sent", rec.GetStatus())
	}

	m.handleDownloadLifecycleEvent(ctx, contracts.EventDownloadCompleted, contracts.DownloadEventPayload{
		ID:       downloadID,
		SavePath: savePath,
		Name:     "Fight Club",
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(fakeScan.importPathCalls) == 1 && fakeScan.importPathCalls[0] == savePath {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(fakeScan.importPathCalls) != 1 || fakeScan.importPathCalls[0] != savePath {
		t.Fatalf("ImportPath calls: got %v, want [%q]", fakeScan.importPathCalls, savePath)
	}

	var status, completedAt string
	for time.Now().Before(deadline) {
		status, completedAt = historyStatus(t, m, rec.GetId())
		if status == "completed" && completedAt != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status != "completed" {
		t.Errorf("status after import: got %q want completed", status)
	}
	if completedAt == "" {
		t.Error("expected completed_at set")
	}
}
