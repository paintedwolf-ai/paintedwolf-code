package survey

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
)

// EvidenceInput drives optional synthesis curate for one coordinator batch.
type EvidenceInput struct {
	Ctx             context.Context
	ParentSessionID string
	Envelopes       []WorkerEnvelope
	MergedBytes     int
	WorkerCount     int
	Reader          guidance.EvidenceLedgerReader
	Curator         llm.Curator
	DelegationBrief string
	PhaseLabel      string
	TopologyOutput  string
	AllowCurate     bool
}

// EvidenceOutput carries rendered digest and optional truncated topology tail.
type EvidenceOutput struct {
	EvidenceDigest string
	TopologyOutput string
	Stats          SnapshotStats
	Curated        bool
}

// MaybeCurateSynthesisEvidence curates when allowed and thresholds fire; otherwise passthrough.
func MaybeCurateSynthesisEvidence(in EvidenceInput) (EvidenceOutput, error) {
	out := EvidenceOutput{TopologyOutput: strings.TrimSpace(in.TopologyOutput)}
	if !in.AllowCurate || in.Curator == nil || in.Reader == nil || strings.TrimSpace(in.ParentSessionID) == "" {
		return out, nil
	}
	stats := SnapshotStats{
		MergedBytes: in.MergedBytes,
		WorkerCount: in.WorkerCount,
	}
	out.Stats = stats
	if !ShouldCurateSynthesis(stats) {
		LogSynthesisCurate(in.Ctx, in.ParentSessionID, false, 0, 0)
		return out, nil
	}
	snapshot, stats, err := BuildSynthesisSnapshot(in.Ctx, SnapshotInput{
		Envelopes:   in.Envelopes,
		MergedBytes: in.MergedBytes,
		WorkerCount: in.WorkerCount,
		Reader:      in.Reader,
	})
	if err != nil {
		return out, err
	}
	out.Stats = stats
	digest, result, err := CurateSynthesisEvidence(
		in.Ctx,
		in.Curator,
		snapshot,
		in.ParentSessionID,
		in.DelegationBrief,
		in.PhaseLabel,
		in.Envelopes,
	)
	if err != nil {
		return out, err
	}
	digest = strings.TrimSpace(digest)
	if digest == "" {
		LogSynthesisCurate(in.Ctx, in.ParentSessionID, false, result.Report.Selected, result.Report.Total)
		return out, nil
	}
	out.EvidenceDigest = digest
	out.Curated = true
	LogSynthesisCurate(in.Ctx, in.ParentSessionID, true, result.Report.Selected, result.Report.Total)
	if out.TopologyOutput != "" {
		out.TopologyOutput = TruncateTail(out.TopologyOutput, SynthesisTopologyTailBytes)
	}
	return out, nil
}

// DelegationBriefFromScaffold extracts coordinator delegation focus from the
// fanout plan stamped for phase.
func DelegationBriefFromScaffold(vars map[string]any, phase string) string {
	if vars == nil {
		return ""
	}
	plans, _ := vars["fanout_plans"].(map[string]any)
	if planRaw, ok := plans[strings.TrimSpace(phase)].(map[string]any); ok {
		var parts []string
		if tm, _ := planRaw["threat_model"].(string); strings.TrimSpace(tm) != "" {
			parts = append(parts, strings.TrimSpace(tm))
		}
		if rationale, _ := planRaw["rationale"].(string); strings.TrimSpace(rationale) != "" {
			parts = append(parts, strings.TrimSpace(rationale))
		}
		if legs, ok := planRaw["legs"].([]any); ok {
			for _, item := range legs {
				m, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if prompt, _ := m["prompt"].(string); strings.TrimSpace(prompt) != "" {
					parts = append(parts, strings.TrimSpace(prompt))
				}
			}
		}
		if joined := strings.TrimSpace(strings.Join(parts, " ")); joined != "" {
			return joined
		}
	}
	if brief, ok := vars["coordinator_brief"].(string); ok {
		return strings.TrimSpace(brief)
	}
	return ""
}
