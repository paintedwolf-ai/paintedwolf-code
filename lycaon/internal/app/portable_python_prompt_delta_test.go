package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"gopkg.in/yaml.v3"
)

const pythonVerbatimDescription = "Run helper scripts here with the standard library and fixed packages. Run project code and tests in the project environment."

type capturingLLMClient struct {
	inner modelcall.LLMClient
	mu    sync.Mutex
	reqs  []modelcall.CompletionRequest
}

func (c *capturingLLMClient) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	c.record(req)
	return c.inner.Complete(ctx, req)
}

func (c *capturingLLMClient) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	c.record(req)
	return c.inner.Stream(ctx, req)
}

func (c *capturingLLMClient) record(req modelcall.CompletionRequest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reqs = append(c.reqs, req)
}

func (c *capturingLLMClient) firstCoordinatorRequest() (modelcall.CompletionRequest, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.reqs {
		if len(r.Tools) > 0 {
			return r, true
		}
	}
	return modelcall.CompletionRequest{}, false
}

type variantPromptMetrics struct {
	Variant                   string `json:"variant"`
	TotalRequestBytes         int    `json:"total_request_bytes"`
	MessagesBytes             int    `json:"messages_bytes"`
	StandingSystemPromptBytes int    `json:"standing_system_prompt_bytes"`
	TurnInjectSystemBytes     int    `json:"turn_inject_system_bytes"`
	TotalSystemTextBytes      int    `json:"total_system_text_bytes"`
	ToolsBytes                int    `json:"tools_bytes"`
	ToolCount                 int    `json:"tool_count"`
	PythonToolOffered         bool   `json:"python_tool_offered"`
	CommandToolOffered        bool   `json:"command_tool_offered"`
	PythonToolSchemaBytes     int    `json:"python_tool_schema_bytes"`
	VerbatimDescriptionCount  int    `json:"verbatim_description_count"`
}

type matchedPromptDeltaReport struct {
	Offered                    variantPromptMetrics `json:"offered"`
	Loadable                   variantPromptMetrics `json:"loadable"`
	Absent                     variantPromptMetrics `json:"absent"`
	StandingPromptDeltaBytes   int                  `json:"standing_prompt_delta_bytes"`
	LoadableStandingDeltaBytes int                  `json:"loadable_standing_delta_bytes"`
	TurnInjectDeltaBytes       int                  `json:"turn_inject_delta_bytes"`
	LoadableTurnInjectDelta    int                  `json:"loadable_turn_inject_delta_bytes"`
	OfferedToolsDeltaBytes     int                  `json:"offered_tools_delta_bytes"`
	LoadableToolsDeltaBytes    int                  `json:"loadable_tools_delta_bytes"`
	OfferedTotalDeltaBytes     int                  `json:"offered_total_delta_bytes"`
	LoadableTotalDeltaBytes    int                  `json:"loadable_total_delta_bytes"`
}

// TestPortablePythonMatchedModelRequestDelta boots the real composed app.Build
// and captures the complete assembled coordinator modelcall.CompletionRequest
// for three matched turns: python Offered, python Loadable (deferred via
// request_tools), and python Absent.
func TestPortablePythonMatchedModelRequestDelta(t *testing.T) {
	moduleRoot := configlayout.FindModuleRoot()
	baseSurfPath := filepath.Join(moduleRoot, "config", string(config.CoordinatorSurface))
	baseSurfRaw, err := os.ReadFile(baseSurfPath)
	testutil.FailErr(t, "read coordinator-surfaces.yaml", err)

	loadableYAML := rewriteCoordinatorSurfacesPython(t, baseSurfRaw, "loadable")
	absentYAML := rewriteCoordinatorSurfacesPython(t, baseSurfRaw, "absent")

	// Share one workspace path across all three matched turns so system-prompt
	// path strings have identical byte length.
	wsDir := t.TempDir()

	measure := func(variant, surfaceOverlay string) variantPromptMetrics {
		t.Helper()
		var metrics variantPromptMetrics
		t.Run(variant, func(t *testing.T) {
			t.Setenv("LYCAON_LLM_MOCK", "1")
			t.Setenv("LYCAON_API_TOKEN", "prompt-delta-token")
			cfg := testBuildConfig(t, moduleRoot)
			if surfaceOverlay != "" {
				configtest.Overlay(t, map[config.Rel]string{
					config.DistroMCP:          "providers:\n  - id: svca\n    command: \"true\"\n    args: []\n    enabled: false\n",
					config.CoordinatorSurface: surfaceOverlay,
				})
			}
			capClient := &capturingLLMClient{
				inner: llm.NewMockProvider(&llm.MockConfig{
					Responses: []llm.MockResponseEntry{{Pattern: ".*", Text: "Ready."}},
				}),
			}
			cfg.TestLLMClient = capClient
			cfg.TestDecider = nonAbstainingDecider{}

			appInst, err := Build(t.Context(), cfg)
			testutil.FailErr(t, "app.Build "+variant, err)
			t.Cleanup(func() { _ = appInst.Close() })

			testdbseed.InsertProjectRoot(t, appInst.DB, testdbseed.DefaultProjectID, wsDir)
			sess, err := appInst.Sessions.Manager.Chats.CreateForProject(t.Context(), testdbseed.DefaultProjectID, wire.SessionPostureBuild)
			testutil.FailErr(t, "create session "+variant, err)
			_, err = appInst.Sessions.Manager.Submissions.Prompt(t.Context(), sess.ID, "Summarize repository layout.")
			testutil.FailErr(t, "submit matched prompt "+variant, err)

			req, ok := capClient.firstCoordinatorRequest()
			if !ok {
				t.Fatalf("%s: no coordinator CompletionRequest captured", variant)
			}
			metrics = computePromptMetrics(t, variant, req)
		})
		return metrics
	}

	offered := measure("offered", "")
	loadable := measure("loadable", loadableYAML)
	absent := measure("absent", absentYAML)

	report := matchedPromptDeltaReport{
		Offered:                    offered,
		Loadable:                   loadable,
		Absent:                     absent,
		StandingPromptDeltaBytes:   offered.StandingSystemPromptBytes - absent.StandingSystemPromptBytes,
		LoadableStandingDeltaBytes: loadable.StandingSystemPromptBytes - absent.StandingSystemPromptBytes,
		TurnInjectDeltaBytes:       offered.TurnInjectSystemBytes - absent.TurnInjectSystemBytes,
		LoadableTurnInjectDelta:    loadable.TurnInjectSystemBytes - absent.TurnInjectSystemBytes,
		OfferedToolsDeltaBytes:     offered.ToolsBytes - absent.ToolsBytes,
		LoadableToolsDeltaBytes:    loadable.ToolsBytes - absent.ToolsBytes,
		OfferedTotalDeltaBytes:     offered.TotalRequestBytes - absent.TotalRequestBytes,
		LoadableTotalDeltaBytes:    loadable.TotalRequestBytes - absent.TotalRequestBytes,
	}

	rawReport, err := json.MarshalIndent(report, "", "  ")
	testutil.FailErr(t, "marshal prompt delta report", err)
	t.Logf("PROMPT_DELTA_REPORT:\n%s", string(rawReport))
	if outPath := strings.TrimSpace(os.Getenv("PW_PROMPT_DELTA_REPORT_PATH")); outPath != "" {
		testutil.FailErr(t, "write PW_PROMPT_DELTA_REPORT_PATH", os.WriteFile(outPath, append(rawReport, '\n'), 0o644))
	}

	if !offered.PythonToolOffered || !offered.CommandToolOffered {
		t.Fatalf("offered variant must include both python and command tools: %+v", offered)
	}
	if loadable.PythonToolOffered || !loadable.CommandToolOffered {
		t.Fatalf("loadable variant must defer python while keeping command: %+v", loadable)
	}
	if absent.PythonToolOffered || !absent.CommandToolOffered {
		t.Fatalf("absent variant must omit python while keeping command: %+v", absent)
	}
	if report.StandingPromptDeltaBytes != 0 {
		t.Fatalf("standing system prompt delta (offered - absent) = %d bytes, want 0", report.StandingPromptDeltaBytes)
	}
	if report.TurnInjectDeltaBytes != 0 {
		t.Fatalf("turn inject system text delta (offered - absent) = %d bytes, want 0", report.TurnInjectDeltaBytes)
	}
	if offered.VerbatimDescriptionCount != 1 {
		t.Fatalf("offered verbatim description count = %d, want 1", offered.VerbatimDescriptionCount)
	}
	if absent.VerbatimDescriptionCount != 0 {
		t.Fatalf("absent verbatim description count = %d, want 0", absent.VerbatimDescriptionCount)
	}
	if loadable.VerbatimDescriptionCount != 0 {
		t.Fatalf("loadable verbatim description count = %d, want 0", loadable.VerbatimDescriptionCount)
	}
}

func computePromptMetrics(t *testing.T, variant string, req modelcall.CompletionRequest) variantPromptMetrics {
	t.Helper()
	msgBytes, err := json.Marshal(req.Messages)
	testutil.FailErr(t, "marshal messages "+variant, err)
	toolsBytes, err := json.Marshal(req.Tools)
	testutil.FailErr(t, "marshal tools "+variant, err)
	totalBytes, err := json.Marshal(struct {
		Messages any `json:"messages"`
		Tools    any `json:"tools"`
	}{Messages: req.Messages, Tools: req.Tools})
	testutil.FailErr(t, "marshal total request "+variant, err)

	standingBytes := 0
	injectBytes := 0
	sawFirstSystem := false
	for _, m := range req.Messages {
		if m.Role == wire.MessageRoleSystem {
			if !sawFirstSystem {
				standingBytes = len(m.Content)
				sawFirstSystem = true
			} else {
				injectBytes += len(m.Content)
			}
		}
	}

	m := variantPromptMetrics{
		Variant:                   variant,
		TotalRequestBytes:         len(totalBytes),
		MessagesBytes:             len(msgBytes),
		StandingSystemPromptBytes: standingBytes,
		TurnInjectSystemBytes:     injectBytes,
		TotalSystemTextBytes:      standingBytes + injectBytes,
		ToolsBytes:                len(toolsBytes),
		ToolCount:                 len(req.Tools),
	}
	for _, tool := range req.Tools {
		if tool.Name == "command" {
			m.CommandToolOffered = true
		}
		if tool.Name == "python" {
			m.PythonToolOffered = true
			if b, err := json.Marshal(tool); err == nil {
				m.PythonToolSchemaBytes = len(b)
			}
		}
		if strings.Contains(tool.Description, pythonVerbatimDescription) {
			m.VerbatimDescriptionCount += strings.Count(tool.Description, pythonVerbatimDescription)
		}
	}
	return m
}

func rewriteCoordinatorSurfacesPython(t *testing.T, raw []byte, mode string) string {
	t.Helper()
	var doc map[string]map[string]any
	testutil.FailErr(t, "unmarshal coordinator-surfaces.yaml", yaml.Unmarshal(raw, &doc))

	removeString := func(in []any, target string) ([]any, bool) {
		out := make([]any, 0, len(in))
		removed := false
		for _, item := range in {
			if s, ok := item.(string); ok && s == target {
				removed = true
				continue
			}
			out = append(out, item)
		}
		return out, removed
	}

	for _, surf := range doc {
		floor, hasFloor := surf["floor"].([]any)
		loadable, _ := surf["loadable"].([]any)
		var removedFromFloor bool
		hasRequestTools := false
		if hasFloor {
			hasRequestTools = slices.ContainsFunc(floor, func(v any) bool { s, _ := v.(string); return s == "request_tools" })
			floor, removedFromFloor = removeString(floor, "python")
			surf["floor"] = floor
		}
		loadable, _ = removeString(loadable, "python")
		if mode == "loadable" && removedFromFloor && hasRequestTools {
			if !slices.ContainsFunc(loadable, func(v any) bool { s, _ := v.(string); return s == "python" }) {
				loadable = append(loadable, "python")
			}
			surf["loadable"] = loadable
		} else if len(loadable) > 0 {
			surf["loadable"] = loadable
		} else {
			delete(surf, "loadable")
		}
	}

	out, err := yaml.Marshal(doc)
	testutil.FailErr(t, "marshal rewritten coordinator-surfaces.yaml", err)
	return string(out)
}

// nonAbstainingDecider answers turn-load questions deterministically without
// abstaining and without pre-loading optional loadable tools onto the floor.
type nonAbstainingDecider struct{}

func (nonAbstainingDecider) Decide(_ context.Context, _ decide.Head, _ any, questions map[string]decide.Question) (decide.Result, error) {
	ans := make(decide.Answers, len(questions))
	for k, q := range questions {
		probs := make(map[string]float64, len(q.Options))
		choice := ""
		for opt := range q.Options {
			if choice == "" {
				choice = opt
			}
			probs[opt] = 0.0
		}
		if q.Kind == decide.KindChoice && choice != "" {
			probs[choice] = 1.0
		}
		ans[k] = decide.Answer{
			Kind:          q.Kind,
			Choice:        choice,
			Probabilities: probs,
			Confidence:    1.0,
		}
	}
	return decide.Result{
		Answers: ans,
		Engine:  decide.Engine{Name: "test", Model: "deterministic", Device: "cpu"},
	}, nil
}

func (nonAbstainingDecider) Rank(_ context.Context, _ decide.Head, _ string, candidates []string) ([]float64, decide.Engine, error) {
	return make([]float64, len(candidates)), decide.Engine{Name: "test"}, nil
}
func (nonAbstainingDecider) HoldPriority(context.Context) func() { return func() {} }
func (nonAbstainingDecider) Available() bool                     { return true }
func (nonAbstainingDecider) Loaded(decide.Head) bool             { return true }
func (nonAbstainingDecider) Speed() time.Duration                { return time.Millisecond }
