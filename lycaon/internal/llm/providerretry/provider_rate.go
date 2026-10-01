package providerretry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/filelock"
	"github.com/lycaon/lycaon/internal/fseffect"
)

// ProviderRatePolicy is shared by every caller in the declared provider bucket.
type ProviderRatePolicy struct {
	Adaptive          bool   `yaml:"adaptive" json:"adaptive"`
	Scope             string `yaml:"scope" json:"scope"`
	InitialMs         int    `yaml:"initial_ms" json:"initial_ms"`
	MaxBackoffMs      int    `yaml:"max_backoff_ms" json:"max_backoff_ms"`
	RecoverySuccesses int    `yaml:"recovery_successes" json:"recovery_successes"`
}

func (p ProviderRatePolicy) validate() error {
	if p.Scope != "provider" && p.Scope != "model" {
		return fmt.Errorf("http_retry.rate_limit.scope must be provider or model")
	}
	if p.InitialMs <= 0 || p.MaxBackoffMs < p.InitialMs || p.MaxBackoffMs > 86400000 || p.RecoverySuccesses < 1 {
		return fmt.Errorf("http_retry.rate_limit requires positive initial_ms, recovery_successes, and max_backoff_ms between initial_ms and one day")
	}
	return nil
}

type providerRateState struct {
	Version          int                  `json:"version"`
	ProviderID       string               `json:"provider_id"`
	Model            string               `json:"model,omitempty"`
	Policy           ProviderRatePolicy   `json:"policy"`
	Generation       uint64               `json:"generation"`
	Limited          int64                `json:"rate_limits"`
	Admitted         int64                `json:"admitted"`
	Accepted         int64                `json:"accepted"`
	ServerRetryUntil int64                `json:"server_retry_until_ms"`
	CooldownUntil    int64                `json:"cooldown_until_ms"`
	NextAdmission    int64                `json:"next_admission_ms"`
	PenaltyMs        int                  `json:"penalty_ms"`
	SpacingMs        int                  `json:"spacing_ms"`
	Successes        int                  `json:"successes"`
	UpdatedAt        int64                `json:"updated_at_ms"`
	Windows          []providerRateWindow `json:"minute_windows"`
}

// ProviderRateGate coordinates HTTP attempts without replaying completed model work.
// A directory shares the same state machine across isolated application processes.
type ProviderRateGate struct {
	mu        sync.Mutex
	states    map[string]providerRateState
	directory string
	now       func() time.Time
	jitter    func(int) int
	sleep     func(context.Context, time.Duration) error
}

func NewProviderRateGate(directory string) *ProviderRateGate {
	return &ProviderRateGate{states: make(map[string]providerRateState), directory: directory, now: time.Now,
		jitter: func(window int) int { return window/2 + rand.Intn(window-window/2+1) }, // #nosec G404 -- retry scheduling only.
		sleep:  SleepHTTPRetry}
}

var directProviderRateGate = NewProviderRateGate("")

type Admission struct {
	gate       *ProviderRateGate
	key        string
	provider   string
	model      string
	policy     ProviderRatePolicy
	generation uint64
}

func (a ProviderAttempt) Admission(ctx context.Context) (*Admission, error) {
	if a.Policy.RateLimit == nil {
		return nil, nil
	}
	gate := a.Policy.rateGate
	if gate == nil {
		gate = directProviderRateGate
	}
	origin := a.Policy.rateOrigin
	if origin == "" {
		origin = a.ProviderID
	}
	model := ""
	if a.Policy.RateLimit.Scope == "model" {
		model = a.Model
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(origin+"\x00"+model)))
	lease := &Admission{gate: gate, key: key, provider: a.ProviderID, model: model, policy: *a.Policy.RateLimit}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var delay time.Duration
		err := lease.update(ctx, func(s *providerRateState, now time.Time) {
			if a.Policy.RateLimit.Adaptive || a.Policy.Availability != nil {
				deadline := max(s.CooldownUntil, s.NextAdmission)
				if a.Policy.Availability != nil {
					deadline = max(deadline, s.ServerRetryUntil)
				}
				delay = time.UnixMilli(deadline).Sub(now)
			}
			if delay <= 0 {
				s.Admitted++
				s.window(now).Admitted++
				lease.generation = s.Generation
				s.NextAdmission = now.UnixMilli() + int64(s.SpacingMs)
			}
		})
		if err != nil {
			return nil, err
		}
		if delay <= 0 {
			return lease, nil
		}
		status := http.StatusTooManyRequests
		if a.Policy.Availability != nil {
			status = 0
		}
		ObserveRetry(ctx, RetryAttempt{Attempt: 1, Wait: delay, Status: status, Reason: RetryReasonCooldown})
		if err := gate.sleep(ctx, delay); err != nil {
			return nil, err
		}
	}
}

func (a *Admission) update(ctx context.Context, change func(*providerRateState, time.Time)) error {
	return a.gate.update(ctx, a.key, func(s *providerRateState) error {
		if s.Version == 0 {
			*s = providerRateState{Version: 1, ProviderID: a.provider, Model: a.model, Policy: a.policy}
		}
		if s.Version != 1 || s.Policy != a.policy {
			return fmt.Errorf("provider rate state does not match the configured policy")
		}
		now := a.gate.now()
		change(s, now)
		s.UpdatedAt = now.UnixMilli()
		return nil
	})
}

func (a *Admission) Limited(ctx context.Context, headers http.Header, retry ProviderHTTPRetry) error {
	if a == nil {
		return nil
	}
	return a.update(ctx, func(s *providerRateState, now time.Time) {
		s.Limited++
		s.window(now).Limited++
		if server, ok := headerWait(retry.WaitHeaders, headers); ok {
			s.ServerRetryUntil = max(s.ServerRetryUntil, now.Add(server).UnixMilli())
		}
		if !a.policy.Adaptive {
			return
		}
		// Concurrent rejections from one admission generation are one congestion wave.
		if a.generation == s.Generation {
			s.PenaltyMs = min(a.policy.MaxBackoffMs, max(a.policy.InitialMs, s.PenaltyMs*2))
			s.Generation++
		}
		delay := time.Duration(a.gate.jitter(s.PenaltyMs)) * time.Millisecond
		if server, ok := headerWait(retry.WaitHeaders, headers); ok {
			delay = max(delay, server)
		}
		s.CooldownUntil = max(s.CooldownUntil, now.Add(delay).UnixMilli())
		s.SpacingMs = max(s.SpacingMs, s.PenaltyMs/2)
		s.Successes = 0
	})
}

func (a *Admission) Succeeded(ctx context.Context) error {
	if a == nil {
		return nil
	}
	return a.update(ctx, func(s *providerRateState, now time.Time) {
		s.Accepted++
		s.window(now).Accepted++
		// An older in-flight success cannot undo a newer rejection or server deadline.
		if a.generation != s.Generation || now.UnixMilli() < s.CooldownUntil || s.PenaltyMs == 0 {
			return
		}
		s.Successes++
		if s.Successes < a.policy.RecoverySuccesses {
			return
		}
		s.Successes = 0
		s.PenaltyMs /= 2
		s.SpacingMs /= 2
		if s.PenaltyMs < a.policy.InitialMs {
			s.PenaltyMs = 0
			s.SpacingMs = 0
		}
	})
}

// Minute windows retain recent admission evidence, including observe-only providers.
type providerRateWindow struct {
	StartMs  int64 `json:"start_ms"`
	Admitted int64 `json:"admitted"`
	Accepted int64 `json:"accepted"`
	Limited  int64 `json:"rate_limits"`
}

func (s *providerRateState) window(now time.Time) *providerRateWindow {
	start := now.Truncate(time.Minute).UnixMilli()
	cutoff := start - int64(time.Hour/time.Millisecond)
	for len(s.Windows) > 0 && s.Windows[0].StartMs <= cutoff {
		s.Windows = s.Windows[1:]
	}
	for i := range s.Windows {
		if s.Windows[i].StartMs == start {
			return &s.Windows[i]
		}
	}
	// Bound storage even if the host wall clock moves backwards.
	if len(s.Windows) >= 60 {
		s.Windows = s.Windows[1:]
	}
	s.Windows = append(s.Windows, providerRateWindow{StartMs: start})
	return &s.Windows[len(s.Windows)-1]
}

func (a *Admission) unavailable(ctx context.Context, delay time.Duration) error {
	return a.update(ctx, func(s *providerRateState, now time.Time) {
		s.Generation++
		s.CooldownUntil = max(s.CooldownUntil, now.Add(delay).UnixMilli())
	})
}

// ProviderAvailabilityRetry waits for transport availability until cancellation.
type ProviderAvailabilityRetry struct {
	InitialMs    int `yaml:"initial_ms"`
	MaxBackoffMs int `yaml:"max_backoff_ms"`
}

func (p ProviderAvailabilityRetry) validate() error {
	if p.InitialMs <= 0 || p.MaxBackoffMs < p.InitialMs || p.MaxBackoffMs > 86400000 {
		return fmt.Errorf("http_retry.availability requires positive initial_ms and max_backoff_ms between initial_ms and one day")
	}
	return nil
}

func (p ProviderHTTPRetry) waitsForAvailability(f Fault) bool {
	if p.Availability == nil {
		return false
	}
	switch f.Kind {
	case FaultUnreachable, FaultSilent:
		return p.TransportPolicy().Covers(f.Kind)
	case FaultRateLimited, FaultCapacity:
		return p.AllowsStatus(f.Status)
	case FaultStatus:
		return f.Status >= 500 && p.AllowsStatus(f.Status)
	default:
		return false
	}
}

func (p ProviderHTTPRetry) availabilityWait(attempt int, f Fault) time.Duration {
	policy := p.Availability
	delay := time.Duration(policy.InitialMs) * time.Millisecond
	ceiling := time.Duration(policy.MaxBackoffMs) * time.Millisecond
	for n := 0; n < attempt && delay < ceiling; n++ {
		delay = min(delay*2, ceiling)
	}
	// Jitter spreads recovery probes without shortening server deadlines.
	delay = delay/2 + time.Duration(rand.Int63n(int64(delay-delay/2)+1)) // #nosec G404
	if server, ok := headerWait(p.PolicyForStatus(f.Status).WaitHeaders, f.Header); ok {
		delay = max(delay, server)
	}
	return delay
}

func (g *ProviderRateGate) update(ctx context.Context, key string, change func(*providerRateState) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if g.directory != "" {
		return g.updateFile(ctx, key, change)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	state := g.states[key]
	if err := change(&state); err != nil {
		return err
	}
	g.states[key] = state
	return nil
}

func (g *ProviderRateGate) updateFile(ctx context.Context, key string, change func(*providerRateState) error) error {
	if err := os.MkdirAll(g.directory, 0o700); err != nil {
		return err
	}
	lock, err := filelock.Open(filepath.Join(g.directory, key+".lock"))
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		held, err := filelock.TryExclusive(lock)
		if err != nil {
			return err
		}
		if held {
			break
		}
		if err := SleepHTTPRetry(ctx, 25*time.Millisecond); err != nil {
			return err
		}
	}
	defer func() { _ = filelock.Unlock(lock) }()
	path := filepath.Join(g.directory, key+".json")
	var state providerRateState
	body, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(body, &state); err != nil {
			return fmt.Errorf("read provider rate state: %w", err)
		}
		if state.Version != 1 {
			return fmt.Errorf("unsupported provider rate state version %d", state.Version)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := change(&state); err != nil {
		return err
	}
	body, err = json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{Location: fseffect.Location{Root: g.directory, Rel: key + ".json"}, Source: bytes.NewReader(body), Mode: 0o600})
	return err
}
