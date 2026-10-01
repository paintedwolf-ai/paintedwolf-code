package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBackupExportReportsMissingRetainedData(t *testing.T) {
	configDir := t.TempDir()
	database := testdbfixture.OpenPath(t, filepath.Join(configDir, "store.db"))
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	_, err := database.ExecContext(t.Context(), `INSERT INTO artifacts
		(id,project_id,content_hash,mime,source,created_at,updated_at)
		VALUES ('missing',?,?,'image/png','user','2026-09-10','2026-09-10')`, testdbseed.DefaultProjectID, strings.Repeat("a", 64))
	testutil.FailErr(t, "seed missing artifact reference", err)
	srv := newTestServer(t, withSessionStore(store.NewSQL(database)), func(d *Dependencies) {
		d.DataDir, d.StorePath = configDir, filepath.Join(configDir, "store.db")
	})
	request := httptest.NewRequest(http.MethodGet, "/v1/backup", nil)
	request.Header.Set("Authorization", "Bearer "+TestAPIToken)
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, request)
	assertErrorResponse(t, response, http.StatusConflict, "backup_incomplete")
	if response.Header().Get("Content-Disposition") != "" {
		t.Fatal("incomplete backup was offered as a download")
	}
	if !strings.Contains(response.Body.String(), "retained data") || !strings.Contains(response.Body.String(), "try again") {
		t.Fatalf("backup error offers no retry guidance: %s", response.Body.String())
	}
}
