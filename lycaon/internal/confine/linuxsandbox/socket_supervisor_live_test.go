//go:build linux

package linuxsandbox_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/api/capabilityadmin"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/capabilitygrants"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/confine/linuxsandbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/apitestdeps"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
	"golang.org/x/sys/unix"
)

// TestLinuxRetainedSocketTarget is re-executed only inside the actual boundary.
func TestLinuxRetainedSocketTarget(t *testing.T) {
	if os.Getenv("PW_SOCKET_REFERENCE_TARGET") != "1" {
		return
	}
	kind, err := strconv.Atoi(os.Getenv("PW_SOCKET_REFERENCE_KIND"))
	testutil.FailErr(t, "target socket kind", err)
	fd, err := unix.Socket(unix.AF_UNIX, kind|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "target creates socket", err)
	defer unix.Close(fd)
	testutil.FailErr(t, "target supervised connect", unix.Connect(fd, &unix.SockaddrUnix{Name: os.Getenv("PW_SOCKET_REFERENCE_PATH")}))
	_, err = unix.Write(fd, []byte("approved-original"))
	testutil.FailErr(t, "target sends marker", err)
}

func TestLinuxApprovedSocketSupervisorCannotReachSwappedListener(t *testing.T) {
	for _, kind := range []int{unix.SOCK_STREAM, unix.SOCK_SEQPACKET} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			root, err := os.MkdirTemp("/tmp", "sv-") //nolint:usetesting // Kernel socket addresses are bounded.
			testutil.FailErr(t, "supervisor fixture", err)
			defer os.RemoveAll(root)
			workspace := filepath.Join(root, "workspace")
			testutil.FailErr(t, "supervisor workspace", os.Mkdir(workspace, 0700))
			t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(root, "state"))
			path := filepath.Join(root, "service")
			network := "unix"
			if kind == unix.SOCK_SEQPACKET {
				network = "unixpacket"
			}
			original, err := net.ListenUnix(network, &net.UnixAddr{Name: path, Net: network})
			testutil.FailErr(t, "original listener", err)
			original.SetUnlinkOnClose(false)
			defer original.Close()
			ctx, scope := socketref.WithScope(t.Context())
			defer scope.Close()
			grant, runtime, actionDigest := approveSupervisorSocket(t, ctx, workspace, path)
			tc := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "root-1", ToolCallID: "call"}, Socket: tools.InvocationSocket{SocketGrants: []confine.SocketGrant{grant}, SocketCapabilityRuntime: runtime, SocketActionDigest: actionDigest}}
			finalized, reject := tools.FinalizeSocketGrantsForSpawn(ctx, tc)
			if reject != nil {
				t.Fatalf("real permit transfer: %v", reject)
			}
			c := confine.Confinement{Roots: []string{workspace}, Network: confine.NetworkDeny, EgressOff: confine.EgressOffOverride, SocketGrants: finalized}
			self, err := os.Executable()
			testutil.FailErr(t, "test executable", err)
			command, cleanup, err := confine.Command(ctx, self, self, []string{"-test.run=^TestLinuxRetainedSocketTarget$"}, c)
			testutil.FailErr(t, "real command descriptor handoff", err)
			defer cleanup()
			profile, err := confine.BuildProfile(c)
			testutil.FailErr(t, "real boundary plan", err)
			plan, err := linuxsandbox.DecodePlan([]byte(profile))
			testutil.FailErr(t, "decode actual plan", err)
			source := command.ExtraFiles[len(command.ExtraFiles)-1]
			inherited, err := unix.FcntlInt(source.Fd(), unix.F_DUPFD_CLOEXEC, linuxsandbox.SocketReferenceFD)
			testutil.FailErr(t, "outer source reference", err)
			defer unix.Close(inherited)
			outer := plan.Network
			outer.SocketGrants[0].FD = inherited
			paused, resume := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(resume) })
			outer = linuxsandbox.WithSocketConnectPauseForTest(outer, func() { close(paused); <-resume })
			t.Setenv("PW_SOCKET_REFERENCE_TARGET", "1")
			t.Setenv("PW_SOCKET_REFERENCE_KIND", strconv.Itoa(kind))
			t.Setenv("PW_SOCKET_REFERENCE_PATH", path)
			completed := make(chan error, 1)
			go func() {
				code, signal, err := linuxsandbox.Relaunch(self, command.Args[1:], []byte(profile), outer, nil, nil)
				if err == nil && (code != 0 || signal != 0) {
					err = fmt.Errorf("helper exit %d, signal %v", code, signal)
				}
				completed <- err
			}()
			joined := false
			defer func() {
				once.Do(func() { close(resume) })
				if !joined {
					<-completed
				}
			}()
			select {
			case <-paused:
			case err := <-completed:
				joined = true
				t.Fatalf("real supervisor did not reach connect pause: %v", err)
			case <-t.Context().Done():
				t.Fatal("real supervisor canceled before pause")
			}
			testutil.FailErr(t, "swap final name after supervisor resolution", os.Rename(path, path+"-original"))
			decoy, err := net.ListenUnix(network, &net.UnixAddr{Name: path, Net: network})
			testutil.FailErr(t, "replacement listener", err)
			defer decoy.Close()
			once.Do(func() { close(resume) })
			completionErr := <-completed
			joined = true
			testutil.FailErr(t, "real supervised command completion", completionErr)
			originalRaw, err := original.SyscallConn()
			testutil.FailErr(t, "original descriptor", err)
			originalReady := 0
			var originalPollErr error
			testutil.FailErr(t, "inspect original readiness", originalRaw.Control(func(fd uintptr) {
				originalReady, originalPollErr = unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 0)
			}))
			testutil.FailErr(t, "original readiness", originalPollErr)
			if originalReady != 1 {
				t.Fatal("actual target did not queue a connection at the retained original")
			}
			accepted, err := original.AcceptUnix()
			testutil.FailErr(t, "original accepts actual target", err)
			marker := make([]byte, len("approved-original"))
			_, err = io.ReadFull(accepted, marker)
			_ = accepted.Close()
			testutil.FailErr(t, "original receives target bytes", err)
			if string(marker) != "approved-original" {
				t.Fatalf("original marker=%q", marker)
			}
			raw, err := decoy.SyscallConn()
			testutil.FailErr(t, "decoy descriptor", err)
			var ready int
			var pollErr error
			testutil.FailErr(t, "inspect actual decoy", raw.Control(func(fd uintptr) { ready, pollErr = unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 0) }))
			testutil.FailErr(t, "decoy readiness", pollErr)
			if ready != 0 {
				t.Fatal("real supervisor connected to replacement listener")
			}
		})
	}
}

func approveSupervisorSocket(t *testing.T, ctx context.Context, workspace, path string) (confine.SocketGrant, *approvalstate.SocketCapabilityRuntime, string) {
	t.Helper()
	database := testdbfixture.Open(t, "supervisor-approval.db")
	testdbseed.InsertSessionWithRoot(t, database, "root-1", testdbseed.DefaultProjectID, workspace)
	human := testdbseed.OwnerCaller(t, t.Context(), database)
	checkpoints := hitl.NewCheckpoints(hitl.NewSQLStore(database), &events.Publisher{Hub: events.NewMemoryHub()}, authzcontext.SQLRecorder(database))
	t.Cleanup(func() {
		testutil.FailErr(t, "stop supervisor checkpoints", checkpoints.StopExpiryTimers(context.Background()))
	})
	runtime := approvalstate.NewSocketCapabilityRuntime()
	t.Cleanup(runtime.Close)
	store, err := settings.NewApprovalStoreAt(filepath.Join(filepath.Dir(path), "approvals.yaml"))
	testutil.FailErr(t, "supervisor approval store", err)
	t.Cleanup(store.Close)
	approvalGate := settings.NewRuleApprovalGate(store, settings.NoSources())
	deps := apitestdeps.Deps{Database: database, Checkpoints: checkpoints, ApprovalGate: approvalGate, Settings: &settings.Service{Approvals: store}}
	apitestdeps.Fill(t, &deps)
	handler := capabilityadmin.New(&httpio.Responder{}, capabilityadmin.Deps{Checkpoints: checkpoints, Gate: approvalGate, Settings: deps.Settings, Store: deps.Store, Projects: deps.Projects, Authority: capabilityadmin.Authority{ApprovalDecisions: deps.ApprovalDecisions, Sockets: runtime}})
	checkpoints.Authority.SetApprovalAuthorityInstaller(handler.Installation)
	grant, err := confine.ResolveSocketRequest(path)
	testutil.FailErr(t, "resolve approval target", err)
	grant, err = confine.CaptureSocketGrant(ctx, grant)
	testutil.FailErr(t, "capture actual approval target", err)
	action := hitl.ProposedAction{Invocation: hitl.ActionInvocation{Tool: "command", Args: map[string]any{"command": "fixture-client"}}, Scope: hitl.ActionScope{SessionID: "root-1", RootSessionID: "root-1", ProjectID: testdbseed.DefaultProjectID, ProjectDir: workspace}, Sockets: hitl.ActionSockets{SocketGrants: []confine.SocketGrant{grant}}}
	_, decision := gate.Evaluate(gate.Facts{Stage: gate.StagePreSpawn, Ran: gate.ProducerApprovalRequest, ApprovalRequest: &gate.ApprovalRequest{Count: 1}}, gate.DefaultPosture)
	request := hitl.CheckpointRequest{Decision: decision, SessionID: "root-1", ProjectID: testdbseed.DefaultProjectID, ToolCallID: "call", Kind: wire.CheckpointKindToolApproval, ProposedAction: &action, SocketCapability: &hitl.SocketCapability{Targets: []hitl.SocketCapabilityTarget{{ApprovedPath: grant.ApprovedPath, ResolvedPath: grant.ResolvedPath, Binding: grant.Binding}}, EffectiveAuthority: hitl.SocketAuthorityOutsideSandboxDaemon}, GrantOffers: capabilitygrants.SocketExecutionGrantOffers(action, []confine.SocketGrant{grant})}
	pending, err := checkpoints.RequestCheckpoint(ctx, request)
	testutil.FailErr(t, "persist real socket approval", err)
	_, err = checkpoints.Authority.ResolveApprovalOption(human, "root-1", pending.CheckpointID, "approve_socket_set_once")
	testutil.FailErr(t, "resolve human socket approval", err)
	return grant, runtime, hitl.GrantKey(action)
}
