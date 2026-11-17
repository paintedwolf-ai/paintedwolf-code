package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil/rootfacts"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

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
	read := openAPIGetJSON[wire.Project](t, base, "/v1/projects/{id}", map[string]string{"id": created.ID}, http.StatusOK)
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
	created := createAPIProjectAtPath(t, base, t.TempDir())
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
