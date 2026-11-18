package policytest

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/testutil"
)

// New persists a model policy in an isolated store.
func New(t testing.TB, policy llm.ModelPolicy) *llm.PolicyStore {
	t.Helper()
	store, err := llm.NewPolicyStoreAt(filepath.Join(t.TempDir(), "model-policy.yaml"))
	testutil.FailErr(t, "open model policy", err)
	testutil.FailErr(t, "persist model policy", store.PutGlobal(policy))
	return store
}
