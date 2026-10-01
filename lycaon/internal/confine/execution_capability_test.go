package confine_test

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProcessControlOnlyWidensSignals(t *testing.T) {
	base := confine.Confinement{Roots: []string{t.TempDir()}, Network: confine.NetworkDeny}
	ordinary, err := confine.BuildProfile(base)
	testutil.FailErr(t, "ordinary profile", err)
	base.ProcessControl = true
	widened, err := confine.BuildProfile(base)
	testutil.FailErr(t, "process-control profile", err)
	if widened != strings.Replace(ordinary, "(allow signal (target same-sandbox))", "(allow signal)", 1) {
		t.Fatal("process control changed something other than signal authority")
	}
	report := confine.ReportOf(confine.BoundaryOf(&base))
	if !report.Confined || !report.ProcessControl || report.HostExecution {
		t.Fatalf("incorrect report: %+v", report)
	}
}

func TestHostExecutionSelectsExplicitUnconfinedCommand(t *testing.T) {
	c, ok := confine.DefaultConfinement(confine.Request{HostExecution: true, Roots: []string{t.TempDir()}})
	if !ok || c == nil || !c.HostExecution {
		t.Fatal("host execution not selected")
	}
	report := confine.ReportOf(confine.BoundaryOf(c)).WithLocalNetwork(confine.LocalNetworkGrant{Listen: true, ListenPorts: []uint16{3000}}).WithRemotePackageExecution([]string{"registry.example"}, nil)
	if report.ListenGranted || len(report.ListenPorts) != 0 || report.RemotePackageExecution != nil {
		t.Fatalf("host execution claimed a reduced boundary: %+v", report)
	}
	if report.Confined || !report.HostExecution {
		t.Fatalf("misleading report: %+v", report)
	}
	command, cleanup, err := confine.Command(t.Context(), "not-the-helper", "/usr/bin/true", nil, *c)
	testutil.FailErr(t, "build host command", err)
	defer cleanup()
	if command.Path != "/usr/bin/true" {
		t.Fatalf("wrapped host executable: %s", command.Path)
	}
	if _, err := confine.BuildProfile(*c); err == nil {
		t.Fatal("host execution produced sandbox profile")
	}
}

func TestProcessControlSignalsOwnedOutsideProcess(t *testing.T) {
	self := requireSeatbelt(t)
	child := exec.CommandContext(t.Context(), "/bin/sleep", "60")
	testutil.FailErr(t, "start owned process", child.Start())
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	c := confine.Confinement{Roots: []string{t.TempDir()}, Network: confine.NetworkDeny}
	// Signal zero checks reachability without changing process state.
	pid := strconv.Itoa(child.Process.Pid)
	if code, _ := confinedRun(t, self, c, "/bin/kill", "-0", pid); code == 0 {
		t.Fatal("ordinary sandbox reached outside process")
	}
	c.ProcessControl = true
	if code, out := confinedRun(t, self, c, "/bin/kill", "-0", pid); code != 0 {
		t.Fatalf("approved process control failed: %d %s", code, out)
	}
}

func TestHostExecutionRunsSetuidProcessInspection(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS setuid process inspection fixture")
	}
	c, _ := confine.DefaultConfinement(confine.Request{HostExecution: true, Roots: []string{t.TempDir()}})
	command, cleanup, err := confine.Command(t.Context(), "unused", "/bin/ps", []string{"-p", strconv.Itoa(os.Getpid()), "-o", "pid="}, *c)
	testutil.FailErr(t, "build setuid inspection", err)
	defer cleanup()
	output, err := command.CombinedOutput()
	testutil.FailErr(t, "run setuid inspection", err)
	if strings.TrimSpace(string(output)) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("unexpected process inspection: %q", output)
	}
}
