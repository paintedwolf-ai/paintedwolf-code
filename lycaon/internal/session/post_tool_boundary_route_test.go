package session

import (
	"context"
	"errors"
	"fmt"
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
	out, facts := mgr.ToolPolicy.AfterTool(tools.WithRecoveryTools(t.Context(), []string{"command", "command_stop"}), &api.Session{ID: "sess-host"}, "command",
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

// A capability retry starts a new operation. A stop acknowledgement must not
// teach the agent that the previous operation has already settled.
func TestSandboxRecoveryPreservesRunningJobDependency(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(t.TempDir(), "config"))
	mgr := newPostToolGuidanceManager(t)
	cases := []struct {
		code, capability string
		refusal          confine.SandboxRefusal
	}{
		{"SANDBOX_TRY_READ_PATH", "read_path", confine.SandboxRefusal{Operation: "file-read-data", Target: "/protected/input", Grant: "/protected/input", Layer: confine.FloorReadKeyMaterial, Recovery: confine.RecoverReadPath}},
		{"SANDBOX_TRY_WRITE_ROOT", "write_root", confine.SandboxRefusal{Operation: "file-write-data", Target: "/outside/output", Grant: "/outside", Layer: confine.FloorOutsideWriteRoots, Recovery: confine.RecoverWriteRoot}},
		{"SANDBOX_TRY_SOCKET_PATH", "socket_paths", confine.SandboxRefusal{Operation: "network-outbound", Target: "/tmp/service.sock", Grant: "/tmp/service.sock", Recovery: confine.RecoverSocketPath}},
		{"SANDBOX_TRY_LOCAL_NETWORK", "local_listen", confine.SandboxRefusal{Operation: "network-bind", Target: "local:*:8080", Recovery: confine.RecoverLocalListen}},
		{"SANDBOX_TRY_LOCAL_NETWORK", "loopback_connect", confine.SandboxRefusal{Operation: "network-outbound", Target: "remote:*:5432", Recovery: confine.RecoverOutboundPort}},
		{"SANDBOX_TRY_PROCESS_CONTROL", "process_control", confine.SandboxRefusal{Operation: "signal", Target: "4121", Recovery: confine.RecoverProcessControl}},
		{"SANDBOX_TRY_HOST_EXECUTION", "host_execution", confine.SandboxRefusal{Operation: "iokit-open", Target: "AppleUSBHostDevice", Recovery: confine.RecoverHostExecution}},
	}
	for _, tc := range cases {
		for _, tool := range []string{"command", "verify"} {
			for _, state := range []string{"running", "failed", "succeeded"} {
				for offered := 0; offered < 4; offered++ {
					t.Run(fmt.Sprintf("%s/%s/%s/%d", tc.capability, tool, state, offered), func(t *testing.T) {
						names := []string{tool}
						if offered&1 != 0 {
							names = append(names, "command_stop")
						}
						if offered&2 != 0 {
							names = append(names, "command_output")
						}
						obs := confine.Observation{Applied: true, Running: state == "running", Refusals: confine.SandboxRefusals{
							Witness: confine.WitnessKernel, Refusals: []confine.SandboxRefusal{tc.refusal},
						}}
						if state == "failed" {
							obs.FailedStages = []string{"fixture"}
						}
						ctx := tools.WithRecoveryTools(t.Context(), names)
						out, facts := mgr.ToolPolicy.AfterTool(ctx, &api.Session{ID: t.Name()}, tool, nil, "{}", 1, guidance.ToolResultFacts{Confine: obs})
						if state == "succeeded" {
							if facts.HasCode(tc.code) {
								t.Fatalf("absorbed refusal acquired retry guidance: %s", out)
							}
							return
						}
						if !facts.HasCode(tc.code) || !strings.Contains(out, "capability_request."+tc.capability) {
							t.Fatalf("refusal lost its precise capability remedy: %s", out)
						}
						for _, control := range []struct {
							name string
							mask int
						}{{"command_stop", 1}, {"command_output", 2}} {
							want := state == "running" && offered&control.mask != 0
							if strings.Contains(out, "`"+control.name+"`") != want {
								t.Errorf("control %s offered=%v: %s", control.name, want, out)
							}
						}
						if !strings.Contains(out, "Preserve completed effects") || !strings.Contains(out, "If the task still needs the refused operation") {
							t.Errorf("retry lost the remaining-work condition: %s", out)
						}
						if state != "running" {
							if !strings.Contains(out, "only if the operation is still needed") {
								t.Errorf("summary lost remaining-work condition: %s", out)
							}
							return
						}
						for _, phrase := range []string{"Do not reissue while the job is running", "After it exits", "Next: Resolve the running job before retrying", "a stop request alone does not establish exit"} {
							if !strings.Contains(out, phrase) {
								t.Errorf("running-job dependency missing %q: %s", phrase, out)
							}
						}
					})
				}
			}
		}
	}
}

func TestSilentRefusalGuidanceUsesObservedTermination(t *testing.T) {
	mgr := newPostToolGuidanceManager(t)
	statuses := []struct{ reason, want string }{
		{"timed_out", "The command reached its host deadline"}, {"stopped", "The command was stopped"},
		{"exited", "The command exited unsuccessfully"}, {"", "The invocation did not finish successfully"},
		{"future_reason", "The invocation did not finish successfully"},
	}
	for _, tool := range []string{"command", "verify", "command_output"} {
		for _, tc := range statuses {
			t.Run(tool+"/"+tc.reason, func(t *testing.T) {
				for mask := 0; mask < 32; mask++ {
					applied, failed, kernel, reported, running := mask&1 != 0, mask&2 != 0, mask&4 != 0, mask&8 != 0, mask&16 != 0
					refusals := confine.SandboxRefusals{}
					if kernel {
						refusals.Witness = confine.WitnessKernel
					}
					if reported {
						refusals.Refusals = []confine.SandboxRefusal{{Operation: "file-write-data", Target: "/tmp/fixture", Recovery: confine.RecoverNone}}
					}
					obs := confine.StampRefusal(tool, "session", confine.Boundary{Applied: applied}, confine.RefusalContext{TerminationReason: tc.reason, Running: running, Refusals: refusals}).Observation
					if applied && failed {
						obs.FailedStages = []string{"fixture"}
					}
					out, facts := mgr.ToolPolicy.AfterTool(t.Context(), &api.Session{ID: t.Name()}, tool, nil,
						`{"output":"exited successfully; timed out; stopped; permission denied"}`, 1, guidance.ToolResultFacts{Confine: obs})
					wantWarning := applied && failed && kernel && !reported
					if facts.HasCode("SANDBOX_REFUSAL_REPORT_SILENT") != wantWarning {
						t.Fatalf("mask %d: warning selection changed: %s", mask, out)
					}
					if !wantWarning {
						continue
					}
					want := tc.want
					if running {
						want = statuses[3].want
					}
					for _, status := range statuses[:4] {
						if strings.Contains(out, status.want) != (status.want == want) {
							t.Fatalf("mask %d: inconsistent status %q, want %q: %s", mask, status.want, want, out)
						}
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
