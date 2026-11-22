package contract

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/settings"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func mustApprovalStore(t *testing.T) *settings.ApprovalStore {
	t.Helper()
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	contractcheck.FailErr(t, "NewApprovalStoreAt", err)
	return store
}
