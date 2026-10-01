package discovery

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDiscoverVertexModelsKeepsGenerationCapableRows(t *testing.T) {
	const wantToken = "test-token"
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta1/publishers/google/models" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("view"); got != "PUBLISHER_MODEL_VIEW_FULL" {
			t.Fatalf("view = %q", got)
		}
		if r.Header.Get("Authorization") != "Bearer "+wantToken {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		calls++
		if calls == 1 {
			if tok := r.URL.Query().Get("pageToken"); tok != "" {
				t.Fatalf("unexpected pageToken %q on first page", tok)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"publisherModels": []map[string]any{
					{
						"name": "publishers/google/models/gemini-3.5-flash",
						"supportedActions": map[string]any{
							"openGenerationAiStudio": map[string]any{
								"references": map[string]any{"us-central1": map[string]any{"uri": "x"}},
							},
						},
					},
					{
						// Embedding models are not chat-capable and omit the
						// generation-studio action.
						"name":             "publishers/google/models/text-embedding-005",
						"supportedActions": map[string]any{},
					},
				},
				"nextPageToken": "page-2",
			})
			return
		}
		if got := r.URL.Query().Get("pageToken"); got != "page-2" {
			t.Fatalf("pageToken = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"publisherModels": []map[string]any{
				{
					"name": "publishers/google/models/gemini-2.5-pro",
					"supportedActions": map[string]any{
						"openGenerationAiStudio": map[string]any{},
					},
				},
				{
					// No supportedActions at all — must be dropped, not
					// guessed from the name.
					"name": "publishers/google/models/imagen-4",
				},
			},
		})
	}))
	defer srv.Close()

	restore := vertexModelGardenHost
	vertexModelGardenHost = srv.URL
	t.Cleanup(func() { vertexModelGardenHost = restore })

	models, err := discoverVertexModelsWithToken(t.Context(), srv.Client(), wantToken)
	testutil.FailErr(t, "discover vertex models", err)
	if calls != 2 {
		t.Fatalf("pages fetched = %d, want 2", calls)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v, want only generation-capable rows", models)
	}
	if models[0].ID != "google/gemini-2.5-pro" {
		t.Fatalf("first = %+v", models[0])
	}
	if models[1].ID != "google/gemini-3.5-flash" {
		t.Fatalf("second = %+v", models[1])
	}
}

func TestDiscoverVertexModelsEmptyWhenNoRowCarriesTheField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"publisherModels": []map[string]any{
				{"name": "publishers/google/models/imagen-4"},
				{"name": "publishers/google/models/text-embedding-005", "supportedActions": map[string]any{}},
			},
		})
	}))
	defer srv.Close()

	restore := vertexModelGardenHost
	vertexModelGardenHost = srv.URL
	t.Cleanup(func() { vertexModelGardenHost = restore })

	models, err := discoverVertexModelsWithToken(t.Context(), srv.Client(), "tok")
	testutil.FailErr(t, "discover vertex models", err)
	if len(models) != 0 {
		t.Fatalf("models = %+v, want empty — no name-based fallback", models)
	}
}

func TestDiscoverVertexModelsFailsWithoutADCToken(t *testing.T) {
	// Force ADC resolution to fail deterministically regardless of the host's
	// ambient gcloud config: GOOGLE_APPLICATION_CREDENTIALS pointing at a
	// nonexistent file takes priority over any well-known credentials file.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", t.TempDir()+"/does-not-exist.json")

	models, err := VertexModels(t.Context(), nil)
	if err == nil {
		t.Fatal("expected missing ADC to fail discovery")
	}
	if models != nil {
		t.Fatalf("models = %+v, want nil when ADC cannot mint a token", models)
	}
}

func TestVertexModelIDFromName(t *testing.T) {
	cases := map[string]string{
		"publishers/google/models/gemini-2.5-pro": "google/gemini-2.5-pro",
		"publishers/google/models/":               "",
		"models/gemini-2.5-pro":                   "",
		"":                                        "",
	}
	for name, want := range cases {
		if got := vertexModelIDFromName(name); got != want {
			t.Errorf("vertexModelIDFromName(%q) = %q, want %q", name, got, want)
		}
	}
}
