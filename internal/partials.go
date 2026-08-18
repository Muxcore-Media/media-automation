package internal

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	scannerv1 "github.com/Muxcore-Media/media-scanner/proto/scannerv1"
)

func (m *Module) maybeMergeMagnet(ctx context.Context, itemID string, loop int, results []scoredRelease, best *scoredRelease) string {
	if best == nil {
		return ""
	}
	_ = ctx
	_ = itemID
	_ = loop
	url := magnetURLForRelease(results, best)
	id := parseMagnetIdentity(url)
	if id.InfoHash == "" && id.InfoHashV2 == "" {
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

func magnetURLForRelease(results []scoredRelease, best *scoredRelease) string {
	if best == nil {
		return ""
	}
	url := strings.TrimSpace(best.DownloadURL)
	if isMagnetURL(url) {
		return url
	}
	for i := range results {
		cand := strings.TrimSpace(results[i].DownloadURL)
		if !isMagnetURL(cand) {
			continue
		}
		if best.GUID != "" && results[i].GUID == best.GUID {
			return cand
		}
		if results[i].Title == best.Title && best.Size > 0 && results[i].Size == best.Size {
			return cand
		}
	}
	return url
}

func isMagnetURL(u string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(u)), "magnet:")
}

func (m *Module) dispatchSavePath(ctx context.Context, itemID string, downloadURL, guid string) string {
	id := parseMagnetIdentity(downloadURL)
	if reuse := m.keptSavePath(ctx, itemID, id); reuse != "" {
		return m.absoluteDownloadPath(reuse)
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
	return m.absoluteDownloadPath(partialSavePath(itemID, ident))
}

func (m *Module) absoluteDownloadPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	m.mu.RLock()
	root := strings.TrimSpace(m.downloadDir)
	m.mu.RUnlock()
	if root == "" {
		return p
	}
	return filepath.Join(root, p)
}

func resolveExistingImportPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return p
	}
	var cands []string
	if cwd, err := os.Getwd(); err == nil {
		cands = append(cands, filepath.Join(cwd, p))
	}
	for _, env := range []string{"MVP_DOWNLOADS_DIR", "AUTOMATION_DOWNLOAD_DIR"} {
		if d := strings.TrimSpace(os.Getenv(env)); d != "" {
			cands = append(cands, filepath.Join(d, p))
		}
	}
	for _, c := range cands {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return p
}

func (m *Module) retryImportFailed(ctx context.Context) {
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil {
		return
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, COALESCE(download_id, ''), COALESCE(save_path, ''), COALESCE(wanted_item_id, '')
		FROM download_history
		WHERE status = 'import_failed'
		LIMIT 20
	`)
	if err != nil {
		return
	}
	type failed struct {
		id, downloadID, savePath, wantedID string
	}
	var recs []failed
	for rows.Next() {
		var r failed
		if err := rows.Scan(&r.id, &r.downloadID, &r.savePath, &r.wantedID); err != nil {
			continue
		}
		recs = append(recs, r)
	}
	rows.Close()
	if len(recs) == 0 {
		return
	}
	if err := m.ensureScanner(ctx); err != nil {
		slog.Debug("retry import_failed: scanner unavailable", "error", err)
		return
	}
	client := m.getScannerClient()
	if client == nil {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, rec := range recs {
		path := resolveExistingImportPath(rec.savePath)
		if path == "" {
			continue
		}
		impCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		resp, err := client.ImportPath(impCtx, &scannerv1.ImportPathRequest{Path: path})
		cancel()
		if err != nil {
			slog.Warn("retry ImportPath failed", "id", rec.id, "path", path, "error", err)
			continue
		}
		if resp.GetFilesImported() == 0 && resp.GetFilesFound() == 0 {
			slog.Debug("retry ImportPath found nothing", "id", rec.id, "path", path)
			continue
		}
		if _, uerr := db.ExecContext(ctx,
			`UPDATE download_history SET status = ?, completed_at = ? WHERE id = ?`,
			"completed", now, rec.id,
		); uerr != nil {
			slog.Warn("mark retried import completed", "id", rec.id, "error", uerr)
			continue
		}
		m.cleanupWantedPartials(rec.wantedID, path)
		slog.Info("retried import succeeded", "id", rec.id, "title_id", rec.wantedID, "imported", resp.GetFilesImported(), "skipped", resp.GetFilesSkipped())
	}
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
