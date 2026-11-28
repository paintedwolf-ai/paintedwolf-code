// Package readerseam compares the Reader a live host builds for a model
// request with the hoststate oracle's Reader for the same observed state.
package readerseam

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/readerfacts"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/wiring"
)

// requests answers every call in prose and keeps the coordinator and worker
// requests apart.
type requests struct {
	mu          sync.Mutex
	coordinator []modelcall.CompletionRequest
	worker      []modelcall.CompletionRequest
}

func (r *requests) Complete(_ context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if strings.TrimSpace(req.Debug.ParentSessionID) != "" {
		r.worker = append(r.worker, req)
	} else {
		r.coordinator = append(r.coordinator, req)
	}
	return &modelcall.Completion{Content: "Done."}, nil
}

func (r *requests) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	completion, err := r.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan modelcall.StreamChunk, 1)
	ch <- modelcall.StreamChunk{Content: completion.Content, Done: true}
	close(ch)
	return ch, nil
}

// visionFree drops the model-vision suffix the oracle adds for rendering;
// request Readers carry no model capability.
func visionFree(fingerprint string) string {
	return strings.TrimSuffix(strings.TrimSuffix(fingerprint, ":v0"), ":v1")
}

// The coordinator's first request in a one-root Git project is read by the
// Reader the hoststate oracle builds for that observed state.
func TestCoordinatorRequestReaderMatchesOracle(t *testing.T) {
	t.Cleanup(hoststate.HoldAuthorityMemos())
	mock := &requests{}
	h := wiring.BuildForTest(t, wiring.WithLLMClient(mock))
	h.Start(t)
	ctx := context.Background()
	dir := t.TempDir()
	contractcheck.FailErr(t, "write fixture file", os.WriteFile(filepath.Join(dir, "README.md"), []byte("fixture\n"), 0o600))
	gittest.InitCommit(t, dir, "fixture")
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{ProviderID: "mock", Model: "mock"}, dir)
	contractcheck.FailErr(t, "create session", err)
	_, err = h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "Inspect current state")
	contractcheck.FailErr(t, "prompt", err)
	reloaded, err := h.Store.Sessions.Get(h.OwnerCtx(t, ctx), sess.ID)
	contractcheck.FailErr(t, "reload session", err)

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.coordinator) == 0 {
		t.Fatal("the coordinator sent no request")
	}
	req := mock.coordinator[0]
	if req.Debug.ReaderFingerprint == "" {
		t.Fatal("the coordinator request carries no Reader")
	}
	obs, err := h.Sessions.Manager.ReaderFacts.Observe(ctx, reloaded)
	contractcheck.FailErr(t, "observe host facts", err)
	if len(obs.Roots) != 1 {
		t.Fatalf("observed roots = %+v, want the one project root", obs.Roots)
	}
	host, err := readerfacts.Derive(obs)
	contractcheck.FailErr(t, "derive host facts", err)
	ambient := host.SpawnAllowlist
	roster := inject.ResolveAgentRoster(req.Debug.Surface, ambient, 1, false, obs.WebSearch)

	state := hoststate.CoordinatorState{
		ID:                    "seam/coordinator",
		SessionMode:           reloaded.Mode,
		Posture:               reloaded.Posture,
		RootCount:             1,
		Roots:                 []hoststate.RootSpec{{Ref: obs.Roots[0], HasGit: true}},
		HasGit:                true,
		HasAnyGitRoot:         true,
		WebSearch:             obs.WebSearch,
		CommandExternalHidden: obs.CommandExternalHidden,
		ApprovalPosture:       obs.ApprovalPosture,
		SessionScratchDir:     obs.SessionScratchDir,
		SharedTempLocations:   obs.SharedTempLocations,
		SpawnAllowlist:        roster.Effective,
		MCPState:              hoststate.MCPNone,
		TurnProfile:           surface.TurnProfile{SurfaceID: req.Debug.Surface},
		RunContext:            api.CoordinatorRunContext{AllowedAgents: obs.AllowedAgents},
		Loaded:                h.Sessions.Manager.Coordinator.Loading.LoadedTools(sess.ID),
	}
	auth, err := hoststate.FreezeTurnAuthority(ctx, state)
	contractcheck.FailErr(t, "freeze oracle turn", err)
	if got := visionFree(auth.Reader.Fingerprint()); got != req.Debug.ReaderFingerprint {
		t.Fatalf("oracle Reader %s != request Reader %s\n%s", got, req.Debug.ReaderFingerprint, readerDiff(t, host, auth, req))
	}
}

// readerDiff attributes a mismatch: the oracle projection read with the live
// host facts either matches the request, isolating the host facts, or not.
func readerDiff(t *testing.T, host prompts.HostFacts, auth hoststate.TurnAuthority, req modelcall.CompletionRequest) string {
	t.Helper()
	schemas := map[string]map[string]any{}
	for name, schema := range auth.PostLoadSchemas {
		schemas[name] = schema
	}
	for name, schema := range auth.Schemas {
		schemas[name] = schema
	}
	live, err := prompts.NewReader(host, prompts.TurnProjection{
		ProfileID: auth.Reader.ProfileID(), SurfaceID: auth.Reader.SurfaceID(),
		Plan: auth.Plan, Offered: auth.Offered, ProjectedSchemas: schemas,
	})
	if err != nil {
		return "live host facts on the oracle projection: " + err.Error()
	}
	var diffs []string
	oracleFacts, liveFacts := auth.Reader.Facts(), live.Facts()
	for key, want := range liveFacts {
		if !reflect.DeepEqual(oracleFacts[key], want) {
			diffs = append(diffs, fmt.Sprintf("%s: oracle=%v live=%v", key, oracleFacts[key], want))
		}
	}
	for _, meta := range req.Tools {
		if !reflect.DeepEqual(auth.Schemas[meta.Name], meta.ArgsSchema) {
			diffs = append(diffs, fmt.Sprintf("schema %s: oracle=%v request=%v", meta.Name, auth.Schemas[meta.Name], meta.ArgsSchema))
		}
	}
	diffs = append(diffs, fmt.Sprintf("oracle requestable=%v", auth.Reader.Requestable()))
	sort.Strings(diffs)
	return fmt.Sprintf("oracle projection with live host facts matches request: %t\nfact differences:\n%s\nrequest tools=%v",
		live.Fingerprint() == req.Debug.ReaderFingerprint, strings.Join(diffs, "\n"), toolNames(req.Tools))
}

// A write-scope implementer's first request is read by the Reader the
// hoststate worker oracle builds for that observed state.
func TestWorkerRequestReaderMatchesOracle(t *testing.T) {
	t.Cleanup(hoststate.HoldAuthorityMemos())
	mock := &requests{}
	h := wiring.BuildForTest(t, wiring.WithLLMClient(mock))
	ctx := context.Background()
	dir := t.TempDir()
	contractcheck.FailErr(t, "write fixture file", os.WriteFile(filepath.Join(dir, "README.md"), []byte("fixture\n"), 0o600))
	gittest.InitCommit(t, dir, "fixture")
	parent, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	contractcheck.FailErr(t, "create parent", err)
	task := api.WorkerTask{
		ParentSessionID: parent.ID, Prompt: "Survey the project.", Brief: "fixture", AgentType: orchestration.ProfileImplementer,
		Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"."}},
	}
	contractcheck.FailErr(t, "worker defaults", worker.ApplyEnqueueDefaults(
		&task, project.ProjectScope{ProjectID: parent.ProjectID, WorkspacePath: dir}, worker.DefaultWorkersConfig(),
	))
	_, err = h.Workers.Queue.Enqueue(ctx, task)
	contractcheck.FailErr(t, "enqueue worker", err)
	claimed, err := h.Workers.Queue.ClaimNext(ctx, worker.ClaimRequest{
		ProjectID: parent.ProjectID, ExecutionTarget: api.ExecutionTargetLocal, ClaimedBy: "reader-seam",
	})
	contractcheck.FailErr(t, "claim worker", err)
	_, err = wiring.WiringWorkerExecutor(h).Execute(ctx, *claimed, worker.RunContextForTask(*claimed, dir))
	contractcheck.FailErr(t, "execute worker", err)

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.worker) == 0 {
		t.Fatal("the worker sent no request")
	}
	req := mock.worker[0]
	if req.Debug.ReaderFingerprint == "" {
		t.Fatal("the worker request carries no Reader")
	}
	child, err := h.Store.Sessions.Get(h.OwnerCtx(t, ctx), req.Debug.SessionID)
	contractcheck.FailErr(t, "load worker session", err)
	obs, err := h.Sessions.Manager.ReaderFacts.Observe(ctx, child)
	contractcheck.FailErr(t, "observe worker host facts", err)
	host, err := readerfacts.Derive(obs)
	contractcheck.FailErr(t, "derive worker host facts", err)

	branch := hoststate.BranchMainGit
	if host.OnWorkerBranch {
		branch = hoststate.BranchIsolatedWorker
	}
	state := hoststate.WorkerState{
		ID:                    "seam/worker",
		AgentID:               orchestration.ProfileImplementer,
		ToolProfile:           req.Debug.ProfileID,
		Posture:               child.Posture,
		BranchView:            branch,
		RootCount:             host.RootCount,
		ParentSessionMode:     child.Mode,
		WebSearch:             host.WebSearch,
		CommandExternalHidden: host.CommandExternalHidden,
		ApprovalPosture:       obs.ApprovalPosture,
		HasGit:                host.HasGit,
		HasAnyGitRoot:         host.HasAnyGitRoot,
		OnWorkerBranch:        host.OnWorkerBranch,
		SessionScratchDir:     host.SessionScratchDir,
		SharedTempLocations:   host.SharedTempLocations,
		Loaded:                h.Sessions.Manager.Coordinator.Loading.LoadedTools(child.ID),
	}
	auth, err := hoststate.FreezeWorkerAuthority(ctx, state)
	contractcheck.FailErr(t, "freeze worker oracle", err)
	if got := visionFree(auth.Reader.Fingerprint()); got != req.Debug.ReaderFingerprint {
		t.Fatalf("oracle Reader %s != worker request Reader %s\n%s", got, req.Debug.ReaderFingerprint, readerDiff(t, host, auth, req))
	}
}

func toolNames(metas []tools.ToolMeta) []string {
	out := make([]string, 0, len(metas))
	for _, meta := range metas {
		out = append(out, meta.Name)
	}
	return out
}
