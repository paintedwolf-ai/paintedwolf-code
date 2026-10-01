package confine_test

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/lineage"
	"github.com/lycaon/lycaon/internal/testutil"
)

// liveBroker starts the host's real front door for one test.
func liveBroker(t *testing.T) egressproxy.Addrs {
	t.Helper()
	if !lineage.Supported() {
		t.Skip("descendant observation requires a supported platform")
	}
	addrs, err := confine.StartEgressBroker(t.TempDir())
	testutil.FailErr(t, "start egress broker", err)
	t.Cleanup(func() { _ = confine.StopEgressBroker() })
	return addrs
}

// boundAction registers one action with the broker and returns its boundary.
func boundAction(t *testing.T, cmd confine.EgressCommand) (*confine.Confinement, *confine.ActionLease) {
	t.Helper()
	// The fixture upstream is a local server, so the boundary carries the
	// loopback-connect grant a real command would have been approved for.
	c := &confine.Confinement{
		Network: confine.NetworkProxyOnly, ProjectID: cmd.ProjectID, LoopbackConnect: true,
	}
	lease, err := confine.BindAction(c, cmd)
	testutil.FailErr(t, "bind action", err)
	t.Cleanup(func() { lease.Close(t.Context()) })
	return c, lease
}

// daemonShell starts a process that outlives its command, holding the lineage
// marker the way any descendant does, and returns a function that makes it
// fetch a URL through the broker and report the status code.
func daemonShell(t *testing.T, c *confine.Confinement, brokerAddr string) func(url string) string {
	t.Helper()
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is required to drive a descendant through the broker")
	}
	cmd := exec.Command("/bin/sh")
	cmd.ExtraFiles = []*os.File{confine.LineageMarkerForTest(c)}
	stdin, err := cmd.StdinPipe()
	testutil.FailErr(t, "stdin pipe", err)
	stdout, err := cmd.StdoutPipe()
	testutil.FailErr(t, "stdout pipe", err)
	testutil.FailErr(t, "start descendant", cmd.Start())
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	reader := bufio.NewReader(stdout)
	return func(target string) string {
		line := "curl -s -o /dev/null -m 5 -w '%{http_code}\\n' -x http://" + brokerAddr + " " + target + "\n"
		if _, err := io.WriteString(stdin, line); err != nil {
			t.Fatalf("drive descendant: %v", err)
		}
		status, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read descendant status: %v", err)
		}
		return strings.TrimSpace(status)
	}
}

// The environment a confined command reads is a function of its boundary: two
// separate actions in one project receive the same bytes, so build caches keyed
// on the environment hit across calls.
func TestConfinedEnvironmentIsStableAcrossActions(t *testing.T) {
	liveBroker(t)
	first, _ := boundAction(t, confine.EgressCommand{ProjectID: "project-a", ToolCallID: "call-1"})
	second, _ := boundAction(t, confine.EgressCommand{ProjectID: "project-a", ToolCallID: "call-2"})

	base := []string{"PATH=/usr/bin", "HOME=/tmp"}
	firstEnv := strings.Join(confine.ProcessEnvironment(base, *first), "\n")
	secondEnv := strings.Join(confine.ProcessEnvironment(base, *second), "\n")
	if firstEnv != secondEnv {
		t.Fatalf("two actions received different environments:\n%s\n---\n%s", firstEnv, secondEnv)
	}
	if first.LineageID == second.LineageID {
		t.Fatal("two actions shared one lineage; their effects would be indistinguishable")
	}
}

// A process a confined command leaves running stays attributed to that action.
func TestSurvivorsResolveToTheActionThatStartedThem(t *testing.T) {
	liveBroker(t)
	c, lease := boundAction(t, confine.EgressCommand{
		ProjectID: "project-a", SessionID: "s1", ToolCallID: "call-1",
		ToolName: "command", CommandLine: "./task check",
	})

	// A shell that detaches a child and exits leaves it holding the marker.
	cmd := exec.Command("/bin/sh", "-c", "(sleep 30 &) ; exit 0")
	cmd.ExtraFiles = []*os.File{confine.LineageMarkerForTest(c)}
	testutil.FailErr(t, "run", cmd.Run())

	survivors, err := lease.Survivors()
	testutil.FailErr(t, "survivors", err)
	if len(survivors) == 0 {
		t.Fatal("a process left running after the command exited was not observed")
	}
	t.Cleanup(func() {
		for _, pid := range survivors {
			if p, findErr := os.FindProcess(pid); findErr == nil {
				_ = p.Kill()
			}
		}
	})
	owner, ok := lineage.Of(survivors[0])
	if !ok || !strings.Contains(owner.Owner(), "./task check") {
		t.Fatalf("survivor %d did not resolve to the action that started it", survivors[0])
	}
}

// A daemon one command left running reaches the broker while its own action is
// live, is answered for by a later action of the same project, and reaches
// nothing once that project has no action running.
func TestSurvivorEgressFollowsProjectAccountability(t *testing.T) {
	addrs := liveBroker(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "reached")
	}))
	defer upstream.Close()

	confine.SetEgressResolver(func(context.Context, confine.EgressCommand, egressproxy.Endpoint, *confine.EgressDetectionCitation) bool {
		return true
	})
	t.Cleanup(func() { confine.SetEgressResolver(nil) })

	c, lease := boundAction(t, confine.EgressCommand{
		ProjectID: "project-b", SessionID: "s1", ToolCallID: "call-1",
		ToolName: "command", CommandLine: "./build --all",
	})
	fetch := daemonShell(t, c, addrs.HTTP)

	if status := fetch(upstream.URL); status != "200" {
		t.Fatalf("a live action's descendant was refused: %s", status)
	}

	// A second command of the same project starts while the daemon runs.
	_, successor := boundAction(t, confine.EgressCommand{
		ProjectID: "project-b", SessionID: "s1", ToolCallID: "call-2",
		ToolName: "command", CommandLine: "./build --test",
	})
	lease.Close(t.Context())
	if status := fetch(upstream.URL); status != "200" {
		t.Fatalf("a later action of the same project did not answer for the daemon: %s", status)
	}

	// The successor answered for that request; it did not make it. Its receipt
	// has to say so, or it reads as a destination the command itself reached.
	inherited := successor.ObservedHosts()
	if len(inherited) == 0 {
		t.Fatal("the answering action recorded nothing for the daemon it covered")
	}
	if !inherited[0].Inherited {
		t.Fatalf("a destination reached by a left-over process is unmarked on the receipt: %+v", inherited[0])
	}

	// With nothing running, nobody is accountable and the daemon reaches nothing.
	successor.Close(t.Context())
	confine.ForgetOrphanEgressAttempts()
	if status := fetch(upstream.URL); status != "403" {
		t.Fatalf("status with no live action = %s, want 403", status)
	}
	attempts := confine.OrphanEgressAttempts()
	if len(attempts) == 0 {
		t.Fatal("a refused dial from a surviving process was not recorded")
	}
	if !strings.Contains(attempts[0].Owner, "./build --all") {
		t.Fatalf("the record does not name the command that left the process running: %+v", attempts[0])
	}
}
