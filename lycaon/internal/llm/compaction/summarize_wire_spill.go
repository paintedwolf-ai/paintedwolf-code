package compaction

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

const summarizeWireSpillStrategy = "spill_summarize"

// summarizeWireEstimateSlackPct absorbs the bytes a pack gains between the two
// measurements, so a wire-fitted pack stays inline at commit. The wire fit
// measures the marshalled result; commit measures the tool-result body after the
// banner, receipt, and evidence handle are attached.
const summarizeWireEstimateSlackPct = 6

// summarizeCommitInlineCeiling is the single inline/spill rail for anchor_residue.
// SkipOversizedChunk and compactSpillSummarize both use it — no second threshold.
func summarizeCommitInlineCeiling() int {
	caps := summarize.DefaultCaps()
	wb := caps.Pack.WireBudgetTokens
	if wb <= 0 {
		wb = caps.Pack.InputBudgetTokens
	}
	if wb <= 0 {
		return DefaultCompactionConfig().ChunkTokenThreshold
	}
	return wb + wb*summarizeWireEstimateSlackPct/100
}

// compactSpillSummarize spills the full handle+JSON summarize payload to the
// project host data dir and returns a pointer residue (wire_spill_path + orientation).
// No pack substance is trimmed inline — full bytes live in the spill file.
func (c *ChunkCompactor) compactSpillSummarize(chunk ContentChunk, content string, originalTokens int) (string, string, bool) {
	if originalTokens <= summarizeCommitInlineCeiling() {
		return content, "", false
	}
	hostDataDir := strings.TrimSpace(chunk.HostDataDir)
	if hostDataDir == "" {
		return content, "", false
	}
	out := tooloutput.SpillWholeToolOutput(hostDataDir, tooloutput.Screened(content), chunk.MaxToolSpillBytes)
	if out.RejectCode != "" || out.SpillPath == "" {
		return content, "", false
	}
	rel := out.SpillPath
	pointer, err := summarizeSpillPointer(content, rel)
	if err != nil {
		return content, "", false
	}
	var b strings.Builder
	b.WriteString(hostmarker.CompactionBanner(fmt.Sprintf(
		"tool_result — summarize pack ~%d tokens; full pack at wire_spill_path", originalTokens)) + "\n")
	b.WriteString(pointer)
	return b.String(), summarizeWireSpillStrategy, true
}

func summarizeSpillPointer(content, spillRel string) (string, error) {
	spillRel = strings.TrimSpace(spillRel)
	if spillRel == "" {
		return "", fmt.Errorf("empty spill path")
	}
	prefix, body, suffix, ok := hostmarker.SplitToolJSONBody(content)
	if !ok {
		return tooloutput.InjectWireSpillPath(content, spillRel), nil
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(body), &obj); err != nil {
		return "", err
	}
	ptr := map[string]any{
		"wire_spill_path": spillRel,
	}
	for _, key := range []string{"task", "selected", "total", "truncated"} {
		if v, exists := obj[key]; exists {
			ptr[key] = v
		}
	}
	if g, ok := obj["gather"].(map[string]any); ok && g != nil {
		orient := map[string]any{}
		for _, key := range []string{"mode", "path", "candidates", "bytes"} {
			if v, exists := g[key]; exists {
				orient[key] = v
			}
		}
		if len(orient) > 0 {
			ptr["gather"] = orient
		}
	}
	out, err := surveyjson.Marshal(ptr)
	if err != nil {
		return "", err
	}
	return prefix + string(out) + suffix, nil
}
