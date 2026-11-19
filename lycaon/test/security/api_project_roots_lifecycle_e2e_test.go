package security

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/rootfacts"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestDetachThenReadDenied(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := httptest.NewServer(h.Server)
	t.Cleanup(srv.Close)
	base := srv.URL

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("hello"), 0o600); err != nil {
		testutil.FailErr(t, "write fixture file", err)
	}

	project := CreateAPIProjectAtPath(t, base, dir)
	if project.RootsGeneration != 0 {
		t.Fatalf("initial generation = %d want 0", project.RootsGeneration)
	}
	rootID := project.Roots[0].ID

	sess := OpenAPIPostJSON[struct {
		ID string `json:"id"`
	}](t, base, "/v1/sessions", nil,
		`{"project_id":"`+project.ID+`","posture":"build"}`, http.StatusAccepted)

	req := AuthedRequest(t, http.MethodDelete, "/v1/projects/"+project.ID+"/roots/"+rootID, nil)
	w := httptest.NewRecorder()
	h.Server.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("detach status = %d body = %s", w.Code, w.Body.String())
	}

	updated := OpenAPIGetJSON[struct {
		Roots           []any `json:"roots"`
		RootsGeneration int   `json:"roots_generation"`
	}](t, base, "/v1/projects/{id}", map[string]string{"id": project.ID}, http.StatusOK)
	if len(updated.Roots) != 0 {
		t.Fatalf("roots = %d want 0 after detach", len(updated.Roots))
	}
	if updated.RootsGeneration != 1 {
		t.Fatalf("roots_generation = %d want 1", updated.RootsGeneration)
	}

	gotSess, err := h.Store.Get(t.Context(), sess.ID)
	testutil.FailErr(t, "get session", err)
	if gotSess.WorkspaceRootID != "" {
		t.Fatalf("workspace_root_id = %q want empty", gotSess.WorkspaceRootID)
	}

	_, err = h.ToolRegistry.Run(t.Context(), "command", map[string]any{"command": "cat note.txt"}, tools.ToolContext{
		Identity: tools.InvocationIdentity{ProjectID: project.ID,
			SessionID: sess.ID},
		Source: tools.InvocationSource{Roots: nil},
	})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "PROJECT_HAS_NO_ROOTS" {
		t.Fatalf("read err = %v want PROJECT_HAS_NO_ROOTS", err)
	}
}

// admissionJSONPath quotes a filesystem path for a request body.
func admissionJSONPath(t *testing.T, path string) string {
	t.Helper()
	b, err := json.Marshal(path)
	if err != nil {
		t.Fatalf("quote %s: %v", path, err)
	}
	return string(b)
}

// assertProjectRootEditorOnly creates a project on path over the real server
// and requires the root to be registered editor-only with code, both in the
// create response and when the project is read back, while the execution
// boundary refuses the same root with the same code.
func assertProjectRootEditorOnly(t *testing.T, base, path, code string) {
	t.Helper()
	created := createAPIProject(t, base, `{"roots":[{"path":`+admissionJSONPath(t, path)+`}]}`)
	read := OpenAPIGetJSON[wire.Project](t, base, "/v1/projects/{id}", map[string]string{"id": created.ID}, http.StatusOK)
	for name, p := range map[string]wire.Project{"created": created, "read back": read} {
		if len(p.Roots) != 1 || p.Roots[0].Admitted || p.Roots[0].AdmissionCode == nil || *p.Roots[0].AdmissionCode != code {
			t.Fatalf("%s root %s = %+v, want editor-only with %q", name, path, p.Roots, code)
		}
	}
	if err := confine.ValidateAttachedWriteRoots([]string{path}); err == nil || err.Code != code {
		t.Fatalf("execution boundary for %s: %v, want code %q", path, err, code)
	}
}

// assertProjectRootAdmitted is the positive control: an ordinary folder is
// admitted with no code.
func assertProjectRootAdmitted(t *testing.T, base string) {
	t.Helper()
	created := CreateAPIProjectAtPath(t, base, t.TempDir())
	if len(created.Roots) != 1 || !created.Roots[0].Admitted || created.Roots[0].AdmissionCode != nil {
		t.Fatalf("positive control: ordinary root = %+v, want admitted", created.Roots)
	}
}

// A folder holding the state tree registers editor-only.
func TestConfinementIntegrityProjectRootStateAncestorRefused(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := httptest.NewServer(h.Server)
	t.Cleanup(srv.Close)
	ancestor := rootfacts.Install(t).ContainsState
	assertProjectRootEditorOnly(t, srv.URL, ancestor, confine.WriteRootCodeContainsState)
	assertProjectRootAdmitted(t, srv.URL)
}

// A caller holding the bearer token still cannot make the state tree an
// agent write root: it registers editor-only as a secret store.
func TestConfinementIntegrityTokenHolderCannotAdmitControlPlaneRoot(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := httptest.NewServer(h.Server)
	t.Cleanup(srv.Close)
	cfg := os.Getenv("LYCAON_CONFIG_DIR")
	if cfg == "" {
		t.Fatal("harness did not set LYCAON_CONFIG_DIR")
	}
	assertProjectRootEditorOnly(t, srv.URL, cfg, confine.WriteRootCodeSecretStore)
	assertProjectRootAdmitted(t, srv.URL)
}
