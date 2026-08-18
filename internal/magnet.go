package internal

import (
	"encoding/base32"
	"encoding/hex"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/Muxcore-Media/core/pkg/contracts"
)

type magnetIdentity struct {
	InfoHash   string
	InfoHashV2 string
	Display    string
	Trackers   []string
	Webseeds   []string
}

func parseMagnetIdentity(uri string) magnetIdentity {
	var id magnetIdentity
	uri = strings.TrimSpace(uri)
	if uri == "" || !strings.HasPrefix(strings.ToLower(uri), "magnet:") {
		return id
	}
	u, err := url.Parse(uri)
	if err != nil {
		return parseMagnetIdentityLoose(uri)
	}
	q := u.Query()
	id.Display = strings.TrimSpace(q.Get("dn"))
	id.Trackers = uniqueKeepOrder(q["tr"])
	id.Webseeds = uniqueKeepOrder(q["ws"])
	for _, xt := range q["xt"] {
		applyMagnetXT(&id, xt)
	}
	if id.InfoHash == "" && id.InfoHashV2 == "" {
		return parseMagnetIdentityLoose(uri)
	}
	return id
}

func parseMagnetIdentityLoose(uri string) magnetIdentity {
	var id magnetIdentity
	lower := strings.ToLower(uri)
	if i := strings.Index(lower, "xt=urn:btih:"); i >= 0 {
		applyMagnetXT(&id, takeMagnetToken(uri[i+3:])) // skip "xt="
	}
	if i := strings.Index(lower, "xt=urn:btmh:"); i >= 0 {
		applyMagnetXT(&id, takeMagnetToken(uri[i+3:]))
	}
	if i := strings.Index(lower, "dn="); i >= 0 {
		dn := takeMagnetToken(uri[i+3:])
		if decoded, err := url.QueryUnescape(dn); err == nil {
			dn = decoded
		}
		id.Display = dn
	}
	return id
}

func takeMagnetToken(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "&"); i >= 0 {
		s = s[:i]
	}
	return s
}

func applyMagnetXT(id *magnetIdentity, xt string) {
	xt = strings.TrimSpace(xt)
	lower := strings.ToLower(xt)
	switch {
	case strings.HasPrefix(lower, "urn:btih:"):
		if h := normalizeBTIH(xt[len("urn:btih:"):]); h != "" && id.InfoHash == "" {
			id.InfoHash = h
		}
	case strings.HasPrefix(lower, "urn:btmh:"):
		if h := normalizeBTMH(xt[len("urn:btmh:"):]); h != "" && id.InfoHashV2 == "" {
			id.InfoHashV2 = h
		}
	}
}

func normalizeBTIH(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimRight(raw, "/")
	if raw == "" {
		return ""
	}
	if hex40 := strings.ToLower(raw); len(hex40) == 40 && isHex(hex40) {
		return hex40
	}
	enc := strings.ToUpper(strings.TrimRight(raw, "="))
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enc)
	if err != nil || len(decoded) != 20 {
		return strings.ToLower(raw)
	}
	return hex.EncodeToString(decoded)
}

func normalizeBTMH(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	raw = strings.TrimRight(raw, "/")
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "1220") && len(raw) == 68 && isHex(raw) {
		return raw[4:]
	}
	if len(raw) == 64 && isHex(raw) {
		return raw
	}
	return raw
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' {
			continue
		}
		return false
	}
	return true
}

func identitiesMatch(a, b magnetIdentity) bool {
	if a.InfoHash != "" && a.InfoHash == b.InfoHash {
		return true
	}
	if a.InfoHashV2 != "" && a.InfoHashV2 == b.InfoHashV2 {
		return true
	}
	return false
}

func mergeMagnets(urls []string) string {
	var (
		btih, btmh, dn string
		trackers       []string
		webseeds       []string
	)
	for _, u := range urls {
		id := parseMagnetIdentity(u)
		if id.InfoHash == "" && id.InfoHashV2 == "" && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(u)), "magnet:") {
			continue
		}
		if btih == "" {
			btih = id.InfoHash
		}
		if btmh == "" {
			btmh = id.InfoHashV2
		}
		if dn == "" {
			dn = id.Display
		}
		trackers = append(trackers, id.Trackers...)
		webseeds = append(webseeds, id.Webseeds...)
	}
	if btih == "" && btmh == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("magnet:?")
	first := true
	add := func(k, v string) {
		if v == "" {
			return
		}
		if !first {
			b.WriteByte('&')
		}
		first = false
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(v)
	}
	if btih != "" {
		add("xt", "urn:btih:"+btih)
	}
	if btmh != "" {
		add("xt", "urn:btmh:1220"+btmh)
	}
	if dn != "" {
		add("dn", url.QueryEscape(dn))
	}
	for _, tr := range uniqueKeepOrder(trackers) {
		add("tr", tr)
	}
	for _, ws := range uniqueKeepOrder(webseeds) {
		add("ws", ws)
	}
	return b.String()
}

func uniqueKeepOrder(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		key := v
		if strings.Contains(v, "://") {
			key = strings.ToLower(v)
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, v)
	}
	return out
}

func filesFingerprint(files []contracts.DownloadEventFile) string {
	if len(files) == 0 {
		return ""
	}
	sizes := make([]string, 0, len(files))
	for _, f := range files {
		if f.Size > 0 {
			sizes = append(sizes, strconv.FormatInt(f.Size, 10))
		}
	}
	if len(sizes) == 0 {
		return ""
	}
	sort.Strings(sizes)
	return strings.Join(sizes, ":")
}

func partialDirIdentity(id magnetIdentity) string {
	if id.InfoHash != "" {
		return "btih_" + id.InfoHash
	}
	if id.InfoHashV2 != "" {
		return "btmh_" + id.InfoHashV2
	}
	return ""
}

func partialSavePath(itemID, identity string) string {
	itemID = sanitizePartialToken(itemID)
	identity = sanitizePartialToken(identity)
	if itemID == "" || identity == "" {
		return ""
	}
	return path.Join("partials", itemID, identity)
}

func sanitizePartialToken(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if len(out) > 80 {
		out = out[:80]
	}
	return strings.Trim(out, "._-")
}
