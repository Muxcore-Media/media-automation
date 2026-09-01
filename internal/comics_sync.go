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
	"strings"
	"time"
)

type missingComicItem struct {
	IssueID    string `json:"issue_id"`
	SeriesID   string `json:"series_id"`
	Title      string `json:"title"`
	Number     string `json:"number"`
	Year       int32  `json:"year"`
	SeriesName string `json:"series_name"`
}

type missingComicsResponse struct {
	Items    []missingComicItem `json:"items"`
	Total    int                `json:"total"`
	Page     int                `json:"page"`
	PageSize int                `json:"page_size"`
}

func (m *Module) syncWantedComics(ctx context.Context, seen map[string]struct{}) (int, error) {
	base, err := m.libraryHTTPBase(ctx, "media.comics")
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
			return totalUpserted, fmt.Errorf("comics missing: HTTP %d: %s", resp.StatusCode, string(body))
		}
		var payload missingComicsResponse
		if err := json.Unmarshal(body, &payload); err != nil {
			return totalUpserted, fmt.Errorf("decode comics missing: %w", err)
		}
		for _, item := range payload.Items {
			if seen != nil {
				seen["comic:"+item.IssueID] = struct{}{}
			}
			title := strings.TrimSpace(item.SeriesName)
			if n := strings.TrimSpace(item.Number); n != "" {
				if title != "" {
					title += " #" + n
				} else {
					title = "#" + n
				}
			}
			if t := strings.TrimSpace(item.Title); t != "" {
				if title != "" {
					title += " - " + t
				} else {
					title = t
				}
			}
			m.upsertWanted(ctx, wantedEntry{
				ItemType: "comic", ItemID: item.IssueID,
				Title: title, Year: item.Year,
				SeriesID: item.SeriesID, IssueNumber: strings.TrimSpace(item.Number),
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

func (m *Module) logComicsSync(n int, ok bool, err error) {
	if err != nil {
		slog.Debug("sync missing comics", "error", err)
		return
	}
	if ok {
		slog.Debug("sync missing comics upserted", "issues", n)
	}
}
