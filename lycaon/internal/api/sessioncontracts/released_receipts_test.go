package sessioncontracts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

const releasedCorpus = "../../../testdata/upgrade-corpus"

// Receipts written by every shipped release keep rendering through the
// invocation recorder that serves the session API after the store upgrades to
// the current schema. TestListSessionInvocations covers the route itself.
func TestReleasedInvocationReceiptsRenderAfterUpgrade(t *testing.T) {
	entries, err := os.ReadDir(releasedCorpus)
	testutil.FailErr(t, "list released corpus", err)
	versions := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		versions++
		version := entry.Name()
		t.Run(version, func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(filepath.Join(releasedCorpus, version, "store.db"))
			testutil.FailErr(t, "read released store", err)
			path := filepath.Join(t.TempDir(), "store.db")
			testutil.FailErr(t, "copy released store", os.WriteFile(path, raw, 0o600))
			testutil.FailErr(t, "upgrade released store", db.UpgradeStaged(t.Context(), path))
			upgraded := testdbfixture.OpenPath(t, path)

			var sessionID, receiptID, toolName, status string
			testutil.FailErr(t, "read released receipt", upgraded.QueryRowContext(t.Context(), `SELECT session_id, id, tool_name, status FROM invocation_receipts ORDER BY started_at LIMIT 1`).Scan(&sessionID, &receiptID, &toolName, &status))
			_, err = store.NewSQL(upgraded).Get(t.Context(), sessionID)
			testutil.FailErr(t, "read released session", err)
			list, err := invocation.NewSQLRecorder(upgraded).ListSessionPage(t.Context(), sessionID, "", 100)
			testutil.FailErr(t, "list released receipts", err)
			encoded, err := json.Marshal(list)
			testutil.FailErr(t, "encode receipts", err)
			var body wire.InvocationReceiptList
			testutil.FailErr(t, "decode receipts", json.Unmarshal(encoded, &body))
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
	if versions == 0 {
		t.Fatal("no released store in the corpus")
	}
}
