package sessioncontracts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

const releasedCorpus = "../../../testdata/upgrade-corpus"

// Receipts written by every shipped release keep rendering through the
// session API after the store upgrades to the current schema.
func TestReleasedInvocationReceiptsRenderAfterUpgrade(t *testing.T) {
	entries, err := os.ReadDir(releasedCorpus)
	testutil.FailErr(t, "list released corpus", err)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		version := entry.Name()
		t.Run(version, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(releasedCorpus, version, "store.db"))
			testutil.FailErr(t, "read released store", err)
			path := filepath.Join(t.TempDir(), "store.db")
			testutil.FailErr(t, "copy released store", os.WriteFile(path, raw, 0o600))
			testutil.FailErr(t, "upgrade released store", db.UpgradeStaged(t.Context(), path))
			upgraded := testdbfixture.OpenPath(t, path)

			var sessionID, receiptID, toolName, status string
			testutil.FailErr(t, "read released receipt", upgraded.QueryRowContext(t.Context(), `SELECT session_id, id, tool_name, status FROM invocation_receipts ORDER BY started_at LIMIT 1`).Scan(&sessionID, &receiptID, &toolName, &status))

			srv := contractfixture.NewTestServerWithWorkflowRegistry(t, tools.NewStubRegistry(), func(d *hostapi.Dependencies) {
				d.Core.Database, d.Core.Store, d.Core.Projects = upgraded, store.NewSQL(upgraded), project.NewSQLRegistry(upgraded)
				d.Core.Invocations = invocation.NewSQLRecorder(upgraded)
			})
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, contractfixture.NewAuthedRequest(http.MethodGet, "/v1/sessions/"+sessionID+"/invocations", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
			}
			var body wire.InvocationReceiptList
			testutil.FailErr(t, "decode receipts", json.Unmarshal(rec.Body.Bytes(), &body))
			for _, receipt := range body.Invocations {
				if receipt.ID == receiptID {
					if receipt.Tool != toolName || string(receipt.Status) != status || receipt.Evidence.Ref == "" {
						t.Fatalf("released receipt rendered as %+v", receipt)
					}
					return
				}
			}
			t.Fatalf("released receipt %s missing from %+v", receiptID, body.Invocations)
		})
	}
}
