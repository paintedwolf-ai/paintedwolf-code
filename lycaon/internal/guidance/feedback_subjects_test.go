package guidance

import (
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestResultRetainsSameCodeForDistinctSubjects(t *testing.T) {
	first := ToolResultFacts{}.WithFeedback("CHECK", map[string]any{"reason": "first"}, &api.FeedbackSubject{Kind: "task", ID: "first"})
	second := ToolResultFacts{}.WithFeedback("CHECK", map[string]any{"reason": "second"}, &api.FeedbackSubject{Kind: "task", ID: "second"})
	merged := first.Merge(second).Merge(first)
	result := ComposeToolResult("completed", merged, nil)
	if len(result.Codes) != 1 || len(result.Feedback) != 2 {
		t.Fatalf("feedback lost or duplicated: %#v", result)
	}
	if result.Feedback[1].Subject.ID != "second" || result.Feedback[1].Details["reason"] != "second" {
		t.Fatal("second subject details lost")
	}
}

func TestResultFeedbackDetachesNestedDetails(t *testing.T) {
	details := map[string]any{"nested": map[string]any{"value": "original"}}
	facts := ToolResultFacts{}.WithFeedback("CHECK", details, nil)
	details["nested"].(map[string]any)["value"] = "changed"
	if facts.Feedback[0].Details["nested"].(map[string]any)["value"] != "original" {
		t.Fatal("feedback retained mutable producer details")
	}
}
