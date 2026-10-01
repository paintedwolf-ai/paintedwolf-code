package compaction

import (
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// maxCompactedRowBodyBytes bounds one substance row body in the final clamp.
const maxCompactedRowBodyBytes = 400

// summarizeWireTargetTokens is the default residue size for TrimSummarizeChunk.
func summarizeWireTargetTokens(cfg CompactionConfig) int {
	defaults := DefaultCompactionConfig()
	threshold := cfg.ChunkTokenThreshold
	if threshold <= 0 {
		threshold = defaults.ChunkTokenThreshold
	}
	target := cfg.ChunkTargetTokens
	if target <= 0 {
		target = defaults.ChunkTargetTokens
	}
	// Prefer a rich pack under the ingestion threshold; fall back to the
	// ordinary chunk target when the threshold is configured unusually low.
	rich := threshold - 64
	if rich > target {
		return rich
	}
	return target
}

// TrimSummarizeChunk shrinks an oversized summarize payload without
// paraphrasing it or destroying its JSON shape.
// Unbacked names yield to source windows; coverage rollups outlive both.
// Anchors, identity, next_actions, and gather/completeness stay.
func TrimSummarizeChunk(content string, targetTokens int) (string, bool) {
	if targetTokens <= 0 {
		targetTokens = summarizeWireTargetTokens(CompactionConfig{})
	}
	prefix, body, suffix, ok := hostmarker.SplitToolJSONBody(content)
	if !ok {
		return "", false
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(body), &obj); err != nil {
		return "", false
	}
	pack, ok := obj["pack"].(map[string]any)
	if !ok || pack == nil {
		return "", false
	}

	outBody, changed := trimSummarizeWireObject(obj, pack, targetTokens)
	if !changed {
		return "", false
	}
	return prefix + outBody + suffix, true
}

func trimSummarizeWireObject(obj, pack map[string]any, targetTokens int) (string, bool) {
	raw, err := surveyjson.Marshal(obj)
	if err != nil {
		return "", false
	}
	if tokenest.EstimateDefault(string(raw)) <= targetTokens {
		return string(raw), false
	}

	droppedSubstance := 0
	droppedSkeleton := 0
	changed := false
	task, _ := obj["task"].(string)

	// 1. Fit garnish — recoverable via next_actions / re-summarize.
	for _, key := range []string{"imports", "call_sites", "neighbors"} {
		if _, ok := pack[key]; ok {
			delete(pack, key)
			changed = true
		}
	}
	if raw, err = surveyjson.Marshal(obj); err == nil && tokenest.EstimateDefault(string(raw)) <= targetTokens {
		noteSummarizeTrim(pack, droppedSubstance, droppedSkeleton)
		return string(raw), true
	}

	// 2. Task-scored pack trim: lowest-utility substance/skeleton first;
	// directory_rollup rows outlive drilled symbol rows.
	for steps := 0; steps < 256; steps++ {
		subBefore, _ := pack["substance"].([]any)
		skelBefore, _ := pack["skeleton"].([]any)
		if !summarize.TrimWirePackMapOneStep(task, pack) {
			break
		}
		changed = true
		if subAfter, _ := pack["substance"].([]any); len(subAfter) < len(subBefore) {
			droppedSubstance++
		}
		if skelAfter, _ := pack["skeleton"].([]any); len(skelAfter) < len(skelBefore) {
			droppedSkeleton++
		}
		raw, err = surveyjson.Marshal(obj)
		if err != nil {
			return "", false
		}
		if tokenest.EstimateDefault(string(raw)) <= targetTokens {
			noteSummarizeTrim(pack, droppedSubstance, droppedSkeleton)
			return string(raw), true
		}
	}
	if _, ok := pack["substance"]; ok {
		delete(pack, "substance")
		changed = true
	}

	// 3. Cap sources_touched / sample_matches — orientation noise under pressure.
	if sources, ok := obj["sources_touched"].([]any); ok && len(sources) > 8 {
		obj["sources_touched"] = sources[:8]
		changed = true
	}
	if gather, ok := obj["gather"].(map[string]any); ok {
		if samples, ok := gather["sample_matches"].([]any); ok && len(samples) > 4 {
			gather["sample_matches"] = samples[:4]
			changed = true
		}
	}
	if orch, ok := obj["orchestration"].(map[string]any); ok {
		delete(orch, "curator")
		changed = true
	}

	raw, err = surveyjson.Marshal(obj)
	if err != nil {
		return "", false
	}
	if !changed {
		return "", false
	}
	noteSummarizeTrim(pack, droppedSubstance, droppedSkeleton)
	// Final hard clamp: shorten remaining substance bodies if any leaked back,
	// else leave anchors/identity/next_actions and accept residual size.
	if tokenest.EstimateDefault(string(raw)) > targetTokens {
		if substance, ok := pack["substance"].([]any); ok {
			for i := range substance {
				row, ok := substance[i].(map[string]any)
				if !ok {
					continue
				}
				if body, ok := row["body"].(string); ok {
					row["body"] = runeclamp.ClampBytes(body, maxCompactedRowBodyBytes)
				}
			}
			if rebuilt, err := surveyjson.Marshal(obj); err == nil {
				raw = rebuilt
			}
		}
	}
	return string(raw), true
}

func noteSummarizeTrim(pack map[string]any, droppedSubstance, droppedSkeleton int) {
	if droppedSubstance == 0 && droppedSkeleton == 0 {
		return
	}
	parts := make([]string, 0, 2)
	if droppedSubstance > 0 {
		parts = append(parts, "substance windows trimmed for context budget")
	}
	if droppedSkeleton > 0 {
		parts = append(parts, "skeleton rows trimmed for context budget")
	}
	note := strings.Join(parts, "; ")
	gaps, _ := pack["gaps"].([]any)
	for _, g := range gaps {
		if s, ok := g.(string); ok && s == note {
			return
		}
	}
	pack["gaps"] = append(gaps, note)
}
