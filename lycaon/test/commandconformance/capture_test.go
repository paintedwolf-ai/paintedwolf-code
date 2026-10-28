package commandconformance

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/test/referenceuserland"
)

// fakeReferences answers each program with its own text on stdout, and records
// what each userland ran.
type fakeReferences struct {
	ran    map[referenceuserland.ID][]referenceuserland.Case
	answer func(referenceuserland.Case) referenceuserland.Result
	err    error
}

func (f *fakeReferences) Run(_ context.Context, id referenceuserland.ID, cases []referenceuserland.Case) ([]referenceuserland.Result, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.ran == nil {
		f.ran = map[referenceuserland.ID][]referenceuserland.Case{}
	}
	f.ran[id] = append(f.ran[id], cases...)
	results := make([]referenceuserland.Result, len(cases))
	for i, c := range cases {
		results[i] = f.answer(c)
	}
	return results, nil
}

func echoProgram(c referenceuserland.Case) referenceuserland.Result {
	files := map[string]*referenceuserland.Entry{}
	for _, name := range c.Tracked {
		files[name] = &referenceuserland.Entry{Mode: 0o644, Data: []byte(name)}
	}
	return referenceuserland.Result{Stdout: []byte(c.Program), Files: files}
}

func TestCaptureRunsEachCaseInItsUserlandsOnly(t *testing.T) {
	cases := []Case{
		{ID: "everywhere", Program: "true", References: allUserlands},
		{ID: "bsd-only", Program: "stat -f %z x", Tracked: []string{"x"}, References: bsdUserland},
		{ID: "git", Program: "git log", Git: true, References: gnuUserland},
	}
	seed := GitSeed{".git/HEAD": {Data: []byte("ref: refs/heads/main\n")}}
	refs := &fakeReferences{answer: echoProgram}
	captured, err := Capture(t.Context(), refs, cases, seed)
	testutil.FailErr(t, "capture", err)

	if len(refs.ran[referenceuserland.GNU]) != 2 || len(refs.ran[referenceuserland.BSD]) != 2 || len(refs.ran[referenceuserland.BusyBox]) != 1 {
		t.Fatalf("runs per userland = %v", refs.ran)
	}
	if got := refs.ran[referenceuserland.BusyBox][0].Program; got != "set -o pipefail\ntrue" {
		t.Fatalf("busybox program = %q", got)
	}
	gitRun := refs.ran[referenceuserland.GNU][1]
	if gitRun.Program != "set -o pipefail\nshopt -s globstar\ngit log" || gitRun.Workspace[".git/HEAD"].Data == nil || gitRun.Workspace["notes.txt"].Data == nil {
		t.Fatalf("git case ran as %+v", gitRun)
	}
	if _, seeded := refs.ran[referenceuserland.GNU][0].Workspace[".git/HEAD"]; seeded {
		t.Fatal("a case without Git was given the repository")
	}
	bsd := captured["bsd-only"][referenceuserland.BSD]
	if bsd.Input != cases[1].Input(referenceuserland.BSD, seed) || string(bsd.Files["x"].Data) != "x" || bsd.Files["x"].Mode != 0o644 {
		t.Fatalf("bsd snapshot = %+v", bsd)
	}
	if len(captured["bsd-only"]) != 1 || len(captured["everywhere"]) != 3 || captured["everywhere"][referenceuserland.GNU].Files != nil {
		t.Fatalf("captured = %+v", captured)
	}

	failing := &fakeReferences{err: errors.New("no daemon")}
	if _, err := Capture(t.Context(), failing, cases, seed); err == nil {
		t.Fatal("capture hid a runner failure")
	}
}

func TestReproducesReportsEveryChangedResult(t *testing.T) {
	stored := map[string]map[referenceuserland.ID]Snapshot{
		"a": {referenceuserland.GNU: {Input: "i", Stdout: Text("x\n")}, referenceuserland.BSD: {Input: "i", Status: 1}},
		"b": {referenceuserland.GNU: {Input: "i", Stderr: Text("e\n")}},
	}
	captured := map[string]map[referenceuserland.ID]Snapshot{
		"a": {referenceuserland.GNU: {Input: "i", Stdout: Text("x\n")}, referenceuserland.BSD: {Input: "i", Status: 2}},
		"b": {referenceuserland.GNU: {Input: "j", Stderr: Text("e\n")}},
		"c": {referenceuserland.GNU: {Input: "i"}},
	}
	want := []string{
		"a in bsd: status 1, now 2",
		"b in gnu: input i, captured from j",
		"c in gnu: no stored snapshot",
	}
	if got := Reproduces(stored, captured); !slices.Equal(got, want) {
		t.Fatalf("Reproduces = %q, want %q", got, want)
	}
	if got := Reproduces(stored, map[string]map[referenceuserland.ID]Snapshot{"a": stored["a"]}); len(got) != 0 {
		t.Fatalf("an identical capture reported %q", got)
	}
}

func TestCaptureGitSeedRecordsTheRepositoryTree(t *testing.T) {
	var archive bytes.Buffer
	w := tar.NewWriter(&archive)
	testutil.FailErr(t, "write dir", w.WriteHeader(&tar.Header{Name: ".git/", Typeflag: tar.TypeDir, Mode: 0o755}))
	testutil.FailErr(t, "write head", w.WriteHeader(&tar.Header{Name: ".git/HEAD", Typeflag: tar.TypeReg, Mode: 0o644, Size: 4}))
	_, err := w.Write([]byte("main"))
	testutil.FailErr(t, "write head data", err)
	testutil.FailErr(t, "close archive", w.Close())

	refs := &fakeReferences{answer: func(c referenceuserland.Case) referenceuserland.Result {
		return referenceuserland.Result{Stdout: []byte(base64.StdEncoding.EncodeToString(archive.Bytes()))}
	}}
	files, err := CaptureGitSeed(t.Context(), refs)
	testutil.FailErr(t, "capture the Git seed", err)
	if len(files) != 2 || files[".git"].Mode != Mode(fs.ModeDir|0o755) || string(files[".git/HEAD"].Data) != "main" {
		t.Fatalf("seed = %+v", files)
	}
	if ran := refs.ran[referenceuserland.GNU]; len(ran) != 1 || !strings.Contains(ran[0].Program, "git commit") {
		t.Fatalf("seed program ran as %+v", ran)
	}

	symlinkArchive := tarOf(t, &tar.Header{Name: ".git/link", Typeflag: tar.TypeSymlink, Linkname: "HEAD"})
	failing := []func(referenceuserland.Case) referenceuserland.Result{
		func(referenceuserland.Case) referenceuserland.Result {
			return referenceuserland.Result{Status: 128, Stderr: []byte("fatal")}
		},
		func(referenceuserland.Case) referenceuserland.Result {
			return referenceuserland.Result{Stdout: []byte("not base64!")}
		},
		func(referenceuserland.Case) referenceuserland.Result {
			return referenceuserland.Result{Stdout: []byte(symlinkArchive)}
		},
	}
	for i, answer := range failing {
		if _, err := CaptureGitSeed(t.Context(), &fakeReferences{answer: answer}); err == nil {
			t.Errorf("failing seed %d was recorded", i)
		}
	}
}

func tarOf(t *testing.T, header *tar.Header) string {
	t.Helper()
	var archive bytes.Buffer
	w := tar.NewWriter(&archive)
	testutil.FailErr(t, "write header", w.WriteHeader(header))
	testutil.FailErr(t, "close archive", w.Close())
	return base64.StdEncoding.EncodeToString(archive.Bytes())
}

func TestValidateRefusesAnIncomparableCorpus(t *testing.T) {
	testutil.FailErr(t, "an ordinary program", Validate([]Case{{ID: "a", References: gnuUserland, Program: "printf 'information\\n' | grep -c info.txt"}}))
	for name, cases := range map[string][]Case{
		"duplicate":  {{ID: "a", References: gnuUserland}, {ID: "a", References: gnuUserland}},
		"no id":      {{References: gnuUserland}},
		"no refs":    {{ID: "a"}},
		"unknown":    {{ID: "a", References: []referenceuserland.ID{"macos"}}},
		"capability": {{ID: "a", References: gnuUserland, CapabilityRequest: "{"}},
		"help":       {{ID: "a", References: gnuUserland, Program: "sed --help | head -n 1"}},
		"version":    {{ID: "a", References: gnuUserland, Program: "grep -c x f; ls '--version'"}},
		"manual":     {{ID: "a", References: gnuUserland, Program: "true && man sed"}},
	} {
		if err := Validate(cases); err == nil {
			t.Errorf("%s corpus validated", name)
		}
	}
}
