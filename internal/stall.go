package internal

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	cdlv1 "github.com/Muxcore-Media/contracts-downloader/muxcore/downloader/v1"
	"github.com/Muxcore-Media/core/pkg/contracts"
)

const (
	defaultStallTimeoutMinutes = 180
	defaultStallLoopCSV        = "60,360"
	stallWatchInterval         = time.Minute
)

func parseStallLoopMinutes(raw string) ([]int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []int{60, 360}, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("invalid stall_loop_minutes value %q", p)
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("stall_loop_minutes is empty")
	}
	return out, nil
}

func stallLoopCSV(loops []int) string {
	if len(loops) == 0 {
		return defaultStallLoopCSV
	}
	parts := make([]string, len(loops))
	for i, n := range loops {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ",")
}

func (m *Module) migrateStallTables(ctx context.Context, db *sql.DB) error {
	for _, col := range []string{
		`ALTER TABLE wanted_items ADD COLUMN attempt_loop INTEGER DEFAULT 1`,
		`ALTER TABLE download_history ADD COLUMN attempt_loop INTEGER DEFAULT 1`,
		`ALTER TABLE download_history ADD COLUMN last_bytes INTEGER DEFAULT 0`,
		`ALTER TABLE download_history ADD COLUMN last_progress_at TEXT DEFAULT ''`,
	} {
		if _, err := db.ExecContext(ctx, col); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migrate stall columns: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS release_blacklist (
			wanted_item_id TEXT NOT NULL,
			guid           TEXT NOT NULL,
			loop           INTEGER NOT NULL,
			reason         TEXT DEFAULT '',
			created_at     TEXT NOT NULL,
			PRIMARY KEY (wanted_item_id, guid, loop)
		)
	`); err != nil {
		return fmt.Errorf("create release_blacklist: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_download_history_sent ON download_history(status)
	`); err != nil {
		return fmt.Errorf("create sent index: %w", err)
	}
	return nil
}

func releaseAttemptKey(guid, url string) string {
	guid = strings.TrimSpace(guid)
	if guid != "" {
		return guid
	}
	return strings.TrimSpace(url)
}

func (m *Module) stallTimeoutForLoop(loop int) time.Duration {
	m.mu.RLock()
	auto := m.stallAutoMode
	mins := m.stallTimeoutMinutes
	loops := append([]int(nil), m.stallLoopMinutes...)
	m.mu.RUnlock()
	if loop < 1 {
		loop = 1
	}
	if auto && len(loops) > 0 {
		idx := loop - 1
		if idx >= len(loops) {
			idx = len(loops) - 1
		}
		return time.Duration(loops[idx]) * time.Minute
	}
	if mins < 1 {
		mins = defaultStallTimeoutMinutes
	}
	return time.Duration(mins) * time.Minute
}

func (m *Module) attemptLoop(ctx context.Context, itemID string) int {
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil || itemID == "" {
		return 1
	}
	var loop int
	err := db.QueryRowContext(ctx,
		`SELECT COALESCE(attempt_loop, 1) FROM wanted_items WHERE item_id = ?`,
		itemID,
	).Scan(&loop)
	if err != nil || loop < 1 {
		return 1
	}
	return loop
}

func (m *Module) isBlacklisted(ctx context.Context, itemID, key string, loop int) bool {
	if itemID == "" || key == "" {
		return false
	}
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil {
		return false
	}
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM release_blacklist WHERE wanted_item_id = ? AND guid = ? AND loop = ?`,
		itemID, key, loop,
	).Scan(&n)
	return err == nil && n > 0
}

func (m *Module) blacklistRelease(ctx context.Context, itemID, key string, loop int, reason string) {
	key = releaseAttemptKey(key, "")
	if itemID == "" || key == "" {
		return
	}
	if loop < 1 {
		loop = 1
	}
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO release_blacklist (wanted_item_id, guid, loop, reason, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(wanted_item_id, guid, loop) DO UPDATE SET reason = excluded.reason
	`, itemID, key, loop, reason, now); err != nil {
		slog.Warn("blacklist release", "item", itemID, "guid", key, "error", err)
		return
	}
	slog.Info("blacklisted release for attempt loop", "item", itemID, "guid", key, "loop", loop, "reason", reason)
}

func (m *Module) pickNextRelease(ctx context.Context, itemID string, loop int, results []scoredRelease) *scoredRelease {
	for i := range results {
		r := &results[i]
		if strings.TrimSpace(r.DownloadURL) == "" {
			continue
		}
		if m.isBlacklisted(ctx, itemID, releaseAttemptKey(r.GUID, r.DownloadURL), loop) {
			continue
		}
		return r
	}
	return nil
}

func (m *Module) advanceAttemptLoop(ctx context.Context, itemID string, loop int) int {
	if itemID == "" {
		return loop
	}
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil {
		return loop
	}
	var n int
	_ = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM release_blacklist WHERE wanted_item_id = ? AND loop = ?`,
		itemID, loop,
	).Scan(&n)
	if n == 0 {
		return loop
	}
	next := loop + 1
	if _, err := db.ExecContext(ctx,
		`UPDATE wanted_items SET attempt_loop = ?, updated_at = datetime('now') WHERE item_id = ?`,
		next, itemID,
	); err != nil {
		slog.Warn("advance attempt loop", "item", itemID, "error", err)
		return loop
	}
	slog.Info("stall attempt loop advanced", "item", itemID, "from", loop, "to", next, "timeout", m.stallTimeoutForLoop(next))
	return next
}

func (m *Module) stallWatchLoop() {
	ticker := time.NewTicker(stallWatchInterval)
	defer ticker.Stop()
	for range ticker.C {
		m.mu.RLock()
		db := m.db
		m.mu.RUnlock()
		if db == nil {
			return
		}
		m.reapStalledDownloads(context.Background(), time.Now().UTC())
	}
}

type inflightRow struct {
	id, wantedID, guid, url, downloadID, sentAt, lastProgressAt string
	loop, lastBytes                                             int
}

func (m *Module) reapStalledDownloads(ctx context.Context, now time.Time) {
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil {
		return
	}

	rows, err := db.QueryContext(ctx, `
		SELECT id, wanted_item_id, guid, COALESCE(download_url, ''), COALESCE(download_id, ''),
		       COALESCE(sent_at, created_at), COALESCE(attempt_loop, 1), COALESCE(last_bytes, 0),
		       COALESCE(last_progress_at, '')
		FROM download_history WHERE status = 'sent'
	`)
	if err != nil {
		slog.Warn("query in-flight downloads", "error", err)
		return
	}
	var batch []inflightRow
	for rows.Next() {
		var r inflightRow
		if err := rows.Scan(&r.id, &r.wantedID, &r.guid, &r.url, &r.downloadID, &r.sentAt, &r.loop, &r.lastBytes, &r.lastProgressAt); err != nil {
			slog.Warn("scan in-flight download", "error", err)
			continue
		}
		batch = append(batch, r)
	}
	_ = rows.Close()

	for _, r := range batch {
		m.evaluateInflight(ctx, db, r, now)
	}
}

func (m *Module) evaluateInflight(ctx context.Context, db *sql.DB, r inflightRow, now time.Time) {
	timeout := m.stallTimeoutForLoop(r.loop)
	progressAt := parseFlexibleTime(r.lastProgressAt)
	if progressAt.IsZero() {
		progressAt = parseFlexibleTime(r.sentAt)
	}
	if progressAt.IsZero() {
		progressAt = now
	}

	snap, ok := m.torrentSnapshot(ctx, r.downloadID)
	if ok {
		switch strings.ToLower(strings.TrimSpace(snap.status)) {
		case "error", "failed":
			m.removeInflightTorrent(ctx, r.downloadID)
			m.finishInflight(ctx, db, r, "failed", "torrent reported "+snap.status)
			return
		case "completed", "seeding":
			if snap.savePath == "" {
				return
			}
			// Completion event may have been missed; import path still owns wanted-state.
			m.handleDownloadLifecycleEvent(ctx, contracts.EventDownloadCompleted, contracts.DownloadEventPayload{
				ID:       r.downloadID,
				SavePath: snap.savePath,
				Name:     snap.name,
			})
			return
		}
		if snap.downloaded > int64(r.lastBytes) {
			_, _ = db.ExecContext(ctx,
				`UPDATE download_history SET last_bytes = ?, last_progress_at = ? WHERE id = ?`,
				snap.downloaded, now.Format(time.RFC3339), r.id,
			)
			return
		}
	}

	if now.Sub(progressAt) < timeout {
		return
	}
	reason := "stalled: no progress for " + timeout.String()
	m.removeInflightTorrent(ctx, r.downloadID)
	m.finishInflight(ctx, db, r, "stalled", reason)
}

// finishInflight marks history and blacklists the GUID so the next search tries another release.
func (m *Module) finishInflight(ctx context.Context, db *sql.DB, r inflightRow, status, reason string) {
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.ExecContext(ctx,
		`UPDATE download_history SET status = ?, completed_at = ? WHERE id = ? AND status = 'sent'`,
		status, now, r.id,
	); err != nil {
		slog.Warn("mark inflight "+status, "id", r.id, "error", err)
		return
	}
	m.blacklistRelease(ctx, r.wantedID, releaseAttemptKey(r.guid, r.url), r.loop, reason)
	slog.Info("gave up on torrent", "id", r.id, "status", status, "title_guid", r.guid, "reason", reason)
}

func (m *Module) removeInflightTorrent(ctx context.Context, downloadID string) {
	if downloadID == "" {
		return
	}
	client := m.downloaderClientLocked()
	if client == nil {
		_ = m.ensureDownloader(ctx)
		client = m.downloaderClientLocked()
	}
	if client == nil {
		return
	}
	rmCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := client.RemoveTorrent(rmCtx, &cdlv1.RemoveTorrentRequest{TorrentId: downloadID, DeleteFiles: true}); err != nil {
		slog.Debug("remove stalled torrent", "id", downloadID, "error", err)
	}
}

type torrentSnap struct {
	downloaded int64
	status     string
	savePath   string
	name       string
}

func (m *Module) downloaderClientLocked() cdlv1.DownloaderServiceClient {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.downloaderClient
}

func (m *Module) torrentSnapshot(ctx context.Context, downloadID string) (torrentSnap, bool) {
	if downloadID == "" {
		return torrentSnap{}, false
	}
	client := m.downloaderClientLocked()
	if client == nil {
		_ = m.ensureDownloader(ctx)
		client = m.downloaderClientLocked()
	}
	if client == nil {
		return torrentSnap{}, false
	}
	snapCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := client.GetTorrent(snapCtx, &cdlv1.GetTorrentRequest{TorrentId: downloadID})
	if err != nil || resp.GetTorrent() == nil {
		return torrentSnap{}, false
	}
	t := resp.GetTorrent()
	return torrentSnap{
		downloaded: t.GetDownloaded(),
		status:     t.GetStatus(),
		savePath:   t.GetSavePath(),
		name:       t.GetName(),
	}, true
}
