package survey

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/runeclamp"
)

// CurateSynthesisEvidence runs ONE Curate pass and renders evidence_digest markdown.
func CurateSynthesisEvidence(
	ctx context.Context,
	curator llm.Curator,
	snapshot evidence.Ledger,
	parentSessionID string,
	delegationBrief string,
	phaseLabel string,
	envs []WorkerEnvelope,
) (string, llm.CurationResult, error) {
	if curator == nil || len(snapshot.Handles) == 0 {
		return "", llm.CurationResult{}, nil
	}
	focus := llm.CurationFocus{
		Tool:   "synthesis",
		View:   "batch",
		Target: strings.TrimSpace(parentSessionID),
		Task:   resolveSynthesisCurateTask(delegationBrief, snapshot, phaseLabel),
	}
	result, err := curator.Curate(ctx, snapshot, focus, SynthesisCurateBudget)
	if err != nil {
		return "", llm.CurationResult{}, err
	}
	digest := RenderEvidenceDigest(result, workerLabelsFromEnvelopes(envs))
	return digest, result, nil
}

func resolveSynthesisCurateTask(delegationBrief string, snapshot evidence.Ledger, phaseLabel string) string {
	if brief := strings.TrimSpace(delegationBrief); brief != "" {
		return runeclamp.Clamp(brief, 600)
	}
	return defaultSynthesisCurateTask(snapshot, phaseLabel)
}

func defaultSynthesisCurateTask(snapshot evidence.Ledger, phaseLabel string) string {
	var objectives []string
	for _, handle := range sortedLedgerHandles(snapshot) {
		rec, ok := snapshot.Handles[handle]
		if !ok || rec.Kind != "worker_report" {
			continue
		}
		for _, line := range rec.Body {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "objective: ") {
				objectives = append(objectives, strings.TrimPrefix(line, "objective: "))
			}
		}
	}
	phase := strings.TrimSpace(phaseLabel)
	if phase == "" {
		phase = "investigate closeout"
	}
	if len(objectives) > 0 {
		n := len(objectives)
		if n > 3 {
			n = 3
		}
		return fmt.Sprintf("synthesis: rank worker evidence for %s — focus: %s", phase, strings.Join(objectives[:n], "; "))
	}
	return fmt.Sprintf("synthesis: rank worker evidence for %s", phase)
}

// RenderEvidenceDigest formats curated selections into synthesis navigation markdown.
func RenderEvidenceDigest(result llm.CurationResult, workerLabels []string) string {
	if len(result.Selections) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Evidence digest (curated)\n")
	if len(workerLabels) > 0 {
		b.WriteString("Workers: ")
		b.WriteString(strings.Join(workerLabels, ", "))
		b.WriteString("\n")
	}
	for _, sel := range result.Selections {
		handle := strings.TrimSpace(sel.Resolution.Handle)
		if handle == "" {
			handle = strings.TrimSpace(sel.Triple.Path)
		}
		path := strings.TrimSpace(sel.Resolution.Path)
		line := sel.Resolution.Line
		excerpt := strings.Join(sel.Lines, " ")
		if excerpt == "" {
			excerpt = strings.TrimSpace(sel.Triple.Excerpt)
		}
		if path == "" && excerpt == "" {
			continue
		}
		if path == "" {
			fmt.Fprintf(&b, "[%s] — %s\n", handle, excerpt)
			continue
		}
		if line <= 0 {
			line = 1
		}
		fmt.Fprintf(&b, "[%s] %s:%d — %s\n", handle, path, line, excerpt)
	}
	if len(result.Gloss) > 0 {
		b.WriteString("\nNavigation: ")
		labels := make([]string, 0, len(result.Gloss))
		for _, g := range result.Gloss {
			if label := strings.TrimSpace(g.Label); label != "" {
				labels = append(labels, label)
			}
		}
		b.WriteString(strings.Join(labels, "; "))
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "selected/total: %d/%d\n", result.Report.Selected, result.Report.Total)
	return strings.TrimRight(b.String(), "\n")
}

func workerLabelsFromEnvelopes(envs []WorkerEnvelope) []string {
	labels := make([]string, 0, len(envs))
	for _, env := range envs {
		label := strings.TrimSpace(env.AgentType)
		if label == "" {
			label = "worker"
		}
		if job := strings.TrimSpace(env.JobID); job != "" {
			label = label + "/" + job
		}
		labels = append(labels, label)
	}
	return labels
}

// TruncateTail keeps the trailing maxBytes of s when over budget.
func TruncateTail(s string, maxBytes int) string {
	s = strings.TrimSpace(s)
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	return "…(truncated)\n" + s[len(s)-maxBytes:]
}
