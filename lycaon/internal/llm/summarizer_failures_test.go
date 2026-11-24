package llm

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

type scriptedUtilityProvider struct {
	id            string
	completeCalls atomic.Int64
	streamCalls   atomic.Int64
	completeFn    func(context.Context, modelcall.CompletionRequest) (*modelcall.Completion, error)
	streamFn      func(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error)
}

func (p *scriptedUtilityProvider) ID() string { return p.id }
func (p *scriptedUtilityProvider) Models() []modelcall.ModelInfo {
	return []modelcall.ModelInfo{{ID: p.id}}
}
func (p *scriptedUtilityProvider) Profile() providerprofile.Profile {
	return providerprofile.Profile{UtilitySingleFlight: true}
}

func (p *scriptedUtilityProvider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	p.completeCalls.Add(1)
	if p.completeFn != nil {
		return p.completeFn(ctx, req)
	}
	return &modelcall.Completion{Content: "ok"}, nil
}

func (p *scriptedUtilityProvider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	p.streamCalls.Add(1)
	if p.streamFn != nil {
		return p.streamFn(ctx, req)
	}
	ch := make(chan modelcall.StreamChunk, 1)
	ch <- modelcall.StreamChunk{Content: "ok", Done: true}
	close(ch)
	return ch, nil
}

func newDualProviderSummarizer(t *testing.T, lite, coord modelcall.Provider, class UtilityClass) *RegistrySummarizer {
	t.Helper()
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	policy := newPolicyFixture(t, ModelPolicy{
		Lite:        ModelRef{ProviderID: lite.ID(), Model: lite.ID()},
		Coordinator: ModelRef{ProviderID: coord.ID(), Model: coord.ID()},
	})
	reg := newTestRegistry()
	testutil.FailErr(t, "register lite", reg.Register(lite))
	testutil.FailErr(t, "register coord", reg.Register(coord))
	return &RegistrySummarizer{
		Registry: reg,
		Policy:   policy,
		Scope:    SettingsScopeGlobal,
		Purpose:  "tool_activity",
		Class:    class,
		Plane:    NewUtilityPlane(),
	}
}

func TestSummarizeRequiredPreservesDualStageFailureAndRespectsRequestedFallback(t *testing.T) {
	liteErr := errors.New("lite upstream 502")
	coordErr := errors.New("coordinator upstream 503")
	lite := &scriptedUtilityProvider{
		id: "lite",
		completeFn: func(context.Context, modelcall.CompletionRequest) (*modelcall.Completion, error) {
			return nil, liteErr
		},
	}
	coord := &scriptedUtilityProvider{
		id: "coord",
		completeFn: func(context.Context, modelcall.CompletionRequest) (*modelcall.Completion, error) {
			return &modelcall.Completion{Content: "Coordinator rescued summary."}, nil
		},
	}
	r := newDualProviderSummarizer(t, lite, coord, UtilityClassRequested)
	out, err := r.SummarizeRequired(t.Context(), "sys", "usr", 80)
	testutil.FailErr(t, "coordinator rescue", err)
	if out != "Coordinator rescued summary." || lite.completeCalls.Load() != 1 || coord.completeCalls.Load() != 1 {
		t.Fatalf("out=%q lite=%d coord=%d", out, lite.completeCalls.Load(), coord.completeCalls.Load())
	}

	coord.completeFn = func(context.Context, modelcall.CompletionRequest) (*modelcall.Completion, error) {
		return nil, coordErr
	}
	_, err = r.SummarizeRequired(t.Context(), "sys", "usr", 80)
	if !errors.Is(err, liteErr) || !errors.Is(err, coordErr) {
		t.Fatalf("expected both stage errors preserved, got %v", err)
	}
	var dual *SummarizerFallbackError
	if !errors.As(err, &dual) || dual.Selected != coordErr {
		t.Fatalf("expected SummarizerFallbackError selecting coordinator failure, got %+v", dual)
	}
}

func TestExpiredCallerAndScreenRefusalSkipCoordinatorAndStreamFallbacks(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	lite := &scriptedUtilityProvider{
		id: "lite",
		completeFn: func(context.Context, modelcall.CompletionRequest) (*modelcall.Completion, error) {
			cancel(context.DeadlineExceeded)
			return nil, context.DeadlineExceeded
		},
	}
	coord := &scriptedUtilityProvider{id: "coord"}
	r := newDualProviderSummarizer(t, lite, coord, UtilityClassRequested)
	if _, err := r.SummarizeRequired(ctx, "sys", "usr", 80); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired lite call err = %v", err)
	}
	if coord.completeCalls.Load() != 0 {
		t.Fatalf("expired caller invoked coordinator fallback %d times", coord.completeCalls.Load())
	}

	// Already-expired context never invokes primary or fallback.
	if _, err := r.SummarizeRequired(ctx, "sys", "usr", 80); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-expired caller err = %v", err)
	}
	if lite.completeCalls.Load() != 1 || coord.completeCalls.Load() != 0 {
		t.Fatalf("pre-expired caller made calls: lite=%d coord=%d", lite.completeCalls.Load(), coord.completeCalls.Load())
	}

	// Screen refusal is terminal across both coordinator fallback and streaming fallback.
	screenLite := &scriptedUtilityProvider{
		id: "lite-screen",
		completeFn: func(context.Context, modelcall.CompletionRequest) (*modelcall.Completion, error) {
			return nil, ErrModelRequestSecretWithheld
		},
		streamFn: func(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
			return nil, ErrModelRequestSecretWithheld
		},
	}
	screenCoord := &scriptedUtilityProvider{id: "coord-screen"}
	sr := newDualProviderSummarizer(t, screenLite, screenCoord, UtilityClassRequested)
	if _, err := sr.SummarizeRequired(t.Context(), "sys", "usr", 80); !errors.Is(err, ErrModelRequestSecretWithheld) {
		t.Fatalf("screen refusal err = %v", err)
	}
	if screenCoord.completeCalls.Load() != 0 {
		t.Fatalf("screen refusal invoked coordinator %d times", screenCoord.completeCalls.Load())
	}
	screenLite.completeCalls.Store(0)
	if _, err := sr.SummarizeStream(t.Context(), "sys", "usr", 80, nil); !errors.Is(err, ErrModelRequestSecretWithheld) {
		t.Fatalf("stream screen refusal err = %v", err)
	}
	if screenLite.completeCalls.Load() != 0 || screenCoord.completeCalls.Load() != 0 {
		t.Fatalf("stream screen refusal fell back: liteComplete=%d coord=%d", screenLite.completeCalls.Load(), screenCoord.completeCalls.Load())
	}
}

func TestEvidenceDeadlineRetainsPhaseAndCancellationIdentity(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "blocking", true: "streaming"}[streaming], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			screen := NewModelSecretScreen(&secretmatch.Matcher{}, nil)
			screen.SetManagedSecretEvidence(func(context.Context, string, string) ([]secretmatch.Remembered, error) {
				cancel()
				return nil, context.DeadlineExceeded
			})
			inner := &scriptedUtilityProvider{id: "lite"}
			screened := &secretScreenedProvider{
				inner:       inner,
				destination: ScreenDestination{ID: "lite", ProviderID: "lite"},
				screen:      screen,
			}
			coord := &scriptedUtilityProvider{id: "coord"}
			r := newDualProviderSummarizer(t, screened, coord, UtilityClassRequested)
			r.ProjectID = "proj-1"

			var err error
			if streaming {
				_, err = r.SummarizeStream(ctx, "sys", "usr", 80, nil)
			} else {
				_, err = r.SummarizeRequired(ctx, "sys", "usr", 80)
			}
			var fault *ModelRequestSecretScreenFaultError
			if !errors.As(err, &fault) || fault.Stage != secretmatch.FaultStageManagedSecretStore {
				t.Fatalf("lost evidence fault phase: %v", err)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("lost deadline identity: %v", err)
			}
			if inner.completeCalls.Load() != 0 || inner.streamCalls.Load() != 0 || coord.completeCalls.Load() != 0 {
				t.Fatalf("dispatched after evidence deadline: innerComplete=%d innerStream=%d coord=%d",
					inner.completeCalls.Load(), inner.streamCalls.Load(), coord.completeCalls.Load())
			}
		})
	}
}

func TestSummarizerPolicyAndUnconfiguredHelpers(t *testing.T) {
	lite := &scriptedUtilityProvider{id: "lite"}
	coord := &scriptedUtilityProvider{id: "coord"}
	r := newDualProviderSummarizer(t, lite, coord, UtilityClassOverlay)
	rev, err := r.SummaryRevision(t.Context())
	testutil.FailErr(t, "SummaryRevision", err)
	if rev == "" {
		t.Fatal("empty summary revision")
	}
	if budget := r.SummaryBudget(t.Context()); budget.Input <= 0 || budget.Output <= 0 {
		t.Fatalf("unexpected summary budget: %+v", budget)
	}
	empty := &RegistrySummarizer{}
	if _, err := empty.SummarizeRequired(t.Context(), "sys", "usr", 40); !IsUnavailableUtilityError(err) {
		t.Fatalf("expected unavailable utility error, got %v", err)
	}
	if !UtilityBudgetExceeded(&utilityBudgetExceededError{cause: context.DeadlineExceeded}) {
		t.Fatal("UtilityBudgetExceeded returned false for utilityBudgetExceededError")
	}
}
