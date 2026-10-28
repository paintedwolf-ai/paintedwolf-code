package security

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/resourcelifecycle"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/protection"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/tools"
)

type directAuditFixture struct {
	records  []authzledger.DirectIPLifecycleRecord
	canceled bool
}

func (a *directAuditFixture) AppendDirectIPLifecycle(ctx context.Context, record authzledger.DirectIPLifecycleRecord) {
	a.records = append(a.records, record)
	a.canceled = ctx.Err() != nil
}

func TestExceptionalCapabilitiesReleasePermitsAtRunEndAndGrantsAtChatDisposal(t *testing.T) {
	host := session.NewHost(store.NewMemory(), session.Models{Limits: settings.DefaultSessionLimits()}, tools.NewStubRegistry())
	control := &toolexecution.Capabilities{}
	var runtime Runtime
	audit := &directAuditFixture{}
	var recovered protection.DirectIPReconstructHook
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	previous := tools.ExternalAccessBuilder
	t.Cleanup(func() { tools.SetExternalAccessBuilder(previous) })
	testutil.FailErr(t, "bind exceptional capabilities", runtime.BuildExceptional(ctx, control, nil, func(string) bool { return false }, host.Resources, audit, func(h protection.DirectIPReconstructHook) { recovered = h }))
	socket, owner := exceptionalSocketFixture(t)
	t.Cleanup(runtime.Sockets.Close)
	if !runtime.Sockets.GrantChat("chat", socket, "socket-grant", "checkpoint", "digest", nil) {
		t.Fatal("socket grant was not installed")
	}
	owner.Close()
	runtime.Sockets.IssuePermit("chat", "chat", "call", "digest", socket)
	lease := hitl.DirectIPLease{ActionDigest: "action", RequestDigest: "request", ConfinementDigest: "roots", ChatConfinementDigest: "chat-roots"}
	if !runtime.DirectIP.GrantChat("chat", lease, "ip-grant", "checkpoint", nil) {
		t.Fatal("direct-IP grant was not installed")
	}
	runtime.DirectIP.IssuePermit("chat", "call", "action", "request", "roots")
	testutil.FailErr(t, "release run resources", host.Resources.Registry.Release(t.Context(), resourcelifecycle.SessionScope("chat")))
	if ok, _ := runtime.Sockets.ConsumePermit("chat", "call", "digest", socket); ok {
		t.Fatal("run cleanup retained socket permit")
	}
	if runtime.DirectIP.Authorized("chat", "call", "action") {
		t.Fatal("run cleanup retained direct-IP permit")
	}
	if len(runtime.Sockets.ListChatGrants("chat")) != 1 || len(runtime.DirectIP.ListChatGrants("chat")) != 1 {
		t.Fatal("run cleanup revoked reusable chat grants")
	}
	host.Resources.Dispose(t.Context(), "chat")
	if lease, err := socket.Binding.Acquire(); !errors.Is(err, socketref.ErrUnbound) {
		if lease != nil {
			lease.Close()
		}
		t.Fatalf("chat disposal retained socket reference: %v", err)
	}
	if len(runtime.Sockets.ListChatGrants("chat")) != 0 || len(runtime.DirectIP.ListChatGrants("chat")) != 0 {
		t.Fatal("chat disposal retained exceptional grants")
	}
	recovered("chat", "worker", "ignored")
	if len(audit.records) != 1 || audit.records[0].SessionID != "chat" || !audit.records[0].Background || len(audit.records[0].DeclaredDestinations) != 0 || audit.canceled {
		t.Fatalf("recovery audit lost facts or inherited cancellation: %+v canceled=%v", audit.records, audit.canceled)
	}
	access := tools.ExternalAccessBuilder(tools.ExternalAccessBuildInput{Direct: true, DeclaredDestinations: []string{"203.0.113.10:443"}, Endpoints: []tools.ExternalAccessEndpointFact{{Host: "203.0.113.10", Port: 443, Transport: "tcp", Allowed: true, Attempts: 1}}, Sockets: []tools.ExternalAccessSocketFact{{ApprovedPath: socket.ApprovedPath, ResolvedPath: socket.ResolvedPath, Scope: "chat"}}})
	if access == nil {
		t.Fatal("applied exceptional access produced no attributable result")
	}
}

func exceptionalSocketFixture(t *testing.T) (confine.SocketGrant, *socketref.Lease) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "ex-") //nolint:usetesting // Kernel Unix socket paths are bounded.
	testutil.FailErr(t, "exceptional socket directory", err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(root, "state"))
	path := filepath.Join(root, "service")
	listener, err := net.Listen("unix", path)
	testutil.FailErr(t, "exceptional listener", err)
	t.Cleanup(func() { _ = listener.Close() })
	grant, err := confine.ResolveSocketRequest(path)
	testutil.FailErr(t, "exceptional socket label", err)
	owner, err := socketref.Capture(path)
	testutil.FailErr(t, "exceptional socket reference", err)
	t.Cleanup(owner.Close)
	grant.Binding = owner.Binding()
	return grant, owner
}

type failingExceptionalLifetime struct{ err error }

func (l failingExceptionalLifetime) RegisterCleanup(string, int, func(context.Context, string) error) error {
	return l.err
}
func (l failingExceptionalLifetime) RegisterDisposal(string, int, func(context.Context, string) error) error {
	return l.err
}

func TestExceptionalSocketRuntimeClosesOnPartialWiringFailure(t *testing.T) {
	failure := errors.New("session resources unavailable")
	var runtime Runtime
	err := runtime.BuildExceptional(t.Context(), &toolexecution.Capabilities{}, nil, nil, failingExceptionalLifetime{err: failure}, nil, nil)
	if !errors.Is(err, failure) {
		t.Fatalf("wiring failure=%v", err)
	}
	grant, _ := exceptionalSocketFixture(t)
	if runtime.Sockets.GrantChat("chat", grant, "grant", "checkpoint", "digest", nil) {
		t.Fatal("failed wiring left socket authority open")
	}
}
