package native

import (
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
	"testing"
)

func TestExtractionFailureKeepsCompletePathsOutsideBoundedCopy(t *testing.T) {
	budget := &extractBudget{entries: 500, bytes: 500}
	for i := 0; i < 500; i++ {
		budget.out = append(budget.out, extractEntryResult{Path: strings.Repeat("p", 80) + string(rune('a'+i))})
	}
	cause := &toolrejection.ToolReject{Code: "EXTRACT_ENTRY_LIMIT", Data: map[string]any{"max_entries": 500}}
	err := extractionFailure(cause, "archive.zip", "output", budget)
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || !errors.Is(err, cause) {
		t.Fatalf("lost extraction failure: %v", err)
	}
	paths := reject.Data["extracted_paths"].([]string)
	sample := reject.Data["extracted_paths_sample"].([]string)
	omitted := reject.Data["extracted_paths_omitted"].(int)
	if len(paths) != 500 || len(sample) == 0 || len(sample) >= len(paths) || len(sample)+omitted != len(paths) {
		t.Fatalf("lost complete extraction accounting: paths=%d sample=%d omitted=%d", len(paths), len(sample), omitted)
	}
	for i, path := range sample {
		if path != paths[i] {
			t.Fatal("sample changed a completed path")
		}
	}
	if len(strings.Join(sample, ", ")) > 2100 {
		t.Fatal("recovery sample exceeds its bounded presentation budget")
	}
	if got := extractionPathSample([]string{strings.Repeat("x", 3000)}); len(got) != 0 {
		t.Fatal("oversized path was truncated into a different path")
	}
}
