package llm

import (
	"github.com/lycaon/lycaon/internal/testutil"
	"path/filepath"
	"testing"
)

func newPolicyFixture(t *testing.T, policy ModelPolicy) *PolicyStore {
	t.Helper()
	store, err := NewPolicyStoreAt(filepath.Join(t.TempDir(), "model-policy.yaml"))
	testutil.FailErr(t, "open model policy", err)
	testutil.FailErr(t, "persist model policy", store.PutGlobal(policy))
	return store
}
