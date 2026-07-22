package internal

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"unicode"

	mgmntv1 "github.com/Muxcore-Media/media-movies/proto/mgmntv1"
	tvmgmtv1 "github.com/Muxcore-Media/media-tvshows/proto/tvmgmtv1"
)

var reSpaces = regexp.MustCompile(`\s+`)

func cleanMatchTitle(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '\'' || r == '`' || r == '´':
		default:
			b.WriteByte(' ')
		}
	}
	s = reSpaces.ReplaceAllString(strings.TrimSpace(b.String()), " ")
	for _, art := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(s, art) {
			s = strings.TrimSpace(s[len(art):])
			break
		}
	}
	return s
}

func releaseMatchesTitles(releaseTitle string, cleanTitles []string) bool {
	if len(cleanTitles) == 0 {
		// no titles configured — keep legacy behavior (do not reject)
		return true
	}
	cleanRel := cleanMatchTitle(releaseTitle)
	if cleanRel == "" {
		return false
	}
	for _, want := range cleanTitles {
		want = strings.TrimSpace(want)
		if want == "" {
			continue
		}
		if cleanRel == want || strings.Contains(cleanRel, want) {
			return true
		}
	}
	return false
}

func encodeCleanTitles(titles []string) string {
	if len(titles) == 0 {
		return "[]"
	}
	b, err := json.Marshal(titles)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func decodeCleanTitles(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func (m *Module) fetchCleanTitles(ctx context.Context, itemType, itemID, seriesID, title string) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(s string) {
		c := cleanMatchTitle(s)
		if c == "" {
			return
		}
		if _, ok := seen[c]; ok {
			return
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	add(title)

	switch itemType {
	case "movie":
		if err := m.ensureMovies(ctx); err != nil {
			return out
		}
		m.mu.RLock()
		cli := m.moviesClient
		m.mu.RUnlock()
		if cli == nil {
			return out
		}
		resp, err := cli.ListAlternateTitles(ctx, &mgmntv1.ListAlternateTitlesRequest{MovieId: itemID})
		if err != nil {
			return out
		}
		for _, t := range resp.GetTitles() {
			add(t.GetTitle())
			add(t.GetCleanTitle())
		}
	case "tv":
		sid := seriesID
		if sid == "" {
			sid = itemID
		}
		if err := m.ensureTV(ctx); err != nil {
			return out
		}
		m.mu.RLock()
		cli := m.tvClient
		m.mu.RUnlock()
		if cli == nil {
			return out
		}
		resp, err := cli.ListAlternateTitles(ctx, &tvmgmtv1.ListAlternateTitlesRequest{SeriesId: sid})
		if err != nil {
			return out
		}
		for _, t := range resp.GetTitles() {
			add(t.GetTitle())
			add(t.GetCleanTitle())
		}
	}
	return out
}
