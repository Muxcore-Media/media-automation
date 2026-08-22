package internal

import (
	"os"
	"strconv"
	"strings"
)

const (
	defaultUsenetGrabBonus    = 8
	defaultDeadTorrentPenalty = 45
)

func usenetGrabBonus() int {
	if v := os.Getenv("AUTOMATION_USENET_GRAB_BONUS"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
			return n
		}
	}
	return defaultUsenetGrabBonus
}

func deadTorrentPenalty() int {
	if v := os.Getenv("AUTOMATION_DEAD_TORRENT_PENALTY"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
			return n
		}
	}
	return defaultDeadTorrentPenalty
}

// refineGrabRanking adjusts scores for cross-protocol acquisition decisions.
// Higher grab rank wins when pickNextRelease walks the sorted list.
//
// Rules:
//   - Dead torrents (0 seeders) sink — usenet often wins at equal quality.
//   - Small usenet bonus for missing items — instant grab path (0 delay profile).
//   - Upgrades still require beating current_score via decideGrab; ranking only
//     orders candidates within a search pass.
func refineGrabRanking(scored []scoredRelease, missing bool) []scoredRelease {
	if len(scored) == 0 {
		return scored
	}
	bonus := usenetGrabBonus()
	deadPen := deadTorrentPenalty()
	out := make([]scoredRelease, len(scored))
	copy(out, scored)
	for i := range out {
		rank := out[i].Score
		proto := normalizeProtocol(out[i].DownloadProtocol)
		if isTorrentProtocol(proto) && out[i].Seeders == 0 {
			rank -= deadPen
		}
		if missing && isUsenetProtocol(proto) && bonus > 0 {
			rank += bonus
		}
		out[i].GrabRank = rank
	}
	sortByGrabRankDesc(out)
	return out
}

func sortByGrabRankDesc(scored []scoredRelease) {
	for i := 0; i < len(scored); i++ {
		for j := i + 1; j < len(scored); j++ {
			if scored[j].GrabRank > scored[i].GrabRank ||
				(scored[j].GrabRank == scored[i].GrabRank && scored[j].Score > scored[i].Score) {
				scored[i], scored[j] = scored[j], scored[i]
			}
		}
	}
}
