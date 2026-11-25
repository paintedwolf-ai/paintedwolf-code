package wiring

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

// requestReader projects sess's next request through the host.
func requestReader(t *testing.T, h *Harness, sess *api.Session) prompts.Reader {
	t.Helper()
	reader, err := h.Sessions.Manager.Coordinator.Context.NextRequestReader(t.Context(), sess, nil)
	testutil.FailErr(t, "next request reader", err)
	return reader
}

// Every registered approval posture resolves as itself for both recipients, and
// each recipient's host facts and request Reader carry its review rule.
func TestApprovalPostureReachesCoordinatorAndWorkerReaders(t *testing.T) {
	h := BuildForTest(t)
	ctx := context.Background()
	dir := h.ProjectDir(t, "approval-posture")
	// The project setting can only tighten the device posture.
	testutil.FailErr(t, "set device posture", h.Server.Admin.Settings.Service.Approvals.PutGlobal(settings.ApprovalConfig{Posture: gate.PostureLight}))
	parent, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create coordinator session", err)
	child, err := h.Sessions.Manager.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{AgentType: orchestration.ProfileImplementer, Prompt: "survey"})
	testutil.FailErr(t, "spawn worker session", err)
	recipients := []struct {
		sess  *api.Session
		agent string
	}{{parent, "coordinator"}, {child, orchestration.ProfileImplementer}}

	outcomes := map[bool]bool{}
	for _, posture := range gate.AllPostures() {
		testutil.FailErr(t, "set project posture", h.Server.Admin.Settings.Service.Approvals.PutProject(dir, settings.ApprovalConfig{Posture: posture}))
		for _, r := range recipients {
			resolved := h.Sessions.Manager.Profiles.ProjectApprovalPosture(ctx, r.sess)
			if resolved != posture {
				t.Fatalf("%s resolved posture %q with project posture %q", r.agent, resolved, posture)
			}
			outcomes[resolved.ReviewsConfinedWrites()] = true
			host, err := h.Sessions.Manager.ReaderFacts.HostFacts(ctx, r.sess)
			testutil.FailErr(t, "host facts", err)
			if host.ReviewsConfinedWrites != resolved.ReviewsConfinedWrites() {
				t.Errorf("%s under %q: host facts review confined writes = %v", r.agent, posture, host.ReviewsConfinedWrites)
			}
			if got := requestReader(t, h, r.sess).Facts()["reviews_confined_writes"]; got != resolved.ReviewsConfinedWrites() {
				t.Errorf("%s under %q: request Reader reviews_confined_writes = %v", r.agent, posture, got)
			}
		}
	}
	if len(outcomes) != 2 {
		t.Fatalf("approval postures produced review outcomes %v; the sweep needs both", outcomes)
	}
}

// The primary and any-root Git facts follow the attached roots in every
// feasible two-root membership.
func TestMultiRootGitFactsReachTheRequestReader(t *testing.T) {
	h := BuildForTest(t)
	for _, tc := range []struct {
		name, id              string
		primaryGit, secondGit bool
	}{
		{name: "neither", id: "neither"},
		{name: "secondary only", id: "secondary", secondGit: true},
		{name: "both", id: "both", primaryGit: true, secondGit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projectID := uuid.NewString()
			primary, secondary := t.TempDir(), t.TempDir()
			if tc.primaryGit {
				gittest.Init(t, primary)
			}
			if tc.secondGit {
				gittest.Init(t, secondary)
			}
			testdbseed.InsertProjectRoot(t, h.DB, projectID, primary)
			testdbseed.InsertProjectRoot(t, h.DB, projectID, secondary)
			sess, err := h.Store.Sessions.Create(t.Context(), api.CreateSessionRequest{ProjectID: projectID}, projectID)
			testutil.FailErr(t, "create session", err)
			host, err := h.Sessions.Manager.ReaderFacts.HostFacts(t.Context(), sess)
			testutil.FailErr(t, "host facts", err)
			if host.RootCount != 2 || host.HasGit != tc.primaryGit || host.HasAnyGitRoot != (tc.primaryGit || tc.secondGit) {
				t.Fatalf("host facts roots=%d git=%v any=%v", host.RootCount, host.HasGit, host.HasAnyGitRoot)
			}
			facts := requestReader(t, h, sess).Facts()
			if facts["has_git"] != tc.primaryGit || facts["has_any_git_root"] != (tc.primaryGit || tc.secondGit) || facts["multi_root"] != true {
				t.Fatalf("request Reader has_git=%v has_any_git_root=%v multi_root=%v", facts["has_git"], facts["has_any_git_root"], facts["multi_root"])
			}
		})
	}
}

// requestRecorder plays a worker script and keeps every worker request.
type requestRecorder struct {
	mu       sync.Mutex
	script   []api.ToolCall
	requests []modelcall.CompletionRequest
}

func (m *requestRecorder) Complete(_ context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if !SchemaHasTool(req.Tools, "complete_leg") {
		return &modelcall.Completion{Content: "Done."}, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, req)
	if len(m.script) == 0 {
		return &modelcall.Completion{Content: "Done."}, nil
	}
	call := m.script[0]
	m.script = m.script[1:]
	return &modelcall.Completion{ToolCalls: []api.ToolCall{call}}, nil
}

func (m *requestRecorder) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	completion, err := m.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan modelcall.StreamChunk, 1)
	ch <- modelcall.StreamChunk{Content: completion.Content, ToolCalls: completion.ToolCalls, Done: true}
	close(ch)
	return ch, nil
}

// A worker that loads one deferred tool is offered that tool on its next
// request while its other deferred tools stay withheld.
func TestWorkerPartialLoadingOffersOnlyTheLoadedTool(t *testing.T) {
	const loadedTool = "capture_page"
	stillDeferred := []string{"code_rewrite", "fetch_url", "source_history"}
	mock := &requestRecorder{script: []api.ToolCall{{ID: "load", Name: "request_tools", Args: map[string]any{"need": loadedTool}}}}
	h := BuildForTest(t, WithLLMClient(mock))
	ctx := context.Background()
	dir := t.TempDir()
	parent, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create parent", err)
	task := api.WorkerTask{
		ParentSessionID: parent.ID, Prompt: "Capture the page.", Brief: "fixture", AgentType: orchestration.ProfileImplementer,
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
	if len(mock.requests) < 2 {
		t.Fatalf("worker sent %d requests, want the load request and the one after it", len(mock.requests))
	}
	offered := func(req modelcall.CompletionRequest) []string {
		names := make([]string, 0, len(req.Tools))
		for _, meta := range req.Tools {
			names = append(names, meta.Name)
		}
		return names
	}
	first, next := offered(mock.requests[0]), offered(mock.requests[1])
	for _, name := range append([]string{loadedTool}, stillDeferred...) {
		if slices.Contains(first, name) {
			t.Fatalf("first worker request already offers deferred %q: %v", name, first)
		}
	}
	if !slices.Contains(next, loadedTool) {
		t.Fatalf("request after loading lacks %q: %v", loadedTool, next)
	}
	for _, name := range stillDeferred {
		if slices.Contains(next, name) {
			t.Fatalf("request after loading %q also offers %q: %v", loadedTool, name, next)
		}
	}
	if !slices.Contains(next, "request_tools") {
		t.Fatalf("request after a partial load drops request_tools: %v", next)
	}
}
