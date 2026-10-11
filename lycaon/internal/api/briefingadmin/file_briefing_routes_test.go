package briefingadmin_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/api/apitest"
	"github.com/lycaon/lycaon/internal/api/briefingadmin"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/filebriefing"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

const briefingsPath = "/v1/projects/{id}/source/file-briefings"

type briefingRoutes struct {
	router    http.Handler
	project   *project.Project
	root      string
	briefings *filebriefing.Service
}

// switchable answers the file summaries setting the service consults.
type switchable struct{ enabled bool }

func (s *switchable) Enabled() bool                 { return s.enabled }
func (s *switchable) PutEnabled(enabled bool) error { s.enabled = enabled; return nil }

func newBriefingRoutes(t *testing.T, setting *switchable) briefingRoutes {
	t.Helper()
	t.Setenv("LYCAON_LLM_MOCK", "1")
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	deps := apitest.Dependencies(t, api.Dependencies{})
	root := t.TempDir()
	testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(root, "main.go"),
		[]byte("package main\n\nfunc Build() error {\n\treturn nil\n}\n"), 0o600))
	testutil.FailErr(t, "write binary source", os.WriteFile(filepath.Join(root, "blob.bin"), []byte{0, 1, 2, 0, 3}, 0o600))
	p, err := project.CreateWithRoot(t.Context(), deps.Core.Projects, root)
	testutil.FailErr(t, "create project", err)

	cfg, err := filebriefing.LoadConfig()
	testutil.FailErr(t, "load file briefing config", err)
	fileDeps := filebriefing.Dependencies{Store: filebriefing.NewMemory(), Config: cfg}
	if setting != nil {
		fileDeps.Settings = setting
	}
	briefings := filebriefing.NewService(context.Background(), fileDeps)
	t.Cleanup(func() { briefings.Stop(); briefings.Wait(context.Background()) })

	h := briefingadmin.New(&httpio.Responder{Logger: slog.Default()}, briefingadmin.Dependencies{
		EditorDocuments: deps.Source.EditorDocuments, FileBriefings: briefings,
		ProjectRegistry: deps.Core.Projects, SessionStore: deps.Core.Store, SourceLedger: deps.Source.SourceLedger,
		WorkerBranchRoot: func(context.Context, string, string) (string, func()) { return "", func() {} },
	})
	r := chi.NewRouter()
	r.Get(briefingsPath, h.HandleGetFileBriefing)
	r.Post(briefingsPath, h.HandleRequestFileBriefing)
	return briefingRoutes{router: r, project: p, root: root, briefings: briefings}
}

func (b briefingRoutes) url(projectID string) string {
	return "/v1/projects/" + projectID + "/source/file-briefings"
}

func (b briefingRoutes) post(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, b.url(b.project.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	b.router.ServeHTTP(w, req)
	return w
}

func (b briefingRoutes) get(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	b.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, b.url(b.project.ID)+"?"+query, nil))
	return w
}

func (b briefingRoutes) request(fields string) string {
	return `{"root_id":"` + b.project.Roots[0].ID + `",` + fields + `}`
}

func errorCode(t *testing.T, w *httptest.ResponseRecorder) wire.ApiErrorCode {
	t.Helper()
	var body wire.ErrorResponse
	testutil.FailErr(t, "decode error body", json.Unmarshal(w.Body.Bytes(), &body))
	return body.Code
}

func TestFileBriefingRequestThenRead(t *testing.T) {
	b := newBriefingRoutes(t, nil)
	query := "root_id=" + b.project.Roots[0].ID + "&path=main.go&presentation=current"
	if w := b.get(t, query); errorCode(t, w) != wire.ApiErrorCodeFileBriefingNotFound {
		t.Fatalf("unrequested briefing = %d %s", w.Code, w.Body.String())
	}

	w := b.post(t, b.request(`"path":" main.go ","presentation":"current","trigger":"manual"`))
	if w.Code != http.StatusAccepted {
		t.Fatalf("request status = %d body = %s", w.Code, w.Body.String())
	}
	var requested wire.FileBriefingResponse
	testutil.FailErr(t, "decode requested briefing", json.Unmarshal(w.Body.Bytes(), &requested))
	if requested.Status != string(filebriefing.StatusPending) || requested.Preview.LineCount == 0 {
		t.Fatalf("requested briefing = %+v", requested)
	}

	read := b.get(t, query)
	if read.Code != http.StatusOK {
		t.Fatalf("read status = %d body = %s", read.Code, read.Body.String())
	}
	var got wire.FileBriefingResponse
	testutil.FailErr(t, "decode read briefing", json.Unmarshal(read.Body.Bytes(), &got))
	if got.TargetKey != requested.TargetKey {
		t.Fatalf("read target %q, want the requested %q", got.TargetKey, requested.TargetKey)
	}
}

func TestFileBriefingRequestRefusals(t *testing.T) {
	b := newBriefingRoutes(t, nil)
	cases := []struct {
		name string
		body string
		want wire.ApiErrorCode
	}{
		{"malformed body", `{"root_id":`, wire.ApiErrorCodeInvalidJson},
		{"missing path", b.request(`"path":" ","presentation":"current","trigger":"manual"`), wire.ApiErrorCodeInvalidRequest},
		{"unknown trigger", b.request(`"path":"main.go","presentation":"current","trigger":"later"`), wire.ApiErrorCodeInvalidRequest},
		{"unknown presentation", b.request(`"path":"main.go","presentation":"draft","trigger":"manual"`), wire.ApiErrorCodeInvalidRequest},
		{"current with a version", b.request(`"path":"main.go","presentation":"current","trigger":"manual","version_id":"v"`), wire.ApiErrorCodeInvalidRequest},
		{"document with a worker", b.request(`"path":"main.go","presentation":"document","trigger":"manual","worker_id":"w"`), wire.ApiErrorCodeInvalidRequest},
		{"document without a revision", b.request(`"path":"main.go","presentation":"document","trigger":"manual","document_id":"d"`), wire.ApiErrorCodeInvalidRequest},
		{"version with a document", b.request(`"path":"main.go","presentation":"version","trigger":"manual","document_id":"d"`), wire.ApiErrorCodeInvalidRequest},
		{"version without an id", b.request(`"path":"main.go","presentation":"version","trigger":"manual"`), wire.ApiErrorCodeInvalidRequest},
		{"unknown version", b.request(`"path":"main.go","presentation":"version","trigger":"manual","version_id":"missing"`), wire.ApiErrorCodeSourceVersionNotFound},
		{"unknown document", b.request(`"path":"main.go","presentation":"document","trigger":"manual","document_id":"missing","document_revision":1`), wire.ApiErrorCodeEditorDocumentNotFound},
		{"missing file", b.request(`"path":"absent.go","presentation":"current","trigger":"manual"`), wire.ApiErrorCodeSourceNotFound},
		{"binary file", b.request(`"path":"blob.bin","presentation":"current","trigger":"manual"`), wire.ApiErrorCodeSourceBinary},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := b.post(t, tc.body)
			if got := errorCode(t, w); got != tc.want {
				t.Fatalf("code = %q, want %q (body %s)", got, tc.want, w.Body.String())
			}
		})
	}
}

func TestFileBriefingReadRefusals(t *testing.T) {
	b := newBriefingRoutes(t, nil)
	rootID := b.project.Roots[0].ID
	cases := []struct {
		name, query string
		want        wire.ApiErrorCode
	}{
		{"missing root", "path=main.go&presentation=current", wire.ApiErrorCodeInvalidQuery},
		{"missing path", "root_id=" + rootID + "&presentation=current", wire.ApiErrorCodeInvalidQuery},
		{"bad document revision", "root_id=" + rootID + "&path=main.go&presentation=document&document_revision=x", wire.ApiErrorCodeInvalidQuery},
		{"document without an id", "root_id=" + rootID + "&path=main.go&presentation=document&document_revision=1", wire.ApiErrorCodeInvalidRequest},
		{"missing file", "root_id=" + rootID + "&path=absent.go&presentation=current", wire.ApiErrorCodeSourceNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := b.get(t, tc.query)
			if got := errorCode(t, w); got != tc.want {
				t.Fatalf("code = %q, want %q (status %d body %s)", got, tc.want, w.Code, w.Body.String())
			}
		})
	}
}

func TestFileBriefingRoutesNameTheProject(t *testing.T) {
	b := newBriefingRoutes(t, nil)
	w := httptest.NewRecorder()
	b.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, b.url("missing-project")+"?root_id=r&path=main.go", nil))
	if got := errorCode(t, w); got != wire.ApiErrorCodeProjectNotFound {
		t.Fatalf("read code = %q, want project_not_found", got)
	}
	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, b.url("missing-project"), strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	b.router.ServeHTTP(w, req)
	if got := errorCode(t, w); got != wire.ApiErrorCodeProjectNotFound {
		t.Fatalf("request code = %q, want project_not_found", got)
	}
}

func TestFileBriefingsOffOrStopping(t *testing.T) {
	setting := &switchable{enabled: false}
	b := newBriefingRoutes(t, setting)
	body := b.request(`"path":"main.go","presentation":"current","trigger":"manual"`)
	query := "root_id=" + b.project.Roots[0].ID + "&path=main.go&presentation=current"
	if got := errorCode(t, b.post(t, body)); got != wire.ApiErrorCodeFileBriefingDisabled {
		t.Fatalf("request while off = %q", got)
	}
	if got := errorCode(t, b.get(t, query)); got != wire.ApiErrorCodeFileBriefingDisabled {
		t.Fatalf("read while off = %q", got)
	}

	setting.enabled = true
	b.briefings.Stop()
	if got := errorCode(t, b.post(t, body)); got != wire.ApiErrorCodeFileBriefingUnavailable {
		t.Fatalf("request while stopping = %q", got)
	}
	if got := errorCode(t, b.get(t, query)); got != wire.ApiErrorCodeFileBriefingUnavailable {
		t.Fatalf("read while stopping = %q", got)
	}
}
