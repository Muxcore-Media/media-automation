package internal

import "strings"

// normalizeProtocol maps indexer/downloader protocol strings to canonical values.
func normalizeProtocol(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	switch p {
	case "", "http", "https":
		return "torrent"
	case "magnet", "torrent", "bt":
		return "torrent"
	case "nzb", "usenet", "newznab":
		return "usenet"
	default:
		return p
	}
}

func isTorrentProtocol(p string) bool {
	switch normalizeProtocol(p) {
	case "torrent":
		return true
	default:
		return false
	}
}

func isUsenetProtocol(p string) bool {
	return normalizeProtocol(p) == "usenet"
}

func detectProtocolFromURL(raw string) string {
	u := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case u == "":
		return "torrent"
	case strings.HasPrefix(u, "magnet:"):
		return "torrent"
	case strings.HasSuffix(u, ".torrent"):
		return "torrent"
	case strings.HasSuffix(u, ".nzb"):
		return "usenet"
	case strings.Contains(u, "/nzb") || strings.Contains(u, "mode=addurl"):
		return "usenet"
	default:
		return "torrent"
	}
}
