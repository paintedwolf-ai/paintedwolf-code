//go:build integration

package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

type restoreAcknowledgementWriter struct {
	*httptest.ResponseRecorder
	beforeHeader func(int)
}

func (w restoreAcknowledgementWriter) WriteHeader(status int) {
	w.beforeHeader(status)
	w.ResponseRecorder.WriteHeader(status)
}

func TestRestorePendingRejectsFurtherRequestsUntilRestart(t *testing.T) {
	srv := newTestServer(t)
	srv.markRestorePending()

	health := httptest.NewRecorder()
	srv.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d, want %d", health.Code, http.StatusOK)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/projects", nil)
	req.Header.Set("Authorization", "Bearer "+TestAPIToken)
	blocked := httptest.NewRecorder()
	srv.ServeHTTP(blocked, req)
	assertErrorResponse(t, blocked, http.StatusConflict, "backup_restore_pending")
}

func TestStagedRecoveryRemainsReadableAcrossOrigins(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		for _, action := range []string{"/backup/restore", "/backup/restore/recovery-snapshot", "/store/reset"} {
			name := "normal" + action
			if recovery {
				name = "recovery" + action
			}
			t.Run(name, func(t *testing.T) {
				configDir, dbPath, _ := writeRefusedStoreAndSnapshot(t)
				var srv *Server
				if recovery {
					srv = NewRecoveryServer(t.Context(), RecoveryServerOpts{APIToken: TestAPIToken, DataDir: configDir, DBPath: dbPath})
				} else {
					testutil.FailErr(t, "remove refused fixture", os.Remove(dbPath))
					database := testdbfixture.OpenPath(t, dbPath)
					srv = newTestServer(t, withSessionStore(store.NewSQL(database)), func(d *Dependencies) {
						d.Storage.DataDir, d.Storage.StorePath = configDir, dbPath
					})
				}
				var archive []byte
				if action == "/backup/restore" {
					archivePath := filepath.Join(t.TempDir(), "restore.zip")
					sourceDir := t.TempDir()
					sourcePath := filepath.Join(sourceDir, "store.db")
					source := testdbfixture.OpenPath(t, sourcePath)
					_, err := backup.Create(t.Context(), backup.CreateOpts{
						ConfigDir: sourceDir, DBPath: sourcePath, SQLDB: source, SchemaUserVersion: db.SchemaVersion,
					}, archivePath)
					testutil.FailErr(t, "archive snapshot", err)
					archive, err = os.ReadFile(archivePath)
					testutil.FailErr(t, "read archive", err)
				}
				request := newAuthedRequest(http.MethodPost, "/v1"+action, bytes.NewReader(archive))
				if len(archive) > 0 {
					request.Header.Set("Content-Type", "application/zip")
				}
				request.Header.Set("Origin", "http://tauri.localhost")
				w := httptest.NewRecorder()
				srv.ServeHTTP(restoreAcknowledgementWriter{w, func(status int) {
					if status == http.StatusOK && !srv.restorePending.Load() {
						t.Error("staging acknowledged before restart-required state was published")
					}
				}}, request)
				if w.Code != http.StatusOK {
					t.Fatalf("stage status=%d body=%s", w.Code, w.Body.String())
				}
				assertPendingCORS(t, srv, "/v1"+action)
			})
		}
	}
}

func assertPendingCORS(t *testing.T, srv *Server, stagingPath string) {
	t.Helper()
	for _, origin := range productionCORSOrigins {
		for _, path := range []string{"/v1/projects", stagingPath} {
			// The retry covers loss of the original successful staging response.
			request := newAuthedRequest(http.MethodPost, path, nil)
			request.Header.Set("Origin", origin)
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, request)
			assertErrorResponse(t, w, http.StatusConflict, "backup_restore_pending")
			if w.Header().Get("Access-Control-Allow-Origin") != origin {
				t.Fatal("pending error is hidden by CORS")
			}
			request.Header.Del("Authorization")
			w = httptest.NewRecorder()
			srv.ServeHTTP(w, request)
			assertErrorResponse(t, w, http.StatusUnauthorized, "unauthorized")
		}
		preflight := httptest.NewRequest(http.MethodOptions, stagingPath, nil)
		preflight.Header.Set("Origin", origin)
		preflight.Header.Set("Access-Control-Request-Method", "POST")
		preflight.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, preflight)
		if w.Code != http.StatusOK || w.Header().Get("Access-Control-Allow-Origin") != origin || !strings.Contains(w.Header().Get("Access-Control-Allow-Methods"), "POST") {
			t.Fatalf("pending preflight status=%d headers=%v", w.Code, w.Header())
		}
		health := httptest.NewRequest(http.MethodGet, "/health", nil)
		health.Header.Set("Origin", origin)
		w = httptest.NewRecorder()
		srv.ServeHTTP(w, health)
		if w.Code != http.StatusOK || w.Header().Get("Access-Control-Allow-Origin") != origin {
			t.Fatal("health is unavailable after staging")
		}
	}
}
