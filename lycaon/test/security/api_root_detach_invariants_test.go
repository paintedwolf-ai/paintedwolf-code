package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

// I13: after forced detach, no in-flight sandbox-bound artifact references the removed root.
func TestI13ForcedDetachLeavesNoRootDependents(t *testing.T) {
	h := wiring.BuildForTest(t)
	httpSrv := httptest.NewServer(h.Server)
	t.Cleanup(httpSrv.Close)
	base := httpSrv.URL
	ctx := t.Context()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("x"), 0o600); err != nil {
		testutil.FailErr(t, "write fixture", err)
	}

	project := createAPIProjectAtPath(t, base, dir)
	rootID := project.Roots[0].ID

	sess := openAPIPostJSON[wire.Session](t, base, "/v1/sessions", nil,
		`{"project_id":"`+project.ID+`","posture":"build"}`, http.StatusAccepted)

	_, err := h.WorkerQueue.Enqueue(ctx, wire.WorkerTask{
		Prompt:          "fixture",
		Brief:           "fixture",
		ID:              "job-detach-i13",
		ParentSessionID: sess.ID,
		ProjectID:       project.ID,
		WorkspaceRootID: rootID,
		WorkspacePath:   dir,
		AgentType:       "implementer",
		Status:          wire.WorkerStatusRunning,
		Scope:           &wire.TaskScope{Mode: wire.TaskScopeModeRead},
	})
	testutil.FailErr(t, "enqueue worker", err)

	req := authedRequest(t, http.MethodDelete, "/v1/projects/"+project.ID+"/roots/"+rootID+"?force=true", nil)
	w := httptest.NewRecorder()
	h.Server.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("force detach status = %d body = %s", w.Code, w.Body.String())
	}

	got, ok := h.WorkerQueue.Get("job-detach-i13")
	if !ok {
		t.Fatal("worker job missing")
	}
	if got.Status != wire.WorkerStatusCanceled {
		t.Fatalf("worker status = %q want canceled", got.Status)
	}
	inFlight, err := h.WorkerQueue.List(ctx, project.ID, wire.WorkerStatusPending, wire.WorkerStatusRunning, wire.WorkerStatusHeld)
	testutil.FailErr(t, "list in-flight workers", err)
	if len(inFlight) != 0 {
		t.Fatalf("in-flight workers after force detach = %+v want none", inFlight)
	}
	updated := openAPIGetJSON[wire.Project](t, base, "/v1/projects/{id}",
		map[string]string{"id": project.ID}, http.StatusOK)
	if len(updated.Roots) != 0 {
		t.Fatalf("roots = %d want 0 after detach", len(updated.Roots))
	}
}

func TestGuardedDetachReturnsRootBusy(t *testing.T) {
	h := wiring.BuildForTest(t)
	httpSrv := httptest.NewServer(h.Server)
	t.Cleanup(httpSrv.Close)
	base := httpSrv.URL
	ctx := t.Context()

	dir := t.TempDir()
	project := createAPIProjectAtPath(t, base, dir)
	rootID := project.Roots[0].ID

	sess := openAPIPostJSON[wire.Session](t, base, "/v1/sessions", nil,
		`{"project_id":"`+project.ID+`","posture":"build"}`, http.StatusAccepted)

	_, err := h.WorkerQueue.Enqueue(ctx, wire.WorkerTask{
		Prompt:          "fixture",
		Brief:           "fixture",
		ID:              "job-busy",
		ParentSessionID: sess.ID,
		ProjectID:       project.ID,
		WorkspaceRootID: rootID,
		WorkspacePath:   dir,
		AgentType:       "implementer",
		Status:          wire.WorkerStatusRunning,
	})
	testutil.FailErr(t, "enqueue", err)

	req := authedRequest(t, http.MethodDelete, "/v1/projects/"+project.ID+"/roots/"+rootID, nil)
	w := httptest.NewRecorder()
	h.Server.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d want 409; body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		testutil.FailErr(t, "decode body", err)
	}
	if body.Code != "root_busy" {
		t.Fatalf("code = %q want root_busy", body.Code)
	}
	got, ok := h.WorkerQueue.Get("job-busy")
	if !ok || got.Status != wire.WorkerStatusRunning {
		t.Fatal("guarded detach must not cancel running worker")
	}
}
