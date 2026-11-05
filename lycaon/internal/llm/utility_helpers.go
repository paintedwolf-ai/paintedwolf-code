package llm

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"strings"
	"sync"
	"time"
)

const defaultUtilityCallTimeout = 2 * time.Minute

// utilityBudgetExceededError labels an attempt the utility budget ended, so
// logs tell it apart from a caller's own deadline.
type utilityBudgetExceededError struct{ cause error }

func (e *utilityBudgetExceededError) Error() string {
	return fmt.Sprintf("utility call budget exceeded: %v", e.cause)
}

func (e *utilityBudgetExceededError) Unwrap() error { return e.cause }

func resolveUtilityCallTimeout(profile providerprofile.Profile, class UtilityClass) time.Duration {
	if class == UtilityClassBackground && profile.BackgroundUtilityCallTimeout > 0 {
		return profile.BackgroundUtilityCallTimeout
	}
	if profile.UtilityCallTimeout > 0 {
		return profile.UtilityCallTimeout
	}
	return defaultUtilityCallTimeout
}

// UtilityClass is the product decision for one lite call when the slot is
// down or the attempt fails. The coordinator turn is never a utility class.
type UtilityClass string

const (
	// UtilityClassGift is a nicety (session title, draft project name). Lite
	// down leaves the field unset; the coordinator is not spent on it.
	UtilityClassGift UtilityClass = "gift"
	// UtilityClassQuality improves a projection the turn can already compute
	// (curate). Lite down falls back to the coordinator, then a
	// deterministic truncate.
	UtilityClassQuality UtilityClass = "quality"
	// UtilityClassRequested is a user-asked artifact (commit draft, file
	// briefing). Lite down falls back to the coordinator, then a surfaced error.
	UtilityClassRequested UtilityClass = "requested"
	// UtilityClassOverlay is a background hint (verify detect, warm seed).
	// Lite down skips the work and does not cache a negative result.
	UtilityClassOverlay UtilityClass = "overlay"
	// UtilityClassBackground improves a projection off the latency path.
	// Failure is returned to the calling subsystem, which keeps durable state
	// unchanged and may retry later; it never spends the coordinator.
	UtilityClassBackground UtilityClass = "background"
)

// ClassForPurpose maps a capture-row purpose onto its product class.
func ClassForPurpose(purpose string) UtilityClass {
	switch strings.TrimSpace(purpose) {
	case "session_title", "project_name":
		return UtilityClassGift
	case "commit_draft", "file_briefing", "approval_rationale":
		return UtilityClassRequested
	case "verify_detect", "warm_seed":
		return UtilityClassOverlay
	case "compaction":
		return UtilityClassBackground
	case "curate":
		return UtilityClassQuality
	default:
		return UtilityClassQuality
	}
}

// AllowsCoordinatorFallback reports whether a failed or skipped lite attempt
// may spend the coordinator model.
func (c UtilityClass) AllowsCoordinatorFallback() bool {
	return c == UtilityClassQuality || c == UtilityClassRequested
}

// AllowsTruncateFallback reports whether a deterministic truncation may stand
// in for a model summary.
func (c UtilityClass) AllowsTruncateFallback() bool {
	return c == UtilityClassQuality
}

// ReturnsFailureToCaller preserves background errors for transactional handling.
func (c UtilityClass) ReturnsFailureToCaller() bool {
	return c == UtilityClassBackground
}

// UtilityNamer names one project or session.
type UtilityNamer interface {
	Name(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// PlaneNamer names through the utility plane.
type PlaneNamer struct {
	Summarizer *RegistrySummarizer
}

// Name sends a gift-class utility request.
func (n PlaneNamer) Name(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	if n.Summarizer == nil {
		return "", ErrLiteUnavailable
	}
	n.Summarizer.Class = UtilityClassGift
	// Completion tokens include reasoning even when visible output is a title.
	name, err := n.Summarizer.SummarizeRequired(ctx, systemPrompt, userPrompt, 1024)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	empty, _ := failure.AsProviderEmptyCompletion(err)
	if errors.Is(err, failure.ErrProviderOutputTruncated) || empty.OutputLimitReached() {
		return n.Summarizer.SummarizeRequired(ctx, systemPrompt, userPrompt, 4096)
	}
	return name, err
}

// Namer returns a gift-class namer on this service's plane.
func (s *Service) Namer(purpose, projectDir, sessionID, projectID string) UtilityNamer {
	if s == nil {
		return nil
	}
	sum := s.BindSummarizer(&RegistrySummarizer{
		Scope:      SettingsScopeProject,
		ProjectID:  projectID,
		ProjectDir: projectDir,
		Purpose:    purpose,
		Class:      UtilityClassGift,
	})
	if sessionID != "" {
		sum.Cost = nil // session cost is attached by the caller when needed
	}
	return PlaneNamer{Summarizer: sum}
}

// OverlayLaneWait bounds opportunistic utility admission.
const OverlayLaneWait = 10 * time.Second
const backgroundIdleWait = 30 * time.Second

type utilityLanes struct {
	mu          sync.Mutex
	lanes       map[string]*singleLane
	overlayWait time.Duration
}

type singleLane struct {
	busy              bool
	generation        uint64
	backgroundCancel  context.CancelFunc
	backgroundReady   time.Time
	foregroundWaiting int
	changed           chan struct{}
}

func newUtilityLanes() *utilityLanes {
	return &utilityLanes{lanes: make(map[string]*singleLane)}
}

func (l *utilityLanes) overlayWaitOrDefault() time.Duration {
	if l != nil && l.overlayWait > 0 {
		return l.overlayWait
	}
	return OverlayLaneWait
}

func (l *utilityLanes) acquire(ctx context.Context, providerID string, singleFlight bool, class UtilityClass) (func(), error) {
	wait := ctx
	if class == UtilityClassOverlay {
		var cancel context.CancelFunc
		wait, cancel = context.WithTimeout(ctx, l.overlayWaitOrDefault())
		defer cancel()
	}
	release, err := l.admit(wait, providerID, singleFlight, false, nil)
	if err != nil && ctx.Err() == nil && class == UtilityClassOverlay {
		return nil, ErrLiteBusy
	}
	return release, err
}

func (l *utilityLanes) acquireForeground(ctx context.Context, providerID string, singleFlight bool) (func(), error) {
	return l.admit(ctx, providerID, singleFlight, true, nil)
}

func (l *utilityLanes) admit(ctx context.Context, providerID string, singleFlight, foreground bool, preempt context.CancelFunc) (func(), error) {
	if l == nil || !singleFlight || providerID == "" {
		return func() {}, nil
	}
	l.mu.Lock()
	lane := l.lanes[providerID]
	if lane == nil {
		lane = &singleLane{changed: make(chan struct{})}
		l.lanes[providerID] = lane
	}
	if foreground {
		lane.foregroundWaiting++
		if lane.backgroundCancel != nil && ctx.Err() == nil {
			lane.backgroundCancel()
			lane.backgroundCancel = nil
			lane.busy = false
			lane.notify()
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			if foreground {
				lane.foregroundWaiting--
				lane.notify()
			}
			l.mu.Unlock()
			return nil, err
		}
		idleWait := time.Duration(0)
		if preempt != nil {
			idleWait = time.Until(lane.backgroundReady)
		}
		if !lane.busy && (foreground || lane.foregroundWaiting == 0) && idleWait <= 0 {
			if foreground {
				lane.foregroundWaiting--
			}
			lane.busy = true
			lane.generation++
			generation := lane.generation
			lane.backgroundCancel = preempt
			l.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					l.mu.Lock()
					if lane.generation == generation {
						lane.busy = false
						lane.backgroundCancel = nil
						if foreground {
							lane.backgroundReady = time.Now().Add(backgroundIdleWait)
						}
						lane.notify()
					}
					l.mu.Unlock()
				})
			}, nil
		}
		changed := lane.changed
		var idle <-chan time.Time
		var timer *time.Timer
		if !lane.busy && idleWait > 0 {
			timer = time.NewTimer(idleWait)
			idle = timer.C
		}
		l.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
		case <-idle:
		}
		if timer != nil {
			timer.Stop()
		}
		l.mu.Lock()
	}
}

// notify broadcasts a state change while the admission lock is held.
func (lane *singleLane) notify() {
	close(lane.changed)
	lane.changed = make(chan struct{})
}

// acquireBackground allows foreground work to cancel a dispensable projection.
func (l *utilityLanes) acquireBackground(ctx context.Context, providerID string, singleFlight bool) (context.Context, func(), error) {
	work, cancel := context.WithCancel(ctx)
	release, err := l.admit(work, providerID, singleFlight, false, cancel)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return work, func() { cancel(); release() }, nil
}
