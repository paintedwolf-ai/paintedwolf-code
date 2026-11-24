package llm

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type diagMockProvider struct {
	id           string
	singleFlight bool
}

func (p *diagMockProvider) ID() string                    { return p.id }
func (p *diagMockProvider) Models() []modelcall.ModelInfo { return []modelcall.ModelInfo{{ID: "m1"}} }
func (p *diagMockProvider) Profile() providerprofile.Profile {
	return providerprofile.Profile{UtilitySingleFlight: p.singleFlight}
}
func (p *diagMockProvider) Complete(_ context.Context, _ modelcall.CompletionRequest) (*modelcall.Completion, error) {
	return &modelcall.Completion{Content: "done"}, nil
}
func (p *diagMockProvider) Stream(_ context.Context, _ modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	ch := make(chan modelcall.StreamChunk, 1)
	ch <- modelcall.StreamChunk{Content: "done", Done: true}
	close(ch)
	return ch, nil
}

func TestScreenLifecycleEmitsJoinedValueFreePhasesThroughDispatch(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "")
	t.Setenv("LYCAON_LLM_MANUAL", "")
	var mu sync.Mutex
	var records []secretmatch.ScreenPhaseRecord
	unsub := secretmatch.RegisterPhaseObserver(func(rec secretmatch.ScreenPhaseRecord) {
		mu.Lock()
		records = append(records, rec)
		mu.Unlock()
	})
	defer unsub()

	matcher := modelScreenMatcher(t)
	screen := NewModelSecretScreen(matcher, func(_ context.Context, _ secretmatch.Alert) (secretmatch.Resolution, error) {
		return secretmatch.Resolution{Decision: secretmatch.SendRedacted}, nil
	})
	screen.SetManagedSecretEvidence(func(ctx context.Context, projectID, _ string) ([]secretmatch.Remembered, error) {
		secretmatch.EmitPhaseRecord(ctx, secretmatch.ScreenPhaseRecord{
			CallID: secretmatch.ScreenCallIDFrom(ctx), ProjectID: projectID,
			Phase: "evidence_reconciled", ManagedValues: 1,
		})
		return nil, nil
	})

	base := &diagMockProvider{id: "ollama-diag", singleFlight: true}

	reg := newTestRegistry()
	reg.SetOutboundSecretScreen(screen)
	testutil.FailErr(t, "register provider", reg.Register(base))
	plane := NewUtilityPlane()
	client := NewRoutingClient(reg, nil, nil, plane, NewCapacityGate(CapacityPolicy{}), nil, func(context.Context) (*ModelSelection, error) {
		return &ModelSelection{ProviderID: "ollama-diag", Model: "m1"}, nil
	})

	req := modelSecretRequest()
	req.Debug.CallID = "call-joined-101"
	resp, err := client.Complete(t.Context(), req)
	testutil.FailErr(t, "client Complete", err)
	if resp == nil || resp.Content != "done" {
		t.Fatalf("unexpected response = %+v", resp)
	}

	// Exercise utility lane capacity phase too.
	rel, err := plane.acquireLane(secretmatch.WithScreenCallID(t.Context(), "call-util-202"), base, UtilityClassOverlay)
	testutil.FailErr(t, "acquire utility lane", err)
	rel()

	// Exercise additional decision outcomes (SendUnchanged, Withhold, Unanswered, ask==nil, ask fault).
	for _, decision := range []secretmatch.Decision{secretmatch.SendUnchanged, secretmatch.Withhold, secretmatch.Unanswered} {
		dScreen := NewModelSecretScreen(matcher, func(_ context.Context, _ secretmatch.Alert) (secretmatch.Resolution, error) {
			return secretmatch.Resolution{Decision: decision, Guidance: "stop"}, nil
		})
		_, _ = dScreen.Screen(secretmatch.WithScreenCallID(t.Context(), "call-"+string(decision)), ScreenDestination{ID: "dest"}, modelSecretRequest())
	}
	unwired := NewModelSecretScreen(matcher, nil)
	if _, err := unwired.Screen(secretmatch.WithScreenCallID(t.Context(), "call-unwired"), ScreenDestination{ID: "dest"}, modelSecretRequest()); err == nil {
		t.Fatal("expected unwired ask error")
	}
	faultScreen := NewModelSecretScreen(matcher, func(_ context.Context, _ secretmatch.Alert) (secretmatch.Resolution, error) {
		return secretmatch.Resolution{}, errors.New("gate disconnected")
	})
	if _, err := faultScreen.Screen(secretmatch.WithScreenCallID(t.Context(), "call-fault"), ScreenDestination{ID: "dest"}, modelSecretRequest()); err == nil {
		t.Fatal("expected ask fault error")
	}
	// Direct dispatchContext without lifecycle trace still emits provider_dispatch.
	_ = dispatchContext(secretmatch.WithScreenCallID(t.Context(), "call-direct-dispatch"))
	// Clean request records 0 hits and skips person_decision.
	cleanReq := modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello world"}},
		Debug:    modelcall.RequestDebug{CallID: "call-clean", ProjectID: "proj-1"},
	}
	_, err = screen.Screen(t.Context(), ScreenDestination{ID: "dest"}, cleanReq)
	testutil.FailErr(t, "clean screen", err)

	mu.Lock()
	defer mu.Unlock()
	var joinedPhases []string
	for _, rec := range records {
		raw, mErr := json.Marshal(rec)
		testutil.FailErr(t, "marshal record", mErr)
		if strings.Contains(string(raw), modelScreenGitHubToken) {
			t.Fatalf("phase record leaked token: %s", string(raw))
		}
		if rec.CallID == "call-joined-101" {
			joinedPhases = append(joinedPhases, rec.Phase)
		}
	}
	wantPhases := []string{"evidence_reconciled", "matcher_redaction", "person_decision", "provider_dispatch"}
	var screenOrder []string
	hasCapacityWait := false
	for _, phase := range joinedPhases {
		if phase == "inference_capacity_wait" {
			hasCapacityWait = true
			continue
		}
		screenOrder = append(screenOrder, phase)
	}
	if len(joinedPhases) != 5 || !hasCapacityWait || !slices.Equal(screenOrder, wantPhases) || joinedPhases[len(joinedPhases)-1] != "provider_dispatch" {
		t.Fatalf("joined phases for call-joined-101 = %v, want capacity wait plus ordered %v ending in provider_dispatch", joinedPhases, wantPhases)
	}
}
