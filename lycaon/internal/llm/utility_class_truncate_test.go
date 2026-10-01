package llm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/compaction"
)

// A Requested-class degrade must surface an error, never truncated text with
// a nil error — every caller reads a nil error as a real model answer.
func TestRequestedClassNeverReturnsTruncatedTextAsAnAnswer(t *testing.T) {
	prompt := strings.Repeat("secret-ish rationale prompt content. ", 40)
	r := &RegistrySummarizer{
		Fallback: compaction.TruncateSummarizer{},
		Class:    UtilityClassRequested,
	}

	out, err := r.degradeSummarize(context.Background(), "sys", prompt, 64, true, UtilityClassRequested, errors.New("lite slot down"))
	if err == nil {
		t.Fatalf("degradeSummarize returned text with no error for a Requested class: %q", out)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("degradeSummarize returned %q; a forbidden-truncate class must carry no text", out)
	}
}

// Quality is the one class that may degrade to truncated input.
func TestQualityClassStillAllowsTruncateFallback(t *testing.T) {
	prompt := strings.Repeat("curation candidate. ", 40)
	r := &RegistrySummarizer{
		Fallback: compaction.TruncateSummarizer{},
		Class:    UtilityClassQuality,
	}

	out, err := r.degradeSummarize(context.Background(), "sys", prompt, 64, true, UtilityClassQuality, errors.New("lite slot down"))
	if err != nil {
		t.Fatalf("degradeSummarize err = %v; Quality may truncate", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("Quality class produced no truncated text")
	}
}

// A non-truncating fallback still runs for a forbidden-truncate class.
func TestRequestedClassStillUsesNonTruncatingFallback(t *testing.T) {
	r := &RegistrySummarizer{
		Fallback: compaction.MockSummarizer{Text: "a real fallback answer"},
		Class:    UtilityClassRequested,
	}

	out, err := r.degradeSummarize(context.Background(), "sys", "prompt", 64, true, UtilityClassRequested, errors.New("lite slot down"))
	if err != nil {
		t.Fatalf("degradeSummarize err = %v; a non-truncating fallback must still run", err)
	}
	if out != "a real fallback answer" {
		t.Fatalf("out = %q want the fallback's answer", out)
	}
}

// UnavailableSummarizer carries no text a caller could mistake for output.
func TestUnavailableSummarizerReturnsErrorAndNoText(t *testing.T) {
	out, err := compaction.UnavailableSummarizer{}.Summarize(context.Background(), "sys", "prompt", 64)
	if err == nil {
		t.Fatal("UnavailableSummarizer returned a nil error")
	}
	if out != "" {
		t.Fatalf("out = %q want empty", out)
	}
}
