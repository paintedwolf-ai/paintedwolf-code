package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
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

	_, err := h.Delegations.Queue.Enqueue(ctx, wire.WorkerTask{
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

	got, ok := h.Delegations.Queue.Get("job-detach-i13")
	if !ok {
		t.Fatal("worker job missing")
	}
	if got.Status != wire.WorkerStatusCanceled {
		t.Fatalf("worker status = %q want canceled", got.Status)
	}
	inFlight, err := h.Delegations.Queue.List(ctx, project.ID, wire.WorkerStatusPending, wire.WorkerStatusRunning, wire.WorkerStatusHeld)
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

	_, err := h.Delegations.Queue.Enqueue(ctx, wire.WorkerTask{
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
	got, ok := h.Delegations.Queue.Get("job-busy")
	if !ok || got.Status != wire.WorkerStatusRunning {
		t.Fatal("guarded detach must not cancel running worker")
	}
}

func TestRootDetachDirectAPIStopsIdleAndArchivedBackgroundCommandsUnderDetachedRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep fixture is unix-oriented")
	}
	h := wiring.BuildForTest(t)
	httpSrv := httptest.NewServer(h.Server)
	t.Cleanup(httpSrv.Close)
	base := httpSrv.URL
	ctx := t.Context()

	rootADir := t.TempDir()
	nestedADir := filepath.Join(rootADir, "nested")
	testutil.FailErr(t, "mkdir nestedA", os.MkdirAll(nestedADir, 0o755))
	rootBDir := t.TempDir()

	project := createAPIProjectAtPath(t, base, rootADir)
	rootAID := project.Roots[0].ID

	attachReq := authedRequest(t, http.MethodPost, "/v1/projects/"+project.ID+"/roots",
		strings.NewReader(`{"path":`+ strconvQuote(rootBDir) +`,"label":"secondary"}`))
	attachReq.Header.Set("Content-Type", "application/json")
	attachW := httptest.NewRecorder()
	h.Server.ServeHTTP(attachW, attachReq)
	if attachW.Code != http.StatusCreated {
		t.Fatalf("attach second root status = %d body = %s", attachW.Code, attachW.Body.String())
	}

	idleSess := openAPIPostJSON[wire.Session](t, base, "/v1/sessions", nil,
		`{"project_id":"`+project.ID+`","posture":"build"}`, http.StatusAccepted)
	archivedSess := openAPIPostJSON[wire.Session](t, base, "/v1/sessions", nil,
		`{"project_id":"`+project.ID+`","posture":"build"}`, http.StatusAccepted)
	now := time.Now().UTC()
	testutil.FailErr(t, "archive session", h.Store.UpdateSession(ctx, archivedSess.ID, func(s *wire.Session) {
		s.ArchivedAt = &now
	}))

	runner := hostcmd.NewRunner()
	bg := h.Sessions.Manager.Processes.Background
	idleHandleA, err := bg.StartPipeline(ctx, bgprocess.PipelineSpec{
		SessionID: idleSess.ID, ProjectID: project.ID, Runner: runner, Mode: bgprocess.JobModeBackground,
		Request: hostcmd.Request{
			Launch:     exec.HostLaunch("api idle rootA server"),
			ProjectDir: nestedADir,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
		},
	})
	testutil.FailErr(t, "start idleHandleA", err)

	archivedHandleA, err := bg.StartPipeline(ctx, bgprocess.PipelineSpec{
		SessionID: archivedSess.ID, ProjectID: project.ID, Runner: runner, Mode: bgprocess.JobModeBackground,
		Request: hostcmd.Request{
			Launch:     exec.HostLaunch("api archived rootA server"),
			ProjectDir: rootADir,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"29"}}},
		},
	})
	testutil.FailErr(t, "start archivedHandleA", err)

	idleHandleB, err := bg.StartPipeline(ctx, bgprocess.PipelineSpec{
		SessionID: idleSess.ID, ProjectID: project.ID, Runner: runner, Mode: bgprocess.JobModeBackground,
		Request: hostcmd.Request{
			Launch:     exec.HostLaunch("api idle rootB server"),
			ProjectDir: rootBDir,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"28"}}},
		},
	})
	testutil.FailErr(t, "start idleHandleB", err)

	// Guarded detach without ?force=true returns 409 root_busy even with no busy turn, worker, or overlay.
	guardedReq := authedRequest(t, http.MethodDelete, "/v1/projects/"+project.ID+"/roots/"+rootAID, nil)
	guardedW := httptest.NewRecorder()
	h.Server.ServeHTTP(guardedW, guardedReq)
	if guardedW.Code != http.StatusConflict {
		t.Fatalf("guarded detach status = %d want 409; body=%s", guardedW.Code, guardedW.Body.String())
	}

	// Forced detach stops background commands under rootA in both idle and archived chats while preserving rootB.
	forceReq := authedRequest(t, http.MethodDelete, "/v1/projects/"+project.ID+"/roots/"+rootAID+"?force=true", nil)
	forceW := httptest.NewRecorder()
	h.Server.ServeHTTP(forceW, forceReq)
	if forceW.Code != http.StatusNoContent {
		t.Fatalf("forced detach status = %d want 204; body=%s", forceW.Code, forceW.Body.String())
	}

	for _, item := range []struct {
		sess   string
		handle string
	}{
		{idleSess.ID, idleHandleA},
		{archivedSess.ID, archivedHandleA},
	} {
		snap, err := bg.Snapshot(ctx, item.sess, item.handle, 0)
		testutil.FailErr(t, "snapshot "+item.handle, err)
		if !snap.HasExit || snap.TerminationReason != bgprocess.TerminationRootDetached {
			t.Fatalf("handle %s after forced detach = (hasExit=%v, reason=%q), want (true, root_detached)", item.handle, snap.HasExit, snap.TerminationReason)
		}
	}

	snapB, err := bg.Snapshot(ctx, idleSess.ID, idleHandleB, 0)
	testutil.FailErr(t, "snapshot idleHandleB", err)
	if snapB.HasExit {
		t.Fatal("expected background server under rootB to keep running after detaching rootA")
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
