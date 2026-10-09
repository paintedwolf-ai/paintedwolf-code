package hostcontracts

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/project"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDetachProjectRootRemovesWorkerSeed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	rootDir := filepath.Join(home, "repo")
	testutil.FailErr(t, "mkdir root", os.MkdirAll(rootDir, 0o755))

	reg := project.NewMemoryRegistry()
	p, err := reg.Create(t.Context(), project.CreateParams{Roots: []project.AttachRootParams{{Path: rootDir}}})
	testutil.FailErr(t, "create project", err)
	if len(p.Roots) != 1 {
		t.Fatalf("roots = %d want 1", len(p.Roots))
	}
	seedRoot := enginepaths.WorkerSeedsRootUnder(filepath.Join(home, ".config", "paintedwolf"))
	seedDir := enginepaths.ProjectSeedDir(seedRoot, p.Roots[0].Path)
	testutil.FailErr(t, "mkdir seed", os.MkdirAll(seedDir, 0o700))

	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: sessionstore.NewMemory(), Projects: reg}, Storage: hostapi.StorageDependencies{WorkerSeedRoot: seedRoot}}), nil, hostapi.TestAPIToken)
	req := contractfixture.NewAuthedRequest(http.MethodDelete, "/v1/projects/"+p.ID+"/roots/"+p.Roots[0].ID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(seedDir); !os.IsNotExist(err) {
		t.Fatalf("worker seed still present: %v", err)
	}
}
