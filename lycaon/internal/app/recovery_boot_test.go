package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/app/configuration"
	"github.com/lycaon/lycaon/internal/configlayout"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testbackup"
	"github.com/lycaon/lycaon/internal/testutil"
)

func prepareRecoveryStore(t *testing.T, dbPath string) {
	t.Helper()
	sqlDB, err := db.Open(dbPath)
	testutil.FailErr(t, "seed Open", err)
	testbackup.Recovery(t, sqlDB, dbPath)
	testutil.FailErr(t, "close store", sqlDB.Close())
	testutil.FailErr(t, "truncate store", os.WriteFile(dbPath, nil, 0o600))
	testutil.FailErr(t, "drop store sidecars", db.RemoveStoreSidecars(dbPath))
}

// recoveryTestConfig uses store.db — staged restores replace that relative path.
func recoveryTestConfig(t *testing.T) configuration.Config {
	t.Helper()
	cfg := testBuildConfig(t, configlayout.FindModuleRoot())
	cfg.DBPath = filepath.Join(t.TempDir(), "store.db")
	return cfg
}

func getHealth(t *testing.T, h http.Handler) map[string]any {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("health status = %d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	testutil.FailErr(t, "decode", json.Unmarshal(w.Body.Bytes(), &body))
	return body
}

func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var resp struct {
		Code string `json:"code"`
	}
	testutil.FailErr(t, "decode error", json.Unmarshal(body, &resp))
	return resp.Code
}

func TestBuildRecoveryModeOnIntegrityFailure(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-token")
	t.Setenv("LYCAON_TEST", "1")

	cfg := recoveryTestConfig(t)
	prepareRecoveryStore(t, cfg.DBPath)

	app, err := Build(t.Context(), cfg)
	testutil.FailErr(t, "Build recovery", err)
	t.Cleanup(func() { _ = app.Close() })

	if app.SessionMgr != nil || app.CoordinatorRuntime != nil {
		t.Fatal("recovery ServeApp must not wire session or coordinator")
	}

	body := getHealth(t, app.Server)
	if body["status"] != "recovery" {
		t.Fatalf("status = %v", body["status"])
	}
	if body["recovery_reason"] != "integrity_failed" {
		t.Fatalf("recovery_reason = %v", body["recovery_reason"])
	}
	if int(body["schema_version"].(float64)) != db.SchemaVersion {
		t.Fatalf("schema_version = %v", body["schema_version"])
	}
	if int(body["store_schema_version"].(float64)) != 0 {
		t.Fatalf("store_schema_version = %v", body["store_schema_version"])
	}
	if _, ok := body["path"]; ok {
		t.Fatal("health must not leak a path field")
	}
}

func TestBuildEmptyStoreWithoutRecoveryArtifactsBootsNormally(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-token")
	t.Setenv("LYCAON_TEST", "1")

	cfg := recoveryTestConfig(t)
	testutil.FailErr(t, "write empty store", os.WriteFile(cfg.DBPath, nil, 0o600))

	app, err := Build(t.Context(), cfg)
	testutil.FailErr(t, "Build fresh store", err)
	t.Cleanup(func() { _ = app.Close() })
	if app.SessionMgr == nil || app.CoordinatorRuntime == nil {
		t.Fatal("fresh store must build the normal app")
	}
	body := getHealth(t, app.Server)
	if body["status"] != "ok" {
		t.Fatalf("status = %v want ok", body["status"])
	}
	if int(body["schema_version"].(float64)) != db.SchemaVersion {
		t.Fatalf("schema_version = %v", body["schema_version"])
	}
}

func TestBuildRecoveryModePreservesSchemaMismatch(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-token")
	t.Setenv("LYCAON_TEST", "1")

	cfg := recoveryTestConfig(t)
	sqlDB, err := db.Open(cfg.DBPath)
	testutil.FailErr(t, "seed Open", err)
	_, err = sqlDB.ExecContext(t.Context(), `PRAGMA user_version = 2`)
	testutil.FailErr(t, "change baseline marker", err)
	testutil.FailErr(t, "close seed store", sqlDB.Close())

	app, err := Build(t.Context(), cfg)
	testutil.FailErr(t, "Build schema mismatch recovery", err)
	t.Cleanup(func() { _ = app.Close() })

	body := getHealth(t, app.Server)
	if body["recovery_reason"] != "schema_mismatch" {
		t.Fatalf("recovery_reason = %v", body["recovery_reason"])
	}
	if int(body["store_schema_version"].(float64)) != 2 {
		t.Fatalf("store_schema_version = %v", body["store_schema_version"])
	}
	store, err := db.OpenReadOnly(t.Context(), cfg.DBPath)
	testutil.FailErr(t, "open preserved store", err)
	version, err := db.ReadUserVersion(t.Context(), store)
	testutil.FailErr(t, "read preserved baseline marker", err)
	testutil.FailErr(t, "close preserved store", store.Close())
	if version != 2 {
		t.Fatalf("user_version = %d want preserved mismatch 2", version)
	}
}

func TestBuildFailsWhenStoreParentIsAFile(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-token")
	t.Setenv("LYCAON_TEST", "1")

	cfg := recoveryTestConfig(t)
	parent := filepath.Join(t.TempDir(), "not-a-dir")
	testutil.FailErr(t, "write parent file", os.WriteFile(parent, []byte("x"), 0o600))
	cfg.DBPath = filepath.Join(parent, "store.db")
	_, err := Build(t.Context(), cfg)
	if err == nil {
		t.Fatal("expected Build failure")
	}
	if errors.Is(err, db.ErrStoreIncompatible) {
		t.Fatalf("must not be incompatible: %v", err)
	}
}

func TestBuildRecoveryModePreservesUnreadableStore(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-token")
	t.Setenv("LYCAON_TEST", "1")

	cfg := recoveryTestConfig(t)
	want := []byte("not a database")
	testutil.FailErr(t, "write unreadable store", os.WriteFile(cfg.DBPath, want, 0o600))
	app, err := Build(t.Context(), cfg)
	testutil.FailErr(t, "Build recovery", err)
	t.Cleanup(func() { _ = app.Close() })
	body := getHealth(t, app.Server)
	if body["status"] != "recovery" || body["recovery_reason"] != "integrity_failed" {
		t.Fatalf("health = %#v", body)
	}
	got, err := os.ReadFile(cfg.DBPath)
	testutil.FailErr(t, "read preserved store", err)
	if !bytes.Equal(got, want) {
		t.Fatalf("store changed to %q", got)
	}
}

func TestRecoveryRouteClosure(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-token")
	t.Setenv("LYCAON_TEST", "1")

	cfg := recoveryTestConfig(t)
	prepareRecoveryStore(t, cfg.DBPath)

	app, err := Build(t.Context(), cfg)
	testutil.FailErr(t, "Build recovery", err)
	t.Cleanup(func() { _ = app.Close() })

	cases := []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/sessions"},
		{http.MethodPost, "/v1/sessions"},
		{http.MethodGet, "/v1/projects"},
		{http.MethodGet, "/v1/preflight"},
		{http.MethodGet, "/v1/backup"},
		{http.MethodDelete, "/v1/backup/restore"},
		{http.MethodGet, "/nope"},
	}
	for _, tc := range cases {
		req := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil)
		req.Header.Set("Authorization", "Bearer test-token")
		w := httptest.NewRecorder()
		app.Server.ServeHTTP(w, req)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s status = %d want 503 body=%s", tc.method, tc.path, w.Code, w.Body.String())
		}
		if code := errorCode(t, w.Body.Bytes()); code != "store_incompatible" {
			t.Fatalf("%s %s code = %q", tc.method, tc.path, code)
		}
	}
}

func TestRecoverySnapshotRestoreAuth(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-token")
	t.Setenv("LYCAON_TEST", "1")

	cfg := recoveryTestConfig(t)
	prepareRecoveryStore(t, cfg.DBPath)

	app, err := Build(t.Context(), cfg)
	testutil.FailErr(t, "Build recovery", err)
	t.Cleanup(func() { _ = app.Close() })

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/backup/restore/recovery-snapshot", nil)
	w := httptest.NewRecorder()
	app.Server.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d want 401 body=%s", w.Code, w.Body.String())
	}
}

func TestRecoverySnapshotFields(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-token")
	t.Setenv("LYCAON_TEST", "1")

	cfg := recoveryTestConfig(t)
	prepareRecoveryStore(t, cfg.DBPath)

	app, err := Build(t.Context(), cfg)
	testutil.FailErr(t, "Build", err)
	t.Cleanup(func() { _ = app.Close() })

	body := getHealth(t, app.Server)
	if body["recovery_snapshot_available"] != true {
		t.Fatalf("available = %v", body["recovery_snapshot_available"])
	}
	at, _ := body["recovery_snapshot_at"].(string)
	if at == "" {
		t.Fatal("expected recovery_snapshot_at")
	}
}

func TestRecoveryRestoreRoundTrip(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-token")
	t.Setenv("LYCAON_TEST", "1")

	cfg := recoveryTestConfig(t)
	prepareRecoveryStore(t, cfg.DBPath)

	app, err := Build(t.Context(), cfg)
	testutil.FailErr(t, "Build recovery", err)
	beforeStage, err := os.ReadFile(cfg.DBPath)
	testutil.FailErr(t, "read live before stage", err)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/backup/restore/recovery-snapshot", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	app.Server.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("restore status = %d body=%s", w.Code, w.Body.String())
	}
	var result struct {
		RestartRequired  bool   `json:"restart_required"`
		RecoveryCopyPath string `json:"recovery_copy_path"`
	}
	testutil.FailErr(t, "decode restore", json.Unmarshal(w.Body.Bytes(), &result))
	if !result.RestartRequired {
		t.Fatal("expected restart_required")
	}
	if result.RecoveryCopyPath == "" {
		t.Fatal("expected recovery copy path")
	}

	liveAfterStage, err := os.ReadFile(cfg.DBPath)
	testutil.FailErr(t, "read live after stage", err)
	if !bytes.Equal(liveAfterStage, beforeStage) {
		t.Fatal("live store must be untouched after stage")
	}

	_ = app.Close()

	app2, err := Build(t.Context(), cfg)
	testutil.FailErr(t, "Build after restore", err)
	t.Cleanup(func() { _ = app2.Close() })
	if app2.SessionMgr == nil {
		t.Fatal("expected normal ServeApp after restore apply")
	}
	body := getHealth(t, app2.Server)
	if body["status"] != "ok" {
		t.Fatalf("status after restore = %v", body["status"])
	}
	if _, err := os.Stat(result.RecoveryCopyPath); err != nil {
		t.Fatalf("recovery copy missing: %v", err)
	}
}

func TestRecoveryRejectsIncompatibleSnapshot(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-token")
	t.Setenv("LYCAON_TEST", "1")

	cfg := recoveryTestConfig(t)
	prepareRecoveryStore(t, cfg.DBPath)
	archive, _, err := backup.LatestUpgradeRecovery(t.Context(), filepath.Dir(cfg.DBPath))
	testutil.FailErr(t, "find recovery archive", err)
	testutil.FailErr(t, "corrupt recovery manifest", os.WriteFile(filepath.Join(archive, "manifest.json"), []byte("corrupt"), 0o600))

	app, err := Build(t.Context(), cfg)
	testutil.FailErr(t, "Build recovery", err)
	t.Cleanup(func() { _ = app.Close() })

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/backup/restore/recovery-snapshot", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	app.Server.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if code := errorCode(t, w.Body.Bytes()); code != "backup_incompatible" {
		t.Fatalf("code = %q", code)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(cfg.DBPath), "restore.pending.json")); !os.IsNotExist(err) {
		t.Fatalf("pending marker must not exist: %v", err)
	}
}

func TestRecoverySnapshotOnHealthyServerUsesConfiguredStorePath(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-token")
	t.Setenv("LYCAON_TEST", "1")

	cfg := recoveryTestConfig(t)
	cfg.DBPath = filepath.Join(filepath.Dir(cfg.DBPath), "custom.db")
	app, err := Build(t.Context(), cfg)
	testutil.FailErr(t, "Build", err)
	t.Cleanup(func() { _ = app.Close() })

	_, err = app.DB.ExecContext(t.Context(), `INSERT INTO store_meta(key,value) VALUES('custom-restore-probe','captured')`)
	testutil.FailErr(t, "seed captured custom store", err)
	testbackup.Recovery(t, app.DB, cfg.DBPath)
	_, err = app.DB.ExecContext(t.Context(), `UPDATE store_meta SET value='changed' WHERE key='custom-restore-probe'`)
	testutil.FailErr(t, "change live custom store", err)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/backup/restore/recovery-snapshot", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	app.Server.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	testutil.FailErr(t, "close before restore restart", app.Close())
	app, err = Build(t.Context(), cfg)
	testutil.FailErr(t, "restart configured custom store", err)
	var restored string
	testutil.FailErr(t, "read restored custom store", app.DB.QueryRowContext(t.Context(), `SELECT value FROM store_meta WHERE key='custom-restore-probe'`).Scan(&restored))
	if restored != "captured" {
		t.Fatalf("custom store restored=%q", restored)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(cfg.DBPath), "store.db")); !os.IsNotExist(err) {
		t.Fatalf("restore wrote the wrong store filename: %v", err)
	}

}
