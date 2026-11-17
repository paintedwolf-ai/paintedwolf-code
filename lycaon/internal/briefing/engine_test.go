package briefing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// memStore implements the store contracts the SQL store must also meet.
type memStore struct {
	mu       sync.Mutex
	results  map[string]Result
	records  map[string][]SectionHash
	attempts map[string]string
	settled  map[string]string
	// cooling is the failure automatic requests report instead of starting.
	cooling *Failure
	nextID  int
}

func newMemStore() *memStore {
	return &memStore{results: map[string]Result{}, records: map[string][]SectionHash{}, attempts: map[string]string{}, settled: map[string]string{}}
}

func (m *memStore) Epoch(context.Context, string) (int64, error) { return 1, nil }

func (m *memStore) GetResults(_ context.Context, _ string, hashes []string) (map[string]Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]Result{}
	for _, hash := range hashes {
		if result, ok := m.results[hash]; ok {
			out[hash] = result
		}
	}
	return out, nil
}

func (m *memStore) PutResult(_ context.Context, _ string, _ int64, _ Manifest, hash string, result Result) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if prior, ok := m.results[hash]; ok && !(prior.Source == SourceFallback && result.Source == SourceSummarizer) {
		return nil
	}
	m.results[hash] = result
	return nil
}

func (m *memStore) StartAttempt(_ context.Context, request Request, _ int64, _ []SectionHash) (Attempt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.attempts[request.TargetKey]; ok {
		return Attempt{ID: id, Joined: true}, nil
	}
	if m.cooling != nil && !request.Manual {
		return Attempt{ID: "failed", Cooling: m.cooling}, nil
	}
	m.nextID++
	id := fmt.Sprint(m.nextID)
	m.attempts[request.TargetKey] = id
	return Attempt{ID: id}, nil
}

func (m *memStore) CompleteAttempt(_ context.Context, request Request, attempt string, _ []SectionHash, recordID string) (Settled, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.attempts[request.TargetKey] != attempt {
		return Settled{}, nil
	}
	delete(m.attempts, request.TargetKey)
	m.settled[request.TargetKey] = recordID
	return Settled{Applied: true}, nil
}

func (m *memStore) Resolve(_ context.Context, request Request, _ int64, _ []SectionHash, recordID string) (Settled, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settled[request.TargetKey] = recordID
	return Settled{Applied: true}, nil
}

func (m *memStore) FailAttempt(_ context.Context, request Request, attempt string, _ Failure) (Settled, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.attempts[request.TargetKey] != attempt {
		return Settled{}, nil
	}
	delete(m.attempts, request.TargetKey)
	return Settled{Applied: true}, nil
}

func (m *memStore) PutRecord(_ context.Context, _ string, _ int64, record Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records[record.ID] = record.Sections
	return nil
}

type countingGenerator struct {
	calls   atomic.Int64
	text    string
	err     error
	release chan struct{}
	entered chan struct{}
}

func (g *countingGenerator) Generate(ctx context.Context, _ Invocation) (string, string, string, error) {
	g.calls.Add(1)
	if g.entered != nil {
		g.entered <- struct{}{}
	}
	if g.release != nil {
		select {
		case <-g.release:
		case <-ctx.Done():
			return "", "", "", ctx.Err()
		}
	}
	return g.text, "provider", "model", g.err
}

type snapshotLog struct {
	seen chan Snapshot
}

func (s *snapshotLog) PublishSnapshot(_ context.Context, _ string, snapshot Snapshot) {
	s.seen <- snapshot
}

func purposeSection(intent string, mode SectionMode) SectionPlan {
	manifest := Manifest{ProfileID: "tool_activity", ProfileVersion: "1", SectionID: "purpose", Template: Template{ID: "action_purpose", Version: "1"}, Contract: OneSentence,
		Items: []Item{{Key: "user_intent", Role: RoleGoal, SourceKind: "message", Inclusion: IncludedWhole, Text: intent}}}
	return SectionPlan{ID: "purpose", Mode: mode, Manifest: manifest, Prompt: Prompt{System: "s", User: intent, MaxTokens: 60}}
}

func outcomeSection(text string) SectionPlan {
	manifest := Manifest{ProfileID: "tool_activity", ProfileVersion: "1", SectionID: "outcome", Template: Template{ID: "outcome", Version: "1"}, Contract: OneSentence,
		Items: []Item{{Key: "receipt", Role: RoleObservation, SourceKind: "receipt", Ref: text, Inclusion: IncludedWhole, Text: text}}}
	return SectionPlan{ID: "outcome", Mode: Deterministic, Manifest: manifest, Text: text}
}

func request(target string, manual bool, sections ...SectionPlan) Request {
	return Request{ProjectID: "p", ProfileID: "tool_activity", Version: "1", TargetKey: target, Manual: manual, Policy: QueuedPolicy, Sections: sections}
}

func newTestEngine(t *testing.T, generator Generator) (*Engine, *memStore, *snapshotLog) {
	t.Helper()
	scheduler := NewScheduler(context.Background())
	t.Cleanup(func() { scheduler.Stop(); scheduler.Wait(context.Background()) })
	store, log := newMemStore(), &snapshotLog{seen: make(chan Snapshot, 16)}
	return NewEngine(scheduler, store, generator, log, nil), store, log
}

func TestEqualMaterialSharesOneGenerationAcrossTargets(t *testing.T) {
	generator := &countingGenerator{text: "Rebuild from a clean folder.", release: make(chan struct{}), entered: make(chan struct{}, 2)}
	engine, store, log := newTestEngine(t, generator)
	for i, target := range []string{"walk:step", "activity:card"} {
		snap, err := engine.Request(t.Context(), request(target, false, purposeSection("fix the build", Generated)))
		testutil.FailErr(t, "request "+target, err)
		if snap.Status != StatusPending {
			t.Fatalf("%s: %s", target, snap.Status)
		}
		if i == 0 {
			receive(t, generator.entered)
		}
	}
	close(generator.release)
	first, second := receive(t, log.seen), receive(t, log.seen)
	if generator.calls.Load() != 1 {
		t.Fatalf("generated %d times for one manifest", generator.calls.Load())
	}
	if first.RecordID == "" || first.RecordID != second.RecordID {
		t.Fatalf("equal sections produced different records: %s %s", first.RecordID, second.RecordID)
	}
	if len(store.records) != 1 {
		t.Fatalf("%d records", len(store.records))
	}
}

func TestStoredResultsCompleteWithoutAModelCall(t *testing.T) {
	generator := &countingGenerator{text: "Clear the cached token."}
	engine, _, log := newTestEngine(t, generator)
	_, err := engine.Request(t.Context(), request("a", false, purposeSection("sign out", Generated), outcomeSection("exit 0")))
	testutil.FailErr(t, "first request", err)
	first := receive(t, log.seen)
	again, err := engine.Request(t.Context(), request("a", true, purposeSection("sign out", Generated), outcomeSection("exit 0")))
	testutil.FailErr(t, "repeat request", err)
	if generator.calls.Load() != 1 || again.Status != StatusComplete || again.Regenerate != InputsUnchanged || again.RecordID != first.RecordID {
		t.Fatalf("repeat cost a call or changed identity: calls=%d %+v", generator.calls.Load(), again)
	}
	if !again.Sections[0].Reused || again.Sections[1].Reused {
		t.Fatalf("reuse flags: %+v", again.Sections)
	}
	changed, err := engine.Request(t.Context(), request("a", true, purposeSection("sign out everywhere", Generated), outcomeSection("exit 0")))
	testutil.FailErr(t, "changed request", err)
	if changed.Regenerate != Started {
		t.Fatalf("changed input did not regenerate: %+v", changed)
	}
	if next := receive(t, log.seen); next.RecordID == first.RecordID {
		t.Fatal("changed input reused the old record")
	}
}

type recordedLog struct {
	mu      sync.Mutex
	records []slog.Record
}

func (l *recordedLog) Enabled(context.Context, slog.Level) bool { return true }
func (l *recordedLog) WithAttrs([]slog.Attr) slog.Handler       { return l }
func (l *recordedLog) WithGroup(string) slog.Handler            { return l }
func (l *recordedLog) Handle(_ context.Context, record slog.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, record)
	return nil
}

func (l *recordedLog) requests() []map[string]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]string
	for _, record := range l.records {
		if record.Message != "briefing request" {
			continue
		}
		attrs := map[string]string{}
		record.Attrs(func(attr slog.Attr) bool { attrs[attr.Key] = attr.Value.String(); return true })
		out = append(out, attrs)
	}
	return out
}

// Each request leaves one line saying, per section, how it is produced and
// whether its text was already stored.
func TestRequestLogExplainsEachSection(t *testing.T) {
	scheduler := NewScheduler(context.Background())
	t.Cleanup(func() { scheduler.Stop(); scheduler.Wait(context.Background()) })
	recorded := &recordedLog{}
	snapshots := &snapshotLog{seen: make(chan Snapshot, 4)}
	engine := NewEngine(scheduler, newMemStore(), &countingGenerator{text: "Clear the cached token."}, snapshots, slog.New(recorded))
	ask := func() Snapshot {
		snap, err := engine.Request(t.Context(), request("card", false, purposeSection("sign out", Generated), outcomeSection("exit 0")))
		testutil.FailErr(t, "request", err)
		return snap
	}
	ask()
	receive(t, snapshots.seen)
	ask()
	lines := recorded.requests()
	if len(lines) != 2 {
		t.Fatalf("%d request lines", len(lines))
	}
	if got := lines[0]["sections"]; got != "purpose:generated:missing,outcome:deterministic:stored" || lines[0]["status"] != string(StatusPending) {
		t.Fatalf("first request logged %v", lines[0])
	}
	if got := lines[1]["sections"]; got != "purpose:generated:stored,outcome:deterministic:stored" || lines[1]["status"] != string(StatusComplete) {
		t.Fatalf("second request logged %v", lines[1])
	}
}

func TestLookupSectionsNeverAdmitWork(t *testing.T) {
	generator := &countingGenerator{text: "unused"}
	engine, _, _ := newTestEngine(t, generator)
	snap, err := engine.Request(t.Context(), request("lookup", false, purposeSection("read a file", Lookup), outcomeSection("214 lines")))
	testutil.FailErr(t, "request", err)
	if snap.Status != StatusComplete || snap.Sections[0].Result != nil || snap.Sections[1].Result == nil || generator.calls.Load() != 0 {
		t.Fatalf("lookup admitted work or invented text: calls=%d %+v", generator.calls.Load(), snap)
	}
}

func TestWithoutAModelOnlyFallbacksAdmitWork(t *testing.T) {
	engine, store, log := newTestEngine(t, nil)
	snap, err := engine.Request(t.Context(), request("none", false, purposeSection("run tests", Generated), outcomeSection("exit 0")))
	testutil.FailErr(t, "request", err)
	if snap.Status != StatusComplete || snap.Sections[0].Result != nil || len(store.attempts) != 0 {
		t.Fatalf("a model-less engine admitted work: %+v", snap)
	}
	binary := purposeSection("change a binary", Generated)
	binary.Fallback = "Modified a binary asset."
	_, err = engine.Request(t.Context(), request("bin", false, binary))
	testutil.FailErr(t, "fallback request", err)
	if done := receive(t, log.seen); done.Status != StatusComplete || done.Sections[0].Result.Source != SourceFallback {
		t.Fatalf("fallback did not fill: %+v", done)
	}
}

func TestFallbackStandsInAndUpgradesAtTheSameHash(t *testing.T) {
	generator := &countingGenerator{text: "**  **"}
	engine, store, log := newTestEngine(t, generator)
	section := purposeSection("change a binary", Generated)
	section.Fallback = "Modified a binary asset."
	_, err := engine.Request(t.Context(), request("bin", false, section))
	testutil.FailErr(t, "first request", err)
	fallback := receive(t, log.seen)
	if fallback.Sections[0].Result == nil || fallback.Sections[0].Result.Source != SourceFallback {
		t.Fatalf("unusable output did not fall back: %+v", fallback.Sections[0].Result)
	}
	generator.text = "Replaced the app icon."
	_, err = engine.Request(t.Context(), request("bin", true, section))
	testutil.FailErr(t, "retry", err)
	upgraded := receive(t, log.seen)
	if upgraded.Sections[0].Result.Source != SourceSummarizer || upgraded.Sections[0].ManifestHash != fallback.Sections[0].ManifestHash {
		t.Fatalf("fallback not upgraded at its hash: %+v", upgraded.Sections[0])
	}
	if got := store.results[upgraded.Sections[0].ManifestHash]; got.Source != SourceSummarizer {
		t.Fatalf("stored %s", got.Source)
	}
}

func TestFailedGenerationWithoutFallbackFailsTheAttempt(t *testing.T) {
	engine, store, log := newTestEngine(t, &countingGenerator{err: errors.New("provider down")})
	// A deterministic outcome beside the failed section must not settle it.
	_, err := engine.Request(t.Context(), request("x", false, purposeSection("anything", Generated), outcomeSection("exit 1")))
	testutil.FailErr(t, "request", err)
	if snap := receive(t, log.seen); snap.Status != StatusFailed || snap.Error == "" {
		t.Fatalf("failure not published: %+v", snap)
	}
	if len(store.results) != 0 || len(store.records) != 0 || len(store.attempts) != 0 || len(store.settled) != 0 {
		t.Fatalf("failure left state: %+v %+v %+v %+v", store.results, store.records, store.attempts, store.settled)
	}
}

func TestCoolingFailureAnswersWithoutAModelCall(t *testing.T) {
	generator := &countingGenerator{text: "Retried."}
	engine, store, log := newTestEngine(t, generator)
	store.cooling = &Failure{Code: CodeGenerationFailed, Message: "provider down"}
	snap, err := engine.Request(t.Context(), request("x", false, purposeSection("anything", Generated)))
	testutil.FailErr(t, "automatic", err)
	if snap.Status != StatusFailed || snap.ErrorCode != CodeGenerationFailed || generator.calls.Load() != 0 {
		t.Fatalf("cooling target asked the model: calls=%d %+v", generator.calls.Load(), snap)
	}
	retried, err := engine.Request(t.Context(), request("x", true, purposeSection("anything", Generated)))
	testutil.FailErr(t, "manual", err)
	if retried.Status != StatusPending {
		t.Fatalf("manual retry waited: %+v", retried)
	}
	if done := receive(t, log.seen); done.Status != StatusComplete || generator.calls.Load() != 1 {
		t.Fatalf("manual retry: calls=%d %+v", generator.calls.Load(), done)
	}
}

func TestSupersededAttemptCannotPublish(t *testing.T) {
	generator := &countingGenerator{text: "Old answer.", release: make(chan struct{})}
	engine, store, log := newTestEngine(t, generator)
	_, err := engine.Request(t.Context(), request("t", false, purposeSection("intent", Generated)))
	testutil.FailErr(t, "request", err)
	store.mu.Lock()
	store.attempts["t"] = "successor"
	store.mu.Unlock()
	close(generator.release)
	engine.scheduler.Wait(context.Background())
	if generator.calls.Load() != 1 {
		t.Fatal("generation never ran")
	}
	select {
	case snap := <-log.seen:
		t.Fatalf("superseded attempt published %+v", snap)
	default:
	}
	if store.settled["t"] != "" {
		t.Fatal("superseded attempt settled the target")
	}
}

func TestOrphanedAttemptIsReplaced(t *testing.T) {
	engine, store, log := newTestEngine(t, &countingGenerator{text: "Rebuild the cache."})
	store.attempts["t"] = "ghost"
	_, err := engine.Request(t.Context(), request("t", false, purposeSection("intent", Generated)))
	testutil.FailErr(t, "request", err)
	failed, done := receive(t, log.seen), receive(t, log.seen)
	if failed.Status != StatusFailed || failed.AttemptID != "ghost" || failed.ErrorCode != CodeInterrupted {
		t.Fatalf("orphan not ended: %+v", failed)
	}
	if done.Status != StatusComplete || done.AttemptID == "ghost" {
		t.Fatalf("replacement did not settle: %+v", done)
	}
}

func TestStoppedRunEndsCancelled(t *testing.T) {
	generator := &countingGenerator{text: "unused", release: make(chan struct{}), entered: make(chan struct{}, 1)}
	engine, _, log := newTestEngine(t, generator)
	_, err := engine.Request(t.Context(), request("t", false, purposeSection("intent", Generated)))
	testutil.FailErr(t, "request", err)
	receive(t, generator.entered)
	engine.scheduler.Cancel(func(job Job) bool { return JobProfile(job) == "tool_activity" })
	if snap := receive(t, log.seen); snap.Status != StatusFailed || snap.ErrorCode != CodeCancelled {
		t.Fatalf("stopped run settled as %+v", snap)
	}
}

func TestJobProfileReadsOnlyEngineJobs(t *testing.T) {
	if got := JobProfile(Job{Key: jobKey(request("t", false))}); got != "tool_activity" {
		t.Fatalf("engine job profile %q", got)
	}
	if got := JobProfile(Job{Key: "file\x00p\x00tool_activity\x00x"}); got != "" {
		t.Fatalf("another kind's job read as %q", got)
	}
}

func TestResultQualityFollowsTheReasoningInput(t *testing.T) {
	excerpt := func(truncated, shared bool) ReasoningInput {
		return ReasoningInput{Excerpt: &Reasoning{Text: "Tag after the push.", Ref: "message:out", Truncated: truncated, Shared: shared}}
	}
	unavailable := []string{QualityReasoningUnavailable}
	cases := []struct {
		name  string
		input ReasoningInput
		want  []string
	}{
		{"whole", excerpt(false, false), []string{QualityReasoningIncluded}},
		{"suffix", excerpt(true, false), []string{QualityReasoningIncluded, QualityReasoningTruncated}},
		{"shared", excerpt(false, true), []string{QualityReasoningIncluded, QualityReasoningShared}},
		{"absent", ReasoningInput{}, nil},
		{"details only", ReasoningInput{Gap: string(ReasoningDetailsOnly)}, unavailable},
		{"oversized", ReasoningInput{Gap: string(ReasoningOversized)}, unavailable},
		{"malformed", ReasoningInput{Gap: string(ReasoningMalformed)}, unavailable},
		{"output not found", ReasoningInput{Gap: "the action's model output was not found"}, unavailable},
	}
	for _, tc := range cases {
		for _, fallback := range []bool{false, true} {
			ev := liveEvidence()
			ev.Reasoning = tc.input
			section, err := PurposeSection(t.Context(), ev)
			testutil.FailErr(t, "build "+tc.name, err)
			var generator Generator = &countingGenerator{text: "Pushes the release before tagging it."}
			if fallback {
				generator, section.Fallback = nil, "Ran a command."
			}
			engine, _, log := newTestEngine(t, generator)
			_, err = engine.Request(t.Context(), request(tc.name, false, section))
			testutil.FailErr(t, "request "+tc.name, err)
			done := receive(t, log.seen)
			if done.Status != StatusComplete || done.Sections[0].Result == nil {
				t.Fatalf("%s (fallback %v): %+v", tc.name, fallback, done)
			}
			if got := done.Sections[0].Result.Quality; !slices.Equal(got, tc.want) {
				t.Fatalf("%s (fallback %v): quality %v, want %v", tc.name, fallback, got, tc.want)
			}
		}
	}
}
