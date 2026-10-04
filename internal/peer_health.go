package internal

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// peerHealth scores peer modules (indexers, downloaders) from a rolling window
// of recent RPC outcomes and benches peers that keep failing so callers can
// skip them for a cooldown and fail over to the next candidate.
//
// The zero value is ready to use.
type peerHealth struct {
	mu    sync.Mutex
	peers map[string]*peerState
	// now is overridable in tests.
	now func() time.Time
}

type peerOutcome int

const (
	peerSuccess peerOutcome = iota
	peerFailure
	peerTimeout
)

const (
	peerSampleWindow    = 20
	peerMinObservations = 4
	peerFailurePenalty  = 80.0
	peerTimeoutPenalty  = 140.0
	peerLatencyPenalty  = 15.0
	peerLatencyTarget   = 2 * time.Second
	// peerBenchScore: below this (with enough observations) a peer is benched.
	peerBenchScore    = 50.0
	peerBenchCooldown = 5 * time.Minute
)

type peerObservation struct {
	outcome peerOutcome
	latency time.Duration
}

type peerState struct {
	window       []peerObservation
	next         int
	benchedUntil time.Time
	lastError    string
	lastObserved time.Time
}

// peerHealthReport summarizes one peer for logs and operator views.
type peerHealthReport struct {
	ID           string
	Score        float64
	Observations int
	Benched      bool
	BenchedUntil time.Time
	LastError    string
	LastObserved time.Time
}

func (h *peerHealth) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

func (h *peerHealth) stateLocked(id string) *peerState {
	if h.peers == nil {
		h.peers = make(map[string]*peerState)
	}
	st, ok := h.peers[id]
	if !ok {
		st = &peerState{}
		h.peers[id] = st
	}
	return st
}

// record ingests one RPC outcome for peer id. err == nil is a success; errors
// that say nothing about the peer's health (bad request, not found, rate
// limit, caller cancellation) are ignored.
func (h *peerHealth) record(id string, latency time.Duration, err error) {
	outcome, counts := classifyPeerError(err)
	if id == "" || !counts {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.clock()
	st := h.stateLocked(id)
	if !st.benchedUntil.IsZero() && !now.Before(st.benchedUntil) {
		// Trial call after the cooldown: start the window fresh so one good
		// response un-benches and one bad response re-benches.
		st.window = st.window[:0]
		st.next = 0
		st.benchedUntil = time.Time{}
		if outcome != peerSuccess {
			st.benchedUntil = now.Add(peerBenchCooldown)
		}
	}
	obs := peerObservation{outcome: outcome, latency: latency}
	if len(st.window) < peerSampleWindow {
		st.window = append(st.window, obs)
	} else {
		st.window[st.next] = obs
		st.next = (st.next + 1) % peerSampleWindow
	}
	st.lastObserved = now
	if err != nil {
		st.lastError = err.Error()
	}
	if st.benchedUntil.IsZero() && len(st.window) >= peerMinObservations && scoreWindow(st.window) < peerBenchScore {
		st.benchedUntil = now.Add(peerBenchCooldown)
		slog.Warn("peer benched after repeated failures", "peer", id, "score", scoreWindow(st.window), "until", st.benchedUntil.UTC().Format(time.RFC3339), "last_error", st.lastError)
	}
}

// bench takes a peer out of rotation immediately (e.g. connection refused).
func (h *peerHealth) bench(id string, err error) {
	if id == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.clock()
	st := h.stateLocked(id)
	st.benchedUntil = now.Add(peerBenchCooldown)
	if err != nil {
		st.lastError = err.Error()
	}
	slog.Warn("peer benched", "peer", id, "until", st.benchedUntil.UTC().Format(time.RFC3339), "error", st.lastError)
}

// usable reports whether id may be called now. Unknown peers are usable, and a
// benched peer becomes usable again (for a trial call) once its cooldown ends.
func (h *peerHealth) usable(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	st, ok := h.peers[id]
	if !ok {
		return true
	}
	return st.benchedUntil.IsZero() || !h.clock().Before(st.benchedUntil)
}

// filterUsable returns the usable ids, or all ids when none are usable so a
// caller never ends up with nothing to try.
func (h *peerHealth) filterUsable(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if h.usable(id) {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return ids
	}
	return out
}

func (h *peerHealth) snapshot() []peerHealthReport {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.clock()
	out := make([]peerHealthReport, 0, len(h.peers))
	for id, st := range h.peers {
		out = append(out, peerHealthReport{
			ID:           id,
			Score:        scoreWindow(st.window),
			Observations: len(st.window),
			Benched:      !st.benchedUntil.IsZero() && now.Before(st.benchedUntil),
			BenchedUntil: st.benchedUntil,
			LastError:    st.lastError,
			LastObserved: st.lastObserved,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// scoreWindow maps a window to 0..100: failures and timeouts cost their
// penalty scaled by their share of the window (all-failure scores 0), and slow successes cost up to
// peerLatencyPenalty as average latency grows past peerLatencyTarget.
func scoreWindow(window []peerObservation) float64 {
	if len(window) == 0 {
		return 100
	}
	var failures, timeouts, successes int
	var latency time.Duration
	for _, o := range window {
		switch o.outcome {
		case peerFailure:
			failures++
		case peerTimeout:
			timeouts++
		default:
			successes++
			latency += o.latency
		}
	}
	n := float64(len(window))
	score := 100 - peerFailurePenalty*float64(failures)/n - peerTimeoutPenalty*float64(timeouts)/n
	if successes > 0 {
		avg := latency / time.Duration(successes)
		if avg > peerLatencyTarget {
			over := float64(avg-peerLatencyTarget) / float64(peerLatencyTarget)
			if over > 1 {
				over = 1
			}
			score -= peerLatencyPenalty * over
		}
	}
	if score < 0 {
		return 0
	}
	return score
}

// classifyPeerError maps an RPC error to an outcome and whether it says
// anything about the peer's health.
func classifyPeerError(err error) (peerOutcome, bool) {
	if err == nil {
		return peerSuccess, true
	}
	if errors.Is(err, context.Canceled) {
		return peerFailure, false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return peerTimeout, true
	}
	st, ok := status.FromError(err)
	if !ok {
		return peerFailure, true
	}
	switch st.Code() {
	case codes.DeadlineExceeded:
		return peerTimeout, true
	case codes.Unavailable, codes.Internal, codes.Unknown, codes.DataLoss, codes.Unimplemented:
		return peerFailure, true
	default:
		// InvalidArgument, NotFound, ResourceExhausted, Canceled, ...: the peer
		// answered or the caller gave up; not a health signal.
		return peerFailure, false
	}
}

// isPeerUnreachable reports errors where the request certainly never reached
// the peer, so retrying on another peer cannot duplicate work.
func isPeerUnreachable(err error) bool {
	st, ok := status.FromError(err)
	return ok && st.Code() == codes.Unavailable
}
