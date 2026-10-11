package hostcontracts

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/filebriefing"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestFileSummariesSettingsDisablePurgesAndRejectsBriefings(t *testing.T) {
	briefings := filebriefing.NewMemory()
	cfg, err := filebriefing.LoadConfig()
	testutil.FailErr(t, "load File summaries config", err)
	srv, _, projects := contractfixture.NewSettingsTestServer(t, contractfixture.WithTestFileBriefings(t, briefings, cfg))

	root := t.TempDir()
	p, err := project.CreateWithRoot(t.Context(), projects, root)
	testutil.FailErr(t, "create project", err)
	stored := filebriefing.Briefing{
		ProjectID: p.ID, RootID: p.Roots[0].ID, Path: "main.go", TargetKey: "stored",
		Presentation: "current", SourceSHA256: "sha", Trigger: "manual",
		Status: filebriefing.StatusPending, UpdatedAt: time.Now().UTC(),
	}
	_, err = briefings.Start(t.Context(), stored)
	testutil.FailErr(t, "seed stored briefing", err)

	get := httptest.NewRecorder()
	srv.ServeHTTP(get, contractfixture.NewAuthedRequest(http.MethodGet, "/v1/settings/file-summaries", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", get.Code, get.Body.String())
	}
	var initial wire.FileSummariesSettingsResponse
	testutil.FailErr(t, "decode initial setting", json.Unmarshal(get.Body.Bytes(), &initial))
	if !initial.Enabled {
		t.Fatal("File summaries should default enabled")
	}

	Put := httptest.NewRecorder()
	req := contractfixture.NewAuthedRequest(http.MethodPatch, "/v1/settings/file-summaries", strings.NewReader(`{"enabled":false}`))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(Put, req)
	if Put.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", Put.Code, Put.Body.String())
	}
	if _, err := briefings.Get(t.Context(), stored.ProjectID, stored.RootID, stored.Path, stored.TargetKey); !errors.Is(err, filebriefing.ErrNotFound) {
		t.Fatalf("stored briefing error = %v, want ErrNotFound", err)
	}

	briefingURL := "/v1/projects/" + p.ID + "/source/file-briefings?root_id=" + p.Roots[0].ID + "&path=main.go&presentation=current"
	rejected := httptest.NewRecorder()
	srv.ServeHTTP(rejected, contractfixture.NewAuthedRequest(http.MethodGet, briefingURL, nil))
	contractfixture.AssertErrorResponse(t, rejected, http.StatusConflict, "file_briefing_disabled")

	Post := httptest.NewRecorder()
	postReq := contractfixture.NewAuthedRequest(http.MethodPost, briefingURL, strings.NewReader(`{}`))
	postReq.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(Post, postReq)
	contractfixture.AssertErrorResponse(t, Post, http.StatusConflict, "file_briefing_disabled")
}

func TestFileSummariesSettingsDisableKeepsSettingOnWhenPurgeFails(t *testing.T) {
	cfg, err := filebriefing.LoadConfig()
	testutil.FailErr(t, "load briefing config", err)
	srv, _, _ := contractfixture.NewSettingsTestServer(t, contractfixture.WithTestFileBriefings(t, contractfixture.FailingClearFileBriefingStore{Store: filebriefing.NewMemory()}, cfg))

	Put := httptest.NewRecorder()
	req := contractfixture.NewAuthedRequest(http.MethodPatch, "/v1/settings/file-summaries", strings.NewReader(`{"enabled":false}`))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(Put, req)
	if Put.Code != http.StatusInternalServerError {
		t.Fatalf("PUT status = %d body=%s", Put.Code, Put.Body.String())
	}
	if !srv.Admin.Project.Trust.Settings.FileSummaries.Enabled() {
		t.Fatal("File summaries setting changed after purge failed")
	}
}
