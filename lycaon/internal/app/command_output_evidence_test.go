package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestRetainedCommandOutputCreditsOnlyDeliveredHeadAndTail(t *testing.T) {
	f, service, project := documentProgram(t)
	configure := f.Context
	f.Context = func(dir string, tc *tools.ToolContext) {
		if configure != nil {
			configure(dir, tc)
		}
		tc.Host.ModelContextWindow = 16000
	}
	var content strings.Builder
	for line := 1; line <= 2000; line++ {
		fmt.Fprintf(&content, "line %04d: saved source content for delivery coverage\n", line)
	}
	testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(f.Dir, "source.txt"), []byte(content.String()), 0o644))
	doc, err := service.Open(t.Context(), project, "source.txt", "r1", "", "window", nil)
	testutil.FailErr(t, "open source document", err)
	out := callDocumentProgram(t, f, "cat source.txt")
	if out.Status != 0 || !out.Truncated || len(out.Outputs) != 1 {
		t.Fatalf("source delivery=%+v", out)
	}
	window := out.Outputs[0]
	if window.HeadEndLine < 1 || window.TailStartLine <= window.HeadEndLine+1 || window.TotalLines != 2000 {
		t.Fatalf("delivery coordinates=%+v", window)
	}
	basis, err := service.AgentReadBase(t.Context(), project.ID, f.Session, doc.ID, nil)
	testutil.FailErr(t, "delivered source establishes editor read evidence", err)
	if basis.ReadWhole || basis.Revision != doc.Revision || len(basis.ReadSpans) != 2 {
		t.Fatalf("source read evidence=%+v", basis)
	}
	head, tail := basis.ReadSpans[0], basis.ReadSpans[1]
	if head.StartLine != 1 || head.EndLine != int(window.HeadEndLine) || tail.StartLine != int(window.TailStartLine) || tail.EndLine != 2000 {
		t.Fatalf("read spans=%+v, delivered window=%+v", basis.ReadSpans, window)
	}
}
