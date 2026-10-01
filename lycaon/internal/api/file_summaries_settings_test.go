package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/filebriefing"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestFileSummariesSettingsDisablePurgesAndRejectsBriefings(t *testing.T) {
	briefings := filebriefing.NewMemory()
	cfg, err := filebriefing.LoadConfig()
	testutil.FailErr(t, "load File summaries config", err)
	srv, _, projects := newSettingsTestServer(t, withTestFileBriefings(t, briefings, cfg))

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
	srv.ServeHTTP(get, newAuthedRequest(http.MethodGet, "/v1/settings/file-summaries", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", get.Code, get.Body.String())
	}
	var initial wire.FileSummariesSettingsResponse
	testutil.FailErr(t, "decode initial setting", json.Unmarshal(get.Body.Bytes(), &initial))
	if !initial.Enabled {
		t.Fatal("File summaries should default enabled")
	}

	put := httptest.NewRecorder()
	req := newAuthedRequest(http.MethodPatch, "/v1/settings/file-summaries", strings.NewReader(`{"enabled":false}`))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(put, req)
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", put.Code, put.Body.String())
	}
	if _, err := briefings.Get(t.Context(), stored.ProjectID, stored.RootID, stored.Path, stored.TargetKey); !errors.Is(err, filebriefing.ErrNotFound) {
		t.Fatalf("stored briefing error = %v, want ErrNotFound", err)
	}

	briefingURL := "/v1/projects/" + p.ID + "/source/file-briefings?root_id=" + p.Roots[0].ID + "&path=main.go&presentation=current"
	rejected := httptest.NewRecorder()
	srv.ServeHTTP(rejected, newAuthedRequest(http.MethodGet, briefingURL, nil))
	assertErrorResponse(t, rejected, http.StatusConflict, "file_briefing_disabled")

	post := httptest.NewRecorder()
	postReq := newAuthedRequest(http.MethodPost, briefingURL, strings.NewReader(`{}`))
	postReq.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(post, postReq)
	assertErrorResponse(t, post, http.StatusConflict, "file_briefing_disabled")
}

type failingClearFileBriefingStore struct {
	filebriefing.Store
}

func (failingClearFileBriefingStore) Clear(context.Context) error {
	return errors.New("clear failed")
}

func TestFileSummariesSettingsDisableKeepsSettingOnWhenPurgeFails(t *testing.T) {
	cfg, err := filebriefing.LoadConfig()
	testutil.FailErr(t, "load briefing config", err)
	srv, _, _ := newSettingsTestServer(t, withTestFileBriefings(t, failingClearFileBriefingStore{Store: filebriefing.NewMemory()}, cfg))

	put := httptest.NewRecorder()
	req := newAuthedRequest(http.MethodPatch, "/v1/settings/file-summaries", strings.NewReader(`{"enabled":false}`))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(put, req)
	if put.Code != http.StatusInternalServerError {
		t.Fatalf("PUT status = %d body=%s", put.Code, put.Body.String())
	}
	if !srv.settingsSvc.FileSummaries.Enabled() {
		t.Fatal("File summaries setting changed after purge failed")
	}
}
