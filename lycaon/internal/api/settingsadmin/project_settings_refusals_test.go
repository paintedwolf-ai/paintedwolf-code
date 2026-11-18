package settingsadmin

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/apitestdeps"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// An unreadable project limits, verify, or review file is reported on every
// read and refuses a save that would replace it.
func TestUnreadableProjectSettingsAreReportedAndProtected(t *testing.T) {
	deps := apitestdeps.Deps{}
	apitestdeps.Fill(t, &deps)
	dir := t.TempDir()
	proj, err := deps.Projects.Create(t.Context(), project.CreateParams{Name: "p", Roots: []project.AttachRootParams{{Path: dir}}})
	testutil.FailErr(t, "create project", err)
	files := map[string]string{
		settingsoverlay.BasenameLimits: "max_iteration: 7\n",
		settingsoverlay.BasenameVerify: "tests: make test\n",
		settingsoverlay.BasenameReview: "review_paths: [\n",
	}
	for name, body := range files {
		path := settingsoverlay.ProjectOverlayPath(dir, name)
		testutil.FailErr(t, "create overlay", os.MkdirAll(filepath.Dir(path), 0o755))
		testutil.FailErr(t, "write "+name, os.WriteFile(path, []byte(body), 0o600))
	}
	h := &Handler{
		Deps:      Deps{Events: deps.Events, Projects: deps.Projects, Sessions: deps.Sessions, Service: deps.Settings},
		responses: &httpio.Responder{Logger: slog.New(slog.DiscardHandler)},
	}
	target := "/v1/settings/x?scope=project&project_id=" + proj.ID
	for _, tc := range []struct {
		name   string
		get    http.HandlerFunc
		update http.HandlerFunc
		body   string
	}{
		{settingsoverlay.BasenameLimits, h.HandleGetLimits, h.HandleUpdateLimits, `{"max_iterations":5}`},
		{settingsoverlay.BasenameVerify, h.HandleGetVerify, h.HandleUpdateVerify, `{"test":"make check"}`},
		{settingsoverlay.BasenameReview, h.HandleGetReview, h.HandleUpdateReview, `{"review_paths":[]}`},
	} {
		rec := httptest.NewRecorder()
		tc.get(rec, httptest.NewRequest(http.MethodGet, target, nil))
		var resp struct {
			Rejected []wire.ProjectSettingsRejectedRow `json:"rejected"`
		}
		testutil.FailErr(t, "decode "+tc.name, json.Unmarshal(rec.Body.Bytes(), &resp))
		if rec.Code != http.StatusOK || len(resp.Rejected) != 1 || !strings.Contains(resp.Rejected[0].Detail, tc.name) {
			t.Fatalf("%s read: status=%d body=%s", tc.name, rec.Code, rec.Body.String())
		}
		req := httptest.NewRequest(http.MethodPut, target, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		rec = httptest.NewRecorder()
		tc.update(rec, req)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), string(wire.ApiErrorCodeProjectSettingsNeedRepair)) {
			t.Fatalf("%s save: status=%d body=%s", tc.name, rec.Code, rec.Body.String())
		}
		if got, _ := os.ReadFile(settingsoverlay.ProjectOverlayPath(dir, tc.name)); string(got) != files[tc.name] {
			t.Fatalf("%s was overwritten:\n%s", tc.name, got)
		}
		// Once the file is fixed, the same save lands.
		testutil.FailErr(t, "remove "+tc.name, os.Remove(settingsoverlay.ProjectOverlayPath(dir, tc.name)))
		req = httptest.NewRequest(http.MethodPut, target, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		rec = httptest.NewRecorder()
		tc.update(rec, req)
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"rejected"`) {
			t.Fatalf("%s save after the fix: status=%d body=%s", tc.name, rec.Code, rec.Body.String())
		}
	}
}
