package llm

import (
	"context"
	"path/filepath"
	"testing"

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
	registry, err := NewRegistry(catalog, providercredentials.NewAt(filepath.Join(tmp, "credential-vault.age")))
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
