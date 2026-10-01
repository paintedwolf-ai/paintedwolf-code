package llm

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Trust installed by an approval is withdrawn only by that approval's
// operation; trust a person set in Settings survives every operation rollback.
func TestProviderSecretTrustWithdrawalHonorsProvenance(t *testing.T) {
	catalog, registry, _ := trustTestRegistry(t)
	svc := &Service{Catalog: catalog, Registry: registry}
	ctx := context.Background()
	entry, _ := catalog.Get("ollama")
	destination := entry.SecretDestinationID()

	changed, err := svc.TrustProviderForSecrets(ctx, "ollama", destination, "checkpoint-1")
	testutil.FailErr(t, "trust by approval", err)
	if !changed {
		t.Fatal("first trust must change state")
	}
	if entry, _ = catalog.Get("ollama"); !entry.SecretScreenTrusted() || entry.SecretScreenTrustOperation != "checkpoint-1" {
		t.Fatalf("trusted entry = %+v, want trust attributed to checkpoint-1", entry)
	}
	changed, err = svc.WithdrawProviderSecretTrust(ctx, "ollama", destination, "checkpoint-other")
	testutil.FailErr(t, "foreign withdrawal", err)
	if changed {
		t.Fatal("another operation must not withdraw this approval's trust")
	}
	if entry, _ = catalog.Get("ollama"); !entry.SecretScreenTrusted() {
		t.Fatal("foreign withdrawal removed the trust")
	}
	changed, err = svc.WithdrawProviderSecretTrust(ctx, "ollama", destination, "checkpoint-1")
	testutil.FailErr(t, "own withdrawal", err)
	if !changed {
		t.Fatal("the installing operation must be able to withdraw its trust")
	}
	if entry, _ = catalog.Get("ollama"); entry.SecretScreenTrusted() || entry.SecretScreenTrustOperation != "" {
		t.Fatalf("withdrawn entry = %+v", entry)
	}

	// A person's Settings decision carries no operation and outlives any rollback.
	changed, err = svc.TrustProviderForSecrets(ctx, "ollama", destination, "")
	testutil.FailErr(t, "trust by person", err)
	if !changed {
		t.Fatal("person trust must change state")
	}
	if changed, err = svc.TrustProviderForSecrets(ctx, "ollama", destination, "checkpoint-2"); err != nil || changed {
		t.Fatalf("re-trusting an already trusted provider = changed %v err %v", changed, err)
	}
	if entry, _ = catalog.Get("ollama"); entry.SecretScreenTrustOperation != "" {
		t.Fatalf("a later approval must not claim the person's trust: %+v", entry)
	}
	changed, err = svc.WithdrawProviderSecretTrust(ctx, "ollama", destination, "checkpoint-2")
	testutil.FailErr(t, "rollback against person trust", err)
	if changed {
		t.Fatal("an approval rollback must not withdraw trust the person set")
	}
	changed, err = svc.WithdrawProviderSecretTrust(ctx, "ollama", destination, "")
	testutil.FailErr(t, "person withdrawal", err)
	if !changed {
		t.Fatal("the person can always withdraw trust")
	}
	if entry, _ = catalog.Get("ollama"); entry.SecretScreenTrusted() {
		t.Fatal("person withdrawal left the trust in place")
	}
}
