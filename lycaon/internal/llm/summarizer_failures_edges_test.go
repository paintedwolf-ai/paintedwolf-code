package llm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/testutil"
)

type errOnlyProvider struct {
	id  string
	err error
}

func (p *errOnlyProvider) ID() string                       { return p.id }
func (p *errOnlyProvider) Models() []modelcall.ModelInfo    { return []modelcall.ModelInfo{{ID: "m1"}} }
func (p *errOnlyProvider) Profile() providerprofile.Profile { return providerprofile.Profile{} }
func (p *errOnlyProvider) Complete(context.Context, modelcall.CompletionRequest) (*modelcall.Completion, error) {
	return nil, p.err
}
func (p *errOnlyProvider) Stream(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	return nil, p.err
}

func TestSummarizerFailureSelectionStreamAndPolicyEdges(t *testing.T) {
	// unconfiguredUtilityError empty vs detailed text + Unwrap.
	emptyUnconf := newUnconfiguredUtilityError("")
	if emptyUnconf.Error() != ErrUtilityProviderUnconfigured.Error() || !IsUnavailableUtilityError(emptyUnconf) {
		t.Fatalf("empty unconfigured err = %v", emptyUnconf)
	}
	detailUnconf := newUnconfiguredUtilityError("lite slot missing")
	if detailUnconf.Error() != "lite slot missing" || !errors.Is(detailUnconf, ErrUtilityProviderUnconfigured) {
		t.Fatalf("detailed unconfigured err = %v", detailUnconf)
	}

	// SummarizerFallbackError formatting, nil receiver, and cause priority.
	var nilFallback *SummarizerFallbackError
	if nilFallback.Error() != "" || nilFallback.Unwrap() != nil {
		t.Fatalf("nil fallback err = %q / %v", nilFallback.Error(), nilFallback.Unwrap())
	}
	rawFmt := (&SummarizerFallbackError{
		PrimaryProvider: "p1", PrimaryModel: "m1", PrimaryErr: errors.New("p-boom"),
		FallbackProvider: "p2", FallbackModel: "m2", FallbackErr: errors.New("f-boom"),
	}).Error()
	if !strings.Contains(rawFmt, "lite (p1/m1)") || !strings.Contains(rawFmt, "fallback (p2/m2)") {
		t.Fatalf("raw fallback format = %q", rawFmt)
	}

	primaryRateErr := errors.New("rate limit")
	screenBlocked := ErrModelRequestSecretWithheld
	if got := selectSummarizerCause(primaryRateErr, nil); got != primaryRateErr {
		t.Fatalf("nil fallback cause = %v", got)
	}
	if got := selectSummarizerCause(nil, primaryRateErr); got != primaryRateErr {
		t.Fatalf("nil primary cause = %v", got)
	}
	fbScreen := newSummarizerFallbackError("p1", "m1", primaryRateErr, "p2", "m2", screenBlocked)
	if !errors.Is(fbScreen, ErrModelRequestSecretWithheld) || fbScreen.Error() != screenBlocked.Error() {
		t.Fatalf("fallback screen cause = %v", fbScreen)
	}
	primScreen := newSummarizerFallbackError("p1", "m1", screenBlocked, "p2", "m2", primaryRateErr)
	if !errors.Is(primScreen, ErrModelRequestSecretWithheld) || len(primScreen.Unwrap()) != 2 {
		t.Fatalf("primary screen cause = %v", primScreen)
	}
	fbUnavail := newSummarizerFallbackError("p1", "m1", primaryRateErr, "p2", "m2", ErrLiteUnavailable)
	if fbUnavail.Selected != primaryRateErr {
		t.Fatalf("unavailable fallback hid primary failure: %v", fbUnavail.Selected)
	}

	// preserveCallerContextError edges.
	baseErr := errors.New("io fault")
	if got := preserveCallerContextError(baseErr, nil); got != baseErr {
		t.Fatalf("nil ctxErr altered err: %v", got)
	}
	if got := preserveCallerContextError(nil, context.Canceled); !errors.Is(got, context.Canceled) {
		t.Fatalf("nil err lost ctxErr: %v", got)
	}
	joined := preserveCallerContextError(baseErr, context.DeadlineExceeded)
	if !errors.Is(joined, baseErr) || !errors.Is(joined, context.DeadlineExceeded) {
		t.Fatalf("joined err missing both causes: %v", joined)
	}
	if got := preserveCallerContextError(context.DeadlineExceeded, context.DeadlineExceeded); got != context.DeadlineExceeded {
		t.Fatalf("already-matching deadline wrapped twice: %v", got)
	}

	// SummaryRevision & SummaryBudget with nil policy / unconfigured registry.
	emptyReg := NewEmptyRegistry()
	unconfSum := &RegistrySummarizer{Registry: emptyReg, Class: UtilityClassRequested}
	rev, err := unconfSum.SummaryRevision(t.Context())
	testutil.FailErr(t, "SummaryRevision nil policy", err)
	if rev != "local" {
		t.Fatalf("SummaryRevision = %q, want local", rev)
	}
	if bud := unconfSum.SummaryBudget(t.Context()); bud.Input != 0 || bud.Output != 0 {
		t.Fatalf("unconfigured SummaryBudget = %+v", bud)
	}

	// SummarizeStreamOnce + SummarizeRequired unconfigured, canceled, and coordinator-fallback error paths.
	canceledCtx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := unconfSum.SummarizeStreamOnce(canceledCtx, "s", "u", 32, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled SummarizeStreamOnce err = %v", err)
	}
	if _, err := unconfSum.SummarizeRequired(canceledCtx, "s", "u", 32); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled SummarizeRequired err = %v", err)
	}
	if _, err := unconfSum.SummarizeStreamOnce(t.Context(), "s", "u", 32, nil); !IsUnavailableUtilityError(err) {
		t.Fatalf("unconfigured SummarizeStreamOnce err = %v", err)
	}
	if _, err := unconfSum.SummarizeRequired(t.Context(), "s", "u", 32); !IsUnavailableUtilityError(err) {
		t.Fatalf("unconfigured SummarizeRequired err = %v", err)
	}

	// Unconfigured lite + failing coordinator fallback preserves SummarizerFallbackError.
	coordFail := &errOnlyProvider{id: "coord-fail", err: ErrModelRequestSecretWithheld}
	coordReg := NewEmptyRegistry()
	testutil.FailErr(t, "register coord-fail", coordReg.Register(coordFail))
	pol := newPolicyFixture(t, ModelPolicy{
		Lite:        ModelRef{ProviderID: "missing-lite", Model: "m0"},
		Coordinator: ModelRef{ProviderID: "coord-fail", Model: "m1"},
	})
	fbSum := &RegistrySummarizer{
		Registry: coordReg, Policy: pol, Scope: SettingsScopeGlobal, Class: UtilityClassRequested,
	}
	if _, err := fbSum.SummarizeRequired(t.Context(), "s", "u", 32); !SecretScreenBlocked(err) {
		t.Fatalf("expected secret-blocked coordinator fallback err, got %v", err)
	}
}
