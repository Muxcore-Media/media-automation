package internal

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// seriesOverride holds per-series grab policy (delay + release groups).
type seriesOverride struct {
	SeriesID        string   `json:"series_id"`
	DelayMinutes    *int     `json:"delay_minutes,omitempty"` // nil → protocol default
	PreferredGroups []string `json:"preferred_groups,omitempty"`
	IgnoredGroups   []string `json:"ignored_groups,omitempty"`
}

var reReleaseGroup = regexp.MustCompile(`(?i)-([A-Za-z0-9]+)(?:[\[\(].*)?$`)

func (m *Module) migrateSeriesOverrides(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS series_overrides (
			series_id         TEXT PRIMARY KEY,
			delay_minutes     INTEGER,
			preferred_groups  TEXT NOT NULL DEFAULT '',
			ignored_groups    TEXT NOT NULL DEFAULT '',
			updated_at        TEXT NOT NULL
		)
	`); err != nil {
		return err
	}
	if v := strings.TrimSpace(os.Getenv("AUTOMATION_SERIES_OVERRIDES_JSON")); v != "" {
		_ = m.replaceSeriesOverridesJSON(ctx, db, v)
	}
	return nil
}

func (m *Module) replaceSeriesOverridesJSON(ctx context.Context, db *sql.DB, raw string) error {
	var list []seriesOverride
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return fmt.Errorf("series_overrides_json: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM series_overrides`); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, o := range list {
		id := strings.TrimSpace(o.SeriesID)
		if id == "" {
			continue
		}
		var delay any
		if o.DelayMinutes != nil {
			delay = *o.DelayMinutes
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO series_overrides (series_id, delay_minutes, preferred_groups, ignored_groups, updated_at)
			VALUES (?, ?, ?, ?, ?)
		`, id, delay, joinGroups(o.PreferredGroups), joinGroups(o.IgnoredGroups), now)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func joinGroups(gs []string) string {
	out := make([]string, 0, len(gs))
	for _, g := range gs {
		g = strings.TrimSpace(g)
		if g != "" {
			out = append(out, g)
		}
	}
	return strings.Join(out, ",")
}

func splitGroups(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (m *Module) seriesOverride(ctx context.Context, seriesID string) *seriesOverride {
	seriesID = strings.TrimSpace(seriesID)
	if seriesID == "" {
		return nil
	}
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil {
		return nil
	}
	var delay sql.NullInt64
	var pref, ign string
	err := db.QueryRowContext(ctx, `
		SELECT delay_minutes, preferred_groups, ignored_groups FROM series_overrides WHERE series_id = ?
	`, seriesID).Scan(&delay, &pref, &ign)
	if err != nil {
		return nil
	}
	o := &seriesOverride{
		SeriesID:        seriesID,
		PreferredGroups: splitGroups(pref),
		IgnoredGroups:   splitGroups(ign),
	}
	if delay.Valid {
		n := int(delay.Int64)
		o.DelayMinutes = &n
	}
	return o
}

func (m *Module) listSeriesOverridesJSON(ctx context.Context) (string, error) {
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil {
		return "[]", nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT series_id, delay_minutes, preferred_groups, ignored_groups FROM series_overrides ORDER BY series_id
	`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var list []seriesOverride
	for rows.Next() {
		var id, pref, ign string
		var delay sql.NullInt64
		if err := rows.Scan(&id, &delay, &pref, &ign); err != nil {
			return "", err
		}
		o := seriesOverride{
			SeriesID:        id,
			PreferredGroups: splitGroups(pref),
			IgnoredGroups:   splitGroups(ign),
		}
		if delay.Valid {
			n := int(delay.Int64)
			o.DelayMinutes = &n
		}
		list = append(list, o)
	}
	if list == nil {
		list = []seriesOverride{}
	}
	b, err := json.Marshal(list)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func parseReleaseGroup(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	// Prefer last path segment / basename for multi-file names.
	if i := strings.LastIndexAny(title, "/\\"); i >= 0 {
		title = title[i+1:]
	}
	m := reReleaseGroup.FindStringSubmatch(title)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func groupMatch(group string, list []string) bool {
	if group == "" || len(list) == 0 {
		return false
	}
	g := strings.EqualFold
	for _, x := range list {
		if g(group, x) {
			return true
		}
	}
	return false
}

// applyReleaseGroupOverrides filters ignored groups and boosts preferred ones.
func applyReleaseGroupOverrides(scored []scoredRelease, preferred, ignored []string) []scoredRelease {
	if len(scored) == 0 || (len(preferred) == 0 && len(ignored) == 0) {
		return scored
	}
	filtered := make([]scoredRelease, 0, len(scored))
	for _, r := range scored {
		g := parseReleaseGroup(r.Title)
		if groupMatch(g, ignored) {
			continue
		}
		if groupMatch(g, preferred) {
			r.Score += 500
		}
		filtered = append(filtered, r)
	}
	if len(filtered) == 0 {
		// All ignored — keep original list so we still can grab something.
		return scored
	}
	sortScoredDesc(filtered)
	return filtered
}
