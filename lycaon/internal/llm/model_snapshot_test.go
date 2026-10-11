package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/llm/discovery"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestKnownModelMetadataDoesNotWaitForCatalogRefresh(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if calls.Add(1) == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "known"}}})
			return
		}
		once.Do(func() { close(started) })
		select {
		case <-release:
			_, _ = w.Write([]byte(`{"data":[]}`))
		case <-req.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	yaml := "providers:\n  - id: local\n    kind: openai-compatible\n    base_url: " + server.URL + "/v1\n    api_key_env: \"\"\n    models:\n      - id: known\n        capabilities:\n          vision:\n            state: supported\n            sources: [local-config]\n" + MinimalShipHTTPRetryYAML
	catalog := mustCatalogCloneShipToLocal(t, yaml)
	reg, err := NewRegistry(t.Context(), catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")))
	testutil.FailErr(t, "build registry", err)
	t.Cleanup(func() { testutil.FailErr(t, "close discovery", reg.discovery.Close(context.Background())) })
	entry, _ := catalog.Get("local")
	key := discoveryCacheKey(entry.ID, providerprofile.OpenAI(), entry.BaseURL, "")
	cached, fresh, cacheErr := reg.discovery.Get(key)
	testutil.FailErr(t, "read initial metadata", cacheErr)
	if !fresh {
		t.Fatal("initial metadata was not cached")
	}
	testutil.FailErr(t, "close initial discovery", reg.discovery.Close(t.Context()))
	now := time.Now()
	reg.discovery = discovery.NewCache(func() time.Time { return now })
	reg.discovery.Put(key, cached, nil)
	now = now.Add(2 * discovery.CacheTTL)
	ctx := testutil.BoundedContext(t, time.Second)
	if !reg.ModelHasVision(ctx, "local", "known") {
		t.Fatal("known capability waited for or was lost during refresh")
	}
	testutil.FailErr(t, "known capability context", ctx.Err())
	<-started
	for range 3 {
		if !reg.ModelHasVision(ctx, "local", "known") {
			t.Fatal("refresh hid existing model capabilities")
		}
		_, _ = reg.DiscoveredRate("local", "known")
		_ = reg.PricedAsModelID("local", "known")
	}
	if calls.Load() != 2 {
		t.Fatalf("discovery calls=%d, want initial load and one refresh", calls.Load())
	}
	unknownCtx := testutil.BoundedContext(t, 25*time.Millisecond)
	unknown := reg.resolveSelectedModel(unknownCtx, "local", "unknown")
	if !errors.Is(unknown.DiscoveryError, context.DeadlineExceeded) || len(unknown.Models) != 0 {
		t.Fatal("unknown selection bypassed required discovery")
	}
	close(release)
	models := reg.EffectiveModels(t.Context(), "local")
	if len(models) != 0 || reg.ModelHasVision(t.Context(), "local", "known") {
		t.Fatal("successful empty discovery did not remove the old model")
	}
}
