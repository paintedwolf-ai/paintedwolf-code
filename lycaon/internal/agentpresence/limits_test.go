package agentpresence

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestOutputLimitsKeepWhatTheModelReceived(t *testing.T) {
	a := Read{Target: Target{RootID: "r", Path: "a"}, Extent: api.AgentPresenceExtentMatches, Spans: []Span{line(1, 1), line(1, 1), line(4, 4)}, ItemSpans: []int{2, 1}}
	b := Read{Target: Target{RootID: "r", Path: "b"}, Extent: api.AgentPresenceExtentMatches, Spans: []Span{line(7, 7)}, ItemSpans: []int{1}}
	kept := 1
	got := WithinLimit([]Read{a, b}, OutputLimit{KeptEntries: &kept})
	if len(got) != 1 || len(got[0].Spans) != 2 {
		t.Fatalf("first entry = %+v", got)
	}
	kept = 3
	if got := WithinLimit([]Read{a, b}, OutputLimit{KeptEntries: &kept}); len(got) != 2 || len(got[1].Spans) != 1 {
		t.Fatalf("three entries = %+v", got)
	}
	content := Read{Target: Target{RootID: "r", Path: "a"}, Extent: api.AgentPresenceExtentRange, Spans: []Span{line(10, 90)}}
	if got := WithinLimit([]Read{content}, OutputLimit{KeptThroughLine: 40}); got[0].Spans[0].EndLine != 40 {
		t.Fatalf("clamped content = %+v", got)
	}
	whole := Read{Target: Target{RootID: "r", Path: "a"}, Extent: api.AgentPresenceExtentWholeFile}
	if got := WithinLimit([]Read{whole}, OutputLimit{KeptThroughLine: 12}); got[0].Extent != api.AgentPresenceExtentRange || got[0].Spans[0].EndLine != 12 {
		t.Fatalf("clamped whole file = %+v", got)
	}
	if got := WithinLimit([]Read{content}, OutputLimit{Spilled: true}); got != nil {
		t.Fatalf("spilled output kept %+v", got)
	}
}
