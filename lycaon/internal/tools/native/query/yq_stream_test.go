package query

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/toolrejection"
)

func TestYqStreamLimitsAndCancellation(t *testing.T) {
	for _, source := range []string{"abc", "abcd"} {
		r := &yqInput{ctx: t.Context(), source: strings.NewReader(source), maximum: 3}
		out, err := io.ReadAll(r)
		if string(out) != "abc" {
			t.Fatalf("decoded prefix=%q", out)
		}
		if source == "abc" && err != nil {
			t.Fatalf("at-limit stream refused: %v", err)
		}
		if source == "abcd" {
			reject := toolrejection.AsToolReject(err)
			if reject == nil || reject.Code != "JQ_INPUT_TOO_LARGE" {
				t.Fatalf("over-limit stream error=%v", err)
			}
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r := &yqInput{ctx: ctx, source: strings.NewReader("abc"), maximum: 3}
	if _, err := r.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled stream=%v", err)
	}
}
