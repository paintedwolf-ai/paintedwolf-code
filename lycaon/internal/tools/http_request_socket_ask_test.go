package tools_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

// An http_request unix_socket is daemon authority: with no grant, the call
// raises the exact socket card before the handler runs.
func TestHTTPRequestUnixSocketAsksForExactSocketAuthority(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "lyc-ask-") //nolint:usetesting // Socket paths must stay short.
	testutil.FailErr(t, "mkdir socket dir", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "daemon.sock")
	listener, err := net.Listen("unix", sock)
	testutil.FailErr(t, "listen unix", err)
	t.Cleanup(func() { _ = listener.Close() })
	grant, err := confine.ResolveSocketRequest(sock)
	testutil.FailErr(t, "resolve socket", err)

	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
	testutil.FailErr(t, "create approval store", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	registry := tools.NewDefaultRegistry()
	entered := make(chan struct{}, 1)
	testutil.FailErr(t, "register http_request", registry.Register("http_request", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		entered <- struct{}{}
		return "{}", nil
	}))
	executor := tools.NewDefaultToolExecutor(tools.NewApprovalPolicyEngine(tools.NewProfilePolicyEngine(boundary), gate), registry, "implement")
	manager := &asyncHITL{requested: make(chan struct{}, 4)}
	executor.SetCheckpointManager(manager, gate)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	tc := tools.ToolContext{Roots: []projectroot.RootRef{{ID: "root", Path: dir, IsPrimary: true}}, ActiveRootID: "root",
		SessionID: "task", ToolCallID: "call", Agent: "implement"}
	done := make(chan error, 1)
	go func() {
		_, err := executor.Invoke(ctx, "http_request", map[string]any{"url": "http://docker.invalid/info", "unix_socket": "daemon.sock"}, tc)
		done <- err
	}()

	select {
	case <-manager.requested:
	case err := <-done:
		t.Fatalf("returned without asking: %v", err)
	case <-ctx.Done():
		t.Fatal("no socket card raised")
	}
	select {
	case <-entered:
		t.Fatal("handler ran before the socket was approved")
	default:
	}
	card := manager.request.SocketCapability
	if card == nil || len(card.Targets) != 1 || card.Targets[0].ResolvedPath != grant.ResolvedPath {
		t.Fatalf("socket card = %+v, want %s", card, grant.ResolvedPath)
	}
	if manager.request.Explanation == nil || manager.request.Explanation.Who != hitl.WhoAgentAction {
		t.Fatalf("explanation = %+v, want the host-dialed socket copy", manager.request.Explanation)
	}
	cancel()
	<-done
}
