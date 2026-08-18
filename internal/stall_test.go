package internal

import (
	"context"
	"testing"
	"time"

	cdlv1 "github.com/Muxcore-Media/contracts-downloader/muxcore/downloader/v1"
	"github.com/Muxcore-Media/core/pkg/contracts"
)

func TestParseStallLoopMinutes(t *testing.T) {
	got, err := parseStallLoopMinutes("60,360")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != 60 || got[1] != 360 {
		t.Fatalf("got %v", got)
	}
	if _, err := parseStallLoopMinutes("0,60"); err == nil {
		t.Fatal("expected error for 0")
	}
	if _, err := parseStallLoopMinutes("abc"); err == nil {
		t.Fatal("expected error for abc")
	}
	def, err := parseStallLoopMinutes("")
	if err != nil || len(def) != 2 {
		t.Fatalf("empty default: %v %v", def, err)
	}
}

func TestStallTimeoutForLoop(t *testing.T) {
	m := newTestModule(t)
	m.mu.Lock()
	m.stallAutoMode = true
	m.stallLoopMinutes = []int{60, 360}
	m.stallTimeoutMinutes = 180
	m.mu.Unlock()
	if d := m.stallTimeoutForLoop(1); d != time.Hour {
		t.Fatalf("loop1 auto: got %s want 1h", d)
	}
	if d := m.stallTimeoutForLoop(2); d != 6*time.Hour {
		t.Fatalf("loop2 auto: got %s want 6h", d)
	}
	if d := m.stallTimeoutForLoop(9); d != 6*time.Hour {
		t.Fatalf("loop9 should clamp to last: got %s", d)
	}
	m.mu.Lock()
	m.stallAutoMode = false
	m.mu.Unlock()
	if d := m.stallTimeoutForLoop(1); d != 180*time.Minute {
		t.Fatalf("manual: got %s want 180m", d)
	}
}

func TestPickNextReleaseSkipsBlacklist(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	m.upsertWanted(ctx, wantedEntry{ItemType: "movie", ItemID: "mv_stall", TmdbID: 1, Title: "Stall"})
	results := []scoredRelease{
		{GUID: "dead", Title: "Dead.1080p", DownloadURL: "magnet:?xt=urn:btih:dead", Score: 200},
		{GUID: "live", Title: "Live.1080p", DownloadURL: "magnet:?xt=urn:btih:live", Score: 100},
	}
	m.blacklistRelease(ctx, "mv_stall", "dead", 1, "metadata timeout")
	got := m.pickNextRelease(ctx, "mv_stall", 1, results)
	if got == nil || got.GUID != "live" {
		t.Fatalf("expected live, got %+v", got)
	}
	if m.pickNextRelease(ctx, "mv_stall", 1, results[:1]) != nil {
		t.Fatal("expected nil when only blacklisted remains")
	}
	if next := m.advanceAttemptLoop(ctx, "mv_stall", 1); next != 2 {
		t.Fatalf("advance: got %d want 2", next)
	}
	got = m.pickNextRelease(ctx, "mv_stall", 2, results)
	if got == nil || got.GUID != "dead" {
		t.Fatalf("loop 2 should retry dead first, got %+v", got)
	}
}

func TestDownloadFailedBlacklistsGUID(t *testing.T) {
	m := newTestModule(t)
	insertHistoryWithDownloadID(t, m, "dl_fail_bl", "torrent-fail-bl")
	m.handleDownloadLifecycleEvent(context.Background(), contracts.EventDownloadFailed, contracts.DownloadEventPayload{
		ID:    "torrent-fail-bl",
		Error: "wait metadata: context deadline exceeded",
	})
	if !m.isBlacklisted(context.Background(), "item-1", "guid-1", 1) {
		t.Fatal("expected GUID blacklisted after fail")
	}
	if m.hasInFlightDownload(context.Background(), "item-1") {
		t.Fatal("failed download should not count as in-flight")
	}
}

func TestReapStalledNoProgress(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	m.mu.Lock()
	m.stallAutoMode = false
	m.stallTimeoutMinutes = 60
	m.mu.Unlock()

	sent := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	m.mu.Lock()
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO download_history (id, wanted_item_id, guid, title, indexer, size, score, download_url, download_protocol, status, sent_at, created_at, download_id, attempt_loop, last_bytes, last_progress_at)
		VALUES ('dl_stall', 'mv_stall', 'guid-stall', 'Stuck', '', 0, 10, 'magnet:?xt=urn:btih:stall', 'torrent', 'sent', ?, ?, 'tor-stall', 1, 0, ?)`,
		sent, sent, sent)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	fake := &fakeDownloaderClient{
		torrents: map[string]*cdlv1.TorrentInfo{
			"tor-stall": {Id: "tor-stall", Status: "downloading", Downloaded: 0},
		},
	}
	m.downloaderClient = fake

	m.reapStalledDownloads(ctx, time.Now().UTC())

	status, _ := historyStatus(t, m, "dl_stall")
	if status != "stalled" {
		t.Fatalf("status=%q want stalled", status)
	}
	if !m.isBlacklisted(ctx, "mv_stall", "guid-stall", 1) {
		t.Fatal("expected blacklist after stall")
	}
	if len(fake.removed) != 1 || fake.removed[0] != "tor-stall" {
		t.Fatalf("RemoveTorrent: %v", fake.removed)
	}
	if m.hasInFlightDownload(ctx, "mv_stall") {
		t.Fatal("stalled should clear in-flight")
	}
}

func TestReapKeepsProgressingDownload(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	m.mu.Lock()
	m.stallAutoMode = false
	m.stallTimeoutMinutes = 60
	m.mu.Unlock()

	sent := time.Now().UTC().Add(-3 * time.Hour).Format(time.RFC3339)
	m.mu.Lock()
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO download_history (id, wanted_item_id, guid, title, status, sent_at, created_at, download_id, attempt_loop, last_bytes, last_progress_at)
		VALUES ('dl_prog', 'mv_prog', 'guid-prog', 'Moving', 'sent', ?, ?, 'tor-prog', 1, 100, ?)`,
		sent, sent, sent)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	m.downloaderClient = &fakeDownloaderClient{
		torrents: map[string]*cdlv1.TorrentInfo{
			"tor-prog": {Id: "tor-prog", Status: "downloading", Downloaded: 5000},
		},
	}

	m.reapStalledDownloads(ctx, time.Now().UTC())
	status, _ := historyStatus(t, m, "dl_prog")
	if status != "sent" {
		t.Fatalf("progressing download reaped: status=%q", status)
	}
	if m.isBlacklisted(ctx, "mv_prog", "guid-prog", 1) {
		t.Fatal("should not blacklist a progressing torrent")
	}
}

func TestReapImmediateErrorStatus(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)
	m.mu.Lock()
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO download_history (id, wanted_item_id, guid, title, status, sent_at, created_at, download_id, attempt_loop)
		VALUES ('dl_err', 'mv_err', 'guid-err', 'Dead', 'sent', ?, ?, 'tor-err', 1)`, now, now)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	m.downloaderClient = &fakeDownloaderClient{
		torrents: map[string]*cdlv1.TorrentInfo{
			"tor-err": {Id: "tor-err", Status: "error"},
		},
	}
	m.reapStalledDownloads(ctx, time.Now().UTC())
	status, _ := historyStatus(t, m, "dl_err")
	if status != "failed" {
		t.Fatalf("status=%q want failed", status)
	}
	if !m.isBlacklisted(ctx, "mv_err", "guid-err", 1) {
		t.Fatal("expected blacklist on error snapshot")
	}
}

func TestStallSettingsRoundTrip(t *testing.T) {
	m := newTestModule(t)
	if err := m.UpdateSetting("stall_timeout_minutes", "90"); err != nil {
		t.Fatal(err)
	}
	if err := m.UpdateSetting("stall_auto_mode", "false"); err != nil {
		t.Fatal(err)
	}
	if err := m.UpdateSetting("stall_loop_minutes", "30,120,720"); err != nil {
		t.Fatal(err)
	}
	if err := m.UpdateSetting("stall_loop_minutes", "nope"); err == nil {
		t.Fatal("expected invalid CSV to fail")
	}
	defs := m.Settings()
	got := map[string]string{}
	for _, d := range defs {
		got[d.Key] = d.Value
	}
	if got["stall_timeout_minutes"] != "90" || got["stall_auto_mode"] != "false" || got["stall_loop_minutes"] != "30,120,720" {
		t.Fatalf("settings: %+v", got)
	}
}
