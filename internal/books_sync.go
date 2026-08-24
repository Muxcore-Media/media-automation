package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type missingBookItem struct {
	BookID     string `json:"book_id"`
	AuthorID   string `json:"author_id"`
	Title      string `json:"title"`
	AuthorName string `json:"author_name"`
	Year       int32  `json:"year"`
}

type missingBooksResponse struct {
	Items    []missingBookItem `json:"items"`
	Total    int               `json:"total"`
	Page     int               `json:"page"`
	PageSize int               `json:"page_size"`
}

func (m *Module) ensureBooksHTTP(ctx context.Context) (string, error) {
	return m.libraryHTTPBase(ctx, "media.books")
}

func (m *Module) syncWantedBooks(ctx context.Context, seen map[string]struct{}) (int, error) {
	base, err := m.ensureBooksHTTP(ctx)
	if err != nil {
		return 0, err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	page := 1
	totalUpserted := 0
	for {
		u, err := url.Parse(base)
		if err != nil {
			return totalUpserted, err
		}
		u.Path = "/api/missing"
		q := u.Query()
		q.Set("page", strconv.Itoa(page))
		q.Set("page_size", "100")
		u.RawQuery = q.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return totalUpserted, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return totalUpserted, err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if err != nil {
			return totalUpserted, err
		}
		if resp.StatusCode != http.StatusOK {
			return totalUpserted, fmt.Errorf("books missing: HTTP %d: %s", resp.StatusCode, string(body))
		}
		var payload missingBooksResponse
		if err := json.Unmarshal(body, &payload); err != nil {
			return totalUpserted, fmt.Errorf("decode books missing: %w", err)
		}
		for _, item := range payload.Items {
			key := "book:" + item.BookID
			if seen != nil {
				seen[key] = struct{}{}
			}
			title := item.Title
			if item.AuthorName != "" {
				title = item.AuthorName + " - " + item.Title
			}
			m.upsertWanted(ctx, wantedEntry{
				ItemType: "book", ItemID: item.BookID,
				Title: title, Year: item.Year,
				SeriesID: item.AuthorID,
			})
			totalUpserted++
		}
		if page*payload.PageSize >= payload.Total || len(payload.Items) == 0 {
			break
		}
		page++
	}
	return totalUpserted, nil
}

func (m *Module) logBooksSync(n int, ok bool, err error) {
	if err != nil {
		slog.Debug("sync missing books", "error", err)
		return
	}
	if ok {
		slog.Debug("sync missing books upserted", "books", n)
	}
}
