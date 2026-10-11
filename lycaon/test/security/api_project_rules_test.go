package security

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestProjectRuleBlocksToolInProductionWiring(t *testing.T) {
	mock := newScriptedLLM().on("project_rule_write",
		toolStep("", call("write-denied", "write", map[string]any{"path": "out.txt", "content": "x"})),
		textStep("The project rule blocked the write."),
	)
	recording := llm.NewRecordingClient(mock)
	h := wiring.BuildForTest(t, wiring.WithLLMClient(recording))
	srv := h.Server
	projectDir := t.TempDir()
	rulesDir := filepath.Join(projectDir, settingsoverlay.DirName(), "rules")
	testutil.FailErr(t, "create rules directory", os.MkdirAll(rulesDir, 0o755))
	testutil.FailErr(t, "write project rules", os.WriteFile(filepath.Join(rulesDir, "rules.yaml"), []byte(`
rules:
  - when: tool_is_write
    code: PROJECT_DENY_WRITE
    message: blocked
`), 0o644))

	openBody := `{"roots":[{"path":"` + projectDir + `"}]}`
	req := authedRequest(t, http.MethodPost, "/v1/projects", strings.NewReader(openBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d body = %s", w.Code, w.Body.String())
	}
	var p wire.Project
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}

	ctx := context.Background()
	parent := createSessionForProjectHTTP(t, srv, p.ID, wire.SessionPostureBuild)
	task := wire.WorkerTask{
		ParentSessionID: parent.ID,
		AgentType:       "implementer",
		Prompt:          "attempt project write",
		Brief:           "fixture",
		Status:          wire.WorkerStatusPending,
		SpawnReason:     wire.SpawnReasonHumanRequest,
	}
	testutil.FailErr(t, "enqueue defaults", worker.ApplyEnqueueDefaults(&task,
		project.ProjectScope{ProjectID: p.ID, WorkspacePath: projectDir}, worker.DefaultWorkersConfig()))
	jobID, err := h.Delegations.Queue.Enqueue(ctx, task)
	testutil.FailErr(t, "enqueue worker job", err)
	child, err := h.Sessions.Manager.Workers.SpawnChild(ctx, parent.ID, wire.SpawnChildRequest{
		AgentType:   "implementer",
		Prompt:      "attempt project write",
		WorkerJobID: jobID,
	})
	testutil.FailErr(t, "spawn implementer", err)
	testutil.FailErr(t, "link child", h.Delegations.Queue.SetChildSessionID(ctx, jobID, child.ID))
	// A worker child's turn resolves its queued task from the bound job.
	workerCtx := workercontext.WithJob(ctx, jobID)
	if _, err := h.Sessions.Manager.Submissions.Prompt(workerCtx, child.ID, "[[scn:project_rule_write]] attempt project write"); err != nil {
		testutil.FailErr(t, "prompt denied write", err)
	}
	messages, err := h.Sessions.Manager.Runner.Transcript.GetMessages(t.Context(), child.ID)
	testutil.FailErr(t, "read denied tool result", err)
	for _, tool := range recording.LastRequest().Tools {
		if tool.Name == "write" {
			t.Fatal("project-denied write was advertised to the implementer")
		}
	}
	seenRejection := false
	for _, message := range messages {
		if result := message.ToolResult; result != nil && result.ToolCallID == "write-denied" {
			if result.Tool != "write" || result.Outcome != wire.ToolResultOutcomeRejected || !slices.Contains(result.Codes, "PROJECT_DENY_WRITE") {
				t.Fatalf("project-denied write result = %+v", result)
			}
			seenRejection = true
		}
	}
	if !seenRejection {
		t.Fatal("project-denied write has no structured rejection")
	}
	if _, err := os.Stat(filepath.Join(projectDir, "out.txt")); !os.IsNotExist(err) {
		t.Fatalf("denied write reached filesystem: %v", err)
	}
}

func TestSessionPreparationRejectsInvalidProjectRules(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	projectDir := t.TempDir()
	rulesDir := filepath.Join(projectDir, settingsoverlay.DirName(), "rules")
	if err := os.MkdirAll(rulesDir, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(filepath.Join(rulesDir, "bad.yaml"), []byte(`rules: [{`), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}

	openBody := `{"roots":[{"path":"` + projectDir + `"}]}`
	req := authedRequest(t, http.MethodPost, "/v1/projects", strings.NewReader(openBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d body = %s", w.Code, w.Body.String())
	}
	var p wire.Project
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}

	req = authedRequest(t, http.MethodPost, "/v1/sessions", strings.NewReader(`{"project_id":"`+p.ID+`","posture":"build"}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("create session status = %d body = %s", w.Code, w.Body.String())
	}
	var sess wire.Session
	testutil.FailErr(t, "decode preparing session", json.Unmarshal(w.Body.Bytes(), &sess))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req = authedRequest(t, http.MethodGet, "/v1/sessions/"+sess.ID, nil)
		w = httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("get preparing session status = %d body = %s", w.Code, w.Body.String())
		}
		testutil.FailErr(t, "decode prepared session", json.Unmarshal(w.Body.Bytes(), &sess))
		switch sess.Status {
		case wire.SessionStatusError:
			return
		case wire.SessionStatusIdle:
			t.Fatal("invalid project rules were accepted during session preparation")
		case wire.SessionStatusPreparing, wire.SessionStatusBusy:
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("session preparation did not reject invalid project rules")
}
