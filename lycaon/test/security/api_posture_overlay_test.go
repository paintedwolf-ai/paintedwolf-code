package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func createAPISessionForProject(t *testing.T, serve http.Handler, projectDir, posture string) wire.Session {
	t.Helper()
	createBody := `{"roots":[{"path":"` + projectDir + `"}]}`
	createReq := authedRequest(t, http.MethodPost, "/v1/projects", strings.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	serve.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create project status = %d body = %s", createRec.Code, createRec.Body.String())
	}
	var proj wire.Project
	if err := json.Unmarshal(createRec.Body.Bytes(), &proj); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}

	return createSessionForProjectHandlerHTTP(t, serve, proj.ID, wire.SessionPosture(posture))
}

func TestPostureOverlayWarmOnCreateSession(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	mgr := h.SessionMgr

	projectDir := t.TempDir()
	overlayDir := filepath.Join(projectDir, settingsoverlay.DirName())
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(filepath.Join(overlayDir, "postures.yaml"), []byte(`
postures:
  spec:
    label: Audit
`), 0o644); err != nil {
		t.Fatal(err)
	}

	sess := createAPISessionForProject(t, srv, projectDir, "spec")

	if sess.AgentType != "coordinator" {
		t.Fatalf("agent_type = %q want coordinator", sess.AgentType)
	}
	got, err := mgr.Profiles.ResolvePromptToolProfile(t.Context(), sess.ID)
	testutil.FailErr(t, "mgr.Profiles.ResolvePromptToolProfile failed", err)
	if got != "coordinator" {
		t.Fatalf("root coordinator profile = %q want coordinator (a posture overlay selects no profile)", got)
	}
}

func TestPostureOverlayRejectsUnknownPostureOnCreateSession(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server

	projectDir := t.TempDir()
	overlayDir := filepath.Join(projectDir, settingsoverlay.DirName())
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(filepath.Join(overlayDir, "postures.yaml"), []byte(`
postures:
  plan:
    label: Plan
`), 0o644); err != nil {
		t.Fatal(err)
	}

	createBody := `{"roots":[{"path":"` + projectDir + `"}]}`
	createReq := authedRequest(t, http.MethodPost, "/v1/projects", strings.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	srv.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create project status = %d body = %s", createRec.Code, createRec.Body.String())
	}
	var proj wire.Project
	if err := json.Unmarshal(createRec.Body.Bytes(), &proj); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	body := `{"project_id":"` + proj.ID + `","posture":"spec"}`
	req := authedRequest(t, http.MethodPost, "/v1/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("create status = %d body = %s", w.Code, w.Body.String())
	}
	var sess wire.Session
	if err := json.Unmarshal(w.Body.Bytes(), &sess); err != nil {
		testutil.FailErr(t, "decode accepted session", err)
	}
	testutil.WaitFor(t, 10*time.Second, func() bool {
		stored, err := h.Store.Get(t.Context(), sess.ID)
		return err == nil && stored.Status == wire.SessionStatusError
	})
}

func TestCreateProjectWarmsPostureOverlay(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	mgr := h.SessionMgr

	projectDir := t.TempDir()
	overlayDir := filepath.Join(projectDir, settingsoverlay.DirName())
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(filepath.Join(overlayDir, "postures.yaml"), []byte(`
postures:
  spec:
    label: Audit
`), 0o644); err != nil {
		t.Fatal(err)
	}

	createBody := `{"roots":[{"path":"` + projectDir + `"}]}`
	req := authedRequest(t, http.MethodPost, "/v1/projects", strings.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d body = %s", w.Code, w.Body.String())
	}

	if err := mgr.Profiles.WarmPostureOverlay(projectDir); err != nil {
		testutil.FailErr(t, "mgr.Profiles.WarmPostureOverlay failed", err)
	}
}
