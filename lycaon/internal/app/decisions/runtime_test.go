package decisions

import (
	"context"
	"errors"
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
