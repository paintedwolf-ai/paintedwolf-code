package webresearch

import (
	"context"
	"math"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/tools"
)

// FetchBudget tracks soft session and per-host caps for fetch_url network fetches.
// Counters live in process memory (reset on sidecar restart). Cache hits do not
// acquire. Direct crawl is out of scope.
type FetchBudget struct {
	limits FetchURLLimits

	mu       sync.Mutex
	sessions map[string]*sessionFetchWindow
	hosts    map[string]*hostFetchGate
}

type sessionFetchWindow struct {
	reservations []*fetchReservation
}

type fetchReservation struct{ started time.Time }

type hostFetchGate struct {
	sem       chan struct{}
	lastStart time.Time
	minGap    time.Duration
}

// NewFetchBudget builds a budget tracker for the given limits.
func NewFetchBudget(limits FetchURLLimits) *FetchBudget {
	defaults := DefaultFetchURLLimits()
	if limits.SessionMaxFetches <= 0 {
		limits.SessionMaxFetches = defaults.SessionMaxFetches
	}
	if limits.SessionWindow <= 0 {
		limits.SessionWindow = defaults.SessionWindow
	}
	if limits.HostMaxInFlight <= 0 {
		limits.HostMaxInFlight = defaults.HostMaxInFlight
	}
	return &FetchBudget{
		limits:   limits,
		sessions: make(map[string]*sessionFetchWindow),
		hosts:    make(map[string]*hostFetchGate),
	}
}

var (
	globalFetchBudgetMu sync.RWMutex
	globalFetchBudget   = NewFetchBudget(DefaultFetchURLLimits())
)

// SetGlobalFetchBudget installs the process-wide fetch_url budget (serve wiring).
func SetGlobalFetchBudget(b *FetchBudget) {
	if b == nil {
		b = NewFetchBudget(DefaultFetchURLLimits())
	}
	globalFetchBudgetMu.Lock()
	defer globalFetchBudgetMu.Unlock()
	globalFetchBudget = b
}

// GlobalFetchBudget returns the process-wide fetch_url budget.
func GlobalFetchBudget() *FetchBudget {
	globalFetchBudgetMu.RLock()
	defer globalFetchBudgetMu.RUnlock()
	return globalFetchBudget
}

// Acquire reserves a session slot and waits for a host in-flight slot.
// Call release when the network fetch finishes (success or error).
func (b *FetchBudget) Acquire(ctx context.Context, sessionID, pageURL string) (release func(), err error) {
	if b == nil {
		return func() {}, nil
	}
	host := budgetHostKey(pageURL)
	if host == "" {
		return func() {}, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = "_anon"
	}

	reservation, err := b.reserveSession(sessionID)
	if err != nil {
		return nil, err
	}
	gate := b.hostGate(host)
	select {
	case gate.sem <- struct{}{}:
	case <-ctx.Done():
		b.refundSession(sessionID, reservation)
		return nil, ctx.Err()
	}
	release = sync.OnceFunc(func() { <-gate.sem })
	if err := b.waitHostStart(ctx, gate); err != nil {
		release()
		b.refundSession(sessionID, reservation)
		return nil, err
	}
	return release, nil
}

// Admit one start per interval, rechecking after every wait because sibling
// fetches can become ready together. Cancellation consumes no start or budget.
func (b *FetchBudget) waitHostStart(ctx context.Context, gate *hostFetchGate) error {
	for {
		b.mu.Lock()
		if err := ctx.Err(); err != nil {
			b.mu.Unlock()
			return err
		}
		now := time.Now()
		delay := gate.lastStart.Add(gate.minGap).Sub(now)
		if delay <= 0 {
			gate.lastStart = now
			b.mu.Unlock()
			return nil
		}
		b.mu.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (b *FetchBudget) reserveSession(sessionID string) (*fetchReservation, error) {
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	w := b.sessions[sessionID]
	if w == nil {
		w = &sessionFetchWindow{}
		b.sessions[sessionID] = w
	}
	cutoff := now.Add(-b.limits.SessionWindow)
	kept := w.reservations[:0]
	for _, t := range w.reservations {
		if t.started.After(cutoff) {
			kept = append(kept, t)
		}
	}
	w.reservations = kept
	if len(w.reservations) >= b.limits.SessionMaxFetches {
		firstExpiry := w.reservations[0].started.Add(b.limits.SessionWindow)
		for _, reserved := range w.reservations[1:] {
			if expiry := reserved.started.Add(b.limits.SessionWindow); expiry.Before(firstExpiry) {
				firstExpiry = expiry
			}
		}
		return nil, &tools.ToolReject{
			Code: "FETCH_URL_BUDGET_EXCEEDED",
			Data: map[string]any{
				"kind":                             "session",
				"fetch_budget_used":                len(w.reservations),
				"fetch_budget_retry_after_seconds": int(math.Ceil(firstExpiry.Sub(now).Seconds())),
				"max":                              b.limits.SessionMaxFetches,
				"window":                           b.limits.SessionWindow.String(),
				"session_id":                       sessionID,
			},
		}
	}
	reservation := &fetchReservation{started: now}
	w.reservations = append(w.reservations, reservation)
	return reservation, nil
}

func (b *FetchBudget) refundSession(sessionID string, reservation *fetchReservation) {
	b.mu.Lock()
	defer b.mu.Unlock()
	w := b.sessions[sessionID]
	if w == nil || len(w.reservations) == 0 {
		return
	}
	for i, existing := range w.reservations {
		if existing == reservation {
			w.reservations = append(w.reservations[:i], w.reservations[i+1:]...)
			return
		}
	}
}

func (b *FetchBudget) hostGate(host string) *hostFetchGate {
	b.mu.Lock()
	defer b.mu.Unlock()
	g := b.hosts[host]
	if g == nil {
		g = &hostFetchGate{
			sem:    make(chan struct{}, b.limits.HostMaxInFlight),
			minGap: b.limits.HostMinInterval,
		}
		b.hosts[host] = g
	}
	return g
}

func budgetHostKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return ""
		}
		return strings.ToLower(u.Hostname())
	}
	host := strings.ToLower(raw)
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}
