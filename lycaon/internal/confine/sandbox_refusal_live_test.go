package confine_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

// An empty state root prevents reclamation of concurrent test streams.
func requireRefusalWatch(t *testing.T) {
	t.Helper()
	confine.StartRefusalWatch(t.Context(), "")
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	for !confine.RefusalWatchLive() {
		if ctx.Err() != nil {
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
	self := requireLiveBoundary(t)
	requireRefusalWatch(t)
	outside, err := filepath.EvalSymlinks(outsideTemporaryWriteRoots(t))
	testutil.FailErr(t, "resolve outside dir", err)
	target := filepath.Join(outside, "escape")

	refusals, out := refusalsOf(t, self, []string{t.TempDir()}, "/usr/bin/touch", target)
	if refusals.Witness != liveRefusalWitness {
		t.Fatalf("witness = %s, want %s", refusals.Witness, liveRefusalWitness)
	}
	if liveRefusalWitness != confine.WitnessKernel {
		t.Skip("Landlock has no stream of its own refusals; the floor's refusal below is what reaches the action")
	}
	refused, ok := findRefusal(refusals, "file-write-create", target)
	if !ok {
		t.Fatalf("the kernel's refusal of %s did not reach the action: %+v\noutput: %s", target, refusals.Refusals, out)
	}
	if refused.Recovery != confine.RecoverWriteRoot || refused.Grant != outside || refused.Process != "touch" {
		t.Fatalf("refusal = %+v, want write_root %s from touch", refused, outside)
	}
}

// A write a floor refuses reaches the action on every platform.
func TestFloorRefusalOfAWriteReachesItsAction(t *testing.T) {
	self := requireLiveBoundary(t)
	requireRefusalWatch(t)
	project, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve project", err)
	target := filepath.Join(project, "AGENTS.md")

	refusals, out := refusalsOf(t, self, liveBoundaryRoots(project), "/usr/bin/touch", target)
	if _, ok := findRefusal(refusals, "file-write-create", target); !ok {
		t.Fatalf("the floor's refusal of %s did not reach the action: %+v\noutput: %s", target, refusals.Refusals, out)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("the refused write landed: %v", err)
	}
}

func TestKernelRefusalOfAUnixListenerNeedsHostExecution(t *testing.T) {
	self := requireLiveBoundary(t)
	requireRefusalWatch(t)
	nc, err := exec.LookPath("nc")
	if err != nil {
		t.Skip("nc is required to open a unix-socket listener")
	}
	project, err := filepath.EvalSymlinks(shortTempDir(t))
	testutil.FailErr(t, "resolve project", err)
	socket := filepath.Join(project, "l.sock")

	refusals, out := refusalsOf(t, self, liveBoundaryRoots(project), nc, "-lU", socket)
	refused, ok := findRefusal(refusals, "network-bind", socket)
	if !ok {
		t.Fatalf("the refused listener did not reach the action: %+v\noutput: %s", refusals.Refusals, out)
	}
	if refused.Recovery != confine.RecoverHostExecution {
		t.Fatalf("refusal = %+v, want host_execution", refused)
	}
}

func TestDiskSpaceQueriesAreNotRefused(t *testing.T) {
	self := requireSeatbelt(t)
	requireRefusalWatch(t)
	project, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve project", err)
	refusals, out := refusalsOf(t, self, []string{project}, "/bin/df", "-k", "/")
	if refusals.Witness != confine.WitnessKernel || len(refusals.Refusals) > 0 {
		t.Fatalf("df witness = %s, refusals = %+v\noutput: %s", refusals.Witness, refusals.Refusals, out)
	}
}

func TestOrdinaryToolchainsStartWithoutRefusals(t *testing.T) {
	self := requireLiveBoundary(t)
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
