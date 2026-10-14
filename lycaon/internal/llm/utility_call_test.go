package llm

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestResolveUtilityCallTimeout(t *testing.T) {
	if got := resolveUtilityCallTimeout(providerprofile.Profile{}, UtilityClassQuality); got != defaultUtilityCallTimeout {
		t.Fatalf("default budget = %v want %v", got, defaultUtilityCallTimeout)
	}
	if got := resolveUtilityCallTimeout(providerprofile.Profile{UtilityCallTimeout: time.Minute}, UtilityClassQuality); got != time.Minute {
		t.Fatalf("declared budget = %v want 1m", got)
	}
}

func TestForegroundPreemptsBackgroundWithoutReleasingItsOwnLease(t *testing.T) {
	lanes := newUtilityLanes()
	work, lateRelease, err := lanes.acquireBackground(t.Context(), "local", true)
	testutil.FailErr(t, "admit background", err)
	foreground, err := lanes.acquireForeground(t.Context(), "local", true)
	testutil.FailErr(t, "admit foreground", err)
	defer foreground()
	if !errors.Is(work.Err(), context.Canceled) {
		t.Fatal("foreground did not cancel background synthesis")
	}
	lateRelease()
	lanes.mu.Lock()
	busy := lanes.lanes["local"].busy
	lanes.mu.Unlock()
	if !busy {
		t.Fatal("late synthesis released the foreground lease")
	}
}

func TestCanceledForegroundDoesNotPreemptSynthesis(t *testing.T) {
	lanes := newUtilityLanes()
	work, release, err := lanes.acquireBackground(t.Context(), "local", true)
	testutil.FailErr(t, "admit background", err)
	defer release()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = lanes.acquireForeground(ctx, "local", true)
	if !errors.Is(err, context.Canceled) || work.Err() != nil {
		t.Fatal("canceled foreground changed admitted synthesis")
	}
}

func TestLocalInferenceDriversDeclareTheColdStartBudget(t *testing.T) {
	for name, profile := range map[string]providerprofile.Profile{
		"ollama":            providerprofile.Ollama(),
		"lmstudio":          providerprofile.Lmstudio(),
		"omlx":              providerprofile.Omlx(),
		"openai-compatible": providerprofile.LocalInference(providerprofile.OpenAI()),
		"litellm-proxy":     providerprofile.Litellm(),
	} {
		t.Run(name, func(t *testing.T) {
			if profile.StreamResponseHeaderTimeout != providerprofile.LocalInferenceStreamResponseHeaderTimeout {
				t.Fatalf("stream header timeout = %v want %v", profile.StreamResponseHeaderTimeout, providerprofile.LocalInferenceStreamResponseHeaderTimeout)
			}
			if profile.CompleteResponseHeaderTimeout != providerprofile.LocalInferenceCompleteResponseHeaderTimeout {
				t.Fatalf("complete header timeout = %v want %v", profile.CompleteResponseHeaderTimeout, providerprofile.LocalInferenceCompleteResponseHeaderTimeout)
			}
			if got := resolveUtilityCallTimeout(profile, UtilityClassQuality); got != providerprofile.LocalInferenceUtilityCallTimeout {
				t.Fatalf("budget = %v want %v", got, providerprofile.LocalInferenceUtilityCallTimeout)
			}
			if got := resolveUtilityCallTimeout(profile, UtilityClassBackground); got != providerprofile.LocalInferenceBackgroundUtilityCallTimeout {
				t.Fatalf("background budget = %v want %v", got, providerprofile.LocalInferenceBackgroundUtilityCallTimeout)
			}
		})
	}
	if resolveUtilityCallTimeout(providerprofile.Fireworks(), UtilityClassQuality) != defaultUtilityCallTimeout {
		t.Fatal("a hosted driver must not take the local model-load budget")
	}
}

// blockingProvider waits for its request deadline.
type blockingProvider struct {
	stubCuratorProvider
}

func (p *blockingProvider) Complete(ctx context.Context, _ modelcall.CompletionRequest) (*modelcall.Completion, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// Fallback attempts receive a fresh deadline.
func TestUtilityFallbackGetsItsOwnBudgetAfterTheLiteAttemptExpires(t *testing.T) {
	lite := &blockingProvider{stubCuratorProvider: stubCuratorProvider{id: "lite", budget: 20 * time.Millisecond}}
	coordinator := &stubCuratorProvider{id: "coord", responses: []string{"fallback answer"}}

	r := newTestRegistrySummarizer(t, lite)
	testutil.FailErr(t, "Register coordinator", r.Registry.Register(coordinator))
	r.Policy = NewInMemoryPolicyStore(ModelPolicy{
		Lite:        ModelRef{ProviderID: "lite", Model: "lite"},
		Coordinator: ModelRef{ProviderID: "coord", Model: "coord"},
	})

	out, err := r.SummarizeRequired(context.Background(), "system", "user", 100)
	testutil.FailErr(t, "SummarizeRequired", err)
	if out != "fallback answer" {
		t.Fatalf("content = %q want the fallback provider's answer", out)
	}
}

// Canceled admission creates no usage receipt.
func TestUtilityCallOnADeadContextOpensNoReceipt(t *testing.T) {
	ledger := &receiptLedgerTracker{}
	r := newTestRegistrySummarizer(t, &blockingProvider{stubCuratorProvider: stubCuratorProvider{id: "lite"}})
	r.Cost = ledger
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := r.SummarizeRequired(ctx, "system", "user", 100); err == nil {
		t.Fatal("summarize on a canceled context returned no error")
	}
	ledger.assertOps(t)
}

func awaitForegroundWaiter(t *testing.T, lanes *utilityLanes, id string) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		lanes.mu.Lock()
		lane := lanes.lanes[id]
		waiting := lane != nil && lane.foregroundWaiting > 0
		lanes.mu.Unlock()
		if waiting {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("foreground did not enter admission")
		default:
			runtime.Gosched()
		}
	}
}

func TestAdmissionPrioritizesWaitingForeground(t *testing.T) {
	lanes := newUtilityLanes()
	release, err := lanes.acquire(t.Context(), "local", true, UtilityClassQuality)
	testutil.FailErr(t, "hold utility", err)
	t.Cleanup(release)
	foreground := make(chan func(), 1)
	go func() {
		done, err := lanes.acquireForeground(t.Context(), "local", true)
		if err != nil {
			t.Error(err)
			return
		}
		foreground <- done
	}()
	awaitForegroundWaiter(t, lanes, "local")
	utility := make(chan func(), 1)
	go func() {
		done, err := lanes.acquire(t.Context(), "local", true, UtilityClassRequested)
		if err != nil {
			t.Error(err)
			return
		}
		utility <- done
	}()
	release()
	var finish func()
	select {
	case finish = <-foreground:
	case done := <-utility:
		done()
		t.Fatal("utility overtook queued foreground")
	case <-time.After(time.Second):
		t.Fatal("foreground was not admitted")
	}
	t.Cleanup(finish)
	select {
	case done := <-utility:
		done()
		t.Fatal("utility overlapped foreground")
	default:
	}
	finish()
	select {
	case done := <-utility:
		done()
	case <-time.After(time.Second):
		t.Fatal("utility did not resume")
	}
}

func TestAdmissionCancellationReleasesForegroundPriority(t *testing.T) {
	lanes := newUtilityLanes()
	release, err := lanes.acquire(t.Context(), "local", true, UtilityClassQuality)
	testutil.FailErr(t, "hold utility", err)
	t.Cleanup(release)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := lanes.acquireForeground(ctx, "local", true); result <- err }()
	awaitForegroundWaiter(t, lanes, "local")
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
	release()
	done, err := lanes.acquire(t.Context(), "local", true, UtilityClassQuality)
	testutil.FailErr(t, "utility after canceled foreground", err)
	done()
}

func TestAdmissionRejectsCanceledContextBeforeTakingLane(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	lanes := newUtilityLanes()
	_, err := lanes.acquire(ctx, "local", true, UtilityClassOverlay)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled overlay = %v", err)
	}
	done, err := lanes.acquireForeground(t.Context(), "local", true)
	testutil.FailErr(t, "foreground after cancellation", err)
	done()
	done()
	done, err = lanes.acquire(t.Context(), "local", true, UtilityClassRequested)
	testutil.FailErr(t, "utility after repeated release", err)
	done()
}

func TestAdmissionIsolationAndOptionalPlane(t *testing.T) {
	var lanes *utilityLanes
	done, err := lanes.acquireForeground(t.Context(), "local", true)
	testutil.FailErr(t, "absent lane", err)
	done()
	var plane *UtilityPlane
	done, err = plane.acquireForeground(t.Context(), nil)
	testutil.FailErr(t, "absent plane", err)
	done()
	lanes = newUtilityLanes()
	done, err = lanes.acquireForeground(t.Context(), "local-a", true)
	testutil.FailErr(t, "first provider", err)
	defer done()
	other, err := lanes.acquire(t.Context(), "local-b", true, UtilityClassQuality)
	testutil.FailErr(t, "independent provider", err)
	other()
}

func TestBackgroundSynthesisWaitsForLocalForegroundIdle(t *testing.T) {
	lanes := newUtilityLanes()
	foreground, err := lanes.acquireForeground(t.Context(), "local", true)
	testutil.FailErr(t, "admit foreground", err)
	foreground()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	_, _, err = lanes.acquireBackground(ctx, "local", true)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("background bypassed idle wait: %v", err)
	}
	_, release, err := lanes.acquireBackground(t.Context(), "hosted", false)
	testutil.FailErr(t, "hosted background", err)
	release()
}

func TestBackgroundSynthesisDoesNotPublishTurnErrors(t *testing.T) {
	life := &recordingLifecycle{}
	sum := &RegistrySummarizer{Class: UtilityClassBackground, Lifecycle: life}
	id, occupied := sum.beginLaneCall(occupiedSessionCtx(), "synthesis", "local", "model")
	failure := errors.New("isolated synthesis failure")
	sum.endLaneCall(occupiedSessionCtx(), occupied, id, "local", "model", &failure)
	if occupied || len(life.evs) != 0 {
		t.Fatal("background synthesis changed foreground activity")
	}
}
