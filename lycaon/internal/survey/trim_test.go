package survey

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
)

func TestTrimCandidatesKeepsHighPriority(t *testing.T) {
	tagged := []taggedRecord{
		{Priority: 1, Label: "low", Record: evidence.Record{Handle: "snap#1", Path: "a.go"}},
		{Priority: 10, Label: "high", Record: evidence.Record{Handle: "snap#2", Path: "b.go"}},
		{Priority: 5, Label: "mid", Record: evidence.Record{Handle: "snap#3", Path: "c.go"}},
	}
	out := retainCandidates(tagged, 2)
	if len(out) != 2 {
		t.Fatalf("len = %d", len(out))
	}
	if out[0].Label != "high" || out[1].Label != "mid" {
		t.Fatalf("trim order = %+v", out)
	}
}
