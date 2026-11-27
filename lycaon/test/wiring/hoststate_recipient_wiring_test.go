package wiring

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/decide/decidetest"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

// Each registered approval posture resolves through the approval store for a
// coordinator and its worker child, and each recipient's host facts carry the
// review rule of the posture resolved for that session.
func TestApprovalPostureResolvesForEachRecipient(t *testing.T) {
	h := BuildForTest(t)
	ctx := context.Background()
	dir := h.ProjectDir(t, "approval-posture")
	// A project setting can only tighten the device posture.
	approvals := h.Server.Admin.Settings.Service.Approvals
	testutil.FailErr(t, "set device posture", approvals.PutGlobal(settings.ApprovalConfig{Posture: gate.PostureLight}))
	parent, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create coordinator session", err)
	child, err := h.Sessions.Manager.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{AgentType: orchestration.ProfileImplementer, Prompt: "survey"})
	testutil.FailErr(t, "spawn worker session", err)

	reviews := map[gate.Posture]map[bool]bool{}
	for _, posture := range gate.AllPostures() {
		testutil.FailErr(t, "set project posture", approvals.PutProject(dir, settings.ApprovalConfig{Posture: posture}))
		reviews[posture] = map[bool]bool{}
		for _, sess := range []*api.Session{parent, child} {
			resolved := h.Sessions.Manager.Profiles.ProjectApprovalPosture(ctx, sess)
			if resolved != posture {
				t.Fatalf("session %s resolved approval %q under project approval %q", sess.ID, resolved, posture)
			}
			host, err := h.Sessions.Manager.ReaderFacts.HostFacts(ctx, sess)
			testutil.FailErr(t, "host facts", err)
			if host.ReviewsConfinedWrites != resolved.ReviewsConfinedWrites() {
				t.Errorf("session %s under %q: host facts review confined writes %t, resolved posture %t",
					sess.ID, resolved, host.ReviewsConfinedWrites, resolved.ReviewsConfinedWrites())
			}
			reviews[posture][host.ReviewsConfinedWrites] = true
			t.Logf("recipient=%s resolved=%s reviews_confined_writes=%t", recipientOf(sess), resolved, host.ReviewsConfinedWrites)
		}
	}
	outcomes := map[bool]bool{}
	for posture, seen := range reviews {
		if len(seen) != 1 {
			t.Errorf("approval %q reached the recipients with different review rules %v", posture, seen)
		}
		for v := range seen {
			outcomes[v] = true
		}
	}
	if len(outcomes) != 2 {
		t.Fatalf("approval postures delivered review rules %v; the sweep needs both", outcomes)
	}
}

func recipientOf(sess *api.Session) string {
	if sess.IsWorkerChild() {
		return "worker"
	}
	return "coordinator"
}

// markGitRoot gives dir the .git entry production repository discovery reads.
func markGitRoot(t *testing.T, dir string) {
	t.Helper()
	testutil.FailErr(t, "create .git", os.Mkdir(filepath.Join(dir, ".git"), 0o755))
}

// The primary-root and any-root Git facts follow the attached roots for a
// coordinator and its worker child in every feasible two-root membership.
func TestMultiRootGitFactsReachEachRecipient(t *testing.T) {
	h := BuildForTest(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name                  string
		primaryGit, secondGit bool
	}{
		{name: "neither"},
		{name: "secondary only", secondGit: true},
		{name: "both", primaryGit: true, secondGit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			primary, secondary := t.TempDir(), t.TempDir()
			for _, dir := range []string{primary, secondary} {
				if root, ok := gitrepo.Discover(dir); ok {
					t.Fatalf("fixture root %s is already inside repository %s", dir, root.Root)
				}
			}
			if tc.primaryGit {
				markGitRoot(t, primary)
			}
			if tc.secondGit {
				markGitRoot(t, secondary)
			}
			projectID := uuid.NewString()
			testdbseed.InsertProjectRoot(t, h.DB, projectID, primary)
			testdbseed.InsertProjectRoot(t, h.DB, projectID, secondary)
			parent, err := h.Store.Sessions.Create(ctx, api.CreateSessionRequest{ProjectID: projectID}, projectID)
			testutil.FailErr(t, "create coordinator session", err)
			child, err := h.Sessions.Manager.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{AgentType: orchestration.ProfileImplementer, Prompt: "survey"})
			testutil.FailErr(t, "spawn worker session", err)
			for _, sess := range []*api.Session{parent, child} {
				host, err := h.Sessions.Manager.ReaderFacts.HostFacts(ctx, sess)
				testutil.FailErr(t, "host facts", err)
				if host.RootCount != 2 || host.HasGit != tc.primaryGit || host.HasAnyGitRoot != (tc.primaryGit || tc.secondGit) {
					t.Errorf("%s: roots=%d has_git=%t has_any_git_root=%t, want roots=2 has_git=%t has_any_git_root=%t",
						recipientOf(sess), host.RootCount, host.HasGit, host.HasAnyGitRoot, tc.primaryGit, tc.primaryGit || tc.secondGit)
				}
				t.Logf("recipient=%s roots=%d has_git=%t has_any_git_root=%t", recipientOf(sess), host.RootCount, host.HasGit, host.HasAnyGitRoot)
			}
		})
	}
}

// workerLoadRecorder records worker requests and, on the first, loads the
// first of the worker's deferred tools by exact name.
type workerLoadRecorder struct {
	mu        sync.Mutex
	deferred  func(modelcall.CompletionRequest) []string
	requests  []modelcall.CompletionRequest
	candidate []string
}

func (m *workerLoadRecorder) Complete(_ context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if !SchemaHasTool(req.Tools, "complete_leg") {
		return &modelcall.Completion{Content: "Done."}, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, req)
	if len(m.requests) > 1 {
		return &modelcall.Completion{Content: "Done."}, nil
	}
	m.candidate = m.deferred(req)
	if len(m.candidate) < 2 {
		return &modelcall.Completion{Content: "Done."}, nil
	}
	return &modelcall.Completion{ToolCalls: []api.ToolCall{{ID: "load", Name: "request_tools", Args: map[string]any{"need": m.candidate[0]}}}}, nil
}

func (m *workerLoadRecorder) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	completion, err := m.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan modelcall.StreamChunk, 1)
	ch <- modelcall.StreamChunk{Content: completion.Content, ToolCalls: completion.ToolCalls, Done: true}
	close(ch)
	return ch, nil
}

func offeredNames(req modelcall.CompletionRequest) []string {
	names := make([]string, 0, len(req.Tools))
	for _, meta := range req.Tools {
		names = append(names, meta.Name)
	}
	return names
}

// A worker whose turn loading can rank keeps its deferred tools withheld; one
// it loads by name is offered on its next request while the others stay
// deferred.
func TestWorkerPartialLoadingOffersOnlyTheLoadedTool(t *testing.T) {
	var h *Harness
	mock := &workerLoadRecorder{}
	mock.deferred = func(req modelcall.CompletionRequest) []string {
		sess, err := h.Store.Sessions.Get(context.Background(), req.Debug.SessionID)
		if err != nil {
			return nil
		}
		offered := offeredNames(req)
		var out []string
		for _, meta := range h.Sessions.Manager.Coordinator.Guards.Policy().ListForPrompt(context.Background(), sess, req.Debug.ProfileID) {
			if meta.Deferred && !slices.Contains(offered, meta.Name) {
				out = append(out, meta.Name)
			}
		}
		slices.Sort(out)
		return out
	}
	h = BuildForTest(t, WithLLMClient(mock), WithDecider(&decidetest.Fake{}))
	ctx := context.Background()
	dir := t.TempDir()
	parent, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create parent", err)
	task := api.WorkerTask{
		ParentSessionID: parent.ID, Prompt: "Survey the project.", Brief: "fixture", AgentType: orchestration.ProfileImplementer,
		Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"."}},
	}
	testutil.FailErr(t, "worker defaults", worker.ApplyEnqueueDefaults(
		&task, project.ProjectScope{ProjectID: parent.ProjectID, WorkspacePath: dir}, worker.DefaultWorkersConfig(),
	))
	_, err = h.Workers.Queue.Enqueue(ctx, task)
	testutil.FailErr(t, "enqueue worker", err)
	claimed, err := h.Workers.Queue.ClaimNext(ctx, worker.ClaimRequest{
		ProjectID: parent.ProjectID, ExecutionTarget: api.ExecutionTargetLocal, ClaimedBy: "partial-loading-test",
	})
	testutil.FailErr(t, "claim worker", err)
	_, err = WiringWorkerExecutor(h).Execute(ctx, *claimed, worker.RunContextForTask(*claimed, dir))
	testutil.FailErr(t, "execute worker", err)

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.candidate) < 2 {
		t.Fatalf("the worker's first request left %v deferred; partial loading needs two", mock.candidate)
	}
	if len(mock.requests) < 2 {
		t.Fatalf("worker sent %d requests, want the load request and the one after it", len(mock.requests))
	}
	loaded, still := mock.candidate[0], mock.candidate[1]
	next := offeredNames(mock.requests[1])
	if !slices.Contains(next, loaded) {
		t.Fatalf("request after loading %q lacks it: %v", loaded, next)
	}
	if slices.Contains(next, still) {
		t.Fatalf("request after loading %q also offers deferred %q: %v", loaded, still, next)
	}
	t.Logf("deferred on first request=%v loaded=%s still_deferred=%s", mock.candidate, loaded, still)
}
