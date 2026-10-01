package confine_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

var startRefusalWatch sync.Once

// An empty state root prevents reclamation of concurrent test streams.
func requireRefusalWatch(t *testing.T) {
	t.Helper()
	startRefusalWatch.Do(func() { confine.StartRefusalWatch("") })
	deadline := time.Now().Add(15 * time.Second)
	for !confine.RefusalWatchLive() {
		if time.Now().After(deadline) {
			t.Skip("kernel sandbox reports are not readable on this host")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// refusalsOf collects kernel reports for one confined action.
func refusalsOf(t *testing.T, self string, roots []string, name string, args ...string) (confine.SandboxRefusals, string) {
	t.Helper()
	c := &confine.Confinement{Roots: roots, Network: confine.NetworkDeny}
	lease, err := confine.BindAction(c, confine.EgressCommand{SessionID: "refusal-live", ToolName: "command", CommandLine: name})
	testutil.FailErr(t, "bind action", err)
	cmd, cleanup, err := confine.Command(context.Background(), self, name, args, *c)
	testutil.FailErr(t, "build confined command", err)
	out, _ := cmd.CombinedOutput()
	cleanup()
	refusals := lease.SettledRefusals(t.Context())
	lease.Close(t.Context())
	return refusals, string(out)
}

func findRefusal(refusals confine.SandboxRefusals, operation, target string) (confine.SandboxRefusal, bool) {
	for _, r := range refusals.Refusals {
		if r.Operation == operation && r.Target == target {
			return r, true
		}
	}
	return confine.SandboxRefusal{}, false
}

func TestKernelRefusalOfAWriteReachesItsAction(t *testing.T) {
	self := requireSeatbelt(t)
	requireRefusalWatch(t)
	outside, err := filepath.EvalSymlinks(outsideTemporaryWriteRoots(t))
	testutil.FailErr(t, "resolve outside dir", err)
	target := filepath.Join(outside, "escape")

	refusals, out := refusalsOf(t, self, []string{t.TempDir()}, "/usr/bin/touch", target)
	if refusals.Witness != confine.WitnessKernel {
		t.Fatalf("witness = %s, want kernel", refusals.Witness)
	}
	refused, ok := findRefusal(refusals, "file-write-create", target)
	if !ok {
		t.Fatalf("the kernel's refusal of %s did not reach the action: %+v\noutput: %s", target, refusals.Refusals, out)
	}
	if refused.Recovery != confine.RecoverWriteRoot || refused.Grant != outside || refused.Process != "touch" {
		t.Fatalf("refusal = %+v, want write_root %s from touch", refused, outside)
	}
}

func TestKernelRefusalOfAUnixListenerNeedsHostExecution(t *testing.T) {
	self := requireSeatbelt(t)
	requireRefusalWatch(t)
	if _, err := exec.LookPath("nc"); err != nil {
		t.Skip("nc is required to open a unix-socket listener")
	}
	project, err := filepath.EvalSymlinks(shortTempDir(t))
	testutil.FailErr(t, "resolve project", err)
	socket := filepath.Join(project, "l.sock")

	refusals, out := refusalsOf(t, self, []string{project}, "/usr/bin/nc", "-lU", socket)
	refused, ok := findRefusal(refusals, "network-bind", socket)
	if !ok {
		t.Fatalf("the refused listener did not reach the action: %+v\noutput: %s", refusals.Refusals, out)
	}
	if refused.Recovery != confine.RecoverHostExecution {
		t.Fatalf("refusal = %+v, want host_execution", refused)
	}
}

func TestOrdinaryToolchainsStartWithoutRefusals(t *testing.T) {
	self := requireSeatbelt(t)
	requireRefusalWatch(t)
	project, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve project", err)
	probes := [][]string{
		{"/bin/sh", "-c", "true"},
		{"/usr/bin/env", "true"},
		{"git", "--version"},
		{"python3", "-c", "pass"},
		{"node", "-e", "0"},
		{"go", "version"},
	}
	for _, probe := range probes {
		name, err := exec.LookPath(probe[0])
		if err != nil {
			continue
		}
		refusals, out := refusalsOf(t, self, []string{project}, name, probe[1:]...)
		if len(refusals.Refusals) > 0 {
			var lines []string
			for _, r := range refusals.Refusals {
				lines = append(lines, r.Display())
			}
			t.Errorf("%s started with refusals:\n%s\noutput: %s", strings.Join(probe, " "), strings.Join(lines, "\n"), out)
		}
	}
}
