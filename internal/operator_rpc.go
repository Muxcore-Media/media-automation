package internal

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	automationv1 "github.com/Muxcore-Media/contracts-automation/muxcore/automation/v1"
)

func (m *Module) RemoveFromQueue(ctx context.Context, req *automationv1.RemoveFromQueueRequest) (*automationv1.RemoveFromQueueResponse, error) {
	id := strings.TrimSpace(req.GetQueueId())
	if id == "" {
		return nil, fmt.Errorf("queue_id required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}
	res, err := m.db.ExecContext(ctx, `DELETE FROM wanted_items WHERE id = ?`, id)
	if err != nil {
		return nil, fmt.Errorf("remove from queue: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, fmt.Errorf("queue item not found: %s", id)
	}
	return &automationv1.RemoveFromQueueResponse{}, nil
}

func (m *Module) ListBlocklist(ctx context.Context, req *automationv1.ListBlocklistRequest) (*automationv1.ListBlocklistResponse, error) {
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil {
		return nil, fmt.Errorf("not initialized")
	}
	page := int(req.GetPage())
	if page < 1 {
		page = 1
	}
	pageSize := int(req.GetPageSize())
	if pageSize < 1 || pageSize > 100 {
		pageSize = 50
	}
	offset := (page - 1) * pageSize

	query := `
		SELECT b.wanted_item_id, b.guid, b.loop, b.reason, b.created_at, COALESCE(w.title, '')
		FROM release_blacklist b
		LEFT JOIN wanted_items w ON w.id = b.wanted_item_id`
	countQuery := `SELECT COUNT(*) FROM release_blacklist b`
	var args []any
	if id := strings.TrimSpace(req.GetWantedItemId()); id != "" {
		query += ` WHERE b.wanted_item_id = ?`
		countQuery += ` WHERE b.wanted_item_id = ?`
		args = append(args, id)
	}
	var total int
	_ = db.QueryRowContext(ctx, countQuery, args...).Scan(&total)
	query += ` ORDER BY b.created_at DESC LIMIT ? OFFSET ?`
	qargs := append(append([]any{}, args...), pageSize, offset)

	rows, err := db.QueryContext(ctx, query, qargs...)
	if err != nil {
		return nil, fmt.Errorf("list blocklist: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var entries []*automationv1.BlocklistEntry
	for rows.Next() {
		var wantedID, guid, reason, createdAt, title string
		var loop int
		if err := rows.Scan(&wantedID, &guid, &loop, &reason, &createdAt, &title); err != nil {
			continue
		}
		entries = append(entries, &automationv1.BlocklistEntry{
			WantedItemId: wantedID,
			Guid:         guid,
			Loop:         int32(loop),
			Reason:       reason,
			CreatedAt:    createdAt,
			Title:        title,
		})
	}
	return &automationv1.ListBlocklistResponse{
		Entries: entries, Total: int32(total), Page: int32(page), PageSize: int32(pageSize),
	}, nil
}

func (m *Module) ClearBlocklist(ctx context.Context, req *automationv1.ClearBlocklistRequest) (*automationv1.ClearBlocklistResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}

	var (
		res sql.Result
		err error
	)
	switch {
	case req.GetClearAll():
		res, err = m.db.ExecContext(ctx, `DELETE FROM release_blacklist`)
	case req.GetWantedItemId() != "" && req.GetGuid() != "":
		res, err = m.db.ExecContext(ctx,
			`DELETE FROM release_blacklist WHERE wanted_item_id = ? AND guid = ?`,
			req.GetWantedItemId(), req.GetGuid(),
		)
	case req.GetWantedItemId() != "":
		res, err = m.db.ExecContext(ctx,
			`DELETE FROM release_blacklist WHERE wanted_item_id = ?`,
			req.GetWantedItemId(),
		)
	default:
		return nil, fmt.Errorf("wanted_item_id or clear_all required")
	}
	if err != nil {
		return nil, fmt.Errorf("clear blocklist: %w", err)
	}
	n, _ := res.RowsAffected()
	return &automationv1.ClearBlocklistResponse{Removed: int32(n)}, nil
}

func (m *Module) BlocklistRelease(ctx context.Context, req *automationv1.BlocklistReleaseRequest) (*automationv1.BlocklistReleaseResponse, error) {
	wanted := strings.TrimSpace(req.GetWantedItemId())
	guid := strings.TrimSpace(req.GetGuid())
	if wanted == "" || guid == "" {
		return nil, fmt.Errorf("wanted_item_id and guid required")
	}
	reason := strings.TrimSpace(req.GetReason())
	if reason == "" {
		reason = "operator"
	}
	loop := int(req.GetLoop())
	if loop < 1 {
		loop = 1
	}
	m.blacklistRelease(ctx, wanted, guid, loop, reason)
	return &automationv1.BlocklistReleaseResponse{Success: true}, nil
}

func (m *Module) RetryImport(ctx context.Context, req *automationv1.RetryImportRequest) (*automationv1.RetryImportResponse, error) {
	id := strings.TrimSpace(req.GetHistoryId())
	if id != "" {
		n, err := m.retryImportHistoryID(ctx, id)
		if err != nil {
			return nil, err
		}
		return &automationv1.RetryImportResponse{Attempted: int32(n), Message: "ok"}, nil
	}
	m.retryImportFailed(ctx)
	return &automationv1.RetryImportResponse{Attempted: -1, Message: "retried import_failed batch"}, nil
}

func (m *Module) ListDelayProfiles(ctx context.Context, req *automationv1.ListDelayProfilesRequest) (*automationv1.ListDelayProfilesResponse, error) {
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil {
		return nil, fmt.Errorf("not initialized")
	}
	rows, err := db.QueryContext(ctx, `SELECT protocol, wait_minutes FROM delay_profiles ORDER BY protocol`)
	if err != nil {
		return nil, fmt.Errorf("list delay profiles: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var profiles []*automationv1.DelayProfile
	for rows.Next() {
		var protocol string
		var mins int
		if err := rows.Scan(&protocol, &mins); err != nil {
			continue
		}
		profiles = append(profiles, &automationv1.DelayProfile{
			Protocol: protocol, WaitMinutes: int32(mins),
		})
	}
	return &automationv1.ListDelayProfilesResponse{Profiles: profiles}, nil
}

func (m *Module) UpsertDelayProfile(ctx context.Context, req *automationv1.UpsertDelayProfileRequest) (*automationv1.UpsertDelayProfileResponse, error) {
	protocol := strings.ToLower(strings.TrimSpace(req.GetProtocol()))
	if protocol == "" {
		return nil, fmt.Errorf("protocol required")
	}
	mins := req.GetWaitMinutes()
	if mins < 0 {
		return nil, fmt.Errorf("wait_minutes must be >= 0")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db == nil {
		return nil, fmt.Errorf("not initialized")
	}
	if _, err := m.db.ExecContext(ctx, `
		INSERT INTO delay_profiles (protocol, wait_minutes) VALUES (?, ?)
		ON CONFLICT(protocol) DO UPDATE SET wait_minutes = excluded.wait_minutes
	`, protocol, mins); err != nil {
		return nil, fmt.Errorf("upsert delay profile: %w", err)
	}
	return &automationv1.UpsertDelayProfileResponse{
		Profile: &automationv1.DelayProfile{Protocol: protocol, WaitMinutes: mins},
	}, nil
}

func (m *Module) ListCutoffUnmet(ctx context.Context, req *automationv1.ListCutoffUnmetRequest) (*automationv1.ListCutoffUnmetResponse, error) {
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil {
		return nil, fmt.Errorf("not initialized")
	}
	page := int(req.GetPage())
	if page < 1 {
		page = 1
	}
	pageSize := int(req.GetPageSize())
	if pageSize < 1 || pageSize > 100 {
		pageSize = 50
	}

	rows, err := db.QueryContext(ctx, `
		SELECT id, item_type, item_id, title, year, quality_profile_id, current_score
		FROM wanted_items
		WHERE monitored = 1 AND missing = 0
		ORDER BY updated_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list cutoff candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var all []*automationv1.CutoffItem
	for rows.Next() {
		var id, itemType, itemID, title, profileID string
		var year, score int64
		if err := rows.Scan(&id, &itemType, &itemID, &title, &year, &profileID, &score); err != nil {
			continue
		}
		p := m.loadProfileDecision(ctx, profileID)
		if p.CutoffScore <= 0 {
			continue
		}
		if int(score) >= p.CutoffScore {
			continue
		}
		if !p.UpgradeAllowed {
			continue
		}
		all = append(all, &automationv1.CutoffItem{
			QueueId: id, ItemType: itemType, ItemId: itemID, Title: title,
			Year: int32(year), CurrentScore: int32(score), CutoffScore: int32(p.CutoffScore),
			QualityProfileId: profileID,
		})
	}
	total := len(all)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return &automationv1.ListCutoffUnmetResponse{
		Items: all[start:end], Total: int32(total), Page: int32(page), PageSize: int32(pageSize),
	}, nil
}
