package session

import (
	"github.com/lycaon/lycaon/internal/toolreach"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/indexwatch"
	"github.com/lycaon/lycaon/internal/isolation"
	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	"github.com/lycaon/lycaon/internal/session/checkpointcontrol"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// A write the kernel refused gets the grant to declare when the invocation
// failed or is still running; a refusal a successful command absorbed raises
// no card. The retry is what asks.
func TestRefusedWriteGuidanceNamesTheGrant(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(t.TempDir(), "config"))
	project := t.TempDir()
	boundary := confine.BoundaryOf(&confine.Confinement{Roots: []string{project}})
	if !boundary.Applied {
		t.Fatal("fixture boundary did not apply")
	}
	agents := filepath.Join(project, "AGENTS.md")
	refusals := confine.SandboxRefusals{Witness: confine.WitnessKernel, Refusals: []confine.SandboxRefusal{{
		Operation: "file-write-data", Target: agents, Process: "touch", Count: 1,
		Recovery: confine.RecoverWriteRoot, Grant: agents, Layer: confine.FloorAgentPolicy,
	}}}
	for _, tc := range []struct {
		name    string
		failed  []string
		running bool
		fires   bool
	}{
		{"swallowed failure", []string{"touch -f AGENTS.md"}, false, true},
		{"still running", nil, true, true},
		{"success", nil, false, false},
	} {
		mgr := newPostToolGuidanceManager(t)
		obs := confine.StampRefusal("command", "sess-route", boundary, confine.RefusalContext{
			FailedStages: tc.failed, Running: tc.running, Refusals: refusals,
		}).Observation
		out, facts := mgr.ToolPolicy.AfterTool(t.Context(), &api.Session{ID: "sess-route"}, "command",
			map[string]any{"command": "touch -f AGENTS.md || true"}, `{"exit_code":0}`, 1, guidance.ToolResultFacts{Confine: obs})
		fired := strings.Contains(out, "Code: "+isolation.CodeTryWriteRoot)
		if fired != tc.fires {
			t.Fatalf("%s: guidance fired=%v, want %v:\n%s", tc.name, fired, tc.fires, out)
		}
		if !tc.fires {
			continue
		}
		if !strings.Contains(out, agents) || !facts.HasCode(isolation.CodeTryWriteRoot) {
			t.Fatalf("%s: guidance did not name the grant, or state its code:\n%s\n%#v", tc.name, out, facts)
		}
	}
}

// An operation no capability admits routes to host execution, and a running
// job is told to stop before the retry.
func TestUnsandboxedRefusalRoutesToHostExecution(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(t.TempDir(), "config"))
	boundary := confine.BoundaryOf(&confine.Confinement{Roots: []string{t.TempDir()}})
	socket := "/Users/person/.colima/_lima/_networks/user-v2/user-v2_fd.sock"
	obs := confine.StampRefusal("command", "sess-host", boundary, confine.RefusalContext{
		Running: true,
		Refusals: confine.SandboxRefusals{Witness: confine.WitnessKernel, Refusals: []confine.SandboxRefusal{{
			Operation: "network-bind", Target: socket, Process: "limactl", Count: 3,
			Recovery: confine.RecoverHostExecution,
		}}},
	}).Observation
	mgr := newPostToolGuidanceManager(t)
	out, facts := mgr.ToolPolicy.AfterTool(tools.WithCallReach(t.Context(), toolreach.Offered([]string{"command", "command_stop"})), &api.Session{ID: "sess-host"}, "command",
		map[string]any{"command": "colima start"}, `{"running":true}`, 1, guidance.ToolResultFacts{Confine: obs})
	for _, want := range []string{"Code: " + isolation.CodeTryHostExecution, socket, "command_stop", "capability_request.host_execution"} {
		if !strings.Contains(out, want) {
			t.Fatalf("guidance lacks %q:\n%s", want, out)
		}
	}
	if !facts.HasCode(isolation.CodeTryHostExecution) {
		t.Fatalf("OAR must state the code on facts: %#v", facts)
	}
}

// A branch switch the sandbox kept out of an instruction file routes to the
// native tools that finish it under review.
func TestWorktreeBehindIndexRoutesToNativeTools(t *testing.T) {
	dir := t.TempDir()
	gittest.Init(t, dir)
	docs := filepath.Join(dir, "docs")
	testutil.FailErr(t, "create docs", os.MkdirAll(docs, 0o755))
	testutil.FailErr(t, "write main", os.WriteFile(filepath.Join(docs, "AGENTS.md"), []byte("main\n"), 0o644))
	gittest.CommitAll(t, dir, "Initial")
	gittest.Run(t, dir, "branch", "-M", "main")
	gittest.Run(t, dir, "checkout", "-q", "-b", "other")
	testutil.FailErr(t, "write other", os.WriteFile(filepath.Join(docs, "AGENTS.md"), []byte("other\n"), 0o644))
	gittest.CommitAll(t, dir, "Other")
	gittest.Run(t, dir, "checkout", "-q", "main")

	snapshot := indexwatch.Take(dir)
	testutil.FailErr(t, "lock docs", os.Chmod(docs, 0o555))
	t.Cleanup(func() { _ = os.Chmod(docs, 0o755) })
	gittest.Run(t, dir, "checkout", "-q", "other")

	mgr := newPostToolGuidanceManager(t)
	out, facts := mgr.ToolPolicy.AfterTool(t.Context(), &api.Session{ID: "sess-worktree"}, "command",
		map[string]any{"command": "git checkout other"}, `{"exit_code":0}`, 1, guidance.ToolResultFacts{
			Confine:    confine.Observation{Applied: true},
			IndexWatch: snapshot,
		})
	if !strings.Contains(out, "Code: "+isolation.CodeWorktreeBehindIndex) || !strings.Contains(out, "docs/AGENTS.md") {
		t.Fatalf("worktree divergence carried no route:\n%s", out)
	}
	if !facts.HasCode(isolation.CodeWorktreeBehindIndex) {
		t.Fatalf("OAR must state the code on facts: %#v", facts)
	}
	if snapshot.Active() {
		t.Fatal("post-tool guidance did not release the index capture")
	}
}

func TestUnsuccessfulCommandWithoutRefusalAddsNoSandboxGuidance(t *testing.T) {
	mgr := newPostToolGuidanceManager(t)
	for _, tool := range []string{"command", "verify", "command_output"} {
		for _, reason := range []string{"timed_out", "stopped", "stopped_by_user", "exited", "", "future_reason"} {
			t.Run(tool+"/"+reason, func(t *testing.T) {
				for _, running := range []bool{false, true} {
					obs := confine.StampRefusal(tool, "session", confine.Boundary{Applied: true}, confine.RefusalContext{TerminationReason: reason, Running: running, Refusals: confine.SandboxRefusals{Witness: confine.WitnessKernel}}).Observation
					obs.FailedStages = []string{"fixture"}
					const content = `{"exit_code":1,"stderr":"permission denied; timed out; stopped"}`
					out, _ := mgr.ToolPolicy.AfterTool(t.Context(), &api.Session{ID: t.Name()}, tool, nil, content, 1, guidance.ToolResultFacts{Confine: obs})
					if out != content {
						t.Fatalf("ordinary failure acquired sandbox guidance: %s", out)
					}
				}
			})
		}
	}
}

// Rewind requires the checkpoint state root.
func TestRewindRefusesWhenNoCheckpointRootIsConfigured(t *testing.T) {
	mgr, sessionID, _ := newCheckpointTestSession(t)
	mgr.SetDataDir("")
	ctx := context.Background()

	anchor := api.Message{
		ID: "u-unsealed", Role: api.MessageRoleUser, Content: "change something",
		Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser,
		TrustTier: api.ContentTrustTierTrusted,
	}
	testutil.FailErr(t, "append", mgr.Coordinator.Context.Sessions.(Store).AppendMessages(ctx, sessionID, anchor))

	if _, err := rewindTest(t, mgr, ctx, uuid.NewString(), sessionID, anchor.ID); !errors.Is(err, checkpointcontrol.ErrCheckpointRootUnset) {
		t.Fatalf("RewindToPrompt err = %v, want checkpointcontrol.ErrCheckpointRootUnset", err)
	}
	msgs, err := mgr.Runner.Transcript.GetMessages(ctx, sessionID)
	testutil.FailErr(t, "get messages", err)
	if _, ok := rewindFixtureHasMessage(msgs, anchor.ID); !ok {
		t.Fatal("a refused rewind must leave the transcript intact")
	}
}

func TestRewindOfTranscriptOnlyTurnNeedsNoFileCheckpoint(t *testing.T) {
	mgr, id, _ := newCheckpointTestSession(t)
	anchor := appendRewindAsk(t, mgr, id)
	result, err := rewindTest(t, mgr, t.Context(), uuid.NewString(), id, anchor)
	testutil.FailErr(t, "rewind transcript only turn", err)
	if result.TruncatedMessageCount != 1 || len(result.RestoredPaths) != 0 {
		t.Fatalf("result=%+v", result)
	}
}

func TestRewindRestoredPromptIsUserInstructionOnly(t *testing.T) {
	mgr, sessionID, dir := newCheckpointTestSession(t)
	ctx := context.Background()

	anchor := api.Message{
		ID:        "u-attach",
		Role:      api.MessageRoleUser,
		Content:   "review this\n\n```attachment filename=\"main.go\" mime=\"text/plain\" truncated=\"false\"\n[User attached file: src/main.go]\n```",
		Origin:    api.MessageOriginUser,
		Authority: api.ContentAuthorityUser,
		TrustTier: api.ContentTrustTierTrusted,
		ContentParts: []api.MessageContentPart{
			{
				Content: "review this", Origin: api.MessageOriginUser,
				Authority: api.ContentAuthorityUser, TrustTier: api.ContentTrustTierTrusted,
			},
			{
				Content: "```attachment filename=\"main.go\" mime=\"text/plain\" truncated=\"false\"\n[User attached file: src/main.go]\n```",
				Origin:  api.MessageOriginRetrieval, Authority: api.ContentAuthorityNone,
				TrustTier: api.ContentTrustTierUntrusted,
				Source:    "src/main.go", MediaType: "text/plain",
			},
		},
		ArtifactIDs: []string{"art-1"},
	}
	testutil.FailErr(t, "append", mgr.Coordinator.Context.Sessions.(Store).AppendMessages(ctx, sessionID, anchor))
	_, err := sessioncheckpoint.New(mgr.Workspace.DataDir, dir, mgr.Coordinator.Context.Sessions.(Store)).Open(t.Context(), sessionID, anchor.ID)
	testutil.FailErr(t, "open checkpoint", err)

	result, err := rewindTest(t, mgr, ctx, uuid.NewString(), sessionID, anchor.ID)
	testutil.FailErr(t, "rewind", err)
	if result.RestoredPrompt != "review this" {
		t.Fatalf("RestoredPrompt = %q, want prose only", result.RestoredPrompt)
	}
	if len(result.RestoredContentParts) != 2 {
		t.Fatalf("RestoredContentParts = %d, want 2", len(result.RestoredContentParts))
	}
	if len(result.RestoredArtifactIDs) != 1 || result.RestoredArtifactIDs[0] != "art-1" {
		t.Fatalf("RestoredArtifactIDs = %#v", result.RestoredArtifactIDs)
	}
}

type decisionWorkerJobs struct {
	jobLister
	job *api.WorkerTask
}

func (q decisionWorkerJobs) Get(id string) (*api.WorkerTask, bool) {
	return q.job, q.job != nil && q.job.ID == id
}

func TestWorkerDecisionUsesBoundJobInsteadOfRenderedPreamble(t *testing.T) {
	worker := &api.Session{ID: "child", ParentSessionID: "parent", AgentType: "builder"}
	job := &api.WorkerTask{ID: "job", ChildSessionID: worker.ID, ParentSessionID: worker.ParentSessionID, AgentType: worker.AgentType, Brief: "Add Docker support", Prompt: strings.Repeat("host instructions ", 200)}
	mgr := NewHost(sessionstore.NewMemory(), Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	mgr.SetWorkerQueue(decisionWorkerJobs{job: job})
	history := []api.Message{{ID: "opening", WorkerID: job.ID, Content: strings.Repeat("host instructions ", 200)}}
	for _, jobID := range []string{job.ID, ""} {
		text, ok := mgr.Coordinator.Loading.WorkerRequest(worker, jobID, "opening", history)
		if !ok || text != job.Brief {
			t.Fatalf("worker request = %q, %v", text, ok)
		}
	}
	job.LegID = "leg"
	job.Prompt = "Add a Dockerfile and run the container"
	if text, ok := mgr.Coordinator.Loading.WorkerRequest(worker, job.ID, "opening", history); !ok || text != job.Prompt {
		t.Fatalf("delegation request = %q, %v", text, ok)
	}
	job.ChildSessionID = "another child"
	if text, ok := mgr.Coordinator.Loading.WorkerRequest(worker, job.ID, "opening", history); ok || text != "" {
		t.Fatalf("mismatched job supplied request = %q, %v", text, ok)
	}
	mgr.Coordinator.Loading.SetWorkers(nil)
	if _, ok := mgr.Coordinator.Loading.WorkerRequest(worker, job.ID, "opening", history); ok {
		t.Fatal("missing job ledger supplied a request")
	}
}
