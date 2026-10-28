package patchapply

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

const twoHunks = `--- a/f.txt
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

const original = "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n"

func parseOne(t *testing.T, diff string) File {
	t.Helper()
	patch, err := Parse([]byte(diff))
	testutil.FailErr(t, "parse", err)
	if len(patch.Files) != 1 {
		t.Fatalf("parsed %d files, want 1", len(patch.Files))
	}
	return patch.Files[0]
}

func TestApplyTextLocatesHunksExactlyByOffsetAndWithFuzz(t *testing.T) {
	file := parseOne(t, twoHunks)
	cases := []struct {
		name, src, want string
		lines, offsets  []int
		fuzz            []int
	}{
		{"exact", original, strings.Replace(strings.Replace(original, "two", "TWO", 1), "nine", "NINE", 1), []int{1, 8}, []int{0, 0}, []int{0, 0}},
		{"offset", "zero\nzero\n" + original, "zero\nzero\n" + strings.Replace(strings.Replace(original, "two", "TWO", 1), "nine", "NINE", 1), []int{3, 10}, []int{2, 2}, []int{0, 0}},
		{"fuzz", strings.Replace(original, "one", "ONE", 1), strings.Replace(strings.Replace(strings.Replace(original, "one", "ONE", 1), "two", "TWO", 1), "nine", "NINE", 1), []int{1, 8}, []int{0, 0}, []int{1, 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, err := file.Apply([]byte(tc.src), Options{Fuzz: 2})
			testutil.FailErr(t, "apply", err)
			if text.Failed != 0 || string(text.Content) != tc.want {
				t.Fatalf("failed=%d content=%q", text.Failed, text.Content)
			}
			for i, hunk := range text.Hunks {
				if !hunk.Applied || hunk.Line != tc.lines[i] || hunk.Offset != tc.offsets[i] || hunk.Fuzz != tc.fuzz[i] {
					t.Fatalf("hunk %d = %+v", i+1, hunk)
				}
			}
		})
	}
}

func TestApplyTextReportsFailedHunkAndAppliesTheRest(t *testing.T) {
	file := parseOne(t, twoHunks)
	src := strings.Replace(original, "nine", "nine changed", 1)
	text, err := file.Apply([]byte(src), Options{Fuzz: 0})
	testutil.FailErr(t, "apply", err)
	if text.Failed != 1 || !text.Hunks[0].Applied || text.Hunks[1].Applied {
		t.Fatalf("hunks = %+v", text.Hunks)
	}
	if text.Hunks[1].Header != "@@ -8,3 +8,3 @@" || text.Hunks[1].Line != 8 || text.Hunks[1].Reason == "" {
		t.Fatalf("failed hunk = %+v", text.Hunks[1])
	}
	if string(text.Content) != strings.Replace(src, "two", "TWO", 1) {
		t.Fatalf("content = %q", text.Content)
	}
	failures := text.Failures("f.txt")
	if len(failures) != 1 || failures[0] != (Failure{File: "f.txt", Hunk: 2, Header: "@@ -8,3 +8,3 @@", Reason: "context does not match"}) {
		t.Fatalf("failures = %+v", failures)
	}
}

func TestReverseUndoesAnAppliedPatchExactly(t *testing.T) {
	file := parseOne(t, twoHunks)
	forward, err := file.Apply([]byte(original), Options{})
	testutil.FailErr(t, "forward", err)
	reversed, err := Reverse(file)
	testutil.FailErr(t, "reverse", err)
	back, err := reversed.Apply(forward.Content, Options{})
	testutil.FailErr(t, "back", err)
	if string(back.Content) != original || back.Failed != 0 {
		t.Fatalf("reverse = %q failed=%d", back.Content, back.Failed)
	}
	if !Applies(forward.Content, reversed.TextFragments, Options{}) || Applies([]byte(original), reversed.TextFragments, Options{}) {
		t.Fatal("reversed detection disagrees with the applied state")
	}
}

func TestApplyTextKeepsMissingFinalNewline(t *testing.T) {
	file := parseOne(t, "--- a/f\n+++ b/f\n@@ -1,2 +1,2 @@\n-alpha\n+ALPHA\n beta\n\\ No newline at end of file\n")
	text, err := file.Apply([]byte("alpha\nbeta"), Options{})
	testutil.FailErr(t, "apply", err)
	if string(text.Content) != "ALPHA\nbeta" || text.Failed != 0 {
		t.Fatalf("content = %q", text.Content)
	}
}

func TestParseNamesFollowEachSectionsConvention(t *testing.T) {
	diff := "--- old/dir/a.txt\t2026-01-01 00:00:00\n+++ new/dir/a.txt\t2026-01-01 00:00:00\n@@ -1 +1 @@\n-a\n+b\n" +
		"diff --git a/src/b.txt b/src/b.txt\nindex 1111111..2222222 100644\n--- a/src/b.txt\n+++ b/src/b.txt\n@@ -1 +1 @@\n-a\n+b\n" +
		"diff --git a/gone.txt b/gone.txt\ndeleted file mode 100644\nindex 1111111..0000000\n--- a/gone.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-a\n" +
		"diff --git a/old.txt b/renamed.txt\nsimilarity index 100%\nrename from old.txt\nrename to renamed.txt\n"
	patch, err := Parse([]byte(diff))
	testutil.FailErr(t, "parse", err)
	type names struct{ old, new string }
	want := map[int][]names{
		0: {{"new/dir/a.txt", "new/dir/a.txt"}, {"a/src/b.txt", "b/src/b.txt"}, {"a/gone.txt", ""}, {"old.txt", "renamed.txt"}},
		1: {{"dir/a.txt", "dir/a.txt"}, {"src/b.txt", "src/b.txt"}, {"gone.txt", ""}, {"old.txt", "renamed.txt"}},
		2: {{"a.txt", "a.txt"}, {"b.txt", "b.txt"}, {"", ""}, {"", ""}},
	}
	for strip, expected := range want {
		for i, file := range patch.Files {
			old, new, err := file.Names(strip)
			if strip == 2 && i >= 2 {
				if err == nil {
					t.Fatalf("-p2 on %s accepted a name with too few components: %q %q", file.NewName, old, new)
				}
				continue
			}
			testutil.FailErr(t, "names", err)
			if (names{old, new}) != expected[i] {
				t.Fatalf("-p%d file %d = %q %q, want %+v", strip, i, old, new, expected[i])
			}
		}
	}
	if patch.Files[0].Git || !patch.Files[1].Git || !patch.Files[3].IsRename {
		t.Fatalf("section conventions = %+v", patch.Files)
	}
}

func TestParseRefusesInputWithoutSections(t *testing.T) {
	if _, err := Parse([]byte("just prose\n")); !errors.Is(err, ErrNoPatch) {
		t.Fatalf("prose parsed: %v", err)
	}
	if _, err := Parse([]byte("@@ -1 +1 @@\n-a\n+b\n")); err == nil || errors.Is(err, ErrNoPatch) {
		t.Fatalf("headerless hunk = %v", err)
	}
}

// Generated with git diff --binary: a new file holding 00 01 02 03, and a
// 2048-byte file whose bytes 100 and 1500 change, which Git encodes as delta.
const (
	literalPatch = "diff --git a/b.bin b/b.bin\nnew file mode 100644\nindex 0000000000000000000000000000000000000000..eaf36c1daccfdf325514461cd1a2ffbc139b5464\nGIT binary patch\nliteral 4\nLcmZQzWMT#Y01f~L\n\nliteral 0\nHcmV?d00001\n\n"
	deltaPatch   = "diff --git a/d.bin b/d.bin\nindex d37d08ab50b80f6d09dc129b2e8df68056d75971..528baabc7e45b06c555ce6033c3d87dc3938d283 100644\nGIT binary patch\ndelta 25\nhcmZn=Xb_l?!uWr4Aj^8@&HSt<7#SHhe`Hi<0swPj2r2*o\n\ndelta 21\nbcmZn=Xb_l?vN?dIhjH@<#y8ABLWKzcS$PNs\n\n"
)

func deltaSource() []byte {
	data := make([]byte, 2048)
	for i := range data {
		data[i] = byte((i * 7) % 251)
	}
	return data
}

func TestBinaryPatchAppliesLiteralAndDeltaAndReverses(t *testing.T) {
	created := parseOne(t, literalPatch)
	out, err := ApplyBinary(nil, created)
	testutil.FailErr(t, "literal", err)
	if string(out) != "\x00\x01\x02\x03" || !created.IsBinary || !created.IsNew {
		t.Fatalf("literal = %x", out)
	}
	reversed, err := Reverse(created)
	testutil.FailErr(t, "reverse literal", err)
	if back, err := ApplyBinary(out, reversed); err != nil || len(back) != 0 || !reversed.IsDelete {
		t.Fatalf("reverse literal = %x %v delete=%v", back, err, reversed.IsDelete)
	}

	delta := parseOne(t, deltaPatch)
	src := deltaSource()
	want := append([]byte(nil), src...)
	want[100], want[1500] = 255, 0
	text, err := delta.Apply(src, Options{})
	testutil.FailErr(t, "delta", err)
	if string(text.Content) != string(want) {
		t.Fatal("delta result differs from the patched bytes")
	}
	undo, err := Reverse(delta)
	testutil.FailErr(t, "reverse delta", err)
	if back, err := ApplyBinary(text.Content, undo); err != nil || string(back) != string(src) {
		t.Fatalf("reverse delta: %v", err)
	}
	if _, err := ApplyBinary([]byte("not the preimage"), delta); err == nil {
		t.Fatal("delta applied to the wrong preimage")
	}

	stripped := *delta.File
	stripped.ReverseBinaryFragment = nil
	if _, err := Reverse(File{File: &stripped, Git: true}); !errors.Is(err, ErrNoReverseBinary) {
		t.Fatalf("reverse without data = %v", err)
	}
}

func TestEvidenceRefFitsAndCountsWhatItDrops(t *testing.T) {
	var failures []Failure
	for i := 1; i <= 20; i++ {
		failures = append(failures, Failure{File: "dir/file.txt", Hunk: i, Header: "@@ -10,7 +10,8 @@ func example()", Reason: "context does not match"})
	}
	ref := EvidenceRef(failures)
	if len(ref) > EvidenceBytes {
		t.Fatalf("evidence is %d bytes", len(ref))
	}
	var decoded struct {
		Failed []Failure `json:"failed"`
		More   int       `json:"more"`
	}
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(ref), &decoded))
	if len(decoded.Failed) == 0 || len(decoded.Failed)+decoded.More != 20 || decoded.Failed[0] != failures[0] {
		t.Fatalf("evidence = %s", ref)
	}
}

func TestModeAndSymlinkFacts(t *testing.T) {
	cases := []struct {
		diff    string
		mode    string
		symlink bool
	}{
		{"diff --git a/s.sh b/s.sh\nold mode 100644\nnew mode 100755\n", "755", false},
		{"diff --git a/s.sh b/s.sh\nold mode 100755\nnew mode 100644\n", "644", false},
		{"diff --git a/n.sh b/n.sh\nnew file mode 100755\nindex 0000000..1111111\n--- /dev/null\n+++ b/n.sh\n@@ -0,0 +1 @@\n+x\n", "755", false},
		{"diff --git a/n.txt b/n.txt\nnew file mode 100644\nindex 0000000..1111111\n--- /dev/null\n+++ b/n.txt\n@@ -0,0 +1 @@\n+x\n", "", false},
		{"diff --git a/l b/l\nnew file mode 120000\nindex 0000000..1111111\n--- /dev/null\n+++ b/l\n@@ -0,0 +1 @@\n+target\n\\ No newline at end of file\n", "", true},
	}
	for _, tc := range cases {
		file := parseOne(t, tc.diff)
		if file.ModeChange() != tc.mode || file.Symlink() != tc.symlink {
			t.Fatalf("%q: mode=%q symlink=%v", tc.diff, file.ModeChange(), file.Symlink())
		}
	}
}

func TestParseLocatesErrorsInLaterSections(t *testing.T) {
	diff := "diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1 +1 @@\n-a\n+b\n" + "diff --git a/c b/c\n--- a/c\n+++ b/c\n@@ -1,2 +1,2 @@\n-a\n"
	_, err := Parse([]byte(diff))
	if err == nil || !strings.HasPrefix(err.Error(), "in the section starting at line 7: ") {
		t.Fatalf("error = %v", err)
	}
}
