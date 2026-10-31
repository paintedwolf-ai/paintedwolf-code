//go:build darwin || linux

package projectpaths_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// pathRun is one path utility invocation's streams and settlement.
type pathRun struct {
	stdout, stderr string
	result         toolcontract.ProducerResult
	err            error
}

func runPathUtility(t *testing.T, p *projectpaths.ProgramPaths, name string, args ...string) pathRun {
	t.Helper()
	var stdout, stderr bytes.Buffer
	result, err := p.Invoke(t.Context(), commandprogram.OperationRequest{OperationID: name, Argv: pathArgv(name, args...)}, commandprogram.Streams{Output: &stdout, Error: &stderr})
	testutil.FailErr(t, "validate producer result", result.Validate())
	return pathRun{stdout: stdout.String(), stderr: stderr.String(), result: result, err: err}
}

func TestRealpathCanonicalizesWithGNUOptions(t *testing.T) {
	tc, root := readSessionContext(t)
	testutil.FailErr(t, "seed src/a/b", os.MkdirAll(filepath.Join(root, "src", "a", "b"), 0o755))
	testutil.FailErr(t, "seed link", os.Symlink("src/a", filepath.Join(root, "alink")))
	p, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "bind program cwd", err)
	canonical := fspath.CanonicalPath(root)
	for _, c := range []struct {
		args           []string
		stdout, stderr string
		status         int
	}{
		{[]string{"--relative-to=src", "src/a/b"}, "a/b\n", "", 0},
		{[]string{"--relative-to", "src/a/b", "src", "a.go"}, "../..\n../../../a.go\n", "", 0},
		{[]string{"--relative-to=src/a", "src/a"}, ".\n", "", 0},
		{[]string{"--relative-base=src", "src/a/b", "a.go"}, "a/b\n" + filepath.Join(canonical, "a.go") + "\n", "", 0},
		{[]string{"--relative-to=src/a", "--relative-base=src", "src/a/b", "pkg"}, "b\n" + filepath.Join(canonical, "pkg") + "\n", "", 0},
		{[]string{"--relative-to=pkg", "--relative-base=src", "src/a"}, filepath.Join(canonical, "src", "a") + "\n", "", 0},
		{[]string{"alink/b"}, filepath.Join(canonical, "src", "a", "b") + "\n", "", 0},
		{[]string{"-s", "alink/b"}, filepath.Join(canonical, "alink", "b") + "\n", "", 0},
		{[]string{"-s", "nope/x"}, filepath.Join(canonical, "nope", "x") + "\n", "", 0},
		{[]string{"-se", "nope/x"}, "", "realpath: nope/x: No such file or directory\n", 1},
		{[]string{"new.txt"}, filepath.Join(canonical, "new.txt") + "\n", "", 0},
		{[]string{"missing/x", "a.go"}, filepath.Join(canonical, "a.go") + "\n", "realpath: missing/x: No such file or directory\n", 1},
		{[]string{"-q", "missing/x"}, "", "", 1},
		{[]string{"-e", "new.txt"}, "", "realpath: new.txt: No such file or directory\n", 1},
		{[]string{"--canonicalize-missing", "missing/x/../y", "alink/../pkg"}, filepath.Join(canonical, "missing", "y") + "\n" + filepath.Join(canonical, "src", "pkg") + "\n", "", 0},
		{[]string{"a.go/x"}, "", "realpath: a.go/x: Not a directory\n", 1},
		{[]string{"-m", "a.go/x"}, filepath.Join(canonical, "a.go", "x") + "\n", "", 0},
		{[]string{"-e", "a.go/x"}, "", "realpath: a.go/x: Not a directory\n", 1},
		{[]string{"-z", "a.go", "pkg"}, filepath.Join(canonical, "a.go") + "\x00" + filepath.Join(canonical, "pkg") + "\x00", "", 0},
		{[]string{"--relative-to=missing/x", "a.go"}, "", "realpath: missing/x: No such file or directory\n", 1},
	} {
		r := runPathUtility(t, p, "realpath", c.args...)
		if r.err != nil || r.stdout != c.stdout || r.stderr != c.stderr || r.result.Status != c.status || !r.result.Complete || r.result.PublicationEligible != (c.status == 0) {
			t.Errorf("realpath %q: stdout=%q stderr=%q result=%+v err=%v; want %q %q %d", c.args, r.stdout, r.stderr, r.result, r.err, c.stdout, c.stderr, c.status)
		}
	}
}

func TestReadlinkCanonicalModesAndQuietFailures(t *testing.T) {
	tc, root := readSessionContext(t)
	testutil.FailErr(t, "seed link", os.Symlink("pkg", filepath.Join(root, "plink")))
	p, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "bind program cwd", err)
	canonical := fspath.CanonicalPath(root)
	for _, c := range []struct {
		args   []string
		stdout string
		status int
	}{
		{[]string{"plink"}, "pkg\n", 0},
		{[]string{"a.go"}, "", 1},
		{[]string{"-f", "plink/new.go"}, filepath.Join(canonical, "pkg", "new.go") + "\n", 0},
		{[]string{"-f", "missing/x"}, "", 1},
		{[]string{"-e", "plink/b.go"}, filepath.Join(canonical, "pkg", "b.go") + "\n", 0},
		{[]string{"-e", "plink/new.go"}, "", 1},
		{[]string{"-m", "missing/x", "plink"}, filepath.Join(canonical, "missing", "x") + "\n" + filepath.Join(canonical, "pkg") + "\n", 0},
	} {
		r := runPathUtility(t, p, "readlink", c.args...)
		if r.err != nil || r.stdout != c.stdout || r.stderr != "" || r.result.Status != c.status {
			t.Errorf("readlink %q: stdout=%q stderr=%q result=%+v err=%v; want %q %d", c.args, r.stdout, r.stderr, r.result, r.err, c.stdout, c.status)
		}
	}
}

func TestBasenameStripsSuffixesOverSeveralNames(t *testing.T) {
	tc, _ := readSessionContext(t)
	p, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "bind program cwd", err)
	for _, c := range []struct {
		args   []string
		stdout string
	}{
		{[]string{"-s", ".go", "a.go", "dir/b.go", "c.txt", ".go"}, "a\nb\nc.txt\n.go\n"},
		{[]string{"--suffix=.tar.gz", "x/release.tar.gz"}, "release\n"},
		{[]string{"-a", "x/y/", "z"}, "y\nz\n"},
		{[]string{"--multiple", "-z", "x/y", "z"}, "y\x00z\x00"},
		{[]string{"-as", ".go", "a.go"}, "a\n"},
		{[]string{"dir/file.txt", ".txt"}, "file\n"},
		{[]string{"/"}, "/\n"},
	} {
		r := runPathUtility(t, p, "basename", c.args...)
		if r.err != nil || r.stdout != c.stdout || r.result.Status != 0 {
			t.Errorf("basename %q: stdout=%q result=%+v err=%v; want %q", c.args, r.stdout, r.result, r.err, c.stdout)
		}
	}
	for _, args := range [][]string{{}, {"-a"}, {"a", "b", "c"}} {
		r := runPathUtility(t, p, "basename", args...)
		var reject *toolrejection.ToolReject
		if !errors.As(projectpaths.WithPathRejection(r.err, toolcontract.SourceSpan{}), &reject) || reject.Code != "COMMAND_OPERAND_INVALID" || r.stdout != "" {
			t.Errorf("basename %q: result=%+v err=%v", args, r.result, r.err)
		}
	}
}
