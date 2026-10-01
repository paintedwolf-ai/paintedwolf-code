package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestCreateProjectNormalizesRelativePath(t *testing.T) {
	srv := wiring.BuildForTest(t).Server
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		testutil.FailErr(t, "os.Mkdir failed", err)
	}

	dirty := filepath.Join(root, "sub", "..", "sub")
	req := authedRequest(t, http.MethodPost, "/v1/projects", strings.NewReader(
		`{"roots":[{"path":"`+dirty+`"}]}`,
	))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}

	var proj wire.Project
	if err := json.Unmarshal(w.Body.Bytes(), &proj); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	want, err := filepath.Abs(sub)
	testutil.FailErr(t, "filepath.Abs failed", err)
	if resolved, err := filepath.EvalSymlinks(want); err == nil {
		want = resolved
	}
	projPath := primaryRootPath(proj)
	if projPath != want {
		t.Fatalf("path = %q, want %q", projPath, want)
	}
	if strings.Contains(projPath, "..") {
		t.Fatalf("path still contains ..: %q", projPath)
	}
}

func TestCreateProjectRejectsFilePath(t *testing.T) {
	srv := wiring.BuildForTest(t).Server
	file := filepath.Join(t.TempDir(), "not-a-dir.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}

	req := authedRequest(t, http.MethodPost, "/v1/projects", strings.NewReader(
		`{"roots":[{"path":"`+file+`"}]}`,
	))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	errResp := decodeAPIError(t, w)
	if errResp.Code != "invalid_path" {
		t.Fatalf("code = %q, want invalid_path", errResp.Code)
	}
}

func TestCreateProjectMissingPathNotFound(t *testing.T) {
	srv := wiring.BuildForTest(t).Server
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	req := authedRequest(t, http.MethodPost, "/v1/projects", strings.NewReader(
		`{"roots":[{"path":"`+missing+`"}]}`,
	))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	errResp := decodeAPIError(t, w)
	if errResp.Code != "path_not_found" {
		t.Fatalf("code = %q, want path_not_found", errResp.Code)
	}
}

// primaryRootPath mirrors the primary-or-first root selection Den applies to wire projects.
func primaryRootPath(p wire.Project) string {
	for _, r := range p.Roots {
		if r.IsPrimary {
			return r.Path
		}
	}
	if len(p.Roots) > 0 {
		return p.Roots[0].Path
	}
	return ""
}
