package llm

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/testutil"
)

func newTestLocalRegistry(t *testing.T, entries ...CatalogEntry) (*Registry, *ProviderCatalog) {
	t.Helper()
	catalog := mustTestProviderCatalog(t, entries...)
	reg, err := NewRegistry(t.Context(), catalog, nil)
	testutil.FailErr(t, "NewRegistry failed", err)
	reg.SetOutboundSecretScreen(NewModelSecretScreen(nil, nil))
	return reg, catalog
}

func TestResolveTrueWindowLlamaCppProps32768(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"qwen2.5-coder-32b"}]}`))
		case "/props":
			_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":32768},"total_slots":1}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	reg, _ := newTestLocalRegistry(t, CatalogEntry{
		ID:      "openai-compatible",
		Kind:    "openai-compatible",
		BaseURL: srv.URL + "/v1",
		Models:  []modelinfo.Entry{{ID: "qwen2.5-coder-32b"}},
	})
	cfg := testBaseCompaction(t)
	windows := testWindows(t)
	policy := ModelPolicy{Coordinator: ModelRef{ProviderID: "openai-compatible", Model: "qwen2.5-coder-32b"}}

	got, source := ResolveTrueWindow(policy, reg, windows, cfg)
	if got != 32768 {
		t.Fatalf("true_window = %d want 32768", got)
	}
	if source != WindowResolveModelEntry {
		t.Fatalf("source = %q want %q", source, WindowResolveModelEntry)
	}
	entry, ok := ContextEntry(reg, "openai-compatible", "qwen2.5-coder-32b")
	if !ok || entry.ContextLength != 32768 || entry.ContextLengthSource != "llamacpp-props" {
		t.Fatalf("ContextEntry = %+v, ok=%v; want ContextLength=32768 source=llamacpp-props", entry, ok)
	}
}

func TestResolveTrueWindowVLLMAndLMStudioDiscovery(t *testing.T) {
	vllmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"Qwen/Qwen2.5-Coder-32B-Instruct","max_model_len":40960}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer vllmSrv.Close()

	lmstudioSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v0/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"qwen2.5-coder","type":"llm","loaded_context_length":24576,"max_context_length":131072,"capabilities":{"trained_for_tool_use":true}}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer lmstudioSrv.Close()

	reg, _ := newTestLocalRegistry(t,
		CatalogEntry{
			ID:      "openai-compatible",
			Kind:    "openai-compatible",
			BaseURL: vllmSrv.URL + "/v1",
			Models:  []modelinfo.Entry{{ID: "Qwen/Qwen2.5-Coder-32B-Instruct"}},
		},
		CatalogEntry{
			ID:      "lmstudio",
			Kind:    "lmstudio",
			BaseURL: lmstudioSrv.URL + "/v1",
		},
	)
	cfg := testBaseCompaction(t)
	windows := testWindows(t)

	gotVLLM, srcVLLM := ResolveTrueWindow(
		ModelPolicy{Coordinator: ModelRef{ProviderID: "openai-compatible", Model: "Qwen/Qwen2.5-Coder-32B-Instruct"}},
		reg, windows, cfg,
	)
	if gotVLLM != 40960 || srcVLLM != WindowResolveModelEntry {
		t.Fatalf("vLLM true_window = (%d, %q) want (40960, %q)", gotVLLM, srcVLLM, WindowResolveModelEntry)
	}
	vllmEntry, ok := ContextEntry(reg, "openai-compatible", "Qwen/Qwen2.5-Coder-32B-Instruct")
	if !ok || vllmEntry.ContextLengthSource != "v1/models" {
		t.Fatalf("vLLM ContextEntry = %+v, ok=%v", vllmEntry, ok)
	}

	gotLMS, srcLMS := ResolveTrueWindow(
		ModelPolicy{Coordinator: ModelRef{ProviderID: "lmstudio", Model: "qwen2.5-coder"}},
		reg, windows, cfg,
	)
	if gotLMS != 24576 || srcLMS != WindowResolveModelEntry {
		t.Fatalf("LM Studio true_window = (%d, %q) want (24576, %q)", gotLMS, srcLMS, WindowResolveModelEntry)
	}
	lmsEntry, ok := ContextEntry(reg, "lmstudio", "qwen2.5-coder")
	if !ok || lmsEntry.ContextLengthSource != "lmstudio" {
		t.Fatalf("LM Studio ContextEntry = %+v, ok=%v", lmsEntry, ok)
	}
}

func TestDeclaredContextLengthWinsOverDiscoveredProps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"qwen","max_model_len":65536}]}`))
		case "/props":
			_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":65536}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	reg, _ := newTestLocalRegistry(t, CatalogEntry{
		ID:      "openai-compatible",
		Kind:    "openai-compatible",
		BaseURL: srv.URL + "/v1",
		Models:  []modelinfo.Entry{{ID: "qwen", ContextLength: 16384}},
	})
	cfg := testBaseCompaction(t)
	windows := testWindows(t)
	got, src := ResolveTrueWindow(
		ModelPolicy{Coordinator: ModelRef{ProviderID: "openai-compatible", Model: "qwen"}},
		reg, windows, cfg,
	)
	if got != 16384 || src != WindowResolveModelEntry {
		t.Fatalf("declared true_window = (%d, %q) want (16384, %q)", got, src, WindowResolveModelEntry)
	}
	entry, ok := ContextEntry(reg, "openai-compatible", "qwen")
	if !ok || entry.ContextLength != 16384 || entry.ContextLengthSource != "config" {
		t.Fatalf("ContextEntry = %+v, ok=%v; want 16384 from config", entry, ok)
	}
	RecordContextWindow(reg, "openai-compatible", "qwen", 8192, "exceed_context_size_error", true)
	entryAfterOverflow, ok := ContextEntry(reg, "openai-compatible", "qwen")
	if !ok || entryAfterOverflow.ContextLength != 8192 {
		t.Fatalf("ContextEntry after lower overflow = %+v, ok=%v; want 8192", entryAfterOverflow, ok)
	}
	if !isLocalRegistryProvider(reg, "openai-compatible") || isLocalRegistryProvider(nil, "ollama") || isLocalRegistryProvider(reg, "") {
		t.Fatal("provider locality did not require configured profile facts")
	}
}

func TestServingWindowRefreshesWhenEndpointIdentityChanges(t *testing.T) {
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/props" {
			_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":16384}}`))
			return
		}
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"local-model"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv1.Close()

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/props" {
			_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":32768}}`))
			return
		}
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"local-model"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv2.Close()

	reg, catalog := newTestLocalRegistry(t, CatalogEntry{
		ID:      "openai-compatible",
		Kind:    "openai-compatible",
		BaseURL: srv1.URL + "/v1",
		Models:  []modelinfo.Entry{{ID: "local-model"}},
	})
	if got := reg.ContextLength("openai-compatible", "local-model"); got != 16384 {
		t.Fatalf("initial ContextLength = %d want 16384", got)
	}

	testutil.FailErr(t, "catalog.Put failed", catalog.Put(ProviderEntry{
		ID:      "openai-compatible",
		Kind:    "openai-compatible",
		BaseURL: srv2.URL + "/v1",
		Models:  []modelinfo.Entry{{ID: "local-model"}},
	}))
	testutil.FailErr(t, "reg.Reload failed", reg.Reload(t.Context()))
	if got := reg.ContextLength("openai-compatible", "local-model"); got != 32768 {
		t.Fatalf("refreshed ContextLength after endpoint change = %d want 32768", got)
	}
	RecordContextWindow(reg, "openai-compatible", "local-model", 12288, "exceed_context_size_error", true)
	if got := reg.ContextLength("openai-compatible", "local-model"); got != 12288 {
		t.Fatalf("ContextLength after RecordContextWindow = %d want 12288", got)
	}
}

func TestResolveTrueWindowLocalFallbackNotHosted262K(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"custom-local-model"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	reg, _ := newTestLocalRegistry(t, CatalogEntry{
		ID:      "openai-compatible",
		Kind:    "openai-compatible",
		BaseURL: srv.URL + "/v1",
		Models:  []modelinfo.Entry{{ID: "custom-local-model"}},
	})
	cfg := testBaseCompaction(t)
	windows := testWindows(t)
	policy := ModelPolicy{Coordinator: ModelRef{ProviderID: "openai-compatible", Model: "custom-local-model"}}

	got, source := ResolveTrueWindow(policy, reg, windows, cfg)
	if got != modelinfo.DefaultLocalFallbackTrueWindow {
		t.Fatalf("local fallback true_window = %d want %d (not 262144)", got, modelinfo.DefaultLocalFallbackTrueWindow)
	}
	if source != WindowResolveFallback {
		t.Fatalf("source = %q want %q", source, WindowResolveFallback)
	}

	// Projected ProviderMeta leaves context_length unset (0) so Den settings shows an unknown window.
	metas := reg.List(t.Context())
	if len(metas) != 1 || len(metas[0].Models) != 1 {
		t.Fatalf("metas = %+v; want 1 provider with 1 model", metas)
	}
	if metas[0].Models[0].ContextLength != 0 {
		t.Fatalf("projected model ContextLength = %d; want 0 (unknown)", metas[0].Models[0].ContextLength)
	}
}
