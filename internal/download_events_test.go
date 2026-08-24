package internal

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	scannerv1 "github.com/Muxcore-Media/contracts-scanner/muxcore/scanner/v1"
	"github.com/Muxcore-Media/core/pkg/contracts"
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

func TestRetryImportFailedMarksCompleted(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)
	save := t.TempDir()
	m.mu.Lock()
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO download_history (id, wanted_item_id, guid, title, indexer, size, score, download_url, download_protocol, status, sent_at, created_at, download_id, save_path)
		VALUES (?, ?, ?, ?, '', 0, 0, '', 'torrent', 'import_failed', ?, ?, ?, ?)`,
		"dl_retry_1", "item-retry", "guid-retry", "Star Trek S03E24", now, now, "tor-retry", save)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeScannerClient{}
	m.testScannerClient = fake
	m.retryImportFailed(ctx)
	if len(fake.importPathCalls) != 1 || fake.importPathCalls[0] != save {
		t.Fatalf("ImportPath calls: %v want [%q]", fake.importPathCalls, save)
	}
	st, _ := historyStatus(t, m, "dl_retry_1")
	if st != "completed" {
		t.Fatalf("status=%q want completed", st)
	}
}

func insertWantedTV(t *testing.T, m *Module, itemID string, tmdb, season, episode int) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	m.mu.Lock()
	_, err := m.db.Exec(`INSERT INTO wanted_items (id, item_type, item_id, tmdb_id, title, year, season_number, episode_number, monitored, missing, series_id, created_at, updated_at)
		VALUES (?, 'tv', ?, ?, 'Star Trek', 1966, ?, ?, 1, 1, 'ser_st', ?, ?)`,
		"w_"+itemID, itemID, tmdb, season, episode, now, now)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
}

func TestFileImportCompletesImportFailedByPath(t *testing.T) {
	m := newTestModule(t)
	save := t.TempDir()
	now := time.Now().UTC().Format(time.RFC3339)
	insertWantedTV(t, m, "item-st", 253, 3, 24)
	m.mu.Lock()
	_, err := m.db.ExecContext(context.Background(), `
		INSERT INTO download_history (id, wanted_item_id, guid, title, indexer, size, score, download_url, download_protocol, status, sent_at, created_at, download_id, save_path)
		VALUES (?, ?, ?, ?, '', 0, 0, '', 'torrent', 'import_failed', ?, ?, ?, ?)`,
		"dl_imp_path", "item-st", "guid-st", "Star Trek S03E24", now, now, "tor-st", save)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	m.completeHistoryFromFileImported(context.Background(), contracts.FileImportedPayload{
		Title:           "Star Trek",
		OriginalPath:    filepath.Join(save, "Star.Trek.S03E24.mkv"),
		DestinationPath: "/library/Star Trek/Season 03/S03E24.mkv",
	})
	st, completedAt := historyStatus(t, m, "dl_imp_path")
	if st != "completed" {
		t.Fatalf("status=%q want completed", st)
	}
	if completedAt == "" {
		t.Fatal("expected completed_at")
	}
}

func TestFileImportCompletesImportFailedByEpisode(t *testing.T) {
	m := newTestModule(t)
	now := time.Now().UTC().Format(time.RFC3339)
	insertWantedTV(t, m, "item-st-ep", 253, 3, 24)
	m.mu.Lock()
	_, err := m.db.ExecContext(context.Background(), `
		INSERT INTO download_history (id, wanted_item_id, guid, title, indexer, size, score, download_url, download_protocol, status, sent_at, created_at, download_id, save_path)
		VALUES (?, ?, ?, ?, '', 0, 0, '', 'torrent', 'import_failed', ?, ?, ?, ?)`,
		"dl_imp_ep", "item-st-ep", "guid-st-ep", "Star Trek S03E24", now, now, "tor-st-ep", "/downloads/other-show")
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	m.completeHistoryFromFileImported(context.Background(), contracts.FileImportedPayload{
		Title:           "Star Trek",
		TMDBID:          253,
		SeasonNumber:    3,
		EpisodeNumber:   24,
		DestinationPath: "/library/Star Trek/Season 03/S03E24.mkv",
	})
	st, _ := historyStatus(t, m, "dl_imp_ep")
	if st != "completed" {
		t.Fatalf("status=%q want completed", st)
	}
}

func TestFileImportDoesNotCompletePackFromOneEpisode(t *testing.T) {
	m := newTestModule(t)
	now := time.Now().UTC().Format(time.RFC3339)
	insertWantedTV(t, m, "item-st-pack", 253, 3, 0)
	m.mu.Lock()
	_, err := m.db.ExecContext(context.Background(), `
		INSERT INTO download_history (id, wanted_item_id, guid, title, indexer, size, score, download_url, download_protocol, status, sent_at, created_at, download_id, save_path)
		VALUES (?, ?, ?, ?, '', 0, 0, '', 'torrent', 'import_failed', ?, ?, ?, ?)`,
		"dl_imp_pack", "item-st-pack", "guid-st-pack", "Star Trek S03", now, now, "tor-st-pack", "/downloads/pack")
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	m.completeHistoryFromFileImported(context.Background(), contracts.FileImportedPayload{
		Title:           "Star Trek",
		TMDBID:          253,
		SeasonNumber:    3,
		EpisodeNumber:   24,
		DestinationPath: "/library/Star Trek/Season 03/S03E24.mkv",
	})
	st, _ := historyStatus(t, m, "dl_imp_pack")
	if st != "import_failed" {
		t.Fatalf("status=%q want import_failed (pack must not complete from one episode)", st)
	}
}

func TestFileImportDoesNotCompleteWrongEpisode(t *testing.T) {
	m := newTestModule(t)
	now := time.Now().UTC().Format(time.RFC3339)
	insertWantedTV(t, m, "item-st-wrong", 253, 3, 24)
	m.mu.Lock()
	_, err := m.db.ExecContext(context.Background(), `
		INSERT INTO download_history (id, wanted_item_id, guid, title, indexer, size, score, download_url, download_protocol, status, sent_at, created_at, download_id, save_path)
		VALUES (?, ?, ?, ?, '', 0, 0, '', 'torrent', 'sent', ?, ?, ?, ?)`,
		"dl_imp_wrong", "item-st-wrong", "guid-st-wrong", "Star Trek S03E24", now, now, "tor-st-wrong", "/downloads/wrong")
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	m.completeHistoryFromFileImported(context.Background(), contracts.FileImportedPayload{
		TMDBID:        253,
		SeasonNumber:  3,
		EpisodeNumber: 12,
	})
	st, _ := historyStatus(t, m, "dl_imp_wrong")
	if st != "sent" {
		t.Fatalf("status=%q want sent", st)
	}
}

func TestFileImportMatchesHistory(t *testing.T) {
	t.Parallel()
	save := "/downloads/Star.Trek.S03E24"
	pPath := contracts.FileImportedPayload{OriginalPath: save + "/file.mkv"}
	if !fileImportMatchesHistory(pPath, save, 0, 0, 0) {
		t.Fatal("path under save_path should match")
	}
	pEp := contracts.FileImportedPayload{TMDBID: 253, SeasonNumber: 3, EpisodeNumber: 24}
	if !fileImportMatchesHistory(pEp, "/elsewhere", 253, 3, 24) {
		t.Fatal("episode-grain TMDB match")
	}
	if fileImportMatchesHistory(pEp, "/elsewhere", 253, 3, 0) {
		t.Fatal("pack episode 0 must not match TMDB+episode")
	}
	if fileImportMatchesHistory(pEp, "/elsewhere", 253, 3, 12) {
		t.Fatal("wrong episode must not match")
	}
}

func TestResolveExistingImportPathPrefersCwd(t *testing.T) {
	dir := t.TempDir()
	rel := filepath.Join("partials", "item1")
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(full, 0755); err != nil {
		t.Fatal(err)
	}
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	got := resolveExistingImportPath(rel)
	if got != full {
		t.Fatalf("got %q want %q", got, full)
	}
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

func TestJoinSaveAndRelPathNoDoubleJoin(t *testing.T) {
	t.Parallel()
	got := joinSaveAndRelPath("/data/downloads/partials/item1", "partials/item1/Show.mkv")
	if got != "/data/downloads/partials/item1/Show.mkv" {
		t.Fatalf("prefix file: %q", got)
	}
	got = joinSaveAndRelPath("/downloads/Show", "Show/S01E01.mkv")
	if got != "/downloads/Show/S01E01.mkv" {
		t.Fatalf("dir name prefix: %q", got)
	}
	targets := importTargets("partials/item1", []contracts.DownloadEventFile{{Path: "partials/item1/file.mkv"}})
	if len(targets) != 1 || targets[0] != "partials/item1/file.mkv" {
		t.Fatalf("relative save+prefix: %v", targets)
	}
}

func TestJoinSaveAndRelPathStorageURI(t *testing.T) {
	t.Parallel()
	uri := "storage://torrent/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/files/Show.S01E01.mkv"
	got := joinSaveAndRelPath("storage://torrent/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", uri)
	if got != uri {
		t.Fatalf("file uri: %q", got)
	}
	got = joinSaveAndRelPath("storage://torrent/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "")
	if got != "storage://torrent/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("save only: %q", got)
	}
	got = joinSaveAndRelPath("storage://torrent/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "files/a.mkv")
	want := "storage://torrent/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/files/a.mkv"
	if got != want {
		t.Fatalf("rel join: %q want %q", got, want)
	}
	targets := importTargets("storage://torrent/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", []contracts.DownloadEventFile{
		{Path: uri},
	})
	if len(targets) != 1 || targets[0] != uri {
		t.Fatalf("importTargets: %v", targets)
	}
}

func TestDownloadStartedPersistsAbsoluteImportPaths(t *testing.T) {
	m := newTestModule(t)
	insertHistoryWithDownloadID(t, m, "dl_paths", "tor-paths")
	m.handleDownloadLifecycleEvent(context.Background(), contracts.EventDownloadStarted, contracts.DownloadEventPayload{
		ID:       "tor-paths",
		InfoHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SavePath: "/data/downloads/partials/item1",
		Files:    []contracts.DownloadEventFile{{Path: "partials/item1/Show.mkv", Size: 10}},
	})
	var stored string
	m.mu.RLock()
	err := m.db.QueryRowContext(context.Background(),
		`SELECT COALESCE(import_paths,'') FROM download_history WHERE id = ?`, "dl_paths",
	).Scan(&stored)
	m.mu.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	if stored != "/data/downloads/partials/item1/Show.mkv" {
		t.Fatalf("import_paths=%q", stored)
	}
}
