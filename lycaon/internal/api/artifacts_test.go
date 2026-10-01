package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/api/apitest"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/storageusage"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/pkg/api"
)

func durableArtifacts(t *testing.T, dataDir, projectID string, sessionIDs ...string) *visual.DurableStore {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProject(t, sqlDB, projectID)
	for _, id := range sessionIDs {
		testdbseed.InsertSession(t, sqlDB, id, projectID)
	}
	return visual.NewDurableStore(visual.DurableConfig{
		ArtifactsDir: func(pid string) (string, error) {
			dir := filepath.Join(dataDir, "projects", pid, "artifacts")
			if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
				return "", mkErr
			}
			return dir, nil
		},
		Lookup:  func(context.Context, string) (string, error) { return projectID, nil },
		Records: visual.NewRecords(sqlDB, eventoutbox.New(sqlDB, nil), visual.ArtifactProjection{}),
	})
}

func TestHandleUploadLiveToolRecordingRejectsInvalidInput(t *testing.T) {
	projects, projectID := artifactProject(t)
	sessionStore := store.NewMemory()
	sess, err := sessionStore.Create(t.Context(), api.CreateSessionRequest{
		ProjectID: projectID,
		Posture:   api.SessionPostureBuild,
	}, projectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "append recording origin", sessionStore.AppendMessages(t.Context(), sess.ID, recordingMessage("message-1", "call-1")))
	srv := hostapi.NewServer(apitest.Dependencies(t, hostapi.Dependencies{Projects: projects, Store: sessionStore, VisualStore: visual.NewMemoryStore()}), nil, hostapi.TestAPIToken)
	router := chi.NewRouter()
	router.Post("/v1/sessions/{id}/artifacts", srv.HandleUploadLiveToolRecordingForTest())

	tests := []struct {
		name          string
		query         string
		contentType   string
		body          string
		contentLength int64
		wantStatus    int
		wantCode      string
	}{
		{name: "page", contentType: "video/mp4", body: "x", wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "transcript origin", query: "?page_id=page-1&operation_id=" + uuid.NewString(), contentType: "video/mp4", body: "x", wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "operation", query: recordingQuery(""), contentType: "video/mp4", body: "x", wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "operation not a uuid", query: recordingQuery("abc"), contentType: "video/mp4", body: "x", wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "unknown transcript origin", query: "?" + recordingParams(uuid.NewString(), "message-x", "call-x"), contentType: "video/mp4", body: "x", wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "mime", query: recordingQuery(uuid.NewString()), contentType: "video/webm", body: "x", wantStatus: http.StatusUnsupportedMediaType, wantCode: "unsupported_media_type"},
		{name: "duration", query: recordingQuery(uuid.NewString()) + "&duration_ms=-1", contentType: "video/mp4", body: "x", wantStatus: http.StatusBadRequest, wantCode: "recording_duration_invalid"},
		{name: "start", query: recordingQuery(uuid.NewString()) + "&recorded_at=not-a-time", contentType: "video/mp4", body: "x", wantStatus: http.StatusBadRequest, wantCode: "recording_start_invalid"},
		{name: "empty", query: recordingQuery(uuid.NewString()), contentType: "video/mp4", wantStatus: http.StatusBadRequest, wantCode: "artifact_empty"},
		{name: "too large", query: recordingQuery(uuid.NewString()), contentType: "video/mp4", body: "x", contentLength: int64(visual.MaxVideoBytes) + 1, wantStatus: http.StatusRequestEntityTooLarge, wantCode: "artifact_too_large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/artifacts"+tt.query, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", tt.contentType)
			if tt.contentLength > 0 {
				req.ContentLength = tt.contentLength
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d want %d; body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			var response api.ErrorResponse
			testutil.FailErr(t, "decode error response", json.Unmarshal(rec.Body.Bytes(), &response))
			if string(response.Code) != tt.wantCode {
				t.Fatalf("code = %q want %q", response.Code, tt.wantCode)
			}
		})
	}
}

func TestHandleUploadLiveToolRecordingPersistsMP4(t *testing.T) {
	projects, projectID := artifactProject(t)
	sessionStore := store.NewMemory()
	sess, err := sessionStore.Create(t.Context(), api.CreateSessionRequest{
		ProjectID: projectID,
		Posture:   api.SessionPostureBuild,
	}, projectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "append recording origins", sessionStore.AppendMessages(t.Context(), sess.ID,
		recordingMessage("message-1", "call-1"),
		recordingMessage("message-2", "call-2"),
	))
	visualStore := durableArtifacts(t, t.TempDir(), projectID, sess.ID)
	srv := hostapi.NewServer(apitest.Dependencies(t, hostapi.Dependencies{Projects: projects, Store: sessionStore, VisualStore: visualStore}), nil, hostapi.TestAPIToken)
	router := chi.NewRouter()
	router.Post("/v1/sessions/{id}/artifacts", srv.HandleUploadLiveToolRecordingForTest())
	router.Get("/v1/sessions/{id}/artifacts/{artifact_id}", srv.HandleSessionArtifactForTest())

	var artifactID string
	for _, mimeType := range []string{"video/mp4", "video/mp4;codecs=avc1.42E01E"} {
		t.Run(mimeType, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost,
				"/v1/sessions/"+sess.ID+"/artifacts"+recordingQuery(uuid.NewString()),
				strings.NewReader("movie "+mimeType))
			req.Header.Set("Content-Type", mimeType)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d want %d; body = %s", rec.Code, http.StatusCreated, rec.Body.String())
			}
			var artifact api.VisualArtifact
			testutil.FailErr(t, "decode uploaded artifact", json.Unmarshal(rec.Body.Bytes(), &artifact))
			wantMIME, _, err := mime.ParseMediaType(mimeType)
			testutil.FailErr(t, "parse expected MIME", err)
			if artifact.Mime != wantMIME {
				t.Fatalf("artifact MIME = %q want %q", artifact.Mime, wantMIME)
			}
			if artifact.Caption != "Live tool recording" {
				t.Fatalf("artifact caption = %q want live tool recording", artifact.Caption)
			}
			if artifact.PageID != "page-1" {
				t.Fatalf("artifact page_id = %q want page-1", artifact.PageID)
			}
			if artifact.OriginMessageID != "message-1" || artifact.ToolCallID != "call-1" {
				t.Fatalf("artifact origin = (%q, %q) want (message-1, call-1)", artifact.OriginMessageID, artifact.ToolCallID)
			}
			if artifactID != "" && artifact.ID != artifactID {
				t.Fatalf("artifact id = %q want stable page artifact %q", artifact.ID, artifactID)
			}
			artifactID = artifact.ID
			fetch := httptest.NewRecorder()
			router.ServeHTTP(fetch, httptest.NewRequest(http.MethodGet, "/v1/sessions/"+sess.ID+"/artifacts/"+artifact.ID, nil))
			if fetch.Code != http.StatusOK {
				t.Fatalf("fetch status = %d want %d; body = %s", fetch.Code, http.StatusOK, fetch.Body.String())
			}
			if got := fetch.Header().Get("Content-Type"); got != wantMIME {
				t.Fatalf("fetched content type = %q want %q", got, wantMIME)
			}
		})
	}

	reanchor := httptest.NewRequest(http.MethodPost,
		"/v1/sessions/"+sess.ID+"/artifacts?"+recordingParams(uuid.NewString(), "message-2", "call-2"),
		strings.NewReader("second invocation"))
	reanchor.Header.Set("Content-Type", "video/mp4")
	reanchorRec := httptest.NewRecorder()
	router.ServeHTTP(reanchorRec, reanchor)
	if reanchorRec.Code != http.StatusCreated {
		t.Fatalf("reanchor status = %d want %d; body = %s", reanchorRec.Code, http.StatusCreated, reanchorRec.Body.String())
	}
	var reanchored api.VisualArtifact
	testutil.FailErr(t, "decode reanchored artifact", json.Unmarshal(reanchorRec.Body.Bytes(), &reanchored))
	if reanchored.ID == artifactID {
		t.Fatalf("new invocation reused artifact %q", artifactID)
	}
}

func recordingQuery(operationID string) string {
	return "?" + recordingParams(operationID, "message-1", "call-1")
}

func recordingParams(operationID, messageID, toolCallID string) string {
	return "page_id=page-1&assistant_message_id=" + messageID + "&tool_call_id=" + toolCallID + "&operation_id=" + operationID
}

func recordingMessage(messageID, toolCallID string) api.Message {
	return api.Message{
		ID: messageID, Role: api.MessageRoleAssistant,
		ToolCalls: []api.ToolCall{{ID: toolCallID, Name: "page_act"}},
	}
}

func TestHandleSessionArtifact_roundTrip(t *testing.T) {
	projects, projectID := artifactProject(t)
	store := store.NewMemory()
	sess, err := store.Create(t.Context(), api.CreateSessionRequest{
		ProjectID: projectID,
		Posture:   api.SessionPostureBuild,
	}, projectID)
	testutil.FailErr(t, "create session in store", err)
	visualStore := visual.NewMemoryStore()
	wireArt, err := visualStore.Put(t.Context(), sess.ID, visual.Entry{
		Meta: api.VisualArtifact{
			Mime:   "image/png",
			Source: api.VisualArtifactSourceRender,
		},
		Bytes: visual.TestPNG1x1Bytes(),
	})
	testutil.FailErr(t, "visualStore.Put failed", err)
	srv := hostapi.NewServer(apitest.Dependencies(t, hostapi.Dependencies{Projects: projects, Store: store, VisualStore: visualStore}), nil, hostapi.TestAPIToken)

	r := chi.NewRouter()
	r.Get("/v1/sessions/{id}/artifacts/{artifact_id}", srv.HandleSessionArtifactForTest())

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+sess.ID+"/artifacts/"+wireArt.ID, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content-type = %q", ct)
	}
	if got := rec.Body.Bytes(); len(got) != len(visual.TestPNG1x1Bytes()) {
		t.Fatalf("bytes len = %d", len(got))
	}
}

func TestHandleSessionArtifactUnavailable(t *testing.T) {
	projects, projectID := artifactProject(t)
	store := store.NewMemory()
	sess, err := store.Create(t.Context(), api.CreateSessionRequest{
		ProjectID: projectID,
		Posture:   api.SessionPostureBuild,
	}, projectID)
	testutil.FailErr(t, "create session in store", err)
	visualStore := visual.NewMemoryStore()
	root := sess.ID
	chunk := append(visual.TestPNG1x1Bytes(), make([]byte, 512*1024)...)
	var victim string
	for i := 0; i < 260; i++ {
		wireArt, err := visualStore.Put(t.Context(), root, visual.Entry{
			Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender},
			Bytes: chunk,
		})
		testutil.FailErr(t, "visualStore.Put failed", err)
		if i == 0 {
			victim = wireArt.ID
		}
	}
	srv := hostapi.NewServer(apitest.Dependencies(t, hostapi.Dependencies{Projects: projects, Store: store, VisualStore: visualStore}), nil, hostapi.TestAPIToken)
	r := chi.NewRouter()
	r.Get("/v1/sessions/{id}/artifacts/{artifact_id}", srv.HandleSessionArtifactForTest())

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+sess.ID+"/artifacts/"+victim, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestHandleSessionArtifact_childSessionResolvesTreeStore(t *testing.T) {
	projects, projectID := artifactProject(t)
	store := store.NewMemory()
	parent, err := store.Create(t.Context(), api.CreateSessionRequest{
		ProjectID: projectID,
		Posture:   api.SessionPostureBuild,
	}, projectID)
	testutil.FailErr(t, "create session in store", err)
	child, err := store.CreateChild(t.Context(), parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "store.CreateChild failed", err)
	visualStore := visual.NewMemoryStore()
	wireArt, err := visualStore.Put(t.Context(), parent.ID, visual.Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceCapture},
		Bytes: visual.TestPNG1x1Bytes(),
	})
	testutil.FailErr(t, "visualStore.Put failed", err)
	srv := hostapi.NewServer(apitest.Dependencies(t, hostapi.Dependencies{Projects: projects, Store: store, VisualStore: visualStore}), nil, hostapi.TestAPIToken)
	r := chi.NewRouter()
	r.Get("/v1/sessions/{id}/artifacts/{artifact_id}", srv.HandleSessionArtifactForTest())

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+child.ID+"/artifacts/"+wireArt.ID, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("child fetch status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleListArtifacts_scopesAndReferenceOnly(t *testing.T) {
	dataDir := t.TempDir()
	reg := project.NewMemoryRegistry()
	proj, err := reg.Create(t.Context(), project.CreateParams{Draft: true, Name: "list-scope"})
	testutil.FailErr(t, "reg.Create failed", err)
	sessStore := store.NewMemory()
	sessA, err := sessStore.Create(t.Context(), api.CreateSessionRequest{
		ProjectID: proj.ID,
		Posture:   api.SessionPostureBuild,
	}, proj.ID)
	testutil.FailErr(t, "sessStore.Create failed", err)
	sessB, err := sessStore.Create(t.Context(), api.CreateSessionRequest{
		ProjectID: proj.ID,
		Posture:   api.SessionPostureBuild,
	}, proj.ID)
	testutil.FailErr(t, "sessStore.Create failed", err)
	visualStore := durableArtifacts(t, dataDir, proj.ID, sessA.ID, sessB.ID)
	_, err = visualStore.Put(t.Context(), sessA.ID, visual.Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender, Caption: "a"},
		Bytes: visual.TestPNG1x1Bytes(),
	})
	testutil.FailErr(t, "put session A artifact", err)
	_, err = visualStore.Put(t.Context(), sessB.ID, visual.Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceCapture, Caption: "b"},
		Bytes: append(visual.TestPNG1x1Bytes(), 0),
	})
	testutil.FailErr(t, "put session B artifact", err)

	srv := hostapi.NewServer(apitest.Dependencies(t, hostapi.Dependencies{Store: sessStore, Projects: reg, VisualStore: visualStore}), nil, hostapi.TestAPIToken)
	r := chi.NewRouter()
	r.Get("/v1/sessions/{id}/artifacts", srv.HandleListSessionArtifactsForTest())
	r.Get("/v1/projects/{id}/artifacts", srv.HandleListProjectArtifactsForTest())

	treeRec := httptest.NewRecorder()
	r.ServeHTTP(treeRec, httptest.NewRequest(http.MethodGet, "/v1/sessions/"+sessA.ID+"/artifacts", nil))
	if treeRec.Code != http.StatusOK {
		t.Fatalf("session list status = %d body = %s", treeRec.Code, treeRec.Body.String())
	}
	var tree api.ArtifactListResponse
	if err := json.Unmarshal(treeRec.Body.Bytes(), &tree); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if len(tree.Artifacts) != 1 || tree.Artifacts[0].Caption != "a" || tree.Artifacts[0].SessionID != sessA.ID {
		t.Fatalf("session list = %+v", tree.Artifacts)
	}

	projRec := httptest.NewRecorder()
	r.ServeHTTP(projRec, httptest.NewRequest(http.MethodGet, "/v1/projects/"+proj.ID+"/artifacts", nil))
	if projRec.Code != http.StatusOK {
		t.Fatalf("project list status = %d body = %s", projRec.Code, projRec.Body.String())
	}
	var all api.ArtifactListResponse
	if err := json.Unmarshal(projRec.Body.Bytes(), &all); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if len(all.Artifacts) != 2 {
		t.Fatalf("project list len = %d want 2", len(all.Artifacts))
	}
	firstRec := httptest.NewRecorder()
	r.ServeHTTP(firstRec, httptest.NewRequest(http.MethodGet, "/v1/projects/"+proj.ID+"/artifacts?limit=1", nil))
	if firstRec.Code != http.StatusOK {
		t.Fatalf("first project page status = %d body = %s", firstRec.Code, firstRec.Body.String())
	}
	var first api.ArtifactListResponse
	testutil.FailErr(t, "decode first project page", json.Unmarshal(firstRec.Body.Bytes(), &first))
	if len(first.Artifacts) != 1 || first.NextCursor == "" {
		t.Fatalf("first project page = %+v", first)
	}

	secondRec := httptest.NewRecorder()
	r.ServeHTTP(secondRec, httptest.NewRequest(http.MethodGet,
		"/v1/projects/"+proj.ID+"/artifacts?limit=1&cursor="+first.NextCursor, nil))
	if secondRec.Code != http.StatusOK {
		t.Fatalf("second project page status = %d body = %s", secondRec.Code, secondRec.Body.String())
	}
	var second api.ArtifactListResponse
	testutil.FailErr(t, "decode second project page", json.Unmarshal(secondRec.Body.Bytes(), &second))
	if len(second.Artifacts) != 1 || second.Artifacts[0].ID == first.Artifacts[0].ID || second.NextCursor != "" {
		t.Fatalf("second project page = %+v", second)
	}

	tamperedRec := httptest.NewRecorder()
	r.ServeHTTP(tamperedRec, httptest.NewRequest(http.MethodGet,
		"/v1/projects/"+proj.ID+"/artifacts?limit=1&cursor="+first.NextCursor+"x", nil))
	if tamperedRec.Code != http.StatusBadRequest {
		t.Fatalf("tampered cursor status = %d body = %s", tamperedRec.Code, tamperedRec.Body.String())
	}
	var cursorErr api.ErrorResponse
	testutil.FailErr(t, "decode cursor error", json.Unmarshal(tamperedRec.Body.Bytes(), &cursorErr))
	if cursorErr.Code != "invalid_page_cursor" {
		t.Fatalf("tampered cursor code = %q want invalid_page_cursor", cursorErr.Code)
	}
	raw := projRec.Body.String()
	if strings.Contains(raw, `"bytes"`) || strings.Contains(raw, "base64") {
		t.Fatal("list response must not carry bytes/base64")
	}
	if strings.Contains(raw, dataDir) || strings.Contains(raw, filepath.Join(dataDir, "projects")) {
		t.Fatal("list response must not leak host filesystem paths")
	}

	miss := httptest.NewRecorder()
	r.ServeHTTP(miss, httptest.NewRequest(http.MethodGet, "/v1/sessions/missing/artifacts", nil))
	if miss.Code != http.StatusNotFound {
		t.Fatalf("unknown session status = %d", miss.Code)
	}
	missProj := httptest.NewRecorder()
	r.ServeHTTP(missProj, httptest.NewRequest(http.MethodGet, "/v1/projects/missing/artifacts", nil))
	if missProj.Code != http.StatusNotFound {
		t.Fatalf("unknown project status = %d", missProj.Code)
	}
}

func TestHandleArtifacts_durableRestartFetchAndGallery(t *testing.T) {
	dataDir := t.TempDir()
	reg := project.NewMemoryRegistry()
	proj, err := reg.Create(t.Context(), project.CreateParams{Draft: true, Name: "restart-gallery"})
	testutil.FailErr(t, "reg.Create failed", err)
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProject(t, sqlDB, proj.ID)
	lookup := func(_ context.Context, _ string) (string, error) { return proj.ID, nil }
	artifactsDir := func(pid string) (string, error) {
		dir := filepath.Join(dataDir, "projects", pid, "artifacts")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		return dir, nil
	}

	sessStore := store.NewMemory()
	sess, err := sessStore.Create(t.Context(), api.CreateSessionRequest{
		ProjectID: proj.ID,
		Posture:   api.SessionPostureBuild,
	}, proj.ID)
	testutil.FailErr(t, "sessStore.Create failed", err)
	testdbseed.InsertSession(t, sqlDB, sess.ID, proj.ID)
	hot := visual.NewDurableStore(visual.DurableConfig{
		DataDir:      dataDir,
		ArtifactsDir: artifactsDir,
		Lookup:       lookup,
		Records:      visual.NewRecords(sqlDB, eventoutbox.New(sqlDB, nil), visual.ArtifactProjection{}),
	})
	png := visual.TestPNG1x1Bytes()
	wireArt, err := hot.Put(t.Context(), sess.ID, visual.Entry{
		Meta: api.VisualArtifact{
			Mime: "image/png", Source: api.VisualArtifactSourceRender,
			Caption: "Signup — empty state",
		},
		Bytes: png,
	})
	testutil.FailErr(t, "hot.Put failed", err)

	hash := sha256.Sum256(png)
	blobPath := filepath.Join(dataDir, "projects", proj.ID, "artifacts", fmt.Sprintf("%x", hash))
	stored, err := os.ReadFile(blobPath)
	testutil.FailErr(t, "read encoded artifact", err)
	testutil.FailErr(t, "verify original media identity", visual.VerifyArtifactBody(bytes.NewReader(stored), fmt.Sprintf("%x", hash), int64(len(png))))
	if bytes.Equal(stored, png) {
		t.Fatal("artifact was not encoded at rest")
	}

	cold := visual.NewDurableStore(visual.DurableConfig{
		DataDir:      dataDir,
		ArtifactsDir: artifactsDir,
		Lookup:       lookup,
		Records:      visual.NewRecords(sqlDB, eventoutbox.New(sqlDB, nil), visual.ArtifactProjection{}),
	})
	srv := hostapi.NewServer(apitest.Dependencies(t, hostapi.Dependencies{Store: sessStore, Projects: reg, VisualStore: cold}), nil, hostapi.TestAPIToken)
	r := chi.NewRouter()
	r.Get("/v1/sessions/{id}/artifacts/{artifact_id}", srv.HandleSessionArtifactForTest())
	r.Get("/v1/projects/{id}/artifacts", srv.HandleListProjectArtifactsForTest())

	fetch := httptest.NewRecorder()
	r.ServeHTTP(fetch, httptest.NewRequest(http.MethodGet, "/v1/sessions/"+sess.ID+"/artifacts/"+wireArt.ID, nil))
	if fetch.Code != http.StatusOK {
		t.Fatalf("restart fetch status = %d body = %s", fetch.Code, fetch.Body.String())
	}
	if ct := fetch.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content-type = %q", ct)
	}
	if got := fetch.Body.Bytes(); len(got) != len(png) {
		t.Fatalf("fetch bytes len = %d want %d", len(got), len(png))
	}

	gallery := httptest.NewRecorder()
	r.ServeHTTP(gallery, httptest.NewRequest(http.MethodGet, "/v1/projects/"+proj.ID+"/artifacts", nil))
	if gallery.Code != http.StatusOK {
		t.Fatalf("gallery status = %d body = %s", gallery.Code, gallery.Body.String())
	}
	var list api.ArtifactListResponse
	if err := json.Unmarshal(gallery.Body.Bytes(), &list); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if len(list.Artifacts) != 1 || list.Artifacts[0].ID != wireArt.ID {
		t.Fatalf("gallery after restart = %+v want id %s", list.Artifacts, wireArt.ID)
	}
	body := gallery.Body.String()
	if strings.Contains(body, dataDir) || strings.Contains(body, `"bytes"`) {
		t.Fatal("gallery must stay path-free and byte-free after restart")
	}
}

// Deletion removes bytes while preserving reference identity.
func TestHandleDeleteProjectArtifact_tombstonesAndReportsImpact(t *testing.T) {
	dataDir := t.TempDir()
	reg := project.NewMemoryRegistry()
	proj, err := reg.Create(t.Context(), project.CreateParams{Draft: true, Name: "delete-artifact"})
	testutil.FailErr(t, "reg.Create failed", err)
	sessStore := store.NewMemory()
	sess, err := sessStore.Create(t.Context(), api.CreateSessionRequest{
		ProjectID: proj.ID,
		Posture:   api.SessionPostureBuild,
	}, proj.ID)
	testutil.FailErr(t, "sessStore.Create failed", err)
	visualStore := durableArtifacts(t, dataDir, proj.ID, sess.ID)
	wireArt, err := visualStore.Put(t.Context(), sess.ID, visual.Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender, Caption: "doomed"},
		Bytes: visual.TestPNG1x1Bytes(),
	})
	testutil.FailErr(t, "visualStore.Put failed", err)

	srv := hostapi.NewServer(apitest.Dependencies(t, hostapi.Dependencies{Store: sessStore, Projects: reg, VisualStore: visualStore}), nil, hostapi.TestAPIToken)
	r := chi.NewRouter()
	r.Delete("/v1/projects/{id}/artifacts/{artifact_id}", srv.HandleDeleteProjectArtifactForTest())
	r.Get("/v1/projects/{id}/artifacts", srv.HandleListProjectArtifactsForTest())
	r.Get("/v1/sessions/{id}/artifacts/{artifact_id}", srv.HandleSessionArtifactForTest())

	present := httptest.NewRecorder()
	r.ServeHTTP(present, httptest.NewRequest(http.MethodGet, "/v1/sessions/"+sess.ID+"/artifacts/"+wireArt.ID, nil))
	if present.Code != http.StatusOK || present.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("present artifact must not outlive deletion in an HTTP cache: status=%d cache=%q", present.Code, present.Header().Get("Cache-Control"))
	}

	del := httptest.NewRecorder()
	r.ServeHTTP(del, httptest.NewRequest(http.MethodDelete, "/v1/projects/"+proj.ID+"/artifacts/"+wireArt.ID, nil))
	if del.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d body = %s", del.Code, del.Body.String())
	}

	gallery := httptest.NewRecorder()
	r.ServeHTTP(gallery, httptest.NewRequest(http.MethodGet, "/v1/projects/"+proj.ID+"/artifacts", nil))
	var list api.ArtifactListResponse
	testutil.FailErr(t, "decode gallery", json.Unmarshal(gallery.Body.Bytes(), &list))
	if len(list.Artifacts) != 0 {
		t.Fatalf("deleted artifact still listed: %+v", list.Artifacts)
	}

	fetch := httptest.NewRecorder()
	r.ServeHTTP(fetch, httptest.NewRequest(http.MethodGet, "/v1/sessions/"+sess.ID+"/artifacts/"+wireArt.ID, nil))
	if fetch.Code != http.StatusGone {
		t.Fatalf("fetch after delete = %d want 410", fetch.Code)
	}
	var fetchErr api.ErrorResponse
	testutil.FailErr(t, "decode fetch error", json.Unmarshal(fetch.Body.Bytes(), &fetchErr))
	if fetchErr.Code != "artifact_deleted" {
		t.Fatalf("fetch code = %q want artifact_deleted", fetchErr.Code)
	}
	if got := fetch.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("artifact tombstone cache policy = %q want private, no-store", got)
	}

	missing := httptest.NewRecorder()
	r.ServeHTTP(missing, httptest.NewRequest(http.MethodDelete, "/v1/projects/"+proj.ID+"/artifacts/"+uuid.NewString(), nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("delete of unknown artifact = %d want 404", missing.Code)
	}
}

type resolutionStore struct {
	res        visual.Resolution
	storageErr error
}

func (s resolutionStore) Put(context.Context, string, visual.Entry) (api.VisualArtifact, error) {
	return api.VisualArtifact{}, nil
}
func (s resolutionStore) Resolve(context.Context, string, string) visual.Resolution { return s.res }
func (s resolutionStore) ListProject(context.Context, string, visual.ArtifactPageQuery) (visual.ArtifactPage, error) {
	return visual.ArtifactPage{}, nil
}
func (s resolutionStore) ListTree(context.Context, string) ([]api.ArtifactListItem, error) {
	return nil, nil
}
func (s resolutionStore) FindByEvidenceHandle(context.Context, string, string) (string, error) {
	return "", nil
}
func (s resolutionStore) Delete(context.Context, string, string, string) (visual.ArtifactDeleteResult, bool, error) {
	return visual.ArtifactDeleteResult{}, false, nil
}
func (s resolutionStore) DeleteGroup(context.Context, string, []string, string) (int64, error) {
	return 0, nil
}
func (s resolutionStore) CollectGarbage(context.Context) error          { return nil }
func (s resolutionStore) Discard(context.Context, string, string) error { return nil }
func (s resolutionStore) StorageUsage(context.Context, string) (storageusage.Usage, error) {
	return storageusage.Usage{}, s.storageErr
}

func TestSessionArtifactAbsenceCodes(t *testing.T) {
	projects, projectID := artifactProject(t)
	sessStore := store.NewMemory()
	sess, err := sessStore.Create(t.Context(), api.CreateSessionRequest{
		ProjectID: projectID,
		Posture:   api.SessionPostureBuild,
	}, projectID)
	testutil.FailErr(t, "create session", err)

	for _, reason := range visual.AbsenceReasons() {
		t.Run(string(reason), func(t *testing.T) {
			srv := hostapi.NewServer(apitest.Dependencies(t, hostapi.Dependencies{Projects: projects, Store: sessStore, VisualStore: resolutionStore{res: visual.Absent(reason)}}), nil, hostapi.TestAPIToken)
			r := chi.NewRouter()
			r.Get("/v1/sessions/{id}/artifacts/{artifact_id}", srv.HandleSessionArtifactForTest())

			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
				"/v1/sessions/"+sess.ID+"/artifacts/"+uuid.NewString(), nil))
			want := api.ApiErrorCode("artifact_" + string(reason))
			if reason == visual.AbsenceUnknown {
				want = api.ApiErrorCodeArtifactNotFound
			}
			if rec.Code != want.HTTPStatus() {
				t.Fatalf("status = %d want %d", rec.Code, want.HTTPStatus())
			}
			var body api.ErrorResponse
			testutil.FailErr(t, "decode error", json.Unmarshal(rec.Body.Bytes(), &body))
			if body.Code != want {
				t.Fatalf("code = %q want %q", body.Code, want)
			}
			if body.Message != reason.Note() {
				t.Fatalf("message = %q want the reason's own sentence %q", body.Message, reason.Note())
			}
		})
	}
}

func artifactProject(t *testing.T) (*project.MemoryRegistry, string) {
	t.Helper()
	registry := project.NewMemoryRegistry()
	p, err := registry.Create(t.Context(), project.CreateParams{Draft: true, Name: "Artifact fixture"})
	testutil.FailErr(t, "register artifact project", err)
	return registry, p.ID
}
