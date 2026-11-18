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
	if _, err := d.Decide(context.Background(), decide.HeadTurnLoad, "state", map[string]decide.Question{"q": {Kind: decide.KindNoul, Instructions: "is it?"}}); !errors.Is(err, decide.ErrUnavailable) {
		t.Fatalf("Decide error = %v want ErrUnavailable", err)
	}
	if _, _, err := d.Rank(context.Background(), decide.HeadCodeRank, "task", []string{"a"}); !errors.Is(err, decide.ErrUnavailable) {
		t.Fatalf("Rank error = %v want ErrUnavailable", err)
	}
}

func TestChoiceCarriesLabels(t *testing.T) {
	t.Parallel()
	if q := decide.Choice("pick", map[string]string{"a": "A"}); q.Kind != decide.KindChoice || q.Options["a"] != "A" {
		t.Fatalf("Choice = %+v", q)
	}
}
