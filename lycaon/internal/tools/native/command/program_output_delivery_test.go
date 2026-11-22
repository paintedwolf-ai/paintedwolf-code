package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/scratch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/pkg/api"
)

func sedDeliveryReach() toolreach.Request {
	return guidance.StockToolReach(toolreach.ProgramTool).WithPrograms(func(name string) bool {
		return name == "sed"
	})
}

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

func TestRetainOutcomePublicationEligibility(t *testing.T) {
	clean := &tooloutput.StreamSpill{Complete: true}
	limited := &tooloutput.StreamSpill{Complete: false, Failure: &tooloutput.StreamSpillLimitError{Maximum: 256 << 20, Observed: 300 << 20}}
	failed := &tooloutput.StreamSpill{Complete: false, Failure: errors.New("blob copy failed mid-stream")}
	screenErr := errors.New("screening aborted")

	spill, err := retainOutcome(clean, nil)
	if err != nil || spill != clean {
		t.Fatalf("clean spill = (%v, %v), want (spill, nil)", spill, err)
	}
	spill, err = retainOutcome(limited, nil)
	if err != nil || spill != limited {
		t.Fatalf("limit-bounded spill = (%v, %v), want (spill, nil)", spill, err)
	}
	spill, err = retainOutcome(failed, nil)
	if spill != nil || err == nil || !errors.Is(err, failed.Failure) {
		t.Fatalf("failed spill = (%v, %v), want (nil, failure)", spill, err)
	}
	spill, err = retainOutcome(failed, screenErr)
	if spill != nil || err == nil || !errors.Is(err, failed.Failure) || !errors.Is(err, screenErr) {
		t.Fatalf("failed spill with screen error = (%v, %v), want (nil, joined)", spill, err)
	}
	spill, err = retainOutcome(nil, screenErr)
	if spill != nil || !errors.Is(err, screenErr) {
		t.Fatalf("missing spill = (%v, %v), want (nil, screenErr)", spill, err)
	}
	spill, err = retainOutcome(nil, nil)
	if spill != nil || err != nil {
		t.Fatalf("missing spill without error = (%v, %v), want (nil, nil)", spill, err)
	}
}

func TestWindowStreamOutputMarksIncompleteCapture(t *testing.T) {
	data := []byte("alpha one\nalpha two\nalpha three\n")
	outputPath := "@output/prog-1.stdout"
	sample, err := sampleOutput(bytes.NewReader(data), 12<<10)
	testutil.FailErr(t, "sample output", err)
	delivered, win, parts, err := sample.window(t.Context(), sedDeliveryReach(), "stdout", "output:program:prog-1:stdout", outputPath, false)
	testutil.FailErr(t, "window incomplete capture", err)

	if win.Complete || !win.Truncated {
		t.Fatalf("window = %+v, want complete=false truncated=true", win)
	}
	if !strings.HasPrefix(delivered, string(data)) {
		t.Fatalf("delivered output lost captured prefix:\n%s", delivered)
	}
	if !strings.Contains(delivered, "saved output is incomplete") {
		t.Fatalf("delivered output missing incomplete notice:\n%s", delivered)
	}
	if len(parts) != 1 || parts[0].start != 0 || !bytes.Equal(parts[0].data, data) {
		t.Fatalf("parts = %+v, want the full captured prefix", parts)
	}

	var large strings.Builder
	for i := 1; i <= 500; i++ {
		fmt.Fprintf(&large, "line-%04d: abcdefghijklmnopqrstuvwxyz0123456789\n", i)
	}
	largeData := []byte(large.String())
	sample, err = sampleOutput(bytes.NewReader(largeData), 12<<10)
	testutil.FailErr(t, "sample large output", err)
	delivered, win, _, err = sample.window(t.Context(), sedDeliveryReach(), "stdout", "output:program:prog-1:stdout", outputPath, false)
	testutil.FailErr(t, "window large incomplete capture", err)
	if win.Complete || !win.Truncated {
		t.Fatalf("large window = %+v, want complete=false truncated=true", win)
	}
	if !strings.Contains(delivered, "saved output is incomplete") {
		t.Fatalf("large delivered output missing incomplete notice:\n%s", delivered)
	}
}

func TestPublishOutputAndParseSource(t *testing.T) {
	outputDir := t.TempDir()
	testutil.FailErr(t, "publishOutputCopy", scratch.PublishCommandOutput(outputDir, scratch.CommandOutput{ProgramID: "prog-42", Stream: "stdout"}, strings.NewReader("alpha\nbeta\n")))
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
	delivered, window, parts, err := sample.window(ctx, sedDeliveryReach(), streamName, ref, outputPath, true)
	var ranges [][2]int64
	for _, part := range parts {
		if len(part.data) > 0 {
			ranges = append(ranges, [2]int64{part.start, part.start + int64(len(part.data))})
		}
	}
	return delivered, window, ranges, err
}

func TestWindowStreamOutputQuotesPathAndFollowsConcreteSedReach(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 500; i++ {
		fmt.Fprintf(&b, "line-%04d: abcdefghijklmnopqrstuvwxyz0123456789\n", i)
	}
	data := []byte(b.String())
	outputDir := filepath.FromSlash("/state/output/sess-1")

	sedAdmitted := guidance.StockToolReach(toolreach.ProgramTool).WithPrograms(func(name string) bool {
		return name == "sed"
	})
	sedDenied := guidance.StockToolReach(toolreach.ProgramTool).WithPrograms(func(name string) bool {
		return name != "sed"
	})
	cmdMissingSchema := toolreach.Offered([]string{toolreach.ProgramTool}).WithPrograms(func(name string) bool {
		return name == "sed"
	})
	cmdNarrowedNoCommandArg := toolreach.Offered([]string{toolreach.ProgramTool}).WithPrograms(func(name string) bool {
		return name == "sed"
	}).WithSchemas(map[string]map[string]any{
		toolreach.ProgramTool: {
			"type":       "object",
			"properties": map[string]any{"description": map[string]any{"type": "string"}},
		},
	})
	cmdUnoffered := guidance.StockToolReach("recall")

	for _, outputPath := range []string{"@output/my file's.stdout", "sub dir/it's here.stdout"} {
		sample, err := sampleOutput(bytes.NewReader(data), 12<<10)
		testutil.FailErr(t, "sample output", err)

		pos, win, _, err := sample.window(t.Context(), sedAdmitted, "stdout", "output:program:p1:stdout", outputPath, true)
		testutil.FailErr(t, "window sed admitted", err)
		if !strings.Contains(pos, "full output: "+outputPath) {
			t.Fatalf("sed-admitted output lost raw saved path %q:\n%s", outputPath, pos)
		}
		prefix := fmt.Sprintf("(sed -n '%d,%dp' ", win.HeadEndLine+1, win.HeadEndLine+150)
		idx := strings.Index(pos, prefix)
		if idx < 0 {
			t.Fatalf("sed-admitted output missing %q:\n%s", prefix, pos)
		}
		endIdx := strings.Index(pos[idx:], ") ...")
		if endIdx < 0 {
			t.Fatalf("sed-admitted output missing closing paren:\n%s", pos[idx:])
		}
		emittedCmd := pos[idx+1 : idx+endIdx]
		plan, err := commandsurface.ParsePlan(map[string]any{"command": emittedCmd})
		testutil.FailErr(t, "ParsePlan "+emittedCmd, err)
		stages, _, err := commandsurface.ResolveSessionOperands(plan.Stages, "", outputDir)
		testutil.FailErr(t, "ResolveSessionOperands "+emittedCmd, err)
		if len(stages) != 1 || stages[0].Name != "sed" || len(stages[0].Args) != 3 || stages[0].Args[0] != "-n" {
			t.Fatalf("emitted command %q parsed into unexpected argv %+v", emittedCmd, stages)
		}
		wantArg := outputPath
		if strings.HasPrefix(outputPath, "@output/") {
			wantArg = filepath.Join(outputDir, strings.TrimPrefix(outputPath, "@output/"))
		}
		if stages[0].Args[2] != wantArg {
			t.Fatalf("emitted command %q resolved path operand to %q, want %q", emittedCmd, stages[0].Args[2], wantArg)
		}

		for label, reach := range map[string]toolreach.Request{
			"sed denied":                  sedDenied,
			"command missing schema":      cmdMissingSchema,
			"command narrowed no command": cmdNarrowedNoCommandArg,
			"command unoffered":           cmdUnoffered,
		} {
			neg, _, _, err := sample.window(t.Context(), reach, "stdout", "output:program:p1:stdout", outputPath, true)
			testutil.FailErr(t, "window "+label, err)
			if strings.Contains(neg, "sed -n") {
				t.Fatalf("%s leaked sed command:\n%s", label, neg)
			}
			if !strings.Contains(neg, "full output: "+outputPath) {
				t.Fatalf("%s lost raw saved path %q:\n%s", label, outputPath, neg)
			}
		}
	}
}
