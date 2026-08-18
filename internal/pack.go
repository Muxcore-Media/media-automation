package internal

import (
	"context"
	"regexp"
	"strconv"
	"strings"
)

var (
	reSeasonRange    = regexp.MustCompile(`(?i)S(\d{1,2})\s*[-–]\s*S(\d{1,2})`)
	reEpisodeRange   = regexp.MustCompile(`(?i)S(\d{1,2})E(\d{1,3})\s*[-–]\s*E(\d{1,3})`)
	reSingleEpisode  = regexp.MustCompile(`(?i)S(\d{1,2})E(\d{1,3})`)
	reSeasonWordPack = regexp.MustCompile(`(?i)season[.\s_-]*(\d{1,2})`)
)

// packSeasonsCovered reports the inclusive season span a release title covers.
// Single-episode names (S07E08) are not packs. S01-S05 and S15.Complete are.
func packSeasonsCovered(title string) (lo, hi int, ok bool) {
	title = strings.TrimSpace(title)
	if title == "" {
		return 0, 0, false
	}
	if m := reSeasonRange.FindStringSubmatch(title); len(m) >= 3 {
		a, errA := strconv.Atoi(m[1])
		b, errB := strconv.Atoi(m[2])
		if errA == nil && errB == nil && a > 0 && b > 0 {
			if a > b {
				a, b = b, a
			}
			return a, b, true
		}
	}
	if m := reEpisodeRange.FindStringSubmatch(title); len(m) >= 4 {
		s, err := strconv.Atoi(m[1])
		if err == nil && s > 0 {
			return s, s, true
		}
	}
	if reSingleEpisode.MatchString(title) {
		return 0, 0, false
	}
	if matches := reSeasonInTitle.FindAllStringSubmatch(title, -1); len(matches) >= 2 {
		lo, hi := 0, 0
		for _, m := range matches {
			if len(m) < 2 {
				continue
			}
			s, err := strconv.Atoi(m[1])
			if err != nil || s < 1 {
				continue
			}
			if lo == 0 || s < lo {
				lo = s
			}
			if s > hi {
				hi = s
			}
		}
		if lo > 0 && hi >= lo {
			return lo, hi, true
		}
	}
	if m := reSeasonInTitle.FindStringSubmatch(title); len(m) >= 2 {
		s, err := strconv.Atoi(m[1])
		if err == nil && s > 0 {
			return s, s, true
		}
	}
	if m := reSeasonWordPack.FindStringSubmatch(title); len(m) >= 2 {
		s, err := strconv.Atoi(m[1])
		if err == nil && s > 0 {
			return s, s, true
		}
	}
	return 0, 0, false
}

// parseSingleEpisode returns the first SxxExx in title. Episode ranges and
// season packs are not a single episode; callers should check packSeasonsCovered first.
func parseSingleEpisode(title string) (season, episode int, ok bool) {
	m := reSingleEpisode.FindStringSubmatch(title)
	if len(m) < 3 {
		return 0, 0, false
	}
	s, errS := strconv.Atoi(m[1])
	e, errE := strconv.Atoi(m[2])
	if errS != nil || errE != nil || s < 1 || e < 1 {
		return 0, 0, false
	}
	return s, e, true
}

type packSpan struct{ lo, hi int }

func packCoversSeason(title string, season int) bool {
	if season < 1 {
		return false
	}
	lo, hi, ok := packSeasonsCovered(title)
	return ok && season >= lo && season <= hi
}

func spansCoverSeason(spans []packSpan, season int) bool {
	if season < 1 {
		return false
	}
	for _, s := range spans {
		if season >= s.lo && season <= s.hi {
			return true
		}
	}
	return false
}

func (m *Module) activeSeasonPacks(ctx context.Context) map[string][]packSpan {
	out := map[string][]packSpan{}
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil {
		return out
	}
	rows, err := db.QueryContext(ctx, `
		SELECT COALESCE(w.series_id, ''), h.title
		FROM download_history h
		INNER JOIN wanted_items w ON w.item_id = h.wanted_item_id
		WHERE h.status IN ('sent', 'completed')
		  AND w.item_type = 'tv'
		  AND COALESCE(w.series_id, '') != ''
	`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var seriesID, title string
		if err := rows.Scan(&seriesID, &title); err != nil {
			continue
		}
		lo, hi, ok := packSeasonsCovered(title)
		if !ok {
			continue
		}
		out[seriesID] = append(out[seriesID], packSpan{lo: lo, hi: hi})
	}
	return out
}

// seasonPackAlreadyGrabbed is true when a sent or completed history row for this
// series is a season pack whose title covers the wanted season.
func (m *Module) seasonPackAlreadyGrabbed(ctx context.Context, seriesID string, season int) bool {
	if strings.TrimSpace(seriesID) == "" || season < 1 {
		return false
	}
	return spansCoverSeason(m.activeSeasonPacks(ctx)[seriesID], season)
}

// seriesWithGrabs is the set of series_id values that already have a sent or
// completed download. Season-0 request placeholders must not keep searching
// once the series is already grabbing.
func (m *Module) seriesWithGrabs(ctx context.Context) map[string]struct{} {
	out := map[string]struct{}{}
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil {
		return out
	}
	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT COALESCE(w.series_id, '')
		FROM download_history h
		INNER JOIN wanted_items w ON w.item_id = h.wanted_item_id
		WHERE h.status IN ('sent', 'completed', 'stalled', 'import_failed')
		  AND w.item_type = 'tv'
		  AND COALESCE(w.series_id, '') != ''
	`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var seriesID string
		if err := rows.Scan(&seriesID); err != nil {
			continue
		}
		out[seriesID] = struct{}{}
	}
	return out
}

func isSeasonZeroEpisodeDummy(itemType string, season, episode int) bool {
	return itemType == "tv" && season == 0 && episode >= 1
}

func skipSeasonZeroPlaceholder(itemType string, season, episode int, seriesID string, grabbing map[string]struct{}) bool {
	_ = grabbing
	_ = seriesID
	return isSeasonZeroEpisodeDummy(itemType, season, episode)
}

// coerceTVWantedGrain turns S00E12-style placeholders into a series pack row
// (episode == 0). Real seasons and pack rows are unchanged.
func coerceTVWantedGrain(itemType string, season, episode int32, itemID, seriesID string) (int32, int32, string) {
	if !isSeasonZeroEpisodeDummy(itemType, int(season), int(episode)) {
		return season, episode, itemID
	}
	if strings.TrimSpace(seriesID) != "" {
		itemID = seriesID
	}
	return 0, 0, itemID
}
