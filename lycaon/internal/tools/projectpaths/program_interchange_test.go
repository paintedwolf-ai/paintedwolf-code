//go:build darwin || linux

package projectpaths_test

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	hostexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"strings"
)

// This real process fixture establishes physical identity and argv/environment
// interchange. It is not installed-platform or confinement qualification.
func TestProgramPathSharesActualTemporaryBytesWithAProcess(t *testing.T) {
	tc, scratch, _ := scratchToolContext(t)
	p, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "bind invocation", err)
	native, err := p.Bind(t.Context(), "@scratch/native file\n", true)
	testutil.FailErr(t, "bind native destination", err)
	_, err = fseffect.Replace(fseffect.ReplaceRequest{Location: native.Resolved.EffectLocation(), Source: strings.NewReader("native bytes"), Mode: 0600})
	testutil.FailErr(t, "publish native fixture", err)
	self, err := os.Executable()
	testutil.FailErr(t, "locate fixture helper", err)
	output, code, err := hostexec.Run(t.Context(), self, []string{"-test.run=^TestProgramPathProcessHelper$", "--", native.Resolved.Abs}, hostexec.ExecOpts{
		Launch: hostexec.HostLaunch("path interchange fixture"), Dir: scratch, Timeout: 10 * time.Second,
		AppendEnv: []string{"PW_PATH_HELPER=1", "TMPDIR=" + scratch},
	})
	testutil.FailErr(t, "run real process fixture", err)
	if code != 0 || string(output) != "native bytes" {
		t.Fatalf("process code=%d output=%q", code, output)
	}
	bound, err := p.Bind(t.Context(), "@scratch/external file", false)
	testutil.FailErr(t, "bind process-produced file", err)
	file, err := bound.Open()
	testutil.FailErr(t, "open process-produced bytes natively", err)
	defer file.Close()
	got := make([]byte, len("external bytes"))
	n, err := file.Read(got)
	testutil.FailErr(t, "read process-produced bytes", err)
	if string(got[:n]) != "external bytes" {
		t.Fatalf("native read=%q", got[:n])
	}
}

func TestProgramPathProcessHelper(t *testing.T) {
	if os.Getenv("PW_PATH_HELPER") != "1" {
		return
	}
	var path string
	for i, arg := range os.Args {
		if arg == "--" && i+1 < len(os.Args) {
			path = os.Args[i+1]
			break
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprint(os.Stderr, err)
		os.Exit(2)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("TMPDIR"), "external file"), []byte("external bytes"), 0600); err != nil {
		fmt.Fprint(os.Stderr, err)
		os.Exit(3)
	}
	fmt.Print(string(data))
	os.Exit(0)
}

func TestNativePathConversionCannotWidenAProcessBoundary(t *testing.T) {
	tc, _ := readSessionContext(t)
	secondary := t.TempDir()
	tc.Source.Roots = append(tc.Source.Roots, projectroot.RootRef{ID: "secondary", Label: "secondary", Path: secondary})
	p, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "bind invocation", err)
	converted, err := p.NativePaths(t.Context(), []string{"@secondary/private"}, false)
	testutil.FailErr(t, "convert separate root", err)
	process := confine.Confinement{Roots: []string{tc.ActiveRootPath()}, ReadDenyPaths: []string{secondary}, Network: confine.NetworkDeny}
	target := strings.TrimSuffix(string(converted), "\n")
	if verdict := confine.BoundaryOf(&process).Filesystem.Verdict(confine.AccessRead, target); verdict.Allowed {
		t.Fatalf("conversion widened denied process input: %+v", verdict)
	}
	if len(process.Roots) != 1 || len(process.ReadDenyPaths) != 1 {
		t.Fatal("conversion changed process confinement")
	}
}

// A lexical Clean would open the decoy at root/same. Both routes must instead
// follow link into nested/child, then ascend to nested/same.
func TestNativeAbsoluteOperandPreservesSymlinkParentTraversal(t *testing.T) {
	tc, root := readSessionContext(t)
	testutil.FailErr(t, "seed nested directory", os.MkdirAll(filepath.Join(root, "nested", "child"), 0700))
	testutil.FailErr(t, "seed physical target", os.WriteFile(filepath.Join(root, "nested", "same"), []byte("actual identity"), 0600))
	testutil.FailErr(t, "seed lexical decoy", os.WriteFile(filepath.Join(root, "same"), []byte("wrong identity"), 0600))
	testutil.FailErr(t, "seed directory link", os.Symlink("nested/child", filepath.Join(root, "link")))
	operand := root + "/link/../same"
	p, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "bind invocation", err)
	converted, err := p.NativePaths(t.Context(), []string{operand}, true)
	testutil.FailErr(t, "convert exact native spelling", err)
	if string(converted) != operand+"\x00" {
		t.Fatalf("conversion changed operand: %q", converted)
	}
	bound, err := p.Bind(t.Context(), operand, false)
	testutil.FailErr(t, "bind exact native operand", err)
	file, err := bound.Open()
	testutil.FailErr(t, "open native identity", err)
	defer file.Close()
	native, err := io.ReadAll(file)
	testutil.FailErr(t, "read actual native identity", err)
	external, err := os.ReadFile(operand)
	testutil.FailErr(t, "read physical OS identity", err)
	if string(native) != "actual identity" || string(external) != string(native) {
		t.Fatalf("native=%q external=%q", native, external)
	}
}
