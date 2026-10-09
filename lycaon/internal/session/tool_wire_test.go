package session

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPrepareToolWireContentRetainsLargeFilesWithoutCompactor(t *testing.T) {
	files := make([]map[string]string, 200)
	for i := range files {
		files[i] = map[string]string{"path": "a.go", "status": " M"}
	}
	raw, err := json.Marshal(map[string]any{"files": files, "dirty": true})
	testutil.FailErr(t, "json.Marshal failed", err)
	mgr := NewHost(sessionstore.NewMemory(), Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	sess := &api.Session{ID: "s1", Posture: api.SessionPostureBuild}
	out, meta := mgr.Runner.History.ToolWire(context.Background(), sess, "git_status", string(raw), compaction.CompactToolWireOpts{})
	if meta != nil {
		t.Fatalf("compact meta = %+v want nil without compactor", meta)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	got, _ := obj["files"].([]any)
	want := 200 // No lossy fallback when recovery storage is unavailable.
	if len(got) != want {
		t.Fatalf("files len = %d want %d", len(got), want)
	}
}

func TestPrepareToolWirePreservesBoundedRecallBody(t *testing.T) {
	st := store.NewMemory()
	mgr := NewHost(st, Models{Client: llm.NewMockProvider(testMockConfig(t)), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetDataDir(t.TempDir())
	mgr.Runner.History.SetCompactor(compaction.NewSimpleCompactor(compaction.CompactionConfig{
		Enabled: true, ChunkTokenThreshold: 50, ChunkTargetTokens: 20,
	}, compaction.MockSummarizer{Text: "summary"}))
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	body := strings.Repeat("observed line\n", 30) + "historical value: 7119\n" + strings.Repeat("observed line\n", 30)
	raw, err := json.Marshal(map[string]any{
		"resolution": "matched", "hits": []any{map[string]any{"handle": "read#1", "body": []string{body}}},
	})
	testutil.FailErr(t, "encode recall", err)
	out, meta := mgr.Runner.History.ToolWire(t.Context(), sess, "recall", string(raw), compaction.CompactToolWireOpts{})
	if meta != nil || out != string(raw) {
		t.Fatalf("bounded recall was compacted before the model could inspect it: meta=%+v, content=%s", meta, out)
	}
}

func TestPrepareToolWireContentCompactsBeforeStore(t *testing.T) {
	store := store.NewMemory()
	mgr := NewHost(store, Models{Client: llm.NewMockProvider(testMockConfig(t)), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetDataDir(t.TempDir())
	mgr.Runner.History.SetCompactor(compaction.NewSimpleCompactor(compaction.CompactionConfig{
		Enabled:             true,
		ChunkTokenThreshold: 50,
		ChunkTargetTokens:   500,
	}, compaction.MockSummarizer{Text: "summary"}))

	ctx := context.Background()

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)

	huge := `{"files":[` + repeatJSONPath(300) + `]}`
	out, meta := mgr.Runner.History.ToolWire(ctx, sess, "git_status", huge, compaction.CompactToolWireOpts{})
	if meta == nil {
		t.Fatal("expected compacted chunk meta")
	}
	if meta.Strategy != "spill_structured" {
		t.Fatalf("strategy=%q want spill_structured", meta.Strategy)
	}
	if len(out) >= len(huge) {
		t.Fatalf("compact out len = %d want smaller than %d", len(out), len(huge))
	}
}

func TestPrepareToolWireContentPreservesOutputBeyondSpillBound(t *testing.T) {
	st := store.NewMemory()
	limits := settings.DefaultSessionLimits()
	limits.MaxToolSpillBytes = 1024
	mgr := NewHost(st, Models{Client: llm.NewMockProvider(testMockConfig(t)), Provider: nil, Limits: limits, Cost: nil}, tools.NewStubRegistry())
	mgr.SetDataDir(t.TempDir())
	mgr.Runner.History.SetCompactor(compaction.NewSimpleCompactor(compaction.CompactionConfig{
		Enabled: true, ChunkTokenThreshold: 100, ChunkTargetTokens: 200,
	}, nil))
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session with spill bound", err)
	content := `{"files":[{"path":"a.go","diff":"` + strings.Repeat("x", 20000) + `"}]}`
	out, meta := mgr.Runner.History.ToolWire(t.Context(), sess, "git_diff", content, compaction.CompactToolWireOpts{})
	if meta != nil || out != content {
		t.Fatal("wire compaction changed output without complete retention")
	}
}

func TestPrepareToolWireContentHandlePrefixedFind(t *testing.T) {
	store := store.NewMemory()
	mgr := NewHost(store, Models{Client: llm.NewMockProvider(testMockConfig(t)), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetDataDir(t.TempDir())
	mgr.Runner.History.SetCompactor(compaction.NewSimpleCompactor(compaction.CompactionConfig{
		Enabled:             true,
		ChunkTokenThreshold: 50,
		ChunkTargetTokens:   500,
	}, nil))

	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)
	if sess.WorkspacePath == "" {
		// Memory sessions may lack a workspace; set one so spill_structured can write.
		sess.WorkspacePath = t.TempDir()
	}

	results := make([]map[string]string, 200)
	for i := range results {
		results[i] = map[string]string{"path": strings.Repeat("d", 40) + "/f" + string(rune('0'+i%10)) + ".go"}
	}
	raw, err := json.Marshal(map[string]any{"results": results, "total_results": 200})
	testutil.FailErr(t, "marshal", err)
	in := "[find#1]\n" + string(raw)
	out, meta := mgr.Runner.History.ToolWire(ctx, sess, "find", in, compaction.CompactToolWireOpts{})
	if meta == nil {
		t.Fatal("expected compact meta for oversized find page")
	}
	if meta.Strategy != "spill_structured" {
		t.Fatalf("strategy=%q want spill_structured", meta.Strategy)
	}
	if !strings.Contains(out, "[find#1]") {
		t.Fatal("handle prefix must survive prepareToolWireContent")
	}
	if !strings.Contains(out, "wire_spill_path") {
		t.Fatal("expected wire_spill_path on list compact")
	}
}

func TestPrepareToolWireContentSummarizeSpillsFullPack(t *testing.T) {
	store := store.NewMemory()
	mgr := NewHost(store, Models{Client: llm.NewMockProvider(testMockConfig(t)), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetDataDir(t.TempDir())
	mgr.Runner.History.SetCompactor(compaction.NewSimpleCompactor(compaction.CompactionConfig{
		Enabled:             true,
		ChunkTokenThreshold: 800,
		ChunkTargetTokens:   400,
	}, nil))

	ctx := context.Background()
	dir := t.TempDir()
	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)
	sess.WorkspacePath = dir

	substance := make([]map[string]any, 0, 24)
	for i := 0; i < 24; i++ {
		substance = append(substance, map[string]any{
			"path": "a.go",
			"body": strings.Repeat("zzzzzzzzzz", 100),
		})
	}
	raw, err := json.Marshal(map[string]any{
		"task": "plugin",
		"pack": map[string]any{
			"identity":  []map[string]any{{"path": "a.go", "kind": "file"}},
			"skeleton":  []map[string]any{{"path": "a.go", "kind": "func", "name": "F", "line": 1}},
			"substance": substance,
		},
		"anchors": []map[string]any{
			{"handle": "summarize#1", "path": "a.go", "line": 1, "excerpt": "func F"},
		},
		"selected": 1,
		"total":    1,
		"gather":   map[string]any{"mode": "repo", "candidates": 1, "bytes": 1},
	})
	testutil.FailErr(t, "marshal", err)
	in := "[summarize#3]\n" + string(raw)
	if tokenest.EstimateDefault(in) <= 2400 {
		t.Fatalf("fixture must exceed wire budget: tokens=%d", tokenest.EstimateDefault(in))
	}

	out, meta := mgr.Runner.History.ToolWire(ctx, sess, "summarize", in, compaction.CompactToolWireOpts{})
	if meta == nil {
		t.Fatal("expected compact meta for oversized summarize pack")
	}
	if meta.Strategy != "spill_summarize" {
		t.Fatalf("strategy=%q want spill_summarize", meta.Strategy)
	}
	_, body, _, ok := hostmarker.SplitToolJSONBody(out)
	if !ok {
		t.Fatal("expected JSON body in spill residue")
	}
	var residueObj map[string]any
	if err := json.Unmarshal([]byte(body), &residueObj); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if _, ok := residueObj["pack"]; ok {
		t.Fatal("commit path must not lossy-trim pack substance inline")
	}
	if !strings.Contains(out, "wire_spill_path") {
		t.Fatal("expected wire_spill_path on summarize spill")
	}
}

func TestPrepareToolWireContentSummarizeWireFittedLandsInline(t *testing.T) {
	store := store.NewMemory()
	mgr := NewHost(store, Models{Client: llm.NewMockProvider(testMockConfig(t)), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetDataDir(t.TempDir())
	mgr.Runner.History.SetCompactor(compaction.NewSimpleCompactor(compaction.CompactionConfig{
		Enabled:             true,
		ChunkTokenThreshold: 800,
		ChunkTargetTokens:   400,
	}, nil))

	ctx := context.Background()
	dir := t.TempDir()
	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)
	sess.WorkspacePath = dir

	substance := make([]map[string]any, 0, 4)
	for i := 0; i < 4; i++ {
		substance = append(substance, map[string]any{
			"path": "a.go",
			"body": strings.Repeat("zzzzzzzzzz", 80),
		})
	}
	raw, err := json.Marshal(map[string]any{
		"task": "plugin",
		"pack": map[string]any{
			"identity":  []map[string]any{{"path": "a.go", "kind": "file"}},
			"skeleton":  []map[string]any{{"path": "a.go", "kind": "func", "name": "F", "line": 1}},
			"substance": substance,
		},
		"anchors": []map[string]any{
			{"handle": "summarize#1", "path": "a.go", "line": 1, "excerpt": "func F"},
		},
		"selected": 1,
		"total":    1,
		"gather":   map[string]any{"mode": "repo", "candidates": 1, "bytes": 1},
	})
	testutil.FailErr(t, "marshal", err)
	in := "[summarize#3]\n" + string(raw)
	tokens := tokenest.EstimateDefault(in)
	if tokens < 800 {
		t.Fatalf("fixture too small vs chunk threshold: tokens=%d", tokens)
	}
	if tokens > 2400 {
		t.Fatalf("fixture must stay under wire budget: tokens=%d", tokens)
	}

	out, meta := mgr.Runner.History.ToolWire(ctx, sess, "summarize", in, compaction.CompactToolWireOpts{})
	if meta != nil {
		t.Fatalf("wire-fitted summarize must not compact at commit: meta=%+v out=%s", meta, out)
	}
	if out != in {
		t.Fatal("wire-fitted pack must land inline unchanged")
	}
}

func repeatJSONPath(n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, 0, n*20)
	for i := 0; i < n; i++ {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, `{"path":"p`...)
		b = append(b, byte('0'+(i%10)))
		b = append(b, `.go","status":" M"}`...)
	}
	return string(b)
}
