package internal

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"time"
)

// HistoryStatusLabel returns a human-readable label for admin UI and APIs.
// Machine-readable status stays in the status column; detail adds context when present.
func HistoryStatusLabel(status, detail string) string {
	status = strings.TrimSpace(status)
	detail = strings.TrimSpace(detail)
	switch status {
	case "sent":
		return "Downloading"
	case "completed":
		return "Completed"
	case "failed":
		if detail != "" {
			return "Download failed: " + detail
		}
		return "Download failed"
	case "import_failed":
		if detail != "" {
			return "Import failed: " + detail
		}
		return "Import failed"
	case "stalled":
		if detail != "" {
			return "Stalled: " + detail
		}
		return "Stalled"
	case "pending":
		return "Pending"
	default:
		if detail != "" {
			return humanizeStatusToken(status) + ": " + detail
		}
		return humanizeStatusToken(status)
	}
}

func humanizeStatusToken(s string) string {
	s = strings.ReplaceAll(s, "_", " ")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// classifyImportError turns scanner/import RPC errors into short operator-facing detail.
func classifyImportError(errMsg string) string {
	msg := strings.ToLower(strings.TrimSpace(errMsg))
	switch {
	case msg == "":
		return "import failed"
	case strings.Contains(msg, "watch director"):
		return "path not under a scanner watch directory"
	case strings.Contains(msg, "not initialized"):
		return "scanner not ready"
	case strings.Contains(msg, "scanner client unavailable"), strings.Contains(msg, "scanner unavailable"):
		return "scanner unavailable"
	case strings.Contains(msg, "context deadline exceeded"), strings.Contains(msg, "timeout"):
		return "import timed out"
	case strings.Contains(msg, "no such file"), strings.Contains(msg, "not exist"):
		return "download path missing on disk"
	case strings.Contains(msg, "imported 0 files"), strings.Contains(msg, "imported nothing"):
		return "no importable files found"
	default:
		if len(errMsg) > 160 {
			return errMsg[:157] + "..."
		}
		return errMsg
	}
}

func finishHistoryStatus(ctx context.Context, db *sql.DB, histID, status, detail string) error {
	if db == nil || histID == "" {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.ExecContext(ctx,
		`UPDATE download_history SET status = ?, status_detail = ?, completed_at = ? WHERE id = ?`,
		status, strings.TrimSpace(detail), now, histID,
	)
	return err
}

func finishHistoryStatusWhere(ctx context.Context, db *sql.DB, histID, status, detail, whereClause string, whereArgs ...any) error {
	if db == nil || histID == "" {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	args := []any{status, strings.TrimSpace(detail), now, histID}
	args = append(args, whereArgs...)
	query := `UPDATE download_history SET status = ?, status_detail = ?, completed_at = ? WHERE id = ?` + whereClause
	_, err := db.ExecContext(ctx, query, args...)
	return err
}

func noteImportFailedDetail(ctx context.Context, db *sql.DB, histID, detail string) {
	if db == nil || histID == "" {
		return
	}
	detail = classifyImportError(detail)
	if _, err := db.ExecContext(ctx,
		`UPDATE download_history SET status_detail = ? WHERE id = ? AND status = 'import_failed'`,
		detail, histID,
	); err != nil {
		slog.Debug("update import_failed detail", "id", histID, "error", err)
	}
}
