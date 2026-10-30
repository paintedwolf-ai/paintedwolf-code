//go:build linux

package linuxsandbox_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/lycaon/lycaon/internal/api/capabilityadmin"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/capabilitygrants"
	"github.com/lycaon/lycaon/internal/confine"
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

func TestLinuxApprovedSocketSupervisorCannotReachSwappedListener(t *testing.T) {
	for _, kind := range []int{unix.SOCK_STREAM, unix.SOCK_SEQPACKET} {
		for _, transition := range []string{"replacement", "unlink", "ancestor", "symlink", "restart", "cancel"} {
			t.Run(fmt.Sprintf("%d/%s", kind, transition), func(t *testing.T) {
				root, err := os.MkdirTemp("/tmp", "sv-") //nolint:usetesting // Kernel socket addresses are bounded.
				testutil.FailErr(t, "supervisor fixture", err)
				defer os.RemoveAll(root)
				workspace := filepath.Join(root, "workspace")
				testutil.FailErr(t, "supervisor workspace", os.Mkdir(workspace, 0700))
				t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(root, "state"))
				socketDir := filepath.Join(root, "listener")
				testutil.FailErr(t, "listener parent", os.Mkdir(socketDir, 0700))
				path := filepath.Join(socketDir, "service")
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
				python := "/usr/bin/python3"
				client := "import socket,sys; s=socket.socket(socket.AF_UNIX,int(sys.argv[2])); s.connect(sys.argv[1]); s.sendall(b'approved-original')"
				command, cleanup, err := confine.Command(ctx, self, python, []string{"-c", client, path, strconv.Itoa(kind)}, c)
				testutil.FailErr(t, "real command descriptor handoff", err)
				defer cleanup()
				process := startSocketSupervisorProcess(t, command)
				process.awaitPause(t)
				if transition == "cancel" {
					process.close()
					assertSocketListenerEmpty(t, original)
					cleanup()
					runtime.Close()
					scope.Close()
					lease, err := grant.Binding.Acquire()
					if lease != nil {
						lease.Close()
					}
					if !errors.Is(err, socketref.ErrUnbound) {
						t.Fatalf("canceled invocation retained authority: %v", err)
					}
					for _, file := range command.ExtraFiles {
						if file != nil {
							if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
								t.Fatalf("command cleanup retained inherited descriptor: %v", err)
							}
						}
					}
					return
				}
				if transition == "ancestor" {
					testutil.FailErr(t, "move listener ancestor", os.Rename(socketDir, socketDir+"-original"))
					testutil.FailErr(t, "replace listener ancestor", os.Mkdir(socketDir, 0700))
				} else {
					testutil.FailErr(t, "swap final name after supervisor resolution", os.Rename(path, path+"-original"))
				}
				if transition == "unlink" {
					testutil.FailErr(t, "unlink retained listener name", os.Remove(path+"-original"))
				}
				decoyPath := path
				if transition == "symlink" {
					decoyPath += "-decoy"
				}
				decoy, err := net.ListenUnix(network, &net.UnixAddr{Name: decoyPath, Net: network})
				testutil.FailErr(t, "replacement listener", err)
				defer decoy.Close()
				if transition == "symlink" {
					testutil.FailErr(t, "replace approved name with symlink", os.Symlink(decoyPath, path))
				}
				if transition == "restart" {
					testutil.FailErr(t, "stop approved listener", original.Close())
					process.resumeAndWaitResult(t, false)
					assertSocketListenerEmpty(t, decoy)
					return
				}
				process.resumeAndWaitResult(t, true)
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
}

func assertSocketListenerEmpty(t *testing.T, listener *net.UnixListener) {
	t.Helper()
	raw, err := listener.SyscallConn()
	testutil.FailErr(t, "listener descriptor", err)
	var ready int
	var pollErr error
	testutil.FailErr(t, "inspect listener", raw.Control(func(fd uintptr) {
		ready, pollErr = unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 0)
	}))
	testutil.FailErr(t, "listener readiness", pollErr)
	if ready != 0 {
		t.Fatal("unexpected connection reached listener")
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
