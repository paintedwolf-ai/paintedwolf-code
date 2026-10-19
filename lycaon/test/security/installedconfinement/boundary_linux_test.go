//go:build linux && installedhelper

package installedconfinement

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/confine/linuxsandbox"
	"github.com/lycaon/lycaon/internal/testutil"
)

func runInstalledHelper(t *testing.T, helper selectedHelper, box confine.Confinement, program string, args ...string) (int, string) {
	t.Helper()
	helper.verify(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	command, cleanup, err := confine.Command(ctx, helper.path, program, args, box)
	testutil.FailErr(t, "prepare actual installed helper invocation", err)
	defer cleanup()
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + box.Roots[0], "TMPDIR=" + box.Roots[0]}
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	err = command.Run()
	if command.ProcessState == nil || ctx.Err() != nil {
		t.Fatalf("selected installed helper did not settle: %v, %s", err, output.String())
	}
	helper.verify(t)
	return command.ProcessState.ExitCode(), output.String()
}

func TestLinuxInstalledHelperAppliedBoundary(t *testing.T) {
	helper := requireSelectedHelper(t)
	project := t.TempDir()
	box := confine.Confinement{Roots: []string{project}, Network: confine.NetworkDeny, EgressOff: confine.EgressOffOverride}
	marker := filepath.Join(project, "target-executed")
	status, output := runInstalledHelper(t, helper, box, "/bin/sh", "-c", `printf executed > "$1"`, "installed-helper", marker)
	if helper.outcome == "refused" {
		reason, typed := linuxsandbox.ParseUnavailable(output)
		if status != confine.HelperUnavailableExit || !typed || reason.Code != helper.refusal {
			t.Fatalf("installed helper did not report its expected typed refusal: exit=%d reason=%+v typed=%t output=%s", status, reason, typed, output)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("refused installed helper launched its target")
		}
		logInstalledResult(t, helper, reason.Code)
		return
	}
	if status != 0 {
		t.Fatalf("installed helper did not apply its required boundary: exit=%d output=%s", status, output)
	}
	data, err := os.ReadFile(marker)
	testutil.FailErr(t, "read installed helper positive control", err)
	if string(data) != "executed" {
		t.Fatal("installed helper did not execute its positive control")
	}
	account, err := user.Current()
	testutil.FailErr(t, "read actual host account", err)
	outside, err := os.MkdirTemp(account.HomeDir, ".pwc-installed-boundary-") //nolint:usetesting // Must be outside the runner's temporary write floor.
	testutil.FailErr(t, "create owned ungranted host directory", err)
	t.Cleanup(func() { _ = os.RemoveAll(outside) })
	secret := filepath.Join(project, "denied-secret")
	testutil.FailErr(t, "write controlled denied input", os.WriteFile(secret, []byte("must-not-cross"), 0o600))
	secondSecret := filepath.Join(project, "second-denied-secret")
	testutil.FailErr(t, "write second controlled denied input", os.WriteFile(secondSecret, []byte("must-not-cross-either"), 0o600))
	box.ReadDenyPaths = []string{secret, secondSecret}
	box.ReadRoots = []string{"/tmp"}
	sockets, err := os.MkdirTemp("/tmp", "pw-installed-") //nolint:usetesting // Linux Unix-socket address limit.
	testutil.FailErr(t, "create short installed-helper socket fixture", err)
	t.Cleanup(func() { _ = os.RemoveAll(sockets) })
	agentPath := filepath.Join(sockets, "agent.sock")
	agent, err := net.Listen("unix", agentPath)
	testutil.FailErr(t, "create controlled ordinary Unix endpoint", err)
	t.Cleanup(func() { _ = agent.Close() })
	loopback, err := net.Listen("tcp4", "127.0.0.1:0")
	testutil.FailErr(t, "create controlled host loopback endpoint", err)
	t.Cleanup(func() { _ = loopback.Close() })
	port := loopback.Addr().(*net.TCPAddr).Port
	python, err := exec.LookPath("python3")
	testutil.FailErr(t, "locate required installed-boundary fixture runtime", err)
	namespaces, err := json.Marshal(linuxsandbox.HostNamespaces())
	testutil.FailErr(t, "encode host namespace identities", err)
	const probe = `import errno, json, os, socket, sys
host = json.loads(sys.argv[1])
for kind, identity in host.items():
    assert os.readlink('/proc/self/ns/' + kind) != identity, kind
status = dict(line.split(':', 1) for line in open('/proc/self/status') if ':' in line)
assert status['NoNewPrivs'].strip() == '1'
for kind in ('CapInh', 'CapPrm', 'CapEff', 'CapBnd', 'CapAmb'):
    assert int(status[kind].strip(), 16) == 0, kind
for operation in (lambda: open(sys.argv[2], 'w'), lambda: open(sys.argv[3], 'r'), lambda: open(sys.argv[6], 'r')):
    try:
        operation()
    except OSError as error:
        assert error.errno in (errno.EACCES, errno.EPERM, errno.EROFS, errno.ENOENT), error
    else:
        raise RuntimeError('installed helper admitted a denied filesystem operation')
for family, address in ((socket.AF_UNIX, sys.argv[4]), (socket.AF_INET, ('127.0.0.1', int(sys.argv[5])))):
    with socket.socket(family, socket.SOCK_STREAM) as channel:
        channel.settimeout(2)
        try:
            channel.connect(address)
        except OSError as error:
            assert error.errno in (errno.EACCES, errno.EPERM, errno.ENETUNREACH, errno.ECONNREFUSED), error
        else:
            raise RuntimeError('installed helper admitted an ungranted host socket')
print('installed-boundary-applied')
`
	status, output = runInstalledHelper(t, helper, box, python, "-I", "-c", probe, string(namespaces), filepath.Join(outside, "escaped"), secret, agentPath, fmt.Sprint(port), secondSecret)
	if status != 0 || !strings.Contains(output, "installed-boundary-applied") {
		t.Fatalf("installed helper adversarial boundary failed: exit=%d output=%s", status, output)
	}
	if _, err := os.Stat(filepath.Join(outside, "escaped")); !os.IsNotExist(err) {
		t.Fatal("installed helper changed the ungranted host directory")
	}
	box.SocketGrants = []confine.SocketGrant{{ApprovedPath: agentPath, ResolvedPath: agentPath}}
	box.LoopbackConnect, box.LoopbackConnectPorts = true, []uint16{uint16(port)}
	const granted = `import socket, sys
for family, address in ((socket.AF_UNIX, sys.argv[1]), (socket.AF_INET, ('127.0.0.1', int(sys.argv[2])))):
    with socket.socket(family, socket.SOCK_STREAM) as channel:
        channel.settimeout(2)
        channel.connect(address)
print('installed-grants-applied')
`
	status, output = runInstalledHelper(t, helper, box, python, "-I", "-c", granted, agentPath, fmt.Sprint(port))
	if status != 0 || !strings.Contains(output, "installed-grants-applied") {
		t.Fatalf("installed helper did not honor exact socket grants: exit=%d output=%s", status, output)
	}
	logInstalledResult(t, helper, "")
}

func logInstalledResult(t *testing.T, helper selectedHelper, refusal string) {
	t.Helper()
	var kernel unix.Utsname
	testutil.FailErr(t, "read native installed-kernel identity", unix.Uname(&kernel))
	raw, err := json.Marshal(map[string]any{"schema_version": 1, "helper": helper.path, "binary_sha256": helper.digest, "architecture": runtime.GOARCH, "kernel_release": unix.ByteSliceToString(kernel.Release[:]), "host_kernel_facts": linuxsandbox.Facts(), "outcome": helper.outcome, "refusal_code": refusal})
	testutil.FailErr(t, "encode installed boundary result", err)
	t.Logf("installed_boundary=%s", raw)
}
