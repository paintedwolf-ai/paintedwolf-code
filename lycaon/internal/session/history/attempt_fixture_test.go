package history

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/compaction"
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

func summaryAnswer(facts ...string) (string, error) {
	raw, err := json.Marshal(map[string][]string{"facts": facts, "completed": {}, "pending": {}, "reacquire": {}, "constraints": {}})
	return string(raw), err
}

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

// answerUnproductive is valid output as long as the page it would replace.
func answerUnproductive() (string, error) {
	facts := make([]string, 8)
	for i := range facts {
		facts[i] = strings.Repeat("A retained fact. ", 9)
	}
	return summaryAnswer(facts...)
}

func answerShort() (string, error) { return summaryAnswer("Short.") }

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
