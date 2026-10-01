package compaction

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/zstdcodec"
)

func TestCompactSpillListPreservesMiddleViaSpill(t *testing.T) {
	dir := t.TempDir()
	results := make([]any, 500)
	for i := range results {
		results[i] = map[string]any{"path": "plans/item_" + strconv.Itoa(i) + ".md", "type": "file"}
	}
	results[268] = map[string]any{"path": "plans/mid_list_sentinel.md", "type": "file"}
	raw, err := json.Marshal(map[string]any{
		"results":       results,
		"total_results": 500,
		"offset":        0,
	})
	testutil.FailErr(t, "marshal", err)
	content := "[find#1]\n" + string(raw)

	cfg := testCompactionConfig()
	cfg.ChunkTargetTokens = 500
	compactor := NewChunkCompactor(cfg, nil)
	chunk := ContentChunk{
		Kind:        ChunkKindToolResult,
		ToolName:    "find",
		Class:       testChunkDiet("find", ""),
		Tokens:      tokenest.EstimateDefault(content),
		HostDataDir: dir,
	}
	out, meta, err := compactor.CompactChunk(context.Background(), chunk, content)
	testutil.FailErr(t, "CompactChunk", err)
	if meta.Strategy != "spill_structured" {
		t.Fatalf("strategy=%q want spill_structured", meta.Strategy)
	}
	if !strings.HasPrefix(out, "[compacted tool_result") {
		t.Fatalf("missing compacted banner: %q", out[:minInt(80, len(out))])
	}
	if !strings.Contains(out, `"plans/item_0.md"`) {
		t.Fatal("expected head window in residue")
	}
	if !strings.Contains(out, `"plans/item_499.md"`) {
		t.Fatal("expected tail window in residue")
	}
	if strings.Contains(out, "mid_list_sentinel.md") {
		t.Fatal("mid-list sentinel must not appear in residue windows")
	}
	if !strings.Contains(out, `"wire_spill_path"`) {
		t.Fatalf("missing wire_spill_path in residue: %q", out[:minInt(200, len(out))])
	}
	_, body, _, ok := hostmarker.SplitToolJSONBody(out)
	if !ok {
		t.Fatal("residue should contain JSON body")
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(body), &obj); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	spillRel, _ := obj["wire_spill_path"].(string)
	if spillRel == "" {
		t.Fatal("empty wire_spill_path")
	}
	if !tooloutput.IsAgentWireSpillRel(spillRel) {
		t.Fatalf("wire_spill_path should be host-data-relative, got %q", spillRel)
	}
	raw2, err := os.ReadFile(tooloutput.DiskPath(dir, spillRel))
	testutil.FailErr(t, "read spill", err)
	// Spill bodies are stored zstd-compressed on disk (blobstore.Store).
	spillBytes, err := zstdcodec.Decompress(raw2)
	testutil.FailErr(t, "decompress spill", err)
	if !strings.Contains(string(spillBytes), "mid_list_sentinel.md") {
		t.Fatal("spill file must contain mid-list sentinel at index 268")
	}
}

func TestCompactSpillIndexNonListUnchanged(t *testing.T) {
	cfg := testCompactionConfig()
	cfg.ChunkTargetTokens = 200
	compactor := NewChunkCompactor(cfg, nil)
	content := strings.Repeat("stdout line\n", 400) + "MIDDLE_OPAQUE\n" + strings.Repeat("tail line\n", 400)
	chunk := ContentChunk{Class: testChunkDiet("command", ""), Kind: ChunkKindToolResult, ToolName: "command", Tokens: tokenest.EstimateDefault(content)}
	out, meta, err := compactor.CompactChunk(context.Background(), chunk, content)
	testutil.FailErr(t, "CompactChunk", err)
	if meta.Strategy != "spill_index" {
		t.Fatalf("strategy=%q want spill_index", meta.Strategy)
	}
	if !strings.Contains(out, "...[compacted middle omitted]...") {
		t.Fatalf("expected char head/tail marker: %q", out[:minInt(200, len(out))])
	}
}

func TestCompactToolWireListHandlePrefixed(t *testing.T) {
	dir := t.TempDir()
	results := make([]any, 120)
	for i := range results {
		results[i] = map[string]any{"path": strings.Repeat("x", 80) + "/p" + strconv.Itoa(i)}
	}
	raw, _ := json.Marshal(map[string]any{"results": results, "total_results": 120})
	content := "[find#3]\n" + string(raw)

	c := NewSimpleCompactor(CompactionConfig{
		Enabled:             true,
		ChunkTokenThreshold: 50,
		ChunkTargetTokens:   500,
	}, nil)
	out, meta, changed, err := c.CompactToolWireIfOversized(context.Background(), SessionInfo{
		ID:          "s1",
		HostDataDir: dir,
	}, "find", content, CompactToolWireOpts{})
	testutil.FailErr(t, "CompactToolWireIfOversized", err)
	if !changed {
		t.Fatal("expected compact change")
	}
	if meta.Strategy != "spill_structured" {
		t.Fatalf("strategy=%q want spill_structured", meta.Strategy)
	}
	if !strings.Contains(out, "[find#3]") {
		t.Fatal("handle prefix must survive")
	}
	if !strings.Contains(out, "wire_spill_path") {
		t.Fatal("expected wire_spill_path")
	}
}

func TestBoundedGitPagesActuallyShrinkAndRetainRecovery(t *testing.T) {
	for _, count := range []int{2, 80} {
		files := make([]any, count)
		for i := range files {
			files[i] = map[string]any{"path": "source.go", "diff": strings.Repeat("+observed\n", 500)}
		}
		raw, err := json.Marshal(map[string]any{"available": true, "files": files, "files_total": 900, "files_truncated": true, "next_offset": count})
		testutil.FailErr(t, "marshal Git page", err)
		cfg := CompactionConfig{Enabled: true, ChunkTokenThreshold: 1800, ChunkTargetTokens: 1000}
		c := NewSimpleCompactor(cfg, nil)
		out, meta, changed, err := c.CompactToolWireIfOversized(t.Context(), SessionInfo{ID: "s", HostDataDir: t.TempDir()}, "git_diff", string(raw), CompactToolWireOpts{})
		testutil.FailErr(t, "compact Git page", err)
		if !changed || meta.CompactedTokens > cfg.ChunkTargetTokens || meta.CompactedTokens >= meta.OriginalTokens {
			t.Fatalf("count %d: changed=%v meta=%+v", count, changed, meta)
		}
		if !strings.Contains(out, "wire_spill_path") || !strings.Contains(out, `"files_total":900`) {
			t.Fatal("lost recovery or totals")
		}
	}
}

func TestStructuredCompactionWithoutStoragePreservesObservation(t *testing.T) {
	content := `{"files":[{"path":"a.go","diff":"` + strings.Repeat("x", 20000) + `"}],"files_truncated":true}`
	c := NewSimpleCompactor(CompactionConfig{Enabled: true, ChunkTokenThreshold: 100, ChunkTargetTokens: 200}, nil)
	out, _, changed, err := c.CompactToolWireIfOversized(t.Context(), SessionInfo{ID: "s"}, "git_diff", content, CompactToolWireOpts{})
	testutil.FailErr(t, "compact without storage", err)
	if changed || out != content {
		t.Fatal("discarded output without recovery")
	}
}

func TestGrowingCompactionMarkerDoesNotSuppressRetry(t *testing.T) {
	cfg := testCompactionConfig()
	cfg.ChunkTokenThreshold = 100
	cfg.ChunkTargetTokens = 500
	cfg.KeepRecentMessages = 1
	raw := `{"files":[{"path":"a.go","diff":"` + strings.Repeat("x", 20000) + `"}],"files_truncated":true}`
	content := hostmarker.CompactionBanner("tool_result — ineffective old projection") + "\n" + raw
	messages := []ContextMessage{{ID: "git", Role: "tool", ToolName: "git_diff", Content: content, CompactedChunk: &CompactedChunkMeta{OriginalTokens: 5000, CompactedTokens: 5100}}, {Role: "assistant", Content: "continue"}}
	chunks := FindOversizedChunks(messages, cfg, nil)
	if len(chunks) != 1 {
		t.Fatalf("ineffective marker hid output: %v", chunks)
	}
	chunks[0].HostDataDir = t.TempDir()
	out, meta, err := NewChunkCompactor(cfg, nil).CompactChunk(t.Context(), chunks[0], content)
	testutil.FailErr(t, "retry ineffective compaction", err)
	if meta.CompactedTokens > cfg.ChunkTargetTokens || meta.Strategy == "" {
		t.Fatalf("retry failed: %+v", meta)
	}
	if _, _, _, ok := hostmarker.SplitToolJSONBody(out); !ok {
		t.Fatal("nested banners obscured structured output")
	}
}

func TestCompactionHonorsSpillLimitAndRetriesWhenRaised(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	for _, tc := range []struct{ tool, content string }{
		{"command", strings.Repeat("observed output\n", 2000) + "original tail"},
		{"git_diff", `{"files":[{"path":"a.go","diff":"` + strings.Repeat("x", 20000) + `"}]}`},
	} {
		for _, mode := range []string{"wire", "transcript"} {
			t.Run(tc.tool+"/"+mode, func(t *testing.T) {
				cfg := testCompactionConfig()
				cfg.ChunkTokenThreshold, cfg.ChunkTargetTokens = 100, 200
				cfg.ChunkMinSavingsTokens, cfg.ChunkProtectedMinSavingsTokens = 1, 1
				cfg.KeepRecentMessages = 1
				c := NewSimpleCompactor(cfg, nil)
				info := SessionInfo{ID: "session", HostDataDir: t.TempDir(), ChunkProjections: chunkMemoFixture{}}
				for _, bound := range []int{1024, len(tc.content) + 1024} {
					info.MaxToolSpillBytes = bound
					var out string
					var changed bool
					if mode == "wire" {
						var err error
						out, _, changed, err = c.CompactToolWireIfOversized(t.Context(), info, tc.tool, tc.content, CompactToolWireOpts{})
						testutil.FailErr(t, "compact tool wire with spill bound", err)
					} else {
						messages, count := c.CompactOversizedChunksOnly(t.Context(), info, []ContextMessage{
							{ID: "result", Role: "tool", ToolName: tc.tool, Content: tc.content},
							{ID: "tail", Role: "assistant", Content: "recent"},
						})
						out, changed = messages[0].Content, count == 1
					}
					if bound < len(tc.content) {
						if changed || out != tc.content {
							t.Fatal("compaction accepted incomplete retention")
						}
					} else if !changed || len(tooloutput.SpillPaths(out)) == 0 {
						t.Fatal("raising the retention bound did not restore recoverable compaction")
					}
				}
			})
		}
	}
}
