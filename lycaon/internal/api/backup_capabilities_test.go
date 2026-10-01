package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestBackupCapabilitiesRemainAuthenticatedDuringRecovery(t *testing.T) {
	root := t.TempDir()
	server := NewRecoveryServer(t.Context(), RecoveryServerOpts{DataDir: root, DBPath: filepath.Join(root, "store.db"), APIToken: "fixture-token"})
	for _, authenticated := range []bool{false, true} {
		request := httptest.NewRequest(http.MethodGet, "/v1/backup/capabilities", nil)
		if authenticated {
			request.Header.Set("Authorization", "Bearer fixture-token")
		}
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if !authenticated {
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated status = %d", response.Code)
			}
			continue
		}
		if response.Code != http.StatusOK {
			t.Fatalf("capabilities status = %d: %s", response.Code, response.Body)
		}
		var got wire.BackupCapabilities
		testutil.FailErr(t, "decode capabilities", json.Unmarshal(response.Body.Bytes(), &got))
		if got.MaxArchiveBytes != backup.MaxArchiveBytes || got.MaxExpandedBytes != int64(backup.MaxExpandedArchiveBytes) || got.FormatVersion != backup.FormatVersion {
			t.Fatalf("capabilities differ from engine limits: %+v", got)
		}
	}
}
