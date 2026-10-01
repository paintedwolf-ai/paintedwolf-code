package main

import (
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCredentialsPurgeRequiresConfirmationAndClearsEveryNamespace(t *testing.T) {
	t.Setenv(configdir.EnvConfigDir, t.TempDir())
	slots, err := allCredentialSlots()
	testutil.FailErr(t, "resolve credential slots", err)

	for _, slot := range slots {
		store, openErr := credentialstore.Open(slot, nil)
		testutil.FailErr(t, "open "+slot.Context, openErr)
		testutil.FailErr(t, "seed "+slot.Context, store.Set("fixture", "secret"))
	}

	if err := runCredentials([]string{"purge"}); err == nil {
		t.Fatal("purge without --yes succeeded")
	}
	for _, slot := range slots {
		store, openErr := credentialstore.Open(slot, nil)
		testutil.FailErr(t, "reopen after refusal "+slot.Context, openErr)
		if _, ok := store.Get("fixture"); !ok {
			t.Fatalf("refused purge removed %s credentials", slot.Context)
		}
	}

	testutil.FailErr(t, "confirmed purge", runCredentials([]string{"purge", "--yes"}))
	for _, slot := range slots {
		store, openErr := credentialstore.Open(slot, nil)
		testutil.FailErr(t, "reopen after purge "+slot.Context, openErr)
		if _, ok := store.Get("fixture"); ok {
			t.Fatalf("confirmed purge left %s credentials", slot.Context)
		}
	}
}
