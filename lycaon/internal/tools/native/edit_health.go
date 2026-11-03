package native

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"

	"github.com/lycaon/lycaon/internal/syntaxhealth"
)

func rejectIfSyntaxUnhealthy(ctx context.Context, tool, path string, before *string, after string) error {
	var beforeBytes []byte
	beforeExists := before != nil
	if before != nil {
		beforeBytes = []byte(*before)
	}
	change := syntaxhealth.Evaluate(ctx, path, beforeBytes, beforeExists, []byte(after))
	if change.Transition == syntaxhealth.TransitionAllowed {
		return nil
	}
	data := syntaxRejectData(tool, path, change)
	data["mutation_new_path"] = !beforeExists
	switch change.Transition {
	case syntaxhealth.TransitionNewFileBroken, syntaxhealth.TransitionBrokeClean:
		return &toolrejection.ToolReject{Code: "MUTATION_BROKE_PARSE", Data: data}
	case syntaxhealth.TransitionRepairRegressed:
		return &toolrejection.ToolReject{Code: "MUTATION_REPAIR_NOT_IMPROVED", Data: data}
	case syntaxhealth.TransitionParseIncomplete:
		return &toolrejection.ToolReject{Code: "MUTATION_PARSE_INCOMPLETE", Data: data}
	default:
		return &toolrejection.ToolReject{Code: "MUTATION_PARSE_FAILED", Data: data}
	}
}

func syntaxRejectData(tool, path string, change syntaxhealth.Change) map[string]any {
	data := change.FeedbackFacts("candidate")
	data["tool"], data["path"] = tool, path

	diag, ok := syntaxhealth.NearestDiagnostic(change.After, 0, 0)
	if ok {
		data["parse_error"] = diag.Description()
		data["syntax_diagnostic"] = diag
	}
	return data
}
