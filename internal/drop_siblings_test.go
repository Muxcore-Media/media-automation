package internal

import (
	"context"
	"testing"
	"time"

	cdlv1 "github.com/Muxcore-Media/contracts-downloader/muxcore/downloader/v1"
)

func TestDropSiblingDownloadsRemovesOthers(t *testing.T) {
	m := newTestModule(t)
	fake := &fakeDownloaderClient{
		torrents: map[string]*cdlv1.TorrentInfo{
			"tor-keep": {Id: "tor-keep"},
			"tor-drop": {Id: "tor-drop"},
		},
	}
	m.downloaderClient = fake
	now := time.Now().UTC().Format(time.RFC3339)
	m.mu.Lock()
	_, err := m.db.Exec(`
		INSERT INTO download_history (id, wanted_item_id, guid, title, status, created_at, download_id, download_url, attempt_loop)
		VALUES ('h-keep', 'ep1', 'g-keep', 'Keep', 'sent', ?, 'tor-keep', 'magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 1),
		       ('h-drop', 'ep1', 'g-drop', 'Drop', 'sent', ?, 'tor-drop', 'magnet:?xt=urn:btih:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 1)`,
		now, now)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	m.dropSiblingDownloads(context.Background(), "ep1", "tor-keep")

	if len(fake.removed) != 1 || fake.removed[0] != "tor-drop" {
		t.Fatalf("removed=%v want [tor-drop]", fake.removed)
	}
	if len(fake.deleteFiles) != 1 || !fake.deleteFiles[0] {
		t.Fatalf("deleteFiles=%v", fake.deleteFiles)
	}
	var st string
	m.mu.RLock()
	err = m.db.QueryRow(`SELECT status FROM download_history WHERE id = 'h-drop'`).Scan(&st)
	m.mu.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	if st != "superseded" {
		t.Fatalf("status=%q want superseded", st)
	}
	m.mu.RLock()
	err = m.db.QueryRow(`SELECT status FROM download_history WHERE id = 'h-keep'`).Scan(&st)
	m.mu.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	if st != "sent" {
		t.Fatalf("keep status=%q want sent", st)
	}
}
