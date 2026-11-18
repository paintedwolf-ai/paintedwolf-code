package credentialstest

import (
	"testing"

	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/testutil"
)

func Open(t testing.TB, path string) *credentials.Store {
	t.Helper()
	store, err := credentials.Open(credentialstore.Slot{Path: path, Namespace: credentialstore.NamespaceProviders, Context: "provider fixture"})
	testutil.FailErr(t, "open provider credential fixture", err)
	return store
}
