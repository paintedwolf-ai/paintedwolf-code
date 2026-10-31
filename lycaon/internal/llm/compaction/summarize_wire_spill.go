package compaction

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

const summarizeWireSpillStrategy = "spill_summarize"

// summarizeWireEstimateSlackPct absorbs envelope overhead added at commit so
// a wire-fitted pack remains inline.
const summarizeWireEstimateSlackPct = 6

// summarizeCommitInlineCeiling returns the token ceiling before spilling a summarize pack.
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

// compactSpillSummarize spills the full summarize payload to the host data dir
// and returns a pointer residue.
func (c *ChunkCompactor) compactSpillSummarize(ctx context.Context, chunk ContentChunk, content string, originalTokens int) (string, string, bool) {
	if originalTokens <= summarizeCommitInlineCeiling() {
		return content, "", false
	}
	hostDataDir := strings.TrimSpace(chunk.HostDataDir)
	if hostDataDir == "" {
		return content, "", false
	}
	out := tooloutput.SpillWholeToolOutput(ctx, hostDataDir, tooloutput.Screened(content), chunk.MaxToolSpillBytes)
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
	for _, key := range []string{"task", "selected", "total", "truncated", "anchors", "highlight_handles"} {
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
