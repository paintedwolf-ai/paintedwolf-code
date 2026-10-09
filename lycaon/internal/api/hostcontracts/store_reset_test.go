package hostcontracts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestRecoveryServerStagesFreshStoreReset(t *testing.T) {
	configDir := t.TempDir()
	dbPath := filepath.Join(configDir, "store.db")
	refused := []byte("store from another schema")
	testutil.FailErr(t, "write refused store", os.WriteFile(dbPath, refused, 0o600))
	srv := hostapi.NewRecoveryServer(context.Background(), hostapi.RecoveryServerOpts{
		APIToken: hostapi.TestAPIToken,
		DBPath:   dbPath,
		DataDir:  configDir,
		Incompatible: &db.StoreIncompatibleError{
			Reason:             db.RecoveryReasonSchemaMismatch,
			StoreSchemaVersion: 5,
			Detail:             "schema mismatch",
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/store/reset", nil)
	req.Header.Set("Authorization", "Bearer "+hostapi.TestAPIToken)
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("reset status=%d body=%s", response.Code, response.Body.String())
	}
	var result wire.BackupRestoreResult
	testutil.FailErr(t, "decode reset response", json.Unmarshal(response.Body.Bytes(), &result))
	if !result.RestartRequired || result.RecoveryCopyPath == "" {
		t.Fatalf("reset result=%+v", result)
	}
	recovered, err := os.ReadFile(filepath.Join(result.RecoveryCopyPath, "store.db"))
	testutil.FailErr(t, "read recovery copy", err)
	if string(recovered) != string(refused) {
		t.Fatalf("recovery copy=%q want %q", recovered, refused)
	}

	testutil.FailErr(t, "apply reset", backup.ApplyPending(configDir))
	fresh := testdbfixture.OpenPath(t, dbPath)
	version, err := db.ReadUserVersion(context.Background(), fresh)
	testutil.FailErr(t, "read fresh schema", err)
	if version != db.SchemaVersion {
		t.Fatalf("schema version=%d want %d", version, db.SchemaVersion)
	}
}

func TestRecoveryServerRejectsSubsequentResetWhilePending(t *testing.T) {
	configDir := t.TempDir()
	dbPath := filepath.Join(configDir, "store.db")
	refused := []byte("store from another schema")
	testutil.FailErr(t, "write refused store", os.WriteFile(dbPath, refused, 0o600))
	srv := hostapi.NewRecoveryServer(context.Background(), hostapi.RecoveryServerOpts{
		APIToken: hostapi.TestAPIToken,
		DBPath:   dbPath,
		DataDir:  configDir,
		Incompatible: &db.StoreIncompatibleError{
			Reason:             db.RecoveryReasonSchemaMismatch,
			StoreSchemaVersion: 5,
			Detail:             "schema mismatch",
		},
	})

	req1 := httptest.NewRequest(http.MethodPost, "/v1/store/reset", nil)
	req1.Header.Set("Authorization", "Bearer "+hostapi.TestAPIToken)
	res1 := httptest.NewRecorder()
	srv.ServeHTTP(res1, req1)
	if res1.Code != http.StatusOK {
		t.Fatalf("first reset status=%d body=%s", res1.Code, res1.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodPost, "/v1/store/reset", nil)
	req2.Header.Set("Authorization", "Bearer "+hostapi.TestAPIToken)
	res2 := httptest.NewRecorder()
	srv.ServeHTTP(res2, req2)
	if res2.Code != http.StatusConflict {
		t.Fatalf("second reset status=%d want %d body=%s", res2.Code, http.StatusConflict, res2.Body.String())
	}
	var errResp map[string]any
	testutil.FailErr(t, "decode error response", json.Unmarshal(res2.Body.Bytes(), &errResp))
	if errResp["code"] != "backup_restore_pending" {
		t.Fatalf("error code=%v want backup_restore_pending", errResp["code"])
	}

	srv2 := hostapi.NewRecoveryServer(context.Background(), hostapi.RecoveryServerOpts{
		APIToken: hostapi.TestAPIToken,
		DBPath:   dbPath,
		DataDir:  configDir,
		Incompatible: &db.StoreIncompatibleError{
			Reason:             db.RecoveryReasonSchemaMismatch,
			StoreSchemaVersion: 5,
			Detail:             "schema mismatch",
		},
	})
	req3 := httptest.NewRequest(http.MethodPost, "/v1/store/reset", nil)
	req3.Header.Set("Authorization", "Bearer "+hostapi.TestAPIToken)
	res3 := httptest.NewRecorder()
	srv2.ServeHTTP(res3, req3)
	if res3.Code != http.StatusConflict {
		t.Fatalf("reset on booted recovery server status=%d want %d body=%s", res3.Code, http.StatusConflict, res3.Body.String())
	}
}
