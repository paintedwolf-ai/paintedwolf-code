//go:build linux

package confine_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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
	goTool := filepath.Join(runtime.GOROOT(), "bin", "go")
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
