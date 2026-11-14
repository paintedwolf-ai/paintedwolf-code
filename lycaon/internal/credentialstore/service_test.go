package credentialstore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// readbackFailureProvider serves the first custodian identity load and refuses
// every later one, standing in for a custodian that loses the identity between
// a vault write and its durable readback.
type readbackFailureProvider struct {
	inner identityProvider
	loads int
}

func (p *readbackFailureProvider) LoadOrCreate(vaultExists bool) (identityDocument, error) {
	p.loads++
	if p.loads > 1 {
		return identityDocument{}, errors.New("custodian refused the durable readback")
	}
	return p.inner.LoadOrCreate(vaultExists)
}

func (p *readbackFailureProvider) Purge() error { return p.inner.Purge() }

func (p *readbackFailureProvider) Describe() string { return p.inner.Describe() }

func (p *readbackFailureProvider) custodyKey() providerCustodyKey { return p.inner.custodyKey() }

func TestLoadOrInitInitializesOnceAndThenServesTheVerifiedCredential(t *testing.T) {
	slot := Slot{Path: filepath.Join(t.TempDir(), VaultBasename), Namespace: "daemon-token", Context: "test"}
	var verified []string
	first, err := LoadOrInit(slot, "bearer", nil,
		func() (string, error) { return "generated-token", nil },
		func() error { verified = append(verified, "initialized"); return nil })
	testutil.FailErr(t, "initialize credential", err)
	if first != "generated-token" || len(verified) != 1 {
		t.Fatalf("first load = %q, verified = %v", first, verified)
	}

	second, err := LoadOrInit(slot, "bearer",
		func(secret string) error {
			if secret != "generated-token" {
				return errors.New("unexpected stored credential")
			}
			return nil
		},
		func() (string, error) { return "", errors.New("init ran for a stored credential") },
		func() error { verified = append(verified, "existing"); return nil })
	testutil.FailErr(t, "load stored credential", err)
	if second != "generated-token" || len(verified) != 2 || verified[1] != "existing" {
		t.Fatalf("second load = %q, verified = %v", second, verified)
	}
}

func TestLoadOrInitAndVerifyDurableRejectIncompleteSlots(t *testing.T) {
	path := filepath.Join(t.TempDir(), VaultBasename)
	init := func() (string, error) { return "secret", nil }
	for _, slot := range []Slot{
		{Namespace: "ns", Context: "ctx"},
		{Path: path, Context: "ctx"},
	} {
		if _, err := LoadOrInit(slot, "id", nil, init, nil); err == nil {
			t.Fatalf("LoadOrInit(%+v) accepted an incomplete slot", slot)
		}
		if err := VerifyDurable(slot, "id", "secret"); err == nil {
			t.Fatalf("VerifyDurable(%+v) accepted an incomplete slot", slot)
		}
	}
	slot := Slot{Path: path, Namespace: "ns", Context: "ctx"}
	if _, err := LoadOrInit(slot, "   ", nil, init, nil); err == nil {
		t.Fatal("LoadOrInit accepted a blank credential id")
	}
	if err := VerifyDurable(slot, "", "secret"); err == nil {
		t.Fatal("VerifyDurable accepted a blank credential id")
	}
}

func TestLoadOrInitPropagatesValidationAndCallbackFailures(t *testing.T) {
	refusal := errors.New("refused")

	fresh := Slot{Path: filepath.Join(t.TempDir(), VaultBasename), Namespace: "ns", Context: "ctx"}
	if _, err := LoadOrInit(fresh, "id", nil, func() (string, error) { return "", refusal }, nil); !errors.Is(err, refusal) {
		t.Fatalf("init failure = %v", err)
	}
	if _, err := LoadOrInit(fresh, "id", func(string) error { return refusal },
		func() (string, error) { return "secret", nil }, nil); !errors.Is(err, refusal) {
		t.Fatalf("generated credential validation failure = %v", err)
	}
	if _, err := LoadOrInit(fresh, "id", nil, func() (string, error) { return "secret", nil },
		func() error { return refusal }); !errors.Is(err, refusal) {
		t.Fatalf("post-verification callback failure = %v", err)
	}

	stored := Slot{Path: filepath.Join(t.TempDir(), VaultBasename), Namespace: "ns", Context: "ctx"}
	_, err := LoadOrInit(stored, "id", nil, func() (string, error) { return "stored-secret", nil }, nil)
	testutil.FailErr(t, "store credential", err)
	if _, err := LoadOrInit(stored, "id", func(string) error { return refusal }, nil, nil); !errors.Is(err, refusal) {
		t.Fatalf("stored credential validation failure = %v", err)
	}
	if _, err := LoadOrInit(stored, "id", nil, nil, func() error { return refusal }); !errors.Is(err, refusal) {
		t.Fatalf("stored credential callback failure = %v", err)
	}
	if _, err := LoadOrInit(stored, "id", nil,
		func() (string, error) { return "", errors.New("init ran for a stored credential") }, nil); err != nil {
		t.Fatalf("stored credential stopped serving after refused callbacks: %v", err)
	}
}

func TestVerifyDurableDetectsMissingAndAlteredCredentials(t *testing.T) {
	slot := Slot{Path: filepath.Join(t.TempDir(), VaultBasename), Namespace: "ns", Context: "ctx"}
	if err := VerifyDurable(slot, "id", "secret"); err == nil {
		t.Fatal("VerifyDurable accepted a credential from a missing vault")
	}
	_, err := LoadOrInit(slot, "id", nil, func() (string, error) { return "secret", nil }, nil)
	testutil.FailErr(t, "store credential", err)
	if err := VerifyDurable(slot, "id", "altered"); err == nil {
		t.Fatal("VerifyDurable accepted an altered credential")
	}
	testutil.FailErr(t, "verify stored credential", VerifyDurable(slot, "id", "secret"))
}

func TestLoadOrInitRefusesAVaultPathHeldByADirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), VaultBasename)
	testutil.FailErr(t, "occupy the vault path with a directory", os.MkdirAll(path, 0o700))
	slot := Slot{Path: path, Namespace: "ns", Context: "ctx"}
	if _, err := LoadOrInit(slot, "id", nil, func() (string, error) { return "secret", nil }, nil); err == nil {
		t.Fatal("LoadOrInit ignored a vault path held by a directory")
	}
}

// The loadOrInit branches these tests cannot reach are the rollback removal
// failure and the joined error when the restore commit itself fails: both
// need a filesystem fault between two operations inside one locked section,
// and the vault exposes no fault seam for that.
func TestLoadOrInitReportsDurableFailuresWithoutMutatingTheVault(t *testing.T) {
	t.Run("existing credential fails durable re-verification", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), VaultBasename)
		good := newDevelopmentIdentityProvider(path)
		seed := &vaultFile{path: path, provider: good}
		doc, err := seed.read()
		testutil.FailErr(t, "create empty vault", err)
		doc.Namespaces["ns"] = map[string]string{"id": "stored"}
		testutil.FailErr(t, "seed vault", seed.commit(doc))

		provider := &readbackFailureProvider{inner: good}
		vault := &vaultFile{path: path, provider: provider}
		if _, err := vault.loadOrInit(provider, "ns", "id", nil,
			func() (string, error) { return "", errors.New("init ran for a stored credential") }, nil); err == nil ||
			!strings.Contains(err.Error(), "durable readback") {
			t.Fatalf("durable re-verification failure = %v", err)
		}
		after, err := (&vaultFile{path: path, provider: good}).read()
		testutil.FailErr(t, "reload vault", err)
		if kept := after.Namespaces["ns"]; len(kept) != 1 || kept["id"] != "stored" {
			t.Fatalf("stored credential changed: %+v", after.Namespaces)
		}
	})

	t.Run("custodian read fails before the vault is written", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), VaultBasename)
		provider := unavailableIdentityProvider{refusal: ErrCustodianUnavailable}
		vault := &vaultFile{path: path, provider: provider}
		if _, err := vault.loadOrInit(provider, "ns", "id", nil,
			func() (string, error) { return "secret", nil }, nil); !errors.Is(err, ErrCustodianUnavailable) {
			t.Fatalf("custodian failure = %v", err)
		}
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("vault was written despite the custodian failure: %v", statErr)
		}
	})
}

func TestLoadOrInitRollsBackWhenDurableReadbackFails(t *testing.T) {
	t.Run("removes a vault that never verified", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), VaultBasename)
		provider := &readbackFailureProvider{inner: newDevelopmentIdentityProvider(path)}
		vault := &vaultFile{path: path, provider: provider}
		_, err := vault.loadOrInit(provider, "ns", "id", nil,
			func() (string, error) { return "secret", nil }, nil)
		if err == nil || !strings.Contains(err.Error(), "durable readback") {
			t.Fatalf("durable readback failure = %v", err)
		}
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("unverified vault remained on disk: %v", statErr)
		}
	})

	t.Run("restores a vault that already held other namespaces", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), VaultBasename)
		good := newDevelopmentIdentityProvider(path)
		seed := &vaultFile{path: path, provider: good}
		doc, err := seed.read()
		testutil.FailErr(t, "create empty vault", err)
		doc.Namespaces["keep"] = map[string]string{"anchor": "kept"}
		testutil.FailErr(t, "seed vault", seed.commit(doc))

		provider := &readbackFailureProvider{inner: good}
		vault := &vaultFile{path: path, provider: provider}
		if _, err := vault.loadOrInit(provider, "added", "id", nil,
			func() (string, error) { return "secret", nil }, nil); err == nil {
			t.Fatal("durable readback failure was ignored")
		}
		after, err := (&vaultFile{path: path, provider: good}).read()
		testutil.FailErr(t, "reload vault", err)
		if kept := after.Namespaces["keep"]; len(kept) != 1 || kept["anchor"] != "kept" {
			t.Fatalf("seeded namespace changed: %+v", after.Namespaces)
		}
		if _, damaged := after.Namespaces["added"]; damaged {
			t.Fatal("unverified credential remained after rollback")
		}
	})

	t.Run("keeps sibling credentials in the same namespace", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), VaultBasename)
		good := newDevelopmentIdentityProvider(path)
		seed := &vaultFile{path: path, provider: good}
		doc, err := seed.read()
		testutil.FailErr(t, "create empty vault", err)
		doc.Namespaces["shared"] = map[string]string{"anchor": "kept"}
		testutil.FailErr(t, "seed vault", seed.commit(doc))

		provider := &readbackFailureProvider{inner: good}
		vault := &vaultFile{path: path, provider: provider}
		if _, err := vault.loadOrInit(provider, "shared", "id", nil,
			func() (string, error) { return "secret", nil }, nil); err == nil {
			t.Fatal("durable readback failure was ignored")
		}
		after, err := (&vaultFile{path: path, provider: good}).read()
		testutil.FailErr(t, "reload vault", err)
		if kept := after.Namespaces["shared"]; len(kept) != 1 || kept["anchor"] != "kept" {
			t.Fatalf("sibling credential changed: %+v", after.Namespaces)
		}
	})
}
