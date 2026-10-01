package security

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestProjectRemovalCascadeAndSSE(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	dir := t.TempDir()
	projectRow := createTestProjectHTTP(t, srv, dir)

	_ = createSessionForProjectHTTP(t, srv, projectRow.ID, wire.SessionPostureBuild)

	assessReq := authedRequest(t, http.MethodGet, "/v1/projects/"+projectRow.ID+"/removal-assessment", nil)
	assessW := httptest.NewRecorder()
	srv.ServeHTTP(assessW, assessReq)
	if assessW.Code != http.StatusOK {
		t.Fatalf("removal assessment status = %d body = %s", assessW.Code, assessW.Body.String())
	}
	var assessment wire.ProjectRemovalAssessment
	testutil.FailErr(t, "decode removal assessment", json.Unmarshal(assessW.Body.Bytes(), &assessment))
	removal, err := json.Marshal(wire.ProjectRemovalRequest{OperationID: uuid.NewString(), AssessmentToken: assessment.AssessmentToken})
	testutil.FailErr(t, "encode removal request", err)
	removeReq := authedRequest(t, http.MethodPost, "/v1/projects/"+projectRow.ID+"/removals", bytes.NewReader(removal))
	removeReq.Header.Set("Content-Type", "application/json")
	removeW := httptest.NewRecorder()
	srv.ServeHTTP(removeW, removeReq)
	if removeW.Code != http.StatusAccepted {
		t.Fatalf("removal status = %d body = %s", removeW.Code, removeW.Body.String())
	}
	var result wire.ProjectRemovalResult
	testutil.FailErr(t, "decode removal result", json.Unmarshal(removeW.Body.Bytes(), &result))
	if result.ProjectState != "deleted" {
		t.Fatalf("removal result = %+v", result)
	}

	getReq := authedRequest(t, http.MethodGet, "/v1/projects/"+projectRow.ID, nil)
	getW := httptest.NewRecorder()
	srv.ServeHTTP(getW, getReq)
	if getW.Code != http.StatusNotFound {
		t.Fatalf("get after removal status = %d want 404", getW.Code)
	}
}

func TestAttachProjectRootDuplicate409(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	dir := t.TempDir()
	projectRow := createTestProjectHTTP(t, srv, dir)

	body := `{"path":"` + dir + `"}`
	req := authedRequest(t, http.MethodPost, "/v1/projects/"+projectRow.ID+"/roots", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate attach status = %d body = %s", w.Code, w.Body.String())
	}
	var errBody struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("decode conflict body: %v", err)
	}
	if errBody.Code != "duplicate_root" {
		t.Fatalf("code = %q want duplicate_root", errBody.Code)
	}
}

func TestPatchProjectRootDuplicateLabel409(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	dirA := t.TempDir()
	dirB := t.TempDir()
	projectRow := createTestProjectHTTP(t, srv, dirA)

	attach := `{"path":"` + dirB + `"}`
	attachReq := authedRequest(t, http.MethodPost, "/v1/projects/"+projectRow.ID+"/roots", strings.NewReader(attach))
	attachReq.Header.Set("Content-Type", "application/json")
	attachW := httptest.NewRecorder()
	srv.ServeHTTP(attachW, attachReq)
	if attachW.Code != http.StatusOK && attachW.Code != http.StatusCreated {
		t.Fatalf("attach second root status = %d body = %s", attachW.Code, attachW.Body.String())
	}
	var withTwo wire.Project
	if err := json.Unmarshal(attachW.Body.Bytes(), &withTwo); err != nil {
		t.Fatalf("decode project: %v", err)
	}
	if len(withTwo.Roots) < 2 {
		t.Fatalf("want 2 roots, got %d", len(withTwo.Roots))
	}
	var primaryLabel, otherID string
	for _, r := range withTwo.Roots {
		if r.IsPrimary {
			primaryLabel = r.Label
		} else {
			otherID = r.ID
		}
	}
	if primaryLabel == "" || otherID == "" {
		t.Fatalf("missing primary label or other root id: roots=%+v", withTwo.Roots)
	}

	patchBody := `{"label":"` + primaryLabel + `"}`
	patchReq := authedRequest(t, http.MethodPatch, "/v1/projects/"+projectRow.ID+"/roots/"+otherID, strings.NewReader(patchBody))
	patchReq.Header.Set("Content-Type", "application/json")
	patchW := httptest.NewRecorder()
	srv.ServeHTTP(patchW, patchReq)
	if patchW.Code != http.StatusConflict {
		t.Fatalf("duplicate label status = %d body = %s", patchW.Code, patchW.Body.String())
	}
	var errBody struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(patchW.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("decode conflict body: %v", err)
	}
	if errBody.Code != "duplicate_root_label" {
		t.Fatalf("code = %q want duplicate_root_label", errBody.Code)
	}
}

func createTestProjectHTTP(t *testing.T, srv http.Handler, dir string) wire.Project {
	t.Helper()
	body := `{"roots":[{"path":"` + dir + `"}]}`
	req := authedRequest(t, http.MethodPost, "/v1/projects", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create project status = %d body = %s", w.Code, w.Body.String())
	}
	var out wire.Project
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode project: %v", err)
	}
	return out
}
