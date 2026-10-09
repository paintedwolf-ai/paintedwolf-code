package sourcecontracts

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/api/briefingadmin"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/filebriefing"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	hostapi "github.com/lycaon/lycaon/internal/api"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestRequestFileBriefingReturnsStructureBeforeGeneration(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	root := t.TempDir()
	testutil.FailErr(t, "write source fixture", os.WriteFile(filepath.Join(root, "main.go"), []byte(`package main

func Build() error {
	return nil
}
`), 0o600))
	projects := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), projects, root)
	testutil.FailErr(t, "create project", err)
	hub := events.NewMemoryHub()
	cfg, err := filebriefing.LoadConfig()
	testutil.FailErr(t, "load file briefing config", err)
	briefings := filebriefing.NewMemory()
	deps := contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: store.NewMemory(), Projects: projects}, Host: hostapi.HostDependencies{Events: hub}})
	contractfixture.WithTestFileBriefings(t, briefings, cfg)(&deps)
	srv := hostapi.NewServer(deps, nil, hostapi.TestAPIToken)
	contractfixture.StopBackgroundOnCleanup(t, srv)

	body := `{"root_id":"` + p.Roots[0].ID + `","path":"main.go","presentation":"current","trigger":"manual"}`
	invalidBody := strings.TrimSuffix(body, "}") + `,"version_id":"version-ignored"}`
	invalidReq := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/projects/"+p.ID+"/source/file-briefings", strings.NewReader(invalidBody))
	invalidReq.Header.Set("Content-Type", "application/json")
	invalid := httptest.NewRecorder()
	srv.ServeHTTP(invalid, invalidReq)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("mixed presentation status = %d body = %s", invalid.Code, invalid.Body.String())
	}
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/projects/"+p.ID+"/source/file-briefings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("POST status = %d body = %s", w.Code, w.Body.String())
	}
	var response wire.FileBriefingResponse
	testutil.FailErr(t, "decode briefing response", json.Unmarshal(w.Body.Bytes(), &response))
	if response.Status != string(filebriefing.StatusPending) || response.Preview.LineCount == 0 {
		t.Fatalf("immediate response = %+v", response)
	}
	if len(response.Locations) == 0 {
		t.Fatalf("preview structure = %+v locations=%+v", response.Preview, response.Locations)
	}

	contractfixture.DrainBackground(t, srv)
	persisted, err := briefings.Get(t.Context(), p.ID, p.Roots[0].ID, "main.go", response.TargetKey)
	testutil.FailErr(t, "get terminal briefing", err)
	if persisted.Status != filebriefing.StatusFailed {
		t.Fatalf("terminal status = %q", persisted.Status)
	}

	automaticBody := strings.Replace(body, `"trigger":"manual"`, `"trigger":"automatic"`, 1)
	automaticReq := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/projects/"+p.ID+"/source/file-briefings", strings.NewReader(automaticBody))
	automaticReq.Header.Set("Content-Type", "application/json")
	automaticW := httptest.NewRecorder()
	srv.ServeHTTP(automaticW, automaticReq)
	if automaticW.Code != http.StatusAccepted {
		t.Fatalf("automatic POST status = %d body = %s", automaticW.Code, automaticW.Body.String())
	}
	contractfixture.DrainBackground(t, srv)
	persisted, err = briefings.Get(t.Context(), p.ID, p.Roots[0].ID, "main.go", response.TargetKey)
	testutil.FailErr(t, "get deterministic fallback", err)
	if persisted.Status != filebriefing.StatusPreview {
		t.Fatalf("automatic fallback status = %q", persisted.Status)
	}

	getPath := "/v1/projects/" + p.ID + "/source/file-briefings?root_id=" + p.Roots[0].ID + "&path=main.go&presentation=current"
	getW := httptest.NewRecorder()
	srv.ServeHTTP(getW, contractfixture.NewAuthedRequest(http.MethodGet, getPath, nil))
	if getW.Code != http.StatusOK {
		t.Fatalf("cached GET status = %d body = %s", getW.Code, getW.Body.String())
	}
	testutil.FailErr(t, "change source fixture", os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc Changed() {}\n"), 0o600))
	changedW := httptest.NewRecorder()
	srv.ServeHTTP(changedW, contractfixture.NewAuthedRequest(http.MethodGet, getPath, nil))
	if changedW.Code != http.StatusNotFound {
		t.Fatalf("changed presentation GET status = %d body = %s", changedW.Code, changedW.Body.String())
	}
}

func TestFileBriefingReadsImmutableRetainedVersion(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	root := t.TempDir()
	projects := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), projects, root)
	testutil.FailErr(t, "create project", err)
	ledger, ledgerDB, withLedger := contractfixture.TestSourceLedger(t)
	testdbseed.InsertProjectRootWithID(t, ledgerDB, p.ID, p.Roots[0].ID, p.Roots[0].Path)
	cfg, err := filebriefing.LoadConfig()
	testutil.FailErr(t, "load file briefing config", err)
	briefings := filebriefing.NewMemory()
	deps := hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: store.NewMemory(), Projects: projects}, Host: hostapi.HostDependencies{Events: events.NewMemoryHub()}}
	withLedger(&deps)
	deps = contractfixture.RequiredTestDeps(t, deps)
	contractfixture.WithTestFileBriefings(t, briefings, cfg)(&deps)
	srv := hostapi.NewServer(deps, nil, hostapi.TestAPIToken)
	contractfixture.StopBackgroundOnCleanup(t, srv)

	oldSource := []byte("package main\n\nfunc Original() {}\n")
	currentSource := []byte("package main\n\nfunc Current() {}\n")
	testutil.FailErr(t, "write current source", os.WriteFile(filepath.Join(root, "main.go"), currentSource, 0o600))
	testutil.FailErr(t, "record source versions", ledger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID, RootID: p.Roots[0].ID, Path: "main.go", Op: wire.SourceChangeOpWrite,
		Origin: wire.SourceChangeOriginUser, SessionID: "briefing-version", Before: oldSource, After: currentSource,
	}))
	walk, err := ledger.QueryWalk(t.Context(), p.ID,
		sourceledger.Baseline{Kind: sourceledger.BaselineSession, SessionID: "briefing-version"},
		10, 0, sourceledger.CommitLens{})
	testutil.FailErr(t, "query source versions", err)
	if len(walk.Files) != 1 || len(walk.Files[0].Effects) != 1 {
		t.Fatalf("walk = %+v", walk.Files)
	}
	versionID := walk.Files[0].Effects[0].BeforeVersionID
	version, err := ledger.CompareVersions(t.Context(), p.ID, versionID)
	testutil.FailErr(t, "read retained version", err)

	body, err := json.Marshal(wire.FileBriefingRequest{
		RootID: p.Roots[0].ID, Path: "main.go", Presentation: "version",
		VersionID: versionID, Trigger: "automatic",
	})
	testutil.FailErr(t, "marshal version briefing", err)
	Post := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/projects/"+p.ID+"/source/file-briefings", strings.NewReader(string(body)))
	Post.Header.Set("Content-Type", "application/json")
	postW := httptest.NewRecorder()
	srv.ServeHTTP(postW, Post)
	if postW.Code != http.StatusAccepted {
		t.Fatalf("version POST status = %d body = %s", postW.Code, postW.Body.String())
	}
	var response wire.FileBriefingResponse
	testutil.FailErr(t, "decode version briefing", json.Unmarshal(postW.Body.Bytes(), &response))
	if response.Presentation != "version" || response.SourceSHA256 != version.After.SHA256 || response.Preview.LineCount != 3 {
		t.Fatalf("version response = %+v", response)
	}

	testutil.FailErr(t, "change current source", os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc Later() {}\n"), 0o600))
	getPath := "/v1/projects/" + p.ID + "/source/file-briefings?root_id=" + p.Roots[0].ID +
		"&path=main.go&presentation=version&version_id=" + versionID
	getW := httptest.NewRecorder()
	srv.ServeHTTP(getW, contractfixture.NewAuthedRequest(http.MethodGet, getPath, nil))
	if getW.Code != http.StatusOK {
		t.Fatalf("version GET status = %d body = %s", getW.Code, getW.Body.String())
	}
	var cached wire.FileBriefingResponse
	testutil.FailErr(t, "decode cached version briefing", json.Unmarshal(getW.Body.Bytes(), &cached))
	if cached.TargetKey != response.TargetKey || cached.SourceSHA256 != response.SourceSHA256 {
		t.Fatalf("cached version changed: first=%+v cached=%+v", response, cached)
	}
}

func TestGetFileBriefingResumesOrphanedPendingWork(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	root := t.TempDir()
	testutil.FailErr(t, "write source fixture", os.WriteFile(
		filepath.Join(root, "main.go"), []byte("package main\n\nfunc Build() {}\n"), 0o600,
	))
	projects := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), projects, root)
	testutil.FailErr(t, "create project", err)
	cfg, err := filebriefing.LoadConfig()
	testutil.FailErr(t, "load file briefing config", err)
	briefings := filebriefing.NewMemory()
	deps := contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: store.NewMemory(), Projects: projects}, Host: hostapi.HostDependencies{Events: events.NewMemoryHub()}})
	contractfixture.WithTestFileBriefings(t, briefings, cfg)(&deps)
	srv := hostapi.NewServer(deps, nil, hostapi.TestAPIToken)
	contractfixture.StopBackgroundOnCleanup(t, srv)

	request := wire.FileBriefingRequest{
		RootID: p.Roots[0].ID, Path: "main.go", Presentation: "current", Trigger: "automatic",
	}
	read, err := projectsource.ReadProjectSource(p, projectsource.SourceReadRequest{Path: request.Path, RootID: request.RootID})
	testutil.FailErr(t, "read briefing source", err)
	input := filebriefing.Input{Path: read.Path, Presentation: request.Presentation, Source: read.Content, SourceSHA256: read.SHA256}
	targetKey := srv.Sources.Briefings.FileBriefings.TargetKey(filebriefing.Target{ProjectID: p.ID, RootID: read.RootID, ProjectDir: p.Roots[0].Path, Input: input})
	material, err := filebriefing.BuildMaterial(t.Context(), input, cfg)
	testutil.FailErr(t, "build briefing material", err)
	_, err = briefings.Start(t.Context(), filebriefing.Briefing{
		ProjectID: p.ID, RootID: read.RootID, Path: read.Path, TargetKey: targetKey,
		Presentation: "current", SourceSHA256: input.SourceSHA256, Trigger: "automatic",
		Status: filebriefing.StatusPending, Preview: material.Preview, Locations: material.Locations,
		UpdatedAt: time.Now().UTC(),
	})
	testutil.FailErr(t, "seed orphaned pending briefing", err)

	getPath := "/v1/projects/" + p.ID + "/source/file-briefings?root_id=" + p.Roots[0].ID +
		"&path=main.go&presentation=current"
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, contractfixture.NewAuthedRequest(http.MethodGet, getPath, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET status = %d body = %s", w.Code, w.Body.String())
	}
	contractfixture.DrainBackground(t, srv)
	got, err := briefings.Get(t.Context(), p.ID, read.RootID, read.Path, targetKey)
	testutil.FailErr(t, "read resumed briefing", err)
	if got.Status != filebriefing.StatusPreview {
		t.Fatalf("resumed briefing status = %q, want preview", got.Status)
	}
}

func TestFileBriefingVersionUnavailableUsesSpecificCode(t *testing.T) {
	code, _, ok := briefingadmin.FileBriefingRequestError(sourceledger.ErrVersionUnavailable)
	if !ok || code != wire.ApiErrorCodeSourceVersionUnavailable || code.HTTPStatus() != http.StatusUnprocessableEntity {
		t.Fatalf("error mapping = %q", code)
	}
}

func TestFileBriefingUnexpectedResolutionErrorStaysInternal(t *testing.T) {
	if _, _, ok := briefingadmin.FileBriefingRequestError(errors.New("storage path detail")); ok {
		t.Fatal("unexpected resolution error mapped to a client response")
	}
}

// withTestFileBriefings serves briefings from store, generating through the
// dependencies' model service, sessions, events, and File summaries setting.
