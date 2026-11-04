package history

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type virtualClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*virtualTimer
}

type virtualTimer struct {
	at   time.Time
	fire func()
	dead bool
}

func newVirtualClock() *virtualClock { return &virtualClock{now: time.Unix(1_000_000, 0)} }

func (c *virtualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *virtualClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &virtualTimer{at: c.now.Add(d), fire: f}
	c.timers = append(c.timers, timer)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		live := !timer.dead
		timer.dead = true
		return live
	}
}

func (c *virtualClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []*virtualTimer
	for _, timer := range c.timers {
		if !timer.dead && !timer.at.After(c.now) {
			timer.dead = true
			due = append(due, timer)
		}
	}
	c.mu.Unlock()
	for _, timer := range due {
		timer.fire()
	}
}

// pending lists live timers as delays from now.
func (c *virtualClock) pending() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []time.Duration
	for _, timer := range c.timers {
		if !timer.dead {
			out = append(out, timer.at.Sub(c.now))
		}
	}
	return out
}

const (
	unchangedSummary = `{"facts":[%q],"completed":[],"pending":[],"reacquire":[],"constraints":[]}`
	longFact         = "A long retained fact that keeps this page from shrinking at all."
)

// scriptedProvider answers each summarizer call from a script; the last entry repeats.
type scriptedProvider struct {
	mu      sync.Mutex
	calls   int
	script  []func() (string, error)
	entered chan struct{}
	release chan struct{}
}

func (p *scriptedProvider) Summarize(ctx context.Context, _, _ string, _ int) (string, error) {
	p.mu.Lock()
	p.calls++
	step := p.script[min(p.calls, len(p.script))-1]
	entered, release := p.entered, p.release
	p.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return step()
}

func (p *scriptedProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func answerUnproductive() (string, error) {
	return fmt.Sprintf(unchangedSummary, strings.Repeat(longFact, 8)), nil
}

func answerShort() (string, error) { return fmt.Sprintf(unchangedSummary, "Short."), nil }

func failWith(err error) func() (string, error) {
	return func() (string, error) { return "", err }
}

// scanCountingStore counts page loads: admission never reads the published view.
type scanCountingStore struct {
	Store
	mu    sync.Mutex
	scans int
}

func (s *scanCountingStore) GetCompactionView(ctx context.Context, id string) (*store.CompactionView, bool, error) {
	s.mu.Lock()
	s.scans++
	s.mu.Unlock()
	return s.Store.GetCompactionView(ctx, id)
}

func (s *scanCountingStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scans
}

type attemptFixture struct {
	svc      *Service
	mem      *store.Memory
	sess     *api.Session
	clock    *virtualClock
	provider *scriptedProvider
	scans    *scanCountingStore
}

func attemptConfig() compaction.CompactionConfig {
	cfg := compaction.DefaultCompactionConfig()
	cfg.TargetTokens, cfg.KeepRecentMessages, cfg.SessionMinSavingsTokens = 1, 1, 1
	cfg.ChunkTokenThreshold = 1_000_000
	cfg.HardCeilingTokens = 2048 // below the cold-start overhead, so every page is under pressure
	return cfg
}

func newAttemptFixture(t *testing.T, script ...func() (string, error)) *attemptFixture {
	t.Helper()
	cfg := attemptConfig()
	svc, mem := newCompactionManager(t, cfg)
	provider := &scriptedProvider{script: script}
	svc.SetCompactor(compaction.NewSimpleCompactor(cfg, provider))
	clock := newVirtualClock()
	svc.Runner.attempts.clock = clock
	scans := &scanCountingStore{Store: mem}
	svc.store = scans
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	f := &attemptFixture{svc: svc, mem: mem, sess: sess, clock: clock, provider: provider, scans: scans}
	t.Cleanup(func() {
		svc.Runner.Stop()
		testutil.FailErr(t, "drain compaction", svc.Runner.WaitContext(context.Background()))
	})
	return f
}

// seedRejectablePage publishes a continuation as long as the page behind it, so the
// deterministic baseline rejects the page and only a summarizer could shrink it.
func (f *attemptFixture) seedRejectablePage(t *testing.T) {
	t.Helper()
	prior := &messageview.Continuation{Format: messageview.ContinuationFormat, Window: 200_000, ThroughOrd: 1, SummaryThroughOrd: 1}
	for range 8 {
		prior.Summary.Facts = append(prior.Summary.Facts, strings.Repeat("earlier clue ", 12))
	}
	text, err := compaction.RenderContinuation(t.Context(), prior, false, 0)
	testutil.FailErr(t, "render continuation", err)
	summary := api.Message{ID: "previous", Role: api.MessageRoleAssistant, Content: text,
		ContentParts: []api.MessageContentPart{{Source: "compaction_summary", Content: text}}}
	testutil.FailErr(t, "append covered row", f.mem.AppendMessages(t.Context(), f.sess.ID, summary))
	rows, err := f.mem.GetMessages(t.Context(), f.sess.ID)
	testutil.FailErr(t, "read covered row", err)
	covered := rows[len(rows)-1]
	summary.Ord = covered.Ord
	testutil.FailErr(t, "publish view", f.mem.PutCompactionView(t.Context(), f.sess.ID, store.CompactionView{
		Generation: 1, CoveredThroughOrd: covered.Ord, CoveredThroughID: covered.ID, SourceSeq: covered.Seq,
		Continuation: prior, Messages: []api.Message{summary}}))
	testutil.FailErr(t, "append page", f.mem.AppendMessages(t.Context(), f.sess.ID,
		api.Message{ID: "observation", Role: api.MessageRoleAssistant, Content: strings.Repeat("new clue ", 12)},
		api.Message{ID: "request", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Continue."}))
}

// schedule runs one background pass and whatever synthesis it started.
func (f *attemptFixture) schedule(t *testing.T) {
	t.Helper()
	sess, err := f.mem.Get(t.Context(), f.sess.ID)
	testutil.FailErr(t, "read session", err)
	f.svc.Schedule(t.Context(), sess)
	f.settle(t)
}

func (f *attemptFixture) settle(t *testing.T) {
	t.Helper()
	for range 3 {
		f.svc.Runner.Wait()
		testutil.FailErr(t, "wait for synthesis", f.svc.Runner.Synthesis.WaitContext(t.Context()))
	}
}

func (f *attemptFixture) appendRows(t *testing.T, n int) {
	t.Helper()
	rows := make([]api.Message, n)
	for i := range rows {
		rows[i] = api.Message{Role: api.MessageRoleAssistant, Content: "progress"}
	}
	testutil.FailErr(t, "append rows", f.mem.AppendMessages(t.Context(), f.sess.ID, rows...))
}

func (f *attemptFixture) record(t *testing.T) attemptRecord {
	t.Helper()
	record, found := f.svc.Runner.attempts.records.Load(f.sess.ID)
	if !found {
		t.Fatal("no attempt record")
	}
	return record
}

func TestUnchangedNoReductionSkipsScanAndSummarizerAC_COMP_01(t *testing.T) {
	cases := []struct {
		name       string
		seed       func(*testing.T, *attemptFixture)
		wantReason string
		wantCalls  int
	}{
		{"rejected page", func(t *testing.T, f *attemptFixture) { f.seedRejectablePage(t) }, "insufficient_savings", 1},
		{"protected history under pressure", func(t *testing.T, f *attemptFixture) {
			testutil.FailErr(t, "append active turn", f.mem.AppendMessages(t.Context(), f.sess.ID, append(
				[]api.Message{{ID: "opener", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Finish it."}},
				turnExchange(0, strings.Repeat("observation ", 4000))...)...))
		}, "protected_history", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAttemptFixture(t, answerUnproductive)
			tc.seed(t, f)
			f.schedule(t)
			record := f.record(t)
			if record.outcome != attemptNoReduction || record.reason != tc.wantReason || f.provider.count() != tc.wantCalls {
				t.Fatalf("first attempt: %+v calls=%d", record, f.provider.count())
			}
			scans := f.scans.count()
			for range 5 {
				f.schedule(t)
			}
			f.appendRows(t, attemptResumeRows-1)
			f.schedule(t)
			if got := f.scans.count(); got != scans || f.provider.count() != tc.wantCalls {
				t.Fatalf("suppressed work ran: scans %d -> %d, calls %d", scans, got, f.provider.count())
			}
		})
	}

	t.Run("ordinal gaps alone do not unlock it", func(t *testing.T) {
		f := newAttemptFixture(t, answerUnproductive)
		f.seedRejectablePage(t)
		f.schedule(t)
		record := f.record(t)
		scans := f.scans.count()
		f.appendRows(t, 2*attemptResumeRows)
		rows, err := f.mem.GetMessages(t.Context(), f.sess.ID)
		testutil.FailErr(t, "read rows", err)
		_, err = f.mem.TruncateMessagesFrom(t.Context(), f.sess.ID, rows[len(rows)-2*attemptResumeRows].ID)
		testutil.FailErr(t, "remove appended rows", err)
		f.appendRows(t, 3)
		rows, err = f.mem.GetMessages(t.Context(), f.sess.ID)
		testutil.FailErr(t, "read rows after gap", err)
		if gap := rows[len(rows)-1].Ord - record.source.ord; gap < attemptResumeRows {
			t.Fatalf("fixture has no ordinal gap: %d", gap)
		}
		f.schedule(t)
		if got := f.scans.count(); got != scans || f.provider.count() != 1 {
			t.Fatalf("ordinal gap unlocked work: scans %d -> %d, calls %d", scans, got, f.provider.count())
		}
	})
}

func TestRelevantChangeResumesSuppressedWorkAC_COMP_02(t *testing.T) {
	cases := []struct {
		name   string
		change func(*testing.T, *attemptFixture)
	}{
		{"32 appended rows", func(t *testing.T, f *attemptFixture) { f.appendRows(t, attemptResumeRows) }},
		{"new human instruction", func(t *testing.T, f *attemptFixture) {
			testutil.FailErr(t, "append instruction", f.mem.AppendMessages(t.Context(), f.sess.ID,
				api.Message{ID: "next", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Now this."}))
		}},
		{"edited eligible source", func(t *testing.T, f *attemptFixture) {
			msg, err := f.mem.GetMessage(t.Context(), f.sess.ID, "observation")
			testutil.FailErr(t, "read source", err)
			msg.Content = strings.Repeat("edited clue ", 12)
			_, err = f.mem.UpdateMessage(t.Context(), f.sess.ID, "observation", msg)
			testutil.FailErr(t, "edit source", err)
		}},
		{"published coverage", func(t *testing.T, f *attemptFixture) {
			view, _, err := f.mem.GetCompactionView(t.Context(), f.sess.ID)
			testutil.FailErr(t, "read view", err)
			view.Generation++
			testutil.FailErr(t, "publish coverage", f.mem.PutCompactionView(t.Context(), f.sess.ID, *view))
		}},
		{"relevant configuration", func(t *testing.T, f *attemptFixture) {
			cfg := attemptConfig()
			cfg.MinSavingsPct++
			f.svc.SetCompactor(compaction.NewSimpleCompactor(cfg, f.provider))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAttemptFixture(t, answerUnproductive)
			f.seedRejectablePage(t)
			f.schedule(t)
			prior := f.record(t)
			scans := f.scans.count()
			tc.change(t, f)
			f.schedule(t)
			if got := f.scans.count(); got <= scans {
				t.Fatal("relevant change did not resume the scan")
			}
			if record := f.record(t); record.source == prior.source && record.key == prior.key {
				t.Fatalf("resumed work recorded nothing new: %+v", record)
			}
		})
	}

	t.Run("explicit request bypasses suppression without duplicating in-flight work", func(t *testing.T) {
		f := newAttemptFixture(t, answerUnproductive)
		f.seedRejectablePage(t)
		f.schedule(t)
		if f.provider.count() != 1 {
			t.Fatalf("calls = %d", f.provider.count())
		}
		f.provider.mu.Lock()
		f.provider.entered, f.provider.release = make(chan struct{}, 4), make(chan struct{})
		f.provider.mu.Unlock()
		for range 2 {
			_, err := f.svc.ForceCompact(t.Context(), f.sess.ID)
			testutil.FailErr(t, "force compact", err)
		}
		<-f.provider.entered
		close(f.provider.release)
		f.settle(t)
		if f.provider.count() != 2 {
			t.Fatalf("explicit requests made %d further calls, want one in flight", f.provider.count()-1)
		}
	})
}

func TestStaleCompletionLeavesNewerStateAC_COMP_03(t *testing.T) {
	transient := &failure.ProviderOverloadedError{ProviderID: "lite", Status: 503}
	blocked := func(t *testing.T, step func() (string, error)) *attemptFixture {
		t.Helper()
		f := newAttemptFixture(t, step)
		f.provider.entered, f.provider.release = make(chan struct{}, 4), make(chan struct{})
		f.seedRejectablePage(t)
		sess, err := f.mem.Get(t.Context(), f.sess.ID)
		testutil.FailErr(t, "read session", err)
		f.svc.Schedule(t.Context(), sess)
		<-f.provider.entered
		return f
	}
	publishNewer := func(t *testing.T, f *attemptFixture) {
		t.Helper()
		view, _, err := f.mem.GetCompactionView(t.Context(), f.sess.ID)
		testutil.FailErr(t, "read view", err)
		view.Generation++
		testutil.FailErr(t, "publish newer", f.mem.PutCompactionView(t.Context(), f.sess.ID, *view))
	}

	t.Run("newer generation is not penalized", func(t *testing.T) {
		for name, step := range map[string]func() (string, error){"transient failure": failWith(transient), "rejection": answerUnproductive} {
			t.Run(name, func(t *testing.T) {
				f := blocked(t, step)
				publishNewer(t, f)
				close(f.provider.release)
				f.settle(t)
				if _, found := f.svc.Runner.attempts.records.Load(f.sess.ID); found || len(f.clock.pending()) != 0 {
					t.Fatalf("stale work left state: timers=%v", f.clock.pending())
				}
				if _, found, err := f.mem.GetCompactionAttempt(t.Context(), f.sess.ID); err != nil || found {
					t.Fatalf("stale work replaced the memo: found=%v err=%v", found, err)
				}
			})
		}
	})

	t.Run("newer attempt record survives", func(t *testing.T) {
		f := blocked(t, failWith(transient))
		f.appendRows(t, 1)
		sess, err := f.mem.Get(t.Context(), f.sess.ID)
		testutil.FailErr(t, "read session", err)
		sc := f.svc.Compactor.(*compaction.SimpleCompactor)
		control := f.svc.attemptControl()
		newer, err := control.capture(t.Context(), sess, sc)
		testutil.FailErr(t, "capture newer attempt", err)
		testutil.FailErr(t, "settle newer attempt", f.svc.Runner.Execute(t.Context(), f.sess.ID, func(ctx context.Context) error {
			control.settle(ctx, sc, f.sess.ID, newer, attemptResult{outcome: attemptNoReduction, reason: "protected_history"})
			return nil
		}))
		close(f.provider.release)
		f.settle(t)
		if record := f.record(t); record.source != newer.source || record.outcome != attemptNoReduction || len(f.clock.pending()) != 0 {
			t.Fatalf("stale completion overwrote the newer record: %+v timers=%v", record, f.clock.pending())
		}
	})

	t.Run("canceled run records nothing and session eviction releases state", func(t *testing.T) {
		f := blocked(t, failWith(transient))
		f.svc.Runner.CancelSession(f.sess.ID)
		f.settle(t)
		if _, found := f.svc.Runner.attempts.records.Load(f.sess.ID); found || len(f.clock.pending()) != 0 {
			t.Fatal("canceled run left attempt state")
		}
	})

	t.Run("shutdown schedules no further work", func(t *testing.T) {
		f := newAttemptFixture(t, failWith(transient))
		f.seedRejectablePage(t)
		f.schedule(t)
		if len(f.clock.pending()) != 1 {
			t.Fatalf("no retry armed: %v", f.clock.pending())
		}
		f.svc.Runner.Stop()
		f.clock.advance(time.Hour)
		testutil.FailErr(t, "drain", f.svc.Runner.WaitContext(t.Context()))
		if f.provider.count() != 1 || len(f.clock.pending()) != 0 {
			t.Fatalf("work after shutdown: calls=%d timers=%v", f.provider.count(), f.clock.pending())
		}
		f.svc.Runner.attempts.arm(f.sess.ID, time.Second, func() { t.Error("armed after shutdown") })
		f.clock.advance(time.Hour)
	})

	t.Run("reset releases the record", func(t *testing.T) {
		f := newAttemptFixture(t, failWith(transient))
		f.seedRejectablePage(t)
		f.schedule(t)
		f.svc.ForgetSessionState(f.sess.ID)
		if _, found := f.svc.Runner.attempts.records.Load(f.sess.ID); found || len(f.clock.pending()) != 0 {
			t.Fatal("reset kept attempt state")
		}
	})
}

func TestTransientFailureRetriesOnVirtualClockAC_COMP_04(t *testing.T) {
	overloaded := func(after time.Duration) error {
		return &failure.ProviderOverloadedError{ProviderID: "lite", Status: 503, RetryAfter: after}
	}

	t.Run("three retries at 5, 30, 120 without new messages", func(t *testing.T) {
		f := newAttemptFixture(t, failWith(overloaded(0)))
		f.seedRejectablePage(t)
		f.schedule(t)
		for retry, delay := range transientRetryDelays {
			calls := f.provider.count()
			f.schedule(t)
			f.clock.advance(delay - time.Millisecond)
			f.settle(t)
			if f.provider.count() != calls {
				t.Fatalf("retry %d ran before its delay", retry)
			}
			f.clock.advance(time.Millisecond)
			f.settle(t)
			if f.provider.count() != calls+1 {
				t.Fatalf("retry %d did not fire at %s: calls=%d", retry, delay, f.provider.count())
			}
		}
		if record := f.record(t); record.retries != maxTransientRetries || len(f.clock.pending()) != 0 {
			t.Fatalf("not exhausted: %+v timers=%v", record, f.clock.pending())
		}
		f.schedule(t)
		f.clock.advance(time.Hour)
		f.settle(t)
		if f.provider.count() != 1+maxTransientRetries {
			t.Fatalf("calls after exhaustion = %d", f.provider.count())
		}
		f.appendRows(t, attemptResumeRows)
		f.schedule(t)
		if record := f.record(t); f.provider.count() != 2+maxTransientRetries || record.retries != 0 {
			t.Fatalf("progress did not start a new budget: calls=%d %+v", f.provider.count(), record)
		}
	})

	for _, tc := range []struct {
		name string
		hint time.Duration
		want time.Duration
	}{
		{"longer retry-after wins", 300 * time.Second, 300 * time.Second},
		{"shorter retry-after keeps the schedule", time.Second, 5 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAttemptFixture(t, failWith(overloaded(tc.hint)))
			f.seedRejectablePage(t)
			f.schedule(t)
			if pending := f.clock.pending(); len(pending) != 1 || pending[0] != tc.want {
				t.Fatalf("delay = %v, want %s", pending, tc.want)
			}
		})
	}

	t.Run("recovery publishes and ends the retries", func(t *testing.T) {
		f := newAttemptFixture(t, failWith(overloaded(0)), answerShort)
		f.seedRejectablePage(t)
		f.schedule(t)
		before, err := f.mem.Get(t.Context(), f.sess.ID)
		testutil.FailErr(t, "read session", err)
		f.clock.advance(transientRetryDelays[0])
		f.settle(t)
		after, err := f.mem.Get(t.Context(), f.sess.ID)
		testutil.FailErr(t, "read session", err)
		if after.CompactionGeneration <= before.CompactionGeneration || len(f.clock.pending()) != 0 {
			t.Fatalf("no recovery: generation %d -> %d timers=%v", before.CompactionGeneration, after.CompactionGeneration, f.clock.pending())
		}
	})

	t.Run("no retry once compaction is no longer needed", func(t *testing.T) {
		f := newAttemptFixture(t, failWith(overloaded(0)))
		f.seedRejectablePage(t)
		f.schedule(t)
		cfg := attemptConfig()
		cfg.HardCeilingTokens, cfg.TargetTokens = 1_000_000, 100_000
		f.svc.SetCompactor(compaction.NewSimpleCompactor(cfg, f.provider))
		f.clock.advance(transientRetryDelays[0])
		f.settle(t)
		if _, found := f.svc.Runner.attempts.records.Load(f.sess.ID); found || f.provider.count() != 1 {
			t.Fatalf("retried without need: calls=%d", f.provider.count())
		}
	})

	t.Run("deterministic capacity never starts a timer", func(t *testing.T) {
		f := newAttemptFixture(t, failWith(&failure.ProviderContextTooSmallError{ProviderID: "lite", MaxContext: 100}))
		f.seedRejectablePage(t)
		f.schedule(t)
		calls := f.provider.count()
		if record := f.record(t); record.outcome != attemptDeterministic || len(f.clock.pending()) != 0 {
			t.Fatalf("capacity failure: %+v timers=%v", record, f.clock.pending())
		}
		f.schedule(t)
		f.clock.advance(time.Hour)
		f.settle(t)
		if f.provider.count() != calls {
			t.Fatalf("capacity failure retried: %d -> %d", calls, f.provider.count())
		}
		f.appendRows(t, attemptResumeRows)
		f.schedule(t)
		if f.provider.count() <= calls {
			t.Fatal("capacity failure did not resume after a relevant change")
		}
	})

	t.Run("fitting stays deterministic during backoff", func(t *testing.T) {
		f := newAttemptFixture(t, failWith(overloaded(0)))
		f.seedRejectablePage(t)
		f.appendRows(t, 16)
		for i := range 40 {
			testutil.FailErr(t, "append exchange", f.mem.AppendMessages(t.Context(), f.sess.ID, turnExchange(i, strings.Repeat("recorded observation ", 150))...))
		}
		f.schedule(t)
		calls := f.provider.count()
		if len(f.clock.pending()) != 1 {
			t.Fatalf("fixture is not in backoff: %v", f.clock.pending())
		}
		sess, err := f.mem.Get(t.Context(), f.sess.ID)
		testutil.FailErr(t, "read session", err)
		history, err := f.svc.Load(t.Context(), sess)
		testutil.FailErr(t, "load", err)
		fitted, err := f.svc.FitRequest(t.Context(), sess, history, requestFor(sess, "Standing instructions.", history), feedBudgets{&feedWindow{window: 1 << 20}})
		testutil.FailErr(t, "fit during backoff", err)
		if input, hard := modelcall.EstimateRequestInput(fitted), attemptConfig().HardCeilingTokens; input > hard {
			t.Fatalf("input %d exceeds ceiling %d", input, hard)
		}
		f.settle(t)
		if f.provider.count() != calls {
			t.Fatalf("fitting called the summarizer: %d -> %d", calls, f.provider.count())
		}
	})
}

func TestClassifyAttemptFailureUsesTypedErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want attemptOutcome
	}{
		{"rate limited", &failure.ProviderRateLimitedError{Status: 429}, attemptTransient},
		{"overloaded", &failure.ProviderOverloadedError{Status: 503}, attemptTransient},
		{"server", &failure.ProviderServerError{Status: 500}, attemptTransient},
		{"unreachable", &failure.ProviderUnreachableError{Cause: errors.New("dial")}, attemptTransient},
		{"silent", &failure.ProviderSilentError{}, attemptTransient},
		{"interrupted", &failure.ProviderResponseInterruptedError{Cause: errors.New("reset")}, attemptTransient},
		{"lite slot down", llm.ErrLiteUnavailable, attemptTransient},
		{"lite slot busy", fmt.Errorf("summarize: %w", llm.ErrLiteBusy), attemptTransient},
		{"utility deadline", context.DeadlineExceeded, attemptTransient},
		{"retryable empty completion", &failure.ProviderEmptyCompletionError{Retryable: true}, attemptTransient},
		{"output limit empty completion", &failure.ProviderEmptyCompletionError{Retryable: true, Terminal: true, Reason: "length"}, attemptDeterministic},
		{"capacity", &failure.ProviderContextTooSmallError{}, attemptDeterministic},
		{"output truncated", &failure.ProviderOutputTruncatedError{}, attemptDeterministic},
		{"not configured", &failure.ProviderNotConfiguredError{}, attemptDeterministic},
		{"no lite provider", compaction.ErrNoLiteProvider, attemptDeterministic},
		{"prose naming a transient cause stays untyped", errors.New("provider overloaded: rate limited, try again"), attemptDeterministic},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyAttemptFailure(tc.err); got != tc.want {
				t.Fatalf("classified %v as %s, want %s", tc.err, got, tc.want)
			}
		})
	}
}
