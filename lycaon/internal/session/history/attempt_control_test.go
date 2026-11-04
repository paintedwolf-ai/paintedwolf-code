package history

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

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

	t.Run("declined synthesis keeps a due retry without spending budget", func(t *testing.T) {
		f := newAttemptFixture(t, failWith(overloaded(0)))
		f.seedRejectablePage(t)
		f.schedule(t)
		occupied, release := make(chan struct{}), make(chan struct{})
		if !f.svc.Runner.Synthesis.Start(t.Context(), f.sess.ID, func(context.Context) { close(occupied); <-release }) {
			t.Fatal("could not occupy synthesis")
		}
		<-occupied
		f.clock.advance(transientRetryDelays[0])
		f.svc.Runner.Wait()
		if pending := f.clock.pending(); f.provider.count() != 1 || len(pending) != 1 || pending[0] != transientRetryDelays[0] {
			t.Fatalf("declined retry: calls=%d timers=%v", f.provider.count(), pending)
		}
		close(release)
		f.settle(t)
		f.clock.advance(transientRetryDelays[0])
		f.settle(t)
		if record := f.record(t); f.provider.count() != 2 || record.retries != 1 {
			t.Fatalf("retry after admission: calls=%d %+v", f.provider.count(), record)
		}
	})

	t.Run("fitting stays deterministic during backoff", func(t *testing.T) {
		f := newAttemptFixture(t, failWith(overloaded(0)))
		f.seedRejectablePage(t)
		f.schedule(t)
		calls := f.provider.count()
		if len(f.clock.pending()) != 1 {
			t.Fatalf("fixture is not in backoff: %v", f.clock.pending())
		}
		for i := range 40 {
			testutil.FailErr(t, "append exchange", f.mem.AppendMessages(t.Context(), f.sess.ID, turnExchange(i, strings.Repeat("recorded observation ", 150))...))
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

func TestAttemptSourceFollowsStoreSemantics(t *testing.T) {
	database := testdbfixture.Open(t, "attempt-source.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	sql := store.NewSQL(database)
	sess, err := sql.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "append rows", sql.AppendMessages(t.Context(), sess.ID,
		api.Message{ID: "first", Role: api.MessageRoleAssistant, Content: "one"},
		api.Message{ID: "second", Role: api.MessageRoleAssistant, Content: "two"}))
	svc := &Service{store: sql, Runner: NewRunner()}
	sc := compaction.NewSimpleCompactor(compaction.DefaultCompactionConfig(), nil)
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	pass, err := svc.attemptControl().capture(t.Context(), sess, sc)
	testutil.FailErr(t, "capture", err)
	if pass.source.id != "second" {
		t.Fatalf("captured %+v, want the last row", pass.source)
	}
	current := func() bool {
		ok, err := sql.CompactionViewCurrent(t.Context(), sess.ID, pass.source.ord, pass.source.id, pass.source.seq)
		testutil.FailErr(t, "validate source", err)
		return ok
	}
	if !current() {
		t.Fatal("captured source is not current")
	}
	testutil.FailErr(t, "append", sql.AppendMessages(t.Context(), sess.ID, api.Message{ID: "third", Role: api.MessageRoleAssistant, Content: "three"}))
	if !current() {
		t.Fatal("an append invalidated the attempted source")
	}
	msg, err := sql.GetMessage(t.Context(), sess.ID, "first")
	testutil.FailErr(t, "read row", err)
	msg.Content = "edited"
	_, err = sql.UpdateMessage(t.Context(), sess.ID, "first", msg)
	testutil.FailErr(t, "edit row", err)
	if current() {
		t.Fatal("an edit left the attempted source current")
	}
}

// faultStore fails one named read so admission and settlement show their fallback.
type faultStore struct {
	Store
	failGet, failPage, failCurrent, failWindow, failPut bool
}

var errAttemptStore = errors.New("store unavailable")

func (s *faultStore) Get(ctx context.Context, id string) (*api.Session, error) {
	if s.failGet {
		return nil, errAttemptStore
	}
	return s.Store.Get(ctx, id)
}

func (s *faultStore) GetTranscriptPage(ctx context.Context, id string, q api.TranscriptPageQuery) (api.SessionTranscriptPage, error) {
	if s.failPage {
		return api.SessionTranscriptPage{}, errAttemptStore
	}
	return s.Store.GetTranscriptPage(ctx, id, q)
}

func (s *faultStore) CompactionViewCurrent(ctx context.Context, id string, ord int64, msg string, seq int64) (bool, error) {
	if s.failCurrent {
		return false, errAttemptStore
	}
	return s.Store.CompactionViewCurrent(ctx, id, ord, msg, seq)
}

func (s *faultStore) ReadMessageWindow(ctx context.Context, id string, window messageview.MessageWindow) ([]api.Message, error) {
	if s.failWindow && window.After {
		return nil, errAttemptStore
	}
	return s.Store.ReadMessageWindow(ctx, id, window)
}

func (s *faultStore) PutCompactionAttempt(ctx context.Context, id string, attempt messageview.CompactionAttempt) error {
	if s.failPut {
		return errAttemptStore
	}
	return s.Store.PutCompactionAttempt(ctx, id, attempt)
}

func TestAttemptControlFailsOpenOnStoreFaults(t *testing.T) {
	cases := []struct {
		name  string
		fault func(*faultStore)
	}{
		{"session read", func(s *faultStore) { s.failGet = true }},
		{"source capture", func(s *faultStore) { s.failPage = true }},
		{"source validation", func(s *faultStore) { s.failCurrent = true }},
		{"appended rows", func(s *faultStore) { s.failWindow = true }},
	}
	for _, tc := range cases {
		t.Run("admission admits the pass after a failed "+tc.name, func(t *testing.T) {
			f := newAttemptFixture(t, answerUnproductive)
			f.seedRejectablePage(t)
			f.schedule(t)
			sc := f.svc.Compactor.(*compaction.SimpleCompactor)
			faults := &faultStore{Store: f.mem}
			tc.fault(faults)
			f.svc.store = faults
			if _, admitted := f.svc.attemptControl().admit(t.Context(), f.sess.ID, sc); !admitted {
				t.Fatal("a store fault suppressed work")
			}
		})
	}

	t.Run("settlement records nothing it cannot fence", func(t *testing.T) {
		for _, fault := range []func(*faultStore){
			func(s *faultStore) { s.failGet = true },
			func(s *faultStore) { s.failCurrent = true },
		} {
			f := newAttemptFixture(t, answerUnproductive)
			f.seedRejectablePage(t)
			sc := f.svc.Compactor.(*compaction.SimpleCompactor)
			sess, err := f.mem.Get(t.Context(), f.sess.ID)
			testutil.FailErr(t, "read session", err)
			pass, err := f.svc.attemptControl().capture(t.Context(), sess, sc)
			testutil.FailErr(t, "capture", err)
			faults := &faultStore{Store: f.mem}
			fault(faults)
			f.svc.store = faults
			f.svc.attemptControl().settle(t.Context(), sc, f.sess.ID, pass, attemptResult{outcome: attemptNoReduction})
			if _, found := f.svc.Runner.attempts.records.Load(f.sess.ID); found {
				t.Fatal("unfenced outcome was recorded")
			}
		}
	})

	t.Run("a pass without identity keeps only its memo", func(t *testing.T) {
		f := newAttemptFixture(t, answerUnproductive)
		sc := f.svc.Compactor.(*compaction.SimpleCompactor)
		memo := &messageview.CompactionAttempt{Revision: "exact", Reason: "insufficient_savings"}
		f.svc.attemptControl().settle(t.Context(), sc, f.sess.ID, attemptPass{}, attemptResult{outcome: attemptNoReduction, memo: memo})
		got, found, err := f.mem.GetCompactionAttempt(t.Context(), f.sess.ID)
		testutil.FailErr(t, "read memo", err)
		if _, recorded := f.svc.Runner.attempts.records.Load(f.sess.ID); !found || got != *memo || recorded {
			t.Fatalf("memo=%+v found=%v recorded=%v", got, found, recorded)
		}
		f.svc.store = &faultStore{Store: f.mem, failPut: true}
		f.svc.attemptControl().saveMemo(t.Context(), f.sess.ID, memo)
	})

	t.Run("a retry firing for a removed session schedules nothing", func(t *testing.T) {
		f := newAttemptFixture(t, answerUnproductive)
		f.svc.store = &faultStore{Store: f.mem, failGet: true}
		f.svc.attemptControl().fire(f.sess.ID)
		f.svc.Runner.Stop()
		f.svc.attemptControl().fire(f.sess.ID)
		if f.provider.count() != 0 {
			t.Fatalf("calls = %d", f.provider.count())
		}
	})

	t.Run("an explicit request without identity still runs", func(t *testing.T) {
		f := newAttemptFixture(t, answerUnproductive)
		sc := f.svc.Compactor.(*compaction.SimpleCompactor)
		sess, err := f.mem.Get(t.Context(), f.sess.ID)
		testutil.FailErr(t, "read session", err)
		f.svc.store = &faultStore{Store: f.mem, failPage: true}
		if pass := f.svc.attemptControl().explicit(t.Context(), sess, sc); !pass.explicit || pass.key != "" {
			t.Fatalf("pass = %+v", pass)
		}
	})
}
