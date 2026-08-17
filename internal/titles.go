package internal

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	mgmntv1 "github.com/Muxcore-Media/media-movies/proto/mgmntv1"
	tvmgmtv1 "github.com/Muxcore-Media/media-tvshows/proto/tvmgmtv1"
)

var (
	reSpaces           = regexp.MustCompile(`\s+`)
	reRemainderRes     = regexp.MustCompile(`^\d{3,4}[pi]$`)
	reRemainderSeason  = regexp.MustCompile(`^(?:s\d{1,2}(?:e\d{1,3})?|e\d{1,3}|\d{1,2}x\d{1,3})$`)
	reRemainderBitrate = regexp.MustCompile(`^\d{2,3}mbps$`)
)

// Tokens that may immediately follow a matched title in a scene/P2P release name.
// Extra story-title words ("and", character names) are intentionally excluded.
var remainderTokens = map[string]struct{}{
	"bluray": {}, "bdrip": {}, "brrip": {}, "bd": {}, "remux": {},
	"webrip": {}, "webdl": {}, "web": {}, "dl": {}, "hdtv": {}, "hdrip": {},
	"dvdrip": {}, "dvd": {}, "dvdr": {}, "pdtv": {}, "dsrip": {}, "satrip": {},
	"cam": {}, "ts": {}, "tc": {}, "r5": {}, "screener": {}, "telesync": {},
	"telecine": {}, "workprint": {}, "uhd": {}, "hdr": {}, "hdr10": {},
	"hdr10plus": {}, "dolby": {}, "vision": {}, "dv": {}, "sdr": {},
	"10bit": {}, "8bit": {}, "hevc": {}, "x264": {}, "x265": {}, "h264": {},
	"h265": {}, "avc": {}, "xvid": {}, "divx": {}, "av1": {}, "aac": {},
	"ac3": {}, "dts": {}, "truehd": {}, "atmos": {}, "flac": {}, "mp3": {},
	"opus": {}, "4k": {}, "hd": {}, "sd": {},
	"complete": {}, "season": {}, "seasons": {}, "pack": {}, "series": {},
	"collection": {}, "boxed": {}, "boxset": {}, "disc": {}, "disk": {},
	"proper": {}, "repack": {}, "internal": {}, "limited": {}, "unrated": {},
	"extended": {}, "directors": {}, "cut": {}, "theatrical": {}, "imax": {},
	"criterion": {}, "multi": {}, "dual": {}, "audio": {}, "subs": {}, "sub": {},
	"dubbed": {}, "english": {}, "eng": {}, "french": {}, "spanish": {},
	"german": {}, "italian": {}, "japanese": {}, "korean": {}, "chinese": {},
	"russian": {}, "latin": {}, "latino": {}, "hindi": {}, "nordic": {},
	"readnfo": {}, "nfo": {}, "sample": {}, "hybrid": {},
	"amzn": {}, "nf": {}, "dsnp": {}, "hmax": {}, "atvp": {}, "pcok": {},
	"hulu": {}, "itunes": {}, "webcap": {},
	"cartoon": {}, "animated": {}, "mkv": {}, "mp4": {}, "avi": {}, "m4v": {},
	"format": {}, "quality": {}, "high": {},
}

var prefixFillers = map[string]struct{}{
	"the": {}, "a": {}, "an": {}, "of": {},
}

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

func releaseMatchesTitles(releaseTitle string, cleanTitles []string, year int) bool {
	if len(cleanTitles) == 0 {
		// no titles configured — keep legacy behavior (do not reject)
		return true
	}
	cleanRel := cleanMatchTitle(releaseTitle)
	if cleanRel == "" {
		return false
	}
	relTok := strings.Fields(cleanRel)
	if !releaseYearCompatible(relTok, year) {
		return false
	}
	for _, want := range cleanTitles {
		want = strings.TrimSpace(want)
		if want == "" {
			continue
		}
		wantTok := strings.Fields(want)
		if len(wantTok) == 0 {
			continue
		}
		if phraseMatchesRelease(relTok, wantTok) {
			return true
		}
	}
	return false
}

func phraseMatchesRelease(rel, want []string) bool {
	idx := indexPhrase(rel, want)
	if idx < 0 {
		return false
	}
	for _, tok := range rel[:idx] {
		if !isRemainderToken(tok) && !isPrefixFiller(tok) {
			return false
		}
	}
	after := idx + len(want)
	if after < len(rel) && !isRemainderToken(rel[after]) {
		return false
	}
	return true
}

func indexPhrase(hay, needle []string) int {
	if len(needle) == 0 || len(hay) < len(needle) {
		return -1
	}
	for i := 0; i <= len(hay)-len(needle); i++ {
		ok := true
		for j := range needle {
			if hay[i+j] != needle[j] {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

func isPrefixFiller(tok string) bool {
	_, ok := prefixFillers[tok]
	return ok
}

func isRemainderToken(tok string) bool {
	if tok == "" {
		return false
	}
	if _, ok := remainderTokens[tok]; ok {
		return true
	}
	if reRemainderRes.MatchString(tok) || reRemainderSeason.MatchString(tok) || reRemainderBitrate.MatchString(tok) {
		return true
	}
	if y, err := strconv.Atoi(tok); err == nil && y >= 1900 && y <= 2099 {
		return true
	}
	return false
}

func releaseYearCompatible(tokens []string, year int) bool {
	if year <= 0 {
		return true
	}
	found := false
	matched := false
	for _, tok := range tokens {
		y, err := strconv.Atoi(tok)
		if err != nil || y < 1900 || y > 2099 {
			continue
		}
		found = true
		if y == year {
			matched = true
		}
	}
	if !found {
		return true
	}
	return matched
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
