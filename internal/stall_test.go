package internal

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cdlv1 "github.com/Muxcore-Media/contracts-downloader/muxcore/downloader/v1"
	"github.com/Muxcore-Media/core/pkg/contracts"
	autov1 "github.com/Muxcore-Media/media-automation/proto/automationv1"
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

func TestReapMissingTorrentFailsImmediately(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	m.mu.Lock()
	m.stallAutoMode = false
	m.stallTimeoutMinutes = 180
	m.mu.Unlock()

	sent := time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339)
	m.mu.Lock()
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO download_history (id, wanted_item_id, guid, title, indexer, size, score, download_url, download_protocol, status, sent_at, created_at, download_id, attempt_loop, last_bytes, last_progress_at)
		VALUES ('dl_gone', 'mv_gone', 'guid-gone', 'Ghost', '', 0, 10, 'magnet:?xt=urn:btih:gone', 'torrent', 'sent', ?, ?, 'tor-gone', 1, 0, ?)`,
		sent, sent, sent)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	m.downloaderClient = &fakeDownloaderClient{torrents: map[string]*cdlv1.TorrentInfo{}}

	m.reapStalledDownloads(ctx, time.Now().UTC())
	status, _ := historyStatus(t, m, "dl_gone")
	if status != "stalled" {
		t.Fatalf("missing torrent should stall immediately, status=%q", status)
	}
}

func TestReapTransientGetErrorWaitsTimeout(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	m.mu.Lock()
	m.stallAutoMode = false
	m.stallTimeoutMinutes = 180
	m.mu.Unlock()

	sent := time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339)
	m.mu.Lock()
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO download_history (id, wanted_item_id, guid, title, status, sent_at, created_at, download_id, attempt_loop, last_bytes, last_progress_at)
		VALUES ('dl_tmp', 'mv_tmp', 'guid-tmp', 'Flaky', 'sent', ?, ?, 'tor-tmp', 1, 0, ?)`,
		sent, sent, sent)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	m.downloaderClient = &fakeDownloaderClient{getErr: fmt.Errorf("connection refused")}

	m.reapStalledDownloads(ctx, time.Now().UTC())
	status, _ := historyStatus(t, m, "dl_tmp")
	if status != "sent" {
		t.Fatalf("transient GetTorrent error should wait stall timeout, status=%q", status)
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

func TestReleaseInFlightMatchesSharedSeasonPack(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	magnet := "magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&dn=BB.Remux"
	title := "Breaking Bad (2008) S01-S05 1080p BluRay REMUX Dual Audio [Hindi+Eng] ~ RemuxDoc"
	now := time.Now().UTC().Format(time.RFC3339)
	m.mu.Lock()
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO download_history (id, wanted_item_id, guid, title, indexer, size, score, download_url, download_protocol, status, sent_at, created_at, download_id, infohash)
		VALUES ('dl_pack', 'ep_s04e01', 'guid-remux', ?, '', 0, 270, ?, 'torrent', 'sent', ?, ?, 'tor-pack', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')`,
		title, magnet, now, now)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	if !m.releaseInFlight(ctx, "guid-remux", magnet, title) {
		t.Fatal("same guid should be in-flight")
	}
	if !m.releaseInFlight(ctx, "other-guid", magnet, "other title") {
		t.Fatal("same magnet hash should be in-flight")
	}
	if !m.releaseInFlight(ctx, "other-guid", "magnet:?xt=urn:btih:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", title) {
		t.Fatal("same title should be in-flight")
	}
	if m.releaseInFlight(ctx, "other-guid", "magnet:?xt=urn:btih:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "Different.S01E01") {
		t.Fatal("unrelated release should not be in-flight")
	}

	fake := &fakeDownloaderClient{torrentID: "tor-should-not-add"}
	m.downloaderClient = fake
	disp, err := m.Dispatch(ctx, &autov1.DispatchRequest{
		Guid:             "guid-remux-ep2",
		Title:            title,
		DownloadUrl:      magnet,
		DownloadProtocol: "torrent",
		ItemType:         "tv",
		ItemId:           "ep_s04e02",
	})
	if err != nil {
		t.Fatal(err)
	}
	if disp.GetDownloadId() != "tor-pack" {
		t.Fatalf("download_id=%q want tor-pack", disp.GetDownloadId())
	}
	if fake.calls != 0 {
		t.Fatalf("AddTorrent calls=%d want 0", fake.calls)
	}
}

func TestReleaseInFlightMatchesCompletedSeasonPack(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	title := "King.of.the.Hill.S15.Complete.1080p.WEBRip.10Bit.DDP5.1.x265-NeoNoir"
	now := time.Now().UTC().Format(time.RFC3339)
	m.mu.Lock()
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO download_history (id, wanted_item_id, guid, title, indexer, size, score, download_url, download_protocol, status, sent_at, created_at, download_id)
		VALUES ('dl_koth', 'ep_s15e01', 'guid-koth', ?, '', 0, 145, 'magnet:?xt=urn:btih:cccccccccccccccccccccccccccccccccccccccc', 'torrent', 'completed', ?, ?, 'tor-koth')`,
		title, now, now)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if !m.releaseInFlight(ctx, "guid-koth-ep2", "magnet:?xt=urn:btih:dddddddddddddddddddddddddddddddddddddddd", title) {
		t.Fatal("completed pack title should still count as grabbed")
	}
	fake := &fakeDownloaderClient{torrentID: "tor-should-not-add"}
	m.downloaderClient = fake
	disp, err := m.Dispatch(ctx, &autov1.DispatchRequest{
		Guid:        "guid-koth-ep2",
		Title:       title,
		DownloadUrl: "magnet:?xt=urn:btih:dddddddddddddddddddddddddddddddddddddddd",
		ItemType:    "tv",
		ItemId:      "ep_s15e02",
	})
	if err != nil {
		t.Fatal(err)
	}
	if disp.GetDownloadId() != "tor-koth" {
		t.Fatalf("download_id=%q want tor-koth", disp.GetDownloadId())
	}
	if fake.calls != 0 {
		t.Fatalf("AddTorrent calls=%d want 0", fake.calls)
	}
}

func TestFailedReleaseNotRedispatchedSameLoop(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	title := "The New Adventures of Winnie the Pooh S02 1080p DSNP WEBRip AAC2 0 x264"
	guid := "guid-pooh-s02"
	url := "https://example.test/pooh-s02"
	now := time.Now().UTC().Format(time.RFC3339)
	m.mu.Lock()
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO download_history (id, wanted_item_id, guid, title, indexer, size, score, download_url, download_protocol, status, sent_at, created_at, download_id, attempt_loop)
		VALUES ('dl_pooh', 'ep_pooh_0_1', ?, ?, '', 0, 130, ?, 'torrent', 'failed', ?, ?, 'tor-pooh', 1)`,
		guid, title, url, now, now)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	fake := &fakeDownloaderClient{torrentID: "tor-should-not-add"}
	m.downloaderClient = fake
	for i := 0; i < 5; i++ {
		disp, err := m.Dispatch(ctx, &autov1.DispatchRequest{
			Guid:             guid,
			Title:            title,
			DownloadUrl:      url,
			DownloadProtocol: "torrent",
			ItemType:         "tv",
			ItemId:           fmt.Sprintf("ep_pooh_0_%d", i+2),
		})
		if err != nil {
			t.Fatal(err)
		}
		if disp.GetDownloadId() != "tor-pooh" {
			t.Fatalf("download_id=%q want tor-pooh (skip duplicate)", disp.GetDownloadId())
		}
	}
	if fake.calls != 0 {
		t.Fatalf("AddTorrent calls=%d want 0 for same failed GUID in loop 1", fake.calls)
	}

	now2 := time.Now().UTC().Format(time.RFC3339)
	m.mu.Lock()
	_, err = m.db.ExecContext(ctx, `
		INSERT INTO wanted_items (id, item_type, item_id, tmdb_id, title, year, season_number, episode_number, monitored, missing, attempt_loop, created_at, updated_at)
		VALUES ('w_pooh_s02', 'tv', 'tv_pooh_s02', 2005, 'Pooh', 1988, 2, 0, 1, 1, 2, ?, ?)`,
		now2, now2)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	disp, err := m.Dispatch(ctx, &autov1.DispatchRequest{
		Guid:             guid,
		Title:            title,
		DownloadUrl:      url,
		DownloadProtocol: "torrent",
		ItemType:         "tv",
		ItemId:           "tv_pooh_s02",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fake.calls != 1 {
		t.Fatalf("loop 2 should retry failed GUID, AddTorrent calls=%d want 1 (id=%s)", fake.calls, disp.GetDownloadId())
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
	if err := m.UpdateSetting("keep_stalled_partials", "true"); err != nil {
		t.Fatal(err)
	}
	defs := m.Settings()
	got := map[string]string{}
	for _, d := range defs {
		got[d.Key] = d.Value
	}
	if got["stall_timeout_minutes"] != "90" || got["stall_auto_mode"] != "false" || got["stall_loop_minutes"] != "30,120,720" {
		t.Fatalf("settings: %+v", got)
	}
	if got["keep_stalled_partials"] != "true" {
		t.Fatalf("keep_stalled_partials=%q", got["keep_stalled_partials"])
	}
}

func TestReapStalledKeepsPartials(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	m.mu.Lock()
	m.stallAutoMode = false
	m.stallTimeoutMinutes = 60
	m.keepStalledPartials = true
	m.mu.Unlock()

	sent := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	m.mu.Lock()
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO download_history (id, wanted_item_id, guid, title, indexer, size, score, download_url, download_protocol, status, sent_at, created_at, download_id, attempt_loop, last_bytes, last_progress_at)
		VALUES ('dl_keep', 'mv_keep', 'guid-keep', 'Stuck', '', 0, 10, 'magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'torrent', 'sent', ?, ?, 'tor-keep', 1, 0, ?)`,
		sent, sent, sent)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeDownloaderClient{
		torrents: map[string]*cdlv1.TorrentInfo{
			"tor-keep": {Id: "tor-keep", Status: "downloading", Downloaded: 0},
		},
	}
	m.downloaderClient = fake
	m.reapStalledDownloads(ctx, time.Now().UTC())
	if len(fake.removed) != 1 || fake.removed[0] != "tor-keep" {
		t.Fatalf("removed %v", fake.removed)
	}
	if len(fake.deleteFiles) != 1 || fake.deleteFiles[0] {
		t.Fatalf("DeleteFiles=%v want false", fake.deleteFiles)
	}
}

func TestMaybeMergeMagnetLoop2(t *testing.T) {
	m := newTestModule(t)
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	results := []scoredRelease{
		{GUID: "a", Title: "A", DownloadURL: "magnet:?xt=urn:btih:" + hash + "&tr=udp://a.example:80/announce", Score: 200},
		{GUID: "b", Title: "B", DownloadURL: "magnet:?xt=urn:btih:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb&tr=udp://b.example:80/announce", Score: 150},
		{GUID: "c", Title: "C", DownloadURL: "magnet:?xt=urn:btih:" + hash + "&tr=udp://c.example:80/announce", Score: 100},
	}
	got := m.maybeMergeMagnet(context.Background(), "mv_x", 2, results, &results[0])
	if !strings.Contains(got, "udp://a.example:80/announce") || !strings.Contains(got, "udp://c.example:80/announce") {
		t.Fatalf("merged trackers: %s", got)
	}
	if strings.Contains(got, "bbbbbbbb") {
		t.Fatalf("should not merge B: %s", got)
	}
	loop1 := m.maybeMergeMagnet(context.Background(), "mv_x", 1, results, &results[0])
	if !strings.Contains(loop1, "udp://c.example:80/announce") {
		t.Fatalf("loop 1 should merge same-hash trackers: %s", loop1)
	}
}

func TestMagnetURLForReleasePrefersSiblingMagnet(t *testing.T) {
	t.Parallel()
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	httpURL := "http://127.0.0.1:9696/2/download?apikey=x"
	magnet := "magnet:?xt=urn:btih:" + hash
	results := []scoredRelease{
		{GUID: "same", Title: "Show.S01E01", Size: 100, DownloadURL: httpURL, Score: 200},
		{GUID: "same", Title: "Show.S01E01", Size: 100, DownloadURL: magnet, Score: 180},
	}
	got := magnetURLForRelease(results, &results[0])
	if got != magnet {
		t.Fatalf("got %q want magnet", got)
	}
}

func TestSameHashHitsShareSavePathAndMergedTrackers(t *testing.T) {
	m := newTestModule(t)
	m.mu.Lock()
	m.keepStalledPartials = true
	m.downloadDir = "/data/downloads"
	m.mu.Unlock()
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	results := []scoredRelease{
		{GUID: "a", Title: "Show", DownloadURL: "magnet:?xt=urn:btih:" + hash + "&tr=udp://a.example:80/announce", Score: 200},
		{GUID: "c", Title: "Show", DownloadURL: "magnet:?xt=urn:btih:" + hash + "&tr=udp://c.example:80/announce", Score: 100},
	}
	grab := m.maybeMergeMagnet(context.Background(), "tv_1", 1, results, &results[0])
	if !strings.Contains(grab, "udp://a.example:80/announce") || !strings.Contains(grab, "udp://c.example:80/announce") {
		t.Fatalf("merged trackers: %s", grab)
	}
	path := m.dispatchSavePath(context.Background(), "tv_1", grab, results[0].GUID)
	want := "/data/downloads/partials/tv_1/btih_" + hash
	if path != want {
		t.Fatalf("save path %q want %q", path, want)
	}
	fake := &fakeDownloaderClient{torrentID: "tor-merge"}
	m.downloaderClient = fake
	_, err := m.Dispatch(context.Background(), &autov1.DispatchRequest{
		Guid:             "a",
		Title:            "Show",
		DownloadUrl:      grab,
		DownloadProtocol: "torrent",
		ItemType:         "tv",
		ItemId:           "tv_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fake.lastSavePath != want {
		t.Fatalf("dispatch save %q want %q", fake.lastSavePath, want)
	}
	if !strings.Contains(fake.lastURL, "udp://a.example:80/announce") || !strings.Contains(fake.lastURL, "udp://c.example:80/announce") {
		t.Fatalf("dispatch url %s", fake.lastURL)
	}
	var infohash string
	m.mu.RLock()
	err = m.db.QueryRowContext(context.Background(),
		`SELECT COALESCE(infohash,'') FROM download_history WHERE download_id = ?`, "tor-merge",
	).Scan(&infohash)
	m.mu.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	if infohash != hash {
		t.Fatalf("infohash %q want %q", infohash, hash)
	}
}

func TestDispatchKeepPartialsSavePath(t *testing.T) {
	m := newTestModule(t)
	m.mu.Lock()
	m.keepStalledPartials = true
	m.downloadDir = "/data/downloads"
	m.mu.Unlock()
	fake := &fakeDownloaderClient{torrentID: "tor-partial"}
	m.downloaderClient = fake
	_, err := m.Dispatch(context.Background(), &autov1.DispatchRequest{
		Guid:             "guid-a",
		Title:            "A",
		DownloadUrl:      "magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&dn=A",
		DownloadProtocol: "torrent",
		ItemType:         "movie",
		ItemId:           "mv_550",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "/data/downloads/partials/mv_550/btih_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if fake.lastSavePath != want {
		t.Fatalf("save path %q want %q", fake.lastSavePath, want)
	}
}

func TestDownloadStartedRecordsIdentity(t *testing.T) {
	m := newTestModule(t)
	insertHistoryWithDownloadID(t, m, "dl_start", "tor-start")
	m.handleDownloadLifecycleEvent(context.Background(), contracts.EventDownloadStarted, contracts.DownloadEventPayload{
		ID:       "tor-start",
		InfoHash: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		SavePath: "/dl/partials/item-1/pending_x",
		Files:    []contracts.DownloadEventFile{{Path: "f.bin", Size: 100}},
	})
	var hash, path, fp, status string
	m.mu.RLock()
	err := m.db.QueryRowContext(context.Background(),
		`SELECT COALESCE(infohash,''), COALESCE(save_path,''), COALESCE(files_fingerprint,''), status FROM download_history WHERE id = ?`,
		"dl_start",
	).Scan(&hash, &path, &fp, &status)
	m.mu.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	if hash != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("hash %q", hash)
	}
	if path != "/dl/partials/item-1/pending_x" {
		t.Fatalf("path %q", path)
	}
	if fp != "100" {
		t.Fatalf("fp %q", fp)
	}
	if status != "sent" {
		t.Fatalf("status %q", status)
	}
}

func TestCleanupWantedPartials(t *testing.T) {
	m := newTestModule(t)
	root := t.TempDir()
	keep := filepath.Join(root, "partials", "mv1", "btih_keep")
	drop := filepath.Join(root, "partials", "mv1", "btih_drop")
	if err := os.MkdirAll(keep, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(drop, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(drop, "x"), []byte("n"), 0644); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	m.mu.Lock()
	_, err := m.db.Exec(`
		INSERT INTO download_history (id, wanted_item_id, guid, title, status, created_at, save_path)
		VALUES ('k', 'mv1', 'g1', 'K', 'completed', ?, ?),
		       ('d', 'mv1', 'g2', 'D', 'stalled', ?, ?)`,
		now, keep, now, drop)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	m.cleanupWantedPartials("mv1", keep)
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("keep dir removed: %v", err)
	}
	if _, err := os.Stat(drop); !os.IsNotExist(err) {
		t.Fatalf("drop dir still present: %v", err)
	}
}

func TestCleanupWantedPartialsDropsCwdLeftover(t *testing.T) {
	m := newTestModule(t)
	base := t.TempDir()
	cwd := filepath.Join(base, "mvp")
	dl := filepath.Join(base, "downloads")
	keep := filepath.Join(dl, "partials", "mv1", "btih_keep")
	drop := filepath.Join(cwd, "partials", "mv1", "btih_drop")
	if err := os.MkdirAll(keep, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(drop, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(drop, "x"), []byte("n"), 0644); err != nil {
		t.Fatal(err)
	}
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	m.mu.Lock()
	m.downloadDir = dl
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = m.db.Exec(`
		INSERT INTO download_history (id, wanted_item_id, guid, title, status, created_at, save_path)
		VALUES ('k', 'mv1', 'g1', 'K', 'completed', ?, ?),
		       ('d', 'mv1', 'g2', 'D', 'stalled', ?, ?)`,
		now, keep, now, "partials/mv1/btih_drop")
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	m.cleanupWantedPartials("mv1", keep)
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("keep dir removed: %v", err)
	}
	if _, err := os.Stat(drop); !os.IsNotExist(err) {
		t.Fatalf("cwd leftover still present: %v", err)
	}
}

func TestRelocateStrayCwdPartials(t *testing.T) {
	m := newTestModule(t)
	base := t.TempDir()
	cwd := filepath.Join(base, "mvp")
	dl := filepath.Join(base, "downloads")
	unique := filepath.Join(cwd, "partials", "item-unique")
	dupSrc := filepath.Join(cwd, "partials", "item-dup")
	dupDst := filepath.Join(dl, "partials", "item-dup")
	for _, d := range []string{unique, dupSrc, dupDst} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(unique, "a"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dupSrc, "old"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dupDst, "keep"), []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	m.mu.Lock()
	m.downloadDir = dl
	m.mu.Unlock()
	m.relocateStrayCwdPartials()
	moved := filepath.Join(dl, "partials", "item-unique")
	if _, err := os.Stat(filepath.Join(moved, "a")); err != nil {
		t.Fatalf("unique dir not moved: %v", err)
	}
	if _, err := os.Stat(unique); !os.IsNotExist(err) {
		t.Fatalf("unique src still present")
	}
	if _, err := os.Stat(dupSrc); !os.IsNotExist(err) {
		t.Fatalf("duplicate cwd leftover still present")
	}
	if _, err := os.Stat(filepath.Join(dupDst, "keep")); err != nil {
		t.Fatalf("watch-dir copy removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cwd, "partials")); !os.IsNotExist(err) {
		t.Fatalf("empty cwd partials still present")
	}
}

func TestKeptSavePathReusedOnDispatch(t *testing.T) {
	m := newTestModule(t)
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	now := time.Now().UTC().Format(time.RFC3339)
	kept := "partials/mv_reuse/btih_" + hash
	m.mu.Lock()
	_, err := m.db.Exec(`
		INSERT INTO download_history (id, wanted_item_id, guid, title, status, created_at, infohash, save_path)
		VALUES ('old', 'mv_reuse', 'g-old', 'Old', 'stalled', ?, ?, ?)`,
		now, hash, kept)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeDownloaderClient{torrentID: "tor-reuse"}
	m.downloaderClient = fake
	_, err = m.Dispatch(context.Background(), &autov1.DispatchRequest{
		Guid:        "g-new",
		Title:       "New",
		DownloadUrl: "magnet:?xt=urn:btih:" + hash,
		ItemId:      "mv_reuse",
		ItemType:    "movie",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fake.lastSavePath != kept {
		t.Fatalf("save path %q want %q", fake.lastSavePath, kept)
	}
}
