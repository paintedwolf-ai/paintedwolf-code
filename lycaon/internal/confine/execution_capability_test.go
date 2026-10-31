package confine_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	pwexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProcessControlOnlyWidensSignals(t *testing.T) {
	base := confine.Confinement{Roots: []string{t.TempDir()}, Network: confine.NetworkDeny}
	ordinary, err := confine.BuildSeatbeltProfile(base)
	testutil.FailErr(t, "ordinary profile", err)
	base.ProcessControl = true
	widened, err := confine.BuildSeatbeltProfile(base)
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
	wantArgs := []string{"not-the-helper", "__confine-exec", "--profile-fd", "3", "--launch-host-execution", "--", "/usr/bin/true"}
	if command.Path != "not-the-helper" || !slices.Equal(command.Args, wantArgs) {
		t.Fatalf("host command lost its selected launch helper or exact target: %s %q", command.Path, command.Args)
	}
	if len(command.ExtraFiles) != 1 || command.ExtraFiles[0] == nil {
		t.Fatal("host command lost its inherited profile transport")
	}
	if _, err := confine.BuildSeatbeltProfile(*c); err == nil {
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
	root := t.TempDir()
	c, _ := confine.DefaultConfinement(confine.Request{HostExecution: true, Roots: []string{root}})
	identity := confine.EgressCommand{SessionID: "synthetic-session", ToolCallID: "host-process-inspection", ProjectDir: root}
	lease, err := confine.BindAction(c, identity)
	testutil.FailErr(t, "admit host process inspection", err)
	defer lease.Close(context.Background())
	plan := pwexec.AgentLaunch(pwexec.LaunchAgentCommand, "synthetic setuid process inspection", c)
	plan.CurrentAuthority = func(ctx context.Context) error { return lease.ValidateLaunch(ctx, c, identity) }
	command, cleanup, err := pwexec.PrepareCommand(t.Context(), "/bin/ps", []string{"-p", strconv.Itoa(os.Getpid()), "-o", "pid="}, pwexec.ExecOpts{Launch: plan, Dir: root})
	testutil.FailErr(t, "build setuid inspection", err)
	defer cleanup()
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	testutil.FailErr(t, "run setuid inspection", pwexec.RunPrepared(t.Context(), command))
	if strings.TrimSpace(output.String()) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("unexpected process inspection: %q", output.String())
	}
}
