package hostidentity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLoadOrCreateIsStable(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadOrCreate(dir)
	testutil.FailErr(t, "create identity", err)
	second, err := LoadOrCreate(dir)
	testutil.FailErr(t, "reload identity", err)
	if first.HostID != second.HostID || !first.PublicKey.Equal(second.PublicKey) {
		t.Fatalf("identity changed across loads: %+v then %+v", first, second)
	}
	if len(first.PublicKey) != ed25519.PublicKeySize {
		t.Fatalf("public key length = %d", len(first.PublicKey))
	}
	if first.HostID != DeriveHostID(first.PublicKey) {
		t.Fatalf("host id %s is not derived from the public key", first.HostID)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("standalone %s must not exist on disk: err=%v", FileName, err)
	}
	if _, err := os.Stat(credentialstore.DefaultVaultPath(dir)); err != nil {
		t.Fatalf("expected %s to exist: %v", credentialstore.VaultBasename, err)
	}
}

func TestHostIDIsVersion8UUID(t *testing.T) {
	public, _, err := ed25519.GenerateKey(nil)
	testutil.FailErr(t, "generate key", err)
	parsed, err := uuid.Parse(DeriveHostID(public))
	testutil.FailErr(t, "parse host id", err)
	if parsed.Version() != 8 || parsed.Variant() != uuid.RFC4122 {
		t.Fatalf("host id version=%d variant=%v", parsed.Version(), parsed.Variant())
	}
}

func TestKeyFileIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("profile ACLs govern the config directory on Windows")
	}
	dir := t.TempDir()
	_, err := LoadOrCreate(dir)
	testutil.FailErr(t, "create identity", err)
	vaultPath := credentialstore.DefaultVaultPath(dir)
	info, err := os.Stat(vaultPath)
	testutil.FailErr(t, "stat vault", err)
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("vault mode = %o, want 600", mode)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("standalone %s should not be created: %v", FileName, err)
	}
}

func TestCorruptKeyIsNeverReplaced(t *testing.T) {
	t.Run("corrupt legacy pem", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, FileName)
		testutil.FailErr(t, "write corrupt key", os.WriteFile(path, []byte("not a key"), 0o600))
		if _, err := LoadOrCreate(dir); err == nil {
			t.Fatal("corrupt key loaded")
		}
		data, err := os.ReadFile(path)
		testutil.FailErr(t, "reread key", err)
		if string(data) != "not a key" {
			t.Fatal("corrupt key was overwritten")
		}
		if _, err := os.Stat(credentialstore.DefaultVaultPath(dir)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("corrupt legacy key must not initialize vault: %v", err)
		}
	})

	t.Run("corrupt vault entry", func(t *testing.T) {
		dir := t.TempDir()
		slot := SlotForDir(dir)
		store, err := credentialstore.Open(slot, nil)
		testutil.FailErr(t, "open vault", err)
		testutil.FailErr(t, "seed corrupt vault entry", store.Set(hostIdentitySlotID, "not a pem block"))
		if _, err := LoadOrCreate(dir); err == nil {
			t.Fatal("corrupt vault entry loaded")
		}
		reloaded, err := credentialstore.Open(slot, nil)
		testutil.FailErr(t, "reopen vault", err)
		got, ok := reloaded.Get(hostIdentitySlotID)
		if !ok || got.Value() != "not a pem block" {
			t.Fatalf("corrupt vault entry was replaced: ok=%v", ok)
		}
	})

	t.Run("corrupt vault ciphertext", func(t *testing.T) {
		dir := t.TempDir()
		vaultPath := credentialstore.DefaultVaultPath(dir)
		testutil.FailErr(t, "write corrupt vault", os.WriteFile(vaultPath, []byte("corrupt vault bytes"), 0o600))
		if _, err := LoadOrCreate(dir); err == nil {
			t.Fatal("corrupt vault loaded")
		}
	})
}

func TestLegacyHostIdentityIsImportedAndRemoved(t *testing.T) {
	dir := t.TempDir()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	testutil.FailErr(t, "generate legacy key", err)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	testutil.FailErr(t, "marshal pkcs8", err)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: pemBlockType, Bytes: der})
	legacyPath := filepath.Join(dir, FileName)
	testutil.FailErr(t, "write legacy pem", os.WriteFile(legacyPath, pemBytes, 0o600))

	want := Identity{HostID: DeriveHostID(pub), PublicKey: pub}
	got, err := LoadOrCreate(dir)
	testutil.FailErr(t, "import legacy identity", err)
	if got.HostID != want.HostID || got.EncodedPublicKey() != want.EncodedPublicKey() {
		t.Fatalf("imported identity = %+v, want %+v", got, want)
	}
	if _, err := os.Stat(legacyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy %s still exists after verified import: %v", FileName, err)
	}
	testutil.FailErr(t, "verify durable vault record",
		credentialstore.VerifyDurable(SlotForDir(dir), hostIdentitySlotID, string(pemBytes)))

	reloaded, err := LoadOrCreate(dir)
	testutil.FailErr(t, "reload imported identity", err)
	if reloaded.HostID != want.HostID || reloaded.EncodedPublicKey() != want.EncodedPublicKey() {
		t.Fatalf("reloaded identity = %+v, want %+v", reloaded, want)
	}
}

func TestLegacyHostIdentityPreservedWhenVaultFails(t *testing.T) {
	dir := t.TempDir()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	testutil.FailErr(t, "generate legacy key", err)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	testutil.FailErr(t, "marshal pkcs8", err)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: pemBlockType, Bytes: der})
	legacyPath := filepath.Join(dir, FileName)
	testutil.FailErr(t, "write legacy pem", os.WriteFile(legacyPath, pemBytes, 0o600))

	// Block the vault path by creating a directory where credential-vault.age lives.
	vaultPath := credentialstore.DefaultVaultPath(dir)
	testutil.FailErr(t, "block vault path", os.MkdirAll(vaultPath, 0o700))

	if _, err := LoadOrCreate(dir); err == nil {
		t.Fatal("expected error when vault write is blocked")
	}
	preserved, err := os.ReadFile(legacyPath)
	testutil.FailErr(t, "read preserved legacy pem", err)
	if string(preserved) != string(pemBytes) {
		t.Fatal("legacy host-identity.pem was altered on failed migration")
	}
}

func TestLegacyHostIdentityUnlinkErrorIsSurfaced(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadOrCreate(dir)
	testutil.FailErr(t, "create vault identity", err)

	// Place a non-empty directory at host-identity.pem so os.Remove fails with non-ErrNotExist.
	legacyPath := filepath.Join(dir, FileName)
	testutil.FailErr(t, "mkdir at legacy path", os.MkdirAll(filepath.Join(legacyPath, "child"), 0o700))
	if _, err := LoadOrCreate(dir); err == nil {
		t.Fatal("expected non-ErrNotExist unlink failure to be surfaced")
	}
}

func TestConcurrentLoadOrCreateConverges(t *testing.T) {
	dir := t.TempDir()
	const workers = 16
	var wg sync.WaitGroup
	results := make([]Identity, workers)
	errs := make([]error, workers)
	start := make(chan struct{})
	for i := range workers {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			results[idx], errs[idx] = LoadOrCreate(dir)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		testutil.FailErr(t, "concurrent LoadOrCreate", err)
		if results[i].HostID != results[0].HostID || !results[i].PublicKey.Equal(results[0].PublicKey) {
			t.Fatalf("worker %d got %+v, worker 0 got %+v", i, results[i], results[0])
		}
	}
}
