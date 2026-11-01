package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	anthropicprovider "github.com/lycaon/lycaon/internal/llm/providers/anthropic"
	ollamaprovider "github.com/lycaon/lycaon/internal/llm/providers/ollama"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
)

func TestCurationFocusCacheKeyExcludesTask(t *testing.T) {
	a := CurationFocus{Tool: "read", View: "outline", Target: "a.go", Task: "one"}
	b := CurationFocus{Tool: "read", View: "outline", Target: "a.go", Task: "two"}
	if a.CacheKey() != b.CacheKey() {
		t.Fatalf("cache key should ignore task: %q vs %q", a.CacheKey(), b.CacheKey())
	}
}

func TestCurationFocusPromptFocusIncludesTask(t *testing.T) {
	f := CurationFocus{Tool: "read", View: "outline", Target: "pkg/a.go", Task: "audit altitude"}
	got := f.PromptFocus()
	for _, want := range []string{"read outline: pkg/a.go", "Task: audit altitude"} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt focus = %q want substring %q", got, want)
		}
	}
}

func TestRegistrySummarizerCurate_surveyListDirSelection(t *testing.T) {
	preview := "dir files=12 subdirs=tools entries=foo.go"
	snapshot := evidence.AssembleLedger([]evidence.Record{
		{
			Handle:     "snap#1",
			Kind:       "list",
			Shape:      evidence.ShapeFileRegion,
			SourceTool: "list_dir",
			Survey:     true,
			Path:       "lycaon/internal",
			LineRanges: []evidence.LineRange{{Start: 1, End: 1}},
			Body:       []string{"1\t" + preview},
		},
	})
	provider := &stubCuratorProvider{
		id:        "lite",
		responses: []string{`{"selections":[{"path":"lycaon/internal","line":1,"excerpt":"subdirs=tools"}],"gloss":[{"label":"tools subtree"}]}`},
	}
	cur := newTestRegistrySummarizer(t, provider)
	got, err := cur.Curate(context.Background(), snapshot, CurationFocus{Tool: "list_dir", View: "map", Target: "lycaon/internal"}, 3)
	testutil.FailErr(t, "Curate", err)
	if len(got.Selections) != 1 {
		t.Fatalf("selections = %#v", got.Selections)
	}
	if got.Report.Dropped != 0 {
		t.Fatalf("dropped = %d want 0", got.Report.Dropped)
	}
}

type captureCompleteProvider struct {
	stubCuratorProvider
	lastReq modelcall.CompletionRequest
}

func (c *captureCompleteProvider) Complete(_ context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	c.lastReq = req
	return c.stubCuratorProvider.Complete(context.Background(), req)
}

func TestRegistrySummarizerCurate_usesSystemUserMessages(t *testing.T) {
	snapshot := snapshotFromRead(t, "pkg/a.go", "10| func entry() {}", 10)
	provider := &captureCompleteProvider{
		stubCuratorProvider: stubCuratorProvider{
			id:        "lite",
			responses: []string{`{"selections":[],"gloss":[]}`},
		},
	}
	cur := newTestRegistrySummarizer(t, provider)
	ctx := curationctx.WithSession(context.Background(), curationctx.Session{
		SessionID:       "sess-1",
		Agent:           "explore_readonly",
		ParentSessionID: "parent-1",
	})
	_, err := cur.Curate(ctx, snapshot, CurationFocus{Tool: "read", View: "outline", Target: "pkg/a.go"}, 3)
	testutil.FailErr(t, "Curate", err)
	if len(provider.lastReq.Messages) != 2 {
		t.Fatalf("messages = %d want system+user", len(provider.lastReq.Messages))
	}
	if provider.lastReq.Messages[0].Role != api.MessageRoleSystem {
		t.Fatalf("first role = %q", provider.lastReq.Messages[0].Role)
	}
	if !transcript.HasAuthorityNotice(provider.lastReq.Messages[0]) || provider.lastReq.Messages[1].Role != api.MessageRoleUser {
		t.Fatalf("provenance projection = %+v", provider.lastReq.Messages)
	}
	if provider.lastReq.Think != modelcall.ThinkOff {
		t.Fatalf("Think = %v want ThinkOff", provider.lastReq.Think)
	}
	// The request is self-attributing: session identity and purpose ride the
	// provider capture row.
	debug := provider.lastReq.Debug
	if debug.SessionID != "sess-1" || debug.AgentType != "explore_readonly" || debug.ParentSessionID != "parent-1" {
		t.Fatalf("debug attribution = %+v", debug)
	}
	if debug.Purpose != "curate" {
		t.Fatalf("purpose = %q want curate", debug.Purpose)
	}
	rf := provider.lastReq.ResponseFormat
	if rf == nil || rf.Type != modelcall.ResponseFormatJSONSchema || rf.Name != "curate" {
		t.Fatalf("ResponseFormat = %+v want curate json_schema", rf)
	}
}

func TestCurationTaskHintFromContext(t *testing.T) {
	ctx := curationctx.WithTaskHint(context.Background(), "audit altitude")
	if got := curationctx.TaskHint(ctx); got != "audit altitude" {
		t.Fatalf("hint = %q", got)
	}
	f := SearchFocus(ctx, "find", "*.go")
	if !strings.Contains(f.PromptFocus(), "audit altitude") {
		t.Fatalf("prompt focus = %q", f.PromptFocus())
	}
}

func TestOllamaBuildRequestStripsPerceiveImages(t *testing.T) {
	png := tinyPNG(t)
	p := ollamaprovider.New("ollama", "http://127.0.0.1:9/v1", "", []modelinfo.Entry{
		{ID: "llama3.2", ContextLength: 8192},
	})
	req := modelcall.CompletionRequest{
		Model: "llama3.2",
		Messages: []api.Message{{
			Role: api.MessageRoleTool,
			ToolResult: &api.ToolResult{
				Content: "shot",
				Visual: &api.VisualArtifact{
					Perceive: true,
					Bytes:    png,
					Mime:     "image/png",
				},
			},
		}},
	}
	payload, _, err := p.Prepare(t.Context(), req, false)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if len(payload.Messages) != 1 {
		t.Fatalf("messages = %+v", payload.Messages)
	}
	// Native Ollama content is string-only; stripped perceive leaves the text
	// and states why no image followed.
	content := payload.Messages[0].Content
	if !strings.HasPrefix(content, "shot") || !strings.Contains(content, `"reason":"model_lacks_vision"`) {
		t.Fatalf("content = %q", content)
	}
}

func TestRegistryModelHasVision(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": "gpt-4o"}},
		})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	// Local capability evidence applies to a discovered model with the same id.
	yaml := "providers:\n  - id: openai\n    base_url: " + srv.URL + "/v1\n    models:\n      - id: gpt-4o\n        capabilities:\n          vision:\n            state: supported\n            sources: [local-config]\n" + MinimalShipHTTPRetryYAML
	stageShipProviders(t, yaml)
	localPath := filepath.Join(tmp, "providers.local.yaml")
	writeProvidersLocal(t, localPath, []byte(yaml))
	catalog, err := NewProviderCatalogAt(localPath)
	testutil.FailErr(t, "catalog", err)
	reg, err := NewRegistry(t.Context(), catalog, providercredentials.NewAt(filepath.Join(tmp, "credential-vault.age")))
	if err == nil {
		reg.SetOutboundSecretScreen(NewModelSecretScreen(nil, nil))
	}
	testutil.FailErr(t, "registry", err)
	reg.discoveryClient = srv.Client()
	if !reg.ModelHasVision(t.Context(), "openai", "gpt-4o") {
		t.Fatal("expected gpt-4o vision")
	}
	if reg.ModelHasVision(t.Context(), "openai", "text-only") {
		t.Fatal("expected unknown model non-vision")
	}
}

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	testutil.FailErr(t, "base64.StdEncoding.DecodeString failed", err)
	return png
}

func TestAnthropicUserImageBlock_parityWithOpenAICompat(t *testing.T) {
	png := tinyPNG(t)
	providerwire.SetVisualBytesResolver(func(sessionID, artifactID string) ([]byte, string, bool) {
		return png, "image/png", true
	})
	t.Cleanup(func() { providerwire.SetVisualBytesResolver(nil) })

	msgs := []api.Message{{
		Role:        api.MessageRoleUser,
		Content:     "what is this?",
		ArtifactIDs: []string{"art-twin"},
	}}

	_, anth := anthropicprovider.ProjectMessages(msgs, nil, true, "sess-1", "", "")
	oai := openaicompat.ProjectMessages(msgs, openaicompat.MessageProjection{Vision: true, SessionID: "sess-1"})

	var anthImage *anthropicprovider.ImageSource
	for _, b := range anth[0].Content {
		if b.Type == "image" {
			anthImage = b.Source
			break
		}
	}
	if anthImage == nil {
		t.Fatal("anthropic wire missing image block")
	}

	parts, ok := oai[0].Content.([]openaicompat.ContentPart)
	if !ok {
		t.Fatalf("openai content type = %T", oai[0].Content)
	}
	var oaiURL string
	for _, p := range parts {
		if p.Type == "image_url" && p.ImageURL != nil {
			oaiURL = p.ImageURL.URL
			break
		}
	}
	wantPrefix := "data:" + anthImage.MediaType + ";base64,"
	if !strings.HasPrefix(oaiURL, wantPrefix) {
		t.Fatalf("openai url = %q want prefix %q", oaiURL, wantPrefix)
	}
	if oaiURL[len(wantPrefix):] != anthImage.Data {
		t.Fatalf("base64 payload mismatch anthropic vs openai")
	}
}
