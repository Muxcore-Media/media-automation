package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func libraryPlusImportRoute(itemType, itemID string) (capability, path string, ok bool) {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return "", "", false
	}
	switch strings.ToLower(strings.TrimSpace(itemType)) {
	case "book":
		return "media.books", "/api/books/" + url.PathEscape(itemID) + "/import", true
	case "comic":
		return "media.comics", "/api/issues/" + url.PathEscape(itemID) + "/import", true
	case "audiobook":
		return "media.audiobooks", "/api/audiobooks/" + url.PathEscape(itemID) + "/import", true
	default:
		return "", "", false
	}
}

func (m *Module) wantedItemType(ctx context.Context, itemID string) string {
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil || strings.TrimSpace(itemID) == "" {
		return ""
	}
	var itemType string
	if err := db.QueryRowContext(ctx, `SELECT item_type FROM wanted_items WHERE item_id = ? LIMIT 1`, itemID).Scan(&itemType); err != nil {
		return ""
	}
	return itemType
}

func (m *Module) importLibraryPlusCompleted(ctx context.Context, itemType, itemID string, targets []string) (int, error) {
	cap, path, ok := libraryPlusImportRoute(itemType, itemID)
	if !ok {
		return 0, nil
	}
	base, err := m.libraryHTTPBase(ctx, cap)
	if err != nil {
		return 0, err
	}
	imported := 0
	for _, filePath := range targets {
		filePath = strings.TrimSpace(filePath)
		if filePath == "" {
			continue
		}
		if err := postLibraryImport(ctx, base, path, filePath); err != nil {
			return imported, err
		}
		imported++
	}
	return imported, nil
}

func postLibraryImport(ctx context.Context, base, path, filePath string) error {
	u, err := url.Parse(strings.TrimRight(base, "/") + path)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{"path": filePath})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("library import HTTP %d: %s", resp.StatusCode, msg)
	}
	return nil
}
