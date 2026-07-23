package internal

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

func (m *Module) migrateDelayTables(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS delay_profiles (
			protocol     TEXT PRIMARY KEY,
			wait_minutes INTEGER NOT NULL DEFAULT 0
		)
	`); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS release_seen (
			guid          TEXT PRIMARY KEY,
			first_seen_at TEXT NOT NULL
		)
	`); err != nil {
		return err
	}
	// Seed defaults if empty.
	var n int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM delay_profiles`).Scan(&n)
	if n == 0 {
		_, _ = db.ExecContext(ctx, `INSERT INTO delay_profiles (protocol, wait_minutes) VALUES ('torrent', 15), ('usenet', 0)`)
	}
	return nil
}

func (m *Module) delayMinutesFor(ctx context.Context, protocol string) int {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if protocol == "" {
		protocol = "torrent"
	}
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil {
		return 0
	}
	var mins int
	err := db.QueryRowContext(ctx, `SELECT wait_minutes FROM delay_profiles WHERE protocol = ?`, protocol).Scan(&mins)
	if err != nil {
		// Fallbacks for common aliases.
		if protocol == "magnet" || protocol == "torrent" {
			_ = db.QueryRowContext(ctx, `SELECT wait_minutes FROM delay_profiles WHERE protocol = 'torrent'`).Scan(&mins)
		}
	}
	return mins
}

func (m *Module) noteReleaseSeen(ctx context.Context, guid string) time.Time {
	if guid == "" {
		return time.Now().UTC()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db == nil {
		return time.Now().UTC()
	}
	var raw string
	err := m.db.QueryRowContext(ctx, `SELECT first_seen_at FROM release_seen WHERE guid = ?`, guid).Scan(&raw)
	if err == nil {
		if t, perr := time.Parse(time.RFC3339, raw); perr == nil {
			return t
		}
	}
	now := time.Now().UTC()
	_, _ = m.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO release_seen (guid, first_seen_at) VALUES (?, ?)`,
		guid, now.Format(time.RFC3339),
	)
	return now
}

func (m *Module) delayElapsed(ctx context.Context, guid, protocol string, now time.Time) bool {
	mins := m.delayMinutesFor(ctx, protocol)
	if mins <= 0 {
		return true
	}
	first := m.noteReleaseSeen(ctx, guid)
	return !first.Add(time.Duration(mins) * time.Minute).After(now)
}
