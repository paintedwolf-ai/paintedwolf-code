package credentialstore

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

type fakeBackend struct {
	items     map[string]string
	failWrite bool
}

func newFakeBackend() *fakeBackend      { return &fakeBackend{items: map[string]string{}} }
func (f *fakeBackend) Describe() string { return "fake" }
func (f *fakeBackend) Snapshot() (Snapshot, error) {
	return Snapshot{Values: cloneValues(f.items)}, nil
}
func (f *fakeBackend) Set(id, secret string) error {
	if f.failWrite {
		return errors.New("commit refused")
	}
	f.items[id] = secret
	return nil
}
func (f *fakeBackend) Delete(id string) error {
	if f.failWrite {
		return errors.New("delete refused")
	}
	delete(f.items, id)
	return nil
}

func TestStoreRoundTripsThroughAnyBackend(t *testing.T) {
	backend := newFakeBackend()
	store, err := openWith(backend, "test", nil)
	testutil.FailErr(t, "open", err)
	testutil.FailErr(t, "set", store.Set("anthropic", "secret"))
	if got, ok := store.Get("anthropic"); !ok || got != "secret" {
		t.Fatalf("Get = %q, %v", got, ok)
	}
	testutil.FailErr(t, "delete", store.Delete("anthropic"))
}

func TestStoreFiltersLoadedValues(t *testing.T) {
	backend := newFakeBackend()
	backend.items = map[string]string{"known": "yes", "retired": "no"}
	store, err := openWith(backend, "test", func(id string) bool { return id == "known" })
	testutil.FailErr(t, "open", err)
	if _, ok := store.Get("known"); !ok {
		t.Fatal("accepted credential was not loaded")
	}
	if _, ok := store.Get("retired"); ok {
		t.Fatal("retired credential was loaded")
	}
}

func TestStoreRollsBackRefusedMutations(t *testing.T) {
	backend := newFakeBackend()
	backend.items["anthropic"] = "old"
	store, err := openWith(backend, "test", nil)
	testutil.FailErr(t, "open", err)
	backend.failWrite = true
	if err := store.Set("new", "secret"); err == nil {
		t.Fatal("Set reported success")
	}
	if _, ok := store.Get("new"); ok {
		t.Fatal("refused Set remained in memory")
	}
	if err := store.Delete("anthropic"); err == nil {
		t.Fatal("Delete reported success")
	}
	if got, ok := store.Get("anthropic"); !ok || got != "old" {
		t.Fatalf("refused Delete left %q, %v", got, ok)
	}
}

func TestEncryptedVaultDoesNotPersistValuesInPlaintext(t *testing.T) {
	path := filepath.Join(t.TempDir(), VaultBasename)
	backend := newDevelopmentBackend(path, NamespaceProviders)
	secret := "plaintext-must-not-survive"
	testutil.FailErr(t, "set", backend.Set("provider", secret))
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read ciphertext", err)
	if bytes.Contains(raw, []byte(secret)) || bytes.Contains(raw, []byte("provider")) {
		t.Fatal("encrypted vault exposed a credential or slot id")
	}
	if !bytes.HasPrefix(raw, []byte("age-encryption.org/v1")) {
		t.Fatal("vault is not an age v1 document")
	}
}

func TestVaultNamespacesShareOneCiphertextWithoutAliasing(t *testing.T) {
	path := filepath.Join(t.TempDir(), VaultBasename)
	provider := newDevelopmentIdentityProvider(path)
	providers := newNamespaceBackend(path, NamespaceProviders, provider)
	oauth := newNamespaceBackend(path, NamespaceMCPOAuth, provider)
	testutil.FailErr(t, "set provider", providers.Set("same", "provider-secret"))
	testutil.FailErr(t, "set oauth", oauth.Set("same", "oauth-secret"))
	if got := snapshotValue(t, providers, "same"); got != "provider-secret" {
		t.Fatalf("provider value = %q", got)
	}
	if got := snapshotValue(t, oauth, "same"); got != "oauth-secret" {
		t.Fatalf("oauth value = %q", got)
	}
}

func TestVaultSerializesConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), VaultBasename)
	left := newDevelopmentBackend(path, "shared")
	right := newDevelopmentBackend(path, "shared")
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := range 20 {
		for prefix, backend := range map[string]Backend{"left": left, "right": right} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- backend.Set(fmt.Sprintf("%s-%02d", prefix, i), fmt.Sprintf("value-%02d", i))
			}()
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		testutil.FailErr(t, "concurrent set", err)
	}
	final, err := left.Snapshot()
	testutil.FailErr(t, "snapshot", err)
	if len(final.Values) != 40 {
		t.Fatalf("stored credentials = %d", len(final.Values))
	}
}

func TestPurgeVaultRemovesEveryNamespaceAndIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), VaultBasename)
	first := newDevelopmentBackend(path, "first")
	second := newDevelopmentBackend(path, "second")
	testutil.FailErr(t, "set first", first.Set("a", "secret-a"))
	testutil.FailErr(t, "set second", second.Set("b", "secret-b"))
	testutil.FailErr(t, "purge", PurgeVault(path))
	for _, removed := range []string{path, developmentIdentityPath(path)} {
		if _, err := os.Stat(removed); !os.IsNotExist(err) {
			t.Fatalf("%s still exists", removed)
		}
	}
}

func TestDescribeNamesEncryptedVault(t *testing.T) {
	description := newDevelopmentBackend(filepath.Join(t.TempDir(), VaultBasename), "test").Describe()
	if !strings.Contains(description, "age-encrypted vault") {
		t.Fatalf("description = %q", description)
	}
}

func TestVaultRefusesGrowthBeyondReadableLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential-vault.age")
	slot := Slot{Path: path, Namespace: NamespaceCredentialFiles, Context: "evidence"}
	store := NewEmpty(slot, nil)
	testutil.FailErr(t, "seed vault", store.Set("project", "retained"))
	before, err := os.ReadFile(path)
	testutil.FailErr(t, "capture vault", err)
	if err := store.Set("project", strings.Repeat("x", MaxPlaintextBytes)); err == nil {
		t.Fatal("unreadable vault size was committed")
	}
	after, err := os.ReadFile(path)
	testutil.FailErr(t, "read preserved vault", err)
	if !bytes.Equal(before, after) {
		t.Fatal("size refusal mutated vault")
	}
	if value, _ := store.Get("project"); value.Value() != "retained" {
		t.Fatal("size refusal changed memory")
	}
}
