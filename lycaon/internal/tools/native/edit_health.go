package native

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"

	"github.com/lycaon/lycaon/internal/syntaxhealth"
)

type mutationSeam struct {
	FinalStartLine    int
	FinalEndLine      int
	OriginalStartLine int
	OriginalEndLine   int
	Ranges            []syntaxhealth.LineRange
}

func rejectIfSyntaxUnhealthy(ctx context.Context, tool, path string, before *string, after string, seam mutationSeam) error {
	var beforeBytes []byte
	beforeExists := before != nil
	if before != nil {
		beforeBytes = []byte(*before)
	}
	change := syntaxhealth.Evaluate(ctx, path, beforeBytes, beforeExists, []byte(after))
	if change.Transition == syntaxhealth.TransitionAllowed {
		return nil
	}
	data := syntaxRejectData(tool, path, change, []byte(after), seam)
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

func syntaxRejectData(tool, path string, change syntaxhealth.Change, after []byte, seam mutationSeam) map[string]any {
	data := change.FeedbackFacts("candidate")
	data["tool"], data["path"] = tool, path

	ranges := seam.Ranges
	if len(ranges) == 0 {
		ranges = []syntaxhealth.LineRange{{Start: seam.FinalStartLine, End: seam.FinalEndLine}}
	}
	diag, closest, ok := syntaxhealth.NearestDiagnosticForRanges(change.After, ranges)
	if ok {
		data["parse_error"] = diag.Description()
		data["syntax_diagnostic"] = diag
	}
	if context := syntaxhealth.PythonIndentContext(change.After, after, closest.Start, closest.End); len(context) > 0 {
		data["python_indent_context"] = context
		lines := make([]string, len(context))
		for i, row := range context {
			lines[i] = fmt.Sprintf("line %d: indent %d columns (%s), %s", row.Row, row.IndentColumns, row.Indent, row.Text)
		}
		data["python_indent_lines"] = lines
	}
	return data
}
