package llm

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/modelfeed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"log/slog"
	"strings"
	"testing"
	ollamaprovider "github.com/lycaon/lycaon/internal/llm/providers/ollama"
)

type outcomeScriptedProvider struct {
	chunks []modelcall.StreamChunk
}

func (p *outcomeScriptedProvider) ID() string { return "p" }
func (p *outcomeScriptedProvider) Models() []modelcall.ModelInfo {
	return []modelcall.ModelInfo{{ID: "m"}}
}
func (p *outcomeScriptedProvider) Profile() providerprofile.Profile {
	profile := providerprofile.OpenAI()
	profile.Discovery = providerprofile.DiscoveryNone
	return profile
}
func (p *outcomeScriptedProvider) Complete(_ context.Context, _ modelcall.CompletionRequest) (*modelcall.Completion, error) {
	completion, _, err := modelcall.CollectStream(p.stream())
	return completion, err
}
func (p *outcomeScriptedProvider) Stream(_ context.Context, _ modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	return p.stream(), nil
}
func (p *outcomeScriptedProvider) stream() <-chan modelcall.StreamChunk {
	ch := make(chan modelcall.StreamChunk, len(p.chunks))
	for _, c := range p.chunks {
		ch <- c
	}
	close(ch)
	return ch
}

// finishedPhases returns the outcome and failed fields of each finished
// phase record in the captured logs.
func finishedPhases(t *testing.T, logs string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		testutil.FailErr(t, "decode log line", json.Unmarshal([]byte(line), &record))
		if record["msg"] == "model request phase" && record["phase"] == "finished" {
			out = append(out, record)
		}
	}
	return out
}

// A consumer that cancels its context after the terminal chunk has still
// received a completed stream; only an error or an early stop is a failure.
func TestRequestLifecycleReportsStreamOutcome(t *testing.T) {
	cases := []struct {
		name    string
		chunks  []modelcall.StreamChunk
		cancel  bool
		outcome string
		failed  bool
	}{
		{name: "completed then canceled", chunks: []modelcall.StreamChunk{{Content: "ok", Done: true}}, cancel: true, outcome: "completed", failed: false},
		{name: "error chunk", chunks: []modelcall.StreamChunk{{Err: context.DeadlineExceeded, Done: true}}, outcome: "failed", failed: true},
		{name: "closed without terminal chunk", chunks: []modelcall.StreamChunk{{Content: "partial"}}, outcome: "abandoned", failed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logs lifecycleLogBuffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(old) })

			scripted := &outcomeScriptedProvider{chunks: tc.chunks}
			snapshot := newProviderRegistrySnapshot(providerRegistrySnapshotInput{Providers: map[string]modelcall.Provider{"p": scripted}})
			reg := &Registry{}
			reg.outboundSecretScreen = NewModelSecretScreen(nil, nil)
			provider := reg.decorateProvider(snapshot, scripted)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			stream, err := provider.Stream(ctx, modelcall.CompletionRequest{Model: "m"})
			testutil.FailErr(t, "stream", err)
			for range stream {
			}
			if tc.cancel {
				cancel()
			}
			phases := finishedPhases(t, logs.text())
			if len(phases) != 1 {
				t.Fatalf("finished phases = %v, want one", phases)
			}
			if phases[0]["outcome"] != tc.outcome || phases[0]["failed"] != tc.failed {
				t.Fatalf("finished = %v, want outcome %q failed %v", phases[0], tc.outcome, tc.failed)
			}
		})
	}
}

func TestConfiguredContextCapSurvivesCatalogResolution(t *testing.T) {
	for _, kind := range []string{"ollama", "openai", "azure", "bedrock", "vertex", "vertex-express"} {
		t.Run(kind, func(t *testing.T) {
			for _, tc := range []struct {
				name                  string
				cap, advertised, want int
			}{
				{"bounded", 32768, 262144, 32768},
				{"refreshed", 32768, 524288, 32768},
				{"uncapped", 0, 262144, 262144},
				{"cannot expand capacity", 32768, 16384, 16384},
				{"unknown capacity", 32768, 0, 32768},
			} {
				t.Run(tc.name, func(t *testing.T) {
					local := []modelinfo.Entry{{ID: "model", ContextLength: tc.cap, MaxTokens: 1024}}
					discovered := []modelinfo.Entry{{ID: "model", ContextLength: tc.advertised,
						Capabilities: modelinfo.ModelCapabilities{Tools: modelinfo.Evidence(modelinfo.CapabilitySupported, "probe")}}}
					key, _ := modelfeed.FeedKeyForKind(kind)
					doc := &modelfeed.Document{Providers: map[string]modelfeed.Provider{
						key: {ID: key, Models: map[string]modelfeed.Model{"model": {
							ID: "model", ToolCall: new(true), Modalities: modelfeed.Modalities{Output: []string{"text"}},
						}}},
					}}
					got := mergeAssignableModels(kind, local, discovered, nil, doc, modelfeed.StatusOK, true)
					if len(got.Models) != 1 || got.Models[0].ContextLength != tc.want || got.Models[0].MaxTokens != 1024 {
						t.Fatalf("resolved models = %+v, want context %d and output limit 1024", got.Models, tc.want)
					}
					if local[0].ContextLength != tc.cap || discovered[0].ContextLength != tc.advertised {
						t.Fatal("resolution mutated source metadata")
					}
				})
			}
		})
	}
}

func TestOllamaContextCapSurvivesRuntimeRefresh(t *testing.T) {
	local := []modelinfo.Entry{{ID: "model", ContextLength: 32768}}
	p := ollamaprovider.New("remote", "http://unused.invalid", "", local)
	for _, capacity := range []int{262144, 524288} {
		merged := mergeAssignableModels("ollama", local,
			[]modelinfo.Entry{{ID: "model:latest", ContextLength: capacity}}, nil, nil, "", false)
		p = p.WithEffectiveModels(merged.Models)
		if got := p.ContextLimit(t.Context(), "model:latest"); got != 32768 {
			t.Fatalf("refreshed context = %d, want 32768", got)
		}
		req := modelcall.CompletionRequest{Model: "model:latest", MaxTokens: 1024,
			Messages: []api.Message{{Role: api.MessageRoleUser, Content: strings.Repeat("a", 60000)}}}
		body, _, err := p.Prepare(t.Context(), req, false)
		testutil.FailErr(t, "build capped request", err)
		if body.Options.NumCtx != 32768 {
			t.Fatalf("request context = %d, want 32768", body.Options.NumCtx)
		}
		req.Messages[0].Content = strings.Repeat("a", 140000)
		_, _, err = p.Prepare(t.Context(), req, false)
		var tooSmall *failure.ProviderContextTooSmallError
		if !errors.As(err, &tooSmall) || tooSmall.MaxContext != 32768 {
			t.Fatalf("oversize request = %v, want capped-context refusal", err)
		}
	}
}

func TestRegistryRequestBudgetUsesNativeAdapter(t *testing.T) {
	p := ollamaprovider.New("local", "http://unused.invalid", "", []modelinfo.Entry{{ID: "model", ContextLength: 16384}})
	r := &Registry{}
	r.snapshot.Store(&providerRegistrySnapshot{providers: map[string]modelcall.Provider{"local": p}})
	req := modelcall.CompletionRequest{Model: "model", Messages: []api.Message{{Role: api.MessageRoleUser, Content: strings.Repeat("detail ", 2000)}}}
	projected := req
	projected.Messages = transcript.Project(req.Messages)
	want := p.RequestBudget(t.Context(), projected)
	if got := r.RequestBudget(t.Context(), "local", req); got != want {
		t.Fatalf("registry budget = %+v, adapter = %+v", got, want)
	}
	if want.Input <= modelcall.EstimateRequestInput(req) {
		t.Fatal("fixture must exercise native cold calibration")
	}
	r.snapshot.Store(&providerRegistrySnapshot{providers: map[string]modelcall.Provider{"local": p},
		entries: map[string]CatalogEntry{"local": {Kind: "ollama", Models: []modelinfo.Entry{{ID: "model", ContextLength: 8192}}}}})
	if got := r.RequestBudget(t.Context(), "local", req); got.Window != 8192 || got.Input != want.Input {
		t.Fatalf("configured request cap did not constrain adapter metadata: %+v", got)
	}
}

func testPolicy() ModelPolicy {
	return ModelPolicy{
		Coordinator: ModelRef{ProviderID: "openai", Model: "gpt-4o"},
		Lite:        ModelRef{ProviderID: "openai", Model: "gpt-4o-mini"},
		AgentPool: AgentPool{
			Selection: PoolSelectionRoundRobin,
			Models: []ModelRef{
				{ProviderID: "openai", Model: "gpt-4o-mini"},
				{ProviderID: "openai", Model: "gpt-4o"},
				{ProviderID: "ollama", Model: "llama3.1"},
			},
		},
	}
}

func TestPoolSelectorRoundRobin(t *testing.T) {
	s := NewPoolSelector(testPolicy())
	ctx := context.Background()

	first, err := s.SelectOne(ctx)
	testutil.FailErr(t, "s.SelectOne failed", err)
	second, err := s.SelectOne(ctx)
	testutil.FailErr(t, "s.SelectOne failed", err)
	if first.Model == second.Model {
		t.Fatalf("round_robin should advance: %q then %q", first.Model, second.Model)
	}
}

func TestPoolSelectorFirst(t *testing.T) {
	p := testPolicy()
	p.AgentPool.Selection = PoolSelectionFirst
	s := NewPoolSelector(p)

	sel, err := s.SelectOne(context.Background())
	testutil.FailErr(t, "s.SelectOne failed", err)
	if sel.Model != "gpt-4o-mini" {
		t.Fatalf("first = %q", sel.Model)
	}
}

func TestSelectGroupDiverseProviders(t *testing.T) {
	s := NewPoolSelector(testPolicy())
	group, err := s.SelectGroup(context.Background(), 2)
	testutil.FailErr(t, "s.SelectGroup failed", err)
	if len(group) != 2 {
		t.Fatalf("len = %d", len(group))
	}
	if group[0].ProviderID == group[1].ProviderID && group[0].Model == group[1].Model {
		t.Fatalf("expected diverse pick, got %+v and %+v", group[0], group[1])
	}
	if group[0].ProviderID == group[1].ProviderID {
		t.Fatalf("expected different providers when pool allows, got both %q", group[0].ProviderID)
	}
}

func TestSelectGroupHomogeneousPool(t *testing.T) {
	p := testPolicy()
	p.AgentPool.Models = []ModelRef{{ProviderID: "openai", Model: "gpt-4o-mini"}}
	s := NewPoolSelector(p)

	group, err := s.SelectGroup(context.Background(), 2)
	testutil.FailErr(t, "s.SelectGroup failed", err)
	if len(group) != 2 {
		t.Fatalf("len = %d", len(group))
	}
	if group[0].Model != group[1].Model {
		t.Fatalf("homogeneous pool should duplicate: %+v %+v", group[0], group[1])
	}
}

func TestSelectGroupHomogeneousPoolFillsRequestedCount(t *testing.T) {
	p := testPolicy()
	p.AgentPool.Models = []ModelRef{{ProviderID: "openai", Model: "gpt-4o-mini"}}
	s := NewPoolSelector(p)

	group, err := s.SelectGroup(t.Context(), 5)
	testutil.FailErr(t, "SelectGroup", err)
	if len(group) != 5 {
		t.Fatalf("len = %d, want 5", len(group))
	}
	for i, selection := range group {
		if selection.ProviderID != "openai" || selection.Model != "gpt-4o-mini" {
			t.Fatalf("group[%d] = %+v", i, selection)
		}
	}
}

func TestSelectGroupEmptyPoolFallsBackToCoordinator(t *testing.T) {
	p := testPolicy()
	p.AgentPool.Models = nil
	s := NewPoolSelector(p)

	group, err := s.SelectGroup(context.Background(), 2)
	testutil.FailErr(t, "s.SelectGroup failed", err)
	for _, sel := range group {
		if sel.Model != p.Coordinator.Model {
			t.Fatalf("expected coordinator fallback, got %+v", sel)
		}
	}
}
