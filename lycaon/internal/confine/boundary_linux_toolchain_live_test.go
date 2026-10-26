//go:build linux

package confine_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

// requireTool finds a toolchain the qualification suite exercises; a missing
// one fails, since a skipped toolchain would be a pass that covers nothing.
func requireTool(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("%s is required for the Linux toolchain suite: %v", name, err)
	}
	return path
}

// The supported toolchains work inside a project root under the default
// network-deny boundary: shells, Git, Python, Node, and Go.
func TestLinuxToolchainsWorkInsideTheBoundary(t *testing.T) {
	self := requireLinuxSandbox(t)
	proj := t.TempDir()
	goTool, err := exec.LookPath("go")
	testutil.FailErr(t, "locate the go toolchain", err)
	testutil.FailErr(t, "write module", os.WriteFile(filepath.Join(proj, "go.mod"), []byte("module probe\n\ngo 1.22\n"), 0o644))
	testutil.FailErr(t, "write main", os.WriteFile(filepath.Join(proj, "main.go"), []byte("package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"go-ok\") }\n"), 0o644))
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + proj,
		"GIT_CONFIG_NOSYSTEM=1",
		"GOCACHE=" + filepath.Join(proj, ".gocache"),
		"GOPATH=" + filepath.Join(proj, ".gopath"),
		"GOTOOLCHAIN=local",
		"GOFLAGS=-mod=mod",
		"TMPDIR=" + filepath.Join(proj, ".tmp"),
	}
	testutil.FailErr(t, "temp dir", os.Mkdir(filepath.Join(proj, ".tmp"), 0o700))
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{"shell", []string{requireTool(t, "sh"), "-c", "echo shell-ok > s.txt && cat s.txt"}, "shell-ok"},
		{"git", []string{requireTool(t, "sh"), "-c", requireTool(t, "git") + " init -q && " + requireTool(t, "git") +
			" -c user.email=probe@example.test -c user.name=probe commit -q --allow-empty -m git-ok && " +
			requireTool(t, "git") + " log --format=%s"}, "git-ok"},
		{"python", []string{requireTool(t, "python3"), "-c", "import json, sqlite3, hashlib; open('p.txt','w').write('x'); print('python-ok')"}, "python-ok"},
		{"node", []string{requireTool(t, "node"), "-e", "require('fs').writeFileSync('n.txt', 'x'); console.log('node-ok')"}, "node-ok"},
		{"go", []string{goTool, "run", "."}, "go-ok"},
	}
	c := denyConfinement(proj)
	for _, tc := range cases {
		cmd, cleanup, err := confine.Command(t.Context(), helperBinary(t, self), tc.argv[0], tc.argv[1:], c)
		testutil.FailErr(t, "confine.Command "+tc.name, err)
		cmd.Dir, cmd.Env = proj, env
		out, err := cmd.CombinedOutput()
		cleanup()
		if err != nil || !strings.Contains(string(out), tc.want) {
			t.Fatalf("%s inside the boundary: %v\n%s", tc.name, err, out)
		}
	}
}

// benchCreates is how many files one measured command creates.
const benchCreates = 20000

// benchFileCreates times commands that each create benchCreates files and
// reports the cost per create; prepare returns the command and its cleanup.
func benchFileCreates(b *testing.B, prepare func(root string) (*exec.Cmd, func(), error)) {
	for i := 0; i < b.N; i++ {
		root := b.TempDir()
		cmd, cleanup, err := prepare(root)
		if err != nil {
			b.Fatalf("prepare: %v", err)
		}
		cmd.Env = append(probeEnv("floors"), floorsScriptEnv+"=bulk "+root+" "+strconv.Itoa(benchCreates))
		out, err := cmd.CombinedOutput()
		cleanup()
		if err != nil || !strings.Contains(string(out), ": ok") {
			b.Fatalf("creates: %v\n%s", err, out)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Microseconds())/float64(b.N*benchCreates), "us/create")
}

// BenchmarkLinuxConfinedFileCreates measures a create-heavy command under the
// boundary, where the supervisor performs each create; compare
// BenchmarkLinuxUnconfinedFileCreates.
func BenchmarkLinuxConfinedFileCreates(b *testing.B) {
	if caps, _ := confine.LinuxCapabilities(); !caps.Available() {
		b.Fatalf("linux confinement unavailable: %+v", caps)
	}
	self, err := os.Executable()
	if err != nil {
		b.Fatalf("test binary: %v", err)
	}
	benchFileCreates(b, func(root string) (*exec.Cmd, func(), error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		cmd, cleanup, err := confine.Command(ctx, self, self, []string{probeTestFlag}, denyConfinement(root))
		return cmd, func() { cleanup(); cancel() }, err
	})
}

func BenchmarkLinuxUnconfinedFileCreates(b *testing.B) {
	self, err := os.Executable()
	if err != nil {
		b.Fatalf("test binary: %v", err)
	}
	benchFileCreates(b, func(string) (*exec.Cmd, func(), error) {
		return exec.Command(self, probeTestFlag), func() {}, nil
	})
}
