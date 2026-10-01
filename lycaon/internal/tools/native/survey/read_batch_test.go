package survey

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/tools/readcaps"
)

func rangeArg(offset, limit int) map[string]any {
	return map[string]any{"offset": float64(offset), "limit": float64(limit)}
}

func TestParseReadRangeSpecsWithinBudget(t *testing.T) {
	specs, note, err := parseReadRangeSpecs(map[string]any{
		"ranges": []any{rangeArg(1, 100), rangeArg(200, 50)},
	})
	if err != nil {
		t.Fatalf("parseReadRangeSpecs error: %v", err)
	}
	if note != "" {
		t.Fatalf("unexpected clamp note: %q", note)
	}
	if len(specs) != 2 || specs[0].Limit != 100 || specs[1].Limit != 50 {
		t.Fatalf("specs = %+v", specs)
	}
}

func TestParseReadRangeSpecsClampsLineBudget(t *testing.T) {
	// Three full-page ranges request 3×LineLimit lines against a 2×LineLimit
	// budget: the first two survive intact, the third is dropped.
	specs, note, err := parseReadRangeSpecs(map[string]any{
		"ranges": []any{
			rangeArg(1, readcaps.LineLimit),
			rangeArg(500, readcaps.LineLimit),
			rangeArg(1000, readcaps.LineLimit),
		},
	})
	if err != nil {
		t.Fatalf("parseReadRangeSpecs error: %v", err)
	}
	if note == "" {
		t.Fatal("expected clamp note for over-budget request")
	}
	if len(specs) != 2 {
		t.Fatalf("specs = %+v", specs)
	}
	total := 0
	for _, s := range specs {
		total += s.Limit
	}
	if total != readcaps.BatchMaxTotalLines {
		t.Fatalf("served lines = %d want %d", total, readcaps.BatchMaxTotalLines)
	}
	if !strings.Contains(note, "call read again") {
		t.Fatalf("note missing continuation guidance: %q", note)
	}
}

func TestParseReadRangeSpecsShrinksBoundaryRange(t *testing.T) {
	// LineLimit + (LineLimit-500) + 1000 against a 2×LineLimit budget: the third
	// range is shrunk to the 500 remaining lines instead of being rejected.
	first := readcaps.LineLimit
	second := readcaps.LineLimit - 500
	thirdWant := 1000
	remaining := readcaps.BatchMaxTotalLines - first - second
	specs, note, err := parseReadRangeSpecs(map[string]any{
		"ranges": []any{rangeArg(1, first), rangeArg(500, second), rangeArg(900, thirdWant)},
	})
	if err != nil {
		t.Fatalf("parseReadRangeSpecs error: %v", err)
	}
	if note == "" {
		t.Fatal("expected clamp note")
	}
	if len(specs) != 3 || specs[2].Limit != remaining {
		t.Fatalf("specs = %+v want third.Limit=%d", specs, remaining)
	}
}

func TestParseReadRangeSpecsClampsRangeArity(t *testing.T) {
	raw := make([]any, 0, readcaps.BatchMaxRanges+3)
	for i := 0; i < readcaps.BatchMaxRanges+3; i++ {
		raw = append(raw, rangeArg(1+i*10, 5))
	}
	specs, note, err := parseReadRangeSpecs(map[string]any{"ranges": raw})
	if err != nil {
		t.Fatalf("parseReadRangeSpecs error: %v", err)
	}
	if len(specs) != readcaps.BatchMaxRanges {
		t.Fatalf("specs = %d want %d", len(specs), readcaps.BatchMaxRanges)
	}
	if note == "" {
		t.Fatal("expected clamp note for over-arity request")
	}
}
