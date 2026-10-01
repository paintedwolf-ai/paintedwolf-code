package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestProjectAgentContextReportsActualPathChain(t *testing.T) {
	srv := trustSettingsServer(t)
	root := t.TempDir()
	testutil.FailErr(t, "mkdir nested", os.MkdirAll(filepath.Join(root, "cmd", "app"), 0o755))
	testutil.FailErr(t, "mkdir overlay", os.MkdirAll(filepath.Join(root, ".paintedwolf"), 0o755))
	for rel, body := range map[string]string{
		"AGENTS.md":              "root\n",
		"cmd/AGENTS.md":          "cmd\n",
		".paintedwolf/AGENTS.md": "overlay\n",
	} {
		testutil.FailErr(t, "write "+rel, os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte(body), 0o644))
	}
	p, err := project.CreateWithRoot(t.Context(), srv.projectRegistry, root)
	testutil.FailErr(t, "CreateWithRoot", err)

	load := func() wire.ProjectAgentContext {
		t.Helper()
		req := newAuthedRequest(http.MethodGet,
			"/v1/projects/"+p.ID+"/agent-context?root_id="+p.Roots[0].ID+"&path=cmd/app/main.go", nil)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var got wire.ProjectAgentContext
		testutil.FailErr(t, "decode context", json.Unmarshal(w.Body.Bytes(), &got))
		return got
	}

	got := load()
	if !got.InstructionsEnabled {
		t.Fatal("AGENTS.md should be enabled by default")
	}
	want := []string{"AGENTS.md", "cmd/AGENTS.md", ".paintedwolf/AGENTS.md"}
	if len(got.Instructions) != len(want) {
		t.Fatalf("instructions=%+v want %v", got.Instructions, want)
	}
	for i, path := range want {
		if got.Instructions[i].Path != path {
			t.Fatalf("instructions[%d]=%q want %q", i, got.Instructions[i].Path, path)
		}
	}

	_, err = srv.projectRegistry.SetTrustEnabled(t.Context(), p.ID,
		map[string]bool{projectcontrib.SurfaceAgentsMD: false})
	testutil.FailErr(t, "disable AGENTS.md", err)
	got = load()
	if got.InstructionsEnabled || len(got.Instructions) != 0 {
		t.Fatalf("disabled instructions appeared in context: %+v", got)
	}
}

func TestProjectAgentContextRejectsPathOutsideSelectedRoot(t *testing.T) {
	srv := trustSettingsServer(t)
	p, err := project.CreateWithRoot(t.Context(), srv.projectRegistry, t.TempDir())
	testutil.FailErr(t, "CreateWithRoot", err)
	req := newAuthedRequest(http.MethodGet,
		"/v1/projects/"+p.ID+"/agent-context?root_id="+p.Roots[0].ID+"&path=../outside", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s want path-boundary rejection", w.Code, w.Body.String())
	}
}
