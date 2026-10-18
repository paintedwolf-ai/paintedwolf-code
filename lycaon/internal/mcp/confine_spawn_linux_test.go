//go:build linux

package mcp

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLinuxStdioMCPAppliedBoundary(t *testing.T) {
	if !confine.Available() {
		caps, _ := confine.LinuxCapabilities()
		t.Fatalf("required Linux MCP boundary is unavailable: %+v", caps)
	}
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	_, err := confine.StartEgressBroker(t.TempDir())
	testutil.FailErr(t, "start MCP egress broker", err)
	t.Cleanup(func() { _ = confine.StopEgressBroker() })
	python, err := exec.LookPath("python3")
	testutil.FailErr(t, "locate MCP fixture runtime", err)
	account, err := user.Current()
	testutil.FailErr(t, "current fixture user", err)
	outside, err := os.MkdirTemp(account.HomeDir, ".pwc-mcp-boundary-") //nolint:usetesting // Must lie outside standard temporary write roots.
	testutil.FailErr(t, "outside fixture directory", err)
	t.Cleanup(func() { _ = os.RemoveAll(outside) })
	guard := filepath.Join(outside, "guard")
	testutil.FailErr(t, "native outside write control", os.WriteFile(guard, []byte("before"), 0o600))
	root := t.TempDir()
	const script = `import errno, sys
open('allowed.txt', 'w').write('allowed')
try:
    open(sys.argv[1], 'w').write('escaped')
except OSError as error:
    assert error.errno in (errno.EACCES, errno.EROFS), error
else:
    raise RuntimeError('MCP process wrote outside its grant')
`
	spawn, err := prepareStdioCommand(t.Context(), "linux-boundary-fixture", python, []string{"-I", "-c", script, guard}, stdioEnv{
		base: []string{"PATH=" + os.Getenv("PATH")},
	}, []string{root})
	testutil.FailErr(t, "prepare confined MCP fixture", err)
	defer spawn.cleanup()
	defer spawn.releaseEgress()
	spawn.cmd.Dir = root
	if output, err := spawn.cmd.CombinedOutput(); err != nil {
		t.Fatalf("applied MCP fixture: %v: %s", err, output)
	}
	data, err := os.ReadFile(guard)
	testutil.FailErr(t, "read outside fixture", err)
	if string(data) != "before" {
		t.Fatalf("MCP process changed the outside fixture: %q", data)
	}
	data, err = os.ReadFile(filepath.Join(root, "allowed.txt"))
	testutil.FailErr(t, "read allowed MCP output", err)
	if string(data) != "allowed" {
		t.Fatalf("MCP output = %q", data)
	}
}
