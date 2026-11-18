package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func createAPISessionForProject(t *testing.T, serve http.Handler, projectDir, posture string) wire.Session {
	t.Helper()
	createBody := `{"roots":[{"path":"` + projectDir + `"}]}`
	createReq := AuthedRequest(t, http.MethodPost, "/v1/projects", strings.NewReader(createBody))
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

// writePostureLabel writes a project overlay that relabels the spec posture.
func writePostureLabel(t *testing.T, projectDir, label string) {
	t.Helper()
	overlayDir := filepath.Join(projectDir, settingsoverlay.DirName())
	testutil.FailErr(t, "create overlay directory", os.MkdirAll(overlayDir, 0o755))
	testutil.FailErr(t, "write postures overlay", os.WriteFile(filepath.Join(overlayDir, "postures.yaml"),
		[]byte("postures:\n  spec:\n    label: "+label+"\n"), 0o644))
}

// effectiveSpecLabel reads the spec posture label the session resolves.
func effectiveSpecLabel(t *testing.T, h *wiring.Harness, sessionID string) string {
	t.Helper()
	stored, err := h.Store.Get(t.Context(), sessionID)
	testutil.FailErr(t, "load session", err)
	postures, err := h.Sessions.Manager.Profiles.EffectivePostures(t.Context(), stored)
	testutil.FailErr(t, "EffectivePostures", err)
	spec, err := postures.Get(wire.SessionPostureSpec)
	testutil.FailErr(t, "spec posture", err)
	return spec.Label
}

func TestPostureOverlayWarmOnCreateSession(t *testing.T) {
	h := wiring.BuildForTest(t)
	projectDir := t.TempDir()
	writePostureLabel(t, projectDir, "Audit")

	sess := createAPISessionForProject(t, h.Server, projectDir, "spec")

	if got := effectiveSpecLabel(t, h, sess.ID); got != "Audit" {
		t.Fatalf("spec posture label = %q, want the overlay's Audit", got)
	}
	got, err := h.Sessions.Manager.Profiles.ResolvePromptToolProfile(t.Context(), sess.ID)
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
		testutil.FailErr(t, "write file", err)
	}

	createBody := `{"roots":[{"path":"` + projectDir + `"}]}`
	createReq := AuthedRequest(t, http.MethodPost, "/v1/projects", strings.NewReader(createBody))
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
	req := AuthedRequest(t, http.MethodPost, "/v1/sessions", strings.NewReader(body))
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
	testutil.WaitFor(t, "the session is in error", func() bool {
		stored, err := h.Store.Get(t.Context(), sess.ID)
		return err == nil && stored.Status == wire.SessionStatusError
	})
}

// The overlay cache is keyed by root, so a session created after the file
// changes still sees the overlay POST /v1/projects loaded.
func TestCreateProjectWarmsPostureOverlay(t *testing.T) {
	h := wiring.BuildForTest(t)
	projectDir := t.TempDir()
	writePostureLabel(t, projectDir, "Audit")

	createBody := `{"roots":[{"path":"` + projectDir + `"}]}`
	req := AuthedRequest(t, http.MethodPost, "/v1/projects", strings.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Server.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d body = %s", w.Code, w.Body.String())
	}
	var proj wire.Project
	testutil.FailErr(t, "decode project", json.Unmarshal(w.Body.Bytes(), &proj))

	writePostureLabel(t, projectDir, "Rewritten")
	sess := createSessionForProjectHandlerHTTP(t, h.Server, proj.ID, wire.SessionPostureSpec)
	if got := effectiveSpecLabel(t, h, sess.ID); got != "Audit" {
		t.Fatalf("spec posture label = %q, want Audit warmed by project create", got)
	}
}
