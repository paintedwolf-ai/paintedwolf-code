package security

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestOperationIDConflictSweepE2E(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	h := wiring.BuildForTest(t)
	httpSrv := httptest.NewServer(h.Server)
	t.Cleanup(httpSrv.Close)
	base := httpSrv.URL

	projectDir := t.TempDir()
	for _, name := range []string{"conflict.txt", "rename-from.txt", "copy-from.txt", "trash-me.txt", "trash-other.txt"} {
		if err := os.WriteFile(filepath.Join(projectDir, name), []byte("v1\n"), 0o644); err != nil {
			testutil.FailErr(t, "seed source", err)
		}
	}
	proj := createAPIProjectAtPath(t, base, projectDir)
	sess := openAPIPostJSON[wire.Session](t, base, "/v1/sessions", nil,
		`{"project_id":"`+proj.ID+`","posture":"spec"}`, http.StatusAccepted)

	read := openAPIGetJSON[wire.ProjectSourceReadResponse](t, base,
		"/v1/projects/"+proj.ID+"/source?path=conflict.txt", nil, http.StatusOK)

	t.Run("POST prompts", func(t *testing.T) {
		op := uuid.NewString()
		assertOperationConflict(t, base, http.MethodPost,
			"/v1/sessions/"+sess.ID+"/prompts", http.StatusAccepted,
			`{"operation_id":"`+op+`","text":"first prose"}`,
			`{"operation_id":"`+op+`","text":"different prose"}`)
	})

	t.Run("POST workflow-runs", func(t *testing.T) {
		workflowSession := openAPIPostJSON[wire.Session](t, base, "/v1/sessions", nil,
			`{"project_id":"`+proj.ID+`","posture":"spec"}`, http.StatusAccepted)
		op := uuid.NewString()
		start := func(blueprintTitle string) string {
			return workflowStartJSON(t, base, workflowSession.ID, map[string]any{
				"operation_id": op, "workflow_id": "plan", "workflow_version": "1.0.1", "blueprint_title": blueprintTitle,
			})
		}
		assertOperationConflict(t, base, http.MethodPost,
			"/v1/sessions/"+workflowSession.ID+"/workflow-runs", http.StatusCreated,
			start("first-plan"), start("second-plan"))
		exitPath, exitBody := workflowExitCall(t, base, workflowSession.ID, "")
		_ = journeyPostJSON[wire.WorkflowRun](t, base, exitPath, exitBody, http.StatusOK)
	})

	t.Run("PUT source", func(t *testing.T) {
		op := uuid.NewString()
		write := func(content string) string {
			return fmt.Sprintf(`{"operation_id":%q,"path":"conflict.txt","content":%q,"encoding":%q,"base_sha256":%q}`,
				op, content, string(read.Encoding), read.SHA256)
		}
		assertOperationConflict(t, base, http.MethodPut,
			"/v1/projects/"+proj.ID+"/source", http.StatusOK, write("v2\n"), write("v3\n"))
	})

	t.Run("POST source create", func(t *testing.T) {
		op := uuid.NewString()
		create := func(path string) string {
			return fmt.Sprintf(`{"operation_id":%q,"path":%q,"kind":"file"}`, op, path)
		}
		assertSourceOperationConflict(t, base, proj.ID, http.MethodPost,
			"/v1/projects/"+proj.ID+"/source", http.StatusCreated,
			create("created-a.txt"), create("created-b.txt"))
	})

	t.Run("POST source rename", func(t *testing.T) {
		op := uuid.NewString()
		rename := func(to string) string {
			return fmt.Sprintf(`{"operation_id":%q,"from":"rename-from.txt","to":%q}`, op, to)
		}
		assertSourceOperationConflict(t, base, proj.ID, http.MethodPost,
			"/v1/projects/"+proj.ID+"/source/rename", http.StatusOK,
			rename("renamed.txt"), rename("renamed-elsewhere.txt"))
	})

	t.Run("POST source copy", func(t *testing.T) {
		op := uuid.NewString()
		copyReq := func(to string) string {
			return fmt.Sprintf(`{"operation_id":%q,"from":"copy-from.txt","to":%q}`, op, to)
		}
		assertSourceOperationConflict(t, base, proj.ID, http.MethodPost,
			"/v1/projects/"+proj.ID+"/source/copy", http.StatusOK,
			copyReq("copied.txt"), copyReq("copied-elsewhere.txt"))
	})

	t.Run("DELETE source", func(t *testing.T) {
		op := uuid.NewString()
		del := func(path string) string {
			return "/v1/projects/" + proj.ID + "/source?path=" + path + "&operation_id=" + op
		}
		settleSourceRequest(t, base, proj.ID, http.MethodDelete, del("trash-me.txt"), "", http.StatusNoContent)
		assertConflictCode(t, base, http.MethodDelete, del("trash-other.txt"), "")
	})
}

// assertOperationConflict lands the first request so a receipt exists, then
// replays the same operation identity with mutated input.
func assertOperationConflict(t *testing.T, base, method, path string, firstStatus int, first, conflicting string) {
	t.Helper()
	openAPIDo(t, base, method, path, nil, first, firstStatus)
	assertConflictCode(t, base, method, path, conflicting)
}

// assertConflictCode requires the documented refusal for a conflicting replay.
func assertConflictCode(t *testing.T, base, method, path, body string) {
	t.Helper()
	raw := openAPIDo(t, base, method, path, nil, body, http.StatusConflict)
	var errResp wire.ErrorResponse
	if err := json.Unmarshal(raw, &errResp); err != nil {
		testutil.FailErr(t, "decode conflict response", err)
	}
	if errResp.Code != "idempotency_conflict" {
		t.Fatalf("%s %s conflict code = %q, want idempotency_conflict; body = %s", method, path, errResp.Code, raw)
	}
}

// Source mutations may return a durable operation before verification finishes.
func settleSourceRequest(t *testing.T, base, projectID, method, path, body string, wantStatus int) {
	t.Helper()
	res := validatedRequest(t, base, method, path, nil, body)
	AssertHTTPResponseMatchesOpenAPI(t, res.StatusCode, res.Header, res.Body, method, path, nil)
	if res.StatusCode == wantStatus {
		return
	}
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("source mutation status = %d, want %d or 202; body=%s", res.StatusCode, wantStatus, res.Body)
	}
	status := decodeOpenAPI[wire.SourceOperationStatus](t, method, path, res.Body)
	if status.OperationID == "" {
		t.Fatal("accepted source mutation lacks operation_id")
	}
	params := map[string]string{"id": projectID, "operation_id": status.OperationID}
	testutil.WaitFor(t, func() bool {
		status = openAPIGetJSON[wire.SourceOperationStatus](t, base, "/v1/projects/{id}/source/operations/{operation_id}", params, http.StatusOK)
		return status.Complete
	})
	if wantStatus >= 400 {
		if status.Error == nil {
			t.Fatalf("source mutation expected error status %d, got result: %v", wantStatus, status.Result)
		}
	} else if status.Error != nil {
		t.Fatalf("source mutation settled with error: %v", status.Error)
	}
}

func assertSourceOperationConflict(t *testing.T, base, projectID, method, path string, firstStatus int, first, conflicting string) {
	t.Helper()
	settleSourceRequest(t, base, projectID, method, path, first, firstStatus)
	assertConflictCode(t, base, method, path, conflicting)
}
