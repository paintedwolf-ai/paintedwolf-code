package workercompletion

import "github.com/lycaon/lycaon/pkg/api"

// ReportWire copies the internal validated report into its durable/wire shape.
func ReportWire(in WorkerCompletionReport) *api.WorkerCompletionReport {
	out := &api.WorkerCompletionReport{
		LegStatus: in.LegStatus, FilesModified: append([]string(nil), in.FilesModified...),
		ObjectivesMet:     append([]string(nil), in.ObjectivesMet...),
		RemainingRisk:     append([]string(nil), in.RemainingRisk...),
		SuggestedNextTask: in.SuggestedNextTask, Brief: in.Brief,
		CitedURLs: append([]string(nil), in.CitedURLs...),
	}
	for _, finding := range in.Findings {
		out.Findings = append(out.Findings, api.WorkerCompletionFinding{
			Path: finding.Path, Evidence: finding.Evidence, Line: finding.Line,
			Excerpt: finding.Excerpt, Note: finding.Note, Claim: finding.Claim,
			Adversary: finding.Adversary, Precondition: finding.Precondition, Severity: finding.Severity,
		})
	}
	for _, obligation := range in.EvidenceObligations {
		out.EvidenceObligations = append(out.EvidenceObligations, api.WorkerEvidenceObligation{
			Kind: obligation.Kind, Status: obligation.Status,
			EvidenceRefs: append([]string(nil), obligation.EvidenceRefs...), Reason: obligation.Reason,
		})
	}
	return out
}

// ReportFromWire restores the internal report used by the parent-card projector.
func ReportFromWire(in *api.WorkerCompletionReport) WorkerCompletionReport {
	if in == nil {
		return WorkerCompletionReport{}
	}
	out := WorkerCompletionReport{
		LegStatus: in.LegStatus, FilesModified: append([]string(nil), in.FilesModified...),
		ObjectivesMet:     append([]string(nil), in.ObjectivesMet...),
		RemainingRisk:     append([]string(nil), in.RemainingRisk...),
		SuggestedNextTask: in.SuggestedNextTask, Brief: in.Brief,
		CitedURLs: append([]string(nil), in.CitedURLs...),
	}
	for _, finding := range in.Findings {
		out.Findings = append(out.Findings, WorkerFinding{
			Path: finding.Path, Evidence: finding.Evidence, Line: finding.Line,
			Excerpt: finding.Excerpt, Note: finding.Note, Claim: finding.Claim,
			Adversary: finding.Adversary, Precondition: finding.Precondition, Severity: finding.Severity,
		})
	}
	for _, obligation := range in.EvidenceObligations {
		out.EvidenceObligations = append(out.EvidenceObligations, EvidenceObligation{
			Kind: obligation.Kind, Status: obligation.Status,
			EvidenceRefs: append([]string(nil), obligation.EvidenceRefs...), Reason: obligation.Reason,
		})
	}
	return out
}
