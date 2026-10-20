//go:build darwin || linux

package projectpaths_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestProgramPathHelpersUseActualEntries(t *testing.T) {
	tc, root := readSessionContext(t)
	p, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "bind program cwd", err)
	testutil.FailErr(t, "seed exact file", os.WriteFile(filepath.Join(root, " trailing \n"), []byte("exact"), 0600))
	testutil.FailErr(t, "seed contained link", os.Symlink(" trailing \n", filepath.Join(root, "link")))
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"basename", []string{"--", "dir/file.txt", ".txt"}, "file\n"},
		{"basename", []string{""}, "\n"},
		{"dirname", []string{"--", "./pkg/file.go"}, "pkg\n"},
		{"readlink", []string{"--", "link"}, " trailing \n\n"},
		{"readlink", []string{"-f", "--", "link"}, filepath.Join(root, " trailing \n") + "\n"},
		{"realpath", []string{"--", "link"}, filepath.Join(root, " trailing \n") + "\n"},
		{"native_path", []string{"-0", "--", "@ws/ trailing \n"}, filepath.Join(root, " trailing \n") + "\x00"},
	}
	for _, c := range cases {
		t.Run(c.name+"/"+c.want, func(t *testing.T) {
			var output bytes.Buffer
			result, err := p.Invoke(t.Context(), commandprogram.OperationRequest{OperationID: c.name, Arguments: c.args}, commandprogram.Streams{Output: &output})
			testutil.FailErr(t, "invoke path helper", err)
			if result.Status != 0 || !result.Complete || !result.PublicationEligible || output.String() != c.want {
				t.Fatalf("result=%+v output=%q want=%q", result, output.String(), c.want)
			}
		})
	}
}

func TestProgramPathsKeepCwdInvocationLocal(t *testing.T) {
	tc, root := readSessionContext(t)
	before, err := os.Getwd()
	testutil.FailErr(t, "get host cwd", err)
	first, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "first invocation", err)
	second, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "second invocation", err)
	testutil.FailErr(t, "change logical cwd", first.Chdir(t.Context(), "pkg"))
	bound, err := first.Bind(t.Context(), "b.go", false)
	testutil.FailErr(t, "bind relative path", err)
	if bound.Resolved.Abs != filepath.Join(root, "pkg", "b.go") || second.Cwd().Abs != root {
		t.Fatalf("cwd leaked: first=%+v second=%+v", bound, second.Cwd())
	}
	after, err := os.Getwd()
	testutil.FailErr(t, "get unchanged host cwd", err)
	if after != before {
		t.Fatalf("host cwd changed from %q to %q", before, after)
	}
}

func TestProgramPathBindingRefusesLinkSwapAtEffect(t *testing.T) {
	tc, root := readSessionContext(t)
	p, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "bind invocation", err)
	testutil.FailErr(t, "create link", os.Symlink("a.go", filepath.Join(root, "selected")))
	bound, err := p.Bind(t.Context(), "selected", false)
	testutil.FailErr(t, "bind selected path", err)
	outside := filepath.Join(t.TempDir(), "secret")
	testutil.FailErr(t, "seed outside", os.WriteFile(outside, []byte("must not read"), 0600))
	testutil.FailErr(t, "replace old link", os.Remove(filepath.Join(root, "selected")))
	testutil.FailErr(t, "swap escaping link", os.Symlink(outside, filepath.Join(root, "selected")))
	if file, err := bound.Open(); err == nil {
		file.Close()
		t.Fatal("escaped read admitted after link swap")
	}
}

func TestProgramPathBindingRefusesReplacedRoot(t *testing.T) {
	tc, root := readSessionContext(t)
	p, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "bind invocation", err)
	bound, err := p.Bind(t.Context(), "a.go", false)
	testutil.FailErr(t, "bind file", err)
	displaced := root + "-displaced"
	testutil.FailErr(t, "displace root", os.Rename(root, displaced))
	t.Cleanup(func() { _ = os.RemoveAll(root); _ = os.Rename(displaced, root) })
	testutil.FailErr(t, "replace root", os.Mkdir(root, 0700))
	testutil.FailErr(t, "plant replacement", os.WriteFile(filepath.Join(root, "a.go"), []byte("replacement"), 0600))
	if err := bound.Validate(); err == nil {
		t.Fatal("replacement root gained old binding")
	}
	if file, err := bound.Open(); err == nil {
		file.Close()
		t.Fatal("replacement root read through old binding")
	}
}

func TestProgramPathConversionDoesNotRecordOrGrantMutation(t *testing.T) {
	tc, root, recorder := scratchToolContext(t)
	p, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "bind invocation", err)
	converted, err := p.NativePaths(t.Context(), []string{"./new file"}, true)
	testutil.FailErr(t, "convert path", err)
	if !bytes.Equal(converted, append([]byte(filepath.Join(tc.ActiveRootPath(), "new file")), 0)) {
		t.Fatalf("converted=%q", converted)
	}
	if len(recorder.mutations) != 0 || len(tc.Files.GrantedWriteRoots) != 0 {
		t.Fatal("conversion modified authority or mutation history")
	}
	tmp, err := p.TemporaryDirectory(t.Context())
	testutil.FailErr(t, "session temporary directory", err)
	if tmp != root {
		t.Fatalf("temporary directory=%q want %q", tmp, root)
	}
}

func TestProgramNullOperandIsTypedAndEmpty(t *testing.T) {
	tc, _ := readSessionContext(t)
	p, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "bind invocation", err)
	bound, err := p.Bind(t.Context(), "/dev/null", true)
	testutil.FailErr(t, "bind typed null", err)
	if !bound.Null || bound.Resolved.Abs != "" {
		t.Fatal("null became an ordinary path")
	}
	reader, err := bound.OpenReader()
	testutil.FailErr(t, "open null input", err)
	defer reader.Close()
	bytes, err := io.ReadAll(reader)
	testutil.FailErr(t, "read null input", err)
	if len(bytes) != 0 {
		t.Fatalf("null produced %q", bytes)
	}
	for _, literal := range []string{"./NUL", "./dev/null", "https://example.test/dev/null"} {
		if projectpaths.IsNullOperand(literal) {
			t.Fatalf("literal %q treated as device", literal)
		}
	}
}

func TestProgramPathPreservesNonUnicodeNulLists(t *testing.T) {
	tc, root := readSessionContext(t)
	name := string([]byte{'n', 0xff})
	testutil.FailErr(t, "seed native byte name", os.WriteFile(filepath.Join(root, name), []byte("native bytes"), 0600))
	p, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "bind invocation", err)
	got, err := p.NativePaths(t.Context(), []string{name}, true)
	testutil.FailErr(t, "convert native byte list", err)
	if !bytes.Equal(got, append([]byte(filepath.Join(root, name)), 0)) {
		t.Fatalf("lost native byte name: %q", got)
	}
	if _, err := p.NativePaths(t.Context(), []string{name}, false); err == nil {
		t.Fatal("unrepresentable scalar accepted")
	}
}

func TestProgramCanonicalAllowsOnlyMissingFinalLeaf(t *testing.T) {
	tc, root := readSessionContext(t)
	p, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "bind invocation", err)
	got, err := p.Canonical(t.Context(), "pkg/new.go")
	testutil.FailErr(t, "canonical absent leaf", err)
	if got != filepath.Join(root, "pkg", "new.go") {
		t.Fatalf("canonical=%q", got)
	}
	if _, err := p.Canonical(t.Context(), "missing/new.go"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing parent=%v", err)
	}
	testutil.FailErr(t, "seed dangling link", os.Symlink("pkg/new.go", filepath.Join(root, "dangling")))
	got, err = p.Canonical(t.Context(), "dangling")
	testutil.FailErr(t, "canonical dangling final target", err)
	if got != filepath.Join(root, "pkg", "new.go") {
		t.Fatalf("dangling canonical=%q", got)
	}
}

func TestProgramPathsAuthorizeActualLinkIdentity(t *testing.T) {
	tc, root := readSessionContext(t)
	testutil.FailErr(t, "seed denied target", os.WriteFile(filepath.Join(root, "secret"), []byte("private"), 0600))
	testutil.FailErr(t, "seed allowed spelling", os.Symlink("../secret", filepath.Join(root, "pkg", "alias")))
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: toolprofiles.DefaultToolProfileID, Tools: map[string]bool{"read": true}, ReadGlobs: []string{"pkg", "pkg/**"}}})
	p, err := projectpaths.NewProgramPaths(t.Context(), boundary, tc, "pkg")
	testutil.FailErr(t, "bind allowed invocation directory", err)
	bound, err := p.Bind(t.Context(), "alias", false)
	testutil.FailErr(t, "bind coordinates without authority", err)
	if file, err := bound.Open(); err == nil {
		file.Close()
		t.Fatal("allowed spelling widened actual target read authority")
	}
	if _, err := p.Canonical(t.Context(), "alias"); err == nil {
		t.Fatal("canonical metadata bypassed actual target authority")
	}
	if _, err := p.LinkTarget(t.Context(), "../secret"); err == nil {
		t.Fatal("link metadata bypassed entry authority")
	}
}
