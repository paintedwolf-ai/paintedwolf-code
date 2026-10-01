package worker

import (
	"context"

	"github.com/lycaon/lycaon/internal/syntaxhealth"
)

type promoteSyntaxIssue struct {
	path   string
	change syntaxhealth.Change
}

func validatePromoteSyntax(ctx context.Context, plans []promoteMutation) *promoteSyntaxIssue {
	for _, plan := range plans {
		if !plan.targetExists {
			continue
		}
		change := syntaxhealth.EvaluateFinal(ctx, plan.path, plan.target)
		if change.Transition != syntaxhealth.TransitionAllowed {
			return &promoteSyntaxIssue{path: plan.path, change: change}
		}
	}
	return nil
}

func (issue *promoteSyntaxIssue) rejectData(jobID string) map[string]any {
	data := issue.change.FeedbackFacts("merged")
	data["job_id"], data["path"] = jobID, issue.path
	if diag, ok := syntaxhealth.NearestDiagnostic(issue.change.After, 0, 0); ok {
		data["parse_error"] = diag.Description()
		data["syntax_diagnostic"] = diag
	}
	return data
}
