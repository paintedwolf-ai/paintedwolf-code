package wiring

import (
	"context"
	"github.com/lycaon/lycaon/internal/decide"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestInvestigateCoordinatorWriteLandsOnProjectTree(t *testing.T) {
	var stage atomic.Int32
	mock := llm.NewKickDrivenMock(llm.KickDrivenConfig{
		Fallback: func(_ context.Context, snap llm.Snapshot) modelcall.Completion {
			if !strings.Contains(snap.LastUserMessage, "fix auth") {
				return modelcall.Completion{Content: "No action."}
			}
			switch stage.Add(1) {
			case 1:
				return modelcall.Completion{ToolCalls: []api.ToolCall{{
					ID: "r1", Name: "request_tools", Args: map[string]any{"need": "write and verify"},
				}}}
			case 2:
				return modelcall.Completion{ToolCalls: []api.ToolCall{{
					ID:   "w1",
					Name: "write",
					Args: map[string]any{
						"path":    "src/foo.go",
						"content": "package foo\n// investigate project write\n",
					},
				}}}
			case 3:
				return modelcall.Completion{ToolCalls: []api.ToolCall{{
					ID: "v1", Name: "verify", Args: map[string]any{"command": "true"},
				}}}
			case 4:
				return modelcall.Completion{ToolCalls: []api.ToolCall{{
					ID: "p1", Name: "update_progress",
					Args: map[string]any{"content": "## Progress\n- [x] wiring test plan\n"},
				}}}
			default:
				return modelcall.Completion{Content: MockCoordinatorCloseoutJSON(
					"Updated src/foo.go in the project tree.", "src/foo.go",
				)}
			}
		},
	})
	h := BuildForTest(t, WithLLMClient(mock), WithDecider(decide.Absent{}))
	testutil.FailErr(t, "register verify fixture", h.ToolRegistry.Register(
		"verify",
		func(_ context.Context, _ map[string]any, tctx tools.ToolContext) (string, error) {
			if tctx.Out != nil {
				tctx.Out.SourceRun = &tools.SourceRunCapture{
					Command: "true", ExitCode: 0, Verdict: api.SourceVerdictPassed,
				}
			}
			return `{"stages":[{"command":"true","exit_code":0}],"exit_code":0,"outcome":"passed"}`, nil
		},
	))
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)

	ctx := h.OwnerCtx(t, context.Background())
	dir := t.TempDir()
	testutil.FailErr(t, "create project overlay", os.MkdirAll(
		filepath.Join(dir, settingsoverlay.DirName()), 0o700,
	))
	testutil.FailErr(t, "write project approvals", os.WriteFile(
		filepath.Join(dir, settingsoverlay.DirName(), "approvals.yaml"),
		[]byte("rules:\n  - category: tool\n    pattern: write\n    effect: allow\n  - category: tool\n    pattern: verify\n    effect: allow\n"),
		0o600,
	))
	testutil.FailErr(t, "write project verify command", os.WriteFile(
		filepath.Join(dir, settingsoverlay.DirName(), "verify.yaml"), []byte("test: \"true\"\n"), 0o600,
	))
	testutil.FailErr(t, "create src", os.MkdirAll(filepath.Join(dir, "src"), 0o755))
	projectDir := dir
	targetPath := filepath.Join(dir, "src", "foo.go")

	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	h.SessionMgr.SetVerifyConfig(fixedVerifyConfig("true"))
	if sess.WorkspacePath != projectDir {
		t.Fatalf("ProjectDir = %q want %q", sess.WorkspacePath, projectDir)
	}
	AttachDefaultAmbient(t, h, ctx, sess.ID)
	// Coordinator writes require a progress checklist.
	h.SeedProgress(t, ctx, sess.ID)

	done := make(chan error, 1)
	promptCtx, cancelPrompt := context.WithCancel(ctx)
	promptExited := make(chan struct{})
	// The turn must end before harness teardown closes the app and restores
	// the staged config, or it keeps running into the next test.
	t.Cleanup(func() {
		cancelPrompt()
		h.SessionMgr.CancelInFlightPrompt(sess.ID)
		<-promptExited
	})
	go func() {
		defer close(promptExited)
		_, promptErr := h.SessionMgr.Prompt(promptCtx, sess.ID, "fix auth in src/foo.go")
		done <- promptErr
	}()
	hitlMgr, ok := h.CheckpointMgr.(*hitl.Manager)
	if !ok {
		t.Fatalf("checkpoint manager = %T, want *hitl.Manager", h.CheckpointMgr)
	}
	var checkpointID string
	var promptErr error
	promptFinished := false
	testutil.WaitFor(t, 10*time.Second, func() bool {
		select {
		case promptErr = <-done:
			promptFinished = true
			return true
		default:
		}
		pending, listErr := hitlMgr.ListPending(ctx, sess.ID, ptrKind(api.CheckpointKindToolApproval))
		if listErr != nil || len(pending) != 1 {
			return false
		}
		checkpointID = pending[0].ID
		return true
	})
	if checkpointID != "" {
		_, err = hitlMgr.ResolveApprovalOption(ctx, sess.ID, checkpointID, "approve_current_action")
		testutil.FailErr(t, "approve verify fixture", err)
	}
	if !promptFinished {
		select {
		case promptErr = <-done:
		case <-time.After(testutil.Timeout(10 * time.Second)):
			pending, _ := hitlMgr.ListPending(ctx, sess.ID, ptrKind(api.CheckpointKindToolApproval))
			msgs, _ := h.Store.GetMessages(ctx, sess.ID)
			t.Fatalf("prompt did not finish: stage=%d pending=%+v messages=%+v", stage.Load(), pending, msgs)
		}
	}
	testutil.FailErr(t, "Prompt", promptErr)
	var data []byte
	written := testutil.WaitForNoFatal(5*time.Second, func() bool {
		var readErr error
		data, readErr = os.ReadFile(targetPath)
		return readErr == nil && strings.Contains(string(data), "investigate project write")
	})
	if !written {
		msgs, _ := h.Store.GetMessages(ctx, sess.ID)
		t.Fatalf("src/foo.go = %q want investigate edit in project tree ProjectDir; stage=%d messages=%+v", string(data), stage.Load(), msgs)
	}

	state := h.SessionMgr.BuildImplementSessionState(ctx, sess)
	if len(state.PendingOverlayIDs) != 0 {
		t.Fatalf("PendingOverlayIDs = %v want empty", state.PendingOverlayIDs)
	}
	if h.SessionMgr.Allowed(sess.ID, "src/foo.go") {
		t.Fatal("merge reconcile must not open investigate product path without promote conflict")
	}

	jobs, err := h.WorkerQueue.List(ctx, testdbseed.DefaultProjectID, api.WorkerStatusPending, api.WorkerStatusRunning)
	testutil.FailErr(t, "WorkerQueue.List", err)
	if len(jobs) != 0 {
		t.Fatalf("investigate coordinator write must not enqueue worker jobs: %+v", jobs)
	}
}

type fixedVerifyConfig string

func (config fixedVerifyConfig) VerifyTestCommand(string) string {
	return string(config)
}
