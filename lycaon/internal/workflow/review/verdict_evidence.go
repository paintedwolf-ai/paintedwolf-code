package review

import (
	"context"
	"github.com/lycaon/lycaon/internal/evidence"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"time"
)

func (m *Verdicts) persistReviewLoopEvidence(
	ctx context.Context,
	sessionID string,
	active *api.WorkflowRun,
	rl workflowdef.ReviewLoopDef,
	phaseID string,
	verdict map[string]string,
	citedEvidence []api.CitationGroundingCitedEvidence,
	citedURLs []string,
	recordID string,
	recordedAt time.Time,
	attempt int,
) error {
	if m == nil || m.EvidenceStore == nil {
		return nil
	}
	// Citations anchor durable review evidence.
	if len(citedEvidence) == 0 {
		return nil
	}
	projectDir := ""
	if m.EvidenceProjectDir != nil {
		dir, err := m.EvidenceProjectDir(ctx, sessionID)
		if err != nil {
			return err
		}
		projectDir = dir
	}
	if strings.TrimSpace(projectDir) == "" {
		return nil
	}
	artifacts := verdictArtifacts(verdict, citedEvidence, citedURLs)
	rec := evidence.GateRecord(
		evidence.GateType(strings.TrimSpace(rl.EvidenceKey)),
		phaseID,
		active.ID,
		workflowvalidation.ReviewLoopVerdictEvidenceVerdict(rl, verdict),
		strings.TrimSpace(verdict["verdict"]),
		artifacts,
		"",
		"",
		"",
		attempt,
		recordedAt.UTC(),
	)
	rec.RecordID = recordID
	if err := m.EvidenceStore.Append(ctx, projectDir, rec); err != nil {
		return err
	}
	if m.OnGateEvidencePersisted != nil {
		m.OnGateEvidencePersisted(ctx, sessionID, active.ID, rec)
	}
	return nil
}

func verdictArtifacts(verdict map[string]string, cited []api.CitationGroundingCitedEvidence, citedURLs []string) map[string]any {
	out := make(map[string]any, len(verdict)+2)
	for k, v := range verdict {
		out[k] = v
	}
	citedAny := make([]any, 0, len(cited))
	for _, c := range cited {
		citedAny = append(citedAny, map[string]any{
			"handle":  c.Handle,
			"path":    c.Path,
			"line":    c.Line,
			"excerpt": c.Excerpt,
		})
	}
	out[evidence.CitedEvidenceArtifactKey] = citedAny
	urls := make([]any, 0, len(citedURLs))
	for _, u := range citedURLs {
		if u = strings.TrimSpace(u); u != "" {
			urls = append(urls, u)
		}
	}
	out[evidence.CitedURLsArtifactKey] = urls
	return out
}
