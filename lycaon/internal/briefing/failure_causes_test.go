package briefing

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

type scriptedGenerator struct {
	calls atomic.Int64
	fn    func(context.Context, Invocation) (string, string, string, error)
}

func (g *scriptedGenerator) Generate(ctx context.Context, inv Invocation) (string, string, string, error) {
	g.calls.Add(1)
	return g.fn(ctx, inv)
}

func changeSection(text string, contract Contract, fallback string) SectionPlan {
	manifest := Manifest{
		ProfileID: "tool_activity", ProfileVersion: "1", SectionID: "change",
		Template: Template{ID: "action_change", Version: "1"}, Contract: contract,
		Items: []Item{{Key: "result", Role: RoleObservation, SourceKind: "receipt", Inclusion: IncludedWhole, Text: text}},
	}
	return SectionPlan{
		ID: "change", Mode: Generated, Manifest: manifest,
		Prompt: Prompt{System: "s", User: text, MaxTokens: 60}, Fallback: fallback,
	}
}

func TestClassifySectionFailuresSeparatesGeneratorAndParserStages(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		callerErr error
		wantStage FailureStage
		wantKind  FailureKind
		wantSub   string
	}{
		{
			name:      "slot busy",
			err:       llm.ErrLiteBusy,
			wantStage: StageAdmission,
			wantKind:  FailureSlotBusy,
			wantSub:   "admission failed (utility slot busy)",
		},
		{
			name:      "unavailable model",
			err:       llm.ErrUtilityProviderUnconfigured,
			wantStage: StageAdmission,
			wantKind:  FailureUnavailableGenerator,
			wantSub:   "admission failed (utility model unavailable)",
		},
		{
			name:      "screen withheld",
			err:       llm.ErrModelRequestSecretWithheld,
			wantStage: StageScreening,
			wantKind:  FailureScreeningFailed,
			wantSub:   "protected content withheld",
		},
		{
			name:      "evidence deadline",
			err:       &llm.ModelEvidenceWaitError{Stage: secretmatch.FaultStageManagedSecretStore, Err: context.DeadlineExceeded},
			callerErr: context.DeadlineExceeded,
			wantStage: StageEvidenceReconciliation,
			wantKind:  FailureDeadlineExceeded,
			wantSub:   "credential evidence check timed out",
		},
		{
			name:      "evidence canceled",
			err:       &llm.ModelEvidenceWaitError{Stage: secretmatch.FaultStageManagedSecretStore, Err: context.Canceled},
			callerErr: context.Canceled,
			wantStage: StageEvidenceReconciliation,
			wantKind:  FailureCanceled,
			wantSub:   "credential evidence check canceled",
		},
		{
			name:      "provider failure hides raw body and path",
			err:       errors.New("POST https://api.example/v1: sk-secret-9999 /Users/alice/.env"),
			wantStage: StageGeneration,
			wantKind:  FailureProviderFailed,
			wantSub:   "provider request failed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyGenerationError("purpose", tc.err, tc.callerErr)
			if got.Stage != tc.wantStage || got.Kind != tc.wantKind {
				t.Fatalf("got stage=%s kind=%s; want stage=%s kind=%s", got.Stage, got.Kind, tc.wantStage, tc.wantKind)
			}
			msg := got.PublicSummary()
			if !strings.Contains(msg, "purpose ["+string(tc.wantStage)+"]: "+tc.wantSub) {
				t.Fatalf("unexpected public summary %q", msg)
			}
			if strings.Contains(msg, "sk-secret-9999") || strings.Contains(msg, "/Users/alice") {
				t.Fatalf("public summary leaked raw material: %q", msg)
			}
		})
	}

	emptyErr := classifyParseError("change", ErrNoText)
	if emptyErr.Kind != FailureEmptyOutput || !errors.Is(emptyErr, ErrNoText) {
		t.Fatalf("blank parse classification = %+v", emptyErr)
	}
	malformedErr := classifyParseError("change", errors.New("contract unbounded"))
	if malformedErr.Kind != FailureMalformedOutput || errors.Is(malformedErr, ErrNoText) {
		t.Fatalf("malformed contract classification = %+v", malformedErr)
	}
}

func TestEnginePreservesRealCausesReusesSiblingAndStopsOnRunDeadline(t *testing.T) {
	// 1. Blank model output produces ErrNoText, whereas malformed contract keeps parser failure.
	gen := &scriptedGenerator{fn: func(_ context.Context, inv Invocation) (string, string, string, error) {
		if inv.Prompt.User == "blank" {
			return "   ", "p", "m", nil
		}
		return "Valid sentence.", "p", "m", nil
	}}
	engine, _, log := newTestEngine(t, gen)

	_, err := engine.Request(t.Context(), request("blank-target", false, purposeSection("blank", Generated)))
	testutil.FailErr(t, "blank request", err)
	blankSnap := receive(t, log.seen)
	if blankSnap.Status != StatusFailed || blankSnap.ErrorCode != CodeGenerationFailed || !strings.Contains(blankSnap.Error, ErrNoText.Error()) {
		t.Fatalf("blank output snapshot = %+v", blankSnap)
	}

	malformed := changeSection("valid", Contract{}, "")
	_, err = engine.Request(t.Context(), request("malformed-target", false, malformed))
	testutil.FailErr(t, "malformed request", err)
	malSnap := receive(t, log.seen)
	if malSnap.Status != StatusFailed || strings.Contains(malSnap.Error, ErrNoText.Error()) || !strings.Contains(malSnap.Error, "change [parse]: briefing output did not match section contract") {
		t.Fatalf("malformed contract snapshot = %+v", malSnap)
	}

	// 2. Successful sibling section remains stored when second section fails, and manual retry reuses it.
	siblingGen := &scriptedGenerator{}
	var failChange atomic.Bool
	failChange.Store(true)
	siblingGen.fn = func(_ context.Context, inv Invocation) (string, string, string, error) {
		if inv.Prompt.User == "change-input" && failChange.Load() {
			return "", "", "", llm.ErrLiteBusy
		}
		return "Completed sentence.", "p", "m", nil
	}
	sibEngine, sibStore, sibLog := newTestEngine(t, siblingGen)
	pSec := purposeSection("purpose-input", Generated)
	cSec := changeSection("change-input", OneSentence, "")

	_, err = sibEngine.Request(t.Context(), request("sib", false, pSec, cSec))
	testutil.FailErr(t, "first sibling request", err)
	firstFail := receive(t, sibLog.seen)
	if firstFail.Status != StatusFailed || !strings.Contains(firstFail.Error, "change [admission]: admission failed (utility slot busy)") {
		t.Fatalf("expected slot busy failure on change, got %+v", firstFail)
	}
	if len(sibStore.results) != 1 {
		t.Fatalf("expected purpose result to stay stored, got %d results", len(sibStore.results))
	}

	failChange.Store(false)
	siblingGen.calls.Store(0)
	_, err = sibEngine.Request(t.Context(), request("sib", true, pSec, cSec))
	testutil.FailErr(t, "manual retry", err)
	recovered := receive(t, sibLog.seen)
	if recovered.Status != StatusComplete || siblingGen.calls.Load() != 1 || !recovered.Sections[0].Reused {
		t.Fatalf("manual retry did not reuse purpose: calls=%d snap=%+v", siblingGen.calls.Load(), recovered)
	}

	// 3. Expired run limit never starts the second section's generation.
	deadlineGen := &scriptedGenerator{fn: func(ctx context.Context, _ Invocation) (string, string, string, error) {
		<-ctx.Done()
		return "", "", "", ctx.Err()
	}}
	dlEngine, _, dlLog := newTestEngine(t, deadlineGen)
	req := request("deadline-target", false, purposeSection("p1", Generated), changeSection("c1", OneSentence, ""))
	req.Policy = Policy{Admission: Immediate, RunLimit: 15 * time.Millisecond}
	_, err = dlEngine.Request(t.Context(), req)
	testutil.FailErr(t, "deadline request", err)
	dlSnap := receive(t, dlLog.seen)
	if dlSnap.Status != StatusFailed || dlSnap.ErrorCode != CodeGenerationFailed || deadlineGen.calls.Load() != 1 {
		t.Fatalf("expired run started second section or wrong code: calls=%d snap=%+v", deadlineGen.calls.Load(), dlSnap)
	}
	if !strings.Contains(dlSnap.Error, "purpose [generation]: generation deadline exceeded") ||
		!strings.Contains(dlSnap.Error, "change [generation]: generation deadline exceeded") {
		t.Fatalf("deadline error message = %q", dlSnap.Error)
	}
}

func TestQueueExpiryPreservesQueueExpiredCodeAndSectionSummary(t *testing.T) {
	now := time.Date(2026, 11, 23, 8, 55, 0, 0, time.UTC)
	scheduler := NewScheduler(context.Background())
	scheduler.now = func() time.Time { return now }
	t.Cleanup(func() { scheduler.Stop(); scheduler.Wait(context.Background()) })

	blocker := &countingGenerator{text: "held", release: make(chan struct{}), entered: make(chan struct{}, 1)}
	store := newMemStore()
	log := &snapshotLog{seen: make(chan Snapshot, 8)}
	engine := NewEngine(scheduler, store, blocker, log, nil)

	policy := Policy{Admission: Queued, QueueLimit: 4, Active: 1, QueueWait: 10 * time.Second, RunLimit: time.Minute}
	first := request("active-1", false, purposeSection("first", Generated))
	first.Policy = policy
	_, err := engine.Request(t.Context(), first)
	testutil.FailErr(t, "submit active", err)
	receive(t, blocker.entered)

	queued := request("queued-2", false, purposeSection("second", Generated))
	queued.Policy = policy
	_, err = engine.Request(t.Context(), queued)
	testutil.FailErr(t, "submit queued", err)

	now = now.Add(15 * time.Second)
	scheduler.ExpireQueued("p")
	expired := receive(t, log.seen)
	close(blocker.release)
	_ = receive(t, log.seen)

	if blocker.calls.Load() != 1 {
		t.Fatalf("expired queued job invoked generator: calls=%d", blocker.calls.Load())
	}
	if expired.ErrorCode != CodeQueueExpired || !strings.Contains(expired.Error, "purpose [queue]: briefing waited too long to start") {
		t.Fatalf("unexpected queue expiry snapshot: %+v", expired)
	}
}
