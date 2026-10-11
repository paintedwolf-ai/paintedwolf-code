package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/modelfeed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestModelAssignmentDiscoveryFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(upstream.Close)
	catalog := mustCatalogCloneShipToLocal(t, `providers:
  - id: cloud
    kind: openai
    base_url: `+upstream.URL+`
    api_key_env: ""
    models: []
`+MinimalShipHTTPRetryYAML)
	registry, err := NewRegistry(t.Context(), catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credentials.age")))
	testutil.FailErr(t, "create registry", err)
	service := &Service{Catalog: catalog, Registry: registry}
	ref := ModelRef{ProviderID: "cloud", Model: "candidate"}
	err = service.ValidateModelRef(t.Context(), ref, PolicySlotCoordinator)
	var unavailable *ModelCatalogUnavailableError
	if !errors.As(err, &unavailable) || unavailable.ProviderID != ref.ProviderID || unavailable.Model != ref.Model {
		t.Fatalf("assignment error = %v, want catalog unavailable for exact model", err)
	}
	if unavailable.Cause == nil || !errors.Is(err, unavailable.Cause) {
		t.Fatal("discovery cause was lost")
	}

	feed, err := modelfeed.New(modelfeed.Options{
		CacheDir: t.TempDir(),
		GetBytes: func(context.Context, string) ([]byte, error) {
			return []byte(`{"openai":{"id":"openai","models":{"candidate":{"id":"candidate","tool_call":true,"modalities":{"input":["text"],"output":["text"]}}}}}`), nil
		},
	})
	testutil.FailErr(t, "create fixture feed", err)
	_, err = feed.Refresh(t.Context())
	testutil.FailErr(t, "refresh fixture feed", err)
	registry.SetModelFeed(t.Context(), feed)
	testutil.FailErr(t, "assign known model despite failed discovery", service.ValidateModelRef(t.Context(), ref, PolicySlotCoordinator))
	ref.Model = "absent-from-feed"
	if err = service.ValidateModelRef(t.Context(), ref, PolicySlotCoordinator); !errors.As(err, &unavailable) {
		t.Fatalf("missing model with failed discovery = %v, want unavailable", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err = service.ValidateModelRef(ctx, ref, PolicySlotCoordinator); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled validation = %v", err)
	}
}

func TestModelAssignmentDiscoveryFailurePreservesRefusal(t *testing.T) {
	service := &Service{}
	ref := ModelRef{ProviderID: "cloud", Model: "candidate"}
	resolved := mergeResult{
		DiscoveryError: errors.New("fixture discovery failure"),
		Refused:        []modelinfo.Entry{{ID: ref.Model}},
	}
	err := service.unassignableModelError("openai", ref, resolved)
	var unavailable *ModelCatalogUnavailableError
	if err == nil || errors.As(err, &unavailable) {
		t.Fatalf("explicit refusal = %v, want assignment rejection", err)
	}
}
