package orchestration

import (
	"strings"
	"testing"
)

func TestAggregateUnionDedupesLines(t *testing.T) {
	out := aggregateUnion([]string{
		"alpha\nbeta",
		"beta\ngamma",
	})
	if out != "alpha\nbeta\ngamma" {
		t.Fatalf("union = %q", out)
	}
}

func TestAggregateIntersect(t *testing.T) {
	out := aggregateIntersect([]string{
		"alpha\nbeta\nshared",
		"beta\nshared",
		"shared\nzeta",
	})
	if out != "shared" {
		t.Fatalf("intersect = %q", out)
	}
}

func TestAggregateMergeSections(t *testing.T) {
	out := aggregateMerge([]string{"first", "second"})
	if !containsAll(out, "## Subtask 0", "first", "## Subtask 1", "second") {
		t.Fatalf("merge = %q", out)
	}
}

func TestAggregateVotePicksLongest(t *testing.T) {
	out := aggregateVote([]string{"short", "much longer winning output here"})
	if out != "much longer winning output here" {
		t.Fatalf("vote = %q", out)
	}
}

func TestAggregateFanOutResultsModes(t *testing.T) {
	outputs := []string{"a\nb", "b\nc"}
	if got := aggregateFanOutResults(AggregationUnion, outputs); got != "a\nb\nc" {
		t.Fatalf("union wrapper = %q", got)
	}
	if got := aggregateFanOutResults(AggregationIntersect, outputs); got != "b" {
		t.Fatalf("intersect wrapper = %q", got)
	}
}

func containsAll(text string, parts ...string) bool {
	for _, part := range parts {
		if !contains(text, part) {
			return false
		}
	}
	return true
}

func contains(text, part string) bool {
	return strings.Contains(text, part)
}
