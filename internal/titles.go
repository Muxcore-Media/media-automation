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
	"unknown": {}, "eztv": {}, "yify": {}, "yts": {}, "rarbg": {}, "ettv": {},
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

var (
	// Servarr-style: episode/season tokens mean TV, not a movie.
	reTVRelease = regexp.MustCompile(`(?i)(?:^|[.\s\-_\[(])(?:S\d{1,2}(?:[.\s\-_]E\d{1,3})?|\d{1,2}x\d{1,3}|Season[.\s\-_]+\d{1,2}|(?:complete[.\s\-_]+)?(?:season|series)[.\s\-_](?:pack|complete)|(?:EP|E)\d{2,4})`)
	reMovieYear = regexp.MustCompile(`(?:^|[.\s\-_])((?:19|20)\d{2})(?:[.\s\-_.]|$)`)
)

func releaseLooksLikeTV(name string) bool {
	return reTVRelease.MatchString(name)
}

func releaseLooksLikeMovie(name string) bool {
	if releaseLooksLikeTV(name) {
		return false
	}
	return reMovieYear.MatchString(name)
}

// ambiguousSearchAlias reports TMDB/scene codes that Radarr/Sonarr exclude from
// automatic search (BB, BrBa). Primary titles are never ambiguous.
func ambiguousSearchAlias(primary, alias string) bool {
	p := cleanMatchTitle(primary)
	a := cleanMatchTitle(alias)
	if a == "" || a == p {
		return false
	}
	if !isMostlyLatin(a) {
		return false
	}
	compact := strings.ReplaceAll(a, " ", "")
	primaryWords := strings.Fields(p)
	if len([]rune(compact)) <= 3 {
		return true
	}
	if len([]rune(compact)) <= 5 && len(strings.Fields(a)) == 1 && len(primaryWords) >= 2 {
		for _, w := range primaryWords {
			if w == a {
				return false
			}
		}
		return true
	}
	return false
}

func isMostlyLatin(s string) bool {
	letters := 0
	latin := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			letters++
			if r <= unicode.MaxASCII {
				latin++
			}
		}
	}
	return letters > 0 && latin*2 >= letters
}

func usableSearchTitles(primary string, titles []string) []string {
	if len(titles) == 0 {
		return titles
	}
	out := make([]string, 0, len(titles))
	for _, t := range titles {
		if ambiguousSearchAlias(primary, t) {
			continue
		}
		out = append(out, t)
	}
	if len(out) == 0 && primary != "" {
		return []string{cleanMatchTitle(primary)}
	}
	return out
}

func releaseMatchesTitles(releaseTitle string, cleanTitles []string, year int) bool {
	return releaseMatchesWanted(releaseTitle, "", cleanTitles, year)
}

func releaseMatchesWanted(releaseTitle, itemType string, cleanTitles []string, year int) bool {
	if len(cleanTitles) == 0 {
		// no titles configured — keep legacy behavior (do not reject)
		return true
	}
	switch itemType {
	case "movie":
		if releaseLooksLikeTV(releaseTitle) {
			return false
		}
	case "tv":
		if releaseLooksLikeMovie(releaseTitle) && !releaseLooksLikeTV(releaseTitle) {
			return false
		}
	}
	cleanRel := cleanMatchTitle(releaseTitle)
	if cleanRel == "" {
		return false
	}
	relTok := strings.Fields(cleanRel)
	checkYear := year
	if itemType == "tv" {
		// Episode air years after Sxx (King of the Hill S15 2025) are not the series year.
		// A year glued to the title (Franklin.2024.S01) *is* the series year — reject mismatches.
		checkYear = 0
	}
	if !releaseYearCompatible(relTok, checkYear) {
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
			if itemType == "tv" && year > 0 {
				if adj := titleAdjacentYear(relTok, wantTok); adj > 0 && adj != year {
					continue
				}
			}
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

// titleAdjacentYear is the 19xx/20xx token immediately after the matched title
// (Franklin.2024.S01). Zero if the next token is not a year.
func titleAdjacentYear(rel, want []string) int {
	idx := indexPhrase(rel, want)
	if idx < 0 {
		return 0
	}
	after := idx + len(want)
	if after >= len(rel) {
		return 0
	}
	y, err := strconv.Atoi(rel[after])
	if err != nil || y < 1900 || y > 2099 {
		return 0
	}
	return y
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
	addAlt := func(s string) {
		if ambiguousSearchAlias(title, s) {
			return
		}
		add(s)
	}

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
			addAlt(t.GetTitle())
			addAlt(t.GetCleanTitle())
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
			addAlt(t.GetTitle())
			addAlt(t.GetCleanTitle())
		}
	}
	return out
}
