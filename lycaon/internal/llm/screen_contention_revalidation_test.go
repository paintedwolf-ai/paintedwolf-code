package llm

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/exposure"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestContendedWaitRevalidatesAndRescreensChangedEvidence(t *testing.T) {
	const rotatedSecret = "rotated-contended-secret-77889900"

	bInProvider := make(chan struct{})
	releaseB := make(chan struct{})
	var bOnce sync.Once

	provider := &concurrencyTrackingProvider{
		id:           "ollama",
		singleFlight: true,
		onCall: func(_ context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
			if strings.Contains(req.Messages[len(req.Messages)-1].Content, "from-B") {
				bOnce.Do(func() {
					close(bInProvider)
					<-releaseB
				})
			}
			return okStream(), nil
		},
	}

	var evidenceMu sync.Mutex
	var rotated bool
	screen := NewModelSecretScreen(modelScreenMatcher(t), nil)
	screen.SetManagedSecretEvidence(func(_ context.Context, projectID, _ string) ([]secretmatch.Remembered, error) {
		evidenceMu.Lock()
		active := rotated
		evidenceMu.Unlock()
		if projectID == "proj-A" && active {
			return []secretmatch.Remembered{{
				Secret: rotatedSecret, Name: "ROTATED", RuleID: secretmatch.ManagedRuleID,
				Reference: "{{paintedwolf-secret:rotated-ref}}", NonDisclosable: true,
			}}, nil
		}
		return nil, nil
	})

	reg := newScreenTestRegistry(t, screen, provider)
	plane := NewUtilityPlane()
	policy := newPolicyFixture(t, ModelPolicy{Lite: ModelRef{ProviderID: "ollama", Model: "m"}})

	sumB := &RegistrySummarizer{
		Registry: reg, Policy: policy, Plane: plane, Scope: SettingsScopeGlobal,
		ProjectID: "proj-B", Class: UtilityClassRequested,
	}
	sumA := &RegistrySummarizer{
		Registry: reg, Policy: policy, Plane: plane, Scope: SettingsScopeGlobal,
		ProjectID: "proj-A", Class: UtilityClassRequested,
	}

	bDone := make(chan error, 1)
	go func() {
		_, err := sumB.SummarizeRequired(t.Context(), "sys", "from-B", 32)
		bDone <- err
	}()
	<-bInProvider

	aDone := make(chan error, 1)
	go func() {
		_, err := sumA.SummarizeRequired(t.Context(), "sys", "payload "+rotatedSecret, 32)
		aDone <- err
	}()
	testutil.WaitFor(t, "proj-A queued on single-flight lane", func() bool {
		plane.lanes.mu.Lock()
		defer plane.lanes.mu.Unlock()
		lane := plane.lanes.lanes[modelcall.CapacityKey(provider)]
		return lane != nil && len(lane.backgroundQueue) == 1
	})

	// Rotate Project A's evidence while A is waiting on capacity, then release B.
	evidenceMu.Lock()
	rotated = true
	evidenceMu.Unlock()
	close(releaseB)

	testutil.FailErr(t, "proj-B finish", <-bDone)
	testutil.FailErr(t, "proj-A finish after rescreen", <-aDone)

	if provider.peakFlight.Load() > 1 {
		t.Fatalf("peak concurrency = %d, want 1", provider.peakFlight.Load())
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.received) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(provider.received))
	}
	sentA := provider.received[1].Messages[len(provider.received[1].Messages)-1].Content
	exposure.Absent(t, sentA, rotatedSecret)
	if !strings.Contains(sentA, "{{paintedwolf-secret:rotated-ref}}") {
		t.Fatalf("expected substituted reference after rescreen, got %q", sentA)
	}
}

func TestUnchangedContentionAvoidsRescreenAndPreservesForegroundAndFIFOOrder(t *testing.T) {
	bInProvider := make(chan struct{})
	releaseB := make(chan struct{})
	var bOnce sync.Once
	var orderMu sync.Mutex
	var order []string

	provider := &concurrencyTrackingProvider{
		id:           "ollama",
		singleFlight: true,
		onCall: func(_ context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
			msg := req.Messages[len(req.Messages)-1].Content
			for _, tag := range []string{"hold", "w1", "w2", "fg"} {
				if strings.Contains(msg, "tag:"+tag) {
					orderMu.Lock()
					order = append(order, tag)
					orderMu.Unlock()
				}
			}
			if strings.Contains(msg, "tag:hold") {
				bOnce.Do(func() {
					close(bInProvider)
					<-releaseB
				})
			}
			return okStream(), nil
		},
	}

	var askCount atomic.Int32
	screen := NewModelSecretScreen(modelScreenMatcher(t), func(_ context.Context, _ secretmatch.Alert) (secretmatch.Resolution, error) {
		askCount.Add(1)
		return secretmatch.Resolution{Decision: secretmatch.SendRedacted}, nil
	})
	reg := newScreenTestRegistry(t, screen, provider)
	plane := NewUtilityPlane()
	policy := newPolicyFixture(t, ModelPolicy{
		Lite:        ModelRef{ProviderID: "ollama", Model: "m"},
		Coordinator: ModelRef{ProviderID: "ollama", Model: "m"},
	})
	sum := &RegistrySummarizer{Registry: reg, Policy: policy, Plane: plane, Scope: SettingsScopeGlobal, Class: UtilityClassRequested}
	fgClient := NewRoutingClient(reg, nil, nil, plane, nil, nil, func(context.Context) (*ModelSelection, error) {
		return &ModelSelection{ProviderID: "ollama", Model: "m"}, nil
	})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := sum.SummarizeRequired(t.Context(), "s", "tag:hold", 32)
		testutil.FailErr(t, "hold", err)
	}()
	<-bInProvider

	// Enqueue w1 (carrying a shape-detected secret) then w2 in FIFO background order, then fg.
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := sum.SummarizeRequired(t.Context(), "s", "tag:w1 "+modelScreenGitHubToken, 32)
		testutil.FailErr(t, "w1", err)
	}()
	testutil.WaitFor(t, "w1 queued", func() bool {
		plane.lanes.mu.Lock()
		defer plane.lanes.mu.Unlock()
		lane := plane.lanes.lanes[modelcall.CapacityKey(provider)]
		return lane != nil && len(lane.backgroundQueue) == 1
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := sum.SummarizeRequired(t.Context(), "s", "tag:w2", 32)
		testutil.FailErr(t, "w2", err)
	}()
	testutil.WaitFor(t, "w2 queued", func() bool {
		plane.lanes.mu.Lock()
		defer plane.lanes.mu.Unlock()
		lane := plane.lanes.lanes[modelcall.CapacityKey(provider)]
		return lane != nil && len(lane.backgroundQueue) == 2
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx := curationctx.WithSession(t.Context(), curationctx.Session{SessionID: "fg-sess", ProjectID: "proj-fg"})
		_, err := fgClient.Complete(ctx, modelcall.CompletionRequest{
			Messages: []api.Message{{Role: api.MessageRoleUser, Content: "tag:fg"}},
		})
		testutil.FailErr(t, "fg", err)
	}()
	awaitForegroundWaiter(t, plane.lanes, modelcall.CapacityKey(provider))

	close(releaseB)
	wg.Wait()

	if askCount.Load() != 1 {
		t.Fatalf("ask called %d times across unchanged contention, want 1", askCount.Load())
	}
	orderMu.Lock()
	gotOrder := slices.Clone(order)
	orderMu.Unlock()
	if !slices.Equal(gotOrder, []string{"hold", "fg", "w1", "w2"}) {
		t.Fatalf("dispatch order = %v, want [hold fg w1 w2]", gotOrder)
	}
}

func TestProtectedRefusalCancellationAndOverloadAccounting(t *testing.T) {
	lite := &concurrencyTrackingProvider{id: "ollama-lite", singleFlight: true}
	coord := &concurrencyTrackingProvider{id: "cloud-coord", singleFlight: false}

	screen := NewModelSecretScreen(modelScreenMatcher(t), func(_ context.Context, _ secretmatch.Alert) (secretmatch.Resolution, error) {
		return secretmatch.Resolution{Decision: secretmatch.Withhold, Guidance: "keep local"}, nil
	})
	reg := newScreenTestRegistry(t, screen, lite, coord)
	plane := NewUtilityPlane()
	ledger := &receiptLedgerTracker{}
	policy := newPolicyFixture(t, ModelPolicy{
		Lite:        ModelRef{ProviderID: "ollama-lite", Model: "m"},
		Coordinator: ModelRef{ProviderID: "cloud-coord", Model: "m"},
	})
	sum := &RegistrySummarizer{
		Registry: reg, Policy: policy, Plane: plane, Cost: ledger,
		Scope: SettingsScopeGlobal, Purpose: "file_briefing", Class: UtilityClassRequested,
	}

	// Streaming protected refusal never falls through to blocking Summarize or coordinator fallback,
	// voids the receipt (zero billable generation), and keeps Ollama healthy.
	_, err := sum.SummarizeStream(t.Context(), "sys", "secret "+modelScreenGitHubToken, 64, nil)
	if !errors.Is(err, ErrModelRequestSecretWithheld) {
		t.Fatalf("SummarizeStream err = %v, want withheld", err)
	}
	if lite.calls.Load() != 0 || coord.calls.Load() != 0 {
		t.Fatalf("provider calls on withheld request: lite=%d coord=%d, want 0", lite.calls.Load(), coord.calls.Load())
	}
	if !slices.Equal(ledger.ops, []string{"begin", "void"}) {
		t.Fatalf("ledger ops = %v, want [begin void]", ledger.ops)
	}
	if plane.Snapshot().State != SlotReady {
		t.Fatal("protected refusal marked local slot unhealthy")
	}

	// Cancellation during queueing releases priority cleanly, voids receipt, and leaves slot healthy.
	holdRel, err := plane.acquireForeground(t.Context(), lite)
	testutil.FailErr(t, "hold foreground", err)
	ledger.ops = nil
	qCtx, qCancel := context.WithTimeout(t.Context(), 15*time.Millisecond)
	defer qCancel()
	_, qErr := sum.SummarizeRequired(qCtx, "sys", "clean text", 64)
	if !errors.Is(qErr, context.DeadlineExceeded) {
		t.Fatalf("queue cancel err = %v, want DeadlineExceeded", qErr)
	}
	holdRel()
	if !slices.Equal(ledger.ops, []string{"begin", "void"}) {
		t.Fatalf("queue cancel ledger ops = %v, want [begin void]", ledger.ops)
	}
	if plane.Snapshot().State != SlotReady {
		t.Fatal("queue cancellation marked local slot unhealthy")
	}

	// Overload hold retry releases single-flight capacity during backoff so ready utility calls proceed.
	var attempts atomic.Int32
	overloaded := &concurrencyTrackingProvider{
		id:           "ollama-retry",
		singleFlight: true,
		onCall: func(_ context.Context, _ modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
			if attempts.Add(1) == 1 {
				return nil, &failure.ProviderOverloadedError{ProviderID: "ollama-retry", Status: 503, Detail: "busy"}
			}
			return okStream(), nil
		},
	}
	testutil.FailErr(t, "register retry provider", reg.Register(overloaded))
	capPolicy := testCapacityPolicy()
	capPolicy.Holds = []time.Duration{40 * time.Millisecond}
	client := NewRoutingClient(reg, nil, nil, plane, NewCapacityGate(capPolicy), nil, func(context.Context) (*ModelSelection, error) {
		return &ModelSelection{ProviderID: "ollama-retry", Model: "m"}, nil
	})
	retryDone := make(chan error, 1)
	go func() {
		_, cErr := client.Complete(t.Context(), modelcall.CompletionRequest{
			Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}},
		})
		retryDone <- cErr
	}()
	testutil.WaitFor(t, "first attempt returned 503", func() bool { return attempts.Load() == 1 })
	relDuringBackoff, laneErr := plane.acquireLane(t.Context(), overloaded, UtilityClassOverlay)
	testutil.FailErr(t, "overlay acquires during overload hold backoff", laneErr)
	relDuringBackoff()
	testutil.FailErr(t, "retry Complete", <-retryDone)
}
