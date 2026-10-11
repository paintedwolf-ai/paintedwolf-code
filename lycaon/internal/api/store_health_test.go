package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestQuarantinedStoreExposesRecoveryAndRefusesRoutes(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "store.db"))
	testutil.FailErr(t, "open store", err)
	t.Cleanup(func() { _ = store.Close() })
	_, err = store.ExecContext(t.Context(), `PRAGMA foreign_keys=OFF`)
	testutil.FailErr(t, "disable fixture foreign keys", err)
	_, err = store.ExecContext(t.Context(), `INSERT INTO project_roots(id,project_id,path,label,added_at) VALUES('invalid','absent','/absent','absent','2026-01-01T00:00:00Z')`)
	testutil.FailErr(t, "inject damaged relation", err)
	if err := db.AuditIntegrity(t.Context(), store); !errors.Is(err, db.ErrStoreIncompatible) {
		t.Fatalf("audit = %v", err)
	}
	server := &Server{database: store, router: chi.NewRouter()}
	server.router.Get("/health", server.handleHealth)
	reached := false
	server.router.Post("/v1/backup/restore", func(http.ResponseWriter, *http.Request) { reached = true })
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/backup/restore", nil))
	if response.Code < 400 || reached {
		t.Fatal("quarantined host admitted a route before shutdown")
	}
	health := server.healthPayload()
	if health.Status != "recovery" || health.RecoveryReason != "integrity_failed" {
		t.Fatalf("health = %+v", health)
	}
	response = httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("health status = %d", response.Code)
	}
}
