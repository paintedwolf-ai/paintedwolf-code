package command

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/scratch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCommandOutputBudgetAndStreamSplitting(t *testing.T) {
	if got := CommandOutputBudget(0); got != 64<<10 {
		t.Fatalf("0 token budget = %d, want %d", got, 64<<10)
	}
	if got := CommandOutputBudget(16384); got != 12<<10 {
		t.Fatalf("16k token budget = %d, want %d", got, 12<<10)
	}
	if got := CommandOutputBudget(32768); got != 12<<10 {
		t.Fatalf("32k token budget = %d, want %d", got, 12<<10)
	}
	if got := CommandOutputBudget(64000); got != 32<<10 {
		t.Fatalf("64k token budget = %d, want %d", got, 32<<10)
	}
	if got := CommandOutputBudget(128000); got != 64<<10 {
		t.Fatalf("128k token budget = %d, want %d", got, 64<<10)
	}
	if got := CommandOutputBudget(200000); got != 64<<10 {
		t.Fatalf("200k token budget = %d, want %d", got, 64<<10)
	}

	if out, errB := splitStreamBudgets(12<<10, 4096, 0); out != 12<<10 || errB != 0 {
		t.Fatalf("stdout-only split = (%d, %d)", out, errB)
	}
	if out, errB := splitStreamBudgets(12<<10, 0, 4096); out != 0 || errB != 12<<10 {
		t.Fatalf("stderr-only split = (%d, %d)", out, errB)
	}
	if out, errB := splitStreamBudgets(12<<10, 4096, 4096); out != 6<<10 || errB != 6<<10 {
		t.Fatalf("dual-stream split = (%d, %d)", out, errB)
	}
}

func TestWindowStreamOutputSnapsToLineBoundariesAndRendersNotice(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 500; i++ {
		fmt.Fprintf(&b, "line-%04d: abcdefghijklmnopqrstuvwxyz0123456789\n", i)
	}
	data := []byte(b.String())
	outputPath := "@output/prog-1.stdout"
	delivered, win, ranges, err := windowStreamOutput(t.Context(), data, 12<<10, "stdout", "output:program:prog-1:stdout", outputPath)
	testutil.FailErr(t, "windowStreamOutput", err)

	if !win.Truncated || len(ranges) != 2 {
		t.Fatalf("expected truncated window with 2 ranges, got win=%+v ranges=%v", win, ranges)
	}
	if data[win.HeadEndByte-1] != '\n' || data[win.TailStartByte-1] != '\n' {
		t.Fatalf("head/tail did not snap to newline boundary: headEnd=%d tailStart=%d", win.HeadEndByte, win.TailStartByte)
	}
	wantNoticeSub := fmt.Sprintf("(sed -n '%d,%dp' %s) ...", win.HeadEndLine+1, win.HeadEndLine+150, outputPath)
	if !strings.Contains(delivered, wantNoticeSub) {
		t.Fatalf("delivered output missing expected notice %q:\n%s", wantNoticeSub, delivered)
	}
}

func TestPublishOutputAndParseSource(t *testing.T) {
	outputDir := t.TempDir()
	testutil.FailErr(t, "publishOutputCopy", scratch.PublishCommandOutput(outputDir, "prog-42", "stdout", strings.NewReader("alpha\nbeta\n")))
	target := filepath.Join(outputDir, "prog-42.stdout")
	info, err := os.Stat(target)
	testutil.FailErr(t, "stat output copy", err)
	if info.Mode().Perm() != 0o444 {
		t.Fatalf("mode = %o, want 0444", info.Mode().Perm())
	}

	prog, stream, ok := parseOutputSource("disk:"+target, outputDir)
	if !ok || prog != "prog-42" || stream != "stdout" {
		t.Fatalf("parseOutputSource(%q) = (%q, %q, %v)", target, prog, stream, ok)
	}
	prog2, stream2, ok2 := parseOutputSource("disk:/@output/prog-42.stderr", outputDir)
	if !ok2 || prog2 != "prog-42" || stream2 != "stderr" {
		t.Fatalf("parseOutputSource(@output) = (%q, %q, %v)", prog2, stream2, ok2)
	}

}

func windowStreamOutput(ctx context.Context, data []byte, budget int, streamName, ref, outputPath string) (string, api.CommandProgramOutputWindow, [][2]int64, error) {
	sample, err := sampleOutput(bytes.NewReader(data), budget)
	if err != nil {
		return "", api.CommandProgramOutputWindow{}, nil, err
	}
	delivered, window, parts, err := sample.window(ctx, streamName, ref, outputPath, true)
	var ranges [][2]int64
	for _, part := range parts {
		if len(part.data) > 0 {
			ranges = append(ranges, [2]int64{part.start, part.start + int64(len(part.data))})
		}
	}
	return delivered, window, ranges, err
}
