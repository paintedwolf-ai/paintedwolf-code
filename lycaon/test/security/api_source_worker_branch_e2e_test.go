package security

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

// worker_id reads unpromoted files from the worker branch.
func TestProjectSourceReadsLiveWorkerOverlay(t *testing.T) {
	h := wiring.BuildForTest(t)
	httpSrv := httptest.NewServer(h.Server)
	t.Cleanup(httpSrv.Close)
	base := httpSrv.URL

	projectDir := t.TempDir()
	project := createAPIProjectAtPath(t, base, projectDir)

	workerID, err := h.WorkerQueue.Enqueue(t.Context(), wire.WorkerTask{
		Prompt:        "fixture",
		Brief:         "fixture",
		ProjectID:     project.ID,
		WorkspacePath: projectDir,
		Scope:         &wire.TaskScope{Mode: wire.TaskScopeModeWrite, Paths: []string{"."}},
	})
	testutil.FailErr(t, "enqueue write worker", err)

	// The first isolating operation provisions the private branch.
	claimed, err := h.WorkerQueue.ClaimNext(t.Context(), worker.ClaimRequest{
		ProjectID:       project.ID,
		ClaimedBy:       "source-branch-test",
		ExecutionTarget: wire.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "claim write worker", err)
	if claimed.ID != workerID {
		t.Fatalf("claimed %q want %q", claimed.ID, workerID)
	}
	if strings.TrimSpace(claimed.WorkspaceRoot) != "" {
		t.Fatal("ClaimNext must not provision a private branch")
	}
	claimed, err = h.WorkerQueue.ClaimWorkerBranch(t.Context(), workerID)
	testutil.FailErr(t, "ClaimWorkerBranch", err)
	overlayDir := claimed.WorkspaceRoot
	if overlayDir == "" {
		t.Fatal("ClaimWorkerBranch returned empty overlay workspace root")
	}
	if overlayDir == projectDir {
		t.Fatal("overlay root must not be the project root")
	}

	const rel = "internal/cli/root.go"
	const content = "package cli\n"
	abs := filepath.Join(overlayDir, filepath.FromSlash(rel))
	testutil.FailErr(t, "mkdir overlay", os.MkdirAll(filepath.Dir(abs), 0o755))
	testutil.FailErr(t, "write overlay", os.WriteFile(abs, []byte(content), 0o644))

	sourcePath := func(query url.Values) string {
		return "/v1/projects/" + project.ID + "/source?" + query.Encode()
	}

	t.Run("without worker_id the project roots do not have it yet", func(t *testing.T) {
		getJSON[map[string]any](t, base, sourcePath(url.Values{
			"path": {rel},
		}), http.StatusNotFound)
	})

	t.Run("worker_id serves the file as the worker left it", func(t *testing.T) {
		got := getJSON[wire.ProjectSourceReadResponse](t, base, sourcePath(url.Values{
			"path":      {rel},
			"worker_id": {workerID},
		}), http.StatusOK)
		if got.Path != rel {
			t.Fatalf("path = %q want %q", got.Path, rel)
		}
		if got.Content != content {
			t.Fatalf("content = %q want %q", got.Content, content)
		}
	})

	t.Run("a branch never widens the jail", func(t *testing.T) {
		getJSON[map[string]any](t, base, sourcePath(url.Values{
			"path":      {"/etc/passwd"},
			"worker_id": {workerID},
		}), http.StatusForbidden)
	})

	t.Run("unknown worker_id falls back to the project roots", func(t *testing.T) {
		getJSON[map[string]any](t, base, sourcePath(url.Values{
			"path":      {rel},
			"worker_id": {"3f2a1b0c-0000-4000-8000-000000000000"},
		}), http.StatusNotFound)
	})

	// Released branches resolve reads against the promoted project files.
	t.Run("released overlay falls back to the promoted file", func(t *testing.T) {
		landed := filepath.Join(projectDir, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir primary", os.MkdirAll(filepath.Dir(landed), 0o755))
		testutil.FailErr(t, "write primary", os.WriteFile(landed, []byte(content), 0o644))

		got := getJSON[wire.ProjectSourceReadResponse](t, base, sourcePath(url.Values{
			"path":      {rel},
			"worker_id": {"3f2a1b0c-0000-4000-8000-000000000000"},
		}), http.StatusOK)
		if got.Content != content {
			t.Fatalf("content = %q", got.Content)
		}
	})
}
