package hostcontracts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestConcurrentProjectClonesPreserveRegisteredCheckout(t *testing.T) {
	srv, _ := contractfixture.NewProjectOverlayTestServer(t, func(d *hostapi.Dependencies) { d.Workflow.Board = contractfixture.NewGitBoard() })

	contractfixture.StopBackgroundOnCleanup(t, srv)
	origin := contractfixture.InitCommittedRepoDir(t)
	parent := t.TempDir()
	body, err := json.Marshal(wire.CloneProjectRequest{URL: origin, ParentDir: parent, Name: "checkout"})
	testutil.FailErr(t, "marshal clone", err)
	const count = 4
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, count)
	for range count {
		go func() {
			<-start
			r := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/projects/clone", strings.NewReader(string(body)))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, r)
			results <- w
		}()
	}
	close(start)
	succeeded := 0
	for range count {
		w := <-results
		switch w.Code {
		case http.StatusCreated:
			succeeded++
		case http.StatusConflict:
			var out wire.ErrorResponse
			testutil.FailErr(t, "decode clone conflict", json.Unmarshal(w.Body.Bytes(), &out))
			if out.Code != "folder_exists" {
				t.Errorf("clone refusal code = %s", out.Code)
			}
		default:
			t.Errorf("clone status=%d body=%s", w.Code, w.Body.String())
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful clone requests = %d, want 1", succeeded)
	}
	dest := filepath.Join(parent, "checkout")
	if _, err := os.Stat(filepath.Join(dest, ".git")); err != nil {
		testutil.FailErr(t, "winning checkout retained", err)
	}
	_, err = srv.Admin.Project.Sandboxes.Board.Git.Status(t.Context(), dest)
	testutil.FailErr(t, "winning checkout status", err)
}

func TestProjectCloneRejectsNonLeafFolderNames(t *testing.T) {
	srv, _ := contractfixture.NewProjectOverlayTestServer(t, func(d *hostapi.Dependencies) { d.Workflow.Board = contractfixture.NewGitBoard() })

	for _, name := range []string{".", "..", "../outside", "nested/folder", `nested\folder`} {
		body, err := json.Marshal(wire.CloneProjectRequest{URL: "https://example.invalid/repo.git", ParentDir: t.TempDir(), Name: name})
		testutil.FailErr(t, "marshal clone", err)
		r := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/projects/clone", strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("name %q: status=%d body=%s", name, w.Code, w.Body.String())
		}
	}
}
