package decide_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/decide"
)

func TestAbsentAbstainsEverywhere(t *testing.T) {
	t.Parallel()
	var d decide.Decider = decide.Absent{}
	if d.Available() {
		t.Fatal("Absent must not be available")
	}
	if _, err := d.Decide(context.Background(), decide.HeadTurnLoad, "state", map[string]decide.Question{"q": decide.Noul("is it?")}); !errors.Is(err, decide.ErrUnavailable) {
		t.Fatalf("Decide error = %v want ErrUnavailable", err)
	}
	if _, _, err := d.Rank(context.Background(), decide.HeadCodeRank, "task", []string{"a"}); !errors.Is(err, decide.ErrUnavailable) {
		t.Fatalf("Rank error = %v want ErrUnavailable", err)
	}
}

func TestQuestionConstructorsCarryTheirKind(t *testing.T) {
	t.Parallel()
	if q := decide.Choice("pick", map[string]string{"a": "A"}); q.Kind != decide.KindChoice || q.Options["a"] != "A" {
		t.Fatalf("Choice = %+v", q)
	}
	if q := decide.Score("rate", "low", "high"); q.Kind != decide.KindScore || len(q.Levels) != 2 {
		t.Fatalf("Score = %+v", q)
	}
	if q := decide.Noul("yes?"); q.Kind != decide.KindNoul {
		t.Fatalf("Noul = %+v", q)
	}
}
