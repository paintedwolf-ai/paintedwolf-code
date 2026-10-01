package toolusage_test

import (
	"encoding/json"
	"github.com/lycaon/lycaon/internal/configlayout"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/eval/toolusage"
	"github.com/lycaon/lycaon/internal/testutil"
)

func fixtureCaptureDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(configlayout.FindModuleRoot(), "test", "fixtures", "eval", "capture")
}

func TestProfileFromCaptureDeterministic(t *testing.T) {
	dir := fixtureCaptureDir(t)
	first, err := toolusage.ProfileFromCaptureDir(dir)
	testutil.FailErr(t, "profile replay first", err)
	second, err := toolusage.ProfileFromCaptureDir(dir)
	testutil.FailErr(t, "profile replay second", err)
	if !toolusage.CompareProfiles(first, second) {
		t.Fatal("two replays of the same capture must diff empty")
	}
}

func TestProfileFromCaptureMetrics(t *testing.T) {
	dir := fixtureCaptureDir(t)
	p, err := toolusage.ProfileFromCaptureDir(dir)
	testutil.FailErr(t, "profile replay", err)

	if p.Mode != "replay" {
		t.Fatalf("mode = %q, want replay", p.Mode)
	}
	if p.Read.TotalReads != 3 {
		t.Fatalf("total_reads = %d, want 3", p.Read.TotalReads)
	}
	if p.Read.WholeFileReads != 2 {
		t.Fatalf("whole_file_reads = %d, want 2", p.Read.WholeFileReads)
	}
	if p.Read.ScopedReads != 1 {
		t.Fatalf("scoped_reads = %d, want 1", p.Read.ScopedReads)
	}
	if p.Read.ReReadPaths != 1 {
		t.Fatalf("re_read_paths = %d, want 1", p.Read.ReReadPaths)
	}
	if p.ToolCalls["read"] != 3 {
		t.Fatalf("read tool calls = %d, want 3", p.ToolCalls["read"])
	}
	if p.ToolCalls["grep"] != 2 {
		t.Fatalf("grep tool calls = %d, want 2", p.ToolCalls["grep"])
	}
	if p.ToolCallsByAgentType["coordinator"]["list_dir"] != 2 {
		t.Fatalf("coordinator list_dir = %d, want 2", p.ToolCallsByAgentType["coordinator"]["list_dir"])
	}
	if p.ToolCallsBySurface["implement_worker"]["grep"] != 1 {
		t.Fatalf("worker surface grep = %d, want 1", p.ToolCallsBySurface["implement_worker"]["grep"])
	}
	if p.TokenSpend.PromptTokens != 4700 {
		t.Fatalf("prompt_tokens = %d, want 4700", p.TokenSpend.PromptTokens)
	}
	if p.Cache.CacheReadInputTokens != 3580 {
		t.Fatalf("cache_read = %d, want 3580", p.Cache.CacheReadInputTokens)
	}
	if p.TaskSuccess.WorkersComplete != 1 {
		t.Fatalf("workers_complete = %d, want 1", p.TaskSuccess.WorkersComplete)
	}
}

func TestProfileFromCaptureSessionScopesSubtree(t *testing.T) {
	dir := fixtureCaptureDir(t)
	full, err := toolusage.ProfileFromCaptureDir(dir)
	testutil.FailErr(t, "profile full capture", err)

	run, err := toolusage.ProfileFromCaptureSession(dir, "coord-1")
	testutil.FailErr(t, "profile coord run", err)
	if run.ToolCalls["read"] != full.ToolCalls["read"] {
		t.Fatalf("run read = %d, want %d", run.ToolCalls["read"], full.ToolCalls["read"])
	}
	if run.ToolCalls["grep"] != full.ToolCalls["grep"] {
		t.Fatalf("run grep = %d, want %d", run.ToolCalls["grep"], full.ToolCalls["grep"])
	}
	if run.TokenSpend.PromptTokens != full.TokenSpend.PromptTokens {
		t.Fatalf("run prompt_tokens = %d, want %d", run.TokenSpend.PromptTokens, full.TokenSpend.PromptTokens)
	}

	worker, err := toolusage.ProfileFromCaptureSession(dir, "work-1")
	testutil.FailErr(t, "profile worker session", err)
	if worker.ToolCalls["grep"] != 1 {
		t.Fatalf("worker grep = %d, want 1", worker.ToolCalls["grep"])
	}
	if worker.ToolCalls["read"] != 0 {
		t.Fatalf("worker read = %d, want 0", worker.ToolCalls["read"])
	}
	if worker.TokenSpend.PromptTokens != 500 {
		t.Fatalf("worker prompt_tokens = %d, want 500", worker.TokenSpend.PromptTokens)
	}
}

func TestProfileFromCaptureSessionIsolatesRuns(t *testing.T) {
	dir := t.TempDir()
	sessions := strings.Join([]string{
		`{"session_id":"run-a","agent_type":"coordinator","surface":"implement_investigate","task":"run a"}`,
		`{"session_id":"run-b","agent_type":"coordinator","surface":"implement_investigate","task":"run b"}`,
	}, "\n") + "\n"
	llm := strings.Join([]string{
		`{"session_id":"run-a","agent_type":"coordinator","iteration":1,"usage":{"prompt_tokens":1000},"messages":[{"role":"assistant","tool_calls":[{"name":"read","id":"r1"}]},{"role":"tool","content":"{}"}]}`,
		`{"session_id":"run-b","agent_type":"coordinator","iteration":1,"usage":{"prompt_tokens":2000},"messages":[{"role":"assistant","tool_calls":[{"name":"grep","id":"g1"}]},{"role":"tool","content":"{\"count\":1}"}]}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "sessions.jsonl"), []byte(sessions), 0o600); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "llm-requests.jsonl"), []byte(llm), 0o600); err != nil {
		testutil.FailErr(t, "write file", err)
	}

	full, err := toolusage.ProfileFromCaptureDir(dir)
	testutil.FailErr(t, "profile full capture", err)
	if full.ToolCalls["read"] != 1 || full.ToolCalls["grep"] != 1 {
		t.Fatalf("full tool calls = %+v, want read=1 grep=1", full.ToolCalls)
	}
	if full.TokenSpend.PromptTokens != 3000 {
		t.Fatalf("full prompt_tokens = %d, want 3000", full.TokenSpend.PromptTokens)
	}

	runA, err := toolusage.ProfileFromCaptureSession(dir, "run-a")
	testutil.FailErr(t, "profile run a", err)
	if runA.ToolCalls["read"] != 1 || runA.ToolCalls["grep"] != 0 {
		t.Fatalf("run-a tool calls = %+v, want read=1 grep=0", runA.ToolCalls)
	}
	if runA.TokenSpend.PromptTokens != 1000 {
		t.Fatalf("run-a prompt_tokens = %d, want 1000", runA.TokenSpend.PromptTokens)
	}

	runB, err := toolusage.ProfileFromCaptureSession(dir, "run-b")
	testutil.FailErr(t, "profile run b", err)
	if runB.ToolCalls["grep"] != 1 || runB.ToolCalls["read"] != 0 {
		t.Fatalf("run-b tool calls = %+v, want grep=1 read=0", runB.ToolCalls)
	}
	if runB.TokenSpend.PromptTokens != 2000 {
		t.Fatalf("run-b prompt_tokens = %d, want 2000", runB.TokenSpend.PromptTokens)
	}
}

func TestProfileFromCaptureSessionsCombinesIndependentRoots(t *testing.T) {
	dir := fixtureCaptureDir(t)
	combined, err := toolusage.ProfileFromCaptureSessions(dir, []string{"coord-1", "work-1"})
	testutil.FailErr(t, "profile independent roots", err)
	full, err := toolusage.ProfileFromCaptureDir(dir)
	testutil.FailErr(t, "profile full capture", err)
	if combined.ToolCalls["read"] != full.ToolCalls["read"] || combined.ToolCalls["grep"] != full.ToolCalls["grep"] {
		t.Fatalf("combined calls = %+v, full = %+v", combined.ToolCalls, full.ToolCalls)
	}
}

func TestProfileSurveySequenceMetrics(t *testing.T) {
	dir := t.TempDir()
	sessions := `{"session_id":"run-a","agent_type":"coordinator","surface":"investigate","task":"survey"}` + "\n"
	llm := `{"session_id":"run-a","agent_type":"coordinator","iteration":1,"messages":[{"role":"assistant","tool_calls":[{"name":"summarize","id":"s1","args":{"path":"internal/app"}},{"name":"read","id":"r1","args":{"path":"internal/app/app.go","range":"1-20"}}]},{"role":"tool","content":"{}"},{"role":"tool","content":"{}"},{"role":"assistant","tool_calls":[{"name":"summarize","id":"s2","args":{"path":"internal/app"}}]},{"role":"tool","content":"{}"}]}` + "\n"
	testutil.FailErr(t, "write sessions", os.WriteFile(filepath.Join(dir, "sessions.jsonl"), []byte(sessions), 0o600))
	testutil.FailErr(t, "write llm", os.WriteFile(filepath.Join(dir, "llm-requests.jsonl"), []byte(llm), 0o600))

	p, err := toolusage.ProfileFromCaptureDir(dir)
	testutil.FailErr(t, "profile capture", err)
	if p.Survey.SummarizeCalls != 2 || p.Survey.SummarizeBatches != 2 || p.Survey.MixedSurveyBatches != 1 {
		t.Fatalf("survey sequence = %+v", p.Survey)
	}
	if p.Survey.RepeatedSummarizeScopes != 1 || p.Survey.TargetedInspectionsAfterSummary != 1 {
		t.Fatalf("survey relevance = %+v", p.Survey)
	}
}

func TestProfileFromCaptureSessionRepeatable(t *testing.T) {
	dir := fixtureCaptureDir(t)
	first, err := toolusage.ProfileFromCaptureSession(dir, "coord-1")
	testutil.FailErr(t, "profile first", err)
	second, err := toolusage.ProfileFromCaptureSession(dir, "coord-1")
	testutil.FailErr(t, "profile second", err)
	if !toolusage.CompareProfiles(first, second) {
		t.Fatal("same session scoped twice must diff empty")
	}
}

func TestAggregateProfiles(t *testing.T) {
	dir := fixtureCaptureDir(t)
	base, err := toolusage.ProfileFromCaptureDir(dir)
	testutil.FailErr(t, "profile replay", err)
	runs := []toolusage.Profile{base, base, base}
	agg := toolusage.AggregateProfiles(runs)
	if agg.Aggregates.Runs != 3 {
		t.Fatalf("aggregate runs = %d, want 3", agg.Aggregates.Runs)
	}
	if agg.Aggregates.WholeFileRatio.StdDev != 0 {
		t.Fatalf("identical runs stddev = %v, want 0", agg.Aggregates.WholeFileRatio.StdDev)
	}
}

func TestLoadCorpus(t *testing.T) {
	path := filepath.Join(configlayout.FindModuleRoot(), toolusage.DefaultCorpusPath)
	c, err := toolusage.LoadCorpus(path)
	testutil.FailErr(t, "load corpus", err)
	if c.ID != "tool-sequences-v2" {
		t.Fatalf("corpus id = %q", c.ID)
	}
	if len(c.Tasks) != 4 {
		t.Fatalf("tasks = %d, want 4", len(c.Tasks))
	}
}

func TestBaselineMatchesFixtureCapture(t *testing.T) {
	dir := fixtureCaptureDir(t)
	live, err := toolusage.ProfileFromCaptureDir(dir)
	testutil.FailErr(t, "profile replay", err)
	basePath := filepath.Join(configlayout.FindModuleRoot(), "test", "fixtures", "eval", "tool-usage-profile.baseline.json")
	data, err := os.ReadFile(basePath)
	testutil.FailErr(t, "read baseline", err)
	var baseline toolusage.Profile
	if err := json.Unmarshal(data, &baseline); err != nil {
		t.Fatalf("decode baseline: %v", err)
	}
	// Baseline stores relative capture path placeholder; compare metric payload only.
	live.CaptureDir = baseline.CaptureDir
	if !toolusage.CompareProfiles(baseline, live) {
		t.Fatalf("baseline drift: %s", toolusage.DiffSummary(baseline, live))
	}
}
