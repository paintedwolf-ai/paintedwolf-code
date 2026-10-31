package toolhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
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
	f.Context = func(_ string, tc *tools.ToolContext) {
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
	if win.Stream != "stdout" || win.Ref != out.StdoutRef || win.OutputPath != "@output/"+out.ProgramID+".stdout" {
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
	re := regexp.MustCompile(`\(sed -n '(\d+),(\d+)p' (@output/[^)]+)\)`)
	m := re.FindStringSubmatch(noticeLine)
	if m == nil {
		t.Fatalf("notice line did not match expected sed pattern: %q", noticeLine)
	}
	sedCmd := fmt.Sprintf("sed -n '%s,%sp' %s", m[1], m[2], m[3])
	wantNextLine := fmt.Sprintf("line-%04d: the quick brown fox jumps over the lazy dog\n", win.HeadEndLine+1)

	sedRaw, err := f.Call(t, t.Context(), sedCmd)
	testutil.FailErr(t, "call sed on output copy", err)
	sedOut := decodeProgramOutcome(t, sedRaw)
	if !strings.HasPrefix(sedOut.Stdout, wantNextLine) {
		t.Fatalf("sed output did not start with exact next line %q; got %q", wantNextLine, sedOut.Stdout[:min(len(sedOut.Stdout), 100)])
	}

	grepRaw, err := f.Call(t, t.Context(), fmt.Sprintf("grep 'line-%04d:' %s", win.HeadEndLine+2, win.OutputPath))
	testutil.FailErr(t, "call grep on output copy", err)
	grepOut := decodeProgramOutcome(t, grepRaw)
	if !strings.HasPrefix(grepOut.Stdout, fmt.Sprintf("line-%04d:", win.HeadEndLine+2)) {
		t.Fatalf("grep output = %q", grepOut.Stdout)
	}

	tailRaw, err := f.Call(t, t.Context(), "tail -n 2 "+win.OutputPath)
	testutil.FailErr(t, "call tail on output copy", err)
	tailOut := decodeProgramOutcome(t, tailRaw)
	if !strings.Contains(tailOut.Stdout, "line-0499:") || !strings.Contains(tailOut.Stdout, "line-0500:") {
		t.Fatalf("tail output = %q", tailOut.Stdout)
	}
}

func TestOutputCopyIsReadOnlyAndRefusesMutation(t *testing.T) {
	f := newOutputDeliveryFixture(t)
	raw, err := f.Call(t, t.Context(), "printf 'hello world\n'")
	testutil.FailErr(t, "call printf", err)
	out := decodeProgramOutcome(t, raw)

	diskPath := filepath.Join(f.OutputDir, out.ProgramID+".stdout")
	info, err := os.Stat(diskPath)
	testutil.FailErr(t, "stat output copy", err)
	if info.Mode().Perm() != 0o444 {
		t.Fatalf("output copy mode = %o, want 0444", info.Mode().Perm())
	}

	outputRef := "@output/" + out.ProgramID + ".stdout"
	redirectRaw, redirectErr := f.Call(t, t.Context(), "echo overwrite > "+outputRef)
	if redirectErr == nil {
		redirectOut := decodeProgramOutcome(t, redirectRaw)
		if redirectOut.Status == 0 && redirectOut.Refusal == nil {
			t.Fatalf("expected > on %s to be refused, got %+v", outputRef, redirectOut)
		}
	}

	rmRaw, rmErr := f.Call(t, t.Context(), "rm "+outputRef)
	if rmErr == nil {
		rmOut := decodeProgramOutcome(t, rmRaw)
		if rmOut.Status == 0 && rmOut.Refusal == nil {
			t.Fatalf("expected rm on %s to be refused, got %+v", outputRef, rmOut)
		}
	}

	after, err := os.ReadFile(diskPath)
	testutil.FailErr(t, "read output copy after refused mutations", err)
	if string(after) != "hello world\n" {
		t.Fatalf("output copy mutated: %q", string(after))
	}
}

func TestGoTest200kLinesSummarySurvives(t *testing.T) {
	f := newOutputDeliveryFixture(t)
	f.Context = func(_ string, tc *tools.ToolContext) {
		tc.Host.ModelContextWindow = 200000 // large tier: 64 KiB (32 KiB head + 32 KiB tail)
	}

	var b strings.Builder
	b.Grow(6 << 20)
	const cases = 99999 // 99,999 * 2 = 199,998 lines + 2 summary lines = 200,000 lines
	for i := 1; i <= cases; i++ {
		fmt.Fprintf(&b, "=== RUN   TestCase%06d\n--- PASS: TestCase%06d (0.00s)\n", i, i)
	}
	b.WriteString("PASS\nok  \tgithub.com/lycaon/lycaon/internal/foo\t1.234s\n")
	testutil.FailErr(t, "write gotest.log", os.WriteFile(filepath.Join(f.Dir, "gotest.log"), []byte(b.String()), 0o644))

	owners := f.Runtime.Owners
	releaseCapturePressure := func() {}
	t.Cleanup(func() { releaseCapturePressure() })
	f.Runtime.Owners = func(tc tools.ToolContext, paths *projectpaths.ProgramPaths, memory *toolcontract.ProgramMemory, spill tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		// Keep streaming and retention available while the optional whole-source
		// capture competes with other admitted work in this program.
		var err error
		releaseCapturePressure, err = memory.Reserve(t.Context(), 24<<20)
		if err != nil {
			return command.ProgramOwners{}, err
		}
		return owners(tc, paths, memory, spill)
	}
	raw, err := f.Call(t, t.Context(), "cat gotest.log")
	f.Runtime.Owners = owners
	releaseCapturePressure()
	testutil.FailErr(t, "call cat gotest.log", err)
	out := decodeProgramOutcome(t, raw)

	if !out.Truncated || len(out.Outputs) != 1 || !out.Outputs[0].Truncated {
		t.Fatalf("expected truncated 200k-line output, got %+v", out)
	}
	win := out.Outputs[0]
	if win.TotalLines != 200000 {
		t.Fatalf("TotalLines = %d, want 200000", win.TotalLines)
	}
	if !strings.HasPrefix(out.Stdout, "=== RUN   TestCase000001\n") {
		t.Fatalf("200k-line output missing first line")
	}
	wantSuffix := "PASS\nok  \tgithub.com/lycaon/lycaon/internal/foo\t1.234s\n"
	if !strings.HasSuffix(out.Stdout, wantSuffix) {
		t.Fatalf("200k-line output lost trailing go test summary; tail=%q", out.Stdout[max(0, len(out.Stdout)-120):])
	}

	if len(f.Delivered()) != 0 {
		t.Fatal("oversized optional source capture acquired read credit")
	}

	re := regexp.MustCompile(`\(sed -n '(\d+),(\d+)p' (@output/[^)]+)\)`)
	m := re.FindStringSubmatch(out.Stdout)
	if m == nil {
		t.Fatalf("omitted notice missing sed command in 200k-line output")
	}
	sedCmd := fmt.Sprintf("sed -n '%s,%sp' %s", m[1], m[2], m[3])
	sedRaw, err := f.Call(t, t.Context(), sedCmd)
	testutil.FailErr(t, "call sed on 200k-line output copy", err)
	sedOut := decodeProgramOutcome(t, sedRaw)

	nextLineNum := win.HeadEndLine + 1
	caseNum := (nextLineNum + 1) / 2
	var wantFirstLine string
	if nextLineNum%2 == 1 {
		wantFirstLine = fmt.Sprintf("=== RUN   TestCase%06d\n", caseNum)
	} else {
		wantFirstLine = fmt.Sprintf("--- PASS: TestCase%06d (0.00s)\n", caseNum)
	}
	if !strings.HasPrefix(sedOut.Stdout, wantFirstLine) {
		t.Fatalf("sed output did not start with expected line %d (%q); got %q", nextLineNum, wantFirstLine, sedOut.Stdout[:min(len(sedOut.Stdout), 100)])
	}
}

func TestContextProfileBudgetsAndActiveStreamSplitting(t *testing.T) {
	f := newOutputDeliveryFixture(t)
	writeSizedFile := func(name string, lines int) {
		var b strings.Builder
		for i := 1; i <= lines; i++ {
			fmt.Fprintf(&b, "row-%04d: 0123456789abcdef0123456789abcdef\n", i) // 43 bytes/line
		}
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(f.Dir, name), []byte(b.String()), 0o644))
	}
	writeSizedFile("medium.txt", 200)   // ~8.6 KiB: fits in 12 KiB (small), exceeds 6 KiB split
	writeSizedFile("tier20k.txt", 500)  // ~21.5 KiB: exceeds 12 KiB (small), fits in 32 KiB (medium)
	writeSizedFile("tier45k.txt", 1050) // ~45.1 KiB: exceeds 32 KiB (medium), fits in 64 KiB (large)
	writeSizedFile("tier80k.txt", 1900) // ~81.7 KiB: exceeds 64 KiB (large)

	// Small context tier (16384 tokens -> 12 KiB budget)
	f.Context = func(_ string, tc *tools.ToolContext) {
		tc.Host.ModelContextWindow = 16384
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

	small20kRaw, err := f.Call(t, t.Context(), "cat tier20k.txt")
	testutil.FailErr(t, "small tier 20k call", err)
	if !decodeProgramOutcome(t, small20kRaw).Truncated {
		t.Fatalf("expected 21.5 KiB output to truncate under small (12 KiB) budget")
	}

	// Medium context tier (65536 tokens -> 32 KiB budget)
	f.Context = func(_ string, tc *tools.ToolContext) {
		tc.Host.ModelContextWindow = 65536
	}
	med20kRaw, err := f.Call(t, t.Context(), "cat tier20k.txt")
	testutil.FailErr(t, "medium tier 20k call", err)
	if decodeProgramOutcome(t, med20kRaw).Truncated {
		t.Fatalf("expected 21.5 KiB output to fit without truncation under medium (32 KiB) budget")
	}
	med45kRaw, err := f.Call(t, t.Context(), "cat tier45k.txt")
	testutil.FailErr(t, "medium tier 45k call", err)
	if !decodeProgramOutcome(t, med45kRaw).Truncated {
		t.Fatalf("expected 45.1 KiB output to truncate under medium (32 KiB) budget")
	}

	// Large context tier (200000 tokens -> 64 KiB budget)
	f.Context = func(_ string, tc *tools.ToolContext) {
		tc.Host.ModelContextWindow = 200000
	}
	large45kRaw, err := f.Call(t, t.Context(), "cat tier45k.txt")
	testutil.FailErr(t, "large tier 45k call", err)
	if decodeProgramOutcome(t, large45kRaw).Truncated {
		t.Fatalf("expected 45.1 KiB output to fit without truncation under large (64 KiB) budget")
	}
	large80kRaw, err := f.Call(t, t.Context(), "cat tier80k.txt")
	testutil.FailErr(t, "large tier 80k call", err)
	if !decodeProgramOutcome(t, large80kRaw).Truncated {
		t.Fatalf("expected 81.7 KiB output to truncate under large (64 KiB) budget")
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
	f.Context = func(_ string, tc *tools.ToolContext) {
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
	sedCmd := fmt.Sprintf("sed -n '%d,%dp' %s", midStart, midEnd, win.OutputPath)
	_, err = f.Call(t, t.Context(), sedCmd)
	testutil.FailErr(t, "sed middle lines from output copy", err)

	deliveredAfter := f.Delivered()
	if len(deliveredAfter) != 3 {
		t.Fatalf("expected 3rd delivered source evidence record after reading @output with sed -n, got %d", len(deliveredAfter))
	}
	midRec := deliveredAfter[2]
	if midRec.Path != "tracked.txt" || midRec.SourceDelivery.StartLine != midStart || midRec.SourceDelivery.EndLine != midEnd {
		t.Fatalf("expected middle read of @output to map back to tracked.txt lines %d..%d, got path=%q delivery=%+v",
			midStart, midEnd, midRec.Path, midRec.SourceDelivery)
	}
}

func TestScreenedSecretsAreRedactedOnceAndMatchOutputCopy(t *testing.T) {
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
	copyBytes, err := os.ReadFile(filepath.Join(f.OutputDir, out.ProgramID+".stdout"))
	testutil.FailErr(t, "read scratch copy", err)
	if string(copyBytes) != out.Stdout {
		t.Fatalf("output copy %q != delivered stdout %q", string(copyBytes), out.Stdout)
	}
	if diff := secretCalls - emptyCalls; diff != 1 {
		t.Fatalf("expected non-empty stdout to add exactly 1 ScreenStream call over empty stdout, got %d (empty=%d, secret=%d)", diff, emptyCalls, secretCalls)
	}
}

func TestDeliveryWindowSurvivesRecorderReopenAndRejectsIncompleteDescriptor(t *testing.T) {
	f := newOutputDeliveryFixture(t)
	immediate := &tools.ToolInvocationOut{}
	f.Context = func(_ string, tc *tools.ToolContext) { tc.Host.ModelContextWindow = 16000; tc.Effects.Out = immediate }
	body := strings.Repeat("line for retained output\n", 2000)
	testutil.FailErr(t, "write window fixture", os.WriteFile(filepath.Join(f.Dir, "window.txt"), []byte(body), 0600))
	raw, err := f.Call(t, t.Context(), "cat window.txt")
	testutil.FailErr(t, "produce window", err)
	outcome := decodeProgramOutcome(t, raw)
	if immediate.CommandProgram == nil || !reflect.DeepEqual(immediate.CommandProgram.Outputs, outcome.Outputs) {
		t.Fatalf("immediate receipt=%+v output=%+v", immediate.CommandProgram, outcome.Outputs)
	}
	reopened := invocation.NewSQLRecorder(f.Database)
	page, err := reopened.ReadProgramPage(t.Context(), f.Session, outcome.ProgramID, "", 10)
	testutil.FailErr(t, "read reopened receipt", err)
	if !reflect.DeepEqual(page.Program.Outputs, outcome.Outputs) || len(page.Program.Outputs) != 1 || page.Program.Outputs[0].InlineBudgetBytes != 12<<10 {
		t.Fatalf("durable windows=%+v model windows=%+v", page.Program.Outputs, outcome.Outputs)
	}
	_, err = f.Database.ExecContext(t.Context(), `UPDATE invocation_program_outputs SET preview_truncated=1 WHERE program_id=? AND reference=?`, outcome.ProgramID, "output:program:"+outcome.ProgramID+":window:stdout")
	testutil.FailErr(t, "damage descriptor preview", err)
	if _, err := reopened.ReadProgramPage(t.Context(), f.Session, outcome.ProgramID, "", 10); err == nil {
		t.Fatal("incomplete delivery descriptor became receipt window")
	}
}

func TestChangedScratchCopyCannotReuseOriginalMappings(t *testing.T) {
	f := newOutputDeliveryFixture(t)
	testutil.FailErr(t, "seed mapped source", os.WriteFile(filepath.Join(f.Dir, "source.txt"), []byte("original source\n"), 0600))
	raw, err := f.Call(t, t.Context(), "cat source.txt")
	testutil.FailErr(t, "produce mapped copy", err)
	out := decodeProgramOutcome(t, raw)
	before := len(f.Delivered())
	copyPath := filepath.Join(f.OutputDir, out.ProgramID+".stdout")
	testutil.FailErr(t, "simulate external permission change", os.Chmod(copyPath, 0600))
	testutil.FailErr(t, "simulate equal-size external replacement", os.WriteFile(copyPath, []byte("modified source\n"), 0600))
	_, err = f.Call(t, t.Context(), "cat "+out.Outputs[0].OutputPath)
	testutil.FailErr(t, "read changed scratch copy", err)
	if after := len(f.Delivered()); after != before {
		t.Fatalf("changed copy reused source mappings: before=%d after=%d", before, after)
	}
}

func TestScreeningExpansionKeepsFullCopyBeyondRawByteCount(t *testing.T) {
	f := newOutputDeliveryFixture(t)
	f.Context = func(_ string, tc *tools.ToolContext) { tc.Host.ModelContextWindow = 16000 }
	secret := "tiny-key"
	matcher := secretmatch.NewInertMatcher()
	matcher.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{{Name: "token", Secret: secret, RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle, Source: secretmatch.SourceRememberedMatch, NonDisclosable: true}}
	})
	f.Runtime.Projector = captureprojection.New(matcher, nil)
	padding := " " + strings.Repeat("x", 4096) + "\n"
	// One match per log record keeps detector-window evidence within its memory budget.
	body := strings.Repeat(secret+padding, 400)
	want := []byte(strings.Repeat(secretmatch.RedactedMarker+padding, 400))
	testutil.FailErr(t, "seed expanding output", os.WriteFile(filepath.Join(f.Dir, "expansion.txt"), []byte(body), 0600))
	raw, err := f.Call(t, t.Context(), "cat expansion.txt")
	testutil.FailErr(t, "deliver expanding output", err)
	out := decodeProgramOutcome(t, raw)
	if out.Status != 0 || len(out.Outputs) != 1 || !out.Outputs[0].Complete || out.Outputs[0].Limit != nil || out.Outputs[0].TotalBytes != int64(len(want)) {
		t.Fatalf("expanding outcome=%+v", out)
	}
	copyBytes, err := os.ReadFile(filepath.Join(f.OutputDir, out.ProgramID+".stdout"))
	testutil.FailErr(t, "read expanded copy", err)
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	testutil.FailErr(t, "load mapping read limits", err)
	memory, err := toolcontract.NewProgramMemory(limits)
	testutil.FailErr(t, "admit mapping read", err)
	mappings, err := f.Recorder.ReadProgramOutputMappings(t.Context(), f.Session, out.ProgramID, out.StdoutRef, memory)
	testutil.FailErr(t, "lookup expanded body", err)
	defer mappings.Close()
	store := f.Recorder.ProgramBlobStore(t.Context(), f.Runtime.Blobs)
	store.Dir = tooloutput.ToolOutputSpillDir
	reader, err := store.Open(mappings.Blob)
	testutil.FailErr(t, "open expanded retained body", err)
	retained, err := io.ReadAll(reader)
	testutil.FailErr(t, "read expanded retained body", err)
	testutil.FailErr(t, "close expanded body", reader.Close())
	if !bytes.Equal(copyBytes, want) || !bytes.Equal(retained, want) || len(f.Delivered()) != 0 {
		t.Fatal("expanded bytes changed, shortened, or acquired source credit")
	}
}

func TestScreeningLimitKeepsExecutionOutcomeAndIncompleteReceipt(t *testing.T) {
	f := newOutputDeliveryFixture(t)
	matcher := secretmatch.NewInertMatcher()
	matcher.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{{Name: "token", Secret: "tiny-key", RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle, Source: secretmatch.SourceRememberedMatch, NonDisclosable: true}}
	})
	f.Runtime.Projector = captureprojection.New(matcher, nil)
	testutil.FailErr(t, "seed dense output", os.WriteFile(filepath.Join(f.Dir, "dense.txt"), []byte(strings.Repeat("tiny-key\n", 160000)), 0600))
	raw, err := f.Call(t, t.Context(), "cat dense.txt")
	testutil.FailErr(t, "deliver partial screened output", err)
	out := decodeProgramOutcome(t, raw)
	if out.Status != 0 || out.StopCode != "" || out.State != "completed" || len(out.Outputs) != 1 {
		t.Fatalf("delivery changed execution: %+v", out)
	}
	window := out.Outputs[0]
	if window.Complete || window.Limit == nil || window.Limit.Code != "COMMAND_LIMIT_EXCEEDED" || window.Limit.LimitID != "screen_memory_bytes" || window.Limit.Maximum != 32<<20 || window.Limit.Observed <= window.Limit.Maximum {
		t.Fatalf("partial delivery=%+v", window)
	}
	copyBytes, err := os.ReadFile(filepath.Join(f.OutputDir, out.ProgramID+".stdout"))
	testutil.FailErr(t, "read saved prefix", err)
	if int64(len(copyBytes)) != window.TotalBytes || len(f.Delivered()) != 0 || bytes.Contains(copyBytes, []byte("tiny-key")) {
		t.Fatal("partial copy totals or source credit are false")
	}
	page, err := invocation.NewSQLRecorder(f.Database).ReadProgramPage(t.Context(), f.Session, out.ProgramID, "", 10)
	testutil.FailErr(t, "read partial receipt", err)
	if page.Program.State != "completed" || page.Program.StopCode != "" || !reflect.DeepEqual(page.Program.Outputs, out.Outputs) {
		t.Fatalf("partial receipt=%+v", page.Program)
	}
}
