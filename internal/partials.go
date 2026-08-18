package internal

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (m *Module) maybeMergeMagnet(ctx context.Context, itemID string, loop int, results []scoredRelease, best *scoredRelease) string {
	if best == nil {
		return ""
	}
	url := strings.TrimSpace(best.DownloadURL)
	id := parseMagnetIdentity(url)
	if id.InfoHash == "" && id.InfoHashV2 == "" {
		return url
	}
	combine := loop >= 2 || m.keptSavePath(ctx, itemID, id) != ""
	if !combine {
		return url
	}
	urls := make([]string, 0, len(results))
	for i := range results {
		oid := parseMagnetIdentity(results[i].DownloadURL)
		if identitiesMatch(id, oid) {
			urls = append(urls, results[i].DownloadURL)
		}
	}
	if merged := mergeMagnets(urls); merged != "" {
		return merged
	}
	return url
}

func (m *Module) dispatchSavePath(ctx context.Context, itemID string, downloadURL, guid string) string {
	id := parseMagnetIdentity(downloadURL)
	if reuse := m.keptSavePath(ctx, itemID, id); reuse != "" {
		return reuse
	}
	if !m.keepStalledPartialsLocked() || strings.TrimSpace(itemID) == "" {
		return ""
	}
	ident := partialDirIdentity(id)
	if ident == "" {
		ident = "pending_" + sanitizePartialToken(guid)
		if ident == "pending_" {
			ident = "pending_" + time.Now().UTC().Format("20060102T150405")
		}
	}
	return partialSavePath(itemID, ident)
}

func (m *Module) keptSavePath(ctx context.Context, itemID string, id magnetIdentity) string {
	if itemID == "" || (id.InfoHash == "" && id.InfoHashV2 == "") {
		return ""
	}
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil {
		return ""
	}
	var path string
	err := db.QueryRowContext(ctx, `
		SELECT save_path FROM download_history
		WHERE wanted_item_id = ?
		  AND COALESCE(save_path, '') != ''
		  AND (
		    (? != '' AND lower(infohash) = ?)
		    OR (? != '' AND lower(infohash_v2) = ?)
		  )
		ORDER BY CASE status WHEN 'stalled' THEN 0 WHEN 'failed' THEN 1 WHEN 'sent' THEN 2 ELSE 3 END,
		         COALESCE(last_bytes, 0) DESC
		LIMIT 1
	`, itemID, id.InfoHash, id.InfoHash, id.InfoHashV2, id.InfoHashV2).Scan(&path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(path)
}

func (m *Module) recordDownloadIdentity(ctx context.Context, downloadID, infohash, savePath, fingerprint string) {
	if downloadID == "" {
		return
	}
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil {
		return
	}
	infohash = strings.ToLower(strings.TrimSpace(infohash))
	if _, err := db.ExecContext(ctx, `
		UPDATE download_history SET
			infohash = CASE WHEN ? != '' THEN ? ELSE infohash END,
			save_path = CASE WHEN ? != '' THEN ? ELSE save_path END,
			files_fingerprint = CASE WHEN ? != '' THEN ? ELSE files_fingerprint END
		WHERE download_id = ?
	`, infohash, infohash, savePath, savePath, fingerprint, fingerprint, downloadID); err != nil {
		slog.Debug("record download identity", "download_id", downloadID, "error", err)
	}
}

func (m *Module) cleanupWantedPartials(wantedID, keepPath string) {
	wantedID = strings.TrimSpace(wantedID)
	if wantedID == "" {
		return
	}
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil {
		return
	}
	rows, err := db.Query(`
		SELECT DISTINCT COALESCE(save_path, '') FROM download_history WHERE wanted_item_id = ?
	`, wantedID)
	if err != nil {
		return
	}
	defer rows.Close()
	keepPath = filepath.Clean(strings.TrimSpace(keepPath))
	var parent string
	if keepPath != "" && keepPath != "." {
		parent = filepath.Dir(keepPath)
	}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			continue
		}
		p = filepath.Clean(strings.TrimSpace(p))
		if p == "" || p == "." || p == keepPath {
			continue
		}
		if parent != "" && filepath.Dir(p) != parent {
			continue
		}
		if !strings.Contains(filepath.ToSlash(p), "/partials/") {
			continue
		}
		if err := os.RemoveAll(p); err != nil {
			slog.Debug("cleanup partial dir", "path", p, "error", err)
		}
	}
}

func (m *Module) insertDownloadHistory(ctx context.Context, db *sql.DB, histID, itemID, guid, title, indexer string, size, score int64, downloadURL, protocol, downloadID, savePath string, loop int, now string) error {
	id := parseMagnetIdentity(downloadURL)
	_, err := db.ExecContext(ctx, `
		INSERT INTO download_history (id, wanted_item_id, guid, title, indexer, size, score, download_url, download_protocol, status, sent_at, created_at, download_id, attempt_loop, last_bytes, last_progress_at, infohash, infohash_v2, save_path, files_fingerprint)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'sent', ?, ?, ?, ?, 0, ?, ?, ?, ?, '')`,
		histID, itemID, guid, title, indexer, size, score, downloadURL, protocol,
		now, now, downloadID, loop, now, id.InfoHash, id.InfoHashV2, savePath,
	)
	return err
}
