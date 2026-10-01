package tooloutput

import (
	"strings"
	"testing"
)

func TestWireSpillRejectsStructuredJSONOverCap(t *testing.T) {
	dir := t.TempDir()
	cap := 1024
	content := `{"results":[{"path":"a.go","content":"` + strings.Repeat("x", cap+100) + `"}]}`
	out := WireSpillToolOutput(dir, Screened(content), 50, cap)
	if out.RejectCode != ToolOutputSpillCapExceededCode {
		t.Fatalf("reject code=%q want %q", out.RejectCode, ToolOutputSpillCapExceededCode)
	}
	if out.SpillPath != "" {
		t.Fatalf("expected no spill path, got %q", out.SpillPath)
	}
}

func TestWireSpillCapsOpaqueOverCap(t *testing.T) {
	dir := t.TempDir()
	cap := 1024
	content := strings.Repeat("z", cap+50)
	out := WireSpillToolOutput(dir, Screened(content), 50, cap)
	if out.RejectCode != "" {
		t.Fatalf("unexpected reject %q", out.RejectCode)
	}
	if !out.SpillCapped || out.SpillPath == "" {
		t.Fatalf("expected capped opaque spill: capped=%v path=%q", out.SpillCapped, out.SpillPath)
	}
	if data := readSpill(t, dir, out.SpillPath); len(data) != cap {
		t.Fatalf("spill len=%d want %d", len(data), cap)
	}
}
