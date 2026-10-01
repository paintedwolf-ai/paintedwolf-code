package session

import (
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPeerRejectionFeedRecordsPathNotFound(t *testing.T) {
	feed := NewPeerRejectionFeed()
	content := "Rejected: grep search root lycaon-den/src/hooks does not exist.\n\nCause: The path argument is outside the project tree.\nCode: GREP_PATH_NOT_FOUND"
	feed.RecordPeerToolReject("root-1", "job-a", "GREP_PATH_NOT_FOUND", content, guidance.ToolResultFacts{
		Feedback: []api.ToolFeedback{
			{Code: "GREP_PATH_NOT_FOUND", Details: map[string]any{"path": "lycaon-den/src/hooks"}},
		},
	})
	notes := feed.RecentForRoot("root-1", 5)
	if len(notes) != 1 {
		t.Fatalf("len = %d want 1", len(notes))
	}
	if notes[0].Ref != "lycaon-den/src/hooks" {
		t.Fatalf("ref = %q", notes[0].Ref)
	}
}

func TestPeerRejectionFeedIgnoresNonShareableCode(t *testing.T) {
	feed := NewPeerRejectionFeed()
	feed.RecordPeerToolReject("root-1", "job-a", "SUMMARIZE_EMPTY", "Rejected: summarize gathered zero candidates.\nCode: SUMMARIZE_EMPTY", guidance.ToolResultFacts{})
	if len(feed.RecentForRoot("root-1", 5)) != 0 {
		t.Fatal("expected no peer notes for non-shareable code")
	}
}

func TestPeerRejectionFeedDedupsAdjacent(t *testing.T) {
	feed := NewPeerRejectionFeed()
	content := "Rejected: find walk root missing does not exist.\nCode: FIND_PATH_NOT_FOUND"
	facts := guidance.ToolResultFacts{
		Feedback: []api.ToolFeedback{
			{Code: "FIND_PATH_NOT_FOUND", Details: map[string]any{"path": "missing"}},
		},
	}
	feed.RecordPeerToolReject("root-1", "job-a", "FIND_PATH_NOT_FOUND", content, facts)
	feed.RecordPeerToolReject("root-1", "job-b", "FIND_PATH_NOT_FOUND", content, facts)
	if len(feed.RecentForRoot("root-1", 5)) != 1 {
		t.Fatalf("len = %d want 1 duplicate suppressed", len(feed.RecentForRoot("root-1", 5)))
	}
}

func TestPeerRejectSummaryDoomLoop(t *testing.T) {
	if got := peerRejectSummary("DOOM_LOOP_REPEAT", guidance.ToolResultFacts{}, "Rejected: Identical grep call blocked after 8 repeats.\nCode: DOOM_LOOP_REPEAT"); got == "" {
		t.Fatal("expected doom loop summary")
	}
}
