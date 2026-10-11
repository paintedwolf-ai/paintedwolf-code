package llm

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// terminalErrorStream answers inside the stream, as Converse does.
func terminalErrorStream(err error) <-chan modelcall.StreamChunk {
	ch := make(chan modelcall.StreamChunk, 1)
	ch <- modelcall.StreamChunk{Err: err, Done: true}
	close(ch)
	return ch
}

func testRoutingWithGates(t *testing.T, p modelcall.Provider, capacity *CapacityGate, refusals *providerretry.ModelRefusalGate) *RoutingClient {
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
	return NewRoutingClient(registry, nil, nil, nil, capacity, refusals, func(context.Context) (*ModelSelection, error) {
		return &ModelSelection{ProviderID: p.ID(), Model: "m"}, nil
	})
}

// A refusal the transport answers inside the stream is remembered like one it
// answers when the stream opens, so the next call stops at the gate.
func TestRoutingClientRemembersStreamCarriedRefusal(t *testing.T) {
	refusal := &providerretry.ModelRefusedError{
		ProviderID: "scripted", Model: "m", Status: 403, Code: "AccessDeniedException",
		Evidence: providerretry.RefusalEvidenceModelIdentity, Detail: "not available for this account",
	}
	scripted := &scriptedStreamProvider{
		id: "scripted",
		fn: func(int) (<-chan modelcall.StreamChunk, error) { return terminalErrorStream(refusal), nil },
	}
	gate := providerretry.NewModelRefusalGate()
	client := testRoutingWithGates(t, scripted, nil, gate)
	req := modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}}}
	for attempt := 1; attempt <= 2; attempt++ {
		_, err := client.Complete(context.Background(), req)
		if !errors.Is(err, providerretry.ErrModelRefused) {
			t.Fatalf("attempt %d: err = %v, want the refusal", attempt, err)
		}
	}
	if scripted.calls != 1 {
		t.Fatalf("calls = %d, want 1: the second call must stop at the refusal gate", scripted.calls)
	}
	if _, refused := gate.Refused("scripted", "m"); !refused {
		t.Fatal("the stream-carried refusal was not recorded")
	}
}

// An unavailable slot reported inside the stream cools the slot.
func TestRoutingClientCoolsSlotFromStreamCarriedOverload(t *testing.T) {
	scripted := &scriptedStreamProvider{
		id: "scripted",
		fn: func(int) (<-chan modelcall.StreamChunk, error) {
			return terminalErrorStream(&failure.ProviderOverloadedError{ProviderID: "scripted", Status: 503, Detail: "busy"}), nil
		},
	}
	gate := NewCapacityGate(CapacityPolicy{CooldownMs: 60_000})
	client := testRoutingWithGates(t, scripted, gate, nil)
	_, err := client.Complete(context.Background(), modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}}})
	if _, ok := failure.AsProviderOverloaded(err); !ok {
		t.Fatalf("err = %v, want overloaded", err)
	}
	gate.mu.Lock()
	remaining := gate.remainingLocked("scripted", "m")
	gate.mu.Unlock()
	if remaining <= 0 {
		t.Fatal("the slot was not cooled after the stream reported an overload")
	}
}

// A clean stream records nothing new and leaves a working pair untouched.
func TestRoutingClientStreamOutcomeKeepsWorkingPair(t *testing.T) {
	scripted := &scriptedStreamProvider{id: "scripted", fn: func(int) (<-chan modelcall.StreamChunk, error) { return okStream(), nil }}
	gate := providerretry.NewModelRefusalGate()
	client := testRoutingWithGates(t, scripted, NewCapacityGate(CapacityPolicy{CooldownMs: 60_000}), gate)
	out, err := client.Complete(context.Background(), modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}}})
	testutil.FailErr(t, "Complete", err)
	if out.Content != "ok" {
		t.Fatalf("content = %q", out.Content)
	}
	if _, refused := gate.Refused("scripted", "m"); refused {
		t.Fatal("a completed stream must not record a refusal")
	}
}
