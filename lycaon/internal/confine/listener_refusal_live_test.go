package confine_test

import (
	"context"
	"strconv"
	"strings"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

// A TCP listener the sandbox refuses reports its bind, whatever else the
// program probed on the way.
func TestKernelRefusalOfATCPListenerReachesItsAction(t *testing.T) {
	self := requireLiveBoundary(t)
	requireRefusalWatch(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required to open a TCP listener")
	}
	project, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve project", err)
	// What python3 -m http.server does before serving: pick the address
	// family, then bind dual-stack. It exits instead of serving.
	server := `import contextlib, http.server as h, socket
class Server(h.ThreadingHTTPServer):
    def server_bind(self):
        with contextlib.suppress(Exception):
            self.socket.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 0)
        return super().server_bind()
Server.address_family, address = h._get_best_family(None, 18799)
Server(address, h.SimpleHTTPRequestHandler).server_close()`
	refusals, out := refusalsOf(t, self, liveBoundaryRoots(project), python, "-c", server)
	for _, refusal := range refusals.Refusals {
		t.Logf("refusal %s %q recovery=%s process=%s", refusal.Operation, refusal.Target, refusal.Recovery, refusal.Process)
	}
	t.Logf("witness=%s omitted=%d output:\n%s", refusals.Witness, refusals.Omitted, out)
	if _, ok := findRefusal(refusals, "network-bind", "local:*:18799"); !ok {
		t.Fatalf("the refused listener did not reach the action: %+v", refusals.Refusals)
	}
}

func TestKernelRefusalOfAnAllInterfacesBind(t *testing.T) {
	self := requireLiveBoundary(t)
	requireRefusalWatch(t)
	for _, python := range []string{"/opt/homebrew/bin/python3", "/usr/bin/python3"} {
		for _, address := range []string{"0.0.0.0", "127.0.0.1"} {
			refusals, out := refusalsOf(t, self, liveBoundaryRoots(t.TempDir()), python, "-c", "import socket; s=socket.socket(); s.bind(('"+address+"', 18791))")
			t.Logf("%s %s: %+v\n%s", python, address, refusals.Refusals, out)
		}
	}
}

// Bisects the profile: a Homebrew Python bind under the bare confinement and
// under the default one the command tool requests.
func TestHomebrewBindUnderEachConfinement(t *testing.T) {
	self := requireLiveBoundary(t)
	requireRefusalWatch(t)
	root := t.TempDir()
	defaulted, applied := confine.DefaultConfinement(confine.Request{Roots: []string{root}, SessionScratchRoot: t.TempDir()})
	if !applied {
		t.Skip("default confinement unavailable")
	}
	for name, c := range map[string]confine.Confinement{
		"bare":    {Roots: []string{root}, Network: confine.NetworkDeny},
		"default": *defaulted,
	} {
		profile, err := confine.BuildSeatbeltProfile(c)
		testutil.FailErr(t, "render profile", err)
		lease, err := confine.BindAction(&c, confine.EgressCommand{SessionID: "bisect", ToolName: "command", CommandLine: "python3"})
		testutil.FailErr(t, "bind action", err)
		cmd, cleanup, err := confine.Command(context.Background(), self, "/opt/homebrew/bin/python3", []string{"-c", "import socket; s=socket.socket(); s.bind(('127.0.0.1', 18789))"}, c)
		testutil.FailErr(t, "build confined command", err)
		out, _ := cmd.CombinedOutput()
		cleanup()
		refusals := lease.SettledRefusals(t.Context())
		lease.Close(t.Context())
		t.Logf("%s: refusals %+v\noutput %s\nPROFILE-BEGIN %s\n%s\nPROFILE-END", name, refusals.Refusals, out, name, profile)
	}
}

// Repeated refused binds by one executable in quick succession.
func TestKernelReportsRepeatedBinds(t *testing.T) {
	self := requireLiveBoundary(t)
	requireRefusalWatch(t)
	for i, port := range []string{"18781", "18781", "18782"} {
		refusals, _ := refusalsOf(t, self, liveBoundaryRoots(t.TempDir()), "/opt/homebrew/bin/python3", "-c", "import socket; s=socket.socket(); s.bind(('127.0.0.1', "+port+"))")
		t.Logf("attempt %d port %s: %d refusals %+v", i, port, len(refusals.Refusals), refusals.Refusals)
	}
}

func TestProcessSandboxState(t *testing.T) {
	out, err := exec.Command("/usr/bin/python3", "-c", "import ctypes, os; lib = ctypes.CDLL(None); print('sandboxed', lib.sandbox_check(os.getpid(), None, 0), 'parent', lib.sandbox_check(os.getppid(), None, 0))").CombinedOutput()
	t.Logf("%s err=%v", out, err)
}

// A process that is refused many operations before its bind: does the bind
// report still arrive?
func TestKernelReportsABindAfterManyRefusals(t *testing.T) {
	self := requireLiveBoundary(t)
	requireRefusalWatch(t)
	outside := outsideTemporaryWriteRoots(t)
	for _, before := range []int{0, 2, 5, 10, 30} {
		script := "import os, socket\nfor i in range(" + strconv.Itoa(before) + "):\n    try: open(os.path.join(" + strconv.Quote(outside) + ", 'f%d' % i), 'w')\n    except OSError: pass\ns = socket.socket()\ntry: s.bind(('127.0.0.1', 18780))\nexcept OSError as e: print('bind', e)\n"
		refusals, out := refusalsOf(t, self, liveBoundaryRoots(t.TempDir()), "/opt/homebrew/bin/python3", "-c", script)
		_, bound := findRefusal(refusals, "network-bind", "local:*:18780")
		t.Logf("after %d refused writes: %d refusals, omitted %d, bind reported %v; %s", before, len(refusals.Refusals), refusals.Omitted, bound, strings.TrimSpace(out))
	}
}
