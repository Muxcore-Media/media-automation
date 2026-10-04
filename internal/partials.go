package internal

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	cdlv1 "github.com/Muxcore-Media/contracts-downloader/muxcore/downloader/v1"
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

func sameFilesystemPath(a, b string) bool {
	a = filepath.Clean(strings.TrimSpace(a))
	b = filepath.Clean(strings.TrimSpace(b))
	if a == "" || b == "" || a == "." || b == "." {
		return false
	}
	if a == b {
		return true
	}
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	return err1 == nil && err2 == nil && aa == bb
}

func (m *Module) partialScanRoots() []string {
	seen := map[string]struct{}{}
	var roots []string
	add := func(p string) {
		p = filepath.Clean(strings.TrimSpace(p))
		if p == "" || p == "." {
			return
		}
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		roots = append(roots, p)
	}
	m.mu.RLock()
	add(m.downloadDir)
	m.mu.RUnlock()
	if cwd, err := os.Getwd(); err == nil {
		add(cwd)
	}
	for _, env := range []string{"MVP_DOWNLOADS_DIR", "AUTOMATION_DOWNLOAD_DIR"} {
		add(os.Getenv(env))
	}
	return roots
}

func (m *Module) existingPartialCandidates(p string) []string {
	p = strings.TrimSpace(p)
	if p == "" {
		return nil
	}
	var out []string
	seen := map[string]struct{}{}
	add := func(c string) {
		c = filepath.Clean(strings.TrimSpace(c))
		if c == "" || c == "." {
			return
		}
		if abs, err := filepath.Abs(c); err == nil {
			c = abs
		}
		if _, ok := seen[c]; ok {
			return
		}
		if _, err := os.Stat(c); err != nil {
			return
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	if filepath.IsAbs(p) {
		add(p)
		return out
	}
	for _, root := range m.partialScanRoots() {
		add(filepath.Join(root, p))
	}
	return out
}

// relocateStrayCwdPartials moves leftover {cwd}/partials into the scanner watch dir
// when AUTOMATION_DOWNLOAD_DIR is a different tree. Duplicate names already under
// the watch dir are deleted from cwd.
func (m *Module) relocateStrayCwdPartials() {
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	m.mu.RLock()
	root := strings.TrimSpace(m.downloadDir)
	m.mu.RUnlock()
	if root == "" {
		return
	}
	stray := filepath.Join(cwd, "partials")
	dest := filepath.Join(root, "partials")
	if sameFilesystemPath(stray, dest) {
		return
	}
	fi, err := os.Stat(stray)
	if err != nil || !fi.IsDir() {
		return
	}
	if err := os.MkdirAll(dest, 0755); err != nil {
		slog.Warn("create watch-dir partials", "path", dest, "error", err)
		return
	}
	entries, err := os.ReadDir(stray)
	if err != nil {
		return
	}
	for _, e := range entries {
		src := filepath.Join(stray, e.Name())
		dst := filepath.Join(dest, e.Name())
		if _, err := os.Stat(dst); err == nil {
			slog.Info("removing stray cwd partial already under watch dir", "path", src)
			if err := os.RemoveAll(src); err != nil {
				slog.Warn("remove stray cwd partial", "path", src, "error", err)
			}
			continue
		}
		if err := os.Rename(src, dst); err != nil {
			slog.Warn("move stray cwd partial into watch dir", "from", src, "to", dst, "error", err)
			continue
		}
		slog.Info("moved stray cwd partial into watch dir", "from", src, "to", dst)
	}
	leftover, err := os.ReadDir(stray)
	if err == nil && len(leftover) == 0 {
		_ = os.Remove(stray)
	}
}

func canonicalPartialSavePath(savePath, infoHash string) string {
	savePath = strings.TrimSpace(savePath)
	infoHash = strings.ToLower(strings.TrimSpace(infoHash))
	if savePath == "" || infoHash == "" {
		return savePath
	}
	if savePath == "storage://torrent/pending" || strings.HasSuffix(savePath, "/pending") {
		if len(infoHash) == 40 {
			return "storage://torrent/" + infoHash
		}
		return savePath
	}
	clean := filepath.Clean(savePath)
	base := filepath.Base(clean)
	if !strings.HasPrefix(base, "pending_") {
		return savePath
	}
	if !strings.Contains(filepath.ToSlash(clean), "/partials/") {
		return savePath
	}
	dest := filepath.Join(filepath.Dir(clean), "btih_"+infoHash)
	if _, err := os.Stat(dest); err == nil {
		return dest
	}
	return savePath
}

func resolveExistingImportPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "storage://") {
		return p
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
		SELECT id, COALESCE(download_id, ''), COALESCE(save_path, ''), COALESCE(wanted_item_id, ''), COALESCE(import_paths, '')
		FROM download_history
		WHERE status = 'import_failed'
		LIMIT 20
	`)
	if err != nil {
		return
	}
	type failed struct {
		id, downloadID, savePath, wantedID, importPaths string
	}
	var recs []failed
	for rows.Next() {
		var r failed
		if err := rows.Scan(&r.id, &r.downloadID, &r.savePath, &r.wantedID, &r.importPaths); err != nil {
			continue
		}
		recs = append(recs, r)
	}
	_ = rows.Close()
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
	for _, rec := range recs {
		paths := decodeImportPaths(rec.importPaths)
		if len(paths) == 0 {
			paths = []string{rec.savePath}
		}
		var imported bool
		var used string
		var lastDetail string
		for _, raw := range paths {
			path := resolveExistingImportPath(raw)
			if path == "" {
				lastDetail = "download path missing on disk"
				continue
			}
			req := m.importPathRequest(context.Background(), path, rec.wantedID)
			impCtx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
			resp, err := client.ImportPath(impCtx, req)
			cancel()
			if err != nil {
				lastDetail = classifyImportError(err.Error())
				slog.Warn("retry ImportPath failed", "id", rec.id, "path", path, "error", err)
				continue
			}
			if resp.GetFilesImported() == 0 && resp.GetFilesFound() == 0 {
				lastDetail = "no importable files found"
				slog.Info("retry ImportPath found nothing", "id", rec.id, "path", path)
				continue
			}
			if resp.GetFilesImported() == 0 {
				lastDetail = "no importable files found"
				slog.Warn("retry ImportPath imported nothing", "id", rec.id, "path", path, "found", resp.GetFilesFound(), "skipped", resp.GetFilesSkipped())
				continue
			}
			imported = true
			used = path
			break
		}
		if !imported {
			if lastDetail != "" {
				noteImportFailedDetail(ctx, db, rec.id, lastDetail)
			}
			continue
		}
		if err := finishHistoryStatus(ctx, db, rec.id, "completed", ""); err != nil {
			slog.Warn("mark retried import completed", "id", rec.id, "error", err)
			continue
		}
		m.cleanupWantedPartials(rec.wantedID, used)
		slog.Info("retried import succeeded", "id", rec.id, "title_id", rec.wantedID, "path", used)
	}
}

// retryImportHistoryID retries ImportPath for one download_history row (import_failed or stalled).
func (m *Module) retryImportHistoryID(ctx context.Context, historyID string) (int, error) {
	historyID = strings.TrimSpace(historyID)
	if historyID == "" {
		return 0, fmt.Errorf("history_id required")
	}
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil {
		return 0, fmt.Errorf("not initialized")
	}
	var id, downloadID, savePath, wantedID, importPaths, status string
	err := db.QueryRowContext(ctx, `
		SELECT id, COALESCE(download_id, ''), COALESCE(save_path, ''), COALESCE(wanted_item_id, ''), COALESCE(import_paths, ''), status
		FROM download_history WHERE id = ?
	`, historyID).Scan(&id, &downloadID, &savePath, &wantedID, &importPaths, &status)
	if err != nil {
		return 0, fmt.Errorf("history not found: %w", err)
	}
	_ = downloadID
	if status != "import_failed" && status != "stalled" && status != "sent" {
		return 0, fmt.Errorf("history status %q is not retryable", status)
	}
	if err := m.ensureScanner(ctx); err != nil {
		return 0, fmt.Errorf("scanner unavailable: %w", err)
	}
	client := m.getScannerClient()
	if client == nil {
		return 0, fmt.Errorf("scanner client unavailable")
	}
	paths := decodeImportPaths(importPaths)
	if len(paths) == 0 {
		paths = []string{savePath}
	}
	for _, raw := range paths {
		path := resolveExistingImportPath(raw)
		if path == "" {
			continue
		}
		impCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		resp, err := client.ImportPath(impCtx, m.importPathRequest(ctx, path, wantedID))
		cancel()
		if err != nil {
			detail := classifyImportError(err.Error())
			noteImportFailedDetail(ctx, db, id, detail)
			return 1, fmt.Errorf("ImportPath: %w", err)
		}
		if resp.GetFilesImported() == 0 {
			continue
		}
		if err := finishHistoryStatus(ctx, db, id, "completed", ""); err != nil {
			return 1, err
		}
		m.cleanupWantedPartials(wantedID, path)
		return 1, nil
	}
	noteImportFailedDetail(ctx, db, id, "no importable files found")
	return 1, fmt.Errorf("retry found nothing to import for %s", historyID)
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

func (m *Module) recordDownloadIdentity(ctx context.Context, downloadID, infohash, savePath, fingerprint, importPaths string) {
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
			files_fingerprint = CASE WHEN ? != '' THEN ? ELSE files_fingerprint END,
			import_paths = CASE WHEN ? != '' THEN ? ELSE import_paths END
		WHERE download_id = ?
	`, infohash, infohash, savePath, savePath, fingerprint, fingerprint, importPaths, importPaths, downloadID); err != nil {
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
	defer func() { _ = rows.Close() }()
	keepCands := m.existingPartialCandidates(keepPath)
	keepPath = filepath.Clean(strings.TrimSpace(keepPath))
	if keepPath != "" && keepPath != "." && filepath.IsAbs(keepPath) {
		if abs, err := filepath.Abs(keepPath); err == nil {
			keepPath = abs
		}
		keepCands = append([]string{keepPath}, keepCands...)
	} else if absKeep := m.absoluteDownloadPath(keepPath); absKeep != "" {
		keepCands = append(keepCands, absKeep)
	}
	isKeep := func(p string) bool {
		for _, k := range keepCands {
			if sameFilesystemPath(p, k) {
				return true
			}
		}
		return false
	}
	removePartial := func(p string) {
		p = filepath.Clean(strings.TrimSpace(p))
		if p == "" || p == "." || isKeep(p) {
			return
		}
		if strings.HasPrefix(p, "storage://") {
			return
		}
		if !strings.Contains(filepath.ToSlash(p), "/partials/") {
			return
		}
		if err := os.RemoveAll(p); err != nil {
			slog.Debug("cleanup partial dir", "path", p, "error", err)
		}
	}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			continue
		}
		cands := m.existingPartialCandidates(p)
		if len(cands) == 0 {
			removePartial(m.absoluteDownloadPath(p))
			continue
		}
		for _, c := range cands {
			removePartial(c)
		}
	}
	itemKey := sanitizePartialToken(wantedID)
	if itemKey == "" {
		itemKey = wantedID
	}
	for _, root := range m.partialScanRoots() {
		itemDir := filepath.Join(root, "partials", itemKey)
		entries, err := os.ReadDir(itemDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			removePartial(filepath.Join(itemDir, e.Name()))
		}
		leftover, err := os.ReadDir(itemDir)
		if err == nil && len(leftover) == 0 {
			_ = os.Remove(itemDir)
		}
	}
}

// dropSiblingDownloads removes other in-flight torrents for the same wanted item
// once one grab finishes (delete files / mesh objects). keepDownloadID is retained.
func (m *Module) dropSiblingDownloads(ctx context.Context, wantedID, keepDownloadID string) {
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
	rows, err := db.QueryContext(ctx, `
		SELECT id, COALESCE(download_id, ''), COALESCE(guid, ''), COALESCE(download_url, ''), COALESCE(attempt_loop, 1), COALESCE(downloader_module, '')
		FROM download_history
		WHERE wanted_item_id = ?
		  AND status IN ('sent', 'stalled')
		  AND (? = '' OR COALESCE(download_id, '') != ?)
	`, wantedID, keepDownloadID, keepDownloadID)
	if err != nil {
		slog.Debug("query sibling downloads", "wanted", wantedID, "error", err)
		return
	}
	defer func() { _ = rows.Close() }()
	type sib struct {
		id, downloadID, guid, url, module string
		loop                              int
	}
	var list []sib
	for rows.Next() {
		var s sib
		if err := rows.Scan(&s.id, &s.downloadID, &s.guid, &s.url, &s.loop, &s.module); err != nil {
			continue
		}
		list = append(list, s)
	}
	for _, s := range list {
		if s.downloadID != "" {
			m.removeInflightTorrentDelete(ctx, s.module, s.downloadID, true)
		}
		if err := finishHistoryStatusWhere(ctx, db, s.id, "superseded", "superseded by completed grab", ` AND status IN ('sent', 'stalled')`); err != nil {
			slog.Warn("mark sibling superseded", "id", s.id, "error", err)
			continue
		}
		m.blacklistRelease(ctx, wantedID, releaseAttemptKey(s.guid, s.url), s.loop, "superseded by completed grab")
		slog.Info("dropped sibling download", "wanted", wantedID, "hist", s.id, "download_id", s.downloadID, "keep", keepDownloadID)
	}
}

func (m *Module) removeInflightTorrentDelete(ctx context.Context, moduleID, downloadID string, deleteFiles bool) {
	if downloadID == "" {
		return
	}
	client := m.torrentClientFor(ctx, moduleID)
	if client == nil {
		return
	}
	rmCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, err := client.RemoveTorrent(rmCtx, &cdlv1.RemoveTorrentRequest{TorrentId: downloadID, DeleteFiles: deleteFiles}); err != nil {
		slog.Debug("remove sibling torrent", "id", downloadID, "error", err)
	}
}

func (m *Module) insertDownloadHistory(ctx context.Context, db *sql.DB, histID, itemID, guid, title, indexer string, size, score int64, downloadURL, protocol, downloadID, downloaderModule, savePath string, loop int, now string) error {
	id := parseMagnetIdentity(downloadURL)
	_, err := db.ExecContext(ctx, `
		INSERT INTO download_history (id, wanted_item_id, guid, title, indexer, size, score, download_url, download_protocol, status, sent_at, created_at, download_id, attempt_loop, last_bytes, last_progress_at, infohash, infohash_v2, save_path, files_fingerprint, downloader_module)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'sent', ?, ?, ?, ?, 0, ?, ?, ?, ?, '', ?)`,
		histID, itemID, guid, title, indexer, size, score, downloadURL, protocol,
		now, now, downloadID, loop, now, id.InfoHash, id.InfoHashV2, savePath, downloaderModule,
	)
	return err
}
