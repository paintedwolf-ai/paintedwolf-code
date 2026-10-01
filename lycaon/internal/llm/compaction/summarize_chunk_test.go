package compaction_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/zstdcodec"
)

func TestTrimSummarizeChunkKeepsPackJSON(t *testing.T) {
	substance := make([]map[string]any, 0, 12)
	for i := 0; i < 12; i++ {
		substance = append(substance, map[string]any{
			"path":       "pkg/plugin.go",
			"start_line": i*20 + 1,
			"end_line":   i*20 + 20,
			"symbol":     "Fn",
			"body":       strings.Repeat("x", 800),
		})
	}
	payload := map[string]any{
		"task": "how to make a plugin",
		"pack": map[string]any{
			"identity": []map[string]any{
				{"path": "pkg/plugin.go", "kind": "file", "line_count": 400, "parse_health": "ok"},
			},
			"skeleton": []map[string]any{
				{"path": "pkg/plugin.go", "kind": "function", "name": "define", "line": 10},
				{"path": "pkg/plugin.go", "kind": "function", "name": "hook", "line": 40},
				{"path": "pkg/plugin.go", "kind": "function", "name": "tool", "line": 80},
				{"path": "pkg/plugin.go", "kind": "function", "name": "event", "line": 120},
				{"path": "pkg/plugin.go", "kind": "function", "name": "extra", "line": 160},
			},
			"substance": substance,
			"imports": []map[string]any{
				{"from": "pkg/plugin.go", "to": "effect", "kind": "import"},
			},
			"neighbors": []map[string]any{
				{"path": "pkg/tool.go", "why": "related"},
			},
		},
		"anchors": []map[string]any{
			{"handle": "summarize#1", "path": "pkg/plugin.go", "line": 10, "excerpt": "export function define"},
			{"handle": "summarize#2", "path": "pkg/plugin.go", "line": 40, "excerpt": "hook("},
		},
		"next_actions": []map[string]any{
			{"tool": "read", "path": "pkg/plugin.go", "lines": "1-80", "why": "full plugin define"},
		},
		"selected":  2,
		"total":     12,
		"truncated": false,
		"gather": map[string]any{
			"mode":       "repo",
			"path":       "pkg",
			"candidates": 1,
			"bytes":      12000,
		},
		"orchestration": map[string]any{
			"curator": map[string]any{"budget_tokens_spent": 4000, "depth_admits": 12},
		},
		"sources_touched": []string{"pkg/plugin.go"},
	}
	raw, err := json.Marshal(payload)
	testutil.FailErr(t, "marshal summarize payload", err)
	in := "[summarize#12]\n" + string(raw)
	if tokenest.EstimateDefault(in) < 2500 {
		t.Fatalf("fixture too small (%d tokens); need oversized pack", tokenest.EstimateDefault(in))
	}

	out, ok := compaction.TrimSummarizeChunk(in, 2400)
	if !ok {
		t.Fatal("expected summarize pack compact")
	}
	if !strings.HasPrefix(out, "[summarize#12]\n") {
		t.Fatal("handle prefix must survive")
	}
	if tokenest.EstimateDefault(out) > 2400 {
		t.Fatalf("compacted tokens=%d want ≤2400", tokenest.EstimateDefault(out))
	}
	if strings.Contains(out, "[compacted tool_result") {
		t.Fatal("assembly trim must not use spill_index head/tail residue")
	}

	_, body, _, splitOK := hostmarker.SplitToolJSONBody(out)
	if !splitOK {
		t.Fatal("compacted output must remain handle+JSON")
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(body), &obj); err != nil {
		t.Fatalf("compacted body must be JSON: %v", err)
	}
	pack, _ := obj["pack"].(map[string]any)
	if pack == nil {
		t.Fatal("pack must remain")
	}
	if _, ok := pack["imports"]; ok {
		t.Fatal("imports should be dropped first under budget pressure")
	}
	if _, ok := pack["neighbors"]; ok {
		t.Fatal("neighbors should be dropped first under budget pressure")
	}
	anchors, _ := obj["anchors"].([]any)
	if len(anchors) != 2 {
		t.Fatalf("anchors len=%d want 2 (citable evidence preserved)", len(anchors))
	}
	next, _ := obj["next_actions"].([]any)
	if len(next) != 1 {
		t.Fatalf("next_actions len=%d want 1", len(next))
	}
	identity, _ := pack["identity"].([]any)
	if len(identity) != 1 {
		t.Fatalf("identity len=%d want 1", len(identity))
	}
	remainingSubstance, _ := pack["substance"].([]any)
	if len(remainingSubstance) >= 12 {
		t.Fatalf("substance should shrink under budget; got %d windows", len(remainingSubstance))
	}
}

func TestTrimSummarizeChunkKeepsRollupBreadth(t *testing.T) {
	skeleton := []map[string]any{
		{"path": "pkg/api", "kind": "directory_rollup", "name": "14 files · Go · top: Handler, Router", "line": 1},
		{"path": "pkg/store", "kind": "directory_rollup", "name": "9 files · Go · top: Open, Migrate", "line": 1},
		{"path": "pkg/auth", "kind": "directory_rollup", "name": "6 files · Go · top: Verify", "line": 1},
	}
	for i := 0; i < 10; i++ {
		skeleton = append(skeleton, map[string]any{
			"path": "pkg/core/dispatch.go", "kind": "function",
			"name": strings.Repeat("LongDispatchVariantName", 4), "line": i*20 + 1,
		})
	}
	substance := make([]map[string]any, 0, 6)
	for i := 0; i < 6; i++ {
		substance = append(substance, map[string]any{
			"path": "pkg/core/dispatch.go", "start_line": i*20 + 1, "end_line": i*20 + 20,
			"symbol": "Dispatch", "body": strings.Repeat("y", 900),
		})
	}
	payload := map[string]any{
		"task": "orient over pkg",
		"pack": map[string]any{
			"identity": []map[string]any{
				{"path": "pkg", "kind": "dir_map", "line_count": 29, "parse_health": "ok"},
			},
			"skeleton":  skeleton,
			"substance": substance,
		},
		"anchors": []map[string]any{
			{"handle": "summarize#1", "path": "pkg/core/dispatch.go", "line": 1, "excerpt": "func Dispatch"},
		},
		"selected": 1, "total": 29, "truncated": false,
		"gather": map[string]any{"mode": "repo", "path": "pkg", "candidates": 29},
	}
	raw, err := json.Marshal(payload)
	testutil.FailErr(t, "marshal summarize payload", err)
	in := "[summarize#3]\n" + string(raw)

	out, ok := compaction.TrimSummarizeChunk(in, 400)
	if !ok {
		t.Fatal("expected summarize pack compact")
	}
	_, body, _, splitOK := hostmarker.SplitToolJSONBody(out)
	if !splitOK {
		t.Fatal("compacted output must remain handle+JSON")
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(body), &obj); err != nil {
		t.Fatalf("compacted body must be JSON: %v", err)
	}
	pack, _ := obj["pack"].(map[string]any)
	remaining, _ := pack["skeleton"].([]any)
	if len(remaining) >= len(skeleton) {
		t.Fatalf("skeleton must shrink under budget; got %d of %d rows", len(remaining), len(skeleton))
	}
	rollups := 0
	for _, r := range remaining {
		row, _ := r.(map[string]any)
		if kind, _ := row["kind"].(string); kind == "directory_rollup" {
			rollups++
		}
	}
	if rollups != 3 {
		t.Fatalf("directory_rollup rows = %d after trim, want all 3 (breadth coverage outlives drilled symbol rows)", rollups)
	}
}

func TestTrimSummarizeChunkKeepsTaskMatchingSubstance(t *testing.T) {
	payload := map[string]any{
		"task": "how does auth verify",
		"pack": map[string]any{
			"identity": []map[string]any{
				{"path": "pkg", "kind": "dir_map", "line_count": 2, "parse_health": "ok"},
			},
			"substance": []map[string]any{
				{"path": "pkg/auth/verify.go", "symbol": "VerifyToken", "body": strings.Repeat("a", 1200)},
				{"path": "pkg/logging/trace.go", "symbol": "TraceEvent", "body": strings.Repeat("b", 1200)},
			},
		},
		"selected": 0, "total": 2,
	}
	raw, err := json.Marshal(payload)
	testutil.FailErr(t, "marshal summarize payload", err)
	in := "[summarize#4]\n" + string(raw)

	out, ok := compaction.TrimSummarizeChunk(in, 500)
	if !ok {
		t.Fatal("expected summarize pack compact")
	}
	_, body, _, splitOK := hostmarker.SplitToolJSONBody(out)
	if !splitOK {
		t.Fatal("compacted output must remain handle+JSON")
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(body), &obj); err != nil {
		t.Fatalf("compacted body must be JSON: %v", err)
	}
	pack, _ := obj["pack"].(map[string]any)
	remaining, _ := pack["substance"].([]any)
	if len(remaining) != 1 {
		t.Fatalf("substance len = %d want 1 after task-scored trim", len(remaining))
	}
	row, _ := remaining[0].(map[string]any)
	if sym, _ := row["symbol"].(string); sym != "VerifyToken" {
		t.Fatalf("kept symbol %q want VerifyToken", sym)
	}
}

func TestCompactChunkSummarizeSpillsAtCommit(t *testing.T) {
	dir := t.TempDir()
	// Oversized packs exercise the spill backstop.
	substance := make([]map[string]any, 0, 24)
	for i := 0; i < 24; i++ {
		substance = append(substance, map[string]any{
			"path": "a.go",
			"body": strings.Repeat("body-line\n", 80),
		})
	}
	raw, err := json.Marshal(map[string]any{
		"task": "t",
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
		"gather":   map[string]any{"mode": "repo", "candidates": 1, "bytes": 100},
	})
	testutil.FailErr(t, "marshal", err)
	in := "[summarize#1]\n" + string(raw)
	if tokenest.EstimateDefault(in) <= 2400 {
		t.Fatalf("fixture must exceed wire budget: tokens=%d", tokenest.EstimateDefault(in))
	}

	cc := compaction.NewChunkCompactor(compaction.CompactionConfig{
		Enabled:             true,
		ChunkTokenThreshold: 500,
		ChunkTargetTokens:   200,
	}, nil)
	out, meta, err := cc.CompactChunk(context.Background(), compaction.ContentChunk{
		Kind:     compaction.ChunkKindToolResult,
		ToolName: "summarize",
		Class: compaction.ClassifyMessageDiet(compaction.ClassifyMessageDietInput{
			Messages: []compaction.ContextMessage{{ToolName: "summarize"}}, ForceEligible: true,
		}),
		Tokens:      tokenest.EstimateDefault(in),
		HostDataDir: dir,
	}, in)
	testutil.FailErr(t, "CompactChunk", err)
	if meta.Strategy != "spill_summarize" {
		t.Fatalf("strategy=%q want spill_summarize", meta.Strategy)
	}
	if !strings.Contains(out, "wire_spill_path") {
		t.Fatal("expected wire_spill_path pointer")
	}
	bounded, boundedMeta, err := cc.CompactChunk(context.Background(), compaction.ContentChunk{
		Kind:     compaction.ChunkKindToolResult,
		ToolName: "summarize",
		Class: compaction.ClassifyMessageDiet(compaction.ClassifyMessageDietInput{
			Messages: []compaction.ContextMessage{{ToolName: "summarize"}}, ForceEligible: true,
		}),
		Tokens:            tokenest.EstimateDefault(in),
		HostDataDir:       t.TempDir(),
		MaxToolSpillBytes: 1024,
	}, in)
	testutil.FailErr(t, "CompactChunk with spill bound", err)
	if boundedMeta.Strategy == "spill_summarize" || bounded != in {
		t.Fatal("summarize pack spilled beyond the session retention bound")
	}
	_, body, _, ok := hostmarker.SplitToolJSONBody(out)
	if !ok {
		t.Fatal("expected JSON body")
	}
	var residueObj map[string]any
	if err := json.Unmarshal([]byte(body), &residueObj); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if _, ok := residueObj["pack"]; ok {
		t.Fatal("commit residue must not inline pack substance")
	}
	if residueObj["task"] != "t" {
		t.Fatalf("commit residue task = %v want t", residueObj["task"])
	}
	spillRel, _ := residueObj["wire_spill_path"].(string)
	if spillRel == "" {
		t.Fatal("empty wire_spill_path")
	}
	if !tooloutput.IsAgentWireSpillRel(spillRel) {
		t.Fatalf("wire_spill_path should be host-data-relative, got %q", spillRel)
	}
	rawSpill, err := os.ReadFile(tooloutput.DiskPath(dir, spillRel))
	testutil.FailErr(t, "read spill", err)
	// Spill bodies are stored zstd-compressed on disk (blobstore.Store).
	spillBytes, err := zstdcodec.Decompress(rawSpill)
	testutil.FailErr(t, "decompress spill", err)
	if !strings.Contains(string(spillBytes), `"pack"`) {
		t.Fatal("spill file must contain full pack JSON")
	}
	if !strings.Contains(string(spillBytes), "body-line") {
		t.Fatal("spill file must preserve full substance bytes")
	}
}

func TestCompactToolWireSummarizeWireFittedLandsInline(t *testing.T) {
	dir := t.TempDir()
	// Over chunk_token_threshold (1800-class) but under pack.wire_budget_tokens.
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
		t.Fatalf("fixture too small for chunk threshold regression: tokens=%d", tokens)
	}
	if tokens > 2400 {
		t.Fatalf("fixture must stay under wire budget: tokens=%d", tokens)
	}

	c := compaction.NewSimpleCompactor(compaction.CompactionConfig{
		Enabled:             true,
		ChunkTokenThreshold: 800,
		ChunkTargetTokens:   400,
	}, nil)
	out, _, changed, err := c.CompactToolWireIfOversized(context.Background(), compaction.SessionInfo{
		ID:          "s1",
		HostDataDir: dir,
	}, "summarize", in, compaction.CompactToolWireOpts{})
	testutil.FailErr(t, "CompactToolWireIfOversized", err)
	if changed {
		t.Fatalf("wire-fitted summarize must land inline, got changed residue:\n%s", out)
	}
	if out != in {
		t.Fatal("wire-fitted pack body must be unchanged at commit")
	}
	if strings.Contains(out, "wire_spill_path") {
		t.Fatal("wire-fitted pack must not spill at commit")
	}
}

func TestCompactToolWireSummarizeSpillsFullPack(t *testing.T) {
	dir := t.TempDir()
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

	c := compaction.NewSimpleCompactor(compaction.CompactionConfig{
		Enabled:             true,
		ChunkTokenThreshold: 800,
		ChunkTargetTokens:   400,
	}, nil)
	out, meta, changed, err := c.CompactToolWireIfOversized(context.Background(), compaction.SessionInfo{
		ID:          "s1",
		HostDataDir: dir,
	}, "summarize", in, compaction.CompactToolWireOpts{})
	testutil.FailErr(t, "CompactToolWireIfOversized", err)
	if !changed {
		t.Fatal("expected wire compact")
	}
	if meta.Strategy != "spill_summarize" {
		t.Fatalf("strategy=%q want spill_summarize", meta.Strategy)
	}
	_, body, _, ok := hostmarker.SplitToolJSONBody(out)
	if !ok {
		t.Fatal("expected JSON body")
	}
	var residueObj map[string]any
	if err := json.Unmarshal([]byte(body), &residueObj); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if _, ok := residueObj["pack"]; ok {
		t.Fatal("stored wire must not lossy-trim pack inline")
	}
	if !strings.Contains(out, "[summarize#3]") {
		t.Fatal("handle prefix must survive")
	}
	spillRel, _ := residueObj["wire_spill_path"].(string)
	rawSpill, err := os.ReadFile(tooloutput.DiskPath(dir, spillRel))
	testutil.FailErr(t, "read spill", err)
	// Spill bodies are stored zstd-compressed on disk (blobstore.Store).
	spillBytes, err := zstdcodec.Decompress(rawSpill)
	testutil.FailErr(t, "decompress spill", err)
	if !strings.Contains(string(spillBytes), `"pack"`) {
		t.Fatal("spill file must contain full pack")
	}
}

func TestCompactPreservesLiteralHTML(t *testing.T) {
	// Oversized pack forces trimSummarizeWireObject to remarshal; < > & stay literal.
	substance := make([]map[string]any, 0, 12)
	for i := 0; i < 12; i++ {
		substance = append(substance, map[string]any{
			"path": "main.rs",
			"body": `fn f() -> Vec<u8> { "a & b" }` + strings.Repeat("x", 800),
		})
	}
	payload := map[string]any{
		"task": "rust generics",
		"pack": map[string]any{
			"substance": substance,
			"imports":   []string{"<std>"},
		},
	}
	raw, err := json.Marshal(payload)
	testutil.FailErr(t, "marshal fixture", err)
	out, ok := compaction.TrimSummarizeChunk(string(raw), 400)
	if !ok {
		t.Fatal("expected compact change")
	}
	for _, bad := range []string{`\u003c`, `\u003e`, `\u0026`} {
		if strings.Contains(out, bad) {
			t.Fatalf("compact remarsal HTML-escaped %q: %s", bad, out)
		}
	}
}
