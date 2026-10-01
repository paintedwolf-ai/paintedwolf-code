package guidance_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
)

func TestFormatOffenderReport_zero(t *testing.T) {
	r := guidance.FormatOffenderReport(nil)
	if r.Count != 0 || r.Sample != "" || r.Omitted != 0 {
		t.Fatalf("zero case = %+v", r)
	}
}

func TestFormatOffenderReport_single(t *testing.T) {
	r := guidance.FormatOffenderReport([]string{"internal/foo.go:1"})
	if r.Count != 1 || r.Sample != "internal/foo.go:1" || r.Omitted != 0 {
		t.Fatalf("single = %+v", r)
	}
}

func TestFormatOffenderReport_many(t *testing.T) {
	tokens := make([]string, 100)
	for i := range tokens {
		tokens[i] = "internal/file_" + strings.Repeat("x", 3) + ".go:" + string(rune('0'+i%10))
	}
	r := guidance.FormatOffenderReport(tokens)
	if r.Count != 100 {
		t.Fatalf("count = %d want 100", r.Count)
	}
	if r.Omitted != 92 {
		t.Fatalf("omitted = %d want 92", r.Omitted)
	}
	if guidance.MaxOffenderDisplay != 8 {
		t.Fatalf("MaxOffenderDisplay = %d", guidance.MaxOffenderDisplay)
	}
	if len(r.Sample) > guidance.MaxOffenderSampleChars {
		t.Fatalf("sample too long: %d", len(r.Sample))
	}
}
