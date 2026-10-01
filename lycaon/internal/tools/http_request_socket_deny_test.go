package tools_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

// denySocketHITL raises every card and denies it.
type denySocketHITL struct {
	*asyncHITL
	mu       sync.Mutex
	requests []hitl.CheckpointRequest
}

func (m *denySocketHITL) RequestCheckpoint(ctx context.Context, request hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	m.mu.Lock()
	m.requests = append(m.requests, request)
	m.mu.Unlock()
	return m.asyncHITL.RequestCheckpoint(ctx, request)
}

func (m *denySocketHITL) PollCheckpoint(_ context.Context, checkpointID string) (*hitl.CheckpointResponse, error) {
	return &hitl.CheckpointResponse{CheckpointID: checkpointID, Status: hitl.DecisionStatusRejected, Result: &hitl.DecisionResult{Approved: false}}, nil
}

func (m *denySocketHITL) socketCards() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, request := range m.requests {
		if request.SocketCapability != nil {
			n++
		}
	}
	return n
}

// recordingSocketRuntime holds no grants and records any the executor installs.
type recordingSocketRuntime struct {
	mu      sync.Mutex
	granted []confine.SocketGrant
	permits []confine.SocketGrant
}

func (r *recordingSocketRuntime) AppliedGrants(string) []confine.SocketGrant {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]confine.SocketGrant(nil), r.granted...)
}

func (r *recordingSocketRuntime) AuthorizedGrants(_, _, _, _ string, requested []confine.SocketGrant) []confine.SocketGrant {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []confine.SocketGrant
	for _, want := range requested {
		for _, held := range append(append([]confine.SocketGrant(nil), r.granted...), r.permits...) {
			if held.ResolvedPath == want.ResolvedPath {
				out = append(out, want)
				break
			}
		}
	}
	return out
}

func (r *recordingSocketRuntime) GrantChat(_ string, g confine.SocketGrant, _, _, _ string, _ *time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.granted = append(r.granted, g)
}

func (r *recordingSocketRuntime) IssuePermit(_, _, _ string, g confine.SocketGrant) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.permits = append(r.permits, g)
}

func (r *recordingSocketRuntime) ConsumePermit(string, string, string, confine.SocketGrant) (bool, error) {
	return false, nil
}

// A denied http_request socket card installs no authority: the handler never
// runs, nothing is granted or saved, and the retry asks again.
func TestHTTPRequestUnixSocketDenialRecordsNoGrantAndRetryAsksAgain(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "lyc-deny-") //nolint:usetesting // Socket paths must stay short.
	testutil.FailErr(t, "mkdir socket dir", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "daemon.sock")
	listener, err := net.Listen("unix", sock)
	testutil.FailErr(t, "listen unix", err)
	t.Cleanup(func() { _ = listener.Close() })

	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
	testutil.FailErr(t, "create approval store", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	registry := tools.NewDefaultRegistry()
	handlerRuns := 0
	testutil.FailErr(t, "register http_request", registry.Register("http_request", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		handlerRuns++
		return "{}", nil
	}))
	executor := tools.NewDefaultToolExecutor(tools.NewApprovalPolicyEngine(tools.NewProfilePolicyEngine(boundary), gate), registry, "implement")
	manager := &denySocketHITL{asyncHITL: &asyncHITL{requested: make(chan struct{}, 8)}}
	executor.SetCheckpointManager(manager, gate)
	runtime := &recordingSocketRuntime{}
	executor.SetSocketCapabilityRuntime(runtime)
	rulesBefore := len(store.GlobalRules())

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	args := map[string]any{"url": "http://docker.invalid/info", "unix_socket": "daemon.sock"}
	for attempt, call := range []string{"call-1", "call-2"} {
		tc := tools.ToolContext{Roots: []projectroot.RootRef{{ID: "root", Path: dir, IsPrimary: true}}, ActiveRootID: "root",
			SessionID: "task", ToolCallID: call, ProjectID: "project", Agent: "implement"}
		_, err := executor.Invoke(ctx, "http_request", args, tc)
		reject := tools.AsToolReject(err)
		if reject == nil || reject.Code != isolation.CodeSocketPathDenied {
			t.Fatalf("attempt %d: err = %v, want %s from the denied socket card", attempt+1, err, isolation.CodeSocketPathDenied)
		}
		if got := manager.socketCards(); got != attempt+1 {
			t.Fatalf("attempt %d raised %d socket cards in total, want %d: a denial must not satisfy the retry", attempt+1, got, attempt+1)
		}
	}
	if handlerRuns != 0 {
		t.Fatalf("handler ran %d times after the socket card was denied", handlerRuns)
	}
	if len(runtime.granted) != 0 || len(runtime.permits) != 0 {
		t.Fatalf("denial installed socket authority: task grants=%+v permits=%+v", runtime.granted, runtime.permits)
	}
	if grants := store.GlobalGrants(); len(grants) != 0 {
		t.Fatalf("denial saved approval grants: %+v", grants)
	}
	if sockets := store.SocketPathsForProject("project"); len(sockets) != 0 {
		t.Fatalf("denial saved project socket paths: %+v", sockets)
	}
	if rules := len(store.GlobalRules()); rules != rulesBefore {
		t.Fatalf("denial changed saved approval rules: %d → %d", rulesBefore, rules)
	}
}
