package llm

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	ollamaprovider "github.com/lycaon/lycaon/internal/llm/providers/ollama"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestClassForPurpose(t *testing.T) {
	cases := map[string]UtilityClass{
		"session_title":      UtilityClassGift,
		"project_name":       UtilityClassGift,
		"compaction":         UtilityClassBackground,
		"curate":             UtilityClassQuality,
		"commit_draft":       UtilityClassRequested,
		"file_briefing":      UtilityClassRequested,
		"approval_rationale": UtilityClassRequested,
		"verify_detect":      UtilityClassOverlay,
		"warm_seed":          UtilityClassOverlay,
		"":                   UtilityClassQuality,
	}
	for purpose, want := range cases {
		if got := ClassForPurpose(purpose); got != want {
			t.Fatalf("ClassForPurpose(%q) = %q want %q", purpose, got, want)
		}
	}
	if UtilityClassGift.AllowsCoordinatorFallback() || UtilityClassOverlay.AllowsCoordinatorFallback() {
		t.Fatal("gift/overlay must not spend the coordinator")
	}
	if !UtilityClassQuality.AllowsTruncateFallback() || UtilityClassRequested.AllowsTruncateFallback() {
		t.Fatal("only quality may truncate")
	}
	if !UtilityClassBackground.ReturnsFailureToCaller() || UtilityClassBackground.AllowsCoordinatorFallback() {
		t.Fatal("background work must return failure to its calling subsystem")
	}
}

func silentLite() error {
	return &failure.ProviderSilentError{ProviderID: "lite", Model: "lite-model", Attempts: 1, Cause: timeoutNetError{}}
}

func TestSlotHealthOpensOnSilenceAndSkips(t *testing.T) {
	plane := NewUtilityPlane()
	var changes []SlotState
	plane.SetOnChange(func(snap SlotSnapshot) { changes = append(changes, snap.State) })

	if !plane.allowLite() {
		t.Fatal("fresh plane must admit lite")
	}
	plane.noteOutcome("ollama-1", silentLite())
	if plane.allowLite() {
		t.Fatal("a silent endpoint must open the circuit")
	}
	if snap := plane.Snapshot(); snap.State != SlotUnavailable || snap.Reason != "silent" || snap.ProviderID != "ollama-1" {
		t.Fatalf("snapshot = %+v", snap)
	}
	if len(changes) != 1 || changes[0] != SlotUnavailable {
		t.Fatalf("changes = %v", changes)
	}

	plane.Reset()
	if !plane.allowLite() || plane.Snapshot().State != SlotReady {
		t.Fatal("reset must clear the circuit")
	}
}

func TestSlotHealthIgnoresParseErrors(t *testing.T) {
	plane := NewUtilityPlane()
	plane.noteOutcome("ollama-1", errors.New("empty completion"))
	if !plane.allowLite() {
		t.Fatal("a model answer that failed to parse must not open the circuit")
	}
}

func TestSlotHealthCooldownExpires(t *testing.T) {
	h := newSlotHealth()
	var changes []SlotState
	h.setOnChange(func(snap SlotSnapshot) { changes = append(changes, snap.State) })
	h.failure("p", "silent")
	h.mu.Lock()
	h.snap.Since = time.Now().Add(-LiteUnavailableCooldown - time.Second)
	h.mu.Unlock()
	if !h.allow() {
		t.Fatal("expired cooldown must admit another attempt")
	}
	if h.snapshot().State != SlotReady {
		t.Fatal("snapshot after expiry must read ready")
	}
	if len(changes) < 2 || changes[0] != SlotUnavailable || changes[len(changes)-1] != SlotReady {
		t.Fatalf("changes = %v want unavailable then ready", changes)
	}
}

func TestUtilityLaneGiftWaitsForHolder(t *testing.T) {
	lanes := newUtilityLanes()
	ctx := context.Background()
	release, err := lanes.acquire(ctx, "ollama-1", true, UtilityClassQuality)
	testutil.FailErr(t, "first acquire", err)

	got := make(chan error, 1)
	go func() {
		rel, err := lanes.acquire(ctx, "ollama-1", true, UtilityClassGift)
		if err == nil {
			rel()
		}
		got <- err
	}()
	select {
	case err := <-got:
		t.Fatalf("gift returned early: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case err := <-got:
		testutil.FailErr(t, "gift after release", err)
	case <-time.After(time.Second):
		t.Fatal("gift did not acquire after release")
	}
}

func TestUtilityLaneGiftWaitsForCoordinatorHold(t *testing.T) {
	lanes := newUtilityLanes()
	ctx := context.Background()
	release := lanes.holdCoordinator("ollama-1")

	got := make(chan error, 1)
	go func() {
		rel, err := lanes.acquire(ctx, "ollama-1", true, UtilityClassGift)
		if err == nil {
			rel()
		}
		got <- err
	}()
	select {
	case err := <-got:
		t.Fatalf("gift returned during coordinator hold: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case err := <-got:
		testutil.FailErr(t, "gift after coordinator release", err)
	case <-time.After(time.Second):
		t.Fatal("gift did not acquire after coordinator release")
	}
}

// testOverlayWait replaces OverlayLaneWait where the assertion is that the wait
// is bounded and reports ErrLiteBusy, not how long the bound is.
// TestOverlayLaneWaitDefaultHolds pins the shipped value.
const testOverlayWait = 50 * time.Millisecond

func TestUtilityLaneOverlaySkipsWhenCoordinatorHeld(t *testing.T) {
	lanes := newUtilityLanes()
	lanes.overlayWait = testOverlayWait
	ctx := context.Background()
	release := lanes.holdCoordinator("ollama-1")
	t.Cleanup(release)

	var gotBusy atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := lanes.acquire(ctx, "ollama-1", true, UtilityClassOverlay)
		if errors.Is(err, ErrLiteBusy) {
			gotBusy.Store(true)
		}
	}()
	select {
	case <-done:
	case <-time.After(testOverlayWait + time.Second):
		t.Fatal("overlay wait exceeded")
	}
	if !gotBusy.Load() {
		t.Fatal("overlay must skip while a coordinator stream occupies the instance")
	}
}

func TestUtilityLaneOverlaySkipsWhenBusy(t *testing.T) {
	lanes := newUtilityLanes()
	lanes.overlayWait = testOverlayWait
	ctx := context.Background()
	release, err := lanes.acquire(ctx, "ollama-1", true, UtilityClassQuality)
	testutil.FailErr(t, "first acquire", err)

	var gotBusy atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := lanes.acquire(ctx, "ollama-1", true, UtilityClassOverlay)
		if errors.Is(err, ErrLiteBusy) {
			gotBusy.Store(true)
		}
	}()
	select {
	case <-done:
	case <-time.After(testOverlayWait + time.Second):
		t.Fatal("overlay wait exceeded")
	}
	if !gotBusy.Load() {
		t.Fatal("overlay must skip when the lane is held")
	}
	release()
}

func TestUtilityLaneAllowsConcurrentHosted(t *testing.T) {
	lanes := newUtilityLanes()
	ctx := context.Background()
	release1, err := lanes.acquire(ctx, "openai-1", false, UtilityClassQuality)
	testutil.FailErr(t, "first", err)
	release2, err := lanes.acquire(ctx, "openai-1", false, UtilityClassQuality)
	testutil.FailErr(t, "second", err)
	release1()
	release2()
}

func TestBackgroundSummarizeNeverSpendsCoordinator(t *testing.T) {
	lite := &namedStubProvider{id: "lite", err: silentLite()}
	coord := &namedStubProvider{id: "coord", content: "coordinator summary"}
	r := newFallbackTestSummarizer(t, lite, coord)
	r.Plane = NewUtilityPlane()
	r.Purpose = "compaction"

	_, err := r.Summarize(context.Background(), "sys", "user", 32)
	if _, ok := failure.AsProviderSilent(err); !ok {
		t.Fatalf("first error = %v", err)
	}
	if lite.calls != 1 {
		t.Fatalf("lite calls = %d want 1", lite.calls)
	}

	coord.calls = 0
	lite.calls = 0
	_, err = r.Summarize(context.Background(), "sys", "user", 32)
	if !errors.Is(err, ErrLiteUnavailable) {
		t.Fatalf("second error = %v", err)
	}
	if lite.calls != 0 {
		t.Fatalf("open circuit still called lite (%d)", lite.calls)
	}
	if coord.calls != 0 {
		t.Fatalf("background compaction spent coordinator (%d)", coord.calls)
	}
}

func TestGiftDoesNotFallBackToCoordinator(t *testing.T) {
	lite := &namedStubProvider{id: "lite", err: silentLite()}
	coord := &namedStubProvider{id: "coord", content: "should not run"}
	r := newFallbackTestSummarizer(t, lite, coord)
	r.Plane = NewUtilityPlane()
	r.Purpose = "session_title"
	r.Class = UtilityClassGift

	_, err := r.SummarizeRequired(context.Background(), "sys", "user", 32)
	if _, silent := failure.AsProviderSilent(err); !silent && !errors.Is(err, ErrLiteUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if coord.calls != 0 {
		t.Fatalf("gift spent the coordinator (%d)", coord.calls)
	}
}

type timeoutNetError struct{}

func (timeoutNetError) Error() string   { return "timeout awaiting response headers" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return true }

var _ net.Error = timeoutNetError{}

func TestLiteSlotDownReadsTypedFaults(t *testing.T) {
	down := map[string]struct {
		err  error
		want string
	}{
		"unreachable": {
			err:  &failure.ProviderUnreachableError{ProviderID: "p", Model: "m", Attempts: 1, Cause: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}},
			want: "unreachable",
		},
		"silent":     {err: silentLite(), want: "silent"},
		"overloaded": {err: &failure.ProviderOverloadedError{ProviderID: "p", Model: "m", Status: 503, Attempts: 3}, want: "overloaded"},
		"refused":    {err: &providerretry.ModelRefusedError{ProviderID: "p", Model: "m"}, want: "refused"},
	}
	for name, tc := range down {
		reason, ok := liteSlotDown(tc.err)
		if !ok || reason != tc.want {
			t.Fatalf("%s: liteSlotDown = %q/%v want %q/true", name, reason, ok, tc.want)
		}
	}
	for name, err := range map[string]error{
		"caller deadline":        context.DeadlineExceeded,
		"caller cancellation":    context.Canceled,
		"utility budget":         &utilityBudgetExceededError{cause: context.DeadlineExceeded},
		"untyped header timeout": timeoutNetError{},
		"lite busy":              ErrLiteBusy,
		"parse failure":          errors.New("empty completion"),
		"truncated answer":       &failure.ProviderOutputTruncatedError{ProviderID: "p", Model: "m"},
	} {
		if reason, ok := liteSlotDown(err); ok {
			t.Fatalf("%s: marked the slot down as %q", name, reason)
		}
	}
}

// stallingOllama serves an Ollama endpoint whose chat requests stall until
// the client leaves, like a server still loading weights; closed stops it so
// dials are refused.
func stallingOllama(t *testing.T) (srv *httptest.Server, closed func()) {
	t.Helper()
	release := make(chan struct{})
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ps":
			_, _ = w.Write([]byte(`{"models":[]}`))
		case "/api/chat":
			// The server notices a client disconnect only once the body is read.
			_, _ = io.Copy(io.Discard, r.Body)
			select {
			case <-r.Context().Done():
			case <-release:
			}
		default:
			http.NotFound(w, r)
		}
	}))
	var once sync.Once
	closed = func() {
		once.Do(func() {
			close(release)
			srv.Close()
		})
	}
	t.Cleanup(closed)
	return srv, closed
}

func ollamaLiteSummarizer(t *testing.T, baseURL string) *RegistrySummarizer {
	t.Helper()
	lite := ollamaprovider.New("ollama-1", baseURL+"/v1", "", []modelinfo.Entry{{ID: "ollama-1-model", ContextLength: 16384}})
	coord := &namedStubProvider{id: "coord", content: "unused"}
	r := newFallbackTestSummarizer(t, lite, coord)
	r.Plane = NewUtilityPlane()
	r.Purpose = "warm_seed"
	r.Class = UtilityClassOverlay
	return r
}

func TestCallerDeadlineDoesNotMarkLiteSlotDown(t *testing.T) {
	for name, call := range map[string]func(*RegistrySummarizer, context.Context) error{
		"blocking": func(r *RegistrySummarizer, ctx context.Context) error {
			_, err := r.Summarize(ctx, "sys", "user", 32)
			return err
		},
		"streaming": func(r *RegistrySummarizer, ctx context.Context) error {
			_, err := r.SummarizeStreamOnce(ctx, "sys", "user", 32, nil)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv, _ := stallingOllama(t)
			r := ollamaLiteSummarizer(t, srv.URL)
			var changes []SlotState
			r.Plane.SetOnChange(func(snap SlotSnapshot) { changes = append(changes, snap.State) })

			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			if err := call(r, ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("err = %v want the caller's deadline", err)
			}
			if snap := r.Plane.Snapshot(); snap.State != SlotReady || len(changes) != 0 {
				t.Fatalf("caller deadline marked the slot: snapshot=%+v changes=%v", snap, changes)
			}
		})
	}
}

func TestRefusedConnectionMarksLiteSlotUnreachable(t *testing.T) {
	srv, closed := stallingOllama(t)
	r := ollamaLiteSummarizer(t, srv.URL)
	closed()

	if _, err := r.SummarizeStreamOnce(t.Context(), "sys", "user", 32, nil); err == nil {
		t.Fatal("closed endpoint answered")
	}
	if snap := r.Plane.Snapshot(); snap.State != SlotUnavailable || snap.Reason != "unreachable" || snap.ProviderID != "ollama-1" {
		t.Fatalf("snapshot = %+v want unavailable/unreachable on ollama-1", snap)
	}
}

// silentEndpointProvider fails the way the transport reports a connected
// server that never sends response headers.
type silentEndpointProvider struct{ namedStubProvider }

func (s *silentEndpointProvider) Stream(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	s.calls++
	return nil, silentLite()
}

func TestSilentEndpointMarksLiteSlotDown(t *testing.T) {
	lite := &silentEndpointProvider{namedStubProvider{id: "lite"}}
	coord := &namedStubProvider{id: "coord", content: "unused"}
	r := newFallbackTestSummarizer(t, lite, coord)
	r.Plane = NewUtilityPlane()
	r.Purpose = "warm_seed"
	r.Class = UtilityClassOverlay

	_, err := r.SummarizeStreamOnce(t.Context(), "sys", "user", 32, nil)
	if _, ok := failure.AsProviderSilent(err); !ok {
		t.Fatalf("err = %v want the endpoint silence", err)
	}
	if snap := r.Plane.Snapshot(); snap.State != SlotUnavailable || snap.Reason != "silent" || snap.ProviderID != "lite" {
		t.Fatalf("snapshot = %+v want unavailable/silent on lite", snap)
	}
}

func TestLocalInferenceDriversDeclareSingleFlight(t *testing.T) {
	for name, profile := range map[string]providerprofile.Profile{
		"ollama":   providerprofile.Ollama(),
		"lmstudio": providerprofile.Lmstudio(),
		"omlx":     providerprofile.Omlx(),
	} {
		if !profile.UtilitySingleFlight {
			t.Fatalf("%s must single-flight utility calls", name)
		}
	}
	if providerprofile.Fireworks().UtilitySingleFlight {
		t.Fatal("hosted drivers must not single-flight")
	}
}

func TestResolveUtilityAttemptTimeoutUsesServiceClass(t *testing.T) {
	p := &profiledProvider{namedStubProvider: namedStubProvider{id: "lite"}}
	p.profile = providerprofile.Profile{
		UtilityCallTimeout:           providerprofile.LocalInferenceUtilityCallTimeout,
		BackgroundUtilityCallTimeout: providerprofile.LocalInferenceBackgroundUtilityCallTimeout,
		UtilitySingleFlight:          true,
	}
	if got := resolveUtilityAttemptTimeout(p, UtilityClassQuality); got != providerprofile.LocalInferenceUtilityCallTimeout {
		t.Fatalf("interactive budget = %v want %v", got, providerprofile.LocalInferenceUtilityCallTimeout)
	}
	if got := resolveUtilityAttemptTimeout(p, UtilityClassBackground); got != providerprofile.LocalInferenceBackgroundUtilityCallTimeout {
		t.Fatalf("background budget = %v want %v", got, providerprofile.LocalInferenceBackgroundUtilityCallTimeout)
	}
}

type profiledProvider struct {
	namedStubProvider
	profile providerprofile.Profile
}

func (p *profiledProvider) Profile() providerprofile.Profile { return p.profile }

func TestUtilityLaneQualityWaitsForRelease(t *testing.T) {
	lanes := newUtilityLanes()
	ctx := context.Background()
	release, err := lanes.acquire(ctx, "ollama-1", true, UtilityClassQuality)
	testutil.FailErr(t, "hold", err)

	var waited atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		rel, err := lanes.acquire(ctx, "ollama-1", true, UtilityClassQuality)
		testutil.FailErr(t, "wait acquire", err)
		waited.Store(true)
		rel()
	}()
	time.Sleep(20 * time.Millisecond)
	if waited.Load() {
		t.Fatal("quality acquired while the lane was held")
	}
	release()
	wg.Wait()
	if !waited.Load() {
		t.Fatal("quality did not acquire after release")
	}
}

// Tests narrow the lane wait per instance; a zero field still means the
// shipped constant.
func TestOverlayLaneWaitDefaultHolds(t *testing.T) {
	if OverlayLaneWait != 10*time.Second {
		t.Fatalf("OverlayLaneWait = %v want 10s", OverlayLaneWait)
	}
	if got := newUtilityLanes().overlayWaitOrDefault(); got != OverlayLaneWait {
		t.Fatalf("default overlay wait = %v want %v", got, OverlayLaneWait)
	}
}
