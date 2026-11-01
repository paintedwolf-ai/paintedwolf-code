package gitcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
	"github.com/lycaon/lycaon/internal/tools/native/patchapply"
)

type pinLog struct {
	mu   sync.Mutex
	pins []toolcontract.ExecutionRevision
}

func (l *pinLog) AppendRevisionPin(_ context.Context, _ string, pin toolcontract.ExecutionRevision) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pins = append(l.pins, pin)
	return pin.Validate()
}

// newApplyHarness is the Git harness with the program's staged publication.
// Its tree: a.txt "one\nTWO\nthree\n" against HEAD's "one\ntwo\nthree\n",
// staged.txt staged, new.txt untracked.
func newApplyHarness(t *testing.T) *harness {
	h := newHarness(t)
	b := h.owners.Boundary
	owners, err := mutation.NewOwners(native.ExecutionRevisions{Boundary: b}, &pinLog{}, mutation.Operations{Write: &native.WriteTool{Boundary: b}, Delete: &native.DeleteTool{Boundary: b},
		Move: &native.MoveTool{Boundary: b}, Copy: &native.CopyTool{Boundary: b}, Mkdir: &native.MkdirTool{Boundary: b}, Chmod: &native.ChmodTool{Boundary: b},
		Chown: &native.ChownTool{Boundary: b}, Times: &native.TimesTool{Boundary: b}, Extract: &native.ExtractArchiveTool{Boundary: b}})
	testutil.FailErr(t, "mutation owners", err)
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	testutil.FailErr(t, "memory limits", err)
	owners.Revisions.Memory, err = toolcontract.NewProgramMemory(limits)
	testutil.FailErr(t, "program memory", err)
	h.filesystem = owners
	return h
}

func (h *harness) read(name string) string {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(h.root, name))
	testutil.FailErr(h.t, "read "+name, err)
	return string(data)
}

func (h *harness) absent(name string) bool {
	_, err := os.Lstat(filepath.Join(h.root, name))
	return os.IsNotExist(err)
}

func (h *harness) apply(stdin string, words ...string) run {
	h.t.Helper()
	h.stdin = stdin
	defer func() { h.stdin = "" }()
	return h.exec(append([]string{"git", "apply"}, words...)...)
}

// sample modifies a.txt, creates c.txt, and deletes sub/b.txt against HEAD.
const sample = `diff --git a/a.txt b/a.txt
index 4cb29ea..e1c6e7d 100644
--- a/a.txt
+++ b/a.txt
@@ -1,3 +1,4 @@
 one
 two
 three
+four
diff --git a/c.txt b/c.txt
new file mode 100644
index 0000000..f2ad6c7
--- /dev/null
+++ b/c.txt
@@ -0,0 +1 @@
+c
diff --git a/sub/b.txt b/sub/b.txt
deleted file mode 100644
index 9f5d2bc..0000000
--- a/sub/b.txt
+++ /dev/null
@@ -1 +0,0 @@
-bee
`

func (h *harness) clean() {
	h.t.Helper()
	h.git("reset", "-q", "--hard")
	h.git("clean", "-q", "-fd")
}

func TestApplyReportsMatchBundledGit(t *testing.T) {
	t.Parallel()
	h := newApplyHarness(t)
	h.write("rename-src/long/path/name.txt", strings.Repeat("line\n", 40))
	h.write("tool.sh", "#!/bin/sh\n")
	h.git("add", ".")
	h.git("commit", "-q", "-m", "fixtures")
	h.git("mv", "rename-src/long/path/name.txt", "rename-src/long/path/moved.txt")
	h.write("rename-src/long/path/moved.txt", strings.Repeat("line\n", 39)+"changed\n")
	testutil.FailErr(t, "chmod", os.Chmod(filepath.Join(h.root, "tool.sh"), 0o755))
	h.write("img.bin", "\x00\x01\x02\x03")
	h.write("a.txt", strings.Repeat("x\n", 90))
	h.git("add", "-A")
	diff := h.git("diff", "--cached", "--binary", "-M")
	h.write("change.diff", diff)
	h.git("reset", "-q", "--hard")
	for _, words := range [][]string{{"--stat"}, {"--numstat"}, {"--summary"}, {"--stat", "--summary"}, {"--numstat", "-R"}, {"--stat", "-p1"}, {"--numstat", "--directory=deep/root"}} {
		args := append(append([]string{}, words...), "change.diff")
		want, wantErr, wantStatus := h.direct(h.root, append([]string{"apply"}, args...)...)
		r := h.apply("", args...)
		if r.err != nil || r.result.Status != wantStatus || r.stdout != want || r.stderr != wantErr || r.result.EffectCertainty != toolcontract.NoEffect {
			t.Fatalf("git apply %v:\ngot  %d %q %q %v\nwant %d %q %q", words, r.result.Status, r.stdout, r.stderr, r.err, wantStatus, want, wantErr)
		}
	}
}

func TestApplyWritesTheWorkTreeOnlyWhenEverySectionApplies(t *testing.T) {
	t.Parallel()
	h := newApplyHarness(t)
	h.clean()
	r := h.apply(sample)
	if r.err != nil || r.result.Status != 0 || r.result.EffectCertainty != toolcontract.EffectCompleted || r.stderr != "" {
		t.Fatalf("apply = %+v %v %q", r.result, r.err, r.stderr)
	}
	if h.read("a.txt") != "one\ntwo\nthree\nfour\n" || h.read("c.txt") != "c\n" || !h.absent("sub/b.txt") {
		t.Fatal("work tree does not hold the patch")
	}
	if staged := h.git("diff", "--cached", "--name-only"); staged != "" {
		t.Fatalf("plain apply staged %q", staged)
	}
	h.clean()
	h.write("a.txt", "changed\n")
	r = h.apply(sample, "-v")
	if r.result.Status != 1 || r.result.EffectCertainty != toolcontract.NoEffect {
		t.Fatalf("partial apply = %+v %v", r.result, r.err)
	}
	if !h.absent("c.txt") || h.read("sub/b.txt") != "bee\n" {
		t.Fatal("a failing section let other sections land")
	}
	if !strings.HasPrefix(r.stderr, "Checking patch a.txt...\nerror: while searching for:\none\ntwo\nthree\n\nerror: patch failed: a.txt:1\n") {
		t.Fatalf("verbose stderr = %q", r.stderr)
	}
}

func TestApplyCheckAndPatchDryRunReportTheSameHunks(t *testing.T) {
	t.Parallel()
	h := newApplyHarness(t)
	h.clean()
	h.write("a.txt", "one\ntwo\nTHREE\n")
	prefix, _, _ := strings.Cut(sample, "diff --git a/c.txt")
	diff := strings.Replace(prefix, "@@ -1,3 +1,4 @@\n one\n two\n three\n+four\n", "@@ -1,2 +1,2 @@\n-one\n+ONE\n two\n@@ -2,2 +2,3 @@\n two\n three\n+four\n", 1)
	check := h.apply(diff, "--check")
	if check.result.Status != 1 || check.result.EffectCertainty != toolcontract.NoEffect || h.read("a.txt") != "one\ntwo\nTHREE\n" {
		t.Fatalf("--check = %+v, a.txt=%q", check.result, h.read("a.txt"))
	}
	dry := h.patch(diff, "--dry-run", "-p1", "-F", "0")
	if dry.result.Status != 1 || h.read("a.txt") != "one\ntwo\nTHREE\n" {
		t.Fatalf("patch --dry-run = %+v %v", dry.result, dry.err)
	}
	if check.result.Evidence.Kind != patchapply.EvidenceKind || check.result.Evidence != dry.result.Evidence {
		t.Fatalf("outcomes differ:\ngit apply --check %+v\npatch --dry-run   %+v", check.result.Evidence, dry.result.Evidence)
	}
	var evidence struct {
		Failed []patchapply.Failure `json:"failed"`
	}
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(check.result.Evidence.EvidenceRef), &evidence))
	if len(evidence.Failed) != 1 || evidence.Failed[0].Hunk != 2 || evidence.Failed[0].File != "a.txt" {
		t.Fatalf("evidence = %s", check.result.Evidence.EvidenceRef)
	}
}

// patch runs the patch utility on the same staged publication.
func (h *harness) patch(stdin string, words ...string) run {
	h.t.Helper()
	inv := toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: "program", NodeID: "n1", ExecutionID: uuid.NewString()},
		ActorID: "actor", SessionID: "s1", Scope: toolcontract.OperationScope{Kind: toolcontract.ProjectOperationScope, ProjectID: "p1", RootID: "r1"},
		SourceUnitID: "program", Span: toolcontract.SourceSpan{Line: 1, Column: 1}, CatalogDigest: toolcontract.LanguageCatalogDigest()}
	words = append([]string{"patch"}, words...)
	argv := make(commandprogram.Fields, len(words))
	for i, word := range words {
		argv[i] = commandprogram.Field{Value: word, Span: inv.Span}
	}
	statement := commandsurface.Statement{SyntaxLocation: commandsurface.SyntaxLocation{ID: commandsurface.NodeID(inv.Identity.NodeID), Span: inv.Span}}
	prepared, err := h.filesystem.Prepare(mutation.Program{Paths: h.paths}, commandprogram.OperationRequest{Invocation: inv, OperationID: "patch", Statement: statement, Argv: argv})
	testutil.FailErr(h.t, "prepare patch", err)
	var stdout, stderr bytes.Buffer
	result, err := prepared.InvokeOwner(h.t.Context(), h.tc, commandprogram.Streams{Input: io.NopCloser(strings.NewReader(stdin)), Output: &stdout, Error: &stderr})
	return run{stdout: stdout.String(), stderr: stderr.String(), result: result, err: err}
}

func TestApplyReverseUndoesTheApplication(t *testing.T) {
	t.Parallel()
	h := newApplyHarness(t)
	h.clean()
	if r := h.apply(sample); r.result.Status != 0 {
		t.Fatalf("forward = %+v %q", r.result, r.stderr)
	}
	r := h.apply(sample, "-R")
	if r.err != nil || r.result.Status != 0 {
		t.Fatalf("reverse = %+v %v %q", r.result, r.err, r.stderr)
	}
	if status := h.git("status", "--porcelain"); status != "" {
		t.Fatalf("reverse left %q", status)
	}
}

func TestApplyIndexStagesExactlyThePatchedPaths(t *testing.T) {
	t.Parallel()
	h := newApplyHarness(t)
	h.git("checkout", "-q", "--", "a.txt")
	r := h.apply(sample, "--index")
	if r.err != nil || r.result.Status != 0 || r.result.EffectCertainty != toolcontract.EffectCompleted {
		t.Fatalf("--index = %+v %v %q", r.result, r.err, r.stderr)
	}
	if staged := h.git("diff", "--cached", "--name-status"); staged != "M\ta.txt\nA\tc.txt\nA\tstaged.txt\nD\tsub/b.txt\n" {
		t.Fatalf("index = %q", staged)
	}
	if h.git("show", ":a.txt") != h.read("a.txt") || h.git("show", ":staged.txt") != "s\n" {
		t.Fatal("index content differs from the patched work tree or lost unrelated staged work")
	}
	if got := sessionStaged(h.owners, h.tc.ChatSessionID(), h.root); strings.Join(got, ",") != "a.txt,c.txt,sub/b.txt" {
		t.Fatalf("session staged = %v", got)
	}

	h.clean()
	h.write("a.txt", "one\ntwo\nthree\n\n")
	r = h.apply(sample, "--index")
	if r.result.Status != 1 || r.stderr != "error: a.txt: does not match index\n" || !h.absent("c.txt") {
		t.Fatalf("mismatch = %+v %q", r.result, r.stderr)
	}
}

func TestApplyCachedChangesOnlyTheIndex(t *testing.T) {
	t.Parallel()
	h := newApplyHarness(t)
	r := h.apply(sample, "--cached")
	if r.err != nil || r.result.Status != 0 {
		t.Fatalf("--cached = %+v %v %q", r.result, r.err, r.stderr)
	}
	if h.git("show", ":a.txt") != "one\ntwo\nthree\nfour\n" || h.git("show", ":c.txt") != "c\n" || h.git("ls-files", "sub/b.txt") != "" {
		t.Fatal("index does not hold the patch")
	}
	if h.read("a.txt") != "one\nTWO\nthree\n" || !h.absent("c.txt") || h.read("sub/b.txt") != "bee\n" {
		t.Fatal("--cached changed the work tree")
	}
	r = h.apply(sample, "--cached")
	if r.result.Status != 1 || !strings.Contains(r.stderr, "error: c.txt: already exists in index\n") || !strings.Contains(r.stderr, "error: sub/b.txt: does not exist in index\n") {
		t.Fatalf("second --cached = %+v %q", r.result, r.stderr)
	}
}

func TestApplyThreeWayReportsConflictsAndTheIndexState(t *testing.T) {
	t.Parallel()
	h := newApplyHarness(t)
	h.clean()
	base := h.git("rev-parse", "HEAD:a.txt")
	diff := "diff --git a/a.txt b/a.txt\nindex " + strings.TrimSpace(base)[:7] + "..1111111 100644\n--- a/a.txt\n+++ b/a.txt\n@@ -1,3 +1,3 @@\n one\n-two\n+deux\n three\n"
	h.write("a.txt", "one\nzwei\nthree\n")
	h.git("commit", "-q", "-am", "zwei")
	r := h.apply(diff, "--3way")
	if r.err != nil || r.result.Status != 1 || r.result.EffectCertainty != toolcontract.EffectCompleted {
		t.Fatalf("--3way = %+v %v %q", r.result, r.err, r.stderr)
	}
	if r.stderr != "Applied patch to 'a.txt' with conflicts.\nU a.txt\n" {
		t.Fatalf("stderr = %q", r.stderr)
	}
	if h.read("a.txt") != "one\n<<<<<<< ours\nzwei\n=======\ndeux\n>>>>>>> theirs\nthree\n" {
		t.Fatalf("work tree = %q", h.read("a.txt"))
	}
	for stage, want := range map[string]string{"1": "one\ntwo\nthree\n", "2": "one\nzwei\nthree\n", "3": "one\ndeux\nthree\n"} {
		if got := h.git("show", ":"+stage+":a.txt"); got != want {
			t.Fatalf("stage %s = %q", stage, got)
		}
	}
	if status := h.git("status", "--porcelain"); status != "UU a.txt\n" {
		t.Fatalf("status = %q", status)
	}

	h.clean()
	h.write("a.txt", "one\ntwo\nthree\nfour\n")
	h.git("commit", "-q", "-am", "four")
	r = h.apply(strings.Replace(diff, "@@ -1,3 +1,3 @@\n one\n-two\n+deux\n three\n", "@@ -1,2 +1,2 @@\n-one\n+uno\n two\n", 1), "-3")
	if r.result.Status != 0 || r.stderr != "Applied patch to 'a.txt' cleanly.\n" || h.read("a.txt") != "uno\ntwo\nthree\nfour\n" || h.git("show", ":a.txt") != "uno\ntwo\nthree\nfour\n" {
		t.Fatalf("clean three-way = %+v %q %q", r.result, r.stderr, h.read("a.txt"))
	}
}

func TestApplyBinaryPatches(t *testing.T) {
	t.Parallel()
	h := newApplyHarness(t)
	h.clean()
	data := make([]byte, 2048)
	for i := range data {
		data[i] = byte((i * 7) % 251)
	}
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(h.root, "d.bin"), data, 0o644))
	h.git("add", "d.bin")
	h.git("commit", "-q", "-m", "binary")
	changed := append([]byte(nil), data...)
	changed[100], changed[1500] = 255, 0
	testutil.FailErr(t, "change", os.WriteFile(filepath.Join(h.root, "d.bin"), changed, 0o644))
	h.write("n.bin", "\x00\x01\x02\x03")
	h.git("add", "-A")
	diff := h.git("diff", "--cached", "--binary")
	if !strings.Contains(diff, "delta ") || !strings.Contains(diff, "literal ") {
		t.Fatalf("fixture lacks delta or literal hunks:\n%s", diff)
	}
	h.git("reset", "-q", "--hard")
	r := h.apply(diff)
	if r.err != nil || r.result.Status != 0 || h.read("d.bin") != string(changed) || h.read("n.bin") != "\x00\x01\x02\x03" {
		t.Fatalf("binary apply = %+v %v %q", r.result, r.err, r.stderr)
	}
	if r := h.apply(diff, "-R"); r.result.Status != 0 || h.read("d.bin") != string(data) || !h.absent("n.bin") {
		t.Fatalf("binary reverse = %+v %q", r.result, r.stderr)
	}
	p := h.patch(diff, "-p1")
	if reject := toolrejection.AsToolReject(p.err); reject == nil || reject.Code != "COMMAND_NATIVE_FORM_UNSUPPORTED" || h.read("d.bin") != string(data) {
		t.Fatalf("patch on a binary patch = %+v %v", p.result, p.err)
	}
}

func TestApplyStaleWorkTreeFileRefusesWithTheRevisionCode(t *testing.T) {
	t.Parallel()
	h := newApplyHarness(t)
	h.clean()
	h.tc.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
		for _, change := range changes {
			if strings.HasSuffix(change.Path, "a.txt") {
				return os.WriteFile(filepath.Join(h.root, "a.txt"), []byte("edited by the user\n"), 0o644)
			}
		}
		return nil
	}
	r := h.apply(sample)
	if reject := toolrejection.AsToolReject(r.err); reject == nil || reject.Code != "COMMAND_REVISION_CONFLICT" || r.result.StopCode != "COMMAND_REVISION_CONFLICT" {
		t.Fatalf("stale = %+v %v", r.result, r.err)
	}
	if h.read("a.txt") != "edited by the user\n" {
		t.Fatalf("a.txt = %q", h.read("a.txt"))
	}
}

func TestApplyOptionsAndDiagnosticsFollowGit(t *testing.T) {
	t.Parallel()
	trailing := "--- a/a.txt\n+++ b/a.txt\n@@ -1,3 +1,3 @@\n one\n-two\n+two \n three\n"
	cases := []struct {
		name           string
		stdin          string
		words          []string
		status         int
		stdout, stderr string
		file, content  string
	}{
		{name: "whitespace warn", stdin: trailing, stderr: "<stdin>:6: trailing whitespace.\ntwo \nwarning: 1 line adds whitespace errors.\n", file: "a.txt", content: "one\ntwo \nthree\n"},
		{name: "whitespace nowarn", stdin: trailing, words: []string{"--whitespace=nowarn"}, file: "a.txt", content: "one\ntwo \nthree\n"},
		{name: "whitespace fix", stdin: trailing, words: []string{"--whitespace=fix"}, stderr: "<stdin>:6: trailing whitespace.\ntwo \nwarning: 1 line applied after fixing whitespace errors.\n", file: "a.txt", content: "one\ntwo\nthree\n"},
		{name: "whitespace error", stdin: trailing, words: []string{"--whitespace=error"}, status: 128, stderr: "<stdin>:6: trailing whitespace.\ntwo \nerror: 1 line adds whitespace errors.\n", file: "a.txt", content: "one\ntwo\nthree\n"},
		{name: "check is quiet about whitespace", stdin: trailing, words: []string{"--check"}, file: "a.txt", content: "one\ntwo\nthree\n"},
		{name: "include", stdin: sample, words: []string{"--include=c.*"}, file: "a.txt", content: "one\ntwo\nthree\n"},
		{name: "exclude", stdin: sample, words: []string{"--exclude=*.txt", "--include=c.txt"}, file: "c.txt", content: ""},
		{name: "directory", stdin: "--- a/b.txt\n+++ b/b.txt\n@@ -1 +1 @@\n-bee\n+BEE\n", words: []string{"--directory=sub"}, file: "sub/b.txt", content: "BEE\n"},
		{name: "strip zero", stdin: "--- sub/b.txt\n+++ sub/b.txt\n@@ -1 +1 @@\n-bee\n+BEE\n", words: []string{"-p0"}, file: "sub/b.txt", content: "BEE\n"},
		{name: "verbose", stdin: "--- a/sub/b.txt\n+++ b/sub/b.txt\n@@ -1 +1 @@\n-bee\n+BEE\n", words: []string{"-v"}, stderr: "Checking patch sub/b.txt...\nApplied patch sub/b.txt cleanly.\n", file: "sub/b.txt", content: "BEE\n"},
		{name: "no patch", stdin: "prose only\n", status: 128, stderr: "error: No valid patches in input (allow with \"--allow-empty\")\n"},
		{name: "missing file", stdin: "--- a/nope.txt\n+++ b/nope.txt\n@@ -1 +1 @@\n-a\n+b\n", status: 1, stderr: "error: nope.txt: No such file or directory\n"},
		{name: "creation over an existing file", stdin: "--- /dev/null\n+++ b/a.txt\n@@ -0,0 +1 @@\n+x\n", status: 1, stderr: "error: a.txt: already exists in working directory\n"},
		{name: "removal leaves content", stdin: "--- a/a.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-one\n", status: 1, stderr: "error: removal patch leaves file contents\nerror: a.txt: patch does not apply\n", file: "a.txt", content: "one\ntwo\nthree\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newApplyHarness(t)
			h.clean()
			r := h.apply(tc.stdin, tc.words...)
			if r.err != nil || r.result.Status != tc.status || r.stdout != tc.stdout || r.stderr != tc.stderr {
				t.Fatalf("status=%d err=%v stdout=%q stderr=%q", r.result.Status, r.err, r.stdout, r.stderr)
			}
			if tc.file != "" && tc.content == "" && !h.absent(tc.file) {
				t.Fatalf("%s exists", tc.file)
			}
			if tc.file != "" && tc.content != "" && h.read(tc.file) != tc.content {
				t.Fatalf("%s = %q", tc.file, h.read(tc.file))
			}
		})
	}
	h := newApplyHarness(t)
	for _, words := range [][]string{{"--index", "--cached"}, {"--reject"}, {"--unidiff-zero"}} {
		if reject := toolrejection.AsToolReject(h.apply(sample, words...).err); reject == nil {
			t.Fatalf("%v accepted", words)
		}
	}
	h.filesystem = nil
	if reject := toolrejection.AsToolReject(h.apply(sample).err); reject == nil || reject.Code != "COMMAND_PLATFORM_UNAVAILABLE" {
		t.Fatal("git apply ran without the staged publication")
	}
}
