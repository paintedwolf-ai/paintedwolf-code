package compaction_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tokenest"
)

// TestTranscriptDietForToolCommand pins the catalog declaration: command
// program outcomes are window-bounded by CommandOutputBudget, so the
// transcript diet must respect that bound instead of re-cutting the window.
func TestTranscriptDietForToolCommand(t *testing.T) {
	b := evidence.ActiveBinding()
	if got := b.TranscriptDietForTool("command"); got != evidence.TranscriptDietShapeBounded {
		t.Fatalf("command diet = %q want shape_bounded", got)
	}
}

// TestCompactToolWireCommandProgramOutcomeStaysInline is the regression for
// the merge-queue conformance failure: a windowed program outcome whose
// stdout carries the survey JSON stream crossed chunk_token_threshold once
// the surveyed tree grew, and the commit diet cut stdout to 1024 bytes
// mid-document, so the retained stream no longer decoded. The window is the
// tool's own bound; the commit diet must leave it inline.
func TestCompactToolWireCommandProgramOutcomeStaysInline(t *testing.T) {
	drills := make([]string, 0, 160)
	for i := 0; i < 160; i++ {
		drills = append(drills, `{"path":"internal/pkg/domain/service/adapter/file`+strings.Repeat("x", i%9)+`_`+strconv.Itoa(i)+`.go","kind":"file","lines":`+strconv.Itoa(120+i)+`,"symbols":["Handle`+strings.Repeat("y", i%5)+`","Close`+strings.Repeat("z", i%3)+`"]}`)
	}
	stdout := strings.Join([]string{
		`{"outline":{"files":160,"dirs":6,"languages":["go"]}`,
		`{"repo_map":{"root":".","depth":2,"drill":[` + strings.Join(drills, ",") + `]}`,
		`{"survey":{"paths_scanned":160,"bytes":91234,"complete":true}`,
	}, "\n")
	outcome := map[string]any{
		"result_version": 2,
		"outcome":        "completed",
		"program_id":     "prog_01",
		"detail_ref":     "@program/prog_01",
		"state":          "completed",
		"status":         0,
		"stdout":         stdout,
		"stderr":         "",
		"stdout_ref":     "@output/prog_01.stdout",
		"truncated":      false,
	}
	raw, err := json.Marshal(outcome)
	testutil.FailErr(t, "marshal program outcome", err)
	in := string(raw)

	cfg := compaction.CompactionConfig{Enabled: true, ChunkTokenThreshold: 1800, ChunkTargetTokens: 1000}
	tokens := tokenest.EstimateDefault(in)
	if tokens < cfg.ChunkTokenThreshold {
		t.Fatalf("fixture must cross the commit threshold to guard the regression: tokens=%d threshold=%d", tokens, cfg.ChunkTokenThreshold)
	}

	c := compaction.NewSimpleCompactor(cfg, nil)
	out, meta, changed, err := c.CompactToolWireIfOversized(t.Context(), compaction.SessionInfo{
		ID:          "s1",
		HostDataDir: t.TempDir(),
	}, "command", in, compaction.CompactToolWireOpts{})
	testutil.FailErr(t, "CompactToolWireIfOversized", err)
	if changed {
		t.Fatalf("windowed command outcome must stay inline at commit, got strategy=%q residue:\n%s", meta.Strategy, out)
	}
	if out != in {
		t.Fatal("command outcome content must be unchanged at commit")
	}
	if strings.Contains(out, "wire_spill_path") || strings.Contains(out, "wire_compacted") {
		t.Fatal("command outcome must not be wire-compacted at commit")
	}
}
