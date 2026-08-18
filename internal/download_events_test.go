package internal

import (
	"context"
	"testing"
	"time"

	"github.com/Muxcore-Media/core/pkg/contracts"
	scannerv1 "github.com/Muxcore-Media/media-scanner/proto/scannerv1"
	"google.golang.org/grpc"
)

type fakeScannerClient struct {
	scannerv1.ScannerServiceClient
	importPathCalls []string
	importPathErr   error
}

func (f *fakeScannerClient) ImportPath(ctx context.Context, in *scannerv1.ImportPathRequest, opts ...grpc.CallOption) (*scannerv1.ImportPathResponse, error) {
	f.importPathCalls = append(f.importPathCalls, in.GetPath())
	if f.importPathErr != nil {
		return nil, f.importPathErr
	}
	return &scannerv1.ImportPathResponse{FilesFound: 1, FilesImported: 1}, nil
}

func insertHistoryWithDownloadID(t *testing.T, m *Module, histID, downloadID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)
	m.mu.Lock()
	_, err := m.db.ExecContext(ctx,
		`INSERT INTO download_history (id, wanted_item_id, guid, title, indexer, size, score, download_url, download_protocol, status, sent_at, created_at, download_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'sent', ?, ?, ?)`,
		histID, "item-1", "guid-1", "Test Title", "indexer",
		int64(100), int64(90), "magnet:?xt=urn:btih:test", "torrent",
		now, now, downloadID,
	)
	m.mu.Unlock()
	if err != nil {
		t.Fatalf("insert history: %v", err)
	}
}

func historyStatus(t *testing.T, m *Module, histID string) (status, completedAt string) {
	t.Helper()
	m.mu.RLock()
	err := m.db.QueryRowContext(context.Background(),
		`SELECT status, COALESCE(completed_at, '') FROM download_history WHERE id = ?`, histID,
	).Scan(&status, &completedAt)
	m.mu.RUnlock()
	if err != nil {
		t.Fatalf("query status: %v", err)
	}
	return status, completedAt
}

func TestDownloadCompletedImportsPath(t *testing.T) {
	m := newTestModule(t)
	const histID = "dl_completed_1"
	const downloadID = "torrent-done-1"
	const savePath = "/downloads/Fight.Club.1999"

	insertHistoryWithDownloadID(t, m, histID, downloadID)
	fake := &fakeScannerClient{}
	m.testScannerClient = fake

	m.handleDownloadLifecycleEvent(context.Background(), contracts.EventDownloadCompleted, contracts.DownloadEventPayload{
		ID:       downloadID,
		SavePath: savePath,
		Name:     "Fight Club",
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(fake.importPathCalls) == 1 && fake.importPathCalls[0] == savePath {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(fake.importPathCalls) != 1 || fake.importPathCalls[0] != savePath {
		t.Fatalf("ImportPath calls: got %v, want [%q]", fake.importPathCalls, savePath)
	}
	var status, completedAt string
	for time.Now().Before(deadline) {
		status, completedAt = historyStatus(t, m, histID)
		if status == "completed" && completedAt != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status != "completed" {
		t.Errorf("status: got %q, want completed", status)
	}
	if completedAt == "" {
		t.Error("expected completed_at to be set")
	}
}

func TestDownloadFailedMarksHistory(t *testing.T) {
	m := newTestModule(t)
	const histID = "dl_failed_1"
	const downloadID = "torrent-fail-1"

	insertHistoryWithDownloadID(t, m, histID, downloadID)
	fake := &fakeScannerClient{}
	m.testScannerClient = fake

	m.handleDownloadLifecycleEvent(context.Background(), contracts.EventDownloadFailed, contracts.DownloadEventPayload{
		ID:    downloadID,
		Error: "disk full",
	})

	if len(fake.importPathCalls) != 0 {
		t.Fatalf("ImportPath should not be called on failed, got %v", fake.importPathCalls)
	}
	status, completedAt := historyStatus(t, m, histID)
	if status != "failed" {
		t.Errorf("status: got %q, want failed", status)
	}
	if completedAt == "" {
		t.Error("expected completed_at to be set")
	}
}

func TestDownloadCompletedUnknownID(t *testing.T) {
	m := newTestModule(t)
	fake := &fakeScannerClient{}
	m.testScannerClient = fake

	// Must not panic; must not call ImportPath.
	m.handleDownloadLifecycleEvent(context.Background(), contracts.EventDownloadCompleted, contracts.DownloadEventPayload{
		ID:       "unknown-torrent",
		SavePath: "/downloads/nowhere",
	})

	if len(fake.importPathCalls) != 0 {
		t.Fatalf("ImportPath should not be called for unknown id, got %v", fake.importPathCalls)
	}
}

func TestImportTargetsPrefersCompletedFiles(t *testing.T) {
	t.Parallel()
	got := importTargets("/downloads", []contracts.DownloadEventFile{
		{Path: "Show/S01E01.mkv"},
		{Path: "/downloads/Show/S01E02.mkv"},
	})
	want := []string{
		"/downloads/Show/S01E01.mkv",
		"/downloads/Show/S01E02.mkv",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d]=%q want %q", i, got[i], want[i])
		}
	}

	fallback := importTargets("/downloads/Pack", nil)
	if len(fallback) != 1 || fallback[0] != "/downloads/Pack" {
		t.Fatalf("fallback got %v", fallback)
	}
}
