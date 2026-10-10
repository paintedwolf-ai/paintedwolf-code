package hostcontracts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestHandlePromoteProjectSavesScratchIntoFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	srv, reg := contractfixture.NewProjectOverlayTestServer(t)

	p, err := reg.Create(t.Context(), project.CreateParams{Draft: true})
	testutil.FailErr(t, "create draft project", err)
	if len(p.Roots) != 1 {
		t.Fatalf("expected 1 scratch root on draft create, got %d", len(p.Roots))
	}
	beforeRootID := p.Roots[0].ID
	scratch := p.Roots[0].Path
	testutil.FailErr(t, "seed scratch file", os.WriteFile(filepath.Join(scratch, "notes.md"), []byte("draft work"), 0o644))

	folder := t.TempDir()
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/projects/"+p.ID+"/promotion",
		strings.NewReader(`{"root_path":"`+folder+`"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("promote status = %d body = %s", w.Code, w.Body.String())
	}

	var got wire.Project
	testutil.FailErr(t, "unmarshal project", json.Unmarshal(w.Body.Bytes(), &got))
	if got.IsDraft {
		t.Fatalf("project still draft after promote")
	}
	if len(got.Roots) != 1 {
		t.Fatalf("expected 1 root after promote, got %d", len(got.Roots))
	}
	if got.Roots[0].ID != beforeRootID {
		t.Fatalf("root id changed on promote: got %q want %q", got.Roots[0].ID, beforeRootID)
	}
	folderResolved := got.Roots[0].Path
	if data, readErr := os.ReadFile(filepath.Join(folderResolved, "notes.md")); readErr != nil || string(data) != "draft work" {
		t.Fatalf("saved file = %q err=%v", data, readErr)
	}
	if _, statErr := os.Stat(scratch); !os.IsNotExist(statErr) {
		t.Fatalf("scratch dir still present after save: err=%v", statErr)
	}
}

func TestHandlePromoteProjectRejectsNonEmptyFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	srv, reg := contractfixture.NewProjectOverlayTestServer(t)

	p, err := reg.Create(t.Context(), project.CreateParams{Draft: true})
	testutil.FailErr(t, "create draft project", err)

	folder := t.TempDir()
	testutil.FailErr(t, "make folder non-empty", os.WriteFile(filepath.Join(folder, "existing.txt"), []byte("x"), 0o644))

	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/projects/"+p.ID+"/promotion",
		strings.NewReader(`{"root_path":"`+folder+`"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body = %s", w.Code, w.Body.String())
	}
	var errBody wire.ErrorResponse
	testutil.FailErr(t, "unmarshal error", json.Unmarshal(w.Body.Bytes(), &errBody))
	if errBody.Code != "folder_not_empty" {
		t.Fatalf("error code = %q want folder_not_empty (body=%s)", errBody.Code, w.Body.String())
	}
}

func TestHandlePromoteProjectRejectsDifferentQueuedSettings(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv, reg := contractfixture.NewProjectOverlayTestServer(t)
	p, err := reg.Create(t.Context(), project.CreateParams{Draft: true})
	testutil.FailErr(t, "create draft project", err)
	destination := t.TempDir()
	_, err = srv.Admin.Project.Promotion.PromotionEngine().Create(t.Context(), p.ID, destination, true)
	testutil.FailErr(t, "create promotion", err)

	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/projects/"+p.ID+"/promotion",
		strings.NewReader(`{"root_path":"`+destination+`","init_git":false}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("promote status = %d want %d body = %s", w.Code, http.StatusConflict, w.Body.String())
	}
	var errBody wire.ErrorResponse
	testutil.FailErr(t, "unmarshal error", json.Unmarshal(w.Body.Bytes(), &errBody))
	if errBody.Code != "promotion_conflict" {
		t.Fatalf("error code = %q want promotion_conflict", errBody.Code)
	}
}

func TestHandleCancelProjectPromotion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv, reg := contractfixture.NewProjectOverlayTestServer(t)
	p, err := reg.Create(t.Context(), project.CreateParams{Draft: true})
	testutil.FailErr(t, "create draft", err)
	destination := t.TempDir()
	_, err = srv.Admin.Project.Promotion.PromotionEngine().Create(t.Context(), p.ID, destination, false)
	testutil.FailErr(t, "create promotion", err)

	req := contractfixture.NewAuthedRequest(http.MethodDelete, "/v1/projects/"+p.ID+"/promotion", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("cancel status = %d body = %s", w.Code, w.Body.String())
	}
	projAfter, err := reg.Get(t.Context(), p.ID)
	testutil.FailErr(t, "get project after cancel", err)
	got := project.ToAPI(projAfter)
	if !got.IsDraft || got.Promotion != nil || len(got.Roots) != 1 || got.Roots[0].Kind != string(project.RootKindDraft) {
		t.Fatalf("canceled project = %#v", got)
	}
}
