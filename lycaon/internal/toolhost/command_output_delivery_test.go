package toolhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
)

func newOutputDeliveryFixture(t *testing.T) *programtest.Fixture {
	t.Helper()
	f := programtest.New(t, programtest.Options{})
	for _, name := range []string{"delete", "move", "copy", "mkdir", "chmod", "chown", "times", "extract_archive", "grep", "list_dir", "stat"} {
		_ = f.Runtime.Registry.Register(name, func(context.Context, map[string]any, tools.ToolContext) (string, error) {
			return "", errors.New("structured tool is not exercised")
		})
	}
	owners, err := (programOwnerTools{
		boundary: f.Runtime.Boundary,
		operations: mutation.Operations{
			Write:   &native.WriteTool{Boundary: f.Runtime.Boundary},
			Delete:  &native.DeleteTool{Boundary: f.Runtime.Boundary},
			Move:    &native.MoveTool{Boundary: f.Runtime.Boundary},
			Copy:    &native.CopyTool{Boundary: f.Runtime.Boundary},
			Mkdir:   &native.MkdirTool{Boundary: f.Runtime.Boundary},
			Chmod:   &native.ChmodTool{Boundary: f.Runtime.Boundary},
			Chown:   &native.ChownTool{Boundary: f.Runtime.Boundary},
			Times:   &native.TimesTool{Boundary: f.Runtime.Boundary},
			Extract: &native.ExtractArchiveTool{Boundary: f.Runtime.Boundary},
		},
		registry: f.Runtime.Registry,
	}).compose(f.Recorder)
	testutil.FailErr(t, "compose program owners", err)
	f.Runtime.Owners = owners
	return f
}

func decodeProgramOutcome(t *testing.T, raw string) command.ProgramOutcome {
	t.Helper()
	var out command.ProgramOutcome
	testutil.FailErr(t, "decode program outcome", json.Unmarshal([]byte(raw), &out))
	return out
}

func TestOutputDeliveryHeadTailNoticeAndSedNextLines(t *testing.T) {
	f := newOutputDeliveryFixture(t)
	f.Configure = func(tc *tools.ToolContext) {
		tc.Host.ModelContextWindow = 16000 // 12 KiB budget: 6 KiB head + 6 KiB tail
	}

	var fileBuilder strings.Builder
	const totalLines = 500
	for i := 1; i <= totalLines; i++ {
		fmt.Fprintf(&fileBuilder, "line-%04d: the quick brown fox jumps over the lazy dog\n", i)
	}
	testutil.FailErr(t, "write big.txt", os.WriteFile(filepath.Join(f.Dir, "big.txt"), []byte(fileBuilder.String()), 0o644))

	raw, err := f.Call(t, t.Context(), "cat big.txt")
	testutil.FailErr(t, "call cat big.txt", err)
	out := decodeProgramOutcome(t, raw)

	if !out.Truncated || len(out.Outputs) != 1 || !out.Outputs[0].Truncated {
		t.Fatalf("expected truncated output window, got %+v", out)
	}
	win := out.Outputs[0]
	if win.Stream != "stdout" || win.Ref != out.StdoutRef || win.ScratchPath != "@scratch/output/"+out.ProgramID+".stdout" {
		t.Fatalf("unexpected window metadata: %+v", win)
	}
	if !strings.HasPrefix(out.Stdout, "line-0001: ") || !strings.HasSuffix(out.Stdout, "line-0500: the quick brown fox jumps over the lazy dog\n") {
		t.Fatalf("stdout missing head or tail line boundaries")
	}

	lines := strings.Split(strings.TrimSuffix(out.Stdout, "\n"), "\n")
	noticeIdx := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "... ") && strings.HasSuffix(l, " ...") {
			noticeIdx = i
			break
		}
	}
	if noticeIdx <= 0 || noticeIdx >= len(lines)-1 {
		t.Fatalf("notice line not found in middle of output: noticeIdx=%d len=%d", noticeIdx, len(lines))
	}
	headLastLine := lines[noticeIdx-1]
	wantHeadLastPrefix := fmt.Sprintf("line-%04d: ", win.HeadEndLine)
	if !strings.HasPrefix(headLastLine, wantHeadLastPrefix) {
		t.Fatalf("last head line = %q, want prefix %q", headLastLine, wantHeadLastPrefix)
	}
	tailFirstLine := lines[noticeIdx+1]
	wantTailFirstPrefix := fmt.Sprintf("line-%04d: ", win.TailStartLine)
	if !strings.HasPrefix(tailFirstLine, wantTailFirstPrefix) {
		t.Fatalf("first tail line = %q, want prefix %q", tailFirstLine, wantTailFirstPrefix)
	}

	noticeLine := lines[noticeIdx]
	re := regexp.MustCompile(`\(sed -n '(\d+),(\d+)p' (@scratch/output/[^)]+)\)`)
	m := re.FindStringSubmatch(noticeLine)
	if m == nil {
		t.Fatalf("notice line did not match expected sed pattern: %q", noticeLine)
	}
	sedCmd := fmt.Sprintf("sed -n '%s,%sp' %s", m[1], m[2], m[3])
	wantNextLine := fmt.Sprintf("line-%04d: the quick brown fox jumps over the lazy dog\n", win.HeadEndLine+1)

	sedRaw, err := f.Call(t, t.Context(), sedCmd)
	testutil.FailErr(t, "call sed on scratch output", err)
	sedOut := decodeProgramOutcome(t, sedRaw)
	if !strings.HasPrefix(sedOut.Stdout, wantNextLine) {
		t.Fatalf("sed output did not start with exact next line %q; got %q", wantNextLine, sedOut.Stdout[:min(len(sedOut.Stdout), 100)])
	}

	grepRaw, err := f.Call(t, t.Context(), fmt.Sprintf("grep 'line-%04d:' %s", win.HeadEndLine+2, win.ScratchPath))
	testutil.FailErr(t, "call grep on scratch output", err)
	grepOut := decodeProgramOutcome(t, grepRaw)
	if !strings.HasPrefix(grepOut.Stdout, fmt.Sprintf("line-%04d:", win.HeadEndLine+2)) {
		t.Fatalf("grep output = %q", grepOut.Stdout)
	}

	tailRaw, err := f.Call(t, t.Context(), "tail -n 2 "+win.ScratchPath)
	testutil.FailErr(t, "call tail on scratch output", err)
	tailOut := decodeProgramOutcome(t, tailRaw)
	if !strings.Contains(tailOut.Stdout, "line-0499:") || !strings.Contains(tailOut.Stdout, "line-0500:") {
		t.Fatalf("tail output = %q", tailOut.Stdout)
	}
}

func TestScratchOutputIsReadOnlyAndRefusesMutation(t *testing.T) {
	f := newOutputDeliveryFixture(t)
	raw, err := f.Call(t, t.Context(), "printf 'hello world\n'")
	testutil.FailErr(t, "call printf", err)
	out := decodeProgramOutcome(t, raw)

	diskPath := filepath.Join(f.ScratchDir, "output", out.ProgramID+".stdout")
	info, err := os.Stat(diskPath)
	testutil.FailErr(t, "stat scratch output", err)
	if info.Mode().Perm() != 0o444 {
		t.Fatalf("scratch output mode = %o, want 0444", info.Mode().Perm())
	}

	scratchRef := "@scratch/output/" + out.ProgramID + ".stdout"
	redirectRaw, redirectErr := f.Call(t, t.Context(), "echo overwrite > "+scratchRef)
	if redirectErr == nil {
		redirectOut := decodeProgramOutcome(t, redirectRaw)
		if redirectOut.Status == 0 && redirectOut.Refusal == nil {
			t.Fatalf("expected > on %s to be refused, got %+v", scratchRef, redirectOut)
		}
	}

	rmRaw, rmErr := f.Call(t, t.Context(), "rm "+scratchRef)
	if rmErr == nil {
		rmOut := decodeProgramOutcome(t, rmRaw)
		if rmOut.Status == 0 && rmOut.Refusal == nil {
			t.Fatalf("expected rm on %s to be refused, got %+v", scratchRef, rmOut)
		}
	}

	after, err := os.ReadFile(diskPath)
	testutil.FailErr(t, "read scratch output after refused mutations", err)
	if string(after) != "hello world\n" {
		t.Fatalf("scratch output mutated: %q", string(after))
	}
}

func TestContextProfileBudgetsAndActiveStreamSplitting(t *testing.T) {
	f := newOutputDeliveryFixture(t)
	var b strings.Builder
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&b, "row-%04d: 0123456789abcdef0123456789abcdef\n", i)
	}
	testutil.FailErr(t, "write medium.txt", os.WriteFile(filepath.Join(f.Dir, "medium.txt"), []byte(b.String()), 0o644))

	f.Configure = func(tc *tools.ToolContext) {
		tc.Host.ModelContextWindow = 16384 // 12 KiB total budget
	}

	singleRaw, err := f.Call(t, t.Context(), "cat medium.txt")
	testutil.FailErr(t, "single active stream call", err)
	singleOut := decodeProgramOutcome(t, singleRaw)
	if singleOut.Truncated {
		t.Fatalf("8.6 KiB stdout alone should fit in 12 KiB budget without truncation")
	}

	bothRaw, err := f.Call(t, t.Context(), "cat medium.txt; cat medium.txt >&2")
	testutil.FailErr(t, "both active streams call", err)
	bothOut := decodeProgramOutcome(t, bothRaw)
	if !bothOut.Truncated || len(bothOut.Outputs) != 2 || !bothOut.Outputs[0].Truncated || !bothOut.Outputs[1].Truncated {
		t.Fatalf("expected both stdout and stderr to be truncated when splitting 12 KiB budget (6 KiB each), got %+v", bothOut)
	}
}

func TestPipeStagesProduceZeroRetentionRowsAndFailedChildRetainsStderr(t *testing.T) {
	f := newOutputDeliveryFixture(t)
	var b strings.Builder
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&b, "pipe-line-%04d\n", i)
	}
	testutil.FailErr(t, "write pipe.txt", os.WriteFile(filepath.Join(f.Dir, "pipe.txt"), []byte(b.String()), 0o644))

	raw, err := f.Call(t, t.Context(), "cat pipe.txt | grep pipe-line | tail -n 5")
	testutil.FailErr(t, "pipeline call", err)
	out := decodeProgramOutcome(t, raw)
	if !strings.Contains(out.Stdout, "pipe-line-0300") {
		t.Fatalf("pipeline stdout = %q", out.Stdout)
	}

	var childOutputRows int
	testutil.FailErr(t, "count child output rows for pipeline",
		f.Database.QueryRowContext(t.Context(), `SELECT count(*) FROM invocation_program_outputs WHERE program_id = ? AND execution_id IS NOT NULL AND stream IN ('stdout', 'stderr')`, out.ProgramID).Scan(&childOutputRows))
	if childOutputRows != 0 {
		t.Fatalf("expected 0 child output retention rows for pipeline, got %d", childOutputRows)
	}

	failRaw, err := f.Call(t, t.Context(), "cat nonexistent-file.txt")
	testutil.FailErr(t, "failed child call", err)
	failOut := decodeProgramOutcome(t, failRaw)
	if failOut.Status == 0 {
		t.Fatalf("expected non-zero status for missing file")
	}
	var failedStderrRows int
	testutil.FailErr(t, "count failed child stderr rows",
		f.Database.QueryRowContext(t.Context(), `SELECT count(*) FROM invocation_program_outputs WHERE program_id = ? AND execution_id IS NOT NULL AND stream = 'stderr'`, failOut.ProgramID).Scan(&failedStderrRows))
	if failedStderrRows != 1 {
		t.Fatalf("expected 1 retained stderr row for failed child, got %d", failedStderrRows)
	}
}

func TestSourceMappingsConfirmHeadAndTailAndMapScratchReadsBackToSource(t *testing.T) {
	f := newOutputDeliveryFixture(t)
	f.Configure = func(tc *tools.ToolContext) {
		tc.Host.ModelContextWindow = 16000 // 12 KiB budget
	}

	var b strings.Builder
	for i := 1; i <= 400; i++ {
		fmt.Fprintf(&b, "source-line-%04d: exact workspace content for evidence verification\n", i)
	}
	testutil.FailErr(t, "write tracked.txt", os.WriteFile(filepath.Join(f.Dir, "tracked.txt"), []byte(b.String()), 0o644))

	raw, err := f.Call(t, t.Context(), "cat tracked.txt")
	testutil.FailErr(t, "cat tracked.txt", err)
	out := decodeProgramOutcome(t, raw)
	if !out.Truncated || len(out.Outputs) != 1 {
		t.Fatalf("expected truncated output, got %+v", out)
	}
	win := out.Outputs[0]

	delivered := f.Delivered()
	if len(delivered) != 2 {
		t.Fatalf("expected 2 delivered source evidence records (head and tail), got %d", len(delivered))
	}
	headRec, tailRec := delivered[0], delivered[1]
	if headRec.Path != "tracked.txt" || headRec.SourceDelivery.StartLine != 1 || headRec.SourceDelivery.EndLine != win.HeadEndLine {
		t.Fatalf("unexpected head source delivery: %+v", headRec.SourceDelivery)
	}
	if tailRec.Path != "tracked.txt" || tailRec.SourceDelivery.StartLine != win.TailStartLine || tailRec.SourceDelivery.EndLine != 400 {
		t.Fatalf("unexpected tail source delivery: %+v", tailRec.SourceDelivery)
	}

	midStart := win.HeadEndLine + 1
	midEnd := midStart + 24
	sedCmd := fmt.Sprintf("sed -n '%d,%dp' %s", midStart, midEnd, win.ScratchPath)
	_, err = f.Call(t, t.Context(), sedCmd)
	testutil.FailErr(t, "sed middle lines from scratch output", err)

	deliveredAfter := f.Delivered()
	if len(deliveredAfter) != 3 {
		t.Fatalf("expected 3rd delivered source evidence record after reading @scratch/output with sed -n, got %d", len(deliveredAfter))
	}
	midRec := deliveredAfter[2]
	if midRec.Path != "tracked.txt" || midRec.SourceDelivery.StartLine != midStart || midRec.SourceDelivery.EndLine != midEnd {
		t.Fatalf("expected middle read of @scratch/output to map back to tracked.txt lines %d..%d, got path=%q delivery=%+v",
			midStart, midEnd, midRec.Path, midRec.SourceDelivery)
	}
}

func TestScreenedSecretsAreRedactedOnceAndMatchScratchOutput(t *testing.T) {
	f := newOutputDeliveryFixture(t)
	secret := "sk-live-super-secret-value-1234567890"
	var harvestCalls atomic.Int32
	m := secretmatch.NewInertMatcher()
	m.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		harvestCalls.Add(1)
		return []secretmatch.HarvestedValue{{
			Name:           "token",
			Secret:         secret,
			RuleID:         secretmatch.ManagedRuleID,
			Title:          secretmatch.ManagedRuleTitle,
			Source:         secretmatch.SourceRememberedMatch,
			NonDisclosable: true,
		}}
	})
	f.Runtime.Projector = captureprojection.New(m, nil)

	testutil.FailErr(t, "write empty.txt", os.WriteFile(filepath.Join(f.Dir, "empty.txt"), nil, 0o644))
	testutil.FailErr(t, "write secret.txt", os.WriteFile(filepath.Join(f.Dir, "secret.txt"), []byte("before "+secret+" after\n"), 0o644))

	beforeEmpty := harvestCalls.Load()
	_, err := f.Call(t, t.Context(), "cat empty.txt")
	testutil.FailErr(t, "cat empty.txt", err)
	emptyCalls := harvestCalls.Load() - beforeEmpty

	beforeSecret := harvestCalls.Load()
	raw, err := f.Call(t, t.Context(), "cat secret.txt")
	testutil.FailErr(t, "cat secret.txt", err)
	secretCalls := harvestCalls.Load() - beforeSecret
	out := decodeProgramOutcome(t, raw)

	if strings.Contains(out.Stdout, secret) {
		t.Fatalf("secret leaked in stdout: %q", out.Stdout)
	}
	scratchBytes, err := os.ReadFile(filepath.Join(f.ScratchDir, "output", out.ProgramID+".stdout"))
	testutil.FailErr(t, "read scratch copy", err)
	if string(scratchBytes) != out.Stdout {
		t.Fatalf("scratch output %q != delivered stdout %q", string(scratchBytes), out.Stdout)
	}
	if diff := secretCalls - emptyCalls; diff != 1 {
		t.Fatalf("expected non-empty stdout to add exactly 1 ScreenStream call over empty stdout, got %d (empty=%d, secret=%d)", diff, emptyCalls, secretCalls)
	}
}
