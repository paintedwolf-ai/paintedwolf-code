package security

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

type apiErrorMatrixFixture struct {
	sessionID            string
	projectID            string
	rootlessProjectID    string
	overlaysOffProjectID string
	secretHome           string
	unknownID            string
}

type apiErrorMatrixRow struct {
	name   string
	method string
	path   string
	body   string
	// contentType names the body's media type; empty sends JSON.
	contentType string
	auth        string
	wantStatus  int
	wantCode    string
}

func TestAPIErrorContractsMatrix(t *testing.T) {
	h := wiring.BuildForTest(t)
	httpSrv := httptest.NewServer(h.Server)
	t.Cleanup(httpSrv.Close)
	base := httpSrv.URL

	projectDir := t.TempDir()
	project := createAPIProjectAtPath(t, base, projectDir)
	rootlessProject := openAPIPostJSON[wire.Project](t, base, "/v1/projects", nil, `{"name":"rootless"}`, http.StatusCreated)
	sess := openAPIPostJSON[wire.Session](t, base, "/v1/sessions", nil,
		`{"project_id":"`+project.ID+`","posture":"spec"}`, http.StatusAccepted)

	overlaysOffProject := createAPIProjectAtPath(t, base, t.TempDir())
	openAPIPatchJSON[wire.ProjectTrust](t, base,
		"/v1/projects/{id}/trust", map[string]string{"id": overlaysOffProject.ID},
		`{"enabled":{"scan_config":false}}`, http.StatusOK)

	secretHome := t.TempDir()
	t.Setenv("HOME", secretHome)
	testutil.FailErr(t, "mkdir denied", os.MkdirAll(filepath.Join(secretHome, ".gnupg", "idem"), 0o755))

	fixture := apiErrorMatrixFixture{
		sessionID:            sess.ID,
		projectID:            project.ID,
		rootlessProjectID:    rootlessProject.ID,
		overlaysOffProjectID: overlaysOffProject.ID,
		secretHome:           secretHome,
		unknownID:            uuid.New().String(),
	}
	runAPIErrorRows(t, base, apiErrorAuthRows(fixture))
	runAPIErrorRows(t, base, apiErrorUnknownResourceRows(fixture))
	runAPIErrorRows(t, base, apiErrorSourceReadRows(fixture))
	runAPIErrorRows(t, base, apiErrorSourceMutationRows(fixture))
	runAPIErrorRows(t, base, apiErrorProjectStateRows(fixture))
	// Empty prompts are valid when an active workflow declares a request.
	requestless, err := h.Store.Create(t.Context(), wire.CreateSessionRequest{ProjectID: project.ID}, project.ID)
	testutil.FailErr(t, "create session without a workflow request", err)
	manifest, err := h.Workflows.Manager.Resolver.Overlay.Get("implement", "1.0.0")
	testutil.FailErr(t, "load empty-prompt fixture workflow", err)
	manifest.Request = nil
	h.RegisterManifest(manifest)
	fixture.sessionID = requestless.ID
	runAPIErrorRows(t, base, apiErrorValidationRows(fixture))
}

func apiErrorAuthRows(f apiErrorMatrixFixture) []apiErrorMatrixRow {
	return []apiErrorMatrixRow{
		{
			name:       "missing auth",
			method:     http.MethodGet,
			path:       "/v1/sessions/" + f.sessionID,
			auth:       "none",
			wantStatus: http.StatusUnauthorized,
			wantCode:   "unauthorized",
		},
		{
			name:       "wrong bearer token",
			method:     http.MethodGet,
			path:       "/v1/sessions/" + f.sessionID,
			auth:       "wrong-token",
			wantStatus: http.StatusUnauthorized,
			wantCode:   "unauthorized",
		},

		{
			name:       "empty scan id segment",
			method:     http.MethodPost,
			path:       "/v1/projects/" + f.projectID + "/scans//query",
			body:       "{}",
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			name:       "empty workflow run id segment",
			method:     http.MethodPost,
			path:       "/v1/workflow-runs//cancel",
			body:       "{}",
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			name:       "empty session id segment",
			method:     http.MethodGet,
			path:       "/v1/sessions//messages",
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
	}
}

func apiErrorUnknownResourceRows(f apiErrorMatrixFixture) []apiErrorMatrixRow {
	unknownUUID := f.unknownID
	project := wire.Project{ID: f.projectID}
	sess := wire.Session{ID: f.sessionID}
	secretHome := f.secretHome
	return []apiErrorMatrixRow{
		{
			name:       "unknown background process handle",
			method:     http.MethodPost,
			path:       "/v1/sessions/" + sess.ID + "/background/no-such-handle/stop",
			wantStatus: http.StatusNotFound,
			wantCode:   "background_process_not_found",
		},
		{
			name:       "unknown session GET",
			method:     http.MethodGet,
			path:       "/v1/sessions/" + unknownUUID,
			wantStatus: http.StatusNotFound,
			wantCode:   "session_not_found",
		},
		{
			name:       "project create under a denied secret store",
			method:     http.MethodPost,
			path:       "/v1/projects",
			body:       `{"roots":[{"path":"` + filepath.Join(secretHome, ".gnupg", "idem") + `"}]}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "write_root_under_secret_store",
		},
		{
			name:        "backup restore with malformed archive",
			method:      http.MethodPost,
			path:        "/v1/backup/restore",
			body:        "not-a-zip-archive",
			contentType: "application/zip",
			wantStatus:  http.StatusBadRequest,
			wantCode:    "backup_invalid",
		},
		{
			name:       "unknown session transcript export",
			method:     http.MethodGet,
			path:       "/v1/sessions/" + unknownUUID + "/export?format=md",
			wantStatus: http.StatusNotFound,
			wantCode:   "session_not_found",
		},
		{
			name:       "session transcript export with invalid format",
			method:     http.MethodGet,
			path:       "/v1/sessions/" + unknownUUID + "/export?format=docx",
			wantStatus: http.StatusBadRequest,
			wantCode:   "export_format_invalid",
		},
		{
			name:       "unknown session messages",
			method:     http.MethodGet,
			path:       "/v1/sessions/" + unknownUUID + "/messages",
			wantStatus: http.StatusNotFound,
			wantCode:   "session_not_found",
		},
		{
			name:       "session messages invalid limit",
			method:     http.MethodGet,
			path:       "/v1/sessions/" + sess.ID + "/messages?limit=zero",
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_query",
		},
		{
			name:       "session messages conflicting cursors",
			method:     http.MethodGet,
			path:       "/v1/sessions/" + sess.ID + "/messages?before=1&after=2",
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_query",
		},
		{
			name:       "session messages invalid before cursor",
			method:     http.MethodGet,
			path:       "/v1/sessions/" + sess.ID + "/messages?before=nope",
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_page_cursor",
		},
		{
			name:       "session messages invalid after cursor",
			method:     http.MethodGet,
			path:       "/v1/sessions/" + sess.ID + "/messages?after=nope",
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_page_cursor",
		},
		{
			name:       "unknown session prompt",
			method:     http.MethodPost,
			path:       "/v1/sessions/" + unknownUUID + "/prompts",
			body:       `{"text":"hi"}`,
			wantStatus: http.StatusNotFound,
			wantCode:   "session_not_found",
		},
		{
			name:       "unknown workflow run GET",
			method:     http.MethodGet,
			path:       "/v1/workflow-runs/" + unknownUUID,
			wantStatus: http.StatusNotFound,
			wantCode:   "workflow_run_not_found",
		},
		{
			name:       "unknown workflow run report",
			method:     http.MethodGet,
			path:       "/v1/workflow-runs/" + unknownUUID + "/report",
			wantStatus: http.StatusNotFound,
			wantCode:   "workflow_run_not_found",
		},
		{
			// Blueprint lookup requires project scope.
			name:       "unknown plan GET",
			method:     http.MethodGet,
			path:       "/v1/projects/" + project.ID + "/blueprints/" + unknownUUID,
			wantStatus: http.StatusNotFound,
			wantCode:   "blueprint_not_found",
		},
		{
			name:       "unknown delegation GET",
			method:     http.MethodGet,
			path:       "/v1/delegations/" + unknownUUID,
			wantStatus: http.StatusNotFound,
			wantCode:   "delegation_not_found",
		},
		{
			name:       "unknown scan GET",
			method:     http.MethodGet,
			path:       "/v1/projects/" + project.ID + "/scans/" + unknownUUID,
			wantStatus: http.StatusNotFound,
			wantCode:   "scan_not_found",
		},
		{
			name:       "unknown scan query",
			method:     http.MethodPost,
			path:       "/v1/projects/" + project.ID + "/scans/" + unknownUUID + "/query",
			body:       `{}`,
			wantStatus: http.StatusNotFound,
			wantCode:   "scan_not_found",
		},
		{
			name:       "unknown scan SARIF export",
			method:     http.MethodGet,
			path:       "/v1/projects/" + project.ID + "/scans/" + unknownUUID + "/sarif",
			wantStatus: http.StatusNotFound,
			wantCode:   "scan_not_found",
		},
	}
}

func apiErrorSourceReadRows(f apiErrorMatrixFixture) []apiErrorMatrixRow {
	unknownUUID := f.unknownID
	project := wire.Project{ID: f.projectID}
	overlaysOffProject := wire.Project{ID: f.overlaysOffProjectID}
	return []apiErrorMatrixRow{
		{
			name:       "unknown project source",
			method:     http.MethodGet,
			path:       "/v1/projects/" + unknownUUID + "/source?path=src/a.ts",
			wantStatus: http.StatusNotFound,
			wantCode:   "project_not_found",
		},
		{
			name:       "source path outside sandbox",
			method:     http.MethodGet,
			path:       "/v1/projects/" + project.ID + "/source?path=/etc/passwd",
			wantStatus: http.StatusForbidden,
			wantCode:   "source_path_denied",
		},
		{
			name:       "source missing file under jail",
			method:     http.MethodGet,
			path:       "/v1/projects/" + project.ID + "/source?path=nonexistent.ts",
			wantStatus: http.StatusNotFound,
			wantCode:   "source_not_found",
		},
		{
			name:       "project scanner write with scan trust off",
			method:     http.MethodPatch,
			path:       "/v1/projects/" + overlaysOffProject.ID + "/scanners/opengrep",
			body:       `{"enabled":false}`,
			wantStatus: http.StatusConflict,
			wantCode:   "trust_surface_off",
		},
		{
			name:       "unknown project source browse",
			method:     http.MethodGet,
			path:       "/v1/projects/" + unknownUUID + "/source/browse",
			wantStatus: http.StatusNotFound,
			wantCode:   "project_not_found",
		},
		{
			name:       "unknown project source raw",
			method:     http.MethodGet,
			path:       "/v1/projects/" + unknownUUID + "/source/raw?path=a.png",
			wantStatus: http.StatusNotFound,
			wantCode:   "project_not_found",
		},
		{
			name:       "unknown project source walk",
			method:     http.MethodGet,
			path:       "/v1/projects/" + unknownUUID + "/source/walk",
			wantStatus: http.StatusNotFound,
			wantCode:   "project_not_found",
		},
		{
			name:   "unknown project source definition",
			method: http.MethodPost,
			path:   "/v1/projects/" + unknownUUID + "/source/definition",
			body: `{
				"root_id":"r1","path":"a.go","symbol":"Resolve","line":1
			}`,
			wantStatus: http.StatusNotFound,
			wantCode:   "project_not_found",
		},
		{
			name:       "unknown worker changes",
			method:     http.MethodGet,
			path:       "/v1/workers/" + unknownUUID + "/changes",
			wantStatus: http.StatusNotFound,
			wantCode:   "worker_not_found",
		},
		{
			name:       "source browse dir outside root",
			method:     http.MethodGet,
			path:       "/v1/projects/" + project.ID + "/source/browse?dir=%2Fetc",
			wantStatus: http.StatusForbidden,
			wantCode:   "source_path_denied",
		},
		{
			name:       "source browse missing dir under jail",
			method:     http.MethodGet,
			path:       "/v1/projects/" + project.ID + "/source/browse?dir=nonexistent-folder",
			wantStatus: http.StatusNotFound,
			wantCode:   "source_not_found",
		},
	}
}

func apiErrorSourceMutationRows(f apiErrorMatrixFixture) []apiErrorMatrixRow {
	unknownUUID := f.unknownID
	project := wire.Project{ID: f.projectID}
	return []apiErrorMatrixRow{
		{
			name:       "unknown project source create",
			method:     http.MethodPost,
			path:       "/v1/projects/" + unknownUUID + "/source",
			body:       `{"path":"newdir","kind":"folder"}`,
			wantStatus: http.StatusNotFound,
			wantCode:   "project_not_found",
		},
		{
			name:       "source create file outside root",
			method:     http.MethodPost,
			path:       "/v1/projects/" + project.ID + "/source",
			body:       `{"path":"../escape.ts","kind":"file"}`,
			wantStatus: http.StatusForbidden,
			wantCode:   "source_path_denied",
		},
		{
			name:       "source create folder outside root",
			method:     http.MethodPost,
			path:       "/v1/projects/" + project.ID + "/source",
			body:       `{"path":"../escape","kind":"folder"}`,
			wantStatus: http.StatusForbidden,
			wantCode:   "source_path_denied",
		},
		{
			name:       "source create rejects unknown kind",
			method:     http.MethodPost,
			path:       "/v1/projects/" + project.ID + "/source",
			body:       `{"path":"new.ts","kind":"symlink"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			name:       "unknown project source rename",
			method:     http.MethodPost,
			path:       "/v1/projects/" + unknownUUID + "/source/rename",
			body:       `{"from":"a.ts","to":"b.ts"}`,
			wantStatus: http.StatusNotFound,
			wantCode:   "project_not_found",
		},
		{
			name:       "source rename outside root",
			method:     http.MethodPost,
			path:       "/v1/projects/" + project.ID + "/source/rename",
			body:       `{"from":"../escape.ts","to":"b.ts"}`,
			wantStatus: http.StatusForbidden,
			wantCode:   "source_path_denied",
		},
		{
			name:       "unknown project source copy",
			method:     http.MethodPost,
			path:       "/v1/projects/" + unknownUUID + "/source/copy",
			body:       `{"from":"a.ts","to":"b.ts"}`,
			wantStatus: http.StatusNotFound,
			wantCode:   "project_not_found",
		},
		{
			name:       "source copy outside root",
			method:     http.MethodPost,
			path:       "/v1/projects/" + project.ID + "/source/copy",
			body:       `{"from":"../escape.ts","to":"b.ts"}`,
			wantStatus: http.StatusForbidden,
			wantCode:   "source_path_denied",
		},
		{
			name:       "unknown project source delete",
			method:     http.MethodDelete,
			path:       "/v1/projects/" + unknownUUID + "/source?path=a.ts&operation_id=" + uuid.NewString(),
			wantStatus: http.StatusNotFound,
			wantCode:   "project_not_found",
		},
		{
			name:       "source delete outside root",
			method:     http.MethodDelete,
			path:       "/v1/projects/" + project.ID + "/source?path=../escape.ts&operation_id=" + uuid.NewString(),
			wantStatus: http.StatusForbidden,
			wantCode:   "source_path_denied",
		},
		{
			name:       "unknown project source index",
			method:     http.MethodGet,
			path:       "/v1/projects/" + unknownUUID + "/source/index",
			wantStatus: http.StatusNotFound,
			wantCode:   "project_not_found",
		},
		{
			name:       "unknown project source symbols",
			method:     http.MethodGet,
			path:       "/v1/projects/" + unknownUUID + "/source/symbols?path=x.go",
			wantStatus: http.StatusNotFound,
			wantCode:   "project_not_found",
		},
	}
}

func apiErrorProjectStateRows(f apiErrorMatrixFixture) []apiErrorMatrixRow {
	unknownUUID := f.unknownID
	project := wire.Project{ID: f.projectID}
	rootlessProject := wire.Project{ID: f.rootlessProjectID}
	return []apiErrorMatrixRow{
		{
			name:       "source read without roots",
			method:     http.MethodGet,
			path:       "/v1/projects/" + rootlessProject.ID + "/source?path=AGENTS.md",
			wantStatus: http.StatusConflict,
			wantCode:   "no_project_root",
		},
		{
			name:       "unknown project root detach",
			method:     http.MethodDelete,
			path:       "/v1/projects/" + project.ID + "/roots/" + unknownUUID,
			wantStatus: http.StatusNotFound,
			wantCode:   "root_not_found",
		},
	}
}

func apiErrorValidationRows(f apiErrorMatrixFixture) []apiErrorMatrixRow {
	project := wire.Project{ID: f.projectID}
	sess := wire.Session{ID: f.sessionID}
	return []apiErrorMatrixRow{
		{
			name:       "invalid session posture",
			method:     http.MethodPost,
			path:       "/v1/sessions",
			body:       `{"project_id":"` + project.ID + `","posture":"bogus"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			name:       "session with unknown field",
			method:     http.MethodPost,
			path:       "/v1/sessions",
			body:       `{"project_id":"` + project.ID + `","unexpected":"value","posture":"build"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_json",
		},
		{
			name:       "session missing project",
			method:     http.MethodPost,
			path:       "/v1/sessions",
			body:       `{"posture":"build"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			name:       "prompt missing text",
			method:     http.MethodPost,
			path:       "/v1/sessions/" + sess.ID + "/prompts",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			name:       "delegation invalid strategy",
			method:     http.MethodPost,
			path:       "/v1/delegations",
			body:       `{"project_id":"` + project.ID + `","task":"do it","strategy":"single"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			name:       "model-policy empty patch",
			method:     http.MethodPatch,
			path:       "/v1/settings/model-policy",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},

		{
			name:       "board missing session_id",
			method:     http.MethodGet,
			path:       "/v1/projects/" + f.projectID + "/board",
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_query",
		},
		{
			name:       "workers missing project_id",
			method:     http.MethodGet,
			path:       "/v1/workers",
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_query",
		},
		{
			name:       "cost summary missing scope",
			method:     http.MethodGet,
			path:       "/v1/cost/summary",
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_query",
		},

		{
			name:       "create project at nonexistent path",
			method:     http.MethodPost,
			path:       "/v1/projects",
			body:       `{"roots":[{"path":"/this/does/not/exist/anywhere/12345"}]}`,
			wantStatus: http.StatusNotFound,
			wantCode:   "path_not_found",
		},

		{
			name:       "malformed JSON body decodes to invalid_json",
			method:     http.MethodPost,
			path:       "/v1/sessions",
			body:       `{not-json`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_json",
		},

		{
			name:       "start workflow with unknown workflow_id",
			method:     http.MethodPost,
			path:       "/v1/sessions/" + sess.ID + "/workflow-runs",
			body:       `{"workflow_id":"does-not-exist","workflow_version":"1.0.0"}`,
			wantStatus: http.StatusNotFound,
			wantCode:   "workflow_not_found",
		},
	}
}

func runAPIErrorRows(t *testing.T, base string, rows []apiErrorMatrixRow) {
	t.Helper()
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			var bodyReader io.Reader
			if row.body != "" {
				bodyReader = strings.NewReader(string(mutationRequestBody(t, row.method, row.path, row.body)))
			}
			req, err := http.NewRequestWithContext(t.Context(), row.method, base+row.path, bodyReader)
			testutil.FailErr(t, "http.NewRequest failed", err)
			if row.body != "" {
				contentType := row.contentType
				if contentType == "" {
					contentType = "application/json"
				}
				req.Header.Set("Content-Type", contentType)
			}
			switch row.auth {
			case "none":
				// Omit authorization.
			case "":
				req.Header.Set("Authorization", api.TestAuthHeader())
			default:
				req.Header.Set("Authorization", "Bearer "+row.auth)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)

			assertNoLeaks(t, resp.StatusCode, body)

			if row.wantStatus > 0 {
				if resp.StatusCode != row.wantStatus {
					t.Fatalf("%s %s status = %d, want %d; body = %s",
						row.method, row.path, resp.StatusCode, row.wantStatus, body)
				}
			} else if resp.StatusCode == http.StatusInternalServerError {
				t.Fatalf("%s %s 500'd: %s", row.method, row.path, body)
			} else if resp.StatusCode < 400 || resp.StatusCode >= 600 {
				t.Fatalf("%s %s status = %d, want 4xx/5xx", row.method, row.path, resp.StatusCode)
			}

			if row.wantCode == "" {
				return
			}
			var errResp wire.ErrorResponse
			if err := json.Unmarshal(body, &errResp); err != nil {
				t.Fatalf("decode error: %v body=%s", err, body)
			}
			if string(errResp.Code) != row.wantCode {
				t.Fatalf("error code = %q, want %q; body = %s", errResp.Code, row.wantCode, body)
			}
			if errResp.Message == "" {
				t.Fatalf("error message empty (code=%q): %s", errResp.Code, body)
			}
		})
	}
}

// assertNoLeaks rejects internal error details.
func assertNoLeaks(t *testing.T, status int, body []byte) {
	t.Helper()
	if status < 400 {
		return
	}
	lower := strings.ToLower(string(body))
	forbidden := []string{
		"sql:",                  // database/sql wrapping
		"no rows in result set", // sql.ErrNoRows text
		"goroutine ",            // panic stack
		"runtime.",              // panic frame prefix
		"*workflow.",            // exported type leakage
		"*api.",
		"*plan.",
	}
	for _, f := range forbidden {
		if strings.Contains(lower, f) {
			t.Fatalf("error body leaked internal marker %q: %s", f, body)
		}
	}
}
