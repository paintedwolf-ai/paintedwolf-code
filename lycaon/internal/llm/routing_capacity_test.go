package llm

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type scriptedStreamProvider struct {
	id    string
	calls int
	fn    func(n int) (<-chan modelcall.StreamChunk, error)
}

func (p *scriptedStreamProvider) ID() string { return p.id }
func (p *scriptedStreamProvider) Models() []modelcall.ModelInfo {
	return []modelcall.ModelInfo{{ID: "m"}}
}
func (p *scriptedStreamProvider) Profile() providerprofile.Profile {
	return providerprofile.Default()
}
func (p *scriptedStreamProvider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	ch, err := p.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	completion, _, err := modelcall.CollectStream(ch)
	return completion, err
}
func (p *scriptedStreamProvider) Stream(_ context.Context, _ modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	p.calls++
	return p.fn(p.calls)
}

func okStream() <-chan modelcall.StreamChunk {
	ch := make(chan modelcall.StreamChunk, 1)
	ch <- modelcall.StreamChunk{Content: "ok", Done: true}
	close(ch)
	return ch
}

func testRoutingWithProvider(t *testing.T, p modelcall.Provider, gate *CapacityGate) *RoutingClient {
	t.Helper()
	t.Setenv("LYCAON_LLM_MOCK", "")
	tmp := t.TempDir()
	catalog, err := NewProviderCatalogAt(filepath.Join(tmp, "providers.local.yaml"))
	testutil.FailErr(t, "NewProviderCatalogAt", err)
	registry, err := NewRegistry(t.Context(), catalog, providercredentials.NewAt(filepath.Join(tmp, "credential-vault.age")))
	testutil.FailErr(t, "NewRegistry", err)
	if err := registry.Register(p); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return NewRoutingClient(registry, nil, nil, nil, gate, nil, func(context.Context) (*ModelSelection, error) {
		return &ModelSelection{ProviderID: p.ID(), Model: "m"}, nil
	})
}

func TestRoutingClientHoldsAfterOverload(t *testing.T) {
	scripted := &scriptedStreamProvider{
		id: "scripted",
		fn: func(n int) (<-chan modelcall.StreamChunk, error) {
			if n < 3 {
				return nil, &failure.ProviderOverloadedError{ProviderID: "scripted", Status: 503, Detail: "busy"}
			}
			return okStream(), nil
		},
	}
	client := testRoutingWithProvider(t, scripted, NewCapacityGate(testCapacityPolicy()))
	out, err := client.Complete(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
	})
	testutil.FailErr(t, "Complete after holds", err)
	if out.Content != "ok" {
		t.Fatalf("content = %q", out.Content)
	}
	if scripted.calls != 3 {
		t.Fatalf("calls = %d want 3 (open + 2 holds)", scripted.calls)
	}
}

func TestRoutingClientDoesNotHoldRateLimit(t *testing.T) {
	scripted := &scriptedStreamProvider{
		id: "scripted",
		fn: func(int) (<-chan modelcall.StreamChunk, error) {
			return nil, &failure.ProviderRateLimitedError{ProviderID: "scripted", Status: 429, Detail: "quota"}
		},
	}
	client := testRoutingWithProvider(t, scripted, NewCapacityGate(testCapacityPolicy()))
	_, err := client.Complete(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
	})
	if _, ok := failure.AsProviderRateLimited(err); !ok {
		t.Fatalf("err = %v want rate limited", err)
	}
	if scripted.calls != 1 {
		t.Fatalf("calls = %d want 1", scripted.calls)
	}
}

func TestRoutingClientWithoutGateDoesNotHold(t *testing.T) {
	scripted := &scriptedStreamProvider{
		id: "scripted",
		fn: func(int) (<-chan modelcall.StreamChunk, error) {
			return nil, &failure.ProviderOverloadedError{ProviderID: "scripted", Status: 503, Detail: "busy"}
		},
	}
	client := testRoutingWithProvider(t, scripted, nil)
	_, err := client.Complete(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
	})
	if _, ok := failure.AsProviderOverloaded(err); !ok {
		t.Fatalf("err = %v want overloaded", err)
	}
	if scripted.calls != 1 {
		t.Fatalf("calls = %d want 1", scripted.calls)
	}
}

type admissionProvider struct {
	scriptedStreamProvider
	local bool
}

func (p *admissionProvider) Profile() providerprofile.Profile {
	profile := providerprofile.Default()
	profile.UtilitySingleFlight = p.local
	return profile
}

func TestRoutingAdmissionCoversOpeningAndStream(t *testing.T) {
	opening := make(chan struct{})
	opened := make(chan struct{})
	upstream := make(chan modelcall.StreamChunk)
	p := &admissionProvider{local: true, scriptedStreamProvider: scriptedStreamProvider{id: "local", fn: func(int) (<-chan modelcall.StreamChunk, error) {
		close(opening)
		<-opened
		return upstream, nil
	}}}
	client := testRoutingWithProvider(t, p, nil)
	client.plane = NewUtilityPlane()
	client.plane.lanes.overlayWait = time.Millisecond
	result := make(chan (<-chan modelcall.StreamChunk), 1)
	go func() {
		ch, err := client.Stream(t.Context(), modelcall.CompletionRequest{})
		if err != nil {
			t.Error(err)
			return
		}
		result <- ch
	}()
	<-opening
	assertOverlayBusy(t, client.plane, p)
	close(opened)
	ch := <-result
	assertOverlayBusy(t, client.plane, p)
	close(upstream)
	for range ch {
	}
	done, err := client.plane.acquireLane(t.Context(), p, UtilityClassRequested)
	testutil.FailErr(t, "utility after stream", err)
	done()
}

func assertOverlayBusy(t *testing.T, plane *UtilityPlane, p modelcall.Provider) {
	t.Helper()
	summarizer := &RegistrySummarizer{Plane: plane, Class: UtilityClassOverlay, Purpose: "warm_seed"}
	_, err := summarizer.completeUtility(t.Context(), p, "m", modelcall.CompletionRequest{})
	if !errors.Is(err, ErrLiteBusy) {
		t.Fatalf("overlay admission = %v want busy", err)
	}
	if plane.Snapshot().State != SlotReady {
		t.Fatal("admission contention marked lite unavailable")
	}
}

func TestRoutingAdmissionReleasesOpeningFailure(t *testing.T) {
	failed := errors.New("opening failed")
	p := &admissionProvider{local: true, scriptedStreamProvider: scriptedStreamProvider{id: "local", fn: func(int) (<-chan modelcall.StreamChunk, error) { return nil, failed }}}
	client := testRoutingWithProvider(t, p, nil)
	client.plane = NewUtilityPlane()
	_, err := client.Stream(t.Context(), modelcall.CompletionRequest{})
	if !errors.Is(err, failed) {
		t.Fatalf("stream = %v", err)
	}
	done, err := client.plane.acquireLane(t.Context(), p, UtilityClassRequested)
	testutil.FailErr(t, "utility after open failure", err)
	done()
}

func TestRoutingAdmissionPreservesHostedConcurrency(t *testing.T) {
	opening := make(chan struct{})
	opened := make(chan struct{})
	p := &admissionProvider{scriptedStreamProvider: scriptedStreamProvider{id: "hosted", fn: func(int) (<-chan modelcall.StreamChunk, error) {
		close(opening)
		<-opened
		return okStream(), nil
	}}}
	client := testRoutingWithProvider(t, p, nil)
	client.plane = NewUtilityPlane()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, err := client.Complete(t.Context(), modelcall.CompletionRequest{})
		if err != nil {
			t.Error(err)
		}
	}()
	<-opening
	done, err := client.plane.acquireLane(t.Context(), p, UtilityClassOverlay)
	testutil.FailErr(t, "hosted utility during foreground", err)
	done()
	close(opened)
	<-finished
}

func TestStreamAdmissionDrainsBeforeRelease(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	upstream := make(chan modelcall.StreamChunk)
	released := make(chan struct{})
	out := releaseAfterStream(ctx, upstream, func() { close(released) })
	cancel()
	upstream <- modelcall.StreamChunk{Content: "abandoned"}
	select {
	case <-released:
		t.Fatal("admission released before upstream closed")
	default:
	}
	close(upstream)
	for range out {
	}
	<-released
}

func TestStreamAdmissionHandlesAbsentStream(t *testing.T) {
	released := false
	if out := releaseAfterStream(t.Context(), nil, func() { released = true }); out != nil || !released {
		t.Fatal("absent stream retained admission")
	}
	upstream := okStream()
	if out := releaseAfterStream(t.Context(), upstream, nil); out != upstream {
		t.Fatal("unrestricted stream was wrapped")
	}
}
