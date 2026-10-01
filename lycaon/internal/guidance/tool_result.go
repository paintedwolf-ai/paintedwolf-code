package guidance

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// ComposeToolResult builds the wire ToolResult from stated facts.
// It does not interpret the body.
func ComposeToolResult(content string, facts ToolResultFacts, hints *HintConfig) *api.ToolResult {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	return &api.ToolResult{
		Content:      content,
		Outcome:      facts.Resolution(),
		Codes:        append([]string(nil), facts.Codes...),
		Feedback:     feedbackForFacts(facts),
		UiVisibility: uiVisibilityForFacts(hints, facts),
	}
}

func feedbackForFacts(facts ToolResultFacts) []api.ToolFeedback {
	out := make([]api.ToolFeedback, 0, len(facts.Feedback))
	seen := map[string]bool{}
	for _, item := range facts.Feedback {
		out = append(out, api.ToolFeedback{Code: item.Code, Details: cloneDetails(item.Details), Subject: cloneFeedbackSubject(item.Subject)})
		seen[item.Code] = true
	}
	for _, code := range facts.Codes {
		if !seen[code] {
			out = append(out, api.ToolFeedback{Code: code})
		}
	}
	return out
}

// ApplyTaskDispatchMetadata stamps producer-stated worker identity.
func ApplyTaskDispatchMetadata(toolName string, tr *api.ToolResult, dispatch *api.WorkerDispatch) {
	if tr == nil {
		return
	}
	normalizedTool := strings.TrimSpace(strings.ToLower(toolName))
	switch normalizedTool {
	case "task", "delegate_dispatch":
	default:
		return
	}
	tr.Tool = normalizedTool
	if dispatch == nil || strings.TrimSpace(dispatch.WorkerID) == "" {
		return
	}
	copy := *dispatch
	tr.Dispatch = &copy
	// Worker dispatches remain visible in parent chat.
	tr.UiVisibility = api.ToolResultUiVisibilityNormal
}

// uiVisibilityForFacts maps the primary raised code to quiet chrome. A result
// that raised nothing has no banner to quiet, so it stays visible.
func uiVisibilityForFacts(hints *HintConfig, facts ToolResultFacts) api.ToolResultUiVisibility {
	code := facts.PrimaryCode()
	if code == "" {
		return api.ToolResultUiVisibilityNormal
	}
	if hints != nil {
		if entry, ok := hints.HintCodes[code]; ok {
			return uiVisibilityForEntry(entry)
		}
	}
	// A structured code with no registry row defaults to benign.
	return api.ToolResultUiVisibilityBenign
}

func uiVisibilityForEntry(entry HintEntry) api.ToolResultUiVisibility {
	category := strings.ToLower(strings.TrimSpace(entry.Category))
	severity := strings.ToLower(strings.TrimSpace(entry.Severity))
	emit := strings.ToLower(strings.TrimSpace(entry.Emit))

	switch {
	case category == "worker_dispatch":
		return api.ToolResultUiVisibilityNormal
	case category == "informational" || category == "advisory" || category == "expected_behavior":
		return api.ToolResultUiVisibilityBenign
	case severity == "info" || severity == "warning":
		return api.ToolResultUiVisibilityBenign
	case strings.HasPrefix(emit, "guard:"):
		// Quiet chrome regardless of category/severity.
		return api.ToolResultUiVisibilityBenign
	default:
		return api.ToolResultUiVisibilityNormal
	}
}
