package credentialstoretest

import (
	"testing"

	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/testutil"
)

func Open(t testing.TB, slot credentialstore.Slot, validate credentialstore.Validator) *credentialstore.Store {
	t.Helper()
	store, err := credentialstore.Open(slot, validate)
	testutil.FailErr(t, "open credential fixture", err)
	return store
}
