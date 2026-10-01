package webindex

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func starved(t *testing.T, s *Store, n int) []StarvedQuery {
	t.Helper()
	out, err := s.StarvedQueries(context.Background(), n)
	testutil.FailErr(t, "starved queries", err)
	return out
}

func TestStarvedQueriesPicksUnderfilledOutcomes(t *testing.T) {
	s := openTest(t)
	s.QueueSearchOutcome(t.Context(), "rare widget firmware", "project-a", "/project-a", 1, 10) // starved
	s.QueueSearchOutcome(t.Context(), "common widget docs", "project-a", "/project-a", 10, 10)  // filled
	s.Flush()
	got := starved(t, s, 5)
	if len(got) != 1 || got[0].Query != "rare widget firmware" || got[0].ProjectID != "project-a" {
		t.Fatalf("starved = %v want the underfilled query only", got)
	}
}

func TestStarvedQueriesLatestOutcomeWins(t *testing.T) {
	s := openTest(t)
	// The latest successful outcome removes starvation.
	s.QueueSearchOutcome(t.Context(), "widget guide", "", "", 0, 10)
	s.QueueSearchOutcome(t.Context(), "widget guide", "", "", 8, 10)
	// The latest underfilled outcome restores starvation.
	s.QueueSearchOutcome(t.Context(), "frobnicator api", "", "", 9, 10)
	s.QueueSearchOutcome(t.Context(), "frobnicator api", "", "", 2, 10)
	s.Flush()
	got := starved(t, s, 5)
	if len(got) != 1 || got[0].Query != "frobnicator api" {
		t.Fatalf("starved = %v want latest outcome to decide", got)
	}
}

func TestMarkQueryRewarmedRemovesFromStarvedSet(t *testing.T) {
	s := openTest(t)
	s.QueueSearchOutcome(t.Context(), "rare widget firmware", "", "", 0, 10)
	s.Flush()
	if got := starved(t, s, 5); len(got) != 1 {
		t.Fatalf("starved = %v want one before mark", got)
	}
	s.MarkQueryRewarmed(t.Context(), "rare widget firmware", "")
	s.Flush()
	if got := starved(t, s, 5); len(got) != 0 {
		t.Fatalf("starved = %v want empty after re-warm", got)
	}
	// A fresh starved outcome makes the query eligible for warming again.
	s.QueueSearchOutcome(t.Context(), "rare widget firmware", "", "", 0, 10)
	s.Flush()
	if got := starved(t, s, 5); len(got) != 1 {
		t.Fatalf("starved = %v want re-qualified after new failure", got)
	}
}

func TestSearchOutcomesRecencyCapped(t *testing.T) {
	s := openTest(t)
	for i := 0; i < maxOutcomeRows+20; i++ {
		s.QueueSearchOutcome(t.Context(), "q", "", "", 0, 10)
	}
	s.Flush()
	var n int
	testutil.FailErr(t, "count", s.db.QueryRow(`SELECT COUNT(*) FROM search_outcomes`).Scan(&n))
	if n != maxOutcomeRows {
		t.Fatalf("rows = %d want capped at %d", n, maxOutcomeRows)
	}
}
