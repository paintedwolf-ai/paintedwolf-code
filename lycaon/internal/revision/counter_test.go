package revision_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/revision"
)

func TestCounter_monotonicPerKey(t *testing.T) {
	c := revision.NewCounter()
	if got := c.Bump("a"); got != 1 {
		t.Fatalf("first bump(a) = %d want 1", got)
	}
	if got := c.Bump("a"); got != 2 {
		t.Fatalf("second bump(a) = %d want 2", got)
	}
	if got := c.Get("a"); got != 2 {
		t.Fatalf("get(a) = %d want 2", got)
	}
	if got := c.Get("b"); got != 0 {
		t.Fatalf("get(b) = %d want 0 (independent key)", got)
	}
}

func TestCounter_blankKeyIsZero(t *testing.T) {
	c := revision.NewCounter()
	if got := c.Bump("  "); got != 0 {
		t.Fatalf("bump(blank) = %d want 0", got)
	}
}
