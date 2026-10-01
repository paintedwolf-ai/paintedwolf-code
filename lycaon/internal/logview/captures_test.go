package logview

import "testing"

func TestSummarizeCapture(t *testing.T) {
	sessions := []SessionRecord{
		{SessionID: "c", AgentType: "coordinator", Task: "<!-- marker -->\nFix the bug"},
		{SessionID: "w1", ParentSessionID: "c", AgentType: "command-verifier"},
		{SessionID: "w2", ParentSessionID: "c", AgentType: "command-verifier"},
	}
	s := summarizeCapture("20260622T203110Z", "/tmp/x", sessions)
	if s.Headline != "Fix the bug" {
		t.Errorf("headline = %q, want stripped of the marker", s.Headline)
	}
	if s.AgentCount != 3 || s.WorkerCount != 2 {
		t.Errorf("counts = %d agents / %d workers, want 3 / 2", s.AgentCount, s.WorkerCount)
	}
	if s.When.IsZero() {
		t.Error("When should parse from the capture name")
	}
}

func TestParseCaptureTime(t *testing.T) {
	if parseCaptureTime("20260622T203110Z").IsZero() {
		t.Error("valid timestamp name should parse")
	}
	if !parseCaptureTime("not-a-timestamp").IsZero() {
		t.Error("invalid name should yield zero time")
	}
}
