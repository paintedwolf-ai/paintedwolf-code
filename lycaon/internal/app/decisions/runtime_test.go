package decisions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/bialy"
	"github.com/lycaon/lycaon/internal/testutil"
)

type warmFixture struct {
	decide.Absent
	calls   int
	seen    context.Context
	failure error
}

func (w *warmFixture) Available() bool                { return true }
func (w *warmFixture) Warm(ctx context.Context) error { w.calls++; w.seen = ctx; return w.failure }

func TestDecisionRuntimeWarmsConfiguredEngineWithoutChangingAbstentionPolicy(t *testing.T) {
	engine := &warmFixture{failure: errors.New("handshake unavailable")}
	var runtime Runtime
	testutil.FailErr(t, "load configured decision engine", runtime.Load(engine))
	if runtime.Decider != engine || runtime.Rerank.Decider != engine {
		t.Fatal("ranking and decisions selected different engines")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	testutil.FailErr(t, "warm canceled engine", runtime.Warm(ctx))
	if engine.calls != 1 || engine.seen != ctx {
		t.Fatalf("warm dispatch = %d, %v; want original cancellation context", engine.calls, engine.seen)
	}
	testutil.FailErr(t, "warm unavailable handshake", runtime.Warm(t.Context()))
	if engine.calls != 2 {
		t.Fatalf("warm calls=%d, want two", engine.calls)
	}
	result, err := runtime.Decider.Decide(t.Context(), decide.HeadTurnLoad, "fixture", nil)
	if !errors.Is(err, decide.ErrUnavailable) || len(result.Answers) != 0 {
		t.Fatalf("failed warm invented decision: %+v, %v", result, err)
	}
}

func TestDecisionRuntimeDisabledEngineNeverStartsDuringWarm(t *testing.T) {
	t.Setenv(bialy.EnvDisabled, "1")
	var runtime Runtime
	testutil.FailErr(t, "load disabled decision engine", runtime.Load(nil))
	testutil.FailErr(t, "warm disabled decision engine", runtime.Warm(t.Context()))
	if _, ok := runtime.Warmer(); ok {
		t.Fatal("disabled engine was eligible to start")
	}
	for _, engine := range []decide.Decider{nil, decide.Absent{}} {
		runtime.Decider = engine
		testutil.FailErr(t, "warm absent engine", runtime.Warm(t.Context()))
	}
}

func TestDecisionRuntimeMissingCheckpointCanBootstrapWithoutClaimingAvailability(t *testing.T) {
	binary, err := os.Executable()
	testutil.FailErr(t, "locate fixture executable", err)
	dir := filepath.Join(t.TempDir(), "checkpoint")
	client := bialy.New(bialy.Config{Binary: binary, ModelDir: dir})
	runtime := Runtime{Decider: client}
	if client.Available() || client.Status() != bialy.ReasonModelMissing {
		t.Fatal("missing checkpoint reported ready")
	}
	if _, ok := runtime.Warmer(); !ok {
		t.Fatal("missing checkpoint excluded from bootstrap")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	testutil.FailErr(t, "cancel provisioning before network", runtime.Warm(ctx))
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("provisioning did not create destination: %v", err)
	}
	if client.Available() || client.Status() != bialy.ReasonModelMissing {
		t.Fatal("incomplete provisioning reported ready")
	}
	result, err := client.Decide(t.Context(), decide.HeadTurnLoad, "fixture", nil)
	if len(result.Answers) != 0 || result.Engine.Name != "" {
		t.Fatalf("incomplete provisioning invented decision: %+v, %v", result, err)
	}
}

func TestDecisionRuntimeMissingBinaryNeverProvisions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "checkpoint")
	runtime := Runtime{Decider: bialy.New(bialy.Config{Binary: filepath.Join(t.TempDir(), "missing"), ModelDir: dir})}
	if _, ok := runtime.Warmer(); ok {
		t.Fatal("missing executable eligible for bootstrap")
	}
	testutil.FailErr(t, "warm missing executable", runtime.Warm(t.Context()))
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unavailable executable provisioned checkpoint: %v", err)
	}
}
