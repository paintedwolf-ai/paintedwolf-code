package mutation

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
)

func stage(t *testing.T, h *harness, operator, target, content string) (commandprogram.StagedOutput, commandprogram.OutputDestination) {
	t.Helper()
	destination := h.destination(operator, target)
	staged, err := h.publication().Open(t.Context(), destination)
	testutil.FailErr(t, "open "+operator+" "+target, err)
	_, err = io.WriteString(staged, content)
	testutil.FailErr(t, "stage bytes", err)
	return staged, destination
}

func TestRedirectStagesThenPublishesExactBytes(t *testing.T) {
	cases := []struct {
		name, operator, seed, content, want string
		exists                              bool
	}{
		{"truncate replaces only at publish", ">", "old contents\n", "line one\n\tline two\n", "line one\n\tline two\n", true},
		{"append stages captured plus new", ">>", "first\n", "second\n", "first\nsecond\n", true},
		{"append to absent creates", ">>", "", "only\n", "only\n", false},
		{"create absent", ">", "", "fresh\n", "fresh\n", false},
		{"empty result publishes empty bytes", ">", "prepopulated\n", "", "", true},
		{"both streams", "&>", "", "out and err\n", "out and err\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := map[string]string{}
			if c.exists {
				files["out.txt"] = c.seed
			}
			h := newHarness(t, files)
			staged, destination := stage(t, h, c.operator, "out.txt", c.content)
			if c.exists && h.read("out.txt") != c.seed || !c.exists && !h.absent("out.txt") {
				t.Fatal("destination changed before publication")
			}
			published, err := staged.Publish(t.Context())
			testutil.FailErr(t, "publish", err)
			if got := h.read("out.txt"); got != c.want {
				t.Fatalf("published %q, want %q", got, c.want)
			}
			if published.PublishedRevision == "" {
				t.Fatal("publication reported no published revision")
			}
			pins := h.pins.pins[destination.Invocation.Identity.ExecutionID]
			wantMode := toolcontract.PublishedDiskRevision
			if !c.exists {
				wantMode = toolcontract.AbsentRevision
			}
			if len(pins) != 1 || pins[0].Role != toolcontract.DestinationRevision || pins[0].Mode != wantMode || pins[0].Path != "out.txt" {
				t.Fatalf("destination pin %+v", pins)
			}
			if err := h.program.Barrier.AdmitDependentLaunch(t.Context(), h.tc); err != nil {
				t.Fatalf("published disk write still blocks dependent launch: %v", err)
			}
		})
	}
}

func TestRedirectAbandonLeavesDestination(t *testing.T) {
	h := newHarness(t, map[string]string{"out.txt": "keep\n"})
	staged, _ := stage(t, h, ">", "out.txt", "never published\n")
	testutil.FailErr(t, "abandon", staged.Abandon(t.Context()))
	if h.read("out.txt") != "keep\n" {
		t.Fatal("abandoned staging changed the destination")
	}
	if _, err := staged.Publish(t.Context()); err == nil {
		t.Fatal("abandoned staging published")
	}
}

func conflictCode(err error) bool {
	var conflict *native.ExecutionRevisionConflict
	reject := toolrejection.AsToolReject(err)
	return errors.As(err, &conflict) || (reject != nil && (reject.Code == "COMMAND_REVISION_CONFLICT" || reject.Code == "TEXT_WRITE_CONFLICT"))
}

func TestRedirectConflictsWhenDestinationMovesAfterCapture(t *testing.T) {
	h := newHarness(t, map[string]string{"out.txt": "captured\n"})
	staged, _ := stage(t, h, ">>", "out.txt", "program\n")
	testutil.FailErr(t, "concurrent human change", os.WriteFile(h.path("out.txt"), []byte("human\n"), 0o644))
	_, err := staged.Publish(t.Context())
	reject := toolrejection.AsToolReject(err)
	if !conflictCode(err) || reject == nil || reject.Code != "COMMAND_REVISION_CONFLICT" {
		t.Fatalf("moved destination did not conflict: %v", err)
	}
	if h.read("out.txt") != "human\n" {
		t.Fatal("conflict overwrote the concurrent change")
	}
}

// The destination changes while the prepared write waits for review: the
// approval cannot land it over the change, and nothing is recomputed.
func TestRedirectConflictsWhenDestinationChangesDuringReview(t *testing.T) {
	h := newHarness(t, map[string]string{"out.txt": "captured\n"})
	staged, _ := stage(t, h, ">", "out.txt", "program\n")
	reviews := 0
	h.tc.Files.FileChangeReview = func(context.Context, []tools.FileChange) error {
		reviews++
		return os.WriteFile(h.path("out.txt"), []byte("human during review\n"), 0o644)
	}
	staged.(*fileStage).tools = h.tc
	_, err := staged.Publish(t.Context())
	if err == nil || reviews != 1 {
		t.Fatalf("write landed over a change made during review (reviews=%d): %v", reviews, err)
	}
	if h.read("out.txt") != "human during review\n" {
		t.Fatal("approved write replaced the changed destination")
	}
}

func TestInterruptedPublicationLeavesNoPartialFile(t *testing.T) {
	for name, files := range map[string]map[string]string{"absent": {}, "existing": {"out.txt": "old\n"}} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, files)
			staged, _ := stage(t, h, ">", "out.txt", strings.Repeat("payload\n", 4096))
			ctx, cancel := context.WithCancel(t.Context())
			h.tc.Files.FileChangeReview = func(context.Context, []tools.FileChange) error { cancel(); return nil }
			staged.(*fileStage).tools = h.tc
			if _, err := staged.Publish(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("interrupted publication err=%v", err)
			}
			if _, existed := files["out.txt"]; existed && h.read("out.txt") != "old\n" || !existed && !h.absent("out.txt") {
				t.Fatal("interrupted publication left a partial destination")
			}
			entries, err := os.ReadDir(h.root)
			testutil.FailErr(t, "list root", err)
			for _, entry := range entries {
				if entry.Name() != "out.txt" {
					t.Fatalf("interrupted publication left staging debris %q", entry.Name())
				}
			}
		})
	}
}

func TestNullDeviceIsAVirtualSink(t *testing.T) {
	h := newHarness(t, nil)
	staged, destination := stage(t, h, ">", "/dev/null", "discarded\n")
	if _, err := staged.Publish(t.Context()); err != nil {
		testutil.FailErr(t, "publish staged null-device output", err)
	}
	entries, _ := os.ReadDir(h.root)
	if len(entries) != 0 || len(h.pins.pins[destination.Invocation.Identity.ExecutionID]) != 0 {
		t.Fatal("null sink touched or pinned a file")
	}
	input, err := h.publication().OpenInput(t.Context(), commandprogram.InputDestination{Invocation: h.invocation(), Target: commandprogram.Field{Value: "/dev/null"}})
	testutil.FailErr(t, "open null input", err)
	if data, _ := io.ReadAll(input); len(data) != 0 {
		t.Fatal("null input produced bytes")
	}
}

func TestInputRedirectServesAndPinsItsCapture(t *testing.T) {
	h := newHarness(t, map[string]string{"in.txt": "captured input\n"})
	inv := h.invocation()
	input, err := h.publication().OpenInput(t.Context(), commandprogram.InputDestination{Invocation: inv, Target: commandprogram.Field{Value: "in.txt"}})
	testutil.FailErr(t, "open input", err)
	testutil.FailErr(t, "later change", os.WriteFile(h.path("in.txt"), []byte("changed\n"), 0o644))
	data, err := io.ReadAll(input)
	testutil.FailErr(t, "read input", err)
	pins := h.pins.pins[inv.Identity.ExecutionID]
	if string(data) != "captured input\n" || len(pins) != 1 || pins[0].Role != toolcontract.SourceRevision || pins[0].ContentBytes != int64(len(data)) {
		t.Fatalf("input %q pins %+v", data, pins)
	}
	if _, err := h.publication().OpenInput(t.Context(), commandprogram.InputDestination{Invocation: h.invocation(), Target: commandprogram.Field{Value: "missing.txt"}}); err == nil {
		t.Fatal("missing input opened")
	}
}

func TestRedirectOutsideRootsIsRefusedWithoutEffect(t *testing.T) {
	h := newHarness(t, nil)
	// Not the host temporary area, which carries its own session grants.
	outside := filepath.Join(string(filepath.Separator), "pw-outside-"+h.invocation().Identity.ExecutionID+".txt")
	if _, err := h.publication().Open(t.Context(), h.destination(">", outside)); toolrejection.AsToolReject(err) == nil {
		t.Fatalf("redirect outside attached roots was admitted or missing ToolReject: %v", err)
	}
	if _, err := h.publication().OpenInput(t.Context(), commandprogram.InputDestination{Invocation: h.invocation(), Target: commandprogram.Field{Value: outside}}); toolrejection.AsToolReject(err) == nil {
		t.Fatalf("input redirect outside attached roots was admitted or missing ToolReject: %v", err)
	}
	if _, err := os.Lstat(outside); !os.IsNotExist(err) {
		t.Fatal("refused redirect created a file")
	}
	r := h.run("", "rm", "-f", "--", outside)
	if r.result.Terminal != toolcontract.ProducerRefused || toolrejection.AsToolReject(r.err) == nil || r.result.EffectCertainty != toolcontract.NoEffect || !strings.Contains(r.stderr, r.result.StopCode) {
		t.Fatalf("rm outside roots %+v %v %q", r.result, r.err, r.stderr)
	}
}

func TestRecursiveRemoveIsRecoverableAndRoundTrips(t *testing.T) {
	tree := map[string]string{"tree/a.txt": "alpha\n", "tree/sub/b.txt": "beta\n", "tree/sub/deep/c.txt": "gamma\n", "keep.txt": "stays\n"}
	h := newHarness(t, tree)
	r := h.run("", "rm", "-rf", "tree")
	if r.err != nil || r.result.Status != 0 || r.result.EffectCertainty != toolcontract.EffectCompleted || !h.absent("tree") {
		t.Fatalf("rm -rf %+v %v %q", r.result, r.err, r.stderr)
	}
	// Three files and three directories, each its own recoverable removal.
	h.undoAll(6)
	for name, content := range tree {
		if h.read(name) != content {
			t.Fatalf("%s did not round-trip", name)
		}
	}
	if r := h.run("", "rm", "tree"); r.result.Status != 1 || !strings.Contains(r.stderr, "Is a directory") || h.absent("tree") {
		t.Fatalf("rm without -r removed a directory: %+v %q", r.result, r.stderr)
	}
	if r := h.run("", "rm", "missing", "keep.txt"); r.result.Status != 1 || !h.absent("keep.txt") || !strings.Contains(r.stderr, "No such file") {
		t.Fatalf("rm continued past a missing operand: %+v %q", r.result, r.stderr)
	}
	if r := h.run("", "rm", "-f", "missing"); r.result.Status != 0 || r.err != nil {
		t.Fatalf("rm -f missing %+v %v", r.result, r.err)
	}
}

func TestMoveCopyMkdirChmodAndRmdir(t *testing.T) {
	h := newHarness(t, map[string]string{"a.txt": "a\n", "b.txt": "b\n", "dir/": "", "src/x.txt": "x\n", "src/in/y.txt": "y\n"})
	steps := []struct {
		words  []string
		status int
		check  func() bool
	}{
		{[]string{"mv", "-n", "a.txt", "b.txt"}, 0, func() bool { return h.read("a.txt") == "a\n" && h.read("b.txt") == "b\n" }},
		{[]string{"mv", "a.txt", "dir"}, 0, func() bool { return h.absent("a.txt") && h.read("dir/a.txt") == "a\n" }},
		{[]string{"cp", "b.txt", "c.txt"}, 0, func() bool { return h.read("c.txt") == "b\n" && h.read("b.txt") == "b\n" }},
		{[]string{"cp", "src", "copy"}, 1, func() bool { return h.absent("copy") }},
		{[]string{"cp", "-r", "src", "copy"}, 0, func() bool { return h.read("copy/x.txt") == "x\n" && h.read("copy/in/y.txt") == "y\n" }},
		{[]string{"cp", "-n", "c.txt", "b.txt"}, 0, func() bool { return h.read("b.txt") == "b\n" }},
		{[]string{"mkdir", "-p", "made/deep/er"}, 0, func() bool { info, err := os.Stat(h.path("made/deep/er")); return err == nil && info.IsDir() }},
		{[]string{"mkdir", "made"}, 1, func() bool { return true }},
		{[]string{"mkdir", "no/parent"}, 1, func() bool { return h.absent("no") }},
		{[]string{"chmod", "600", "c.txt"}, 0, func() bool { info, err := os.Stat(h.path("c.txt")); return err == nil && info.Mode().Perm() == 0o600 }},
		{[]string{"rmdir", "made/deep/er"}, 0, func() bool { return h.absent("made/deep/er") }},
		{[]string{"rmdir", "c.txt"}, 1, func() bool { return h.read("c.txt") == "b\n" }},
	}
	for _, step := range steps {
		r := h.run("", step.words...)
		if r.err != nil || r.result.Status != step.status || !step.check() {
			t.Fatalf("%v: %+v %v %q", step.words, r.result, r.err, r.stderr)
		}
	}
	if r := h.run("", "mv", "--not-a-version-1-option", "b.txt", "e.txt"); r.err == nil {
		t.Fatal("unsupported option bound")
	}
}

func TestTouchCreatesMissingFiles(t *testing.T) {
	h := newHarness(t, nil)
	r := h.run("", "touch", "new.txt")
	if r.err != nil || r.result.Status != 0 || h.read("new.txt") != "" || len(h.pins.pins[r.execution]) != 1 {
		t.Fatalf("touch new %+v %v", r.result, r.err)
	}
	if r := h.run("", "touch", "-c", "skipped.txt"); r.err != nil || !h.absent("skipped.txt") {
		t.Fatalf("touch -c created a file: %v", r.err)
	}
}

func TestTeeCapturesBeforeReadingAndPublishesAfterInput(t *testing.T) {
	h := newHarness(t, map[string]string{"log.txt": "earlier\n"})
	r := h.run("streamed\nbytes\n", "tee", "one.txt", "two.txt", "/dev/null")
	if r.err != nil || r.stdout != "streamed\nbytes\n" || h.read("one.txt") != r.stdout || h.read("two.txt") != r.stdout {
		t.Fatalf("tee %+v %v %q", r.result, r.err, r.stdout)
	}
	if pins := h.pins.pins[r.execution]; len(pins) != 2 || pins[0].Path != "one.txt" || pins[1].Path != "two.txt" {
		t.Fatalf("tee pins %+v", pins)
	}
	if r := h.run("more\n", "tee", "-a", "log.txt"); r.err != nil || h.read("log.txt") != "earlier\nmore\n" {
		t.Fatalf("tee -a %v %q", r.err, h.read("log.txt"))
	}
}

func TestMktempUsesTheSessionTMPDIR(t *testing.T) {
	h := newHarness(t, nil)
	scratch := h.tc.Host.SessionScratchDir
	for _, words := range [][]string{{"mktemp"}, {"mktemp", "-d"}, {"mktemp", "-t", "build"}, {"mktemp", "job.XXXX"}} {
		r := h.run("", words...)
		path := strings.TrimSuffix(r.stdout, "\n")
		if r.err != nil || r.result.Status != 0 || filepath.Dir(path) != scratch {
			t.Fatalf("%v: %q %+v %v", words, r.stdout, r.result, r.err)
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() != slices.Contains(words, "-d") {
			t.Fatalf("%v created %v %v", words, info, err)
		}
	}
	if r := h.run("", "mktemp", "short.XX"); r.result.Terminal != toolcontract.ProducerRefused {
		t.Fatalf("short template accepted: %+v", r.result)
	}
	if r := h.run("", "mktemp", "../escape.XXXX"); r.result.Terminal != toolcontract.ProducerRefused {
		t.Fatalf("path template accepted: %+v", r.result)
	}
}

// cmd > a 2> b opens two destinations for one execution, and a destination
// may open after that execution pinned a source: every capture appends at
// the execution's next position and each stage publishes at its own pin.
func TestOneExecutionPinsSeveralDestinationsAfterASource(t *testing.T) {
	h := newHarness(t, map[string]string{"in.txt": "input\n", "b.txt": "old b\n"})
	inv := h.invocation()
	input, err := h.publication().OpenInput(t.Context(), commandprogram.InputDestination{Invocation: inv, Target: commandprogram.Field{Value: "in.txt"}})
	testutil.FailErr(t, "open input", err)
	data, err := io.ReadAll(input)
	testutil.FailErr(t, "read input", err)
	open := func(operator, target string) commandprogram.StagedOutput {
		staged, err := h.publication().Open(t.Context(), commandprogram.OutputDestination{Invocation: inv, Redirect: commandsurface.ProgramRedirect{Operator: operator, Descriptor: -1}, Target: commandprogram.Field{Value: target, Span: inv.Span}})
		testutil.FailErr(t, "open "+operator+" "+target, err)
		return staged
	}
	stdout, stderr := open(">", "a.txt"), open("2>", "b.txt")
	_, err = stdout.Write(data)
	testutil.FailErr(t, "stage stdout", err)
	_, err = io.WriteString(stderr, "diagnostic\n")
	testutil.FailErr(t, "stage stderr", err)
	for _, staged := range []commandprogram.StagedOutput{stdout, stderr} {
		_, err := staged.Publish(t.Context())
		testutil.FailErr(t, "publish", err)
	}
	if h.read("a.txt") != "input\n" || h.read("b.txt") != "diagnostic\n" {
		t.Fatalf("a %q b %q", h.read("a.txt"), h.read("b.txt"))
	}
	pins := h.pins.pins[inv.Identity.ExecutionID]
	if len(pins) != 3 || pins[0].Role != toolcontract.SourceRevision || pins[0].Path != "in.txt" ||
		pins[1].Role != toolcontract.DestinationRevision || pins[1].Path != "a.txt" || pins[1].Mode != toolcontract.AbsentRevision ||
		pins[2].Role != toolcontract.DestinationRevision || pins[2].Path != "b.txt" || pins[2].Mode != toolcontract.PublishedDiskRevision {
		t.Fatalf("pins %+v", pins)
	}
}

// tee with redirected stdout: the redirect's destination and both tee
// destinations share one execution's capture order.
func TestTeeDestinationsAfterARedirectInOneExecution(t *testing.T) {
	h := newHarness(t, nil)
	inv := h.invocation()
	redirect, err := h.publication().Open(t.Context(), commandprogram.OutputDestination{Invocation: inv, Redirect: commandsurface.ProgramRedirect{Operator: ">", Descriptor: -1}, Target: commandprogram.Field{Value: "copy.txt", Span: inv.Span}})
	testutil.FailErr(t, "open redirect", err)
	r := h.runAs(inv, redirect, "teed\n", "tee", "a.txt", "b.txt")
	if r.err != nil || r.result.Status != 0 {
		t.Fatalf("tee %+v %v", r.result, r.err)
	}
	_, err = redirect.Publish(t.Context())
	testutil.FailErr(t, "publish redirect", err)
	if h.read("a.txt") != "teed\n" || h.read("b.txt") != "teed\n" || h.read("copy.txt") != "teed\n" {
		t.Fatalf("a %q b %q copy %q", h.read("a.txt"), h.read("b.txt"), h.read("copy.txt"))
	}
	pins := h.pins.pins[inv.Identity.ExecutionID]
	if len(pins) != 3 || pins[0].Path != "copy.txt" || pins[1].Path != "a.txt" || pins[2].Path != "b.txt" {
		t.Fatalf("pins %+v", pins)
	}
}

func TestEditReplaceLinesCodeRewriteAndSourceCapture(t *testing.T) {
	h := newHarness(t, map[string]string{
		"file.txt":  "alpha\nbeta\ngamma\n",
		"main.go":   "package p\n\nfunc f() int {\n\treturn 1 + 2\n}\n",
		"script.sh": "echo ok\n",
	})
	if r := h.run("", "edit", "--path", "file.txt", "--old-string", "beta", "--new-string", "BETA"); r.err != nil || r.result.Status != 0 || h.read("file.txt") != "alpha\nBETA\ngamma\n" {
		t.Fatalf("edit: %+v %v %q", r.result, r.err, h.read("file.txt"))
	}
	if r := h.run("", "replace-lines", "--path", "file.txt", "--start", "2", "--end", "3", "--content", "two\nthree"); r.err != nil || r.result.Status != 0 || h.read("file.txt") != "alpha\ntwo\nthree\n" {
		t.Fatalf("replace-lines: %+v %v %q", r.result, r.err, h.read("file.txt"))
	}
	if r := h.run("", "code-rewrite", "--path", "main.go", "--pattern", "1 + 2", "--rewrite", "3", "--dry-run"); r.err != nil || r.result.Status != 0 || !strings.Contains(h.read("main.go"), "1 + 2") {
		t.Fatalf("code-rewrite --dry-run: %+v %v %q", r.result, r.err, h.read("main.go"))
	}
	if r := h.run("", "code-rewrite", "--path", "main.go", "--pattern", "1 + 2", "--rewrite", "3"); r.err != nil || r.result.Status != 0 || !strings.Contains(h.read("main.go"), "return 3") {
		t.Fatalf("code-rewrite: %+v %v %q", r.result, r.err, h.read("main.go"))
	}
	inv := h.invocation()
	captured, err := h.publication().Capture(t.Context(), inv, commandprogram.SourceCaptureRequest{
		Path:           "script.sh",
		Representation: toolcontract.UTF8SourceBytes,
	})
	testutil.FailErr(t, "capture source script", err)
	if string(captured.Bytes) != "echo ok\n" || captured.Pin.SourceID != "script.sh" {
		t.Fatalf("captured source = %+v", captured)
	}
}
