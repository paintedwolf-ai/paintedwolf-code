package history

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/scopedstore"
)

const (
	// attemptResumeRows counts appended rows, so ordinal gaps alone never unlock work.
	attemptResumeRows   = 32
	attemptLedgerSize   = 1024
	maxTransientRetries = 3
)

var transientRetryDelays = [maxTransientRetries]time.Duration{5 * time.Second, 30 * time.Second, 120 * time.Second}

type attemptOutcome string

const (
	attemptNoReduction   attemptOutcome = "no_reduction"
	attemptDeterministic attemptOutcome = "deterministic_failure"
	attemptTransient     attemptOutcome = "transient_failure"
)

// classifyAttemptFailure reads typed failures only. Anything else waits for a
// relevant change: an unrecognized failure must not start a timer loop.
func classifyAttemptFailure(err error) attemptOutcome {
	if empty, ok := failure.AsProviderEmptyCompletion(err); ok {
		if empty.Retryable && !empty.OutputLimitReached() {
			return attemptTransient
		}
		return attemptDeterministic
	}
	if _, interrupted := failure.AsProviderResponseInterrupted(err); interrupted {
		return attemptTransient
	}
	for _, transient := range []error{
		failure.ErrProviderRateLimited, failure.ErrProviderOverloaded, failure.ErrProviderServer,
		failure.ErrProviderUnreachable, failure.ErrProviderSilent,
		llm.ErrLiteUnavailable, llm.ErrLiteBusy, context.DeadlineExceeded,
	} {
		if errors.Is(err, transient) {
			return attemptTransient
		}
	}
	return attemptDeterministic
}

// attemptSource is the transcript position an attempt read, compared through the
// store's own edit and rewind checks. Later appends are progress, not changes.
type attemptSource struct {
	ord int64
	id  string
	seq int64
}

// attemptRecord adds retry control to the durable memo of the last rejected page.
// It is rebuilt by the first attempt after a restart, when that memo still spares
// the summarizer call.
type attemptRecord struct {
	key                 string
	source              attemptSource
	candidateThroughOrd int64
	intentID            string
	outcome             attemptOutcome
	reason              string
	retries             int
	retryAt             time.Time
}

// attemptPass is the identity captured before a scan. An empty key means the
// identity was unavailable, so the pass runs and leaves no record.
type attemptPass struct {
	key      string
	source   attemptSource
	retries  int
	explicit bool
}

type attemptClock interface {
	Now() time.Time
	AfterFunc(time.Duration, func()) (stop func() bool)
}

type systemAttemptClock struct{}

func (systemAttemptClock) Now() time.Time { return time.Now() }
func (systemAttemptClock) AfterFunc(d time.Duration, f func()) func() bool {
	return time.AfterFunc(d, f).Stop
}

// attemptLedger holds one bounded record and at most one retry timer per session.
type attemptLedger struct {
	clock   attemptClock
	records *scopedstore.LRU[attemptRecord]
	mu      sync.Mutex
	stopped bool
	timers  map[string]*attemptTimer
}

type attemptTimer struct{ stop func() bool }

func newAttemptLedger() *attemptLedger {
	return &attemptLedger{clock: systemAttemptClock{}, records: scopedstore.New[attemptRecord](attemptLedgerSize), timers: make(map[string]*attemptTimer)}
}

func (l *attemptLedger) arm(sessionID string, delay time.Duration, fire func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return
	}
	l.disarmLocked(sessionID)
	timer := &attemptTimer{}
	l.timers[sessionID] = timer
	timer.stop = l.clock.AfterFunc(delay, func() {
		l.mu.Lock()
		live := l.timers[sessionID] == timer
		if live {
			delete(l.timers, sessionID)
		}
		l.mu.Unlock()
		if live {
			fire()
		}
	})
}

func (l *attemptLedger) disarmLocked(sessionID string) {
	if timer := l.timers[sessionID]; timer != nil {
		timer.stop()
		delete(l.timers, sessionID)
	}
}

func (l *attemptLedger) disarm(sessionID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.disarmLocked(sessionID)
}

func (l *attemptLedger) forget(sessionID string) {
	l.disarm(sessionID)
	l.records.Delete(sessionID)
}

func (l *attemptLedger) stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stopped = true
	for sessionID := range l.timers {
		l.disarmLocked(sessionID)
	}
}

// pendingAttemptMemo holds a synthesis rejection until its completion passes the
// fence, so a stale run cannot replace a newer memo.
type pendingAttemptMemo struct {
	messageview.CompactionAttemptStore
	pending *messageview.CompactionAttempt
}

func (p *pendingAttemptMemo) PutCompactionAttempt(_ context.Context, _ string, attempt messageview.CompactionAttempt) error {
	p.pending = &attempt
	return nil
}
