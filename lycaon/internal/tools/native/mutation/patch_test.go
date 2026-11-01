package mutation

import (
	"context"
	"encoding/json"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/patchapply"
)

// threeFiles modifies a.txt, creates new/c.txt and deletes gone.txt.
const threeFiles = `diff --git a/a.txt b/a.txt
index 1111111..2222222 100644
--- a/a.txt
+++ b/a.txt
@@ -1,3 +1,3 @@
 one
-two
+TWO
 three
diff --git a/new/c.txt b/new/c.txt
new file mode 100644
index 0000000..3333333
--- /dev/null
+++ b/new/c.txt
@@ -0,0 +1,2 @@
+created
+file
diff --git a/gone.txt b/gone.txt
deleted file mode 100644
index 4444444..0000000
--- a/gone.txt
+++ /dev/null
@@ -1 +0,0 @@
-bye
`

const twoHunkDiff = `--- a/f.txt
+++ b/f.txt
@@ -1,3 +1,3 @@
 one
-two
+TWO
 three
@@ -8,3 +8,3 @@
 eight
-nine
+NINE
 ten
`

const tenLines = "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n"

func threeFileTree() map[string]string {
	return map[string]string{"a.txt": "one\ntwo\nthree\n", "gone.txt": "bye\n", "fix.diff": threeFiles}
}

func (h *harness) noRejects() {
	h.t.Helper()
	testutil.FailErr(h.t, "walk", filepath.WalkDir(h.root, func(path string, d os.DirEntry, err error) error {
		if err == nil && (strings.HasSuffix(path, ".rej") || strings.HasSuffix(path, ".orig")) {
			h.t.Fatalf("patch wrote %s", path)
		}
		return err
	}))
}

func TestPatchAppliesCreationModificationAndDeletionAsAuthoredEffects(t *testing.T) {
	h := newHarness(t, threeFileTree())
	r := h.run("", "patch", "-p1", "-i", "fix.diff")
	if r.err != nil || r.result.Status != 0 || r.result.EffectCertainty != toolcontract.EffectCompleted {
		t.Fatalf("patch = %+v %v stderr=%q", r.result, r.err, r.stderr)
	}
	if r.stdout != "patching file a.txt\npatching file new/c.txt\npatching file gone.txt\n" || r.stderr != "" {
		t.Fatalf("stdout=%q stderr=%q", r.stdout, r.stderr)
	}
	if h.read("a.txt") != "one\nTWO\nthree\n" || h.read("new/c.txt") != "created\nfile\n" || !h.absent("gone.txt") {
		t.Fatalf("tree a=%q c=%q gone absent=%v", h.read("a.txt"), h.read("new/c.txt"), h.absent("gone.txt"))
	}
	authored, err := h.tc.Source.History.Authorship.SessionAuthoredPaths(t.Context(), h.project.ID, "s1", "r1")
	testutil.FailErr(t, "authored paths", err)
	for _, path := range []string{"a.txt", "new/c.txt", "gone.txt"} {
		if !slices.Contains(authored, path) {
			t.Fatalf("%s is not an authored effect: %v", path, authored)
		}
	}
	// One pin for the -i input, then each section's input and destination
	// (a deletion pins its file as both).
	if pins := h.pins.pins[r.execution]; len(pins) != 6 {
		t.Fatalf("captured %d revisions before publishing, want 6: %+v", len(pins), pins)
	}
	// The deletion went through the recoverable delete owner.
	h.undoAll(1)
	if h.read("gone.txt") != "bye\n" {
		t.Fatal("history did not restore the deleted file")
	}
}

func TestPatchFailedHunkReportsStatusOneAndPublishesTheRest(t *testing.T) {
	h := newHarness(t, map[string]string{"f.txt": strings.Replace(tenLines, "nine", "nine changed", 1), "fix.diff": twoHunkDiff})
	r := h.run("", "patch", "-p1", "-F", "0", "-i", "fix.diff")
	if r.err != nil || r.result.Status != 1 || r.result.Terminal != toolcontract.ProducerCompleted || r.result.EffectCertainty != toolcontract.EffectCompleted {
		t.Fatalf("patch = %+v %v", r.result, r.err)
	}
	if r.stderr != "Hunk #2 FAILED at 8.\n1 out of 2 hunks FAILED\n" {
		t.Fatalf("stderr = %q", r.stderr)
	}
	if h.read("f.txt") != strings.Replace(strings.Replace(tenLines, "nine", "nine changed", 1), "two", "TWO", 1) {
		t.Fatalf("f.txt = %q", h.read("f.txt"))
	}
	h.noRejects()
	if r.result.Evidence.Kind != patchapply.EvidenceKind {
		t.Fatalf("evidence = %+v", r.result.Evidence)
	}
	var evidence struct {
		Failed []patchapply.Failure `json:"failed"`
	}
	testutil.FailErr(t, "decode evidence", json.Unmarshal([]byte(r.result.Evidence.EvidenceRef), &evidence))
	if len(evidence.Failed) != 1 || evidence.Failed[0] != (patchapply.Failure{File: "f.txt", Hunk: 2, Header: "@@ -8,3 +8,3 @@", Reason: "context does not match"}) {
		t.Fatalf("evidence = %s", r.result.Evidence.EvidenceRef)
	}
}

func TestPatchDryRunChangesNothingAndReportsTheSameHunks(t *testing.T) {
	files := map[string]string{"f.txt": "zero\n" + strings.Replace(tenLines, "nine", "nine changed", 1), "fix.diff": twoHunkDiff}
	h := newHarness(t, files)
	dry := h.run("", "patch", "--dry-run", "-p1", "-i", "fix.diff")
	if dry.result.Status != 1 || dry.result.EffectCertainty != toolcontract.NoEffect || h.read("f.txt") != files["f.txt"] {
		t.Fatalf("dry run = %+v, f.txt=%q", dry.result, h.read("f.txt"))
	}
	real := h.run("", "patch", "-p1", "-i", "fix.diff")
	if dry.stdout != strings.Replace(real.stdout, "patching file", "checking file", 1) || dry.stderr != real.stderr || dry.result.Evidence != real.result.Evidence {
		t.Fatalf("dry run reported\n%q %q\nreal run reported\n%q %q", dry.stdout, dry.stderr, real.stdout, real.stderr)
	}
	if real.stdout != "patching file f.txt\nHunk #1 succeeded at 2 (offset 1 line).\n" {
		t.Fatalf("offset report = %q", real.stdout)
	}
}

func TestPatchReverseUndoesTheAppliedPatchExactly(t *testing.T) {
	h := newHarness(t, threeFileTree())
	before := map[string]string{"a.txt": h.read("a.txt"), "gone.txt": h.read("gone.txt")}
	if r := h.run("", "patch", "-p1", "-i", "fix.diff"); r.result.Status != 0 {
		t.Fatalf("forward = %+v %v", r.result, r.err)
	}
	r := h.run("", "patch", "-R", "-p1", "-i", "fix.diff")
	if r.err != nil || r.result.Status != 0 {
		t.Fatalf("reverse = %+v %v %q", r.result, r.err, r.stderr)
	}
	if h.read("a.txt") != before["a.txt"] || h.read("gone.txt") != before["gone.txt"] || !h.absent("new/c.txt") {
		t.Fatalf("reverse left a=%q gone=%q c absent=%v", h.read("a.txt"), h.read("gone.txt"), h.absent("new/c.txt"))
	}
}

func TestPatchRefusesAFileChangedAfterCapture(t *testing.T) {
	firstHunk, _, _ := strings.Cut(twoHunkDiff, "@@ -8")
	h := newHarness(t, map[string]string{"f.txt": tenLines, "g.txt": "one\ntwo\nthree\n", "fix.diff": strings.Replace(twoHunkDiff, "@@ -8,3 +8,3 @@\n eight\n-nine\n+NINE\n ten\n", "", 1) + strings.ReplaceAll(firstHunk, "f.txt", "g.txt")})
	h.tc.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
		for _, change := range changes {
			if strings.HasSuffix(change.Path, "g.txt") {
				return os.WriteFile(h.path("g.txt"), []byte("edited by the user\n"), 0o644)
			}
		}
		return nil
	}
	r := h.run("", "patch", "-p1", "-i", "fix.diff")
	reject := toolrejection.AsToolReject(r.err)
	if reject == nil || reject.Code != "COMMAND_REVISION_CONFLICT" || r.result.StopCode != "COMMAND_REVISION_CONFLICT" {
		t.Fatalf("stale target = %+v %v", r.result, r.err)
	}
	if h.read("g.txt") != "edited by the user\n" || h.read("f.txt") != strings.Replace(tenLines, "two", "TWO", 1) || r.result.EffectCertainty != toolcontract.EffectCompleted {
		t.Fatalf("g.txt=%q f.txt=%q certainty=%s", h.read("g.txt"), h.read("f.txt"), r.result.EffectCertainty)
	}
	if !strings.Contains(r.stderr, "[COMMAND_REVISION_CONFLICT]") {
		t.Fatalf("stderr = %q", r.stderr)
	}
}

func TestPatchRefusesBinaryAndUnsupportedForms(t *testing.T) {
	binary := "diff --git a/b.bin b/b.bin\nnew file mode 100644\nindex 0000000000000000000000000000000000000000..eaf36c1daccfdf325514461cd1a2ffbc139b5464\nGIT binary patch\nliteral 4\nLcmZQzWMT#Y01f~L\n\nliteral 0\nHcmV?d00001\n\n"
	prefix, _, _ := strings.Cut(threeFiles, "diff --git a/new")
	h := newHarness(t, map[string]string{"a.txt": "one\ntwo\nthree\n", "fix.diff": prefix + binary})
	r := h.run("", "patch", "-p1", "-i", "fix.diff")
	if reject := toolrejection.AsToolReject(r.err); reject == nil || reject.Code != "COMMAND_NATIVE_FORM_UNSUPPORTED" || r.result.EffectCertainty != toolcontract.NoEffect {
		t.Fatalf("binary = %+v %v", r.result, r.err)
	}
	if h.read("a.txt") != "one\ntwo\nthree\n" || !h.absent("b.bin") {
		t.Fatal("a refused binary patch changed the tree")
	}
	for _, words := range [][]string{{"patch", "-b"}, {"patch", "--merge"}, {"patch", "-o", "out"}} {
		r := h.run(threeFiles, words...)
		if reject := toolrejection.AsToolReject(r.err); reject == nil || reject.Code != "COMMAND_OPTION_UNSUPPORTED" {
			t.Fatalf("%v = %v", words, r.err)
		}
	}
}

func TestPatchRefusesPathsOutsideTheAttachedFolders(t *testing.T) {
	account, err := user.LookupId(strconv.Itoa(os.Getuid()))
	testutil.FailErr(t, "look up account home", err)
	outside, err := os.MkdirTemp(account.HomeDir, ".paintedwolf-patch-outside-") //nolint:usetesting // Must lie outside OS temporary write roots.
	testutil.FailErr(t, "create outside fixture dir", err)
	t.Cleanup(func() { _ = os.RemoveAll(outside) })
	h := newHarness(t, map[string]string{"a.txt": "one\ntwo\nthree\n"})
	prefix, _, _ := strings.Cut(threeFiles, "diff --git a/new")
	diff := prefix + "--- /dev/null\n+++ " + outside + "/x.txt\n@@ -0,0 +1 @@\n+escape\n"
	r := h.run(diff, "patch", "-p0")
	if reject := toolrejection.AsToolReject(r.err); reject == nil || reject.Code != "SURVEY_PATH_ESCAPE" || r.result.Terminal != toolcontract.ProducerRefused {
		t.Fatalf("outside path = %+v %v", r.result, r.err)
	}
	if _, err := os.Stat(filepath.Join(outside, "x.txt")); !os.IsNotExist(err) {
		t.Fatalf("patch wrote outside: %v", err)
	}
	if h.read("a.txt") != "one\ntwo\nthree\n" {
		t.Fatal("a refused patch changed an inside file")
	}
}

func TestPatchOptionsFollowGNU(t *testing.T) {
	applied := strings.Replace(strings.Replace(tenLines, "two", "TWO", 1), "nine", "NINE", 1)
	cases := []struct {
		name           string
		files          map[string]string
		stdin          string
		words          []string
		status         int
		stdout, stderr string
		want           map[string]string
		absent         []string
	}{
		{name: "forward skips an applied patch", files: map[string]string{"f.txt": applied}, stdin: twoHunkDiff, words: []string{"-N", "-p1"}, status: 1,
			stdout: "patching file f.txt\n", stderr: "Reversed (or previously applied) patch detected!  Skipping patch.\n2 out of 2 hunks ignored\n", want: map[string]string{"f.txt": applied}},
		{name: "batch assumes reversed", files: map[string]string{"f.txt": applied}, stdin: twoHunkDiff, words: []string{"-t", "-p1"},
			stdout: "patching file f.txt\n", stderr: "Reversed (or previously applied) patch detected!  Assuming -R.\n", want: map[string]string{"f.txt": tenLines}},
		{name: "force applies forward and fails", files: map[string]string{"f.txt": applied}, stdin: twoHunkDiff, words: []string{"-f", "-p1"}, status: 1,
			stdout: "patching file f.txt\n", stderr: "Hunk #1 FAILED at 1.\nHunk #2 FAILED at 8.\n2 out of 2 hunks FAILED\n", want: map[string]string{"f.txt": applied}},
		{name: "quiet keeps errors only", files: map[string]string{"f.txt": tenLines}, stdin: twoHunkDiff, words: []string{"-s", "-p1"},
			want: map[string]string{"f.txt": applied}},
		{name: "directory", files: map[string]string{"sub/f.txt": tenLines}, stdin: twoHunkDiff, words: []string{"-d", "sub", "-p1"},
			stdout: "patching file f.txt\n", want: map[string]string{"sub/f.txt": applied}},
		{name: "strip zero", files: map[string]string{"a/f.txt": tenLines}, stdin: strings.ReplaceAll(twoHunkDiff, "b/f.txt", "a/f.txt"), words: []string{"-p0"},
			stdout: "patching file a/f.txt\n", want: map[string]string{"a/f.txt": applied}},
		{name: "default strip uses the base name", files: map[string]string{"f.txt": tenLines}, stdin: twoHunkDiff, words: nil,
			stdout: "patching file f.txt\n", want: map[string]string{"f.txt": applied}},
		{name: "origfile operand", files: map[string]string{"other.txt": tenLines}, stdin: twoHunkDiff, words: []string{"other.txt"},
			stdout: "patching file other.txt\n", want: map[string]string{"other.txt": applied}},
		{name: "patchfile operand", files: map[string]string{"f.txt": tenLines, "fix.diff": twoHunkDiff}, words: []string{"-u", "--no-backup-if-mismatch", "-p1", "f.txt", "fix.diff"},
			stdout: "patching file f.txt\n", want: map[string]string{"f.txt": applied}},
		{name: "fuzz", files: map[string]string{"f.txt": strings.Replace(tenLines, "one", "ONE", 1)}, stdin: twoHunkDiff, words: []string{"-p1"},
			stdout: "patching file f.txt\nHunk #1 succeeded at 1 with fuzz 1.\n", want: map[string]string{"f.txt": strings.Replace(applied, "one", "ONE", 1)}},
		{name: "remove empty files", files: map[string]string{"e.txt": "only\n"}, stdin: "--- a/e.txt\n+++ b/e.txt\n@@ -1 +0,0 @@\n-only\n", words: []string{"-E", "-p1"},
			stdout: "patching file e.txt\n", absent: []string{"e.txt"}},
		{name: "empty result kept without -E", files: map[string]string{"e.txt": "only\n"}, stdin: "--- a/e.txt\n+++ b/e.txt\n@@ -1 +0,0 @@\n-only\n", words: []string{"-p1"},
			stdout: "patching file e.txt\n", want: map[string]string{"e.txt": ""}},
		{name: "missing file", stdin: twoHunkDiff, words: []string{"-p1"}, status: 1,
			stdout: "patching file f.txt\n", stderr: "can't find file to patch: f.txt\nPerhaps you used the wrong -p or --strip option?\nNo file to patch.  Skipping patch.\n2 out of 2 hunks ignored\n"},
		{name: "creation over an existing file", files: map[string]string{"n.txt": "already\n"}, stdin: "--- /dev/null\n+++ b/n.txt\n@@ -0,0 +1 @@\n+new\n", words: []string{"-p1"}, status: 1,
			stdout: "patching file n.txt\n", stderr: "The next patch would create the file n.txt,\nwhich already exists!  Skipping patch.\n1 out of 1 hunk ignored\n", want: map[string]string{"n.txt": "already\n"}},
		{name: "deletion with leftover content", files: map[string]string{"d.txt": "keep\nbye\n"}, stdin: "--- a/d.txt\n+++ /dev/null\n@@ -2 +0,0 @@\n-bye\n", words: []string{"-p1"}, status: 1,
			stdout: "patching file d.txt\n", stderr: "Not deleting file d.txt as content differs from patch\n", want: map[string]string{"d.txt": "keep\n"}},
		{name: "only garbage", stdin: "no diff here\n", status: 2, stderr: "patch: **** Only garbage was found in the patch input.\n"},
		{name: "malformed", stdin: "--- a/f\n+++ b/f\n@@ -1,2 +1,2 @@\n-a\n", status: 2, stderr: "patch: **** malformed patch: gitdiff: line 3: fragment header miscounts lines: -1 old, -2 new\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.files)
			r := h.run(tc.stdin, append([]string{"patch"}, tc.words...)...)
			if r.err != nil || r.result.Status != tc.status || r.stdout != tc.stdout || r.stderr != tc.stderr {
				t.Fatalf("status=%d err=%v\nstdout=%q\nstderr=%q", r.result.Status, r.err, r.stdout, r.stderr)
			}
			for name, content := range tc.want {
				if got := h.read(name); got != content {
					t.Fatalf("%s = %q, want %q", name, got, content)
				}
			}
			for _, name := range tc.absent {
				if !h.absent(name) {
					t.Fatalf("%s still exists", name)
				}
			}
			h.noRejects()
		})
	}
}

func TestPatchGoldenProgramsReadStdinFilesAndHeredocs(t *testing.T) {
	catalog, err := toolcontract.LanguageV1()
	testutil.FailErr(t, "catalog", err)
	var contract struct {
		Golden []struct {
			ID      string `json:"id"`
			Program string `json:"program"`
			Setup   struct {
				Files map[string]json.RawMessage `json:"files"`
			} `json:"setup"`
			Expected struct {
				Stdout  string `json:"stdout"`
				Stderr  string `json:"stderr"`
				Status  int    `json:"status"`
				Effects []struct {
					Path  string `json:"path"`
					Bytes string `json:"bytes"`
				} `json:"semantic_effects"`
			} `json:"expected"`
		} `json:"golden_programs"`
	}
	testutil.FailErr(t, "golden", json.Unmarshal(catalog.Contracts["golden"], &contract))
	ran := 0
	for _, golden := range contract.Golden {
		if !strings.HasPrefix(golden.Program, "patch ") {
			continue
		}
		ran++
		t.Run(golden.ID, func(t *testing.T) {
			files := map[string]string{}
			for name, raw := range golden.Setup.Files {
				var content string
				testutil.FailErr(t, "golden file "+name, json.Unmarshal(raw, &content))
				files[name] = content
			}
			h := newHarness(t, files)
			stdout := &strings.Builder{}
			out, err := h.evaluateTo(golden.Program, stdout)
			if err != nil || out.Stop != nil || stdout.String() != golden.Expected.Stdout {
				t.Fatalf("evaluate = %v %+v stdout=%q", err, out.Stop, stdout.String())
			}
			for _, effect := range golden.Expected.Effects {
				if got := h.read(effect.Path); got != effect.Bytes {
					t.Fatalf("%s = %q, want %q", effect.Path, got, effect.Bytes)
				}
			}
		})
	}
	if ran < 3 {
		t.Fatalf("ran %d patch golden programs", ran)
	}
}

func TestPatchCatalogExampleRunsInItsFixtureCorpus(t *testing.T) {
	catalog, err := toolcontract.LanguageV1()
	testutil.FailErr(t, "catalog", err)
	for _, command := range catalog.Commands {
		if command.Name != "patch" {
			continue
		}
		var example struct {
			Program string                     `json:"program"`
			Stdout  string                     `json:"stdout"`
			Setup   map[string]json.RawMessage `json:"setup"`
		}
		testutil.FailErr(t, "example", json.Unmarshal(command.Example, &example))
		files := map[string]string{}
		for name, raw := range example.Setup {
			var content string
			if json.Unmarshal(raw, &content) == nil {
				files[name] = content
			}
		}
		h := newHarness(t, files)
		r := h.run("", strings.Fields(example.Program)...)
		if r.err != nil || r.result.Status != 0 || r.stdout != example.Stdout || h.read("input") != "ALPHA\nbeta" {
			t.Fatalf("example = %+v %v %q input=%q", r.result, r.err, r.stdout, h.read("input"))
		}
		return
	}
	t.Fatal("patch has no catalog entry")
}
