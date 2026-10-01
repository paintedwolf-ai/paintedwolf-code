package tools

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestContentApprovalCoversOnlyExactBytesOnce(t *testing.T) {
	tc := ToolContext{contentReviews: &contentReviews{}}
	tc.RecordContentApproval("/project/AGENTS.md", "old", "proposed", "approved")
	change := FileChange{Path: "/project/AGENTS.md", Preview: api.ApprovalFileChange{Operation: "write", Before: "old", After: "different"}}
	if tc.contentReviews.consume([]FileChange{change}) {
		t.Fatal("review authorized different bytes")
	}
	change.Preview.After = "proposed"
	if tc.contentReviews.consume([]FileChange{change}) {
		t.Fatal("review authorized the proposal rather than the approved bytes")
	}
	change.Preview.After = "approved"
	change.Path = "/project/nested/AGENTS.md"
	if tc.contentReviews.consume([]FileChange{change}) {
		t.Fatal("review authorized another path")
	}
	change.Path = "/project/AGENTS.md"
	if !tc.contentReviews.consume([]FileChange{change}) {
		t.Fatal("exact review was not accepted")
	}
	if tc.contentReviews.consume([]FileChange{change}) {
		t.Fatal("review was reusable")
	}
}

// A retry of the same proposal inside one invocation reuses the person's
// decision and re-arms the gate for the approved bytes.
func TestContentDecisionCoversARetriedProposal(t *testing.T) {
	tc := ToolContext{contentReviews: &contentReviews{}}
	if _, ok := tc.ContentDecision("/project/AGENTS.md", "old", "proposed"); ok {
		t.Fatal("no decision exists yet")
	}
	tc.RecordContentApproval("/project/AGENTS.md", "old", "proposed", "approved")
	change := FileChange{Path: "/project/AGENTS.md", Preview: api.ApprovalFileChange{Operation: "write", Before: "old", After: "approved"}}
	if !tc.contentReviews.consume([]FileChange{change}) {
		t.Fatal("first landing was not covered")
	}
	final, ok := tc.ContentDecision("/project/AGENTS.md", "old", "proposed")
	if !ok || final != "approved" {
		t.Fatalf("retried decision = %q, %v", final, ok)
	}
	if !tc.contentReviews.consume([]FileChange{change}) {
		t.Fatal("retried landing was not covered again")
	}
	if _, ok := tc.ContentDecision("/project/AGENTS.md", "old", "other"); ok {
		t.Fatal("a different proposal must review again")
	}
}
