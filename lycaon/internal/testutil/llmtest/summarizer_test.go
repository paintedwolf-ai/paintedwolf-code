package llmtest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSummarizerPreservesTheCallersFormatMarker(t *testing.T) {
	type formatKey struct{}
	reader := func(ctx context.Context) bool { value, _ := ctx.Value(formatKey{}).(bool); return value }
	fixture := Summarizer{Text: "retain this fact", Structured: reader}
	plain, err := fixture.Summarize(t.Context(), "", "", 0)
	testutil.FailErr(t, "plain summary", err)
	if plain != fixture.Text {
		t.Fatalf("plain summary=%q", plain)
	}
	raw, err := fixture.Summarize(context.WithValue(t.Context(), formatKey{}, true), "", "", 0)
	testutil.FailErr(t, "structured summary", err)
	var fields map[string][]string
	testutil.FailErr(t, "decode summary", json.Unmarshal([]byte(raw), &fields))
	for _, name := range []string{"facts", "completed", "pending", "reacquire", "constraints"} {
		if fields[name] == nil {
			t.Fatalf("missing continuation array %s", name)
		}
	}
	if len(fields["facts"]) != 1 || fields["facts"][0] != fixture.Text {
		t.Fatalf("facts=%v", fields["facts"])
	}
	failure := errors.New("summary unavailable")
	fixture.Err = failure
	fixture.Structured = func(context.Context) bool { t.Fatal("format reader called after failure"); return true }
	if text, err := fixture.Summarize(t.Context(), "", "", 0); text != "" || !errors.Is(err, failure) {
		t.Fatalf("failed summary=%q, %v", text, err)
	}
}
