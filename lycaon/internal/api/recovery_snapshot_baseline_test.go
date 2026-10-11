package api

import (
	"context"
	"errors"
	"github.com/go-chi/chi/v5"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testbackup"
	"github.com/lycaon/lycaon/internal/testutil"
)

func writeRefusedStoreAndSnapshot(t *testing.T) (configDir, dbPath, snapshotPath string) {
	t.Helper()
	configDir = t.TempDir()
	dbPath = filepath.Join(configDir, "store.db")

	ctx := context.Background()
	store, err := db.Open(dbPath)
	testutil.FailErr(t, "open source store", err)
	snapshotPath = testbackup.Recovery(t, store, dbPath)
	testutil.FailErr(t, "close source store", store.Shutdown(ctx))

	testutil.FailErr(t, "write refused store", os.WriteFile(dbPath, []byte("refused"), 0o600))
	return configDir, dbPath, snapshotPath
}

func TestRecoveryStateHidesACorruptSnapshot(t *testing.T) {
	ctx := context.Background()
	_, dbPath, snapshotPath := writeRefusedStoreAndSnapshot(t)

	if available, _ := probeRecoverySnapshot(ctx, dbPath); !available {
		t.Fatal("a baseline snapshot must be offered")
	}

	testutil.FailErr(t, "corrupt recovery manifest", os.WriteFile(filepath.Join(snapshotPath, "manifest.json"), []byte("corrupt"), 0o600))
	if available, at := probeRecoverySnapshot(ctx, dbPath); available {
		t.Fatalf("a corrupt recovery archive was still advertised (at=%q)", at)
	}
}

func TestRestoreRecoverySnapshotRefusesACorruptSnapshot(t *testing.T) {
	configDir, dbPath, snapshotPath := writeRefusedStoreAndSnapshot(t)
	testutil.FailErr(t, "corrupt recovery manifest", os.WriteFile(filepath.Join(snapshotPath, "manifest.json"), []byte("corrupt"), 0o600))

	srv := NewRecoveryServer(context.Background(), RecoveryServerOpts{
		APIToken: TestAPIToken,
		DBPath:   dbPath,
		DataDir:  configDir,
		Incompatible: &db.StoreIncompatibleError{
			Reason: db.RecoveryReasonIntegrityFailed,
			Detail: "integrity check failed",
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/backup/restore/recovery-snapshot", nil)
	req.Header.Set("Authorization", "Bearer "+TestAPIToken)
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, req)

	if response.Code != http.StatusConflict {
		t.Fatalf("restore status=%d body=%s, want a refusal instead of a restore that the next boot rejects",
			response.Code, response.Body.String())
	}
	if _, err := os.Stat(filepath.Join(configDir, "restore.pending.json")); !os.IsNotExist(err) {
		t.Fatal("a corrupt recovery snapshot published a pending marker")
	}
}

// The detail is the diagnostic line under Den's own copy, so replacing it with
// that copy deletes the only statement of what actually happened.
func TestRecoveryStateKeepsTheDiagnosticDetail(t *testing.T) {
	_, dbPath, _ := writeRefusedStoreAndSnapshot(t)
	state := buildRecoveryState(context.Background(), dbPath, &db.StoreIncompatibleError{
		Reason: db.RecoveryReasonIntegrityFailed,
		Detail: "a staged restore could not be applied: install store.db: read-only file system",
	})
	if state.Detail != "a staged restore could not be applied: install store.db: read-only file system" {
		t.Fatalf("recovery detail = %q, want the cause the boot reported", state.Detail)
	}
}

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
