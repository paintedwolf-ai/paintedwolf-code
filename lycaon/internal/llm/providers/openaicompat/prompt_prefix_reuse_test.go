package openaicompat

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/debugpaths"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// Complete requests go through a real adapter; the measurement reads what it serialized.
func TestPrefixReuseAttributesFirstDifferencesAC_KV_07(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LYCAON_LLM_DEBUG", "1")
	t.Setenv("LYCAON_LLM_DEBUG_FILE", filepath.Join(dir, "requests.jsonl"))
	observability.CloseLLMDebug()
	t.Cleanup(observability.CloseLLMDebug)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	const model = "fixture-model"
	profile := providerprofile.Together()
	provider := New("fixture", server.URL, "fixture", []modelinfo.Entry{{ID: model}}).WithProfile(profile)
	usage := modelcall.TokenUsage{Present: true, PromptTokens: 5000, CacheReadInputTokens: 1234}

	standing := api.Message{ID: "standing", Role: api.MessageRoleSystem, Content: strings.Repeat("standing ", 200), PromptCacheBreakpoint: api.PromptCacheTierStanding}
	opener := api.Message{ID: "opener", Role: api.MessageRoleUser, Content: "Finish the migration"}
	row := func(id string) api.Message {
		return api.Message{ID: id, Role: api.MessageRoleAssistant, Content: strings.Repeat(id+" ", 100)}
	}
	reminder := api.Message{ID: "reminder", Role: api.MessageRoleSystem, Content: "one rejected operation"}
	build := func(toolName string, inject api.Message, cut modelcall.HistoryCutFacts, history ...api.Message) modelcall.CompletionRequest {
		messages := append([]api.Message{standing, opener}, history...)
		messages[len(messages)-1].PromptCacheBreakpoint = api.PromptCacheTierHistory
		return modelcall.CompletionRequest{Model: model, Messages: append(messages, inject),
			Tools: []tools.ToolMeta{{Name: toolName, Description: "fixture tool"}},
			Debug: modelcall.RequestDebug{SessionID: "prefix-session", HistoryCut: cut}}
	}
	a, b, c, d := row("a"), row("b"), row("c"), row("d")
	rewritten := b
	rewritten.Content = "rewritten"
	held := modelcall.HistoryCutFacts{TailOrd: 40}
	advanced := modelcall.HistoryCutFacts{TailOrd: 64, Change: "tail_advanced"}
	published := modelcall.HistoryCutFacts{Generation: 1}

	steps := []struct {
		name, want string
		req        modelcall.CompletionRequest
		noReuse    bool
	}{
		{"first request", "first_request", build("read", reminder, held, a, b), true},
		{"identical request", "none", build("read", reminder, held, a, b), false},
		{"append", "history_appended", build("read", reminder, held, a, b, c), false},
		{"changed reminder", "tail_injects_changed", build("read", api.Message{ID: "reminder", Role: api.MessageRoleSystem, Content: "two rejected operations"}, held, a, b, c), false},
		{"changed tools", "tools_changed", build("write", reminder, held, a, b, c), true},
		{"unattributed row rewrite", "history_row_changed", build("write", reminder, held, a, rewritten, c), false},
		{"cutoff advance", "tail_advanced", build("write", reminder, advanced, c, d), false},
		{"published summary", "summary_published", build("write", reminder, published, row("summary"), d), false},
	}
	for _, step := range steps {
		step.req.ControlCapture = &modelcall.RequestControlCapture{}
		started := time.Now().UTC()
		_, err := provider.Complete(t.Context(), step.req)
		testutil.FailErr(t, step.name, err)
		providerwire.NotePromptCacheUsage("fixture", step.req, profile, usage, started, false)
	}
	observations := readPromptCacheObservations(t, filepath.Join(dir, debugpaths.Name(debugpaths.KindPromptCache)))
	if len(observations) != len(steps) {
		t.Fatalf("observations = %d, want %d", len(observations), len(steps))
	}
	for i, step := range steps {
		got := observations[i]
		if got.FirstDifference != step.want {
			t.Errorf("%s: first difference %q, want %q", step.name, got.FirstDifference, step.want)
		}
		if got.PrefixMethod != "serialization_proxy_bytes" {
			t.Errorf("%s: method %q is not labeled as a proxy", step.name, got.PrefixMethod)
		}
		if got.ModelInputBytes == 0 || got.ReusablePrefixBytes > got.ModelInputBytes {
			t.Errorf("%s: reusable %d of input %d", step.name, got.ReusablePrefixBytes, got.ModelInputBytes)
		}
		if step.noReuse != (got.ReusablePrefixBytes == 0) {
			t.Errorf("%s: reusable %d, want none=%v", step.name, got.ReusablePrefixBytes, step.noReuse)
		}
		// Provider telemetry is reported beside the measurement, never derived from it.
		if got.ReadTokens != 1234 || got.InputTokens != 5000 {
			t.Errorf("%s: provider usage %d/%d", step.name, got.ReadTokens, got.InputTokens)
		}
	}
	if observations[1].ReusablePrefixBytes != observations[1].ModelInputBytes {
		t.Errorf("identical request reused %d of %d", observations[1].ReusablePrefixBytes, observations[1].ModelInputBytes)
	}
	if got := observations[6]; got.CutoffChange != "tail_advanced" || got.CutoffChanges != 1 {
		t.Errorf("cutoff advance: %+v", got)
	}
	if got := observations[7]; got.CutoffChanges != 2 {
		t.Errorf("published summary changes = %d", got.CutoffChanges)
	}
	// Tool schema order and cache markers aside, the rewrite stops reuse at its own row.
	if rewrite, append := observations[5], observations[2]; rewrite.ReusablePrefixBytes >= append.ReusablePrefixBytes {
		t.Errorf("rewrite reused %d, append %d", rewrite.ReusablePrefixBytes, append.ReusablePrefixBytes)
	}
}

func readPromptCacheObservations(t *testing.T, path string) []observability.PromptCacheObservation {
	t.Helper()
	file, err := os.Open(path)
	testutil.FailErr(t, "open observations", err)
	defer file.Close()
	var out []observability.PromptCacheObservation
	scanner := bufio.NewScanner(file)
	scanner.Buffer(nil, 1<<20)
	for scanner.Scan() {
		var entry observability.PromptCacheObservation
		testutil.FailErr(t, "decode observation", json.Unmarshal(scanner.Bytes(), &entry))
		out = append(out, entry)
	}
	testutil.FailErr(t, "scan observations", scanner.Err())
	return out
}
