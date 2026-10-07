package evidence

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/hostmarker"
)

func TestReadToolBodyIsLineIndexed(t *testing.T) {
	lines := []string{"package main", "", "func main() {}", "// comment"}
	rendered := hostmarker.FormatNumberedLines(lines, 10)
	readOut, err := json.Marshal(map[string]any{
		"path":        "main.go",
		"mode":        "content",
		"content":     rendered,
		"offset":      10,
		"limit":       len(lines),
		"total_lines": len(lines) + 9,
	})
	if err != nil {
		t.Fatalf("marshal read output: %v", err)
	}

	rec := BuildEvidenceRecord("", "read", map[string]any{"path": "main.go", "offset": 10}, string(readOut))
	idx := lineContentIndex(rec, "main.go")

	if len(idx) != len(lines) {
		t.Fatalf("lineContentIndex returned %d lines, want %d", len(idx), len(lines))
	}
	for i, want := range lines {
		got, ok := idx[10+i]
		if !ok {
			t.Errorf("line %d missing from index", 10+i)
		} else if got != want {
			t.Errorf("line %d: got %q, want %q", 10+i, got, want)
		}
	}
}
