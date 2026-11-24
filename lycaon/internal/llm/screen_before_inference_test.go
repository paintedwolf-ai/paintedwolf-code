package llm

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/credentials/credentialstest"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/exposure"
	"github.com/lycaon/lycaon/pkg/api"
)

type concurrencyTrackingProvider struct {
	id           string
	singleFlight bool
	group        *modelcall.CapacityGroup
	budgetWindow int

	inFlight    atomic.Int32
	peakFlight  atomic.Int32
	calls       atomic.Int32
	budgetCalls atomic.Int32

	mu       sync.Mutex
	received []modelcall.CompletionRequest
	onCall   func(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error)
}

func (p *concurrencyTrackingProvider) ID() string { return p.id }
func (p *concurrencyTrackingProvider) Models() []modelcall.ModelInfo {
	return []modelcall.ModelInfo{{ID: "m"}}
}
func (p *concurrencyTrackingProvider) CapacityGroup() *modelcall.CapacityGroup { return p.group }
func (p *concurrencyTrackingProvider) Profile() providerprofile.Profile {
	prof := providerprofile.Default()
	prof.UtilitySingleFlight = p.singleFlight
	return prof
}

func (p *concurrencyTrackingProvider) enter(req modelcall.CompletionRequest) func() {
	cur := p.inFlight.Add(1)
	for {
		old := p.peakFlight.Load()
		if cur <= old || p.peakFlight.CompareAndSwap(old, cur) {
			break
		}
	}
	p.calls.Add(1)
	p.mu.Lock()
	p.received = append(p.received, cloneCompletionRequest(req))
	p.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { p.inFlight.Add(-1) }) }
}

func (p *concurrencyTrackingProvider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	leave := p.enter(req)
	defer leave()
	if p.onCall != nil {
		ch, err := p.onCall(ctx, req)
		if err != nil {
			return nil, err
		}
		out, _, collectErr := modelcall.CollectStream(ch)
		return out, collectErr
	}
	return &modelcall.Completion{Content: "ack:" + p.id}, nil
}

func (p *concurrencyTrackingProvider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	leave := p.enter(req)
	if p.onCall != nil {
		ch, err := p.onCall(ctx, req)
		if err != nil {
			leave()
			return nil, err
		}
		out := make(chan modelcall.StreamChunk)
		go func() {
			defer close(out)
			defer leave()
			for chunk := range ch {
				out <- chunk
			}
		}()
		return out, nil
	}
	ch := make(chan modelcall.StreamChunk, 1)
	ch <- modelcall.StreamChunk{Content: "ack:" + p.id, Done: true}
	close(ch)
	leave()
	return ch, nil
}

func (p *concurrencyTrackingProvider) RequestBudget(_ context.Context, _ modelcall.CompletionRequest) modelcall.RequestBudget {
	cur := p.inFlight.Add(1)
	for {
		old := p.peakFlight.Load()
		if cur <= old || p.peakFlight.CompareAndSwap(old, cur) {
			break
		}
	}
	p.budgetCalls.Add(1)
	p.inFlight.Add(-1)
	win := p.budgetWindow
	if win == 0 {
		win = 8192
	}
	return modelcall.RequestBudget{Window: win, Input: 128, Output: 512, MinimumOutput: 64, Headroom: 64}
}

func newScreenTestRegistry(t *testing.T, screen OutboundSecretScreen, providers ...modelcall.Provider) *Registry {
	t.Helper()
	t.Setenv("LYCAON_LLM_MOCK", "")
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	tmp := t.TempDir()
	cat, err := NewProviderCatalogAt(tmp + "/providers.local.yaml")
	testutil.FailErr(t, "catalog", err)
	reg, err := NewRegistry(t.Context(), cat, credentialstest.Open(t, tmp+"/vault.age"), nil)
	testutil.FailErr(t, "registry", err)
	reg.SetOutboundSecretScreen(screen)
	for _, p := range providers {
		testutil.FailErr(t, "register "+p.ID(), reg.Register(p))
	}
	return reg
}

func TestScreenBeforeInferenceAllowsReadyPeerAcrossSharedSingleFlight(t *testing.T) {
	const secretA = "projA-managed-secret-99112233"
	const secretB = "projB-managed-secret-44556677"

	for _, useStream := range []bool{false, true} {
		name := "complete"
		if useStream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			group := modelcall.NewCapacityGroup()
			lite := &concurrencyTrackingProvider{id: "ollama-lite", singleFlight: true, group: group}
			coord := &concurrencyTrackingProvider{id: "ollama-coord", singleFlight: true, group: group}

			aEnteredScreen := make(chan struct{})
			releaseAScreen := make(chan struct{})
			var aOnce sync.Once

			screen := NewModelSecretScreen(modelScreenMatcher(t), nil)
			screen.SetManagedSecretEvidence(func(_ context.Context, projectID, _ string) ([]secretmatch.Remembered, error) {
				if projectID == "proj-A" {
					aOnce.Do(func() {
						close(aEnteredScreen)
						<-releaseAScreen
					})
					return []secretmatch.Remembered{{
						Secret: secretA, Name: "TOKEN_A", RuleID: secretmatch.ManagedRuleID,
						Reference: "{{paintedwolf-secret:ref-a}}", NonDisclosable: true,
					}}, nil
				}
				return []secretmatch.Remembered{{
					Secret: secretB, Name: "TOKEN_B", RuleID: secretmatch.ManagedRuleID,
					Reference: "{{paintedwolf-secret:ref-b}}", NonDisclosable: true,
				}}, nil
			})

			reg := newScreenTestRegistry(t, screen, lite, coord)
			plane := NewUtilityPlane()
			plane.lanes.overlayWait = 25 * time.Millisecond
			policy := newPolicyFixture(t, ModelPolicy{
				Lite:        ModelRef{ProviderID: "ollama-lite", Model: "m"},
				Coordinator: ModelRef{ProviderID: "ollama-coord", Model: "m"},
			})

			sumA := &RegistrySummarizer{
				Registry: reg, Policy: policy, Plane: plane, Scope: SettingsScopeGlobal,
				ProjectID: "proj-A", Purpose: "file_briefing", Class: UtilityClassRequested,
			}
			sumB := &RegistrySummarizer{
				Registry: reg, Policy: policy, Plane: plane, Scope: SettingsScopeGlobal,
				ProjectID: "proj-B", Purpose: "warm_seed", Class: UtilityClassOverlay,
			}
			fgClient := NewRoutingClient(reg, nil, nil, plane, nil, nil, func(context.Context) (*ModelSelection, error) {
				return &ModelSelection{ProviderID: "ollama-coord", Model: "m"}, nil
			})

			aDone := make(chan error, 1)
			go func() {
				var err error
				if useStream {
					_, err = sumA.SummarizeStreamOnce(t.Context(), "sys", "use "+secretA, 64, nil)
				} else {
					_, err = sumA.SummarizeRequired(t.Context(), "sys", "use "+secretA, 64)
				}
				aDone <- err
			}()
			<-aEnteredScreen

			// Project A is held in credential screening: zero inference capacity is occupied!
			// Both Project B's overlay utility AND a foreground coordinator alias call succeed immediately.
			var outB string
			var errB error
			if useStream {
				outB, errB = sumB.SummarizeStreamOnce(t.Context(), "sys", "use "+secretB, 64, nil)
			} else {
				outB, errB = sumB.SummarizeRequired(t.Context(), "sys", "use "+secretB, 64)
			}
			testutil.FailErr(t, "proj-B utility while proj-A screens", errB)
			if !strings.Contains(outB, "ack:ollama-lite") {
				t.Fatalf("unexpected proj-B output %q", outB)
			}

			fgCtx := curationctx.WithSession(t.Context(), curationctx.Session{ProjectID: "proj-B", SessionID: "s-b"})
			fgOut, fgErr := fgClient.Complete(fgCtx, modelcall.CompletionRequest{
				Messages: []api.Message{{Role: api.MessageRoleUser, Content: "coord " + secretB}},
			})
			testutil.FailErr(t, "proj-B foreground alias while proj-A screens", fgErr)
			if !strings.Contains(fgOut.Content, "ack:ollama-coord") {
				t.Fatalf("unexpected fg output %q", fgOut.Content)
			}

			// Direct Service.RequestBudget also acquires and releases cleanly while A screens.
			svc := &Service{Registry: reg, Utility: plane}
			budget := svc.RequestBudget(t.Context(), "ollama-coord", modelcall.CompletionRequest{Model: "m"})
			testutil.FailErr(t, "RequestBudget while A screens", budget.Err)
			if coord.budgetCalls.Load() != 1 {
				t.Fatalf("expected 1 RequestBudget call, got %d", coord.budgetCalls.Load())
			}

			// Release Project A's screening barrier and verify completion.
			close(releaseAScreen)
			testutil.FailErr(t, "proj-A after screen release", <-aDone)

			if lite.peakFlight.Load() > 1 || coord.peakFlight.Load() > 1 {
				t.Fatalf("peak concurrency exceeded 1: lite=%d coord=%d", lite.peakFlight.Load(), coord.peakFlight.Load())
			}
			for _, p := range []*concurrencyTrackingProvider{lite, coord} {
				p.mu.Lock()
				for _, sent := range p.received {
					raw := sent.Messages[len(sent.Messages)-1].Content
					exposure.Absent(t, raw, secretA)
					exposure.Absent(t, raw, secretB)
				}
				p.mu.Unlock()
			}

			// Every lease was released once: an immediate overlay acquisition succeeds.
			rel, err := plane.acquireLane(t.Context(), lite, UtilityClassOverlay)
			testutil.FailErr(t, "post-test lease check", err)
			rel()
		})
	}
}

func TestMultiCapacityProviderAllowsConcurrentDispatch(t *testing.T) {
	firstIn := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	hosted := &concurrencyTrackingProvider{
		id:           "hosted",
		singleFlight: false,
		onCall: func(_ context.Context, _ modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
			once.Do(func() { close(firstIn) })
			<-release
			return okStream(), nil
		},
	}
	reg := newScreenTestRegistry(t, NewModelSecretScreen(modelScreenMatcher(t), nil), hosted)
	plane := NewUtilityPlane()
	policy := newPolicyFixture(t, ModelPolicy{Lite: ModelRef{ProviderID: "hosted", Model: "m"}})
	sum := &RegistrySummarizer{Registry: reg, Policy: policy, Plane: plane, Scope: SettingsScopeGlobal, Class: UtilityClassRequested}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := sum.SummarizeRequired(t.Context(), "s", "u1", 32)
		testutil.FailErr(t, "call 1", err)
	}()
	<-firstIn
	go func() {
		defer wg.Done()
		_, err := sum.SummarizeRequired(t.Context(), "s", "u2", 32)
		testutil.FailErr(t, "call 2", err)
	}()
	testutil.WaitFor(t, "both calls active in multi-capacity provider", func() bool {
		return hosted.peakFlight.Load() >= 2
	})
	close(release)
	wg.Wait()
}
