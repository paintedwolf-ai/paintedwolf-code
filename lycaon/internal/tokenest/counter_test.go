package tokenest

import (
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestEmbeddedTextCounter(t *testing.T) {
	for _, encoding := range []string{"o200k_base", "cl100k_base"} {
		counter, err := NewCounter(encoding)
		testutil.FailErr(t, "construct text counter", err)
		for text, want := range map[string]int{"": 0, "hello": 1, "hello world": 2, strings.Repeat(" alphabet", 755): 755} {
			got, err := counter.Count(text)
			testutil.FailErr(t, "count ordinary text", err)
			if got != want || counter.Method() != encoding {
				t.Fatalf("%s counted %d, want %d", encoding, got, want)
			}
		}
	}
	if _, err := NewCounter("unknown"); err == nil {
		t.Fatal("unsupported encoding silently estimated")
	}
	proxy, err := NewCounter("")
	testutil.FailErr(t, "construct estimate counter", err)
	got, err := proxy.Count("hello")
	testutil.FailErr(t, "estimate", err)
	if got != EstimateDefault("hello") || proxy.Method() != "estimated" {
		t.Fatal("unlabeled fallback")
	}
}

func TestTextCounterRejectsPathologicalRunsBeforeEncoding(t *testing.T) {
	counter, err := NewCounter("o200k_base")
	testutil.FailErr(t, "counter", err)
	for _, text := range []string{strings.Repeat("a", 4097), strings.Repeat(" ", 4097), strings.Repeat("short words ", 100000)} {
		if _, err := counter.Count(text); !errors.Is(err, ErrTextLimit) {
			t.Fatalf("unbounded encoding accepted: %v", err)
		}
	}
}
