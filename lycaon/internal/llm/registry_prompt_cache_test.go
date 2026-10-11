package llm

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProviderFeaturesMetaPromptCache(t *testing.T) {
	reg, err := NewRegistry(t.Context(),
		mustTestProviderCatalog(t,
			CatalogEntry{ID: "anthropic", Kind: "anthropic", BaseURL: "https://api.anthropic.com"},
			CatalogEntry{ID: "ollama", Kind: "ollama", BaseURL: "http://127.0.0.1:11434"},
			CatalogEntry{ID: "fireworks", Kind: "fireworks", BaseURL: "https://api.fireworks.ai/inference/v1"},
		),
		providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")),
	)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	cases := map[string]string{
		"anthropic": string(providerprofile.PromptCacheExplicitBreakpoints),
		"ollama":    string(providerprofile.PromptCacheLocalKV),
		"fireworks": string(providerprofile.PromptCacheAutomaticPrefix),
	}
	for id, want := range cases {
		meta := providerFeaturesFromSnapshot(reg.snapshot.Load(), id)
		if meta.PromptCache != want {
			t.Fatalf("%s prompt_cache = %q, want %q", id, meta.PromptCache, want)
		}
	}
	if meta := providerFeaturesFromSnapshot(reg.snapshot.Load(), "missing-provider"); meta.PromptCache != string(providerprofile.PromptCacheNone) {
		t.Fatalf("unknown provider prompt_cache = %q, want none", meta.PromptCache)
	}
}

func TestRegistryListCachedReusesSnapshot(t *testing.T) {
	reg, err := NewRegistry(t.Context(),
		mustTestProviderCatalog(t,
			CatalogEntry{ID: "ollama", Kind: "ollama", BaseURL: "http://127.0.0.1:11434"},
		),
		providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")),
	)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	first := reg.ListCached(t.Context())
	second := reg.ListCached(t.Context())
	if len(first) != len(second) {
		t.Fatalf("len first=%d second=%d", len(first), len(second))
	}
	reg.InvalidateListCache()
	if len(reg.ListCached(t.Context())) != len(first) {
		t.Fatal("expected same provider count after cache invalidate")
	}
}

// Empty provider lists remain JSON arrays.
func TestRegistryListCachedEmptyStaysArray(t *testing.T) {
	reg, err := NewRegistry(t.Context(),
		mustTestProviderCatalog(t),
		providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")),
	)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	// Exercise cache fill and clone paths.
	for attempt := range 2 {
		list := reg.ListCached(t.Context())
		if list == nil {
			t.Fatalf("attempt %d: ListCached returned nil, want empty slice", attempt)
		}
		encoded, err := json.Marshal(list)
		if err != nil {
			testutil.FailErr(t, "marshal provider list", err)
		}
		if string(encoded) != "[]" {
			t.Fatalf("attempt %d: wire payload = %s, want []", attempt, encoded)
		}
	}
}

func TestRegistryListCachedServesStaleWhileRefreshing(t *testing.T) {
	reg, err := NewRegistry(t.Context(),
		mustTestProviderCatalog(t,
			CatalogEntry{ID: "ollama", Kind: "ollama", BaseURL: "http://127.0.0.1:11434"},
		),
		providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")),
	)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	warm := reg.ListCached(t.Context())
	if len(warm) == 0 {
		t.Fatal("expected warm list")
	}
	reg.listCache.Expire()

	stale := reg.ListCached(t.Context())
	if len(stale) != len(warm) {
		t.Fatalf("stale len=%d warm=%d", len(stale), len(warm))
	}
	// Background refresh should repopulate without blocking this call.
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, _, fresh, _ := reg.listCache.Read()
		if fresh {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("list cache did not refresh in background")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWorkerChildCompactionConfigTightensBudget(t *testing.T) {
	base := compaction.DefaultCompactionConfig()
	got := compaction.WorkerChildCompactionConfig(base)
	if got.HardCeilingTokens >= base.HardCeilingTokens {
		t.Fatalf("worker ceiling = %d want < %d", got.HardCeilingTokens, base.HardCeilingTokens)
	}
	if got.KeepRecentMessages > 16 {
		t.Fatalf("worker keep-recent = %d want <= 16", got.KeepRecentMessages)
	}
}
