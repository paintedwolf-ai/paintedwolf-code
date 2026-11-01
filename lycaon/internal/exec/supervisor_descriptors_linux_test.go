//go:build linux

package exec

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/launchbroker"
	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

const descriptorFixtureEnv = "PW_SUPERVISOR_DESCRIPTOR_FIXTURE"

func TestSupervisorDescriptorLayoutFixture(t *testing.T) {
	if os.Getenv(descriptorFixtureEnv) != "1" {
		return
	}
	reader, writer, err := os.Pipe()
	testutil.FailErr(t, "create unrelated descriptor canary", err)
	defer reader.Close()
	defer writer.Close()
	testutil.FailErr(t, "bound descriptor canary wait", reader.SetReadDeadline(time.Now().Add(5*time.Second)))
	for _, relay := range []bool{false, true} {
		admitted, err := unix.Dup(int(writer.Fd()))
		testutil.FailErr(t, "supply one explicit descriptor", err)
		layout := make([]byte, admitted-3+1)
		for i := range layout {
			layout[i] = '0'
		}
		layout[len(layout)-1] = '1'
		var files []*os.File
		if relay {
			files, err = relayDescriptorFiles(string(layout))
			testutil.FailErr(t, "capture declared relay descriptors", err)
		} else {
			files = (supervisorTarget{descriptors: string(layout)}).command().ExtraFiles
		}
		for _, file := range files {
			if file != nil {
				testutil.FailErr(t, "release declared descriptor custody", file.Close())
			}
		}
		if _, err := unix.FcntlInt(uintptr(admitted), unix.F_GETFD, 0); err != unix.EBADF {
			t.Fatalf("declared descriptor was not released: %v", err)
		}
		runtime.GC()
		_, err = writer.Write([]byte("x"))
		testutil.FailErr(t, "write unrelated descriptor after handoff cleanup", err)
		var one [1]byte
		_, err = io.ReadFull(reader, one[:])
		testutil.FailErr(t, "observe unrelated descriptor after handoff cleanup", err)
		if one[0] != 'x' {
			t.Fatal("unrelated descriptor payload changed")
		}
	}
}

func TestSupervisorAndRelayNeverBorrowDescriptorsFromEmptySlots(t *testing.T) {
	self, err := os.Executable()
	testutil.FailErr(t, "locate isolated descriptor fixture", err)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	fixture := exec.CommandContext(ctx, self, "-test.run=^TestSupervisorDescriptorLayoutFixture$")
	fixture.Env = append(os.Environ(), descriptorFixtureEnv+"=1")
	output, err := fixture.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated descriptor custody failed: %v\n%s", err, output)
	}
}

func TestMaximumSocketGrantLaunchFitsBrokerDescriptorTransport(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(t.TempDir(), "state"))
	project, err := os.MkdirTemp("/tmp", "pw-fd-") //nolint:usetesting // Unix socket addresses have a kernel byte limit.
	testutil.FailErr(t, "create short socket transport fixture", err)
	t.Cleanup(func() { _ = os.RemoveAll(project) })
	ctx, scope := socketref.WithScope(t.Context())
	defer scope.Close()
	var grants []confine.SocketGrant
	for i := range confine.MaxSocketGrants {
		path := filepath.Join(project, string(rune('a'+i)))
		listener, err := net.Listen("unix", path)
		testutil.FailErr(t, "listen on admitted service", err)
		t.Cleanup(func() { _ = listener.Close() })
		grant, err := confine.ResolveSocketRequest(path)
		testutil.FailErr(t, "resolve admitted service", err)
		grant, err = confine.CaptureSocketGrant(ctx, grant)
		testutil.FailErr(t, "retain admitted service", err)
		grants = append(grants, grant)
	}
	boundary := &confine.Confinement{Roots: []string{project}, Network: confine.NetworkDeny, SocketGrants: grants}
	identity := confine.EgressCommand{SessionID: "descriptor-transport-fixture", ToolName: "command"}
	lease, err := confine.BindAction(boundary, identity)
	testutil.FailErr(t, "bind maximum admitted socket set", err)
	defer lease.Close(context.WithoutCancel(ctx))
	launch := AgentLaunch(LaunchAgentCommand, "command", boundary)
	launch.CurrentAuthority = func(ctx context.Context) error { return lease.ValidateLaunch(ctx, boundary, identity) }
	program, err := exec.LookPath("true")
	testutil.FailErr(t, "locate descriptor transport target", err)
	cmd, release, err := prepareNativeCommand(ctx, program, nil, ExecOpts{Launch: launch, Dir: project}, nil, false)
	testutil.FailErr(t, "prepare maximum admitted socket launch", err)
	defer release()
	cmd, closeSupervisor, err := superviseCommand(cmd, func() {}, ProcessPriorityNormal)
	testutil.FailErr(t, "frame maximum admitted socket launch", err)
	defer closeSupervisor()
	layout, argv, ok := parseRelayTarget(cmd.Path, cmd.Args[1:])
	if !ok {
		t.Fatalf("maximum admitted launch cannot be represented by the broker: %q", cmd.Args)
	}
	request := launchbroker.Request{Version: launchbroker.Version, Argv: argv, Dir: project, ExtraFiles: len(layout)}
	testutil.FailErr(t, "admit complete maximum socket launch transport", request.Validate())
	if len(layout) != len(cmd.ExtraFiles) {
		t.Fatalf("descriptor layout=%d, actual=%d", len(layout), len(cmd.ExtraFiles))
	}
}
